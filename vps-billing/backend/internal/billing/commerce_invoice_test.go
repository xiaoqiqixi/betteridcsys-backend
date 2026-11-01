package billing

import (
	"aster/billing/internal/contracttest"
	api "aster/billing/internal/financeapi"
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func invoice06Data(t *testing.T, body map[string]any) api.AdminInvoice {
	t.Helper()
	raw, _ := json.Marshal(body["data"])
	var out api.AdminInvoice
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func invoice06Input(uid string, amount int64) map[string]any {
	return map[string]any{"client_id": uid, "due_at": time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339), "lines": []any{map[string]any{"description": "Manual service charge", "amount_minor": amount}}}
}
func invoice06Payment(amount int64, reference string) api.AdminAddPaymentInput {
	return api.AdminAddPaymentInput{AmountMinor: amount, Method: "bank_transfer", Reference: reference, ReceivedAt: time.Now().UTC().Format(time.RFC3339)}
}

func TestMySQLCommerceInvoiceHandlersPartialCreditRefundAndOwnership(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	srv := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), CommerceContractVersion: "0.6"}).Handler())
	defer srv.Close()
	admin := client(t, srv.URL, f.admin.Email, "CatalogFixture!2026")
	customer := client(t, srv.URL, f.customer.Email, "CatalogFixture!2026")
	guest := client(t, srv.URL, "", "")
	input := invoice06Input(f.customer.ID, 1000)
	status, body := admin.request("POST", "/api/v1/admin/invoices", input, "invoice-create-1")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/admin/invoices", body)
	v := invoice06Data(t, body)
	status, body = admin.request("POST", "/api/v1/admin/invoices", input, "invoice-create-1")
	if status != 200 || invoice06Data(t, body).ID != v.ID {
		t.Fatal("create replay", status, body)
	}
	contracttest.Data(t, "AdminInvoice", body)
	for _, tc := range []struct {
		path, template string
		staff          bool
	}{
		{"/invoices", "/invoices", false}, {"/invoices/" + v.ID, "/invoices/{id}", false},
		{"/admin/invoices", "/admin/invoices", true}, {"/admin/invoices/" + v.ID, "/admin/invoices/{id}", true},
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
	status, body = admin.request("GET", "/api/v1/invoices/"+v.ID, nil, "")
	if status != 404 {
		t.Fatal("ownership", status, body)
	}
	contracttest.Error(t, body)
	payment := invoice06Payment(400, "bank-reference-1")
	status, body = admin.request("POST", "/api/v1/admin/invoices/"+v.ID+"/payments", payment, "invoice-payment-1")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/admin/invoices/{id}/payments", body)
	v = invoice06Data(t, body)
	manualID := v.Payments[0].ID
	if v.Status != "unpaid" || v.AmountPaid.AmountMinor != 400 || v.BalanceDue.AmountMinor != 600 {
		t.Fatal(v)
	}
	status, body = admin.request("POST", "/api/v1/admin/invoices/"+v.ID+"/payments", payment, "invoice-payment-1")
	if status != 200 || len(invoice06Data(t, body).Payments) != 1 {
		t.Fatal("payment replay", status, body)
	}
	contracttest.Data(t, "AdminInvoice", body)
	payment.AmountMinor++
	status, body = admin.request("POST", "/api/v1/admin/invoices/"+v.ID+"/payments", payment, "invoice-payment-1")
	if status != 409 || body["error"].(map[string]any)["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
	if _, err := m.AdjustCredit06(t.Context(), f.admin, f.customer.ID, 1000, "Opening credit", "invoice-credit-seed"); err != nil {
		t.Fatal(err)
	}
	pay := map[string]string{"method": "credit"}
	status, body = customer.request("POST", "/api/v1/invoices/"+v.ID+"/pay", pay, "invoice-credit-pay")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/invoices/{id}/pay", body)
	status, body = customer.request("POST", "/api/v1/invoices/"+v.ID+"/pay", pay, "invoice-credit-pay")
	if status != 200 {
		t.Fatal("credit replay", status, body)
	}
	contracttest.Response(t, "POST", "/invoices/{id}/pay", body)
	v, err := m.Invoice06(t.Context(), f.customer.ID, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != "paid" || v.CreditApplied.AmountMinor != 600 || v.BalanceDue.AmountMinor != 0 || len(v.Payments) != 2 {
		t.Fatal(v)
	}
	refund := api.AdminRefundInput{PaymentID: manualID, AmountMinor: 100, Destination: "credit", Reason: "Partial refund"}
	status, body = admin.request("POST", "/api/v1/admin/invoices/"+v.ID+"/refunds", refund, "invoice-refund-1")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/admin/invoices/{id}/refunds", body)
	if invoice06Data(t, body).Status != "partially_refunded" {
		t.Fatal(body)
	}
	status, body = admin.request("POST", "/api/v1/admin/invoices/"+v.ID+"/refunds", refund, "invoice-refund-1")
	if status != 200 {
		t.Fatal("refund replay", status, body)
	}
	contracttest.Data(t, "AdminInvoice", body)
	credit, err := m.Credit06(t.Context(), f.customer.ID)
	if err != nil || credit.Balance.AmountMinor != 500 {
		t.Fatal(credit, err)
	}
	for _, tc := range []struct {
		path  string
		input any
	}{
		{"/admin/invoices", input}, {"/admin/invoices/" + v.ID + "/payments", payment}, {"/admin/invoices/" + v.ID + "/refunds", refund}, {"/admin/invoices/" + v.ID + "/cancel", map[string]string{"reason": "Not authorized"}},
	} {
		status, body = customer.request("POST", "/api/v1"+tc.path, tc.input, "invoice-denied-key")
		if status != 403 {
			t.Fatal(tc.path, status, body)
		}
		contracttest.Error(t, body)
	}
	status, body = admin.request("POST", "/api/v1/invoices/"+v.ID+"/pay", pay, "invoice-other-owner")
	if status != 404 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
	status, body = admin.request("POST", "/api/v1/admin/invoices", invoice06Input(f.customer.ID, 200), "invoice-cancel-create")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Data(t, "AdminInvoice", body)
	cancelID := invoice06Data(t, body).ID
	status, body = admin.request("POST", "/api/v1/admin/invoices/"+cancelID+"/cancel", map[string]string{"reason": "Created in error"}, "")
	if status != 200 || invoice06Data(t, body).Status != "cancelled" {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/admin/invoices/{id}/cancel", body)
	status, body = customer.request("POST", "/api/v1/credit/top-up", map[string]int64{"amount_minor": 1000}, "invoice-top-up-1")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/credit/top-up", body)
	top := invoice06Data(t, body)
	status, body = guest.request("POST", "/api/v1/credit/top-up", map[string]int64{"amount_minor": 1000}, "invoice-top-up-noauth")
	if status != 401 && status != 403 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
	status, body = customer.request("POST", "/api/v1/invoices/"+top.ID+"/pay", pay, "invoice-top-up-circular")
	if status != 409 {
		t.Fatal("circular top-up", status, body)
	}
	contracttest.Error(t, body)
	status, body = admin.request("POST", "/api/v1/admin/invoices/"+top.ID+"/payments", invoice06Payment(1000, "top-up-receipt"), "invoice-top-up-paid")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Data(t, "AdminInvoice", body)
	credit, err = m.Credit06(t.Context(), f.customer.ID)
	if err != nil || credit.Balance.AmountMinor != 1500 {
		t.Fatal("top-up", credit, err)
	}
	// Revoking invoice access takes effect even for an already authenticated staff session.
	migrationExec(t, t.Context(), m.DB, "INSERT INTO staff_roles(id,name,description,permissions,builtin,revision) VALUES('invoice-none','None','No financial access',JSON_ARRAY(),FALSE,1)")
	migrationExec(t, t.Context(), m.DB, "UPDATE staff_members SET role_id='invoice-none' WHERE user_id=?", f.admin.ID)
	status, body = admin.request("GET", "/api/v1/admin/invoices", nil, "")
	if status != 403 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
}

type refundTestRuntime06 struct {
	*gatewayTestRuntime
	mu      sync.Mutex
	refunds []paymentv1.Refund
	result  string
	err     error
}

func (g *refundTestRuntime06) RefundPayment(_ context.Context, _ paymentv1.Config, in paymentv1.Refund) (paymentv1.RefundResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.refunds = append(g.refunds, in)
	return paymentv1.RefundResult{RefundID: "refund-" + in.IdempotencyKey, Status: g.result}, g.err
}

func TestMySQLCommerceInvoiceGatewayRefundVoidAndDuplicate(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	p := newPaymentHTTPRuntime(t)
	p.selection.Slot = "gateway-a"
	p.manifest.Capabilities = append(p.manifest.Capabilities, pluginapi.PaymentRefundCapability)
	g := &refundTestRuntime06{gatewayTestRuntime: &gatewayTestRuntime{slots: map[string]*paymentHTTPRuntime{"gateway-a": p}}, result: "succeeded"}
	srv := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), CommerceContractVersion: "0.6", PaymentRuntime: g}).Handler())
	defer srv.Close()
	admin := client(t, srv.URL, f.admin.Email, "CatalogFixture!2026")
	customer := client(t, srv.URL, f.customer.Email, "CatalogFixture!2026")
	status, body := admin.request("POST", "/api/v1/admin/invoices", invoice06Input(f.customer.ID, 1000), "gateway-invoice-create")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Data(t, "AdminInvoice", body)
	id := invoice06Data(t, body).ID
	status, body = admin.request("POST", "/api/v1/admin/invoices/"+id+"/payments", invoice06Payment(250, "partial-receipt"), "gateway-invoice-partial")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Data(t, "AdminInvoice", body)
	pay := map[string]string{"method": "gateway", "gateway_id": "gateway-a"}
	status, body = customer.request("POST", "/api/v1/invoices/"+id+"/pay", pay, "gateway-checkout-one")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/invoices/{id}/pay", body)
	first := p.event(t, 0)
	first.GatewayID = "gateway-a"
	if first.AmountMinor != 750 || first.OrderID != id {
		t.Fatal("remaining balance snapshot", first)
	}
	voidPath := "/api/v1/admin/invoices/" + id + "/payments/" + first.AttemptID + "/void"
	status, body = customer.request("POST", voidPath, map[string]string{"reason": "Not authorized"}, "")
	if status != 403 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
	status, body = admin.request("POST", voidPath, map[string]string{"reason": "Provider confirmed cancelled"}, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/admin/invoices/{id}/payments/{payment_id}/void", body)
	status, body = customer.request("POST", "/api/v1/invoices/"+id+"/pay", pay, "gateway-checkout-two")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/invoices/{id}/pay", body)
	second := p.event(t, 1)
	second.GatewayID = "gateway-a"
	for _, event := range []paymentv1.VerifiedEvent{second, first, first} {
		raw, signature := p.signed(t, event)
		status, body = paymentHTTPNotify(t, &paymentHTTPHarness{server: srv}, "POST", "/api/v1/payment-callbacks/gateway-a", raw, signature, "")
		if status != 200 {
			t.Fatal(status, body)
		}
	}
	status, body = admin.request("GET", "/api/v1/admin/invoices/"+id, nil, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Data(t, "AdminInvoice", body)
	v := invoice06Data(t, body)
	if v.AmountPaid.AmountMinor != 1000 || v.Status != "paid" || len(v.Payments) != 3 {
		t.Fatal(v)
	}
	// Refund the extra receipt without reducing the correctly paid invoice.
	refund := api.AdminRefundInput{PaymentID: first.AttemptID, AmountMinor: 750, Destination: "original", Reason: "Duplicate receipt"}
	status, body = admin.request("POST", "/api/v1/admin/invoices/"+id+"/refunds", refund, "gateway-refund-duplicate")
	if status != 200 || invoice06Data(t, body).Status != "paid" {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/admin/invoices/{id}/refunds", body)
	status, body = admin.request("POST", "/api/v1/admin/invoices/"+id+"/refunds", refund, "gateway-refund-duplicate")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Data(t, "AdminInvoice", body)
	g.mu.Lock()
	calls := len(g.refunds)
	g.mu.Unlock()
	if calls != 1 {
		t.Fatal("refund dispatched twice", calls)
	}
	g.mu.Lock()
	g.err = errors.New("ambiguous transport")
	g.mu.Unlock()
	refund.PaymentID, refund.AmountMinor = second.AttemptID, 100
	for range 2 {
		status, body = admin.request("POST", "/api/v1/admin/invoices/"+id+"/refunds", refund, "gateway-refund-uncertain")
		if status != 409 {
			t.Fatal(status, body)
		}
		contracttest.Error(t, body)
	}
	g.mu.Lock()
	calls = len(g.refunds)
	g.mu.Unlock()
	if calls != 2 {
		t.Fatal("ambiguous refund retried", calls)
	}
	refund.Destination = "credit"
	status, body = admin.request("POST", "/api/v1/admin/invoices/"+id+"/refunds", refund, "gateway-refund-uncertain")
	if status != 409 || body["error"].(map[string]any)["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
}

func TestMySQLCommerceInvoiceCreditAcrossConcurrentInvoices(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	if _, err := m.AdjustCredit06(t.Context(), f.admin, f.customer.ID, 1000, "Opening credit", "concurrent-credit-seed"); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for range 2 {
		raw, _ := json.Marshal(invoice06Input(f.customer.ID, 700))
		var input api.AdminCreateInvoiceInput
		_ = json.Unmarshal(raw, &input)
		v, err := m.CreateInvoice06(t.Context(), f.admin, input, ID())
		if err != nil {
			t.Fatal(err)
		}
		contracttest.Data(t, "AdminInvoice", map[string]any{"data": v})
		ids = append(ids, v.ID)
	}
	var wg sync.WaitGroup
	result := make(chan error, 2)
	for _, id := range ids {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := m.PayCredit06(t.Context(), f.customer, id, ID()); result <- err }()
	}
	wg.Wait()
	close(result)
	paid, denied := 0, 0
	for err := range result {
		if err == nil {
			paid++
		} else if errors.Is(err, ErrInsufficientCredit) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if paid != 1 || denied != 1 {
		t.Fatal("credit overspend", paid, denied)
	}
	balance, err := m.Credit06(t.Context(), f.customer.ID)
	if err != nil || balance.Balance.AmountMinor != 300 {
		t.Fatal(balance, err)
	}
}
