package billing

import (
	"aster/billing/internal/contracttest"
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestMySQLSupportContractOwnershipNotesAndAssignment(t *testing.T) {
	f, _, srv := accountServer(t)
	m := f.Store.(*MySQL)
	c := client(t, srv.URL, f.customer.Email, "AccountFixture!2026")
	admin := client(t, srv.URL, f.admin.Email, "AccountFixture!2026")
	other := client(t, srv.URL, f.otherAdmin.Email, "AccountFixture!2026")
	accountHTTP(t, c, "GET", "/support/departments", "/support/departments", nil, "")
	accountHTTP(t, admin, "GET", "/admin/departments", "/admin/departments", nil, "")
	input := map[string]any{"department_id": "technical", "subject": "Unable to connect", "priority": "high", "body": "The server does not accept SSH connections."}
	ticket := accountHTTP(t, c, "POST", "/tickets", "/tickets", input, "ticket-open-first").(map[string]any)
	id := ticket["id"].(string)
	replay := accountHTTP(t, c, "POST", "/tickets", "/tickets", input, "ticket-open-first").(map[string]any)
	if replay["id"] != id {
		t.Fatal("ticket duplicated")
	}
	accountHTTP(t, c, "GET", "/tickets", "/tickets", nil, "")
	accountHTTP(t, c, "GET", "/tickets/"+id, "/tickets/{id}", nil, "")
	list := accountHTTP(t, other, "GET", "/tickets", "/tickets", nil, "").(map[string]any)
	if len(list["items"].([]any)) != 0 {
		t.Fatal("foreign tickets listed")
	}
	for _, v := range []struct {
		method, path string
		body         any
	}{{"GET", "/tickets/" + id, nil}, {"POST", "/tickets/" + id + "/close", map[string]any{}}, {"POST", "/tickets/" + id + "/replies", map[string]any{"body": "Foreign reply"}}} {
		status, out := other.request(v.method, "/api/v1"+v.path, v.body, "foreign-reply")
		if status != 404 {
			t.Fatal("ticket ownership", v.path, status, out)
		}
		contracttest.Error(t, out)
	}
	accountHTTP(t, admin, "GET", "/admin/tickets?status=open&department_id=technical&q=connect", "/admin/tickets", nil, "")
	accountHTTP(t, admin, "GET", "/admin/tickets/"+id, "/admin/tickets/{id}", nil, "")
	assigned := accountHTTP(t, admin, "PATCH", "/admin/tickets/"+id, "/admin/tickets/{id}", map[string]any{"status": "on_hold", "priority": "urgent", "department_id": "billing", "assignee_id": f.otherAdmin.ID}, "").(map[string]any)
	if assigned["assignee_id"] != f.otherAdmin.ID || assigned["priority"] != "urgent" {
		t.Fatal(assigned)
	}
	accountFailure(t, admin, "PATCH", "/admin/tickets/"+id, map[string]any{"assignee_id": f.customer.ID}, 404)
	privateBody := "PRIVATE NOTE: staff diagnostic details"
	note := accountHTTP(t, admin, "POST", "/admin/tickets/"+id+"/replies", "/admin/tickets/{id}/replies", map[string]any{"body": privateBody, "internal": true}, "internal-note-first").(map[string]any)
	if len(note["messages"].([]any)) != 2 || note["last_reply_at"] != assigned["last_reply_at"] {
		t.Fatal("internal note changed public activity", note)
	}
	for _, path := range []string{"/tickets/" + id, "/tickets", "/account/activity"} {
		_, out := c.request("GET", "/api/v1"+path, nil, "")
		raw, _ := json.Marshal(out)
		if bytes.Contains(raw, []byte(privateBody)) || bytes.Contains(raw, []byte("internal_note")) {
			t.Fatal("internal note leaked", path, string(raw))
		}
	}
	var count int
	if err := m.DB.QueryRow("SELECT COUNT(*) FROM email_outbox WHERE user_id=?", f.customer.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("internal email leak", count, err)
	}
	reply := map[string]any{"body": "We restored connectivity. Please try again.", "internal": false}
	accountHTTP(t, admin, "POST", "/admin/tickets/"+id+"/replies", "/admin/tickets/{id}/replies", reply, "staff-reply-first")
	public := accountHTTP(t, c, "GET", "/tickets/"+id, "/tickets/{id}", nil, "").(map[string]any)
	if len(public["messages"].([]any)) != 2 || public["status"] != "answered" {
		t.Fatal(public)
	}
	for _, message := range public["messages"].([]any) {
		if message.(map[string]any)["internal"] != false {
			t.Fatal("internal message returned")
		}
	}
	accountHTTP(t, c, "POST", "/tickets/"+id+"/replies", "/tickets/{id}/replies", map[string]any{"body": "Connection works again. Thank you."}, "client-reply-first")
	closed := accountHTTP(t, c, "POST", "/tickets/"+id+"/close", "/tickets/{id}/close", map[string]any{}, "").(map[string]any)
	if closed["status"] != "closed" {
		t.Fatal(closed)
	}
	unassigned := accountHTTP(t, admin, "PATCH", "/admin/tickets/"+id, "/admin/tickets/{id}", map[string]any{"assignee_id": nil}, "").(map[string]any)
	if unassigned["assignee_id"] != nil || unassigned["assignee_name"] != nil {
		t.Fatal("unassign failed", unassigned)
	}
	// The service association is checked independently of the ticket owner.
	sid := ID()
	if _, err := m.DB.Exec("INSERT INTO services(id,user_id,name,plan_name,region,ipv4,status,next_due_at,price_minor,currency,cpu,memory_gb,disk_gb) VALUES(?,?,'foreign','Example','Region','','active',UTC_TIMESTAMP(6),100,'CNY',1,1,20)", sid, f.otherAdmin.ID); err != nil {
		t.Fatal(err)
	}
	input["service_id"] = sid
	status, _ := c.request("POST", "/api/v1/tickets", input, "foreign-service-ticket")
	if status != 404 {
		t.Fatal("foreign service associated", status)
	}
}
func TestMySQLSupportPermissionNegatives(t *testing.T) {
	f, _, srv := accountServer(t)
	owner := client(t, srv.URL, f.admin.Email, "AccountFixture!2026")
	role := accountHTTP(t, owner, "POST", "/admin/roles", "/admin/roles", map[string]any{"name": "No support", "description": "No permissions", "permissions": []string{}}, "").(map[string]any)
	accountHTTP(t, owner, "PATCH", "/admin/staff/"+f.otherAdmin.ID, "/admin/staff/{id}", map[string]any{"revision": 1, "role_id": role["id"]}, "")
	limited := client(t, srv.URL, f.otherAdmin.Email, "AccountFixture!2026")
	for _, v := range []struct{ method, path string }{{"GET", "/admin/departments"}, {"GET", "/admin/tickets"}, {"GET", "/admin/tickets/" + ID()}, {"PATCH", "/admin/tickets/" + ID()}, {"POST", "/admin/tickets/" + ID() + "/replies"}} {
		status, out := limited.request(v.method, "/api/v1"+v.path, map[string]any{}, "denied-support")
		if status != 403 {
			t.Fatal(v, status, out)
		}
		contracttest.Error(t, out)
	}
}
func TestMySQLTicketAutocloseUsesSettingsAndScheduler(t *testing.T) {
	f, a, srv := accountServer(t)
	m := f.Store.(*MySQL)
	c := client(t, srv.URL, f.customer.Email, "AccountFixture!2026")
	input := map[string]any{"department_id": "technical", "subject": "Autoclose test", "priority": "normal", "body": "Please check this service for me."}
	closed := accountHTTP(t, c, "POST", "/tickets", "/tickets", input, "autoclose-answered").(map[string]any)
	pending := accountHTTP(t, c, "POST", "/tickets", "/tickets", input, "autoclose-pending").(map[string]any)
	recent := accountHTTP(t, c, "POST", "/tickets", "/tickets", input, "autoclose-recent").(map[string]any)
	if _, err := m.DB.Exec("UPDATE tickets SET status='answered',last_reply_at=DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 100 HOUR) WHERE id=?", closed["id"]); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DB.Exec("UPDATE tickets SET last_reply_at=DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 100 HOUR) WHERE id=?", pending["id"]); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DB.Exec("UPDATE tickets SET status='answered' WHERE id=?", recent["id"]); err != nil {
		t.Fatal(err)
	}
	settings, err := m.AutomationSettings(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.TicketAutocloseHours = 48
	if _, err = m.UpdateAutomation(f.ctx, f.admin, settings); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	job := a.Automation.jobs["close_tickets"]
	if job == nil {
		t.Fatal("ticket job not registered")
	}
	for i := 0; i < 2; i++ {
		ran, err := m.RunAutomationJob(f.ctx, "close_tickets", now, job)
		if err != nil || ran != (i == 0) {
			t.Fatal("scheduler duplicate", ran, err)
		}
	}
	for id, want := range map[string]string{closed["id"].(string): "closed", pending["id"].(string): "open", recent["id"].(string): "answered"} {
		var got string
		if err = m.DB.QueryRow("SELECT status FROM tickets WHERE id=?", id).Scan(&got); err != nil || got != want {
			t.Fatal(id, got, want, err)
		}
	}
	// Internal outbox runs do not expand the contract's public automation enum.
	if _, err = m.RunAutomationJob(f.ctx, "email_outbox", now, a.Automation.jobs["email_outbox"]); err != nil {
		t.Fatal(err)
	}
	admin := client(t, srv.URL, f.admin.Email, "AccountFixture!2026")
	accountHTTP(t, admin, "GET", "/admin/automation/runs", "/admin/automation/runs", nil, "")
}
