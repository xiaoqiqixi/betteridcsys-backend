package billing

import (
	"aster/billing/internal/integrations"
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"aster/billing/plugins/paymentfixture"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// This runtime delegates verification to the real, network-free fixture HMAC
// processor. It records boundary inputs without granting the worker a Store.
type paymentHTTPRuntime struct {
	mu           sync.Mutex
	manifest     pluginapi.Manifest
	selection    pluginapi.Selection
	processor    *paymentfixture.Processor
	secret       []byte
	createErr    error
	duringCreate func(context.Context, paymentv1.Config, paymentv1.Intent)
	intents      []paymentv1.Intent
	callbacks    []paymentv1.Callback
}

func newPaymentHTTPRuntime(t *testing.T) *paymentHTTPRuntime {
	t.Helper()
	key := bytes.Repeat([]byte{0x42}, 32)
	processor, err := paymentfixture.New(key)
	if err != nil {
		t.Fatal(err)
	}
	declarations := paymentfixture.Declarations()
	return &paymentHTTPRuntime{
		manifest: pluginapi.Manifest{Schema: 1, ID: paymentfixture.ProviderID, Version: "1.0.0", Protocol: pluginapi.PaymentProtocol,
			CoreMin: "0.5.3", CoreMaxExclusive: "0.6.0", Capabilities: pluginapi.PaymentCapabilities(), Executable: "plugin", SHA256: strings.Repeat("a", 64), Payment: &declarations},
		selection: pluginapi.Selection{Enabled: true, ID: paymentfixture.ProviderID, Version: "1.0.0", ManifestSHA256: strings.Repeat("b", 64), MerchantID: "fixture-merchant"},
		processor: processor, secret: key,
	}
}

func (p *paymentHTTPRuntime) PaymentSelection() (pluginapi.Manifest, pluginapi.Selection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.manifest, p.selection, nil
}
func (p *paymentHTTPRuntime) CreatePayment(ctx context.Context, cfg paymentv1.Config, in paymentv1.Intent) (paymentv1.Checkout, error) {
	p.mu.Lock()
	p.intents = append(p.intents, in)
	err := p.createErr
	hook := p.duringCreate
	p.mu.Unlock()
	if hook != nil {
		hook(ctx, cfg, in)
	}
	if err != nil {
		return paymentv1.Checkout{}, err
	}
	return p.processor.Create(ctx, cfg, in)
}
func (p *paymentHTTPRuntime) VerifyPaymentCallback(ctx context.Context, cfg paymentv1.Config, in paymentv1.Callback) (paymentv1.VerifiedEvent, error) {
	p.mu.Lock()
	p.callbacks = append(p.callbacks, in)
	p.mu.Unlock()
	return p.processor.Verify(ctx, cfg, in)
}
func (p *paymentHTTPRuntime) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.intents), len(p.callbacks)
}
func (p *paymentHTTPRuntime) event(t *testing.T, intentIndex int) paymentv1.VerifiedEvent {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if intentIndex >= len(p.intents) {
		t.Fatal("checkout did not reach synthetic worker")
	}
	in := p.intents[intentIndex]
	return paymentv1.VerifiedEvent{AttemptID: in.AttemptID, ProviderID: p.selection.ID, EventID: "event-" + in.AttemptID,
		TransactionID: "transaction-" + in.AttemptID, MerchantAccountID: p.selection.MerchantID, AccountID: in.AccountID,
		OrderID: in.OrderID, InvoiceID: in.InvoiceID, AmountMinor: in.AmountMinor, Currency: in.Currency,
		CurrencyDecimalPlaces: in.CurrencyDecimalPlaces, SettledAt: time.Now().UTC().Truncate(time.Microsecond), Status: "settled"}
}
func (p *paymentHTTPRuntime) signed(t *testing.T, event paymentv1.VerifiedEvent) ([]byte, string) {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := paymentfixture.Signature(p.secret, raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw, signature
}

type paymentHTTPHarness struct {
	memory   *Memory
	runtime  *paymentHTTPRuntime
	server   *httptest.Server
	owner    User
	customer *testClient
}

func newPaymentHTTPHarness(t *testing.T, decorate func(Store) Store) *paymentHTTPHarness {
	t.Helper()
	m := NewMemory()
	owner, _, _ := cancellationUsers(t, m)
	runtime := newPaymentHTTPRuntime(t)
	var store Store = m
	if decorate != nil {
		store = decorate(store)
	}
	server := httptest.NewServer(NewApp(store, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), PaymentRuntime: runtime}).Handler())
	t.Cleanup(server.Close)
	return &paymentHTTPHarness{memory: m, runtime: runtime, server: server, owner: owner,
		customer: client(t, server.URL, "demo@aster.local", "DemoCustomer!2026")}
}

func (h *paymentHTTPHarness) order(t *testing.T) (Order, Invoice) {
	t.Helper()
	o, i, _ := cancellationOrder(t, h.memory, h.owner)
	return o, i
}
func (h *paymentHTTPHarness) checkout(t *testing.T, invoice Invoice) {
	t.Helper()
	status, out := h.customer.request("POST", "/api/v1/invoices/"+invoice.ID+"/checkout", map[string]any{"amount_minor": 1, "currency": "USD", "status": "paid", "checkout_url": "https://attacker.example"}, "")
	if status != 200 {
		t.Fatal("synthetic checkout", status, out)
	}
}
func (h *paymentHTTPHarness) pair(t *testing.T, order Order, invoice Invoice, status string) {
	t.Helper()
	stored, err := h.memory.Invoice(t.Context(), h.owner.ID, invoice.ID)
	if err != nil || stored.Status != status || stored.AmountMinor != invoice.AmountMinor || stored.Currency != invoice.Currency {
		t.Fatal("invoice state/amount changed unexpectedly", stored, err)
	}
	orders, err := h.memory.Orders(t.Context(), h.owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := "pending_payment"
	if status == "paid" {
		want = "paid"
	}
	for _, current := range orders {
		if current.ID == order.ID {
			if current.Status != want || current.TotalMinor != order.TotalMinor {
				t.Fatal("order was not atomically paired with invoice", current)
			}
			return
		}
	}
	t.Fatal("missing order")
}

// Notifications deliberately use a fresh HTTP client with no browser session or
// CSRF token. Their authorization comes exclusively from the raw signed body.
func paymentHTTPNotify(t *testing.T, h *paymentHTTPHarness, method, path string, raw []byte, signature, host string) (int, map[string]any) {
	t.Helper()
	request, err := http.NewRequest(method, h.server.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if signature != "" {
		request.Header.Set(paymentfixture.SignatureHeader, signature)
	}
	// Browser credentials must be stripped before the adapter boundary.
	request.Header.Set("Cookie", "aster_demo_session=must-not-reach-worker")
	request.Header.Set("X-CSRF-Token", "must-not-reach-worker")
	request.Header.Set("Origin", "https://third-party.example.test")
	if host != "" {
		request.Host = host
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var out map[string]any
	if err = json.NewDecoder(response.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, out
}

func TestPaymentHTTPAuthorizationPrecedesReservation(t *testing.T) {
	h := newPaymentHTTPHarness(t, nil)
	_, invoice := h.order(t)
	path := "/api/v1/invoices/" + invoice.ID + "/checkout"
	other := client(t, h.server.URL, "other@aster.local", "DemoOther!2026")
	anonymous := client(t, h.server.URL, "", "")
	if status, out := other.request("POST", path, nil, ""); status != 404 {
		t.Fatal("foreign invoice", status, out)
	}
	if status, out := anonymous.request("POST", path, nil, ""); status != 401 {
		t.Fatal("anonymous checkout", status, out)
	}
	original := h.customer.csrf
	h.customer.csrf = "invalid"
	if status, out := h.customer.request("POST", path, nil, ""); status != 403 {
		t.Fatal("CSRF checkout", status, out)
	}
	h.customer.csrf = original
	if status, out := cancellationRawRequest(t, h.customer, path, `{}`, "", "attacker.example"); status != 421 {
		t.Fatal("host checkout", status, out)
	}
	if calls, _ := h.runtime.counts(); calls != 0 || len(h.memory.paymentAttempts) != 0 {
		t.Fatal("rejected authorization reserved or invoked payment", calls)
	}
}

func TestPaymentHTTPSelectionAndCurrencyFailBeforeReservation(t *testing.T) {
	for _, name := range []string{"disabled", "version mismatch", "digest invalid", "currency unsupported"} {
		t.Run(name, func(t *testing.T) {
			h := newPaymentHTTPHarness(t, nil)
			_, invoice := h.order(t)
			h.runtime.mu.Lock()
			switch name {
			case "disabled":
				h.runtime.selection.Enabled = false
			case "version mismatch":
				h.runtime.selection.Version = "9.0.0"
			case "digest invalid":
				h.runtime.selection.ManifestSHA256 = "invalid"
			case "currency unsupported":
				h.runtime.manifest.Payment.Currencies = []paymentv1.Currency{{Code: "JPY", Precision: 0}}
			}
			h.runtime.mu.Unlock()
			want := 503
			if name == "currency unsupported" {
				want = 409
			}
			if status, out := h.customer.request("POST", "/api/v1/invoices/"+invoice.ID+"/checkout", nil, ""); status != want {
				t.Fatal(status, out)
			}
			if calls, _ := h.runtime.counts(); calls != 0 || len(h.memory.paymentAttempts) != 0 {
				t.Fatal("unapproved/unsupported checkout crossed boundary", calls)
			}
		})
	}
}

func TestPaymentHTTPCallbackBoundsBeforeVerification(t *testing.T) {
	h := newPaymentHTTPHarness(t, nil)
	path := "/api/v1/payment-callbacks/" + paymentfixture.ProviderID
	for _, tc := range []struct {
		name, method, path, host string
		body                     []byte
		status                   int
	}{
		{"oversized", "POST", path, "", bytes.Repeat([]byte{'x'}, paymentv1.MaxBodyBytes+1), 413},
		{"wrong host", "POST", path, "attacker.example", []byte(`{}`), 421},
		{"wrong provider", "POST", "/api/v1/payment-callbacks/payment.other", "", []byte(`{}`), 503},
		{"wrong method", "GET", path, "", []byte(`{}`), 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if status, out := paymentHTTPNotify(t, h, tc.method, tc.path, tc.body, "", tc.host); status != tc.status {
				t.Fatal(status, out)
			}
		})
	}
	if _, calls := h.runtime.counts(); calls != 0 || len(h.memory.paymentReceipts) != 0 {
		t.Fatal("malformed notification invoked verification or storage", calls)
	}
}

func TestPaymentHTTPSyntheticSignatureSettlementAndReplay(t *testing.T) {
	h := newPaymentHTTPHarness(t, nil)
	order, invoice := h.order(t)
	h.checkout(t, invoice)
	h.checkout(t, invoice)
	if calls, _ := h.runtime.counts(); calls != 1 {
		t.Fatal("checkout retry created another session", calls)
	}
	h.pair(t, order, invoice, "unpaid")
	event := h.runtime.event(t, 0)
	if event.AmountMinor != invoice.AmountMinor || event.Currency != invoice.Currency || event.InvoiceID != invoice.ID || event.OrderID != order.ID || event.AccountID != h.owner.ID {
		t.Fatal("client checkout fields replaced canonical invoice", event)
	}
	path := "/api/v1/payment-callbacks/" + paymentfixture.ProviderID
	raw, signature := h.runtime.signed(t, event)
	for _, tc := range []struct {
		name      string
		body      []byte
		signature string
	}{
		{"unsigned browser success", raw, ""},
		{"body tampered after signing", append(append([]byte{}, raw...), ' '), signature},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if status, out := paymentHTTPNotify(t, h, "POST", path, tc.body, tc.signature, ""); status != 400 {
				t.Fatal(status, out)
			}
			h.pair(t, order, invoice, "unpaid")
		})
	}
	for _, mutate := range []func(*paymentv1.VerifiedEvent){
		func(v *paymentv1.VerifiedEvent) { v.AmountMinor++ },
		func(v *paymentv1.VerifiedEvent) { v.AccountID = ID() },
		func(v *paymentv1.VerifiedEvent) { v.InvoiceID = ID() },
		func(v *paymentv1.VerifiedEvent) { v.OrderID = ID() },
		func(v *paymentv1.VerifiedEvent) { v.Currency = "USD" },
	} {
		wrong := event
		mutate(&wrong)
		body, sig := h.runtime.signed(t, wrong)
		if status, out := paymentHTTPNotify(t, h, "POST", path, body, sig, ""); status != 409 {
			t.Fatal("signed mismatched evidence accepted", status, out)
		}
		h.pair(t, order, invoice, "unpaid")
	}
	for range 2 {
		if status, out := paymentHTTPNotify(t, h, "POST", path, raw, signature, ""); status != 200 {
			t.Fatal("verified notification/replay", status, out)
		}
	}
	h.pair(t, order, invoice, "paid")
	if len(h.memory.paymentReceipts) != 1 || len(h.memory.audits) != 2 || len(h.memory.orderEvents) != 2 || len(h.memory.services) != 0 {
		t.Fatal("callback replay duplicated accounting/history or provisioned a service")
	}
	h.runtime.mu.Lock()
	for _, callback := range h.runtime.callbacks {
		for _, header := range callback.Headers {
			if strings.EqualFold(header.Name, "Cookie") || strings.EqualFold(header.Name, "X-CSRF-Token") {
				t.Error("browser credential reached worker", header.Name)
			}
		}
	}
	h.runtime.mu.Unlock()
	conflict := event
	conflict.TransactionID += "-different"
	body, sig := h.runtime.signed(t, conflict)
	if status, out := paymentHTTPNotify(t, h, "POST", path, body, sig, ""); status != 409 {
		t.Fatal("event replay accepted different transaction", status, out)
	}
	secondOrder, secondInvoice := h.order(t)
	h.checkout(t, secondInvoice)
	reused := h.runtime.event(t, 1)
	reused.TransactionID = event.TransactionID
	body, sig = h.runtime.signed(t, reused)
	if status, out := paymentHTTPNotify(t, h, "POST", path, body, sig, ""); status != 409 {
		t.Fatal("channel transaction reused for another invoice", status, out)
	}
	h.pair(t, secondOrder, secondInvoice, "unpaid")
	if len(h.memory.paymentReceipts) != 1 {
		t.Fatal("conflict created accounting entry")
	}
}

func TestPaymentHTTPUncertainCheckoutBlocksRetryAndCancellation(t *testing.T) {
	h := newPaymentHTTPHarness(t, nil)
	order, invoice := h.order(t)
	h.runtime.createErr = context.DeadlineExceeded
	path := "/api/v1/invoices/" + invoice.ID + "/checkout"
	for range 2 {
		if status, out := h.customer.request("POST", path, nil, ""); status != 409 || out["error"].(map[string]any)["code"] != "PAYMENT_PENDING" {
			t.Fatal("uncertain checkout was treated as safely failed", status, out)
		}
	}
	if calls, _ := h.runtime.counts(); calls != 1 || len(h.memory.paymentAttempts) != 1 {
		t.Fatal("uncertain operation was retried", calls)
	}
	if status, out := h.customer.request("POST", "/api/v1/orders/"+order.ID+"/cancel", map[string]string{"reason": "Cancel uncertain payment"}, ""); status != 409 {
		t.Fatal("in-flight payment was cancelled", status, out)
	}
	h.pair(t, order, invoice, "unpaid")
	// A provider may have accepted a create request whose response was lost. A
	// later verified notification must still settle its durable pending attempt.
	event := h.runtime.event(t, 0)
	raw, signature := h.runtime.signed(t, event)
	if status, out := paymentHTTPNotify(t, h, "POST", "/api/v1/payment-callbacks/"+paymentfixture.ProviderID, raw, signature, ""); status != 200 {
		t.Fatal("late settlement after timeout", status, out)
	}
	h.pair(t, order, invoice, "paid")
}

type paymentHTTPFailSettlementStore struct{ Store }

func (s *paymentHTTPFailSettlementStore) SettlePayment(context.Context, string, integrations.VerifiedPayment) (PaymentSettlement, error) {
	return PaymentSettlement{}, errors.New("synthetic storage outage")
}
func TestPaymentHTTPStorageFailureDoesNotAcknowledgeSettlement(t *testing.T) {
	h := newPaymentHTTPHarness(t, func(store Store) Store { return &paymentHTTPFailSettlementStore{Store: store} })
	order, invoice := h.order(t)
	h.checkout(t, invoice)
	raw, signature := h.runtime.signed(t, h.runtime.event(t, 0))
	if status, out := paymentHTTPNotify(t, h, "POST", "/api/v1/payment-callbacks/"+paymentfixture.ProviderID, raw, signature, ""); status != 503 {
		t.Fatal("storage failure acknowledged payment", status, out)
	}
	h.pair(t, order, invoice, "unpaid")
	if len(h.memory.paymentReceipts) != 0 {
		t.Fatal("failed settlement created accounting entry")
	}
}

// The hook simulates a verified callback winning the race while the independent
// create request is still in flight. Production adapters have no Store access.
func TestPaymentHTTPSettlementBeforeCreateReturnsNoCheckoutURL(t *testing.T) {
	for _, createErr := range []error{nil, context.DeadlineExceeded} {
		name := "create accepted"
		if createErr != nil {
			name = "create response lost"
		}
		t.Run(name, func(t *testing.T) {
			h := newPaymentHTTPHarness(t, nil)
			order, invoice := h.order(t)
			settled := make(chan error, 1)
			h.runtime.createErr = createErr
			h.runtime.duringCreate = func(ctx context.Context, cfg paymentv1.Config, in paymentv1.Intent) {
				_, err := h.memory.SettlePayment(ctx, in.AttemptID, integrations.VerifiedPayment{
					AttemptID: in.AttemptID, ProviderID: cfg.ProviderID, EventID: "early-event-" + in.AttemptID,
					TransactionID: "early-transaction-" + in.AttemptID, MerchantAccountID: cfg.MerchantID,
					AccountID: in.AccountID, OrderID: in.OrderID, InvoiceID: in.InvoiceID,
					AmountMinor: in.AmountMinor, Currency: in.Currency, CurrencyDecimalPlaces: in.CurrencyDecimalPlaces,
					SettledAt: time.Now().UTC().Truncate(time.Microsecond), Status: "settled",
				})
				settled <- err
			}
			status, out := h.customer.request("POST", "/api/v1/invoices/"+invoice.ID+"/checkout", nil, "")
			if err := <-settled; err != nil {
				t.Fatal("early settlement failed", err)
			}
			if status != 409 || out["error"].(map[string]any)["code"] != "INVOICE_NOT_PAYABLE" || out["data"] != nil {
				t.Fatal("known settled invoice returned an active checkout", status, out)
			}
			h.pair(t, order, invoice, "paid")
			if len(h.memory.paymentReceipts) != 1 {
				t.Fatal("early settlement duplicated accounting")
			}
		})
	}
}
