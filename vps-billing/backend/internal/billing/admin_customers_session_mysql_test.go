package billing

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

func awaitAccountLockWait(t *testing.T, ctx context.Context, db *sql.DB, table string, done, other <-chan accountHTTPResult) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waits int
		err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM performance_schema.data_lock_waits w JOIN performance_schema.data_locks l ON w.REQUESTING_ENGINE_LOCK_ID=l.ENGINE_LOCK_ID AND w.ENGINE=l.ENGINE WHERE l.OBJECT_SCHEMA=DATABASE() AND l.OBJECT_NAME=?", table).Scan(&waits)
		if err != nil {
			t.Fatal("cannot observe actual identity transaction wait", err)
		}
		if waits > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("identity request never reached the expected transaction wait", table)
		}
		select {
		case result := <-done:
			t.Fatal("identity request ended before the expected transaction wait", result.status, result.code, result.err)
		case result := <-other:
			t.Fatal("other identity request ended before the expected transaction wait", result.status, result.code, result.err)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// Hash every migrated business row, not just an asserted wallet total. Users,
// sessions and identity audits are checked separately; raw rows never escape.
func accountBusinessSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query("SHOW TABLES")
	if err != nil {
		t.Fatal("cannot enumerate isolated business tables", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`^[a-z][a-z0-9_]*$`).MatchString(name) {
			t.Fatal("unexpected isolated table identifier")
		}
		if name != "users" && name != "sessions" && name != "audit_events" {
			tables = append(tables, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	sort.Strings(tables)
	h := sha256.New()
	for _, table := range tables {
		rows, err := db.Query("SELECT * FROM `" + table + "`")
		if err != nil {
			t.Fatal("cannot snapshot isolated business table", err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var encoded []string
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			encoded = append(encoded, string(raw))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		sort.Strings(encoded)
		fmt.Fprintln(h, table, strings.Join(columns, ","), len(encoded))
		for _, row := range encoded {
			fmt.Fprintln(h, row)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func accountCookieHash(t *testing.T, c *http.Client, origin string) string {
	t.Helper()
	u, err := url.Parse(origin)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range c.Jar.Cookies(u) {
		if cookie.Name == "__Host-aster_session" {
			return digest(cookie.Value)
		}
	}
	t.Fatal("strict TLS account session cookie was not issued")
	return ""
}

func TestMySQLAccountQueuedWriteRequiresOriginalRequestSession(t *testing.T) {
	for _, test := range []struct{ action, invalidation string }{
		{"create", "logout"}, {"update", "logout"}, {"password", "logout"}, {"revoke", "logout"},
		{"update", "expiry"}, {"update", "same-owner-rotation"},
	} {
		t.Run(test.action+"/"+test.invalidation, func(t *testing.T) {
			f := mysqlAccountFixture(t)
			m := f.Store.(*MySQL)
			c, origin, csrf := accountTLSClient(t, m, f.admin.Email, "AccountFixture!2026")
			originalHash := accountCookieHash(t, c, origin)
			targetSession := Session{Hash: ID() + ID(), UserID: f.customer.ID, Revision: f.customer.Revision, Expires: time.Now().Add(time.Hour)}
			if err := m.PutSession(f.ctx, targetSession); err != nil {
				t.Fatal(err)
			}
			before, err := m.UserByID(f.ctx, f.customer.ID)
			if err != nil {
				t.Fatal(err)
			}
			businessBefore := accountBusinessSnapshot(t, m.DB)
			blocker, err := m.DB.BeginTx(f.ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback()
			var singleton int
			if err := blocker.QueryRowContext(f.ctx, "SELECT id FROM email_settings WHERE id=1 FOR UPDATE").Scan(&singleton); err != nil {
				t.Fatal(err)
			}
			path, body := accountOperationInput(t, f, test.action)
			done := make(chan accountHTTPResult, 1)
			go func() { done <- accountHTTPS(c, origin, "POST", path, csrf, body) }()
			deadline := time.Now().Add(10 * time.Second)
			for {
				var waits int
				err := m.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM performance_schema.data_lock_waits w JOIN performance_schema.data_locks l ON w.REQUESTING_ENGINE_LOCK_ID=l.ENGINE_LOCK_ID AND w.ENGINE=l.ENGINE WHERE l.OBJECT_SCHEMA=DATABASE() AND l.OBJECT_NAME='email_settings'").Scan(&waits)
				if err != nil {
					t.Fatal("cannot observe actual account transaction wait", err)
				}
				if waits > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("account request never reached the held transaction lock")
				}
				select {
				case result := <-done:
					t.Fatal("account request bypassed the held transaction lock", result.status, result.code, result.err)
				case <-time.After(5 * time.Millisecond):
				}
			}
			if test.invalidation == "expiry" {
				if _, err := m.DB.ExecContext(f.ctx, "UPDATE sessions SET expires_at=UTC_TIMESTAMP(6)-INTERVAL 1 SECOND WHERE token_hash=?", originalHash); err != nil {
					t.Fatal(err)
				}
			} else {
				logout := accountHTTPS(c, origin, "POST", "/api/v1/auth/logout", csrf, "{}")
				if logout.err != nil || logout.status != 200 {
					t.Fatal("logout did not commit before releasing the account lock", logout.status, logout.err)
				}
				if test.invalidation == "same-owner-rotation" {
					fresh := accountHTTPS(c, origin, "GET", "/api/v1/session", "", "")
					if fresh.err != nil || fresh.status != 200 {
						t.Fatal("fresh anonymous session unavailable", fresh.status, fresh.err)
					}
					token, _ := fresh.data["csrf_token"].(string)
					raw, _ := json.Marshal(map[string]string{"email": f.admin.Email, "password": "AccountFixture!2026"})
					login := accountHTTPS(c, origin, "POST", "/api/v1/auth/login", token, string(raw))
					if login.err != nil || login.status != 200 || accountCookieHash(t, c, origin) == originalHash {
						t.Fatal("same-owner replacement session was not actually issued", login.status, login.err)
					}
				}
			}
			var auditsBefore int
			if err := m.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM audit_events").Scan(&auditsBefore); err != nil {
				t.Fatal(err)
			}
			if err := blocker.Commit(); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-done:
				if result.err != nil || result.status != 401 || result.code != "AUTH_REQUIRED" {
					t.Fatal("old queued request must receive complete AUTH_REQUIRED", result.status, result.code, result.err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("queued account transaction did not finish")
			}
			after, err := m.UserByID(f.ctx, f.customer.ID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("rejected original-session write changed target credentials/state")
			}
			if _, err := m.Session(f.ctx, targetSession.Hash); err != nil {
				t.Fatal("rejected original-session write deleted target sessions")
			}
			if test.action == "create" {
				if _, err := m.UserByEmail(f.ctx, "session-fence@example.invalid"); err == nil {
					t.Fatal("rejected request created an account")
				}
			}
			var auditsAfter int
			if err := m.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM audit_events").Scan(&auditsAfter); err != nil || auditsAfter != auditsBefore {
				t.Fatal("rejected request emitted a successful identity audit", err)
			}
			if businessBefore != accountBusinessSnapshot(t, m.DB) {
				t.Fatal("account-session rejection changed a business/financial table")
			}
			if test.invalidation == "same-owner-rotation" {
				current := accountHTTPS(c, origin, "GET", "/api/v1/session", "", "")
				user, _ := current.data["user"].(map[string]any)
				if current.err != nil || current.status != 200 || user["id"] != f.admin.ID {
					t.Fatal("old request failure invalidated the newly issued session")
				}
			}
		})
	}
}

func TestMySQLAccountSessionExpiryIsCheckedAfterItsRowLock(t *testing.T) {
	f := mysqlAccountFixture(t)
	m := f.Store.(*MySQL)
	c, origin, csrf := accountTLSClient(t, m, f.admin.Email, "AccountFixture!2026")
	hash := accountCookieHash(t, c, origin)
	before, err := m.UserByID(f.ctx, f.customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	businessBefore := accountBusinessSnapshot(t, m.DB)
	var auditsBefore int
	if err := m.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM audit_events").Scan(&auditsBefore); err != nil {
		t.Fatal(err)
	}
	blocker, err := m.DB.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	var owner string
	if err := blocker.QueryRowContext(f.ctx, "SELECT user_id FROM sessions WHERE token_hash=? FOR UPDATE", hash).Scan(&owner); err != nil || owner != f.admin.ID {
		t.Fatal("cannot lock the exact original session", err)
	}
	path, body := accountOperationInput(t, f, "update")
	done := make(chan accountHTTPResult, 1)
	go func() { done <- accountHTTPS(c, origin, "POST", path, csrf, body) }()
	awaitAccountLockWait(t, f.ctx, m.DB, "sessions", done, nil)
	// Do not make HTTP authentication race a four-second setup window. The
	// fixture owns this row: after observing the real wait, set its deadline
	// strictly after the waiting statement began, then let database time pass it.
	if _, err := blocker.ExecContext(f.ctx, "UPDATE sessions SET expires_at=UTC_TIMESTAMP(6)+INTERVAL 250000 MICROSECOND WHERE token_hash=?", hash); err != nil {
		t.Fatal(err)
	}
	var expires time.Time
	if err := blocker.QueryRowContext(f.ctx, "SELECT expires_at FROM sessions WHERE token_hash=?", hash).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	// Wait for the database's actual deadline, not a blind sleep or host clock.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var expired bool
		if err := m.DB.QueryRowContext(f.ctx, "SELECT ?<=UTC_TIMESTAMP(6)", expires).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("database session deadline never elapsed")
		}
		select {
		case result := <-done:
			t.Fatal("account request bypassed its exact session lock", result.status, result.code, result.err)
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil || result.status != 401 || result.code != "AUTH_REQUIRED" {
			t.Fatal("session expiry during the exact row wait must return complete AUTH_REQUIRED", result.status, result.code, result.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("account request never finished after releasing its expired session")
	}
	after, err := m.UserByID(f.ctx, f.customer.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("session that expired while waiting modified its target", err)
	}
	var auditsAfter int
	if err := m.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM audit_events").Scan(&auditsAfter); err != nil || auditsAfter != auditsBefore {
		t.Fatal("expired queued session emitted a successful identity audit", err)
	}
	if businessBefore != accountBusinessSnapshot(t, m.DB) {
		t.Fatal("session expiry changed a business/financial table")
	}
}

func TestMySQLAccountSelfSessionDeletionSerializesWithLogout(t *testing.T) {
	for _, action := range []string{"password", "revoke-sessions"} {
		t.Run(action, func(t *testing.T) {
			f := mysqlAccountFixture(t)
			m := f.Store.(*MySQL)
			c, origin, csrf := accountTLSClient(t, m, f.admin.Email, "AccountFixture!2026")
			hash := accountCookieHash(t, c, origin)
			businessBefore := accountBusinessSnapshot(t, m.DB)
			blocker, err := m.DB.BeginTx(f.ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback()
			var id string
			if err := blocker.QueryRowContext(f.ctx, "SELECT id FROM users WHERE id=? FOR UPDATE", f.admin.ID).Scan(&id); err != nil {
				t.Fatal(err)
			}
			body := map[string]any{"revision": f.admin.Revision, "reason": "Own session deletion and queued logout"}
			if action == "password" {
				body["password"] = "SelfSessionBoundary!2026"
			}
			raw, _ := json.Marshal(body)
			writeDone, logoutDone := make(chan accountHTTPResult, 1), make(chan accountHTTPResult, 1)
			go func() {
				writeDone <- accountHTTPS(c, origin, "POST", "/api/v1/admin/customer-accounts/"+f.admin.ID+"/"+action, csrf, string(raw))
			}()
			// The write owns its original session row and now waits for actor state.
			awaitAccountLockWait(t, f.ctx, m.DB, "users", writeDone, nil)
			go func() { logoutDone <- accountHTTPS(c, origin, "POST", "/api/v1/auth/logout", csrf, "{}") }()
			awaitAccountLockWait(t, f.ctx, m.DB, "sessions", logoutDone, writeDone)
			if err := blocker.Commit(); err != nil {
				t.Fatal(err)
			}
			for _, done := range []<-chan accountHTTPResult{writeDone, logoutDone} {
				select {
				case result := <-done:
					if result.err != nil || result.status != 200 {
						t.Fatal("self identity action/logout deadlocked or lost its response", result.status, result.code, result.err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("self identity action/logout failed to complete after releasing actor lock")
				}
			}
			if _, err := m.Session(f.ctx, hash); !errors.Is(err, ErrNotFound) {
				t.Fatal("own credential action/logout retained the original session", err)
			}
			if businessBefore != accountBusinessSnapshot(t, m.DB) {
				t.Fatal("identity/logout serialization changed a business table")
			}
		})
	}
}
