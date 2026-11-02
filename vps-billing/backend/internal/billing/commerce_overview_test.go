package billing

import (
	"aster/billing/internal/contracttest"
	api "aster/billing/internal/financeapi"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func manualInvoice06(t *testing.T, m *MySQL, admin User, uid string, amount int64) api.AdminInvoice {
	t.Helper()
	raw, _ := json.Marshal(invoice06Input(uid, amount))
	var input api.AdminCreateInvoiceInput
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	v, err := m.CreateInvoice06(t.Context(), admin, input, ID())
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func TestMySQLCommerceOverviewContractCurrencyRevenueAndOwnership(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	sid, _ := paidService06(t, f)
	if _, err := m.DB.Exec("UPDATE services SET status='active',next_due_at=DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 3 DAY) WHERE id=?", sid); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DB.Exec("UPDATE service_tasks SET status='uncertain' WHERE service_id=?", sid); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CancelService06(t.Context(), f.customer, sid, &api.RequestCancellationInput{Type: "end_of_period", Reason: "End subscription"}); err != nil {
		t.Fatal(err)
	}
	v := manualInvoice06(t, m, f.admin, f.customer.ID, 1000)
	v, err := m.AddInvoicePayment06(t.Context(), f.admin, v.ID, invoice06Payment(1000, ID()), ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.RefundCredit06(t.Context(), f.admin, v.ID, api.AdminRefundInput{PaymentID: v.Payments[0].ID, AmountMinor: 200, Destination: "credit", Reason: "Partial refund"}, ID()); err != nil {
		t.Fatal(err)
	}
	topup, err := m.TopUp06(t.Context(), f.customer, 1000, ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.AddInvoicePayment06(t.Context(), f.admin, topup.ID, invoice06Payment(1000, ID()), ID()); err != nil {
		t.Fatal(err)
	}
	partial := manualInvoice06(t, m, f.admin, f.customer.ID, 500)
	if _, err = m.AddInvoicePayment06(t.Context(), f.admin, partial.ID, invoice06Payment(100, ID()), ID()); err != nil {
		t.Fatal(err)
	}
	configureTestCurrency(t, m, f.admin, "USD", 2, "")
	other := User{ID: ID(), Email: ID() + "@overview.test", Name: "Other currency", Role: "customer", PasswordHash: "unused"}
	if err = m.AddUser(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if _, err = m.DB.Exec("UPDATE users SET currency='USD' WHERE id=?", other.ID); err != nil {
		t.Fatal(err)
	}
	usd := manualInvoice06(t, m, f.admin, other.ID, 700)
	if _, err = m.AddInvoicePayment06(t.Context(), f.admin, usd.ID, invoice06Payment(700, ID()), ID()); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), CommerceContractVersion: "0.6"}).Handler())
	defer server.Close()
	customer := client(t, server.URL, f.customer.Email, "CatalogFixture!2026")
	admin := client(t, server.URL, f.admin.Email, "CatalogFixture!2026")
	guest := client(t, server.URL, "", "")
	status, body := customer.request("GET", "/api/v1/overview?client_id="+other.ID, nil, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "GET", "/overview", body)
	raw, _ := json.Marshal(body["data"])
	var out api.ClientOverview
	_ = json.Unmarshal(raw, &out)
	if out.Credit.AmountMinor != 1200 || out.AmountDue.AmountMinor != 400 || out.Counts.ActiveServices != 1 || out.Counts.ExpiringSoon != 1 || out.Counts.UnpaidInvoices != 1 || len(out.Services) != 1 || out.Services[0].ID != sid {
		t.Fatal(out)
	}
	for _, invoice := range out.Invoices {
		if invoice.Total.Currency != "CNY" {
			t.Fatal("foreign account invoice leaked", invoice)
		}
	}
	for _, tc := range []struct {
		currency string
		amount   int64
	}{{"CNY", 1999}, {"USD", 700}} {
		status, body = admin.request("GET", "/api/v1/admin/overview?currency="+tc.currency, nil, "")
		if status != 200 {
			t.Fatal(status, body)
		}
		contracttest.Response(t, "GET", "/admin/overview", body)
		raw, _ = json.Marshal(body["data"])
		var out api.AdminOverview
		_ = json.Unmarshal(raw, &out)
		if out.Currency != tc.currency || out.RevenueToday.AmountMinor != tc.amount || out.RevenueMonth.AmountMinor != tc.amount || len(out.RevenueSeries) != 30 {
			t.Fatal(out)
		}
		if out.RevenueSeries[29].Date != time.Now().UTC().Format("2006-01-02") || out.RevenueSeries[29].Amount.AmountMinor != tc.amount {
			t.Fatal(out.RevenueSeries)
		}
		if tc.currency == "CNY" && out.Outstanding.AmountMinor != 400 {
			t.Fatal(out.Outstanding)
		}
		if len(out.Attention) != 2 {
			t.Fatal(out.Attention)
		}
	}
	for _, tc := range []struct {
		caller *testClient
		path   string
		code   int
	}{{guest, "/overview", 401}, {customer, "/admin/overview?currency=CNY", 403}} {
		status, body = tc.caller.request("GET", "/api/v1"+tc.path, nil, "")
		if status != tc.code {
			t.Fatal(status, body)
		}
		contracttest.Error(t, body)
	}
}
