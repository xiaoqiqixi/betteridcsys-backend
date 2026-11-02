package billing

import (
	"aster/billing/internal/contracttest"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func auditCurrentContract(t *testing.T, f auditQueryFixture) {
	t.Helper()
	before := f.raw()
	app := NewApp(f.store, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), CommerceContractVersion: "0.6"})
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	guest := client(t, server.URL, "", "")
	customer := client(t, server.URL, f.users[0].Email, "AuditFixture!2026")
	admin := client(t, server.URL, f.users[2].Email, "AuditFixture!2026")
	for _, tc := range []struct {
		client *testClient
		status int
	}{{guest, 401}, {customer, 403}} {
		if status, body := tc.client.request("GET", "/api/v1/admin/audit-events", nil, ""); status != tc.status {
			t.Fatal("unauthorized current audit read", status, body)
		}
	}
	stored := map[string]Audit{}
	for _, row := range before {
		stored[row.ID] = row
	}
	seen := map[string]bool{}
	path := "/api/v1/admin/audit-events?limit=100"
	for {
		status, body := admin.request("GET", path, nil, "")
		if status != 200 {
			t.Fatal(status, body)
		}
		contracttest.Response(t, "GET", "/admin/audit-events", body)
		data := body["data"].(map[string]any)
		items := data["items"].([]any)
		if len(items) > 100 {
			t.Fatal("unbounded audit page")
		}
		for _, item := range items {
			event := item.(map[string]any)
			id := event["id"].(string)
			row, ok := stored[id]
			if !ok || seen[id] || event["action"] != row.Action || event["reason"] != row.Detail || event["target_id"] != row.Target {
				t.Fatal("audit history changed or duplicated", event)
			}
			seen[id] = true
			if strings.HasPrefix(row.ActorEmail, "payment:") {
				if event["actor_type"] != "system" || event["actor_email"] != nil {
					t.Fatal("system actor", event)
				}
			} else if event["actor_email"] != row.ActorEmail || event["actor_type"] != "staff" {
				t.Fatal("historical staff identity changed", event)
			}
		}
		if data["next_cursor"] == nil {
			break
		}
		cursor := data["next_cursor"].(string)
		if status, _ := admin.request("GET", "/api/v1/admin/audit-events?limit=99&cursor="+url.QueryEscape(cursor), nil, ""); status != 400 {
			t.Fatal("cursor escaped its page-size binding", status)
		}
		path = "/api/v1/admin/audit-events?limit=100&cursor=" + url.QueryEscape(cursor)
	}
	if len(seen) != len(before)-1 {
		t.Fatal("missing historical rows or included future event", len(seen))
	}
	status, empty := admin.request("GET", "/api/v1/admin/audit-events?action=absent.event", nil, "")
	if status != 200 {
		t.Fatal(status, empty)
	}
	contracttest.Response(t, "GET", "/admin/audit-events", empty)
	data := empty["data"].(map[string]any)
	if len(data["items"].([]any)) != 0 || data["next_cursor"] != nil {
		t.Fatal("empty page", data)
	}
	if !reflect.DeepEqual(before, f.raw()) {
		t.Fatal("audit read rewrote historical rows")
	}
}

func TestAuditEventsCurrentFrontendContract(t *testing.T) {
	auditCurrentContract(t, newAuditQueryFixture(t, false))
}
func TestMySQLAuditEventsCurrentFrontendContract(t *testing.T) {
	auditCurrentContract(t, newAuditQueryFixture(t, true))
}
