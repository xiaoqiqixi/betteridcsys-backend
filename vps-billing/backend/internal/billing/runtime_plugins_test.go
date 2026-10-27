package billing

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRuntimePluginAdminGuardsAndUnavailableDefaults(t *testing.T) {
	app := NewApp(NewMemory(), Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()})
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	anonymous := client(t, srv.URL, "", "")
	customer := client(t, srv.URL, "demo@aster.local", "DemoCustomer!2026")
	admin := client(t, srv.URL, "admin@aster.local", "DemoAdmin!2026")
	for _, tc := range []struct {
		c    *testClient
		want int
	}{{anonymous, 401}, {customer, 403}} {
		for _, endpoint := range []struct{ method, path string }{{"GET", "/api/v1/admin/plugins"}, {"POST", "/api/v1/admin/plugins/check"}, {"POST", "/api/v1/admin/plugins/payment/check"}} {
			if status, _ := tc.c.request(endpoint.method, endpoint.path, map[string]any{}, ""); status != tc.want {
				t.Fatal("authorization", status, tc.want)
			}
		}
	}
	if status, _ := admin.request("GET", "/api/v1/admin/plugins/check", nil, ""); status != 405 {
		t.Fatal(status)
	}
	if status, _ := admin.request("POST", "/api/v1/admin/plugins", map[string]any{}, ""); status != 405 {
		t.Fatal(status)
	}
	if status, _ := admin.request("POST", "/api/v1/admin/plugins/payment/check", map[string]any{}, ""); status != 503 {
		t.Fatal("unavailable payment worker", status)
	}
	old := admin.csrf
	admin.csrf = "invalid"
	if status, _ := admin.request("POST", "/api/v1/admin/plugins/check", map[string]any{}, ""); status != 403 {
		t.Fatal("csrf", status)
	}
	if status, _ := admin.request("POST", "/api/v1/admin/plugins/payment/check", map[string]any{}, ""); status != 403 {
		t.Fatal("payment csrf", status)
	}
	admin.csrf = old
	for _, raw := range []string{`null`, `[]`, `{"execute":"/tmp/file"}`, `{} {}`, `{"version":"1.0.0","Version":"1.0.1"}`} {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/admin/plugins/check", strings.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", admin.csrf)
		res, err := admin.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatal("invalid request accepted", raw, res.StatusCode)
		}
	}
	status, response := admin.request("GET", "/api/v1/admin/plugins", nil, "")
	if status != 200 {
		t.Fatal(status, response)
	}
	data := response["data"].(map[string]any)
	if len(data["items"].([]any)) != 0 || data["smtp"].(map[string]any)["status"] != "not_installed" {
		t.Fatal(response)
	}
	status, response = admin.request("POST", "/api/v1/admin/plugins/check", map[string]any{}, "")
	if status != 503 || response["error"].(map[string]any)["code"] != "PLUGIN_UNAVAILABLE" {
		t.Fatal(status, response)
	}
	encoded, _ := json.Marshal(response)
	if bytes.Contains(encoded, []byte("/etc/")) || bytes.Contains(encoded, []byte("/opt/")) {
		t.Fatal("internal path leaked")
	}
	status, response = admin.request("GET", "/api/v1/admin/email-settings", nil, "")
	if status != 200 || response["data"].(map[string]any)["plugin_available"] != false || response["data"].(map[string]any)["plugin_status"] != "not_installed" {
		t.Fatal(status, response)
	}
}
