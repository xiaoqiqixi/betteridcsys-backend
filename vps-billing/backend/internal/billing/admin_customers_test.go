package billing

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type accountFixture struct {
	Store
	adminCustomerStore
	ctx                         context.Context
	admin, otherAdmin, customer User
}

func accountWriteTestContext(t *testing.T, store Store, ctx context.Context, actor string) context.Context {
	t.Helper()
	u, err := store.UserByID(ctx, actor)
	if err != nil {
		t.Fatal("account test session owner missing", err)
	}
	s := Session{Hash: ID() + ID(), UserID: actor, Revision: u.Revision, Expires: time.Now().Add(time.Hour)}
	if err := store.PutSession(ctx, s); err != nil {
		t.Fatal("account test session creation failed", err)
	}
	return withAccountWriteSession(ctx, actor, s.Hash)
}

func memoryAccountFixture(t *testing.T) accountFixture {
	t.Helper()
	m := NewMemory()
	customer, _, admin := cancellationUsers(t, m)
	other := admin
	other.ID = ID()
	other.Email = "second-admin@aster.local"
	if err := m.AddUser(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	return accountFixture{m, m, t.Context(), admin, other, customer}
}
func mysqlAccountFixture(t *testing.T) accountFixture {
	t.Helper()
	m, ctx := migrationTestDatabase(t)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("AccountFixture!2026"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	users := []User{{ID: ID(), Email: "admin@fixture.local", Name: "Admin", Role: "admin", PasswordHash: string(hash), Revision: 1}, {ID: ID(), Email: "second@fixture.local", Name: "Second", Role: "admin", PasswordHash: string(hash), Revision: 1}, {ID: ID(), Email: "customer@fixture.local", Name: "Customer", Role: "customer", PasswordHash: string(hash), Revision: 1}}
	for _, u := range users {
		if err = m.AddUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	return accountFixture{m, m, ctx, users[0], users[1], users[2]}
}
func runAccountContract(t *testing.T, f accountFixture) {
	t.Helper()
	ctx := f.ctx
	if _, err := f.AdminCustomerAccounts(ctx, f.customer.ID, accountQuery{}); !errors.Is(err, ErrAdminRequired) {
		t.Fatal("customer list authorized", err)
	}
	if _, err := f.WriteCustomerAccount(accountWriteTestContext(t, f.Store, ctx, f.customer.ID), f.customer.ID, accountWrite{Operation: "revoke", ID: f.admin.ID, Revision: 1, Reason: "test"}); !errors.Is(err, ErrAdminRequired) {
		t.Fatal("cached authority accepted", err)
	}
	// Copy a known fixture hash, not 1005 bcrypt computations or transactions.
	switch m := f.Store.(type) {
	case *Memory:
		m.mu.Lock()
		for n := 1; n <= 1005; n++ {
			id := fmt.Sprintf("%032x", n)
			m.users[id] = User{ID: id, Email: fmt.Sprintf("page-%04d@fixture.local", n), Name: "Page Literal%_", Role: "customer", PasswordHash: f.customer.PasswordHash, Revision: 1, Disabled: n%2 == 0}
		}
		m.mu.Unlock()
	case *MySQL:
		tx, err := m.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		for n := 1; n <= 1005; n++ {
			_, err = tx.ExecContext(ctx, "INSERT INTO users(id,email,name,role,password_hash,disabled,revision) VALUES(?,?,'Page Literal%_','customer',?,?,1)", fmt.Sprintf("%032x", n), fmt.Sprintf("page-%04d@fixture.local", n), f.customer.PasswordHash, n%2 == 0)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	q := accountQuery{Limit: 73, Q: "LITERAL%_", Role: "customer"}
	previous := ""
	for {
		page, err := f.AdminCustomerAccounts(ctx, f.admin.ID, q)
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range page.Items {
			if seen[u.ID] || previous != "" && u.ID >= previous {
				t.Fatal("page duplicate/order", u.ID)
			}
			seen[u.ID] = true
			previous = u.ID
		}
		if !page.HasMore {
			break
		}
		if len(page.Items) != 73 {
			t.Fatal("short page")
		}
		q.Before = previous
	}
	if len(seen) != 1005 {
		t.Fatal("account list silently truncated", len(seen))
	}
	page, err := f.AdminCustomerAccounts(ctx, f.admin.ID, accountQuery{Limit: 100, Status: "disabled", Q: "literal%_"})
	if err != nil || len(page.Items) != 100 || !page.HasMore {
		t.Fatal(page, err)
	}
	for _, u := range page.Items {
		if !u.Disabled {
			t.Fatal("disabled filter")
		}
	}
	for _, bad := range []accountQuery{{Limit: -1}, {Limit: 101}, {Role: "root"}, {Status: "enabled"}, {Before: "bad"}, {Q: "<script>"}} {
		if _, err = f.AdminCustomerAccounts(ctx, f.admin.ID, bad); !errors.Is(err, ErrAccountQuery) {
			t.Fatal("query accepted", bad, err)
		}
	}
	old, err := f.AdminCustomerAccount(ctx, f.admin.ID, f.customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldSession := Session{Revision: old.Revision, Hash: ID() + ID(), UserID: old.ID, Expires: time.Now().Add(time.Hour)}
	if err = f.PutSession(ctx, oldSession); err != nil {
		t.Fatal(err)
	}
	// Name-only changes preserve active sessions; generations still reject an
	// in-flight login whose password check read the earlier account revision.
	next, err := f.WriteCustomerAccount(accountWriteTestContext(t, f.Store, ctx, f.admin.ID), f.admin.ID, accountWrite{Operation: "update", ID: old.ID, Name: "Renamed", Role: old.Role, Disabled: false, Revision: old.Revision, Reason: "support request"})
	if err != nil || next.Revision != old.Revision+1 || next.Email != old.Email {
		t.Fatal(next, err)
	}
	if _, err = f.Session(ctx, oldSession.Hash); err != nil {
		t.Fatal("name edit revoked active session", err)
	}
	if err = f.PutSession(ctx, Session{Revision: old.Revision, Hash: ID() + ID(), UserID: old.ID, Expires: oldSession.Expires}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale in-flight login survived", err)
	}
	beforeAudits, _ := f.Audits(ctx, old.ID)
	if _, err = f.WriteCustomerAccount(accountWriteTestContext(t, f.Store, ctx, f.admin.ID), f.admin.ID, accountWrite{Operation: "update", ID: old.ID, Name: "Lost edit", Role: old.Role, Revision: old.Revision, Reason: "stale"}); !errors.Is(err, ErrAccountConflict) {
		t.Fatal("stale edit accepted", err)
	}
	afterAudits, _ := f.Audits(ctx, old.ID)
	if !reflect.DeepEqual(beforeAudits, afterAudits) {
		t.Fatal("rejected edit audited/wrote")
	}
	// A promotion must revoke customer cookies, not turn a stolen old cookie
	// into an administrator session. Disabling and password resets do likewise.
	next, err = f.WriteCustomerAccount(accountWriteTestContext(t, f.Store, ctx, f.admin.ID), f.admin.ID, accountWrite{Operation: "update", ID: next.ID, Name: next.Name, Role: "admin", Revision: next.Revision, Reason: "approved operator"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Session(ctx, oldSession.Hash); !errors.Is(err, ErrNotFound) {
		t.Fatal("promotion retained session", err)
	}
	for _, op := range []string{"revoke", "password"} {
		session := Session{Revision: next.Revision, Hash: ID() + ID(), UserID: next.ID, Expires: oldSession.Expires}
		if err = f.PutSession(ctx, session); err != nil {
			t.Fatal(err)
		}
		next, err = f.WriteCustomerAccount(accountWriteTestContext(t, f.Store, ctx, f.admin.ID), f.admin.ID, accountWrite{Operation: op, ID: next.ID, Revision: next.Revision, Password: "  ResetPassword!2026  ", Reason: "confirmed by support"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Session(ctx, session.Hash); !errors.Is(err, ErrNotFound) {
			t.Fatal(op, "retained session", err)
		}
		if err = f.PutSession(ctx, session); !errors.Is(err, ErrConflict) {
			t.Fatal(op, "allowed stale authentication", err)
		}
	}
	if bcrypt.CompareHashAndPassword([]byte(next.PasswordHash), []byte("  ResetPassword!2026  ")) != nil {
		t.Fatal("password whitespace changed")
	}
	if cost, _ := bcrypt.Cost([]byte(next.PasswordHash)); cost != 12 {
		t.Fatal("password strength regressed", cost)
	}
	next, err = f.WriteCustomerAccount(accountWriteTestContext(t, f.Store, ctx, f.admin.ID), f.admin.ID, accountWrite{Operation: "update", ID: next.ID, Name: next.Name, Role: "customer", Disabled: true, Revision: next.Revision, Reason: "disabled by request"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.PutSession(ctx, Session{Revision: next.Revision, Hash: ID() + ID(), UserID: next.ID, Expires: oldSession.Expires}); !errors.Is(err, ErrConflict) {
		t.Fatal("disabled login accepted", err)
	}
	audits, _ := f.Audits(ctx, next.ID)
	if len(audits) != 5 {
		t.Fatal("sensitive writes missing audit", len(audits))
	}
	for _, event := range audits {
		if event.UserID != next.ID || event.ActorEmail != f.admin.Email || strings.Contains(event.Detail, "ResetPassword") || strings.Contains(event.Detail, next.PasswordHash) {
			t.Fatal("audit owner/actor/credential", event)
		}
	}
	for _, disabled := range []bool{false, true} {
		role := "customer"
		if disabled {
			role = "admin"
		}
		if _, err = f.WriteCustomerAccount(accountWriteTestContext(t, f.Store, ctx, f.admin.ID), f.admin.ID, accountWrite{Operation: "update", ID: f.admin.ID, Name: f.admin.Name, Role: role, Disabled: disabled, Revision: f.admin.Revision, Reason: "self"}); !errors.Is(err, ErrSelfAccountChange) {
			t.Fatal("self demotion/disable", err)
		}
	}
}
func TestAdminCustomerMemoryContract(t *testing.T) { runAccountContract(t, memoryAccountFixture(t)) }
func TestMySQLAdminCustomerContract(t *testing.T)  { runAccountContract(t, mysqlAccountFixture(t)) }

func TestAdminCustomerValidationAndHiddenFields(t *testing.T) {
	for _, input := range []accountWrite{
		{Operation: "create", Name: "A", Email: "a@fixture.local", Role: "root", Password: strings.Repeat("x", 16), Reason: "test"},
		{Operation: "create", Name: "A", Email: "A <a@fixture.local>", Role: "customer", Password: strings.Repeat("x", 16), Reason: "test"},
		{Operation: "password", ID: ID(), Revision: 1, Password: strings.Repeat("x", 15), Reason: "test"},
		{Operation: "password", ID: ID(), Revision: 1, Password: strings.Repeat("密", 25), Reason: "test"},
		{Operation: "password", ID: ID(), Revision: 1, Password: strings.Repeat(" ", 16), Reason: "test"},
		{Operation: "revoke", ID: ID(), Revision: 1, Reason: "  "},
		{Operation: "revoke", ID: ID(), Revision: 0, Reason: "test"},
		{Operation: "update", ID: ID(), Name: "A", Role: "admin", Revision: 1, Reason: "a\nnewline"},
	} {
		if _, _, err := prepareAccountWrite(input); !errors.Is(err, ErrAccountInput) {
			t.Fatal("unsafe input accepted", input.Operation, err)
		}
	}
	u := User{ID: ID(), Role: "admin", Revision: 1}
	if _, _, err := accountMutation(User{ID: ID()}, u, accountWrite{Operation: "update", Role: "customer", Revision: 1}, "", 1); !errors.Is(err, ErrLastActiveAdmin) {
		t.Fatal("last admin defense missing", err)
	}
}
func runAccountConcurrency(t *testing.T, f accountFixture) {
	t.Helper()
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	contexts := map[string]context.Context{
		f.admin.ID:      accountWriteTestContext(t, f.Store, f.ctx, f.admin.ID),
		f.otherAdmin.ID: accountWriteTestContext(t, f.Store, f.ctx, f.otherAdmin.ID),
	}
	for _, pair := range [][2]User{{f.admin, f.otherAdmin}, {f.otherAdmin, f.admin}} {
		wg.Add(1)
		go func(actor, target User) {
			defer wg.Done()
			<-start
			_, err := f.WriteCustomerAccount(contexts[actor.ID], actor.ID, accountWrite{Operation: "update", ID: target.ID, Name: target.Name, Role: "customer", Revision: target.Revision, Reason: "concurrent demotion"})
			errs <- err
		}(pair[0], pair[1])
	}
	close(start)
	wg.Wait()
	close(errs)
	success, denied := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, ErrAdminRequired) || errors.Is(err, ErrAccountSession) {
			denied++
		} else {
			t.Fatal("unexpected concurrency failure (including deadlock)", err)
		}
	}
	if success != 1 || denied != 1 {
		t.Fatal(success, denied)
	}
	users, err := f.Users(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	admins := 0
	for _, u := range users {
		if u.Role == "admin" && !u.Disabled {
			admins++
		}
	}
	if admins != 1 {
		t.Fatal("last active administrator lost", admins)
	}
}
func TestAdminCustomerMemoryConcurrentDemotions(t *testing.T) {
	runAccountConcurrency(t, memoryAccountFixture(t))
}
func TestMySQLAdminCustomerConcurrentDemotions(t *testing.T) {
	runAccountConcurrency(t, mysqlAccountFixture(t))
}

type accountSpy struct {
	Store
	adminCustomerStore
	calls atomic.Int64
}

func (s *accountSpy) AdminCustomerAccounts(c context.Context, a string, q accountQuery) (accountPage, error) {
	s.calls.Add(1)
	return s.adminCustomerStore.AdminCustomerAccounts(c, a, q)
}
func (s *accountSpy) AdminCustomerAccount(c context.Context, a, id string) (User, error) {
	s.calls.Add(1)
	return s.adminCustomerStore.AdminCustomerAccount(c, a, id)
}
func (s *accountSpy) WriteCustomerAccount(c context.Context, a string, w accountWrite) (User, error) {
	s.calls.Add(1)
	return s.adminCustomerStore.WriteCustomerAccount(c, a, w)
}
func TestAdminCustomerHTTPAuthorizationCursorAndPrivacy(t *testing.T) {
	m := NewMemory()
	store := &accountSpy{Store: m, adminCustomerStore: m}
	app := NewApp(store, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()})
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	admin := client(t, srv.URL, "admin@aster.local", "DemoAdmin!2026")
	customer := client(t, srv.URL, "demo@aster.local", "DemoCustomer!2026")
	anon := client(t, srv.URL, "", "")
	base := "/api/v1/admin/customer-accounts"
	for _, v := range []struct {
		c      *testClient
		status int
	}{{anon, 401}, {customer, 403}} {
		if status, body := v.c.request("GET", base, nil, ""); status != v.status {
			t.Fatal(status, body)
		}
	}
	if store.calls.Load() != 0 {
		t.Fatal("unauthorized Store access")
	}
	status, body := admin.request("GET", base+"?limit=1", nil, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	page := body["data"].(map[string]any)
	cursor := page["next_cursor"].(string)
	first := page["items"].([]any)[0].(map[string]any)
	if len(first) != 6 || first["revision"] != float64(1) || first["disabled"] != false {
		t.Fatal("DTO missing/extra fields", first)
	}
	before := store.calls.Load()
	for _, query := range []string{"?limit=0", "?limit=101", "?limit=1&limit=1", "?q=a&q=b", "?owner_id=" + first["id"].(string), "?role=root", "?status=enabled", "?cursor=bad", "?limit=1&cursor=" + cursor[:len(cursor)-1] + "x", "?limit=2&cursor=" + cursor, "?limit=1&q=admin&cursor=" + cursor, "?limit=1&role=customer&cursor=" + cursor, "?limit=1&status=active&cursor=" + cursor} {
		if status, body = admin.request("GET", base+query, nil, ""); status != 400 {
			t.Fatal(query, status, body)
		}
	}
	if store.calls.Load() != before {
		t.Fatal("invalid cursor/query reached store")
	}
	if status, body = admin.request("GET", base+"?limit=1&cursor="+url.QueryEscape(cursor), nil, ""); status != 200 || body["data"].(map[string]any)["items"].([]any)[0].(map[string]any)["id"] == first["id"] {
		t.Fatal("cursor failed", status, body)
	}
	user, _ := m.UserByEmail(t.Context(), "demo@aster.local")
	status, body = admin.request("GET", base+"/"+user.ID, nil, "")
	if status != 200 || body["data"].(map[string]any)["id"] != user.ID {
		t.Fatal(status, body)
	}
	// Ordinary customer and legacy administrator lists retain their old User wire.
	for _, path := range []string{"/api/v1/session", "/api/v1/admin/customers"} {
		status, body = admin.request("GET", path, nil, "")
		if status != 200 {
			t.Fatal(status, body)
		}
		raw := fmt.Sprint(body)
		for _, key := range []string{"password_hash", "PasswordHash", "disabled", "revision"} {
			if strings.Contains(raw, key) {
				t.Fatal("hidden account state leaked", path, key)
			}
		}
	}
	create := map[string]any{"name": "New account", "email": "NEW@fixture.local", "role": "customer", "password": "NewAccount!2026!!", "reason": "approved"}
	status, body = admin.request("POST", base, create, "")
	if status != 201 {
		t.Fatal(status, body)
	}
	created := body["data"].(map[string]any)
	id := created["id"].(string)
	if created["email"] != "new@fixture.local" {
		t.Fatal("email normalization", body)
	}
	if status, body = admin.request("POST", base, create, ""); status != 409 || body["error"].(map[string]any)["code"] != "ACCOUNT_EXISTS" {
		t.Fatal(status, body)
	}
	// Unknown fields and caller-supplied password hashes cannot enter the Store.
	before = store.calls.Load()
	create["password_hash"] = "client-controlled"
	if status, body = admin.request("POST", base, create, ""); status != 400 {
		t.Fatal(status, body)
	}
	if store.calls.Load() != before {
		t.Fatal("unknown hash reached store")
	}
	if status, body = admin.request("POST", base+"/"+id, map[string]any{"name": "Changed", "role": "customer", "disabled": false, "revision": 1, "reason": "rename"}, ""); status != 200 {
		t.Fatal(status, body)
	}
	if status, body = admin.request("POST", base+"/"+id+"/revoke-sessions", map[string]any{"revision": 1, "reason": "stale"}, ""); status != 409 {
		t.Fatal(status, body)
	}

	// Cursors are not transferable even between two enabled administrators.
	second, _ := m.UserByEmail(t.Context(), "admin@aster.local")
	second.ID = ID()
	second.Email = "cursor-admin@fixture.local"
	if err := m.AddUser(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	secondClient := client(t, srv.URL, second.Email, "DemoAdmin!2026")
	before = store.calls.Load()
	if status, body = secondClient.request("GET", base+"?limit=1&cursor="+cursor, nil, ""); status != 400 {
		t.Fatal("cross-actor cursor accepted", status, body)
	}
	if store.calls.Load() != before {
		t.Fatal("cross-actor cursor reached store")
	}
	adminBefore, _ := m.UserByEmail(t.Context(), "admin@aster.local")
	if _, err := m.WriteCustomerAccount(accountWriteTestContext(t, m, t.Context(), adminBefore.ID), adminBefore.ID, accountWrite{Operation: "update", ID: adminBefore.ID, Name: "Updated operator", Role: "admin", Revision: adminBefore.Revision, Reason: "name correction"}); err != nil {
		t.Fatal(err)
	}
	before = store.calls.Load()
	if status, body = admin.request("GET", base+"?limit=1&cursor="+cursor, nil, ""); status != 400 {
		t.Fatal("old actor generation cursor accepted", status, body)
	}
	if store.calls.Load() != before {
		t.Fatal("old-generation cursor reached store")
	}
	// Bind cursor to actual server actor state. Changing a persisted role is not
	// a role-management API: it is an isolated authorization regression fixture.
	adminUser, _ := m.UserByEmail(t.Context(), "admin@aster.local")
	m.mu.Lock()
	v := m.users[adminUser.ID]
	v.Role = "customer"
	m.users[v.ID] = v
	m.mu.Unlock()
	before = store.calls.Load()
	if status, body = admin.request("GET", base+"?limit=1&cursor="+cursor, nil, ""); status != 403 {
		t.Fatal("cookie cached administrator role", status, body)
	}
	if store.calls.Load() != before {
		t.Fatal("demoted actor reached store")
	}
}
