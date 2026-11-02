package paymentv1

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestFlowsLimitsAndPassiveLogos(t *testing.T) {
	d := Declarations{Name: map[string]string{"en": "Fixture", "zh-CN": "测试支付"}, Flow: "instructions", Currencies: []Currency{{"USD", 2}}, Min: []CurrencyAmount{{"USD", 100}}, Max: []CurrencyAmount{{"USD", 500}}}
	if d.Validate() != nil || d.WithinLimits("USD", 99) || d.WithinLimits("USD", 501) || !d.WithinLimits("USD", 100) {
		t.Fatal("invalid limits")
	}
	c := Checkout{ProviderID: "payment.fixture", SessionID: "a", Flow: "instructions", Instructions: "Fixture instructions\nDo not send funds.", Reference: "invoice-1"}
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
	// The transport value, including its MIME prefix and base64 expansion, must
	// fit the public 16 KiB limit even when the decoded image would fit.
	for _, size := range []int{12000, 12300} {
		raw := "<svg><desc>" + strings.Repeat("a", size) + "</desc></svg>"
		uri := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(raw))
		if (ValidateLogo(uri) == nil) != (len(uri) <= 16<<10) {
			t.Fatal("logo URI size limit", len(uri))
		}
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

func TestExplicitRedirectPreservesFragmentWithinApprovedHost(t *testing.T) {
	d := Declarations{Flow: "redirect", Currencies: []Currency{{"USD", 2}}, CheckoutHosts: []string{"checkout.example.test"}}
	c := Checkout{ProviderID: "payment.fixture", SessionID: "session", Flow: "redirect", URL: "https://checkout.example.test/c/pay/session#opaque%2Fdata", ExpiresAt: time.Now().Add(time.Hour)}
	if ValidateCheckout(c, c.ProviderID, d, time.Now()) != nil {
		t.Fatal("approved provider fragment rejected")
	}
	legacy := d
	legacy.Flow = ""
	if ValidateCheckout(c, c.ProviderID, legacy, time.Now()) == nil {
		t.Fatal("legacy fragment rule changed")
	}
	for _, raw := range []string{"https://checkout.example.test.evil.test/c#x", "https://evil.test/#https://checkout.example.test", "https://user@checkout.example.test/#x", "https://checkout.example.test:8443/#x", "http://checkout.example.test/#x", "https://checkout.example.test/#x\n", "https://checkout.example.test/#" + strings.Repeat("x", 2048)} {
		c.URL = raw
		if ValidateCheckout(c, c.ProviderID, d, time.Now()) == nil {
			t.Fatal("unsafe redirect accepted", raw[:30])
		}
	}
}
