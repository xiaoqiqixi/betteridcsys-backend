package stripe

import (
	"aster/billing/pkg/pluginapi/paymentv1"
	"aster/billing/pkg/pluginapi/paymentv1/conformance"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSignatureFixtures(t *testing.T) {
	raw, e := os.ReadFile("testdata/signature.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		Body, Secret, Header string
		Timestamp            int64
	}
	if json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture")
	}
	now := time.Unix(f.Timestamp, 0)
	for _, test := range []struct {
		name, body, header, secret string
		now                        time.Time
		ok                         bool
	}{
		{"exact", f.Body, f.Header, f.Secret, now, true},
		{"rotation", f.Body, f.Header + ",v1=" + strings.Repeat("0", 64), f.Secret, now, true},
		{"wrong secret", f.Body, f.Header, "whsec_wrong", now, false},
		{"raw whitespace", f.Body + " ", f.Header, f.Secret, now, false},
		{"expired", f.Body, f.Header, f.Secret, now.Add(301 * time.Second), false},
		{"future", f.Body, f.Header, f.Secret, now.Add(-31 * time.Second), false},
		{"duplicate timestamp", f.Body, f.Header + ",t=" + strconv.FormatInt(f.Timestamp, 10), f.Secret, now, false},
		{"v0", f.Body, strings.Replace(f.Header, "v1=", "v0=", 1), f.Secret, now, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if e := VerifySignature([]byte(test.body), test.header, test.secret, test.now); (e == nil) != test.ok {
				t.Fatal(e)
			}
		})
	}
}
func sign(body []byte, secret string, now time.Time) string {
	stamp := strconv.FormatInt(now.Unix(), 10)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(stamp + "."))
	m.Write(body)
	return "t=" + stamp + ",v1=" + hex.EncodeToString(m.Sum(nil))
}
func TestStripeOfflineConformanceAndIsolation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cfg := Config{AccountID: "acct_fixture", APIKey: "sk_test_fixture_secret", WebhookSecret: "whsec_fixture_secret", SuccessURL: "https://billing.example/invoices", CancelURL: "https://billing.example/invoices"}
	c := paymentv1.Config{ProviderID: ID, GatewayID: "stripe-us", MerchantID: cfg.AccountID, ManifestSHA256: strings.Repeat("a", 64)}
	in := paymentv1.Intent{AttemptID: "attempt", IdempotencyKey: "attempt", InvoiceID: "invoice", OrderID: "order", AccountID: "customer", Currency: "USD", AmountMinor: 1200, CurrencyDecimalPlaces: 2}
	pi := intent{ID: "pi_fixture", Status: "succeeded", AmountReceived: 1200, Currency: "usd", Metadata: metadata(c, in), Charge: charge{ID: "ch_fixture", PaymentIntent: "pi_fixture", Paid: true, Captured: true, Status: "succeeded", AmountCaptured: 1200, Currency: "usd", Created: now.Add(-time.Minute).Unix()}}
	piJSON, _ := json.Marshal(pi)
	s := session{ID: "cs_fixture", Object: "checkout.session", Mode: "payment", URL: "https://checkout.stripe.com/c/pay/cs_fixture#provider-fragment", ExpiresAt: now.Add(time.Hour).Unix(), AmountTotal: 1200, Currency: "usd", PaymentStatus: "paid", Status: "complete", Metadata: metadata(c, in), PaymentIntent: piJSON}
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		user, _, ok := r.BasicAuth()
		if !ok || user != cfg.APIKey || r.Header.Get("Stripe-Version") != APIVersion {
			t.Error("missing provider authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/account":
			json.NewEncoder(w).Encode(map[string]string{"id": cfg.AccountID, "object": "account"})
		case r.Method == "POST" && r.URL.Path == "/v1/checkout/sessions":
			if r.ParseForm() != nil || r.Form.Get("line_items[0][price_data][unit_amount]") != "1200" || r.Form.Get("metadata[aster_gateway]") != c.GatewayID || r.Header.Get("Idempotency-Key") != "checkout:stripe-us:attempt" {
				t.Error("checkout parameters changed")
			}
			if r.Form.Get("payment_method_types[0]") != "card" || r.Form.Get("payment_intent_data[capture_method]") != "automatic" || r.Form.Get("adaptive_pricing[enabled]") != "false" || r.Form.Get("automatic_tax[enabled]") != "false" {
				t.Error("payment timing or exact currency policy changed")
			}
			json.NewEncoder(w).Encode(s)
		case r.Method == "GET" && r.URL.Path == "/v1/checkout/sessions/cs_fixture":
			if r.URL.Query().Get("expand[]") != "payment_intent.latest_charge" {
				t.Error("missing settlement expansion")
			}
			json.NewEncoder(w).Encode(s)
		case r.Method == "GET" && r.URL.Path == "/v1/payment_intents/pi_fixture":
			json.NewEncoder(w).Encode(map[string]any{"id": pi.ID, "status": pi.Status, "metadata": pi.Metadata, "currency": pi.Currency, "amount_received": pi.AmountReceived, "livemode": pi.LiveMode, "latest_charge": pi.Charge.ID})
		case r.Method == "POST" && r.URL.Path == "/v1/refunds":
			r.ParseForm()
			if r.Form.Get("amount") != "200" || r.Form.Get("payment_intent") != "pi_fixture" || r.Header.Get("Idempotency-Key") != "refund:stripe-us:refund-request" {
				t.Error("refund parameters changed")
			}
			json.NewEncoder(w).Encode(map[string]any{"id": "re_fixture", "status": "succeeded", "payment_intent": "pi_fixture", "currency": "usd", "amount": 200})
		default:
			t.Error("unexpected provider request", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	p, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	p.endpoint = server.URL
	p.now = func() time.Time { return now }
	body := []byte(`{"id":"evt_fixture","type":"checkout.session.completed","livemode":false,"data":{"object":{"id":"cs_fixture"}}}`)
	callback := paymentv1.Callback{Method: "POST", Path: "/api/v1/payment-callbacks/stripe-us", Body: body, Headers: []paymentv1.Header{{Name: "Stripe-Signature", Values: []string{sign(body, cfg.WebhookSecret, now)}}}}
	refund := paymentv1.Refund{IdempotencyKey: "refund-request", TransactionID: "pi_fixture", AmountMinor: 200, Currency: "USD", CurrencyDecimalPlaces: 2}
	conformance.Test(t, conformance.Suite{Manifest: Manifest(strings.Repeat("b", 64)), Config: c, Processor: p, Intent: in, Callback: &callback, Refund: &refund})
	checkout, e := p.Create(t.Context(), c, in)
	if e != nil || checkout.URL != s.URL {
		t.Fatal("provider URL was rewritten", e)
	}
	query := paymentv1.Query{Intent: in, SessionID: "cs_fixture"}
	q, e := p.Query(t.Context(), c, query)
	if e != nil {
		t.Fatal(e)
	}
	v, e := p.Verify(t.Context(), c, callback)
	if e != nil || *q.Event != v {
		t.Fatal("poll/callback evidence differs", e)
	}
	wrong := c
	wrong.GatewayID = "stripe-eu"
	if _, e = p.Verify(t.Context(), wrong, callback); e == nil {
		t.Fatal("callback crossed gateway")
	}
	if _, e = p.Refund(t.Context(), wrong, refund); e == nil {
		t.Fatal("refund crossed gateway")
	}
	mu.Lock()
	before := calls
	mu.Unlock()
	callback.Body = append(callback.Body, ' ')
	if _, e = p.Verify(t.Context(), c, callback); e == nil {
		t.Fatal("unsigned callback accepted")
	}
	mu.Lock()
	after := calls
	mu.Unlock()
	if before != after {
		t.Fatal("invalid signature contacted provider")
	}
	query.Intent.AmountMinor++
	if _, e = p.Query(t.Context(), c, query); e == nil {
		t.Fatal("changed amount accepted")
	}
}

func TestStripeDoesNotFollowProviderRedirects(t *testing.T) {
	p, e := New(Config{AccountID: "acct_fixture", APIKey: "sk_test_fixture_secret", WebhookSecret: "whsec_fixture_secret", SuccessURL: "https://billing.example", CancelURL: "https://billing.example"})
	if e != nil {
		t.Fatal(e)
	}
	if p.client.CheckRedirect(&http.Request{}, nil) != http.ErrUseLastResponse {
		t.Fatal("redirect policy")
	}
	if p.client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("environment proxy enabled")
	}
}

func TestStripeRejectsAPIKeysForAnotherMerchant(t *testing.T) {
	now := time.Now()
	cfg := Config{AccountID: "acct_expected", APIKey: "sk_test_fixture_secret", WebhookSecret: "whsec_fixture_secret", SuccessURL: "https://billing.example", CancelURL: "https://billing.example"}
	c := paymentv1.Config{ProviderID: ID, GatewayID: "stripe-us", MerchantID: cfg.AccountID, ManifestSHA256: strings.Repeat("a", 64)}
	in := paymentv1.Intent{AttemptID: "attempt", IdempotencyKey: "attempt", InvoiceID: "invoice", OrderID: "order", AccountID: "customer", Currency: "USD", AmountMinor: 1200, CurrencyDecimalPlaces: 2}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/account" {
			t.Error("mismatched merchant reached payment operation", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id": "acct_other", "object": "account"})
	}))
	defer server.Close()
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p.endpoint = server.URL
	if _, err = p.Create(t.Context(), c, in); err == nil {
		t.Fatal("created checkout for another merchant")
	}
	if _, err = p.Query(t.Context(), c, paymentv1.Query{Intent: in, SessionID: "cs_fixture"}); err == nil {
		t.Fatal("queried another merchant")
	}
	if _, err = p.Refund(t.Context(), c, paymentv1.Refund{IdempotencyKey: "refund", TransactionID: "pi_fixture", AmountMinor: 200, Currency: "USD", CurrencyDecimalPlaces: 2}); err == nil {
		t.Fatal("refunded another merchant")
	}
	body := []byte(`{"id":"evt_fixture","type":"checkout.session.completed","livemode":false,"data":{"object":{"id":"cs_fixture"}}}`)
	callback := paymentv1.Callback{Method: "POST", Path: "/api/v1/payment-callbacks/stripe-us", Body: body, Headers: []paymentv1.Header{{Name: "Stripe-Signature", Values: []string{sign(body, cfg.WebhookSecret, now)}}}}
	if _, err = p.Verify(t.Context(), c, callback); err == nil {
		t.Fatal("verified another merchant")
	}
}
