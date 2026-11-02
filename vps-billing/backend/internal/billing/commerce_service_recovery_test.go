package billing

import (
	"aster/billing/internal/contracttest"
	api "aster/billing/internal/financeapi"
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMySQLCommerceServiceClaimLostAcknowledgementNeverDispatches(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	sid, _ := paidService06(t, f)
	activateServiceFixture06(t, m, sid)
	controller, runtime := serviceControllerFixture06(t, m, f.customer.ID, sid)
	view, err := m.Service06(t.Context(), f.customer.ID, sid)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, fault := storePaymentAckDatabase(t, cancellationSQLFixture{m: m, ctx: t.Context(), owner: f.customer, admin: f.admin})
	app := NewApp(wrapped, Config{Services: runtime, Secret: ID() + ID()})
	key := ID()
	input := api.PowerServiceInput{Action: "start"}
	fp := app.serviceActionFingerprint06(sid, "start:", input)
	fault.armed.Store(true)
	task, _, fresh, err := wrapped.reserveServiceAction06(t.Context(), f.customer, false, view, "start", serviceParams06{}, serviceActionPayload06{Source: "customer"}, key, fp)
	if err == nil || fresh || fault.injected.Load() != 1 || fault.committed.Load() != 1 {
		t.Fatal("ambiguous reservation authorized dispatch", fresh, err)
	}
	stored, err := readServiceTask06(t.Context(), m.DB, task.ID, "")
	if err != nil || stored.View.Status != "running" || stored.Payload.ReceiptKnown {
		t.Fatal(stored.View, err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/v1/services/"+sid+"/power", bytes.NewBufferString(`{"action":"start"}`))
	request.Header.Set("Idempotency-Key", key)
	request.Header.Set("Content-Type", "application/json")
	if !app.serviceActions06HTTP(response, request, f.customer, "/services/"+sid+"/power") || response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var body map[string]any
	if err = json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	contracttest.Response(t, "POST", "/services/{id}/power", body)
	if body["data"].(map[string]any)["id"] != task.ID {
		t.Fatal("reservation identity changed")
	}
	if _, err = app.DispatchServiceTask06(t.Context(), task.ID, false, true); err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	sent := controller.sends
	controller.mu.Unlock()
	if sent != 0 {
		t.Fatal("ambiguous commit reached controller")
	}
	if _, err = m.DB.Exec("UPDATE service_tasks SET updated_at=? WHERE id=?", time.Now().Add(-2*time.Minute), task.ID); err != nil {
		t.Fatal(err)
	}
	_, _ = app.DispatchServiceTask06(t.Context(), task.ID, true, true)
	stored, err = readServiceTask06(t.Context(), m.DB, task.ID, "")
	if err != nil || stored.View.Status != "uncertain" {
		t.Fatal("lost claim was not quarantined", stored.View, err)
	}
}

func TestMySQLCommerceAutomationProvisionBootstrapAndActivation(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	sid, _ := paidService06(t, f)
	controller, runtime := serviceControllerFixture06(t, m, f.customer.ID, sid)
	image := api.OsImage{ID: ID(), Family: "debian", Name: "Debian", Version: "12", MinDiskGB: 10}
	raw, _ := json.Marshal(image)
	if _, err := m.DB.Exec(`INSERT INTO os_images(id,family,name,version,min_disk_gb,enabled,controller_id,connection_id,remote_image_id,sha256) VALUES(?,'debian','Debian','12',10,TRUE,'',?,'debian-12',?)`, image.ID, "controller-test", strings.Repeat("1", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DB.Exec(`UPDATE services SET status='provisioning',resource_id=NULL,os_snapshot=?,configuration_snapshot=configuration_snapshot WHERE id=?`, raw, sid); err != nil {
		t.Fatal(err)
	}
	app := NewApp(m, Config{Services: runtime})
	job := app.Automation.jobs["dispatch_provisioning"]
	if job == nil {
		t.Fatal("dispatch not registered")
	}
	settings, err := m.AutomationSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(time.Minute)
	settings.AutoProvision = false
	if result, err := job(t.Context(), settings, at); err != nil || result.Failed != 0 {
		t.Fatal(result, err)
	}
	controller.mu.Lock()
	sent := controller.provisions
	controller.mu.Unlock()
	if sent != 0 {
		t.Fatal("auto_provision=false dispatched a new service")
	}
	settings.AutoProvision = true
	if result, err := job(t.Context(), settings, at.Add(time.Minute)); err != nil || result.Failed != 0 || result.Processed != 1 {
		t.Fatal(result, err)
	}
	s, err := m.Service06(t.Context(), f.customer.ID, sid)
	if err != nil || s.Status != "provisioning" || s.ResourceID != nil {
		t.Fatal("activated before setup", s, err)
	}
	if result, err := job(t.Context(), settings, at.Add(2*time.Minute)); err != nil || result.Failed != 0 {
		t.Fatal(result, err)
	}
	controller.mu.Lock()
	password := controller.lastParams.Password
	imageID := controller.lastParams.ImageID
	controller.mu.Unlock()
	if password == "" || imageID != "debian-12" {
		t.Fatal("setup did not use ephemeral credentials and selected image")
	}
	var payload string
	if err = m.DB.QueryRow("SELECT CAST(payload AS CHAR) FROM service_tasks WHERE service_id=? AND action='provision'", sid).Scan(&payload); err != nil || strings.Contains(payload, password) || strings.Contains(payload, `"password"`) {
		t.Fatal("setup credential persisted", err)
	}
	if result, err := job(t.Context(), settings, at.Add(3*time.Minute)); err != nil || result.Failed != 0 {
		t.Fatal(result, err)
	}
	s, err = m.Service06(t.Context(), f.customer.ID, sid)
	if err != nil || s.Status != "active" {
		t.Fatal(s, err)
	}
	contracttest.Data(t, "AdminService", map[string]any{"data": s})
	var orderState string
	if err = m.DB.QueryRow("SELECT o.status FROM orders o JOIN order_items i ON i.order_id=o.id JOIN service_units u ON u.order_item_id=i.id WHERE u.service_id=?", sid).Scan(&orderState); err != nil || orderState != "active" {
		t.Fatal(orderState, err)
	}
	controller.mu.Lock()
	provisions, actions := controller.provisions, controller.sends
	controller.mu.Unlock()
	if provisions != 1 || actions != 0 {
		t.Fatal("setup repeated", provisions, actions)
	}
}

func TestMySQLCommerceFailedBootstrapRequiresExplicitStaffRetry(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	sid, _ := paidService06(t, f)
	controller, runtime := serviceControllerFixture06(t, m, f.customer.ID, sid)
	if _, err := m.DB.Exec("UPDATE services SET status='provisioning' WHERE id=?", sid); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DB.Exec("UPDATE service_tasks SET status='succeeded' WHERE service_id=? AND action='provision'", sid); err != nil {
		t.Fatal(err)
	}
	tx, err := m.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	setup, err := queueServiceTask06(t.Context(), tx, sid, "reinstall", serviceActionPayload06{Version: 1, Source: "bootstrap", Params: serviceParams06{ImageID: "debian-12"}}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	controller.actionStatus = "rejected"
	app := NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), Services: runtime, CommerceContractVersion: "0.6"})
	settings, err := m.AutomationSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	settings.AutoProvision = true
	result, err := app.Automation.jobs["dispatch_provisioning"](t.Context(), settings, time.Now().UTC().Add(time.Minute))
	if err != nil || result.Failed != 1 {
		t.Fatal("controller rejection absent from automation failures", result, err)
	}
	state, err := readServiceTask06(t.Context(), m.DB, setup.ID, "")
	if err != nil || state.View.Status != "failed" {
		t.Fatal(state.View, err)
	}
	if _, err = app.Automation.jobs["dispatch_provisioning"](t.Context(), settings, time.Now().UTC().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	sent := controller.sends
	controller.actionStatus = ""
	controller.mu.Unlock()
	if sent != 1 {
		t.Fatal("failed setup was retried automatically")
	}
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	admin := client(t, server.URL, f.admin.Email, "CatalogFixture!2026")
	customer := client(t, server.URL, f.customer.Email, "CatalogFixture!2026")
	input := api.AdminServiceActionInput{Action: "provision"}
	key := ID()
	status, body := customer.request("POST", "/api/v1/admin/services/"+sid+"/actions", input, key)
	if status != 403 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
	status, body = admin.request("POST", "/api/v1/admin/services/"+sid+"/actions", input, key)
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/admin/services/{id}/actions", body)
	id := body["data"].(map[string]any)["id"].(string)
	if id == setup.ID {
		t.Fatal("explicit retry reused rejected operation")
	}
	for range 2 {
		task, err := app.DispatchServiceTask06(t.Context(), id, true, true)
		if err != nil || task.Status != "succeeded" {
			t.Fatal(task, err)
		}
	}
	view, err := m.Service06(t.Context(), f.customer.ID, sid)
	if err != nil || view.Status != "active" {
		t.Fatal(view, err)
	}
	controller.mu.Lock()
	sent, provisions := controller.sends, controller.provisions
	controller.mu.Unlock()
	if sent != 2 || provisions != 0 {
		t.Fatal("setup retry duplicated a resource", sent, provisions)
	}
}
