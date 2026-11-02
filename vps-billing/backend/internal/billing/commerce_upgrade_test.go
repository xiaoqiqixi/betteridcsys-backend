package billing

import (
	"aster/billing/internal/contracttest"
	api "aster/billing/internal/financeapi"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type upgradeInspector06 struct{}

func (upgradeInspector06) InspectService(_ context.Context, v api.AdminService) (api.Service, error) {
	out := customerService06(v)
	out.Capabilities.Upgrade = true
	return out, nil
}
func activateServiceFixture06(t *testing.T, m *MySQL, sid string) {
	t.Helper()
	due := time.Now().UTC().Add(15 * 24 * time.Hour).Truncate(time.Second)
	if _, err := m.DB.Exec("UPDATE services SET status='active',next_due_at=?,next_invoice_at=? WHERE id=?", due, due, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DB.Exec("UPDATE service_tasks SET status='succeeded',finished_at=UTC_TIMESTAMP(6) WHERE service_id=? AND action='provision'", sid); err != nil {
		t.Fatal(err)
	}
}
func TestMySQLCommerceUpgradeHandlersCreditInvoiceGuardAndCapability(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	sid, p := paidService06(t, f)
	activateServiceFixture06(t, m, sid)
	in := productInput06(p)
	in.Revision = 0
	in.Slug = "upgrade-target"
	in.Specs.CPUCores = 4
	in.Prices[0].RecurringMinor = 1999
	in.Prices[1].RecurringMinor = 19999
	// Prices are read in currency/cycle order, so set the monthly amount by key.
	for i := range in.Prices {
		if in.Prices[i].Cycle == "monthly" {
			in.Prices[i].RecurringMinor = 1999
		}
	}
	stock := int64(2)
	in.Stock = &stock
	target, err := m.SaveProduct06(t.Context(), f.admin, "", in, ID(), false)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), CommerceContractVersion: "0.6", Services: upgradeInspector06{}})
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	customer := client(t, server.URL, f.customer.Email, "CatalogFixture!2026")
	admin := client(t, server.URL, f.admin.Email, "CatalogFixture!2026")
	status, body := customer.request("GET", "/api/v1/services/"+sid+"/upgrade-options", nil, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "GET", "/services/{id}/upgrade-options", body)
	status, body = customer.request("POST", "/api/v1/services/"+sid+"/upgrade/quote", map[string]string{"target_product_id": target.ID}, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/services/{id}/upgrade/quote", body)
	raw, _ := json.Marshal(body["data"])
	var quote api.UpgradeQuote
	_ = json.Unmarshal(raw, &quote)
	if quote.Prorated.AmountMinor <= 0 {
		t.Fatal(quote)
	}
	request := api.UpgradeServiceInput{TargetProductID: target.ID, QuoteDigest: quote.QuoteDigest}
	for _, tc := range []struct {
		method, path, template string
		input                  any
	}{{"GET", "/upgrade-options", "/services/{id}/upgrade-options", nil}, {"POST", "/upgrade/quote", "/services/{id}/upgrade/quote", map[string]string{"target_product_id": target.ID}}, {"POST", "/upgrade", "/services/{id}/upgrade", request}} {
		status, body = admin.request(tc.method, "/api/v1/services/"+sid+tc.path, tc.input, "upgrade-wrong-owner")
		if status != 404 {
			t.Fatal(status, body)
		}
		contracttest.Error(t, body)
	}
	stale := request
	stale.QuoteDigest = strings.Repeat("0", 64)
	status, body = customer.request("POST", "/api/v1/services/"+sid+"/upgrade", stale, "upgrade-stale-quote")
	if status != 409 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
	status, body = customer.request("POST", "/api/v1/services/"+sid+"/upgrade", request, "upgrade-request-once")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/services/{id}/upgrade", body)
	result := body["data"].(map[string]any)
	if result["status"] != "invoice" {
		t.Fatal(result)
	}
	raw, _ = json.Marshal(result["invoice"])
	var invoice api.Invoice
	_ = json.Unmarshal(raw, &invoice)
	if invoice.Kind != "upgrade" || invoice.Total.AmountMinor != quote.Prorated.AmountMinor {
		t.Fatal(invoice)
	}
	view, err := m.Service06(t.Context(), f.customer.ID, sid)
	if err != nil || view.Recurring.AmountMinor != 999 {
		t.Fatal("changed before payment", view, err)
	}
	paid, err := m.AddInvoicePayment06(t.Context(), f.admin, invoice.ID, invoice06Payment(invoice.Total.AmountMinor, ID()), ID())
	if err != nil {
		t.Fatal(err)
	}
	contracttest.Data(t, "AdminInvoice", map[string]any{"data": paid})
	view, err = m.Service06(t.Context(), f.customer.ID, sid)
	if err != nil || view.Recurring.AmountMinor != 1999 {
		t.Fatal(view, err)
	}
	contracttest.Data(t, "AdminService", map[string]any{"data": view})
	var tasks int
	if err = m.DB.QueryRow("SELECT COUNT(*) FROM service_tasks WHERE service_id=? AND action='resize'", sid).Scan(&tasks); err != nil || tasks != 1 {
		t.Fatal(tasks, err)
	}
	status, body = customer.request("POST", "/api/v1/services/"+sid+"/upgrade", request, "upgrade-request-once")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/services/{id}/upgrade", body)
	status, body = customer.request("POST", "/api/v1/services/"+sid+"/upgrade", request, "upgrade-second-key")
	if status != 409 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
	// A downgrade credits the exact negative prorating amount once and keeps a
	// durable resize task. It cannot be repeated with another key in ten minutes.
	sid2, p2 := paidService06(t, f)
	activateServiceFixture06(t, m, sid2)
	lower := productInput06(p2)
	lower.Revision = 0
	lower.Slug = "downgrade-target"
	for i := range lower.Prices {
		if lower.Prices[i].Cycle == "monthly" {
			lower.Prices[i].RecurringMinor = 500
		}
	}
	lower.Specs.CPUCores = 1
	down, err := m.SaveProduct06(t.Context(), f.admin, "", lower, ID(), false)
	if err != nil {
		t.Fatal(err)
	}
	quote, err = m.UpgradeQuote06(t.Context(), f.customer.ID, sid2, down.ID, time.Now().UTC())
	if err != nil || quote.Prorated.AmountMinor >= 0 {
		t.Fatal(quote, err)
	}
	request = api.UpgradeServiceInput{TargetProductID: down.ID, QuoteDigest: quote.QuoteDigest}
	for range 2 {
		status, body = customer.request("POST", "/api/v1/services/"+sid2+"/upgrade", request, "downgrade-once")
		if status != 200 {
			t.Fatal(status, body)
		}
		contracttest.Response(t, "POST", "/services/{id}/upgrade", body)
		if body["data"].(map[string]any)["status"] != "scheduled" {
			t.Fatal(body)
		}
	}
	credit, err := m.Credit06(t.Context(), f.customer.ID)
	if err != nil || credit.Balance.AmountMinor != -quote.Prorated.AmountMinor {
		t.Fatal(credit, quote, err)
	}
	unsupported := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), CommerceContractVersion: "0.6"}).Handler())
	defer unsupported.Close()
	unsupportedCustomer := client(t, unsupported.URL, f.customer.Email, "CatalogFixture!2026")
	for _, tc := range []struct {
		method, path string
		input        any
	}{{"GET", "/upgrade-options", nil}, {"POST", "/upgrade/quote", map[string]string{"target_product_id": target.ID}}, {"POST", "/upgrade", request}} {
		status, body = unsupportedCustomer.request(tc.method, "/api/v1/services/"+sid2+tc.path, tc.input, "unsupported-upgrade")
		if status != 501 {
			t.Fatal(status, body)
		}
		contracttest.Error(t, body)
	}
}

func TestMySQLCommerceUpgradeCancellationReleasesReservedStock(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	sid, p := paidService06(t, f)
	activateServiceFixture06(t, m, sid)
	input := productInput06(p)
	input.Revision = 0
	input.Slug = "reserved-upgrade"
	stock := int64(1)
	input.Stock = &stock
	for i := range input.Prices {
		input.Prices[i].RecurringMinor *= 2
	}
	p, err := m.SaveProduct06(t.Context(), f.admin, "", input, ID(), false)
	if err != nil {
		t.Fatal(err)
	}
	quote, err := m.UpgradeQuote06(t.Context(), f.customer.ID, sid, p.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	result, err := m.UpgradeService06(t.Context(), f.customer, sid, api.UpgradeServiceInput{TargetProductID: p.ID, QuoteDigest: quote.QuoteDigest}, ID(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	var decoded struct {
		Invoice api.Invoice `json:"invoice"`
	}
	_ = json.Unmarshal(raw, &decoded)
	for range 2 {
		v, err := m.CancelInvoice06(t.Context(), f.admin, decoded.Invoice.ID, "Customer withdrew the plan change")
		if err != nil {
			t.Fatal(err)
		}
		contracttest.Data(t, "AdminInvoice", map[string]any{"data": v})
	}
	var remaining int64
	if err = m.DB.QueryRow("SELECT stock FROM products WHERE id=?", p.ID).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatal(remaining, err)
	}
}
