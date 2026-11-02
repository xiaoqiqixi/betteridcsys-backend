package billing

import (
	"aster/billing/internal/contracttest"
	api "aster/billing/internal/financeapi"
	"aster/billing/serviceapi"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMySQLCommerceServiceActionsContractOwnershipAndSecrets(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	sid, p := paidService06(t, f)
	activateServiceFixture06(t, m, sid)
	controller, runtime := serviceControllerFixture06(t, m, f.customer.ID, sid)
	image := ID()
	if _, err := m.DB.Exec(`INSERT INTO os_images(id,family,name,version,min_disk_gb,enabled,controller_id,remote_image_id,sha256) VALUES(?,'debian','Debian','12',10,TRUE,?,'debian-12',?)`, image, runtime.IDs[0], strings.Repeat("1", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DB.Exec("INSERT INTO product_os_images(product_id,image_id) VALUES(?,?)", p.ID, image); err != nil {
		t.Fatal(err)
	}
	app := NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), CommerceContractVersion: "0.6", Services: runtime})
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	customer := client(t, server.URL, f.customer.Email, "CatalogFixture!2026")
	admin := client(t, server.URL, f.admin.Email, "CatalogFixture!2026")
	password := "Ephemeral-Service-Password!2026"
	cases := []struct {
		method, path, template, power string
		input                         any
	}{
		{"POST", "/power", "/services/{id}/power", "stopped", map[string]string{"action": "start"}},
		{"POST", "/power", "/services/{id}/power", "running", map[string]string{"action": "stop"}},
		{"POST", "/power", "/services/{id}/power", "running", map[string]string{"action": "reboot"}},
		{"POST", "/power", "/services/{id}/power", "running", map[string]string{"action": "force_off"}},
		{"POST", "/reinstall", "/services/{id}/reinstall", "running", api.ReinstallServiceInput{OSImageID: image, RootPassword: password, Confirm: sid}},
		{"POST", "/reset-password", "/services/{id}/reset-password", "running", api.ResetServicePasswordInput{RootPassword: password}},
		{"POST", "/snapshots", "/services/{id}/snapshots", "stopped", api.CreateSnapshotInput{Name: "before-change"}},
		{"POST", "/snapshots/" + strings.Repeat("b", 32) + "/restore", "/services/{id}/snapshots/{snapshot_id}/restore", "stopped", api.RestoreSnapshotInput{Confirm: sid}},
		{"DELETE", "/snapshots/" + strings.Repeat("b", 32), "/services/{id}/snapshots/{snapshot_id}", "stopped", nil},
	}
	for _, tc := range cases {
		controller.mu.Lock()
		controller.view.Power = tc.power
		before := controller.sends
		controller.mu.Unlock()
		key := ID()
		status, body := admin.request(tc.method, "/api/v1/services/"+sid+tc.path, tc.input, key)
		if status != 404 {
			t.Fatal(tc.path, status, body)
		}
		contracttest.Error(t, body)
		status, body = customer.request(tc.method, "/api/v1/services/"+sid+tc.path, tc.input, key)
		if status != 200 {
			t.Fatal(tc.path, status, body)
		}
		contracttest.Response(t, tc.method, tc.template, body)
		id := body["data"].(map[string]any)["id"].(string)
		status, body = customer.request(tc.method, "/api/v1/services/"+sid+tc.path, tc.input, key)
		if status != 200 {
			t.Fatal(status, body)
		}
		contracttest.Response(t, tc.method, tc.template, body)
		controller.mu.Lock()
		after := controller.sends
		controller.mu.Unlock()
		if after-before != 1 {
			t.Fatal("mutation repeated", after, before)
		}
		for range 2 {
			task, err := app.DispatchServiceTask06(t.Context(), id, true, true)
			if err != nil || task.Status != "succeeded" {
				t.Fatal(task, err)
			}
			contracttest.Data(t, "ServiceTask", map[string]any{"data": task})
		}
	}
	for _, tc := range []struct {
		method string
		body   any
	}{{"POST", api.EnterRescueInput{RootPassword: password}}, {"DELETE", nil}} {
		status, body := customer.request(tc.method, "/api/v1/services/"+sid+"/rescue", tc.body, ID())
		if status != 501 {
			t.Fatal(status, body)
		}
		contracttest.Error(t, body)
		status, body = admin.request(tc.method, "/api/v1/services/"+sid+"/rescue", tc.body, ID())
		if status != 404 {
			t.Fatal(status, body)
		}
		contracttest.Error(t, body)
	}
	for _, query := range []string{"SELECT CAST(payload AS CHAR) FROM service_tasks", "SELECT CAST(response AS CHAR) FROM commerce_requests"} {
		rows, err := m.DB.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var raw string
			if err = rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(raw, password) {
				t.Fatal("password persisted")
			}
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	// An uncertain write is a durable barrier. Replays return the first task;
	// dispatching it again performs no mutation, and only lookup can resolve it.
	controller.mu.Lock()
	controller.view.Power = "stopped"
	controller.sendErr = serviceapi.ErrUncertain
	before := controller.sends
	controller.mu.Unlock()
	key := ID()
	in := map[string]string{"action": "start"}
	status, body := customer.request("POST", "/api/v1/services/"+sid+"/power", in, key)
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/services/{id}/power", body)
	id := body["data"].(map[string]any)["id"].(string)
	task, err := app.DispatchServiceTask06(t.Context(), id, false, true)
	if err != nil || task.Status != "uncertain" {
		t.Fatal(task, err)
	}
	status, body = customer.request("POST", "/api/v1/services/"+sid+"/power", in, key)
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/services/{id}/power", body)
	status, body = customer.request("POST", "/api/v1/services/"+sid+"/power", map[string]string{"action": "reboot"}, key)
	if status != 409 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
	controller.mu.Lock()
	after := controller.sends
	controller.mu.Unlock()
	if after-before != 1 {
		t.Fatal("uncertain mutation repeated")
	}
	task, err = app.DispatchServiceTask06(t.Context(), id, true, true)
	if err != nil || task.Status != "succeeded" {
		t.Fatal(task, err)
	}
}

func TestMySQLCommerceManualServiceActionsAndPermissions(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	sid, _ := paidService06(t, f)
	app := NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), CommerceContractVersion: "0.6"})
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	admin := client(t, server.URL, f.admin.Email, "CatalogFixture!2026")
	customer := client(t, server.URL, f.customer.Email, "CatalogFixture!2026")
	for _, action := range []string{"provision", "suspend", "sync", "unsuspend", "terminate"} {
		input := api.AdminServiceActionInput{Action: action, Reason: "Reviewed by staff", Confirm: sid}
		key := ID()
		status, body := customer.request("POST", "/api/v1/admin/services/"+sid+"/actions", input, key)
		if status != 403 {
			t.Fatal(action, status, body)
		}
		contracttest.Error(t, body)
		for range 2 {
			status, body = admin.request("POST", "/api/v1/admin/services/"+sid+"/actions", input, key)
			if status != 200 {
				t.Fatal(action, status, body)
			}
			contracttest.Response(t, "POST", "/admin/services/{id}/actions", body)
		}
	}
	s, err := m.Service06(t.Context(), f.customer.ID, sid)
	if err != nil || s.Status != "terminated" {
		t.Fatal(s, err)
	}
	var active, suspended, terminated int
	if err = m.DB.QueryRow("SELECT SUM(template_key='service_active'),SUM(template_key='service_suspended'),SUM(template_key='service_terminated') FROM email_outbox WHERE user_id=?", f.customer.ID).Scan(&active, &suspended, &terminated); err != nil || active != 2 || suspended != 1 || terminated != 1 {
		t.Fatal(active, suspended, terminated, err)
	}
	status, body := admin.request("POST", "/api/v1/admin/services/"+sid+"/actions", api.AdminServiceActionInput{Action: "unsuspend"}, ID())
	if status != 409 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
}
func TestMySQLCommerceProvisionClaimConcurrencyAndOverdueRace(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	sid, _ := paidService06(t, f)
	controller, runtime := serviceControllerFixture06(t, m, f.customer.ID, sid)
	if _, err := m.DB.Exec(`UPDATE services SET status='provisioning',resource_id=NULL,configuration_snapshot=JSON_SET(configuration_snapshot,'$.remote_product_id','approved-preset') WHERE id=?`, sid); err != nil {
		t.Fatal(err)
	}
	var taskID string
	if err := m.DB.QueryRow("SELECT id FROM service_tasks WHERE service_id=? AND action='provision'", sid).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	app := NewApp(m, Config{Services: runtime})
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := app.DispatchServiceTask06(t.Context(), taskID, false, false)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		var problem *apiProblem
		if err != nil && !errors.As(err, &problem) {
			t.Fatal(err)
		}
	}
	controller.mu.Lock()
	sent := controller.provisions
	request := controller.lastRequest
	controller.mu.Unlock()
	if sent != 1 || request.OrderID != sid || request.OwnerRef != f.customer.ID {
		t.Fatal("bad provisioning cardinality or owner", sent, request)
	}
	task, err := app.DispatchServiceTask06(t.Context(), taskID, true, true)
	if err != nil || task.Status != "succeeded" {
		t.Fatal(task, err)
	}
	s, err := m.Service06(t.Context(), f.customer.ID, sid)
	if err != nil || s.Status != "active" || s.ResourceID == nil {
		t.Fatal(s, err)
	}
	// Model a renewal committing while an accepted overdue suspension is running.
	payload, _ := json.Marshal(serviceActionPayload06{Version: 1, Reason: "overdue", Source: "automation"})
	suspend := ID()
	now := time.Now().UTC()
	if _, err = m.DB.Exec("INSERT INTO service_tasks(id,service_id,action,status,operation_id,payload,created_at,updated_at) VALUES(?,?,'suspend','running',?,?,?,?)", suspend, sid, suspend, payload, now, now); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err = m.finishServiceTask06(t.Context(), suspend, "succeeded", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = m.DB.QueryRow("SELECT COUNT(*) FROM service_tasks WHERE service_id=? AND action='unsuspend' AND status='queued'", sid).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if _, err = m.DB.Exec("UPDATE services SET suspension_reason='administrator' WHERE id=?", sid); err != nil {
		t.Fatal(err)
	}
	var unsuspend string
	if err = m.DB.QueryRow("SELECT id FROM service_tasks WHERE service_id=? AND action='unsuspend'", sid).Scan(&unsuspend); err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	controller.view.Suspended = true
	controller.mu.Unlock()
	task, err = app.DispatchServiceTask06(t.Context(), unsuspend, false, false)
	if err != nil || task.Status != "failed" {
		t.Fatal(task, err)
	}
	s, err = m.Service06(t.Context(), f.customer.ID, sid)
	if err != nil || s.Status != "suspended" {
		t.Fatal("administrator suspension lifted", s, err)
	}
	// Free-form customer reasons must not turn a cancellation into an overdue job.
	cancelled, err := m.CancelService06(t.Context(), f.customer, sid, &api.RequestCancellationInput{Type: "immediate", Reason: "overdue"})
	if err != nil {
		t.Fatal(err)
	}
	contracttest.Data(t, "Service", map[string]any{"data": cancelled})
	var terminate string
	if err = m.DB.QueryRow("SELECT id FROM service_tasks WHERE service_id=? AND action='terminate' AND status='queued'", sid).Scan(&terminate); err != nil {
		t.Fatal(err)
	}
	task, err = app.DispatchServiceTask06(t.Context(), terminate, false, false)
	if err != nil || task.Status != "running" {
		t.Fatal("customer cancellation was superseded by its reason", task, err)
	}
	contracttest.Data(t, "ServiceTask", map[string]any{"data": task})
	task, err = app.DispatchServiceTask06(t.Context(), terminate, true, true)
	if err != nil || task.Status != "succeeded" {
		t.Fatal(task, err)
	}
	contracttest.Data(t, "ServiceTask", map[string]any{"data": task})
	s, err = m.Service06(t.Context(), f.customer.ID, sid)
	if err != nil || s.Status != "terminated" {
		t.Fatal(s, err)
	}
	contracttest.Data(t, "AdminService", map[string]any{"data": s})
}
