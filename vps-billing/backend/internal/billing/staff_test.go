package billing

import (
	"aster/billing/internal/contracttest"
	"net/http/httptest"
	"sync"
	"testing"
)

func runStaffHTTP(t *testing.T, f accountFixture, password string) {
	a := NewApp(f.Store, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()})
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	c := client(t, srv.URL, f.admin.Email, password)
	check := func(method, path, contract string, input any) map[string]any {
		t.Helper()
		status, out := c.request(method, path, input, "")
		if status != 200 {
			t.Fatalf("%s %s: %d %v", method, path, status, out)
		}
		contracttest.Response(t, method, contract, out)
		return out
	}
	check("GET", "/api/v1/admin/roles", "/admin/roles", nil)
	check("GET", "/api/v1/admin/staff", "/admin/staff", nil)
	input := map[string]any{"name": "Support", "description": "Ticket access", "permissions": []string{"tickets.read"}}
	out := check("POST", "/api/v1/admin/roles", "/admin/roles", input)
	role := out["data"].(map[string]any)
	id := role["id"].(string)
	input["id"], input["revision"] = id, role["revision"]
	input["permissions"] = []string{"tickets.read", "tickets.write"}
	check("POST", "/api/v1/admin/roles", "/admin/roles", input)
	if status, out := c.request("POST", "/api/v1/admin/roles", input, ""); status != 409 || out["error"].(map[string]any)["code"] != "REVISION_CONFLICT" {
		t.Fatal("stale role", status, out)
	}
	input["id"], input["revision"] = "owner", 1
	if status, _ := c.request("POST", "/api/v1/admin/roles", input, ""); status != 409 {
		t.Fatal("owner mutable", status)
	}
	check("PATCH", "/api/v1/admin/staff/"+f.otherAdmin.ID, "/admin/staff/{id}", map[string]any{"revision": 1, "role_id": id})
	if status, out := c.request("PATCH", "/api/v1/admin/staff/"+f.admin.ID, map[string]any{"revision": 1, "status": "disabled"}, ""); status != 409 || out["error"].(map[string]any)["code"] != "LAST_ACTIVE_OWNER" {
		t.Fatal("last owner disabled", status, out)
	}
	if status, _ := c.request("PATCH", "/api/v1/admin/staff/"+f.admin.ID, map[string]any{"revision": 1, "role_id": id}, ""); status != 409 {
		t.Fatal("last owner demoted", status)
	}
	otherPassword := password
	if _, ok := f.Store.(*Memory); ok {
		otherPassword = "DemoAdmin!2026"
	}
	other := client(t, srv.URL, f.otherAdmin.Email, otherPassword)
	for _, v := range []struct {
		method, path string
		body         any
	}{{"GET", "/admin/roles", nil}, {"POST", "/admin/roles", input}, {"GET", "/admin/staff", nil}, {"PATCH", "/admin/staff/" + f.admin.ID, map[string]any{"revision": 1, "status": "disabled"}}} {
		if status, out := other.request(v.method, "/api/v1"+v.path, v.body, ""); status != 403 || out["error"].(map[string]any)["code"] != "FORBIDDEN" {
			t.Fatal("permission bypass", v, status, out)
		}
	}
	customerPassword := password
	if _, ok := f.Store.(*Memory); ok {
		customerPassword = "DemoCustomer!2026"
	}
	customer := client(t, srv.URL, f.customer.Email, customerPassword)
	if status, _ := customer.request("GET", "/api/v1/admin/staff", nil, ""); status != 403 {
		t.Fatal("customer read staff", status)
	}
}
func TestStaffHTTPMemory(t *testing.T) { runStaffHTTP(t, memoryAccountFixture(t), "DemoAdmin!2026") }
func TestMySQLStaffHTTP(t *testing.T)  { runStaffHTTP(t, mysqlAccountFixture(t), "AccountFixture!2026") }
func TestMySQLConcurrentLastOwner(t *testing.T) {
	f := mysqlAccountFixture(t)
	s := f.Store.(staffStore)
	var wg sync.WaitGroup
	revision := int64(1)
	status := "disabled"
	errs := make(chan error, 2)
	for _, id := range []string{f.admin.ID, f.otherAdmin.ID} {
		wg.Go(func() {
			_, err := s.UpdateStaffMember(f.ctx, id, id, staffMemberInput{Revision: &revision, Status: &status})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	accepted := 0
	for err := range errs {
		if err == nil {
			accepted++
		} else if err != errLastOwner {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatal("concurrent owner changes", accepted)
	}
	members, err := s.StaffMembers(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	for _, v := range members {
		if v.RoleID == "owner" && v.Status == "active" {
			active++
		}
	}
	if active != 1 {
		t.Fatal("no active owner", members)
	}
}
