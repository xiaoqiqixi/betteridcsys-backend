package billing

import (
	"aster/billing/internal/contracttest"
	api "aster/billing/internal/financeapi"
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/serverv1"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCommerceModuleRequestDigestAndSecretBoundary(t *testing.T) {
	module, connection, resource := "server.fake", "test", ID()
	s := serviceRecord06{View: api.AdminService{ID: ID(), ClientID: ID(), ModuleID: &module, ConnectionID: &connection, ResourceID: &resource}, Configuration: serviceConfiguration06{Module: &storedProductModule06{ModuleID: module, ConnectionID: connection, Config: map[string]any{}}}}
	params := serviceParams06{ActionID: "rotate", Input: serverv1.Fields{"label": "next"}, Secrets: serverv1.Fields{"token": "MODULE-SECRET-CANARY"}}
	r, err := serviceRequest06(s, ID(), "action", params)
	if err != nil || r.Verify() != nil {
		t.Fatal(r, err)
	}
	redacted := r.Redacted()
	payload := serviceActionPayload06{Params: params.Redacted(), Request: &redacted, RequestSHA256: r.Digest}
	raw, _ := json.Marshal(payload)
	if strings.Contains(string(raw), "MODULE-SECRET-CANARY") || strings.Contains(string(raw), `"secrets"`) {
		t.Fatal("credential persisted in task payload")
	}
	if redacted.Digest != r.Digest {
		t.Fatal("reconciliation lost original digest")
	}
}
func TestMySQLCommerceServerModulesConnectionsActionsAndBinding(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	sid, _ := paidService06(t, f)
	fake, runtime := serviceControllerFixture06(t, m, f.customer.ID, sid)
	secret := "Module-Action-Password!2026"
	action := serverv1.CustomAction{ID: "rotate", Name: serverv1.Text{"en": "Rotate credential", "zh-CN": "更换凭据"}, Danger: true, Confirm: serverv1.Text{"en": "Rotate?", "zh-CN": "确认更换？"}, InputSchema: serverv1.Schema{Type: "object", Properties: map[string]serverv1.Field{"token": {Type: "string", WriteOnly: true}, "mode": {Type: "string", Enum: []any{"safe"}}}, Required: []string{"token", "mode"}}}
	fake.view.CustomActions = []serverv1.CustomAction{action}
	app := NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), CommerceContractVersion: "0.6", Services: runtime})
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	admin := client(t, server.URL, f.admin.Email, "CatalogFixture!2026")
	customer := client(t, server.URL, f.customer.Email, "CatalogFixture!2026")
	for _, path := range []string{"modules", "connections"} {
		status, body := admin.request("GET", "/api/v1/admin/integrations/"+path, nil, "")
		if status != 200 {
			t.Fatal(status, body)
		}
		contracttest.Response(t, "GET", "/admin/integrations/"+path, body)
		status, body = customer.request("GET", "/api/v1/admin/integrations/"+path, nil, "")
		if status != 403 {
			t.Fatal(status, body)
		}
	}
	for _, op := range []string{"test", "sync-images"} {
		status, body := admin.request("POST", "/api/v1/admin/integrations/connections/controller-test/"+op, map[string]any{}, "")
		if status != 200 {
			t.Fatal(status, body)
		}
		contracttest.Response(t, "POST", "/admin/integrations/connections/{id}/"+op, body)
	}
	in := product06Fixture(t, f)
	in.Slug = "module-" + ID()
	in.Module = &productModuleInput06{ModuleID: "server.fake", ConnectionID: "controller-test", Config: map[string]any{}}
	status, body := admin.request("POST", "/api/v1/admin/products", in, ID())
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/admin/products", body)
	product := decodeProduct06(t, body["data"])
	if product.Module == nil || !serverv1.ValidDigest(product.Module.ConfigDigest) || product.OrderSchema == nil {
		t.Fatal(product)
	}
	// A product binds to exactly this connection, regardless of its catalog kind.
	order, err := m.CreateOrder06(t.Context(), f.customer, quotedOrder06(t, m, f.customer, product, 1), ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.AddInvoicePayment06(t.Context(), f.admin, order.Invoice.ID, invoice06Payment(order.Invoice.Total.AmountMinor, ID()), ID()); err != nil {
		t.Fatal(err)
	}
	var serviceID, taskID string
	if err = m.DB.QueryRow("SELECT u.service_id,t.id FROM order_items i JOIN service_units u ON u.order_item_id=i.id JOIN service_tasks t ON t.service_id=u.service_id WHERE i.order_id=?", order.Order.ID).Scan(&serviceID, &taskID); err != nil {
		t.Fatal(err)
	}
	fake.view.ResourceID = ID()
	if task, err := app.DispatchServiceTask06(t.Context(), taskID, false, false); err != nil || task.Status != "succeeded" {
		t.Fatal(task, err)
	}
	view, err := m.Service06(t.Context(), f.customer.ID, serviceID)
	if err != nil || view.ModuleID == nil || *view.ModuleID != "server.fake" || view.ConnectionID == nil || *view.ConnectionID != "controller-test" || view.Status != "active" {
		t.Fatal(view, err)
	}
	key := ID()
	input := api.RunServiceActionInput{Input: map[string]any{"token": secret, "mode": "safe"}}
	for range 2 {
		status, body = customer.request("POST", "/api/v1/services/"+serviceID+"/actions/rotate", input, key)
		if status != 200 {
			t.Fatal(status, body)
		}
		contracttest.Response(t, "POST", "/services/{id}/actions/{action_id}", body)
	}
	actionID := body["data"].(map[string]any)["id"].(string)
	if task, err := app.DispatchServiceTask06(t.Context(), actionID, true, false); err != nil || task.Status != "succeeded" {
		t.Fatal(task, err)
	}
	for _, q := range []string{"SELECT CAST(payload AS CHAR) FROM service_tasks", "SELECT CAST(response AS CHAR) FROM commerce_requests", "SELECT detail FROM audit_events", "SELECT CAST(configuration_snapshot AS CHAR) FROM services WHERE configuration_snapshot IS NOT NULL"} {
		rows, err := m.DB.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var raw string
			if err = rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(raw, secret) {
				t.Fatal("module credential persisted", q)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	status, body = admin.request("PATCH", "/api/v1/admin/integrations/connections/controller-test", map[string]any{"revision": 1, "enabled": false}, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "PATCH", "/admin/integrations/connections/{id}", body)
	in.Slug = "disabled-" + ID()
	status, body = admin.request("POST", "/api/v1/admin/products", in, ID())
	if status != 409 {
		t.Fatal("disabled connection accepted", status, body)
	}
	status, body = admin.request("PATCH", "/api/v1/admin/integrations/connections/controller-test", map[string]any{"revision": 1, "enabled": true}, "")
	if status != 409 {
		t.Fatal("stale revision accepted", status, body)
	}
}
func TestCommerceModuleProcessorRejectsReplacedConnectionIdentity(t *testing.T) {
	fake := &fakeServiceController06{}
	runtime := &ServerModules06{Prepare: func(context.Context, string) (serverv1.Processor, error) { return fake, nil }, Selections: func() ([]pluginapi.Selection, error) { return nil, nil }}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := runtime.processor(ctx, "one", "server.some-other-provider"); err == nil {
		t.Fatal("switched module accepted for existing connection")
	}
}
