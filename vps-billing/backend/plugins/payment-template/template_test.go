package paymenttemplate

import (
	"aster/billing/pkg/pluginapi/paymentv1"
	"aster/billing/pkg/pluginapi/paymentv1/conformance"
	"strings"
	"testing"
)

func TestConformance(t *testing.T) {
	conformance.Test(t, conformance.Suite{Manifest: Manifest(strings.Repeat("a", 64)), Config: paymentv1.Config{ProviderID: ID, GatewayID: "example", MerchantID: "merchant", ManifestSHA256: strings.Repeat("b", 64)}, Processor: Processor{}, Intent: paymentv1.Intent{AttemptID: "attempt", IdempotencyKey: "attempt", AccountID: "account", OrderID: "order", InvoiceID: "invoice", AmountMinor: 100, Currency: "USD", CurrencyDecimalPlaces: 2}})
}
