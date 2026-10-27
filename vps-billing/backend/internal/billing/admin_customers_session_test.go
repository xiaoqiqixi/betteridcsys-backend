package billing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type pausedAccountWrite struct {
	Store
	adminCustomerStore
	entered, release chan struct{}
}

func (p *pausedAccountWrite) WriteCustomerAccount(ctx context.Context, actor string, w accountWrite) (User, error) {
	close(p.entered)
	select {
	case <-p.release:
		return p.adminCustomerStore.WriteCustomerAccount(ctx, actor, w)
	case <-ctx.Done():
		return User{}, ctx.Err()
	}
}

type accountHTTPResult struct {
	status int
	code   string
	data   map[string]any
	err    error
}

// Use a real TLS socket and httptest's private client roots, never host trust or
// InsecureSkipVerify. Return errors to the test goroutine; no t.Fatal in workers.
func accountTLSClient(t *testing.T, store Store, email, password string) (*http.Client, string, string) {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	origin := "https://" + srv.Listener.Addr().String()
	srv.Config.Handler = NewApp(store, Config{Origin: origin, Secret: ID() + ID(), SecureCookie: true}).Handler()
	srv.StartTLS()
	t.Cleanup(srv.Close)
	c := srv.Client()
	c.Jar, _ = cookiejar.New(nil)
	c.Timeout = 15 * time.Second
	initial := accountHTTPS(c, origin, "GET", "/api/v1/session", "", "")
	if initial.err != nil || initial.status != 200 {
		t.Fatal("strict TLS session bootstrap failed", initial.status, initial.err)
	}
	csrf, _ := initial.data["csrf_token"].(string)
	raw, _ := json.Marshal(map[string]string{"email": email, "password": password})
	login := accountHTTPS(c, origin, "POST", "/api/v1/auth/login", csrf, string(raw))
	if login.err != nil || login.status != 200 {
		t.Fatal("strict TLS account login failed", login.status, login.code, login.err)
	}
	csrf, _ = login.data["csrf_token"].(string)
	return c, origin, csrf
}

func accountHTTPS(c *http.Client, origin, method, path, csrf, body string) accountHTTPResult {
	r, err := http.NewRequest(method, origin+path, strings.NewReader(body))
	if err != nil {
		return accountHTTPResult{err: err}
	}
	r.Header.Set("Origin", origin)
	r.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	res, err := c.Do(r)
	if err != nil {
		return accountHTTPResult{err: err}
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil {
		return accountHTTPResult{status: res.StatusCode, err: err}
	}
	if len(raw) > 1<<20 || strings.Split(strings.ToLower(res.Header.Get("Content-Type")), ";")[0] != "application/json" {
		return accountHTTPResult{status: res.StatusCode, err: errors.New("invalid bounded account response envelope")}
	}
	var out struct {
		Data  map[string]any `json:"data"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	err = json.Unmarshal(raw, &out)
	return accountHTTPResult{status: res.StatusCode, code: out.Error.Code, data: out.Data, err: err}
}

func TestAccountHTTPSRequiresCompleteJSONRejection(t *testing.T) {
	const payload = `{"error":{"code":"AUTH_REQUIRED"}}`
	for _, fault := range []string{"truncated", "trailing-garbage", "wrong-content-type", "oversized"} {
		t.Run(fault, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				body := payload
				switch fault {
				case "truncated":
					w.Header().Set("Content-Length", strconv.Itoa(len(payload)+32))
				case "trailing-garbage":
					body += "incomplete next message"
				case "wrong-content-type":
					w.Header().Set("Content-Type", "text/plain")
				case "oversized":
					body = strings.Repeat(" ", 1<<20) + payload
				}
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, body)
			}))
			defer srv.Close()
			result := accountHTTPS(srv.Client(), srv.URL, "POST", "/fault", "", "{}")
			if result.err == nil {
				t.Fatal("an incomplete/malformed rejection was accepted as a valid response", fault)
			}
		})
	}
}

func accountOperationInput(t *testing.T, f accountFixture, action string) (string, string) {
	t.Helper()
	path := "/api/v1/admin/customer-accounts"
	body := map[string]any{"reason": "Synthetic original-session regression"}
	switch action {
	case "create":
		body["name"], body["email"], body["role"], body["password"] = "Synthetic account", "session-fence@example.invalid", "customer", "SessionBoundary!2026"
	case "update":
		path += "/" + f.customer.ID
		body["revision"], body["name"], body["role"], body["disabled"] = f.customer.Revision, "Must not be applied", f.customer.Role, false
	case "password", "revoke":
		path += "/" + f.customer.ID
		body["revision"] = f.customer.Revision
		if action == "password" {
			path += "/password"
			body["password"] = "SessionBoundary!2026"
		} else {
			path += "/revoke-sessions"
		}
	default:
		t.Fatal("unknown fixed account test action")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return path, string(raw)
}

func TestAccountQueuedHTTPWriteCannotSurviveOriginalSessionLogout(t *testing.T) {
	for _, action := range []string{"create", "update", "password", "revoke"} {
		t.Run(action, func(t *testing.T) {
			f := memoryAccountFixture(t)
			pause := &pausedAccountWrite{Store: f.Store, adminCustomerStore: f.adminCustomerStore, entered: make(chan struct{}), release: make(chan struct{})}
			c, origin, csrf := accountTLSClient(t, pause, f.admin.Email, "DemoAdmin!2026")
			var release sync.Once
			defer release.Do(func() { close(pause.release) })
			path, body := accountOperationInput(t, f, action)
			done := make(chan accountHTTPResult, 1)
			go func() { done <- accountHTTPS(c, origin, "POST", path, csrf, body) }()
			select {
			case <-pause.entered:
			case result := <-done:
				t.Fatal("request failed before its authenticated write queue", result.status, result.code, result.err)
			case <-time.After(10 * time.Second):
				t.Fatal("request did not reach its authenticated write queue")
			}
			before, err := f.UserByID(f.ctx, f.customer.ID)
			if err != nil {
				t.Fatal(err)
			}
			targetSession := Session{Hash: ID() + ID(), UserID: f.customer.ID, Revision: before.Revision, Expires: time.Now().Add(time.Hour)}
			if err := f.PutSession(f.ctx, targetSession); err != nil {
				t.Fatal(err)
			}
			m := f.Store.(*Memory)
			m.mu.Lock()
			auditsBefore := len(m.audits)
			m.mu.Unlock()
			logout := accountHTTPS(c, origin, "POST", "/api/v1/auth/logout", csrf, "{}")
			if logout.err != nil || logout.status != 200 {
				t.Fatal("original-session logout was not confirmed", logout.status, logout.err)
			}
			release.Do(func() { close(pause.release) })
			select {
			case result := <-done:
				if result.err != nil || result.status != 401 || result.code != "AUTH_REQUIRED" {
					t.Fatal("queued account write must return complete AUTH_REQUIRED after logout", result.status, result.code, result.err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("queued account write deadlocked after release")
			}
			after, err := f.UserByID(f.ctx, f.customer.ID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("denied write modified the target account")
			}
			if _, err := f.Session(f.ctx, targetSession.Hash); err != nil {
				t.Fatal("denied write revoked a target session")
			}
			m.mu.Lock()
			auditsAfter := len(m.audits)
			m.mu.Unlock()
			if auditsAfter != auditsBefore {
				t.Fatal("denied write emitted a successful account audit")
			}
			if action == "create" {
				if _, err = f.UserByEmail(f.ctx, "session-fence@example.invalid"); err == nil {
					t.Fatal("denied write created an account")
				}
			}
		})
	}
}

func TestAccountStoreRequiresExactOriginalSessionBinding(t *testing.T) {
	for _, variant := range []string{"missing", "wrong-context-key", "wrong-actor", "short-hash", "upper-hash", "wrong-owner", "expired", "deleted", "same-owner-replacement", "cancelled"} {
		t.Run(variant, func(t *testing.T) {
			f := memoryAccountFixture(t)
			m := f.Store.(*Memory)
			hash := ID() + ID()
			s := Session{Hash: hash, UserID: f.admin.ID, Revision: f.admin.Revision, Expires: time.Now().Add(time.Hour)}
			if err := m.PutSession(f.ctx, s); err != nil {
				t.Fatal(err)
			}
			ctx := withAccountWriteSession(f.ctx, f.admin.ID, hash)
			switch variant {
			case "missing":
				ctx = f.ctx
			case "wrong-context-key":
				ctx = context.WithValue(f.ctx, struct{ fake bool }{true}, accountSessionBinding{f.admin.ID, hash})
			case "wrong-actor":
				ctx = withAccountWriteSession(f.ctx, f.customer.ID, hash)
			case "short-hash":
				ctx = withAccountWriteSession(f.ctx, f.admin.ID, hash[:32])
			case "upper-hash":
				ctx = withAccountWriteSession(f.ctx, f.admin.ID, strings.ToUpper(strings.Repeat("a", 64)))
			case "wrong-owner":
				s.UserID = f.otherAdmin.ID
				m.mu.Lock()
				m.sessions[hash] = s
				m.mu.Unlock()
			case "expired":
				s.Expires = time.Now().Add(-time.Second)
				m.mu.Lock()
				m.sessions[hash] = s
				m.mu.Unlock()
			case "deleted", "same-owner-replacement":
				if err := m.DeleteSession(f.ctx, hash); err != nil {
					t.Fatal(err)
				}
				if variant == "same-owner-replacement" {
					s.Hash = ID() + ID()
					if err := m.PutSession(f.ctx, s); err != nil {
						t.Fatal(err)
					}
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			_, err := m.WriteCustomerAccount(ctx, f.admin.ID, accountWrite{Operation: "update", ID: f.customer.ID, Revision: f.customer.Revision, Name: "Denied", Role: f.customer.Role, Reason: "Original session binding"})
			if variant == "cancelled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal("cancelled request was applied or misclassified", err)
				}
			} else if !errors.Is(err, ErrAccountSession) {
				t.Fatal("missing/replaced/mismatched session authorized a write", err)
			}
			after, _ := m.UserByID(f.ctx, f.customer.ID)
			if !reflect.DeepEqual(after, f.customer) {
				t.Fatal("rejected session changed its target")
			}
		})
	}
}

func TestAccountOrdinaryNameChangeRetainsIssuedSession(t *testing.T) {
	runAccountOrdinaryNameSession(t, memoryAccountFixture(t))
}
func TestMySQLAccountOrdinaryNameChangeRetainsIssuedSession(t *testing.T) {
	runAccountOrdinaryNameSession(t, mysqlAccountFixture(t))
}
func runAccountOrdinaryNameSession(t *testing.T, f accountFixture) {
	t.Helper()
	ctx := accountWriteTestContext(t, f.Store, f.ctx, f.admin.ID)
	hash, err := accountWriteSessionHash(ctx, f.admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteCustomerAccount(ctx, f.admin.ID, accountWrite{Operation: "update", ID: f.admin.ID, Revision: f.admin.Revision, Name: "Updated admin name", Role: "admin", Reason: "Name only"}); err != nil {
		t.Fatal("ordinary name update failed", err)
	}
	if _, err = f.Session(f.ctx, hash); err != nil {
		t.Fatal("ordinary name update killed the issued session", err)
	}
	if _, err = f.WriteCustomerAccount(ctx, f.admin.ID, accountWrite{Operation: "update", ID: f.customer.ID, Revision: f.customer.Revision, Name: "Updated customer name", Role: f.customer.Role, Reason: "Same original session"}); err != nil {
		t.Fatal("issued session incorrectly became a user-revision invalidation token", err)
	}
}
