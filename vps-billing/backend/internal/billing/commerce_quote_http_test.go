package billing

import (
	"aster/billing/internal/contracttest"
	api "aster/billing/internal/financeapi"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMySQLQuoteCouponAndGatewayHandlers(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	runtime := newPaymentHTTPRuntime(t)
	srv := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), PaymentRuntime: runtime}).Handler())
	defer srv.Close()
	admin := client(t, srv.URL, f.admin.Email, "CatalogFixture!2026")
	customer := client(t, srv.URL, f.customer.Email, "CatalogFixture!2026")
	guest := client(t, srv.URL, "", "")
	product, err := m.SaveProduct06(t.Context(), f.admin, "", product06Fixture(t, f), "quote-product", false)
	if err != nil {
		t.Fatal(err)
	}
	coupon := map[string]any{"code": "SAVE", "kind": "percent", "percent_bp": 3333, "fixed": []any{}, "applies_to": map[string]any{"product_ids": []string{product.ID}, "cycles": []string{"monthly"}}, "recurring": true, "new_clients_only": false, "max_uses": nil, "max_uses_per_client": nil, "starts_at": nil, "ends_at": nil, "active": true}
	status, result := admin.request("POST", "/api/v1/admin/coupons", coupon, "")
	if status != 200 {
		t.Fatal(status, result)
	}
	contracttest.Response(t, "POST", "/admin/coupons", result)
	saved := result["data"].(map[string]any)
	id := saved["id"].(string)
	status, result = admin.request("GET", "/api/v1/admin/coupons", nil, "")
	if status != 200 {
		t.Fatal(status, result)
	}
	contracttest.Response(t, "GET", "/admin/coupons", result)
	input := api.CreateQuoteInput{Currency: "CNY", Coupon: "SAVE", Items: []api.CartItemInput{{ProductID: product.ID, Cycle: "monthly", Quantity: 2, Options: map[string]any{}}}}
	var firstDigest any
	for n := 0; n < 2; n++ {
		status, result = guest.request("POST", "/api/v1/cart/quote", input, "")
		if status != 200 {
			t.Fatal(status, result)
		}
		contracttest.Response(t, "POST", "/cart/quote", result)
		quoted := result["data"].(map[string]any)
		if !quoted["coupon"].(map[string]any)["valid"].(bool) {
			t.Fatal(quoted)
		}
		if quoted["total"].(map[string]any)["amount_minor"] != float64(1467) {
			t.Fatal("per-line rounding", quoted)
		}
		firstDigest = quoted["quote_digest"]
	}
	if firstDigest == nil {
		t.Fatal("missing digest")
	}
	// Quoting is a read-only operation, including coupon eligibility checks.
	for _, table := range []string{"orders", "invoices", "coupon_redemptions"} {
		var count int
		if err = m.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal(table, count, err)
		}
	}
	coupon["revision"] = saved["revision"]
	coupon["active"] = false
	status, result = admin.request("PATCH", "/api/v1/admin/coupons/"+id, coupon, "")
	if status != 200 {
		t.Fatal(status, result)
	}
	contracttest.Response(t, "PATCH", "/admin/coupons/{id}", result)
	status, result = admin.request("PATCH", "/api/v1/admin/coupons/"+id, coupon, "")
	if status != 409 {
		t.Fatal("stale coupon revision", status, result)
	}
	contracttest.Error(t, result)
	status, result = guest.request("POST", "/api/v1/cart/quote", input, "")
	if status != 200 {
		t.Fatal(status, result)
	}
	contracttest.Data(t, "Quote", result)
	if result["data"].(map[string]any)["coupon"].(map[string]any)["valid"] != false {
		t.Fatal("disabled coupon", result)
	}
	for _, tc := range []struct {
		method, path string
		body         any
	}{{"GET", "/admin/coupons", nil}, {"POST", "/admin/coupons", coupon}, {"PATCH", "/admin/coupons/" + id, coupon}} {
		status, result = customer.request(tc.method, "/api/v1"+tc.path, tc.body, "")
		if status != 403 {
			t.Fatal(status, result)
		}
		contracttest.Error(t, result)
	}
	csrf := guest.csrf
	guest.csrf = "bad"
	status, result = guest.request("POST", "/api/v1/cart/quote", input, "")
	guest.csrf = csrf
	if status != 403 {
		t.Fatal(status, result)
	}
	contracttest.Error(t, result)
	input.Items[0].ProductID = "unavailable"
	status, result = guest.request("POST", "/api/v1/cart/quote", input, "")
	if status != 409 {
		t.Fatal(status, result)
	}
	contracttest.Error(t, result)
	status, result = guest.request("GET", "/api/v1/payment-gateways", nil, "")
	if status != 200 {
		t.Fatal(status, result)
	}
	contracttest.Response(t, "GET", "/payment-gateways", result)
	if len(result["data"].([]any)) != 1 {
		t.Fatal("configured gateway omitted", result)
	}
	runtime.mu.Lock()
	runtime.selection.Enabled = false
	runtime.mu.Unlock()
	status, result = guest.request("GET", "/api/v1/payment-gateways", nil, "")
	if status != 200 || len(result["data"].([]any)) != 0 {
		t.Fatal("disabled gateway exposed", status, result)
	}
	contracttest.Response(t, "GET", "/payment-gateways", result)
}

func TestMemoryQuoteDoesNotStoreSecretsOrRedeemCoupon(t *testing.T) {
	f := memoryCatalogFixture(t)
	m := f.store.(*Memory)
	product, err := m.SaveProduct06(t.Context(), f.admin, "", product06Fixture(t, f), "memory-quote", false)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"code":"SAVE","kind":"fixed","percent_bp":null,"fixed":[{"currency":"CNY","amount_minor":500}],"applies_to":{"product_ids":[],"cycles":[]},"recurring":false,"new_clients_only":false,"max_uses":1,"max_uses_per_client":1,"starts_at":null,"ends_at":null,"active":true}`)
	var in api.AdminCreateCouponInput
	if err = json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	c, err := m.SaveCoupon06(t.Context(), f.admin, "", in)
	if err != nil {
		t.Fatal(err)
	}
	input := api.CreateQuoteInput{Currency: "CNY", Coupon: "SAVE", Items: []api.CartItemInput{{ProductID: product.ID, Cycle: "monthly", Quantity: 1, Options: map[string]any{}, RootPassword: "NeverPersistThis!123"}}}
	beforeOrders, beforeInvoices, beforeAudits := len(m.orders), len(m.invoices), len(m.audits)
	for range 2 {
		out, err := m.Quote06(t.Context(), f.customer, input, time.Now().UTC())
		if err != nil || !out.Quote.Coupon.Valid || out.Items[0].Input.RootPassword != "" {
			t.Fatal(out.Quote, err)
		}
		contracttest.Data(t, "Quote", map[string]any{"data": out.Quote})
	}
	if m.catalog06Locked().coupons[c.ID].Used != 0 || len(m.orders) != beforeOrders || len(m.invoices) != beforeInvoices || len(m.audits) != beforeAudits {
		t.Fatal("quoting mutated commerce state")
	}
}
