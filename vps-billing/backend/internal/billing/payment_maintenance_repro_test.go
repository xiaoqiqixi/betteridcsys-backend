package billing

import (
	"aster/billing/pkg/pluginapi/paymentv1"
	"context"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Runtime admission can reject before any handshake or worker operation, for
// example when all shared RPC slots become occupied after PaymentSelection.
// The HTTP contract does not currently expose that distinction to core.
type paymentAdmissionUnavailable struct {
	*paymentHTTPRuntime
	calls atomic.Int32
}

func (p *paymentAdmissionUnavailable) CreatePayment(context.Context, paymentv1.Config, paymentv1.Intent) (paymentv1.Checkout, error) {
	p.calls.Add(1)
	return paymentv1.Checkout{}, paymentv1.ErrUnavailable
}

func TestPaymentPreDispatchUnavailablePreservesConservativeBarrier(t *testing.T) {
	m := NewMemory()
	owner, _, _ := cancellationUsers(t, m)
	base := newPaymentHTTPRuntime(t)
	runtime := &paymentAdmissionUnavailable{paymentHTTPRuntime: base}
	server := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), PaymentRuntime: runtime}).Handler())
	t.Cleanup(server.Close)
	h := &paymentHTTPHarness{memory: m, runtime: base, server: server, owner: owner,
		customer: client(t, server.URL, "demo@aster.local", "DemoCustomer!2026")}
	order, invoice := h.order(t)
	path := "/api/v1/invoices/" + invoice.ID + "/checkout"
	for range 2 {
		status, out := h.customer.request("POST", path, nil, "")
		if status != 409 || out["error"].(map[string]any)["code"] != "PAYMENT_PENDING" {
			t.Fatal("unavailable admission released the durable barrier", status, out)
		}
	}
	if runtime.calls.Load() != 1 {
		t.Fatal("unavailable operation was retried", runtime.calls.Load())
	}
	if creates, _ := base.counts(); creates != 0 {
		t.Fatal("synthetic worker received an operation", creates)
	}
	if len(m.paymentAttempts) != 1 {
		t.Fatal("unexpected attempt count", len(m.paymentAttempts))
	}
	for _, attempt := range m.paymentAttempts {
		if attempt.State != "uncertain" || attempt.Checkout != nil {
			t.Fatal("pre-dispatch failure did not preserve conservative outcome", attempt)
		}
	}
	if status, out := h.customer.request("POST", "/api/v1/orders/"+order.ID+"/cancel", map[string]string{"reason": "Cancel unavailable admission"}, ""); status != 409 {
		t.Fatal("pre-dispatch attempt allowed cancellation", status, out)
	}
	h.pair(t, order, invoice, "unpaid")
	if len(m.paymentReceipts) != 0 {
		t.Fatal("unavailable worker created accounting entries")
	}
}

func TestPaymentExpiredCheckoutBlocksReissueAndMayStillSettle(t *testing.T) {
	h := newPaymentHTTPHarness(t, nil)
	order, invoice := h.order(t)
	h.checkout(t, invoice)
	// Model a persisted session whose external checkout window has elapsed.
	// Expiry is deliberately not evidence of whether the provider took payment.
	h.memory.mu.Lock()
	for id, attempt := range h.memory.paymentAttempts {
		checkout := *attempt.Checkout
		checkout.ExpiresAt = time.Now().UTC().Add(-time.Second)
		attempt.Checkout = &checkout
		h.memory.paymentAttempts[id] = attempt
	}
	h.memory.mu.Unlock()
	if status, out := h.customer.request("POST", "/api/v1/invoices/"+invoice.ID+"/checkout", nil, ""); status != 409 || out["error"].(map[string]any)["code"] != "PAYMENT_PENDING" {
		t.Fatal("expired checkout was reissued", status, out)
	}
	if creates, _ := h.runtime.counts(); creates != 1 {
		t.Fatal("expired checkout called provider again", creates)
	}
	if status, out := h.customer.request("POST", "/api/v1/orders/"+order.ID+"/cancel", map[string]string{"reason": "Cancel expired checkout"}, ""); status != 409 {
		t.Fatal("expired checkout allowed cancellation", status, out)
	}
	h.pair(t, order, invoice, "unpaid")
	raw, signature := h.runtime.signed(t, h.runtime.event(t, 0))
	if status, out := paymentHTTPNotify(t, h, "POST", "/api/v1/payment-callbacks/"+h.runtime.selection.ID, raw, signature, ""); status != 200 {
		t.Fatal("expired checkout rejected a verified later settlement", status, out)
	}
	h.pair(t, order, invoice, "paid")
}
