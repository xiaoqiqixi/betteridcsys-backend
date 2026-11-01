package billing

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestMySQLClientAdminLifecycleAndPrivateNotes(t *testing.T) {
	f, a, srv := accountServer(t)
	m := f.Store.(*MySQL)
	owner := client(t, srv.URL, f.admin.Email, "AccountFixture!2026")
	in := map[string]any{"email": "managed@example.com", "name": "Managed", "company": "Example", "country": "DE", "currency": "CNY", "locale": "en"}
	v := accountHTTP(t, owner, "POST", "/admin/clients", "/admin/clients", in, "create-client-1").(map[string]any)
	id := v["id"].(string)
	replay := accountHTTP(t, owner, "POST", "/admin/clients", "/admin/clients", in, "create-client-1").(map[string]any)
	if replay["id"] != id {
		t.Fatal("client creation duplicated")
	}
	token := queuedLink(t, a, m, id, "password_reset")
	anon := client(t, srv.URL, "", "")
	accountHTTP(t, anon, "POST", "/auth/password/reset", "/auth/password/reset", map[string]any{"token": token, "password": "Managed password!2026"}, "")
	c := client(t, srv.URL, in["email"].(string), "Managed password!2026")
	v = accountHTTP(t, owner, "GET", "/admin/clients/"+id, "/admin/clients/{id}", nil, "").(map[string]any)
	patch := map[string]any{"revision": v["revision"], "notes": "Private staff assessment", "company": "Changed", "phone": "+49 1234", "country": "FR"}
	v = accountHTTP(t, owner, "PATCH", "/admin/clients/"+id, "/admin/clients/{id}", patch, "").(map[string]any)
	accountFailure(t, owner, "PATCH", "/admin/clients/"+id, patch, 409)
	if v["notes"] != patch["notes"] || v["country"] != "FR" {
		t.Fatal(v)
	}
	for _, path := range []string{"/account", "/account/activity"} {
		out := accountHTTP(t, c, "GET", path, path, nil, "")
		raw, _ := json.Marshal(out)
		if bytes.Contains(raw, []byte("Private staff assessment")) || bytes.Contains(raw, []byte(f.admin.Email)) {
			t.Fatal("staff-only data leaked", string(raw))
		}
	}
	items := accountHTTP(t, owner, "GET", "/admin/clients?q=managed&status=active&limit=1", "/admin/clients", nil, "").(map[string]any)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != id {
		t.Fatal(items)
	}
	accountFailure(t, owner, "GET", "/admin/clients?status=unknown", nil, 400)
	accountFailure(t, owner, "GET", "/admin/clients/"+f.admin.ID, nil, 404)
	accountHTTP(t, owner, "POST", "/admin/clients/"+id+"/password-reset", "/admin/clients/{id}/password-reset", map[string]any{}, "")
	queuedLink(t, a, m, id, "password_reset")
	accountHTTP(t, owner, "POST", "/admin/clients/"+id+"/revoke-sessions", "/admin/clients/{id}/revoke-sessions", map[string]any{}, "")
	accountFailure(t, c, "GET", "/account", nil, 401)
	c = client(t, srv.URL, in["email"].(string), "Managed password!2026")
	accountHTTP(t, c, "POST", "/account/api-tokens", "/account/api-tokens", map[string]any{"name": "API", "scopes": []string{"read"}, "ip_allowlist": []string{}}, "managed-token-1")
	v = accountHTTP(t, owner, "PATCH", "/admin/clients/"+id, "/admin/clients/{id}", map[string]any{"revision": v["revision"], "status": "closed"}, "").(map[string]any)
	if v["status"] != "closed" {
		t.Fatal(v)
	}
	accountFailure(t, c, "GET", "/account", nil, 401)
	for _, table := range []string{"sessions", "api_tokens", "email_tokens"} {
		var n int
		if err := m.DB.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE user_id=?", id).Scan(&n); err != nil || n != 0 {
			t.Fatal(table, n, err)
		}
	}
	v = accountHTTP(t, owner, "PATCH", "/admin/clients/"+id, "/admin/clients/{id}", map[string]any{"revision": v["revision"], "status": "active"}, "").(map[string]any)
	if v["status"] != "active" {
		t.Fatal(v)
	}
	client(t, srv.URL, in["email"].(string), "Managed password!2026")
}

func TestMySQLClientAdminCreditAndPermissionNegatives(t *testing.T) {
	f, _, srv := accountServer(t)
	owner := client(t, srv.URL, f.admin.Email, "AccountFixture!2026")
	path := "/admin/clients/" + f.customer.ID
	in := map[string]any{"amount_minor": 500, "reason": "测试款"}
	for range 2 {
		v := accountHTTP(t, owner, "POST", path+"/credit", "/admin/clients/{id}/credit", in, "client-credit-1").(map[string]any)
		if v["credit"].(map[string]any)["amount_minor"] != float64(500) {
			t.Fatal(v)
		}
	}
	accountHTTP(t, owner, "GET", path+"/credit", "/admin/clients/{id}/credit", nil, "")
	role := accountHTTP(t, owner, "POST", "/admin/roles", "/admin/roles", map[string]any{"name": "Support", "description": "No client administration", "permissions": []string{"tickets.read"}}, "").(map[string]any)
	accountHTTP(t, owner, "PATCH", "/admin/staff/"+f.otherAdmin.ID, "/admin/staff/{id}", map[string]any{"revision": 1, "role_id": role["id"]}, "")
	limited := client(t, srv.URL, f.otherAdmin.Email, "AccountFixture!2026")
	for _, route := range []struct{ method, path string }{{"GET", "/admin/clients"}, {"POST", "/admin/clients"}, {"GET", path}, {"PATCH", path}, {"POST", path + "/password-reset"}, {"POST", path + "/revoke-sessions"}, {"POST", path + "/credit"}, {"GET", path + "/credit"}} {
		accountFailure(t, limited, route.method, route.path, map[string]any{}, 403)
	}
	c := client(t, srv.URL, f.customer.Email, "AccountFixture!2026")
	accountFailure(t, c, "GET", path, nil, 403)
	status, _ := owner.request("POST", "/api/v1/admin/clients/"+f.admin.ID+"/credit", in, "staff-credit-1")
	if status != 404 {
		t.Fatal("staff account accepted as a client", status)
	}
}
