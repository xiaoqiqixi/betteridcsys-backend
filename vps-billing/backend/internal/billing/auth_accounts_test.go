package billing

import (
	"aster/billing/internal/contracttest"
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func accountHTTP(t *testing.T, c *testClient, method, path, schema string, input any, key string) any {
	t.Helper()
	status, out := c.request(method, "/api/v1"+path, input, key)
	if status != 200 {
		t.Fatalf("%s %s: %d %v", method, path, status, out)
	}
	contracttest.Response(t, method, schema, out)
	return out["data"]
}
func accountFailure(t *testing.T, c *testClient, method, path string, input any, want int) {
	t.Helper()
	status, out := c.request(method, "/api/v1"+path, input, "")
	if status != want {
		t.Fatalf("%s %s: %d %v, want %d", method, path, status, out, want)
	}
	contracttest.Error(t, out)
}
func accountServer(t *testing.T) (accountFixture, *App, *httptest.Server) {
	t.Helper()
	f := mysqlAccountFixture(t)
	a := NewApp(f.Store, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), SMTPKey: emailTestCipher(t)})
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	return f, a, srv
}
func queuedLink(t *testing.T, a *App, m *MySQL, id, purpose string) string {
	t.Helper()
	var message, sealed string
	if err := m.DB.QueryRow("SELECT id,protected_variables FROM email_outbox WHERE user_id=? AND template_key=? ORDER BY created_at DESC,id DESC LIMIT 1", id, purpose).Scan(&message, &sealed); err != nil {
		t.Fatal(err)
	}
	var values map[string]string
	if err := a.openAccount("email:"+message, sealed, &values); err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(values["url"])
	if err != nil {
		t.Fatal(err)
	}
	token := link.Query().Get("token")
	var stored string
	var minutes int
	if err = m.DB.QueryRow("SELECT token_hash,TIMESTAMPDIFF(MINUTE,created_at,expires_at) FROM email_tokens WHERE user_id=? AND purpose=?", id, purpose).Scan(&stored, &minutes); err != nil {
		t.Fatal(err)
	}
	if token == "" || stored != digest(token) || minutes != 30 || strings.Contains(sealed, token) {
		t.Fatal("token storage or expiry invariant")
	}
	return token
}

func TestMySQLRegistrationAndVerification(t *testing.T) {
	f, a, srv := accountServer(t)
	m := f.Store.(*MySQL)
	c := client(t, srv.URL, "", "")
	accountHTTP(t, c, "GET", "/session", "/session", nil, "")
	input := map[string]any{"email": "new@example.com", "password": strings.Repeat("a", 100), "name": "New client", "country": "DE", "locale": "en", "currency": "CNY", "accept_terms": true}
	first := accountHTTP(t, c, "POST", "/auth/register", "/auth/register", input, "register-first").(map[string]any)
	id := first["user"].(map[string]any)["id"].(string)
	again := accountHTTP(t, c, "POST", "/auth/register", "/auth/register", input, "register-first").(map[string]any)
	if again["user"].(map[string]any)["id"] != id {
		t.Fatal("registration replay created another account")
	}
	var count int
	if err := m.DB.QueryRow("SELECT COUNT(*) FROM email_outbox WHERE user_id=?", id).Scan(&count); err != nil || count != 2 {
		t.Fatal("registration outbox", count, err)
	}
	verify := queuedLink(t, a, m, id, "email_verification")
	accountHTTP(t, c, "POST", "/auth/email/resend", "/auth/email/resend", map[string]any{}, "")
	accountFailure(t, c, "POST", "/auth/email/verify", map[string]any{"token": verify}, 400)
	verify = queuedLink(t, a, m, id, "email_verification")
	accountHTTP(t, c, "POST", "/auth/email/verify", "/auth/email/verify", map[string]any{"token": verify}, "")
	accountFailure(t, c, "POST", "/auth/email/verify", map[string]any{"token": verify}, 400)
	profile := accountHTTP(t, c, "GET", "/account", "/account", nil, "").(map[string]any)
	if profile["email_verified"] != true || profile["country"] != "DE" || profile["locale"] != "en" {
		t.Fatal(profile)
	}
	session := accountHTTP(t, c, "POST", "/auth/logout", "/auth/logout", map[string]any{}, "").(map[string]any)
	c.csrf = session["csrf_token"].(string)
	accountFailure(t, c, "GET", "/account", nil, 401)
	session = accountHTTP(t, c, "POST", "/auth/login", "/auth/login", map[string]any{"email": input["email"], "password": input["password"], "remember": true}, "").(map[string]any)
	c.csrf = session["csrf_token"].(string)
	if session["user"].(map[string]any)["email_verified"] != true {
		t.Fatal(session)
	}
	accountHTTP(t, c, "GET", "/account/login-history", "/account/login-history", nil, "")
	accountHTTP(t, c, "GET", "/account/activity", "/account/activity", nil, "")
	// A new anonymous principal cannot replay a registration authenticated elsewhere.
	outsider := client(t, srv.URL, "", "")
	status, _ := outsider.request("POST", "/api/v1/auth/register", input, "register-first")
	if status != 409 {
		t.Fatal("unbound registration replay", status)
	}
}

func TestMySQLRecoveryExpirySingleUseAndSessions(t *testing.T) {
	f, a, srv := accountServer(t)
	m := f.Store.(*MySQL)
	c := client(t, srv.URL, f.customer.Email, "AccountFixture!2026")
	other := client(t, srv.URL, f.customer.Email, "AccountFixture!2026")
	stranger := client(t, srv.URL, f.otherAdmin.Email, "AccountFixture!2026")
	sessions := accountHTTP(t, c, "GET", "/account/sessions", "/account/sessions", nil, "").([]any)
	for _, row := range sessions {
		s := row.(map[string]any)
		if s["current"] == false {
			accountFailure(t, stranger, "DELETE", "/account/sessions/"+s["id"].(string), nil, 404)
			accountHTTP(t, c, "DELETE", "/account/sessions/"+s["id"].(string), "/account/sessions/{id}", nil, "")
		}
	}
	accountFailure(t, other, "GET", "/account", nil, 401)
	other = client(t, srv.URL, f.customer.Email, "AccountFixture!2026")
	accountHTTP(t, c, "POST", "/account/password", "/account/password", map[string]any{"current_password": "AccountFixture!2026", "new_password": "Changed password!2026"}, "")
	accountFailure(t, other, "GET", "/account", nil, 401)
	accountHTTP(t, c, "GET", "/account", "/account", nil, "")
	anon := client(t, srv.URL, "", "")
	for _, email := range []string{"missing@example.com", f.customer.Email} {
		accountHTTP(t, anon, "POST", "/auth/password/forgot", "/auth/password/forgot", map[string]any{"email": email}, "")
	}
	token := queuedLink(t, a, m, f.customer.ID, "password_reset")
	if _, err := m.DB.Exec("UPDATE email_tokens SET expires_at=DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 SECOND) WHERE user_id=?", f.customer.ID); err != nil {
		t.Fatal(err)
	}
	accountFailure(t, anon, "POST", "/auth/password/reset", map[string]any{"token": token, "password": "Reset password!2026"}, 400)
	accountHTTP(t, anon, "POST", "/auth/password/forgot", "/auth/password/forgot", map[string]any{"email": f.customer.Email}, "")
	token = queuedLink(t, a, m, f.customer.ID, "password_reset")
	accountHTTP(t, anon, "POST", "/auth/password/reset", "/auth/password/reset", map[string]any{"token": token, "password": "Reset password!2026"}, "")
	accountFailure(t, anon, "POST", "/auth/password/reset", map[string]any{"token": token, "password": "Another password!2026"}, 400)
	accountFailure(t, c, "GET", "/account", nil, 401)
	accountHTTP(t, anon, "POST", "/auth/login", "/auth/login", map[string]any{"email": f.customer.Email, "password": "Reset password!2026"}, "")
}

func TestMySQLProfilePreferencesAndStaffRedaction(t *testing.T) {
	f, _, srv := accountServer(t)
	m := f.Store.(*MySQL)
	c := client(t, srv.URL, f.customer.Email, "AccountFixture!2026")
	accountHTTP(t, c, "PATCH", "/account", "/account", map[string]any{"revision": 1, "name": "Updated", "company": "Example", "locale": "en", "marketing_emails": true}, "")
	accountFailure(t, c, "PATCH", "/account", map[string]any{"revision": 1, "name": "Stale"}, 409)
	accountFailure(t, c, "PATCH", "/account", map[string]any{"revision": 2, "email": "stolen@example.com"}, 400)
	accountHTTP(t, c, "GET", "/account/notifications", "/account/notifications", nil, "")
	accountHTTP(t, c, "PATCH", "/account/notifications", "/account/notifications", map[string]any{"invoice_created": false, "ticket_replies": false}, "")
	accountFailure(t, c, "PATCH", "/account/notifications", map[string]any{"announcements": nil}, 400)
	tx, err := m.DB.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = insertAccountAudit(f.ctx, tx, f.customer.ID, f.admin.ID, "client.changed", f.customer.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	result := accountHTTP(t, c, "GET", "/account/activity?limit=1", "/account/activity", nil, "")
	raw, _ := json.Marshal(result)
	if bytes.Contains(raw, []byte(f.admin.Email)) || !bytes.Contains(raw, []byte(`"actor_type":"staff"`)) {
		t.Fatal("staff identity leaked", string(raw))
	}
	outsider := client(t, srv.URL, f.otherAdmin.Email, "AccountFixture!2026")
	foreign := accountHTTP(t, outsider, "GET", "/account/activity", "/account/activity", nil, "")
	raw, _ = json.Marshal(foreign)
	if bytes.Contains(raw, []byte("client.changed")) {
		t.Fatal("activity ownership bypass")
	}
	accountFailure(t, c, "GET", "/account/activity?limit=1junk", nil, 400)
	if _, err = m.DB.Exec("UPDATE users SET currency_locked=TRUE WHERE id=?", f.customer.ID); err != nil {
		t.Fatal(err)
	}
	accountFailure(t, c, "PATCH", "/account", map[string]any{"revision": 2, "currency": "USD"}, 409)
}

func TestMySQLStaffInvitationAndPermissions(t *testing.T) {
	f, a, srv := accountServer(t)
	m := f.Store.(*MySQL)
	c := client(t, srv.URL, f.admin.Email, "AccountFixture!2026")
	role := accountHTTP(t, c, "POST", "/admin/roles", "/admin/roles", map[string]any{"name": "Support", "description": "Support team", "permissions": []string{"tickets.read"}}, "").(map[string]any)
	input := map[string]any{"email": "invited@example.com", "name": "Invited", "role_id": role["id"]}
	member := accountHTTP(t, c, "POST", "/admin/staff", "/admin/staff", input, "invite-first").(map[string]any)
	again := accountHTTP(t, c, "POST", "/admin/staff", "/admin/staff", input, "invite-first").(map[string]any)
	if member["id"] != again["id"] {
		t.Fatal("invitation duplicated")
	}
	token := queuedLink(t, a, m, member["id"].(string), "password_reset")
	anon := client(t, srv.URL, "", "")
	accountHTTP(t, anon, "POST", "/auth/password/reset", "/auth/password/reset", map[string]any{"token": token, "password": "Invited password!2026"}, "")
	limited := client(t, srv.URL, "invited@example.com", "Invited password!2026")
	for _, p := range []string{"/admin/staff", "/admin/roles", "/admin/email-templates"} {
		accountFailure(t, limited, "GET", p, nil, 403)
	}
	status, _ := limited.request("POST", "/api/v1/admin/staff", input, "denied-invite")
	if status != 403 {
		t.Fatal("invitation permission", status)
	}
	accountFailure(t, limited, "PATCH", "/admin/email-templates/welcome", map[string]any{}, 403)
	accountHTTP(t, c, "GET", "/admin/email-templates", "/admin/email-templates", nil, "")
	template := map[string]any{"revision": 1, "subject": map[string]string{"en": "Hello {{.name}}"}, "body": map[string]string{"en": "Welcome to {{.site_name}}"}, "enabled": true}
	accountHTTP(t, c, "PATCH", "/admin/email-templates/welcome", "/admin/email-templates/{key}", template, "")
	template["revision"] = 2
	template["body"] = map[string]string{"en": "{{.password}}"}
	accountFailure(t, c, "PATCH", "/admin/email-templates/welcome", template, 400)
}

func TestMySQLConcurrentVerificationAndRegistrationToggle(t *testing.T) {
	f, a, srv := accountServer(t)
	m := f.Store.(*MySQL)
	c := client(t, srv.URL, f.customer.Email, "AccountFixture!2026")
	accountHTTP(t, c, "POST", "/auth/email/resend", "/auth/email/resend", map[string]any{}, "")
	token := queuedLink(t, a, m, f.customer.ID, "email_verification")
	if _, err := m.DB.Exec("UPDATE email_tokens SET expires_at=DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 SECOND) WHERE user_id=?", f.customer.ID); err != nil {
		t.Fatal(err)
	}
	accountFailure(t, c, "POST", "/auth/email/verify", map[string]any{"token": token}, 400)
	accountHTTP(t, c, "POST", "/auth/email/resend", "/auth/email/resend", map[string]any{}, "")
	token = queuedLink(t, a, m, f.customer.ID, "email_verification")
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for range 2 {
		wg.Go(func() {
			status, _ := c.request("POST", "/api/v1/auth/email/verify", map[string]any{"token": token}, "")
			results <- status
		})
	}
	wg.Wait()
	close(results)
	successes := 0
	for status := range results {
		if status == 200 {
			successes++
		} else if status != 400 {
			t.Fatal(status)
		}
	}
	if successes != 1 {
		t.Fatal("concurrent token reuse", successes)
	}
	site, err := a.readSiteSettings6(f.ctx, m.DB)
	if err != nil {
		t.Fatal(err)
	}
	site.RegistrationOpen = false
	raw, _ := json.Marshal(site)
	if _, err = m.DB.Exec("INSERT INTO account_settings(name,document) VALUES('site',?)", raw); err != nil {
		t.Fatal(err)
	}
	anon := client(t, srv.URL, "", "")
	status, _ := anon.request("POST", "/api/v1/auth/register", map[string]any{"email": "closed@example.com", "password": "Never create!2026", "name": "Closed", "country": "US", "locale": "en", "currency": "CNY", "accept_terms": true}, "closed-registration")
	if status != 403 {
		t.Fatal("registration toggle", status)
	}
	var id string
	if err = m.DB.QueryRow("SELECT id FROM users WHERE email='closed@example.com'").Scan(&id); err != sql.ErrNoRows {
		t.Fatal("closed registration persisted", err)
	}
}

func TestAccountRequestValidation(t *testing.T) {
	for _, raw := range []string{`{"name":"a","name":"b","revision":1}`, `{"revision":1} {}`, `null`, `{"revision":1,"locale":"fr"}`} {
		r := httptest.NewRequest("PATCH", "http://localhost/api/v1/account", strings.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		var value map[string]any
		if accountBody(w, r, "updateProfile", &value) || w.Code != 400 {
			t.Fatal("invalid body accepted", raw, w.Code)
		}
	}
	r := httptest.NewRequest("PATCH", "http://localhost/api/v1/account", strings.NewReader(`{"revision":1,"name":"Valid"}`))
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	if !accountBody(httptest.NewRecorder(), r, "updateProfile", &map[string]any{}) {
		t.Fatal("valid body rejected")
	}
	// Both ends of the accepted password range are hashed without bcrypt truncation.
	password := strings.Repeat("secret", 40)
	hash, err := hashPassword(password)
	if err != nil || !checkPassword(hash, password) || checkPassword(hash, password[:len(password)-1]+"x") {
		t.Fatal("long password mishandled", err)
	}
}
