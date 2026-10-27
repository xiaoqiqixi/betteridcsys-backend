package integrations_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"aster/billing/internal/integrations"
)

// contractOnlyPayment demonstrates the compiled boundary. It has no transport,
// credentials, callback verifier or financial store, and always fails closed.
// A _test.go file cannot register this adapter in the production server.
type contractOnlyPayment struct{ checkoutCalls int }

var _ integrations.PaymentProvider = (*contractOnlyPayment)(nil)

func (p *contractOnlyPayment) CreateCheckout(context.Context, integrations.CheckoutRequest) (integrations.Checkout, error) {
	p.checkoutCalls++
	return integrations.Checkout{}, integrations.ErrUnavailable
}

func (*contractOnlyPayment) VerifyCallback(context.Context, integrations.CallbackRequest) (integrations.VerifiedPayment, error) {
	// Never turn an unsigned or otherwise unverified body into VerifiedPayment.
	return integrations.VerifiedPayment{}, integrations.ErrUnavailable
}

func ExamplePaymentProvider() {
	provider := &contractOnlyPayment{}
	definition := integrations.Definition{
		Manifest: integrations.Manifest{
			ID: "payment.contract-example", Kind: integrations.Payment,
			Name: "Contract example", Description: "Local example that refuses all payment operations",
			Capabilities: []string{"checkout"}, SupportedCurrencies: []string{"CNY"},
			CurrencyDecimalPlaces: map[string]int{"CNY": 2},
			Limitations:           []string{"Test only; no checkout session or verified callback is produced"},
		},
		CheckoutHosts: []string{"checkout.example.invalid"}, // Declaration only; never contacted.
		PaymentFactory: func(context.Context, integrations.Config) (integrations.PaymentProvider, error) {
			return provider, nil
		},
	}
	registry, err := integrations.New(context.Background(), []integrations.Definition{definition}, integrations.Options{
		PaymentID: definition.Manifest.ID,
	})
	if err != nil {
		panic(err)
	}
	request := integrations.CheckoutRequest{
		InvoiceID: "fixture-invoice", OrderID: "fixture-order", AccountID: "fixture-account",
		AmountMinor: 3900, Currency: "CNY", CurrencyDecimalPlaces: 2,
		IdempotencyKey: "invoice:fixture-invoice",
	}
	_, err = registry.CreateCheckout(context.Background(), request)
	fmt.Println("checkout refused:", errors.Is(err, integrations.ErrUnavailable))
	request.CurrencyDecimalPlaces = 3
	_, err = registry.CreateCheckout(context.Background(), request)
	fmt.Println("precision mismatch rejected:", errors.Is(err, integrations.ErrUnsupportedCurrency))
	request.CurrencyDecimalPlaces, request.AmountMinor = 2, 0
	_, err = registry.CreateCheckout(context.Background(), request)
	fmt.Println("zero amount rejected:", errors.Is(err, integrations.ErrInvalidResult))
	fmt.Println("provider checkout calls:", provider.checkoutCalls)
	_, err = registry.VerifyCallback(context.Background(), integrations.CallbackRequest{Body: []byte("unverified fixture")})
	fmt.Println("callback refused:", errors.Is(err, integrations.ErrUnavailable))
	// Output:
	// checkout refused: true
	// precision mismatch rejected: true
	// zero amount rejected: true
	// provider checkout calls: 1
	// callback refused: true
}

func ExampleValidateSettlement() {
	// Synthetic fixture, not a verified callback. In a settlement service, want
	// must instead be loaded independently from core-owned records/configuration.
	want := integrations.SettlementExpectation{
		ProviderID: "payment.contract-example", MerchantAccountID: "fixture-merchant",
		AccountID: "fixture-account", OrderID: "fixture-order", InvoiceID: "fixture-invoice",
		AmountMinor: 3900, Currency: "CNY", CurrencyDecimalPlaces: 2,
	}
	fixture := integrations.VerifiedPayment{
		ProviderID: want.ProviderID, MerchantAccountID: want.MerchantAccountID,
		AccountID: want.AccountID, OrderID: want.OrderID, InvoiceID: want.InvoiceID,
		AmountMinor: want.AmountMinor, Currency: want.Currency, CurrencyDecimalPlaces: want.CurrencyDecimalPlaces,
		EventID: "fixture-event", TransactionID: "fixture-transaction", Status: "settled",
		SettledAt: time.Date(2026, time.October, 15, 0, 0, 0, 0, time.UTC),
	}
	fmt.Println("matching fields:", integrations.ValidateSettlement(fixture, want) == nil)
	fixture.AmountMinor++
	fmt.Println("changed amount rejected:", errors.Is(integrations.ValidateSettlement(fixture, want), integrations.ErrInvalidResult))
	fixture.AmountMinor, fixture.CurrencyDecimalPlaces = want.AmountMinor, 3
	fmt.Println("changed precision rejected:", errors.Is(integrations.ValidateSettlement(fixture, want), integrations.ErrInvalidResult))
	// This pure predicate does not verify signatures, deduplicate or write a payment.

	// Output:
	// matching fields: true
	// changed amount rejected: true
	// changed precision rejected: true
}
