package billing

import (
	"aster/billing/internal/staffaccess"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func runStaffPermissionContract(t *testing.T, f accountFixture) {
	t.Helper()
	a := NewApp(f.Store, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()})
	token := ID() + ID()
	if err := f.Store.PutSession(f.ctx, Session{Hash: digest(token), UserID: f.admin.ID, Revision: f.admin.Revision, Expires: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	set := func(grants []string) {
		t.Helper()
		switch m := f.Store.(type) {
		case *Memory:
			m.mu.Lock()
			m.staffPolicies[f.admin.ID] = staffPolicy{RoleID: "restricted", Permissions: grants}
			m.mu.Unlock()
		case *MySQL:
			raw, _ := json.Marshal(grants)
			if _, err := m.DB.ExecContext(f.ctx, `INSERT INTO staff_roles(id,name,description,permissions,builtin,revision) VALUES('restricted','Restricted','',?,FALSE,1) ON DUPLICATE KEY UPDATE permissions=?`, raw, raw); err != nil {
				t.Fatal(err)
			}
			if _, err := m.DB.ExecContext(f.ctx, "UPDATE staff_members SET role_id='restricted' WHERE user_id=?", f.admin.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	request := func(perm, bearer string) int {
		r := httptest.NewRequest("GET", "http://localhost/api/v1/admin/test", nil)
		r.AddCookie(&http.Cookie{Name: a.cookieName(), Value: token})
		r.Header.Set("Authorization", bearer)
		w := httptest.NewRecorder()
		a.staff(perm, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })(w, r)
		return w.Code
	}
	for _, perm := range staffaccess.Permissions() {
		if got := request(perm, ""); got != 204 {
			t.Fatalf("owner %s: %d", perm, got)
		}
	}
	if got := request("unknown.permission", ""); got != 403 {
		t.Fatal("owner authorized unknown permission", got)
	}
	for _, perm := range staffaccess.Permissions() {
		set([]string{perm})
		if got := request(perm, ""); got != 204 {
			t.Fatalf("granted %s: %d", perm, got)
		}
		set([]string{})
		if got := request(perm, ""); got != 403 {
			t.Fatalf("live revocation %s: %d", perm, got)
		}
	}
	set(staffaccess.Permissions())
	if got := request("staff.write", "Bearer ast_forbidden"); got != 403 {
		t.Fatal("bearer reached staff handler", got)
	}
	// Exercise the actual legacy router too, including reads and mutations.
	set([]string{})
	for _, path := range []string{"overview", "orders", "invoices", "services", "customers", "customer-accounts", "catalog", "catalog-taxonomy", "currencies", "plan-prices", "email-settings", "updates", "plugins", "integrations", "audit-events", "site-settings"} {
		for _, method := range []string{"GET", "POST"} {
			r := httptest.NewRequest(method, "http://localhost/api/v1/admin/"+path, nil)
			r.AddCookie(&http.Cookie{Name: a.cookieName(), Value: token})
			r.Header.Set("X-CSRF-Token", a.csrf(token))
			w := httptest.NewRecorder()
			a.Handler().ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("%s %s bypassed guard: %d", method, path, w.Code)
			}
		}
	}
}

func TestStaffPermissionsLiveMemory(t *testing.T) {
	runStaffPermissionContract(t, memoryAccountFixture(t))
}
func TestMySQLStaffPermissionsLive(t *testing.T) {
	runStaffPermissionContract(t, mysqlAccountFixture(t))
}

func TestMySQLStaffMigrationAndLastOwner(t *testing.T) {
	f := mysqlAccountFixture(t)
	m := f.Store.(*MySQL)
	if err := m.Migrate(f.ctx); err != nil {
		t.Fatal("repeat migration", err)
	}
	if status, err := m.SchemaStatus(f.ctx); err != nil || status.Status != "current" {
		t.Fatal("staff preflight", status, err)
	}
	if _, err := m.DB.ExecContext(f.ctx, `INSERT INTO staff_roles(id,name,description,permissions,builtin,revision) VALUES('staff-manager','Manager','',JSON_ARRAY('staff.write'),FALSE,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DB.ExecContext(f.ctx, "UPDATE staff_members SET role_id='staff-manager' WHERE user_id=?", f.otherAdmin.ID); err != nil {
		t.Fatal(err)
	}
	ctx := accountWriteTestContext(t, m, f.ctx, f.otherAdmin.ID)
	_, err := m.WriteCustomerAccount(ctx, f.otherAdmin.ID, accountWrite{Operation: "update", ID: f.admin.ID, Revision: 1, Name: f.admin.Name, Role: "admin", Disabled: true, Reason: "test last owner"})
	if err != ErrLastActiveAdmin {
		t.Fatal("custom-role staff disabled the final owner", err)
	}
}
