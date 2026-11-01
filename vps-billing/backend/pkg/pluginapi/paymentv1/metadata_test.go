package paymentv1

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestFlowsLimitsAndPassiveLogos(t *testing.T) {
	d := Declarations{Name: map[string]string{"en": "Bank", "zh-CN": "银行转账"}, Flow: "instructions", Currencies: []Currency{{"USD", 2}}, Min: []CurrencyAmount{{"USD", 100}}, Max: []CurrencyAmount{{"USD", 500}}}
	if d.Validate() != nil || d.WithinLimits("USD", 99) || d.WithinLimits("USD", 501) || !d.WithinLimits("USD", 100) {
		t.Fatal("invalid limits")
	}
	c := Checkout{ProviderID: "payment.bank-transfer", SessionID: "a", Flow: "instructions", Instructions: "Bank\nAccount 123", Reference: "invoice-1"}
	if ValidateCheckout(c, c.ProviderID, d, time.Now()) != nil {
		t.Fatal("instructions rejected")
	}
	c.URL = "https://evil.test"
	if ValidateCheckout(c, c.ProviderID, d, time.Now()) == nil {
		t.Fatal("mixed flow accepted")
	}
	for _, raw := range []string{`<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0L1 1"/></svg>`} {
		if ValidateLogo("data:image/svg+xml;base64,"+base64.StdEncoding.EncodeToString([]byte(raw))) != nil {
			t.Fatal("passive svg rejected")
		}
	}
	for _, raw := range []string{`<svg><script>alert(1)</script></svg>`, `<svg onload="evil()"/>`, `<!DOCTYPE svg [<!ENTITY x SYSTEM "file:///etc/passwd">]><svg>&x;</svg>`, `<svg><image href="https://evil.test"/></svg>`, `<svg><path fill="url(https://evil.test)"/></svg>`, `<svg/><svg/>`, strings.Repeat("a", 16385)} {
		if ValidateLogo("data:image/svg+xml;base64,"+base64.StdEncoding.EncodeToString([]byte(raw))) == nil {
			t.Fatal("active or oversized svg accepted", raw[:10])
		}
	}
	if ValidateLogo("data:image/png;base64,"+base64.StdEncoding.EncodeToString([]byte("not PNG"))) == nil {
		t.Fatal("invalid PNG accepted")
	}
}

func TestQueryCannotInventOrCrossSettlement(t *testing.T) {
	q := Query{Intent: Intent{AttemptID: "a", IdempotencyKey: "a", InvoiceID: "i", OrderID: "o", AccountID: "u", AmountMinor: 1, Currency: "JPY"}, SessionID: "s"}
	d := Declarations{Currencies: []Currency{{"JPY", 0}}, CheckoutHosts: []string{"checkout.test"}}
	cfg := Config{ProviderID: "payment.test", MerchantID: "m", GatewayID: "gateway-a"}
	e := VerifiedEvent{AttemptID: "a", ProviderID: cfg.ProviderID, GatewayID: cfg.GatewayID, EventID: "e", TransactionID: "t", MerchantAccountID: "m", AccountID: "u", OrderID: "o", InvoiceID: "i", AmountMinor: 1, Currency: "JPY", SettledAt: time.Now(), Status: "settled"}
	if ValidateQueryResult(QueryResult{Status: "succeeded", Event: &e}, cfg, q, d, time.Now()) != nil {
		t.Fatal("valid query rejected")
	}
	e.GatewayID = "gateway-b"
	if ValidateQueryResult(QueryResult{Status: "succeeded", Event: &e}, cfg, q, d, time.Now()) == nil {
		t.Fatal("query crossed slot")
	}
	if ValidateQueryResult(QueryResult{Status: "succeeded"}, cfg, q, d, time.Now()) == nil {
		t.Fatal("success without evidence")
	}
}
