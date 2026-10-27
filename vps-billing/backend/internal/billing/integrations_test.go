package billing

import (
	"aster/billing/internal/integrations"
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type checkoutRecorder struct {
	mu       sync.Mutex
	requests []integrations.CheckoutRequest
}

func (p *checkoutRecorder) CreateCheckout(_ context.Context, r integrations.CheckoutRequest) (integrations.Checkout, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, r)
	return integrations.Checkout{ProviderID: "payment.test", SessionID: "test-session", URL: "https://checkout.example.com/session", ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (p *checkoutRecorder) VerifyCallback(context.Context, integrations.CallbackRequest) (integrations.VerifiedPayment, error) {
	return integrations.VerifiedPayment{}, integrations.ErrUnavailable
}

// Only the isolated test implements this adapter; the server selects approved
// runtime workers rather than compiled payment factories.
type recorderRuntime struct{ recorder *checkoutRecorder }

func (p recorderRuntime) PaymentSelection() (pluginapi.Manifest, pluginapi.Selection, error) {
	m := pluginapi.Manifest{Schema: 1, ID: "payment.test", Version: "1.0.0", Protocol: pluginapi.PaymentProtocol, CoreMin: "0.5.3", CoreMaxExclusive: "0.6.0", Capabilities: pluginapi.PaymentCapabilities(), Executable: "plugin", SHA256: strings.Repeat("a", 64), Payment: &paymentv1.Declarations{Currencies: []paymentv1.Currency{{Code: "CNY", Precision: 2}, {Code: "USD", Precision: 2}}, CheckoutHosts: []string{"checkout.example.com"}}}
	return m, pluginapi.Selection{Enabled: true, ID: m.ID, Version: m.Version, ManifestSHA256: strings.Repeat("b", 64), MerchantID: "test-merchant"}, nil
}
func (p recorderRuntime) CreatePayment(ctx context.Context, cfg paymentv1.Config, r paymentv1.Intent) (paymentv1.Checkout, error) {
	out, err := p.recorder.CreateCheckout(ctx, integrations.CheckoutRequest{AttemptID: r.AttemptID, MerchantAccountID: cfg.MerchantID, InvoiceID: r.InvoiceID, OrderID: r.OrderID, AccountID: r.AccountID, AmountMinor: r.AmountMinor, Currency: r.Currency, CurrencyDecimalPlaces: r.CurrencyDecimalPlaces, IdempotencyKey: r.IdempotencyKey})
	return paymentv1.Checkout{ProviderID: out.ProviderID, SessionID: out.SessionID, URL: out.URL, ExpiresAt: out.ExpiresAt}, err
}
func (p recorderRuntime) VerifyPaymentCallback(context.Context, paymentv1.Config, paymentv1.Callback) (paymentv1.VerifiedEvent, error) {
	return paymentv1.VerifiedEvent{}, paymentv1.ErrUnavailable
}
func TestInjectedCheckoutBoundary(t *testing.T) {
	recorder := &checkoutRecorder{}
	registry, e := integrations.New(context.Background(), []integrations.Definition{{Manifest: integrations.Manifest{ID: "payment.test", Kind: integrations.Payment, Name: "Test", Description: "Test only", Capabilities: []string{"checkout"}, SupportedCurrencies: []string{"CNY", "USD"}, CurrencyDecimalPlaces: map[string]int{"CNY": 2, "USD": 2}}, CheckoutHosts: []string{"checkout.example.com"}, PaymentFactory: func(context.Context, integrations.Config) (integrations.PaymentProvider, error) { return recorder, nil }}}, integrations.Options{PaymentID: "payment.test"})
	if e != nil {
		t.Fatal(e)
	}
	m := NewMemory()
	srv := httptest.NewServer(NewAppWithIntegrations(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), PaymentRuntime: recorderRuntime{recorder}}, registry).Handler())
	defer srv.Close()
	c := client(t, srv.URL, "demo@aster.local", "DemoCustomer!2026")
	other := client(t, srv.URL, "other@aster.local", "DemoOther!2026")
	admin := client(t, srv.URL, "admin@aster.local", "DemoAdmin!2026")
	anonymous := client(t, srv.URL, "", "")
	for _, tc := range []struct {
		c    *testClient
		want int
	}{{c, 403}, {other, 403}, {anonymous, 401}, {admin, 200}} {
		s, v := tc.c.request("GET", "/api/v1/admin/integrations", nil, "")
		if s != tc.want {
			t.Fatal(s, v)
		}
	}
	if s, _ := admin.request("POST", "/api/v1/admin/integrations", map[string]any{"enabled": true}, ""); s != 404 {
		t.Fatal("unexpected configuration write", s)
	}
	if s, v := c.request("POST", "/api/v1/orders", map[string]string{"plan_id": "starter", "hostname": "plugin-test"}, "plugin-test-1"); s != 201 {
		t.Fatal(s, v)
	}
	_, v := c.request("GET", "/api/v1/invoices", nil, "")
	inv := v["data"].([]any)[0].(map[string]any)
	id := inv["id"].(string)
	path := "/api/v1/invoices/" + id + "/checkout"
	if s, _ := other.request("POST", path, nil, ""); s != 404 {
		t.Fatal(s)
	}
	token := c.csrf
	c.csrf = "bad"
	if s, _ := c.request("POST", path, nil, ""); s != 403 {
		t.Fatal(s)
	}
	c.csrf = token
	recorder.mu.Lock()
	n := len(recorder.requests)
	recorder.mu.Unlock()
	if n != 0 {
		t.Fatal("adapter called before authorization")
	}
	for range 2 {
		s, v := c.request("POST", path, map[string]any{"amount_minor": 1, "url": "https://evil.example"}, "")
		if s != 200 {
			t.Fatal(s, v)
		}
	}
	recorder.mu.Lock()
	requests := append([]integrations.CheckoutRequest{}, recorder.requests...)
	recorder.mu.Unlock()
	if len(requests) != 1 || requests[0].AmountMinor != 3900 || requests[0].Currency != "CNY" || requests[0].InvoiceID != id || requests[0].OrderID != inv["order_id"] || requests[0].AccountID == "" || requests[0].IdempotencyKey != requests[0].AttemptID || requests[0].AttemptID == "" || requests[0].MerchantAccountID != "test-merchant" {
		t.Fatal(requests)
	}
	_, v = c.request("GET", "/api/v1/invoices/"+id, nil, "")
	if v["data"].(map[string]any)["status"] != "unpaid" {
		t.Fatal("checkout marked paid")
	}
	_, v = c.request("GET", "/api/v1/orders", nil, "")
	if v["data"].([]any)[0].(map[string]any)["status"] != "pending_payment" {
		t.Fatal("checkout changed order state")
	}
	m.mu.Lock()
	for j := range m.invoices {
		if m.invoices[j].ID == id {
			m.invoices[j].Status = "paid"
		}
	}
	m.mu.Unlock()
	if s, _ := c.request("POST", path, nil, ""); s != 409 {
		t.Fatal("paid invoice payable", s)
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.requests) != 1 {
		t.Fatal("paid invoice reached provider")
	}
}
