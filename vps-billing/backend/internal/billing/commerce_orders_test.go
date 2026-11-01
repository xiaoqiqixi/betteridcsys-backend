package billing

import (
	"aster/billing/internal/commerce"
	"aster/billing/internal/contracttest"
	api "aster/billing/internal/financeapi"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func quotedOrder06(t *testing.T, m *MySQL, actor User, p api.AdminProduct, quantity int64) api.CreateOrderInput {
	t.Helper()
	input := api.CreateOrderInput{Currency: "CNY", Items: []api.CartItemInput{{ProductID: p.ID, Cycle: "monthly", Quantity: quantity, Options: map[string]any{}, Hostname: "checkout.example"}}}
	quote, err := m.Quote06(t.Context(), actor, api.CreateQuoteInput{Currency: input.Currency, Items: input.Items}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	contracttest.Data(t, "Quote", map[string]any{"data": quote.Quote})
	input.QuoteDigest = quote.Quote.QuoteDigest
	return input
}
func orderResponse06(t *testing.T, body map[string]any) api.CreateOrderResponse {
	t.Helper()
	raw, _ := json.Marshal(body["data"])
	var out api.CreateOrderResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMySQLCommerceOrderHandlersCreditStockCancellationAndOwnership(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	stock := int64(4)
	productInput := product06Fixture(t, f)
	productInput.Stock = &stock
	p, err := m.SaveProduct06(t.Context(), f.admin, "", productInput, "order-product", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.AdjustCredit06(t.Context(), f.admin, f.customer.ID, 500, "Checkout credit", "order-credit-seed"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), CommerceContractVersion: "0.6"}).Handler())
	defer srv.Close()
	customer := client(t, srv.URL, f.customer.Email, "CatalogFixture!2026")
	admin := client(t, srv.URL, f.admin.Email, "CatalogFixture!2026")
	guest := client(t, srv.URL, "", "")
	input := quotedOrder06(t, m, f.customer, p, 2)
	input.ApplyCredit = true
	status, body := customer.request("POST", "/api/v1/orders", input, "order-create-1")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/orders", body)
	result := orderResponse06(t, body)
	if result.Order.Status != "pending_payment" || result.Invoice.Total.AmountMinor != 2198 || result.Invoice.CreditApplied.AmountMinor != 500 || result.Invoice.BalanceDue.AmountMinor != 1698 || len(result.Order.Items[0].ServiceIDs) != 0 {
		t.Fatal(result)
	}
	status, body = customer.request("POST", "/api/v1/orders", input, "order-create-1")
	if status != 200 || orderResponse06(t, body).Order.ID != result.Order.ID {
		t.Fatal("replay", status, body)
	}
	contracttest.Response(t, "POST", "/orders", body)
	var remaining int64
	if err = m.DB.QueryRow("SELECT stock FROM products WHERE id=?", p.ID).Scan(&remaining); err != nil || remaining != 2 {
		t.Fatal(remaining, err)
	}
	for _, tc := range []struct {
		path, template string
		staff          bool
	}{
		{"/orders", "/orders", false}, {"/orders/" + result.Order.ID, "/orders/{id}", false}, {"/admin/orders", "/admin/orders", true}, {"/admin/orders/" + result.Order.ID, "/admin/orders/{id}", true},
	} {
		caller := customer
		if tc.staff {
			caller = admin
		}
		status, body = caller.request("GET", "/api/v1"+tc.path, nil, "")
		if status != 200 {
			t.Fatal(tc.path, status, body)
		}
		contracttest.Response(t, "GET", tc.template, body)
		denied := guest
		code := 401
		if tc.staff {
			denied = customer
			code = 403
		}
		status, body = denied.request("GET", "/api/v1"+tc.path, nil, "")
		if status != code {
			t.Fatal(tc.path, status, body)
		}
		contracttest.Error(t, body)
	}
	status, body = admin.request("GET", "/api/v1/orders/"+result.Order.ID, nil, "")
	if status != 404 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
	for _, tc := range []struct {
		caller *testClient
		path   string
		code   int
	}{
		{admin, "/orders/" + result.Order.ID + "/cancel", 404}, {customer, "/admin/orders/" + result.Order.ID + "/cancel", 403},
	} {
		status, body = tc.caller.request("POST", "/api/v1"+tc.path, map[string]string{"reason": "Not authorized"}, "")
		if status != tc.code {
			t.Fatal(status, body)
		}
		contracttest.Error(t, body)
	}
	status, body = customer.request("POST", "/api/v1/orders/"+result.Order.ID+"/cancel", map[string]string{"reason": "Changed my mind"}, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/orders/{id}/cancel", body)
	if err = m.DB.QueryRow("SELECT stock FROM products WHERE id=?", p.ID).Scan(&remaining); err != nil || remaining != 4 {
		t.Fatal("stock not restored", remaining, err)
	}
	balance, err := m.Credit06(t.Context(), f.customer.ID)
	if err != nil || balance.Balance.AmountMinor != 500 {
		t.Fatal("credit not restored", balance, err)
	}
	input = quotedOrder06(t, m, f.customer, p, 1)
	status, body = customer.request("POST", "/api/v1/orders", input, "order-cancel-admin")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/orders", body)
	result = orderResponse06(t, body)
	status, body = admin.request("POST", "/api/v1/admin/orders/"+result.Order.ID+"/cancel", map[string]string{"reason": "Customer request"}, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/admin/orders/{id}/cancel", body)
	changed := input
	changed.ApplyCredit = true
	status, body = customer.request("POST", "/api/v1/orders", changed, "order-cancel-admin")
	if status != 409 || body["error"].(map[string]any)["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
	status, body = guest.request("POST", "/api/v1/orders", input, "order-no-session")
	if status != 401 && status != 403 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
	input.Items[0].RootPassword = "CheckoutSecretNeverStored!123"
	status, body = customer.request("POST", "/api/v1/orders", input, "order-secret-rejected")
	if status != 400 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
	var requests int
	if err = m.DB.QueryRow("SELECT COUNT(*) FROM commerce_requests WHERE request_key='order-secret-rejected'").Scan(&requests); err != nil || requests != 0 {
		t.Fatal("secret request persisted", requests, err)
	}
}

func TestMySQLCommerceOrderConcurrentFinalStockAndCoupon(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	stock := int64(1)
	in := product06Fixture(t, f)
	in.Stock = &stock
	p, err := m.SaveProduct06(t.Context(), f.admin, "", in, "stock-product", false)
	if err != nil {
		t.Fatal(err)
	}
	input := quotedOrder06(t, m, f.customer, p, 1)
	actors := []User{f.customer}
	for range 9 {
		u := User{ID: ID(), Email: ID() + "@stock.test", Name: "Stock customer", Role: "customer", PasswordHash: "unused"}
		if err = m.AddUser(t.Context(), u); err != nil {
			t.Fatal(err)
		}
		actors = append(actors, u)
	}
	var wg sync.WaitGroup
	results := make(chan error, len(actors))
	ids := make(chan string, len(actors))
	for _, u := range actors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := m.CreateOrder06(t.Context(), u, input, ID())
			results <- err
			if err == nil {
				ids <- out.Order.ID
			}
		}()
	}
	wg.Wait()
	close(results)
	close(ids)
	accepted, rejected := 0, 0
	for err := range results {
		if err == nil {
			accepted++
		} else if errors.Is(err, commerce.ErrQuoteChanged) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 1 || rejected != 9 {
		t.Fatal("oversold", accepted, rejected)
	}
	var remaining, orders, invoices int64
	if err = m.DB.QueryRow("SELECT stock FROM products WHERE id=?", p.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal(remaining, err)
	}
	if err = m.DB.QueryRow("SELECT COUNT(*) FROM orders").Scan(&orders); err != nil || orders != 1 {
		t.Fatal(orders, err)
	}
	if err = m.DB.QueryRow("SELECT COUNT(*) FROM invoices").Scan(&invoices); err != nil || invoices != 1 {
		t.Fatal(invoices, err)
	}
	for id := range ids {
		if _, err = m.CancelOrder06(t.Context(), f.admin, id, "Release reservation"); err != nil {
			t.Fatal(err)
		}
	}
	if err = m.DB.QueryRow("SELECT stock FROM products WHERE id=?", p.ID).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatal(remaining, err)
	}
	// A single-use coupon is reserved with the order even when stock is unlimited.
	in = productInput06(p)
	in.Stock = nil
	in.Revision = p.Revision
	p, err = m.SaveProduct06(t.Context(), f.admin, p.ID, in, "", false)
	if err != nil {
		t.Fatal(err)
	}
	var coupon api.AdminCreateCouponInput
	if err = json.Unmarshal([]byte(`{"code":"ONCE","kind":"percent","percent_bp":10000,"fixed":[],"applies_to":{"product_ids":[],"cycles":[]},"recurring":true,"new_clients_only":false,"max_uses":1,"max_uses_per_client":1,"starts_at":null,"ends_at":null,"active":true}`), &coupon); err != nil {
		t.Fatal(err)
	}
	if _, err = m.SaveCoupon06(t.Context(), f.admin, "", coupon); err != nil {
		t.Fatal(err)
	}
	input = quotedOrder06(t, m, f.customer, p, 2)
	input.Coupon = "ONCE"
	quote, err := m.Quote06(t.Context(), f.customer, api.CreateQuoteInput{Items: input.Items, Coupon: input.Coupon, Currency: input.Currency}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	input.QuoteDigest = quote.Quote.QuoteDigest
	results = make(chan error, len(actors))
	for _, u := range actors {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := m.CreateOrder06(t.Context(), u, input, ID()); results <- err }()
	}
	wg.Wait()
	close(results)
	accepted = 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, commerce.ErrQuoteChanged) {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatal("coupon redeemed more than once", accepted)
	}
	var count int
	if err = m.DB.QueryRow("SELECT COUNT(*) FROM services").Scan(&count); err != nil || count != 2 {
		t.Fatal("one service per unit", count, err)
	}
	if err = m.DB.QueryRow("SELECT COUNT(*) FROM service_tasks WHERE action='provision' AND status='queued'").Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
}

func TestMySQLCommerceOrderPaymentCreatesUnitsExactlyOnce(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	p, err := m.SaveProduct06(t.Context(), f.admin, "", product06Fixture(t, f), "unit-product", false)
	if err != nil {
		t.Fatal(err)
	}
	input := quotedOrder06(t, m, f.customer, p, 3)
	out, err := m.CreateOrder06(t.Context(), f.customer, input, "unit-order-create")
	if err != nil {
		t.Fatal(err)
	}
	contracttest.Data(t, "Order", map[string]any{"data": out.Order})
	contracttest.Data(t, "Invoice", map[string]any{"data": out.Invoice})
	payment := invoice06Payment(out.Invoice.Total.AmountMinor, "unit-payment")
	for range 2 {
		v, err := m.AddInvoicePayment06(t.Context(), f.admin, out.Invoice.ID, payment, "unit-paid")
		if err != nil {
			t.Fatal(err)
		}
		contracttest.Data(t, "AdminInvoice", map[string]any{"data": v})
	}
	order, err := m.Order06(t.Context(), f.customer.ID, out.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	contracttest.Data(t, "Order", map[string]any{"data": order.Order})
	if order.Order.Status != "paid" || len(order.Order.Items[0].ServiceIDs) != 3 {
		t.Fatal(order)
	}
	var services, total int64
	if err = m.DB.QueryRow("SELECT COUNT(*),SUM(recurring_minor) FROM services").Scan(&services, &total); err != nil || services != 3 || total != 2997 {
		t.Fatal(services, total, err)
	}
	if _, err = m.CancelOrder06(t.Context(), f.customer, out.Order.ID, "Cannot cancel paid order"); !errors.Is(err, ErrPaymentState) {
		t.Fatal(err)
	}
	input.QuoteDigest = fmt.Sprintf("%064d", 0)
	if _, err = m.CreateOrder06(t.Context(), f.customer, input, "unit-stale-quote"); !errors.Is(err, commerce.ErrQuoteChanged) {
		t.Fatal(err)
	}
}

func TestCommerceUnitRecurringAllocationPreservesMinorUnits(t *testing.T) {
	for _, tc := range []struct {
		currency        string
		decimals        int
		total, quantity int64
	}{{"JPY", 0, 100, 3}, {"EUR", 2, 1001, 3}, {"KWD", 3, 1, 20}, {"USD", 2, 0, 2}} {
		t.Run(tc.currency, func(t *testing.T) {
			sum := int64(0)
			for i := int64(0); i < tc.quantity; i++ {
				sum += unitAmount06(tc.total, tc.quantity, i)
			}
			if sum != tc.total {
				t.Fatal(tc, sum)
			}
		})
	}
}
