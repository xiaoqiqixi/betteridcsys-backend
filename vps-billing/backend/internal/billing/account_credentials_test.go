package billing

import (
	"aster/billing/internal/contracttest"
	"aster/billing/migrations"
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func sshText(t *testing.T, key any) string {
	t.Helper()
	public, err := ssh.NewPublicKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(ssh.MarshalAuthorizedKey(public))
}
func TestSSHKeyValidation(t *testing.T) {
	ed, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	small, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	strong, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{sshText(t, ed), sshText(t, &p256.PublicKey), sshText(t, &strong.PublicKey)} {
		key, err := validatedSSHKey(raw)
		if err != nil || !strings.HasPrefix(ssh.FingerprintSHA256(key), "SHA256:") {
			t.Fatal("supported key", err)
		}
	}
	edText := sshText(t, ed)
	for _, raw := range []string{sshText(t, &small.PublicKey), sshText(t, &p384.PublicKey), edText + edText, "command=\"echo unsafe\" " + edText, "not a key", "-----BEGIN PRIVATE KEY-----"} {
		if _, err := validatedSSHKey(raw); err == nil {
			t.Fatal("unsupported key accepted", raw[:min(len(raw), 60)])
		}
	}
}
func TestMySQLSSHKeyOwnershipAndDuplicates(t *testing.T) {
	f, _, srv := accountServer(t)
	c := client(t, srv.URL, f.customer.Email, "AccountFixture!2026")
	other := client(t, srv.URL, f.otherAdmin.Email, "AccountFixture!2026")
	key, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"name": "Laptop", "public_key": sshText(t, key)}
	item := accountHTTP(t, c, "POST", "/account/ssh-keys", "/account/ssh-keys", input, "").(map[string]any)
	accountHTTP(t, c, "GET", "/account/ssh-keys", "/account/ssh-keys", nil, "")
	accountFailure(t, c, "POST", "/account/ssh-keys", input, 409)
	list := accountHTTP(t, other, "GET", "/account/ssh-keys", "/account/ssh-keys", nil, "").([]any)
	if len(list) != 0 {
		t.Fatal("foreign key visible", list)
	}
	accountFailure(t, other, "DELETE", "/account/ssh-keys/"+item["id"].(string), nil, 404)
	accountHTTP(t, c, "DELETE", "/account/ssh-keys/"+item["id"].(string), "/account/ssh-keys/{id}", nil, "")
}
func bearerResponse(t *testing.T, a *App, secret, method, path, ip string, input any) (int, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	if input != nil {
		if err := json.NewEncoder(&body).Encode(input); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, "http://localhost/api/v1"+path, &body)
	r.RemoteAddr = ip
	r.Header.Set("Authorization", "Bearer "+secret)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return w.Code, out
}
func TestMySQLAPITokenScopeIPExpiryRevocationAndBoundaries(t *testing.T) {
	f, a, srv := accountServer(t)
	m := f.Store.(*MySQL)
	c := client(t, srv.URL, f.customer.Email, "AccountFixture!2026")
	other := client(t, srv.URL, f.otherAdmin.Email, "AccountFixture!2026")
	input := map[string]any{"name": "Read integration", "scopes": []string{"read"}, "ip_allowlist": []string{"192.0.2.0/24", "2001:db8:1::/64"}, "expires_in_days": 30}
	created := accountHTTP(t, c, "POST", "/account/api-tokens", "/account/api-tokens", input, "token-first").(map[string]any)
	secret := created["secret"].(string)
	id := created["token"].(map[string]any)["id"].(string)
	replay := accountHTTP(t, c, "POST", "/account/api-tokens", "/account/api-tokens", input, "token-first").(map[string]any)
	if replay["secret"] != secret || !strings.HasPrefix(secret, "ast_") {
		t.Fatal("secret or replay")
	}
	var stored string
	if err := m.DB.QueryRow("SELECT token_hash FROM api_tokens WHERE id=?", id).Scan(&stored); err != nil || stored != digest(secret) {
		t.Fatal("stored token", err)
	}
	list := accountHTTP(t, c, "GET", "/account/api-tokens", "/account/api-tokens", nil, "")
	raw, _ := json.Marshal(list)
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatal("list disclosed secret")
	}
	foreign := accountHTTP(t, other, "GET", "/account/api-tokens", "/account/api-tokens", nil, "").([]any)
	if len(foreign) != 0 {
		t.Fatal("foreign token listed")
	}
	accountFailure(t, other, "DELETE", "/account/api-tokens/"+id, nil, 404)
	for _, ip := range []string{"192.0.2.23:1234", "[2001:db8:1::123]:1234"} {
		status, out := bearerResponse(t, a, secret, "GET", "/account", ip, nil)
		if status != 200 {
			t.Fatal(status, out)
		}
		contracttest.Response(t, "GET", "/account", out)
	}
	if status, _ := bearerResponse(t, a, secret, "GET", "/account", "192.0.3.23:1234", nil); status != 403 {
		t.Fatal("IP bypass", status)
	}
	if status, _ := bearerResponse(t, a, secret, "PATCH", "/account/notifications", "192.0.2.23:1234", map[string]any{"announcements": false}); status != 403 {
		t.Fatal("read scope wrote", status)
	}
	for _, path := range []string{"/admin/roles", "/admin/clients", "/admin/tickets", "/admin/site-settings", "/account/password", "/account/sessions", "/account/api-tokens", "/account/ssh-keys", "/account/login-history", "/account/activity", "/auth/logout", "/session", "/audit"} {
		status, out := bearerResponse(t, a, secret, "GET", path, "192.0.2.23:1234", nil)
		if status != 403 {
			t.Fatal("token crossed boundary", path, status, out)
		}
		contracttest.Error(t, out)
	}
	input["name"] = "Write integration"
	input["scopes"] = []string{"write"}
	input["ip_allowlist"] = []string{}
	input["expires_in_days"] = nil
	write := accountHTTP(t, c, "POST", "/account/api-tokens", "/account/api-tokens", input, "token-write").(map[string]any)
	status, out := bearerResponse(t, a, write["secret"].(string), "PATCH", "/account/notifications", "192.0.2.23:1234", map[string]any{"announcements": false})
	if status != 200 {
		t.Fatal("bearer incorrectly required CSRF", status, out)
	}
	contracttest.Response(t, "PATCH", "/account/notifications", out)
	if _, err := m.DB.Exec("UPDATE api_tokens SET expires_at=DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 SECOND) WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	if status, _ = bearerResponse(t, a, secret, "GET", "/account", "192.0.2.23:1234", nil); status != 401 {
		t.Fatal("expired token accepted", status)
	}
	accountHTTP(t, c, "DELETE", "/account/api-tokens/"+id, "/account/api-tokens/{id}", nil, "")
	if status, _ = bearerResponse(t, a, secret, "GET", "/account", "192.0.2.23:1234", nil); status != 401 {
		t.Fatal("revoked token accepted", status)
	}
	// Staff-created tokens have the same restriction and cannot inherit owner privileges.
	input["scopes"] = []string{"read"}
	staffToken := accountHTTP(t, other, "POST", "/account/api-tokens", "/account/api-tokens", input, "token-staff").(map[string]any)
	if status, _ = bearerResponse(t, a, staffToken["secret"].(string), "GET", "/admin/roles", "192.0.2.23:1234", nil); status != 403 {
		t.Fatal("staff token inherited owner", status)
	}
}
func TestMySQLAccountFeatureMigrationResumes(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	prefix := 0
	for i, s := range schemaVersions {
		if s == migrations.AccountFeatures {
			prefix = i
			break
		}
	}
	if prefix == 0 {
		t.Fatal("feature migration missing")
	}
	migrationHistoricalPrefix(t, ctx, m, prefix)
	first := strings.Split(migrations.AccountFeatures, ";")[0]
	migrationExec(t, ctx, m.DB, first)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal("resume", err)
	}
	if status, err := m.SchemaStatus(ctx); err != nil || status.Status != "current" {
		t.Fatal("feature schema inventory", status, err)
	}
	if err := m.Migrate(ctx); err != nil {
		t.Fatal("repeat", err)
	}
}
