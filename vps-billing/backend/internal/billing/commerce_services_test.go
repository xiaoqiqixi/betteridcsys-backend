package billing

import (
	"aster/billing/internal/contracttest"
	api "aster/billing/internal/financeapi"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func paidService06(t *testing.T, f catalogFixture) (string, api.AdminProduct) {
	t.Helper()
	m := f.store.(*MySQL)
	input := product06Fixture(t, f)
	input.Slug = "service-" + ID()
	p, err := m.SaveProduct06(t.Context(), f.admin, "", input, ID(), false)
	if err != nil {
		t.Fatal(err)
	}
	order, err := m.CreateOrder06(t.Context(), f.customer, quotedOrder06(t, m, f.customer, p, 1), ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.AddInvoicePayment06(t.Context(), f.admin, order.Invoice.ID, invoice06Payment(order.Invoice.Total.AmountMinor, ID()), ID()); err != nil {
		t.Fatal(err)
	}
	v, err := m.Order06(t.Context(), f.customer.ID, order.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Order.Items) != 1 || len(v.Order.Items[0].ServiceIDs) != 1 {
		t.Fatal(v)
	}
	return v.Order.Items[0].ServiceIDs[0], p
}
func serviceResponse06(t *testing.T, body map[string]any) api.AdminService {
	t.Helper()
	raw, _ := json.Marshal(body["data"])
	var out api.AdminService
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestMySQLCommerceServiceHandlersOwnershipRevisionAndCancellation(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	sid, _ := paidService06(t, f)
	server := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), CommerceContractVersion: "0.6"}).Handler())
	defer server.Close()
	customer := client(t, server.URL, f.customer.Email, "CatalogFixture!2026")
	admin := client(t, server.URL, f.admin.Email, "CatalogFixture!2026")
	guest := client(t, server.URL, "", "")
	for _, tc := range []struct {
		path, template string
		staff          bool
	}{{"/services", "/services", false}, {"/services/" + sid, "/services/{id}", false}, {"/admin/services", "/admin/services", true}, {"/admin/services/" + sid, "/admin/services/{id}", true}} {
		caller := customer
		denied := guest
		code := 401
		if tc.staff {
			caller = admin
			denied = customer
			code = 403
		}
		status, body := caller.request("GET", "/api/v1"+tc.path, nil, "")
		if status != 200 {
			t.Fatal(tc.path, status, body)
		}
		contracttest.Response(t, "GET", tc.template, body)
		status, body = denied.request("GET", "/api/v1"+tc.path, nil, "")
		if status != code {
			t.Fatal(tc.path, status, body)
		}
		contracttest.Error(t, body)
	}
	for _, method := range []string{"GET", "PATCH"} {
		status, body := admin.request(method, "/api/v1/services/"+sid, map[string]any{"label": "Other customer's service"}, "")
		if status != 404 {
			t.Fatal(status, body)
		}
		contracttest.Error(t, body)
	}
	status, body := customer.request("PATCH", "/api/v1/services/"+sid, map[string]any{"label": "Production database", "auto_renew": false}, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "PATCH", "/services/{id}", body)
	view := serviceResponse06(t, body)
	if view.AutoRenew || view.Label != "Production database" || view.Capabilities != (api.ServiceCapabilities{}) || view.Status != "provisioning" {
		t.Fatal(view)
	}
	if _, ok := body["data"].(map[string]any)["client_id"]; ok {
		t.Fatal("staff ownership leaked")
	}
	status, body = admin.request("GET", "/api/v1/admin/services/"+sid, nil, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "GET", "/admin/services/{id}", body)
	view = serviceResponse06(t, body)
	patch := map[string]any{"revision": view.Revision, "price_override_minor": 700, "auto_renew": true}
	status, body = customer.request("PATCH", "/api/v1/admin/services/"+sid, patch, "")
	if status != 403 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
	status, body = admin.request("PATCH", "/api/v1/admin/services/"+sid, patch, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "PATCH", "/admin/services/{id}", body)
	view = serviceResponse06(t, body)
	if view.Recurring.AmountMinor != 700 || view.PriceOverride == nil || !view.AutoRenew {
		t.Fatal(view)
	}
	status, body = admin.request("PATCH", "/api/v1/admin/services/"+sid, patch, "")
	if status != 409 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
	patch = map[string]any{"revision": view.Revision, "price_override_minor": nil}
	status, body = admin.request("PATCH", "/api/v1/admin/services/"+sid, patch, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "PATCH", "/admin/services/{id}", body)
	view = serviceResponse06(t, body)
	if view.Recurring.AmountMinor != 999 || view.PriceOverride != nil {
		t.Fatal(view)
	}
	if _, err := m.DB.Exec("UPDATE services SET status='active' WHERE id=?", sid); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"end_of_period", "immediate"} {
		reason := "No longer needed"
		if kind == "immediate" {
			reason = strings.Repeat("退", 1000)
		}
		request := map[string]any{"type": kind, "reason": reason}
		status, body = admin.request("POST", "/api/v1/services/"+sid+"/cancellation", request, "")
		if status != 404 {
			t.Fatal(status, body)
		}
		contracttest.Error(t, body)
		status, body = customer.request("POST", "/api/v1/services/"+sid+"/cancellation", request, "")
		if status != 200 {
			t.Fatal(status, body)
		}
		contracttest.Response(t, "POST", "/services/{id}/cancellation", body)
		view = serviceResponse06(t, body)
		if view.Cancellation == nil || view.Cancellation.Type != kind || view.Cancellation.Reason != reason || view.TerminatesAt == nil || len(view.RenewalOptions) != 0 {
			t.Fatal(view)
		}
		status, body = admin.request("DELETE", "/api/v1/services/"+sid+"/cancellation", nil, "")
		if status != 404 {
			t.Fatal(status, body)
		}
		contracttest.Error(t, body)
		status, body = customer.request("DELETE", "/api/v1/services/"+sid+"/cancellation", nil, "")
		if status != 200 {
			t.Fatal(status, body)
		}
		contracttest.Response(t, "DELETE", "/services/{id}/cancellation", body)
		view = serviceResponse06(t, body)
		if view.Cancellation != nil || view.TerminatesAt != nil {
			t.Fatal(view)
		}
	}
	var pending int
	if err := m.DB.QueryRow("SELECT COUNT(*) FROM service_tasks WHERE service_id=? AND action='terminate' AND status='queued'", sid).Scan(&pending); err != nil || pending != 0 {
		t.Fatal(pending, err)
	}
	status, body = customer.request("POST", "/api/v1/services/"+sid+"/cancellation", map[string]any{"type": "immediate", "reason": "Remove now"}, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/services/{id}/cancellation", body)
	if _, err := m.DB.Exec("UPDATE service_tasks SET status='uncertain' WHERE service_id=? AND action='terminate' AND status='queued'", sid); err != nil {
		t.Fatal(err)
	}
	status, body = customer.request("DELETE", "/api/v1/services/"+sid+"/cancellation", nil, "")
	if status != 409 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
}
