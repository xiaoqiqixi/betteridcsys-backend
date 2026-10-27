package billing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// The pause is after login's password comparison but before persistence. Reset
// must win even when the old password was valid at the start of the request.
type accountLoginPause struct {
	Store
	target           string
	entered, release chan struct{}
}

func (s *accountLoginPause) PutSession(ctx context.Context, session Session) error {
	if session.UserID == s.target {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.Store.PutSession(ctx, session)
}
func runAccountInFlightLogin(t *testing.T, f accountFixture, password string) {
	t.Helper()
	pause := &accountLoginPause{Store: f.Store, target: f.customer.ID, entered: make(chan struct{}), release: make(chan struct{})}
	srv := httptest.NewServer(NewApp(pause, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()}).Handler())
	defer srv.Close()
	var release sync.Once
	defer release.Do(func() { close(pause.release) })
	c := client(t, srv.URL, "", "")
	done := make(chan struct {
		status int
		err    error
	}, 1)
	go func() {
		raw := `{"email":"` + f.customer.Email + `","password":"` + password + `"}`
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/auth/login", strings.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", c.csrf)
		res, err := c.c.Do(req)
		status := 0
		if err == nil {
			status = res.StatusCode
			res.Body.Close()
		}
		done <- struct {
			status int
			err    error
		}{status, err}
	}()
	select {
	case <-pause.entered:
	case result := <-done:
		t.Fatal("login did not reach persistence", result)
	case <-time.After(10 * time.Second):
		t.Fatal("login blocked before pause")
	}
	_, err := f.WriteCustomerAccount(accountWriteTestContext(t, f.Store, f.ctx, f.admin.ID), f.admin.ID, accountWrite{Operation: "password", ID: f.customer.ID, Revision: f.customer.Revision, Password: "UpdatedPassword!2026", Reason: "verified recovery"})
	release.Do(func() { close(pause.release) })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil || result.status != 401 {
			t.Fatal("old password in-flight login survived reset", result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("login persistence deadlocked")
	}
}
func TestAdminCustomerMemoryInFlightLoginReset(t *testing.T) {
	runAccountInFlightLogin(t, memoryAccountFixture(t), "DemoCustomer!2026")
}
func TestMySQLAdminCustomerInFlightLoginReset(t *testing.T) {
	runAccountInFlightLogin(t, mysqlAccountFixture(t), "AccountFixture!2026")
}

func TestMySQLAdminCustomerAuditFailureRollsBackAccountAndSessions(t *testing.T) {
	f := mysqlAccountFixture(t)
	m := f.Store.(*MySQL)
	ctx := f.ctx
	session := Session{Revision: f.customer.Revision, Hash: ID() + ID(), UserID: f.customer.ID, Expires: time.Now().Add(time.Hour)}
	if err := m.PutSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	before, err := m.UserByID(ctx, f.customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	migrationExec(t, ctx, m.DB, "CREATE TRIGGER reject_account_audit BEFORE INSERT ON audit_events FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='fixture audit refusal'")
	if _, err = m.WriteCustomerAccount(accountWriteTestContext(t, m, ctx, f.admin.ID), f.admin.ID, accountWrite{Operation: "password", ID: before.ID, Revision: before.Revision, Password: "NotCommitted!2026!", Reason: "audit rollback regression"}); err == nil {
		t.Fatal("audit failure ignored")
	}
	after, err := m.UserByID(ctx, before.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("account changed without mandatory audit", after, err)
	}
	if _, err = m.Session(ctx, session.Hash); err != nil {
		t.Fatal("session revoked by rolled-back reset", err)
	}
	migrationExec(t, ctx, m.DB, "DROP TRIGGER reject_account_audit")
	created, err := m.WriteCustomerAccount(accountWriteTestContext(t, m, ctx, f.admin.ID), f.admin.ID, accountWrite{Operation: "create", Name: "Created", Email: "new@fixture.local", Role: "admin", Password: "CreatedAccount!2026", Reason: "approved"})
	if err != nil || created.Revision != 1 || created.Disabled {
		t.Fatal(created, err)
	}
	// SQL uniqueness is case insensitive; retries cannot create another identity.
	if _, err = m.WriteCustomerAccount(accountWriteTestContext(t, m, ctx, f.admin.ID), f.admin.ID, accountWrite{Operation: "create", Name: "Duplicate", Email: "NEW@fixture.local", Role: "customer", Password: "CreatedAccount!2026", Reason: "duplicate"}); !errors.Is(err, ErrAccountExists) {
		t.Fatal(err)
	}
}
func TestMySQLAdminCustomerHTTPAccountsAndSessionRevocation(t *testing.T) {
	f := mysqlAccountFixture(t)
	srv := httptest.NewServer(NewApp(f.Store, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()}).Handler())
	defer srv.Close()
	admin := client(t, srv.URL, f.admin.Email, "AccountFixture!2026")
	customer := client(t, srv.URL, f.customer.Email, "AccountFixture!2026")
	anon := client(t, srv.URL, "", "")
	base := "/api/v1/admin/customer-accounts"
	if status, body := anon.request("GET", base, nil, ""); status != 401 {
		t.Fatal(status, body)
	}
	if status, body := customer.request("GET", base, nil, ""); status != 403 {
		t.Fatal(status, body)
	}
	status, body := admin.request("GET", base, nil, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	items := body["data"].(map[string]any)["items"].([]any)
	if len(items) != 3 {
		t.Fatal("cross-customer list missing", body)
	}
	for _, v := range items {
		if len(v.(map[string]any)) != 6 {
			t.Fatal("private columns exposed", v)
		}
	}
	for _, op := range []string{"revoke-sessions", "password"} {
		current, err := f.UserByID(f.ctx, f.customer.ID)
		if err != nil {
			t.Fatal(err)
		}
		input := map[string]any{"revision": current.Revision, "reason": "verified support request"}
		if op == "password" {
			input["password"] = "NewCustomer!2026!"
		}
		status, body = admin.request("POST", base+"/"+current.ID+"/"+op, input, "")
		if status != 200 || body["data"].(map[string]any)["revision"] != float64(current.Revision+1) {
			t.Fatal(status, body)
		}
		status, body = customer.request("GET", "/api/v1/orders", nil, "")
		if status != 401 {
			t.Fatal("old customer session survived", op, status, body)
		}
		if op == "revoke-sessions" {
			customer = client(t, srv.URL, current.Email, "AccountFixture!2026")
		}
	}
	// Dedicated DTO only; legacy User JSON does not reveal hidden auth state.
	u, _ := f.UserByID(f.ctx, f.customer.ID)
	raw, _ := json.Marshal(u)
	for _, v := range []string{"disabled", "revision", "password_hash", u.PasswordHash} {
		if strings.Contains(string(raw), v) {
			t.Fatal("legacy User leaked", v)
		}
	}
}
