package banktransfer

import (
	"aster/billing/pkg/pluginapi/paymentv1"
	"aster/billing/pkg/pluginapi/paymentv1/conformance"
	"strings"
	"testing"
)

func TestBankConformance(t *testing.T) {
	p, err := New(Config{MerchantID: "bank-account", Instructions: "Example Bank\nAccount: TEST-001\nInclude the payment reference."})
	if err != nil {
		t.Fatal(err)
	}
	conformance.Test(t, conformance.Suite{Manifest: Manifest(strings.Repeat("a", 64)), Config: paymentv1.Config{ProviderID: ID, GatewayID: "bank-us", MerchantID: "bank-account", ManifestSHA256: strings.Repeat("b", 64)}, Processor: p, Intent: paymentv1.Intent{AttemptID: "attempt", IdempotencyKey: "attempt", InvoiceID: "invoice", OrderID: "order", AccountID: "customer", AmountMinor: 123, Currency: "USD", CurrencyDecimalPlaces: 2}})
}
func TestBankRejectsInvalidConfiguration(t *testing.T) {
	for _, c := range []Config{{}, {MerchantID: "a", Instructions: "secret\x00"}, {MerchantID: "a", Instructions: strings.Repeat("x", 8001)}} {
		if _, e := New(c); e == nil {
			t.Fatal("invalid config accepted")
		}
	}
}
