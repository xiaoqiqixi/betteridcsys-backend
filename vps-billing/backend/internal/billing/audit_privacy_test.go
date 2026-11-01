package billing

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCustomerAuditHidesStaffAcrossAllViews(t *testing.T) {
	m := NewMemory()
	owner, _, admin := cancellationUsers(t, m)
	o, _, _ := cancellationOrder(t, m, owner)
	secret := "staff-only investigation reason"
	m.audits = append(m.audits, Audit{ID: ID(), UserID: owner.ID, Action: "account.updated", Target: o.ID, ActorEmail: admin.Email, Detail: secret, CreatedAt: time.Now().UTC()})
	srv := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()}).Handler())
	defer srv.Close()
	customer := client(t, srv.URL, owner.Email, "DemoCustomer!2026")
	for _, path := range []string{"/api/v1/audit", "/api/v1/audit-events", "/api/v1/orders/" + o.ID} {
		status, out := customer.request("GET", path, nil, "")
		raw, _ := json.Marshal(out)
		if status != 200 || strings.Contains(string(raw), admin.Email) || strings.Contains(string(raw), secret) || !strings.Contains(string(raw), `"actor_type":"staff"`) {
			t.Fatal(path, status, string(raw))
		}
	}
	if status, _ := customer.request("GET", "/api/v1/audit-events?actor_email="+admin.Email, nil, ""); status != 400 {
		t.Fatal("staff identity filter oracle", status)
	}
	staff := client(t, srv.URL, admin.Email, "DemoAdmin!2026")
	for _, path := range []string{"/api/v1/audit", "/api/v1/admin/audit-events", "/api/v1/admin/orders/" + o.ID} {
		status, out := staff.request("GET", path, nil, "")
		raw, _ := json.Marshal(out)
		if status != 200 || !strings.Contains(string(raw), admin.Email) || !strings.Contains(string(raw), secret) {
			t.Fatal("admin history redacted", path, status, string(raw))
		}
	}
	events, _ := m.Audits(context.Background(), owner.ID)
	if events[0].Detail != secret {
		t.Fatal("stored audit mutated")
	}
}
