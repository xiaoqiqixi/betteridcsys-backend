package paymentfixture

import (
	"aster/billing/pkg/pluginapi/paymentv1"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

var fixtureTime = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func setup(t *testing.T) (*Processor, []byte, paymentv1.Config) {
	t.Helper()
	key := bytes.Repeat([]byte{0x56}, 32)
	processor, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	processor.now = func() time.Time { return fixtureTime }
	return processor, key, paymentv1.Config{MerchantID: "synthetic-merchant", ProviderID: ProviderID, ManifestSHA256: strings.Repeat("a", 64)}
}

func intent() paymentv1.Intent {
	return paymentv1.Intent{AttemptID: "attempt-1", IdempotencyKey: "attempt-1", InvoiceID: "invoice-1", OrderID: "order-1", AccountID: "account-1",
		AmountMinor: 12345, Currency: "CNY", CurrencyDecimalPlaces: 2, CallbackURL: "https://billing.example.test/callback", ReturnURL: "https://billing.example.test/invoices/invoice-1"}
}

func event() paymentv1.VerifiedEvent {
	return paymentv1.VerifiedEvent{AttemptID: "attempt-1", ProviderID: ProviderID, EventID: "event-1", TransactionID: "transaction-1", MerchantAccountID: "synthetic-merchant",
		AccountID: "account-1", OrderID: "order-1", InvoiceID: "invoice-1", AmountMinor: 12345, Currency: "CNY", CurrencyDecimalPlaces: 2, SettledAt: fixtureTime, Status: "settled"}
}

func callback(t *testing.T, key []byte, value paymentv1.VerifiedEvent) paymentv1.Callback {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return signed(t, key, body)
}

func signed(t *testing.T, key, body []byte) paymentv1.Callback {
	t.Helper()
	signature, err := Signature(key, body)
	if err != nil {
		t.Fatal(err)
	}
	return paymentv1.Callback{Method: "POST", Path: "/api/payments/callback/payment.fixture", Body: body, Headers: []paymentv1.Header{{Name: SignatureHeader, Values: []string{signature}}}}
}

func TestSyntheticCheckoutIsDeterministicAndCurrencyBound(t *testing.T) {
	processor, _, cfg := setup(t)
	first, err := processor.Create(context.Background(), cfg, intent())
	if err != nil {
		t.Fatal(err)
	}
	second, err := processor.Create(context.Background(), cfg, intent())
	if err != nil || first != second {
		t.Fatalf("same intent changed checkout: %#v %v", second, err)
	}
	u, err := url.Parse(first.URL)
	if err != nil || u.Scheme != "https" || u.Host != CheckoutHost || u.Query().Get("session") != first.SessionID || len(u.Query()) != 1 {
		t.Fatalf("checkout escaped synthetic host: %q", first.URL)
	}
	if first.ProviderID != ProviderID || first.ExpiresAt != fixtureTime.Add(15*time.Minute) || strings.Contains(first.URL, cfg.MerchantID) || strings.Contains(first.URL, intent().InvoiceID) {
		t.Fatalf("invalid synthetic checkout: %#v", first)
	}
	for _, currency := range Declarations().Currencies {
		in := intent()
		in.Currency, in.CurrencyDecimalPlaces = currency.Code, currency.Precision
		if _, err := processor.Create(context.Background(), cfg, in); err != nil {
			t.Errorf("declared currency %s rejected: %v", currency.Code, err)
		}
	}
	mutations := map[string]func(*paymentv1.Intent){
		"wrong precision":           func(in *paymentv1.Intent) { in.CurrencyDecimalPlaces = 0 },
		"unsupported currency":      func(in *paymentv1.Intent) { in.Currency = "EUR" },
		"zero amount":               func(in *paymentv1.Intent) { in.AmountMinor = 0 },
		"overflow amount":           func(in *paymentv1.Intent) { in.AmountMinor = paymentv1.MaxMinor + 1 },
		"different idempotency key": func(in *paymentv1.Intent) { in.IdempotencyKey = "another-attempt" },
		"missing account":           func(in *paymentv1.Intent) { in.AccountID = "" },
		"unsafe callback URL":       func(in *paymentv1.Intent) { in.CallbackURL = "http://billing.example.test/callback" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			in := intent()
			mutate(&in)
			out, err := processor.Create(context.Background(), cfg, in)
			if !errors.Is(err, paymentv1.ErrInvalid) || out != (paymentv1.Checkout{}) {
				t.Fatalf("invalid intent accepted: %#v %v", out, err)
			}
		})
	}
}

func TestSyntheticCallbackSignatureAndCoreReplayBoundary(t *testing.T) {
	processor, key, cfg := setup(t)
	in := callback(t, key, event())
	for delivery := 0; delivery < 2; delivery++ {
		out, err := processor.Verify(context.Background(), cfg, in)
		if err != nil || out != event() {
			t.Fatalf("valid delivery %d failed: %#v %v", delivery, out, err)
		}
	}
	// The worker preserves evidence on valid redelivery. Core must deduplicate
	// both event and channel transaction IDs in its own database transaction.
	in.Headers[0].Name = strings.ToLower(SignatureHeader)
	if _, err := processor.Verify(context.Background(), cfg, in); err != nil {
		t.Fatalf("HTTP header case changed signature semantics: %v", err)
	}
	// Constructor copies the key instead of retaining its caller's buffer.
	clear(key)
	if _, err := processor.Verify(context.Background(), cfg, in); err != nil {
		t.Fatalf("caller changed private fixture key: %v", err)
	}
}

func TestSyntheticCallbackRejectsUnverifiedEvidence(t *testing.T) {
	processor, key, cfg := setup(t)
	mutations := map[string]func(*paymentv1.VerifiedEvent){
		"other provider":      func(v *paymentv1.VerifiedEvent) { v.ProviderID = "payment.other" },
		"other merchant":      func(v *paymentv1.VerifiedEvent) { v.MerchantAccountID = "another-merchant" },
		"missing attempt":     func(v *paymentv1.VerifiedEvent) { v.AttemptID = "" },
		"missing invoice":     func(v *paymentv1.VerifiedEvent) { v.InvoiceID = "" },
		"missing transaction": func(v *paymentv1.VerifiedEvent) { v.TransactionID = "" },
		"unsettled status":    func(v *paymentv1.VerifiedEvent) { v.Status = "pending" },
		"negative amount":     func(v *paymentv1.VerifiedEvent) { v.AmountMinor = -1 },
		"unknown currency":    func(v *paymentv1.VerifiedEvent) { v.Currency = "EUR" },
		"wrong precision":     func(v *paymentv1.VerifiedEvent) { v.CurrencyDecimalPlaces = 0 },
		"stale event":         func(v *paymentv1.VerifiedEvent) { v.SettledAt = fixtureTime.Add(-5*time.Minute - time.Nanosecond) },
		"future event":        func(v *paymentv1.VerifiedEvent) { v.SettledAt = fixtureTime.Add(30*time.Second + time.Nanosecond) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			v := event()
			mutate(&v)
			out, err := processor.Verify(context.Background(), cfg, callback(t, key, v))
			if !errors.Is(err, paymentv1.ErrRejected) || out != (paymentv1.VerifiedEvent{}) {
				t.Fatalf("unusable event accepted: %#v %v", out, err)
			}
		})
	}
	callbackMutations := map[string]func(*paymentv1.Callback){
		"tampered body":       func(v *paymentv1.Callback) { v.Body = append(v.Body, ' ') },
		"missing signature":   func(v *paymentv1.Callback) { v.Headers = nil },
		"duplicate signature": func(v *paymentv1.Callback) { v.Headers = append(v.Headers, v.Headers[0]) },
		"multiple values":     func(v *paymentv1.Callback) { v.Headers[0].Values = append(v.Headers[0].Values, v.Headers[0].Values[0]) },
		"wrong signature":     func(v *paymentv1.Callback) { v.Headers[0].Values[0] = strings.Repeat("0", 64) },
		"wrong method":        func(v *paymentv1.Callback) { v.Method = "GET" },
		"query payload":       func(v *paymentv1.Callback) { v.RawQuery = "paid=true" },
	}
	for name, mutate := range callbackMutations {
		t.Run(name, func(t *testing.T) {
			in := callback(t, key, event())
			mutate(&in)
			out, err := processor.Verify(context.Background(), cfg, in)
			if !errors.Is(err, paymentv1.ErrRejected) || out != (paymentv1.VerifiedEvent{}) {
				t.Fatalf("unverified callback accepted: %#v %v", out, err)
			}
		})
	}
	valid, err := json.Marshal(event())
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"unknown field":      append(append([]byte(nil), valid[:len(valid)-1]...), []byte(`,"admin":true}`)...),
		"duplicate field":    append(append([]byte(nil), valid[:len(valid)-1]...), []byte(`,"amount_minor":1}`)...),
		"case aliased field": append(append([]byte(nil), valid[:len(valid)-1]...), []byte(`,"Amount_Minor":1}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := processor.Verify(context.Background(), cfg, signed(t, key, body)); !errors.Is(err, paymentv1.ErrRejected) {
				t.Fatalf("signed ambiguous JSON accepted: %v", err)
			}
		})
	}
}

func TestSyntheticFixtureKeyAndCancellation(t *testing.T) {
	for _, size := range []int{0, 31, 4097} {
		if _, err := New(bytes.Repeat([]byte{'x'}, size)); !errors.Is(err, paymentv1.ErrInvalid) {
			t.Errorf("accepted %d-byte key: %v", size, err)
		}
	}
	processor, key, cfg := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := processor.Create(ctx, cfg, intent()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled checkout: %v", err)
	}
	if _, err := processor.Verify(ctx, cfg, callback(t, key, event())); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled verification: %v", err)
	}
	for _, cfg := range []paymentv1.Config{{}, {MerchantID: "synthetic-merchant", ProviderID: "payment.other", ManifestSHA256: strings.Repeat("a", 64)}} {
		if _, err := processor.Create(context.Background(), cfg, intent()); !errors.Is(err, paymentv1.ErrInvalid) {
			t.Fatalf("invalid worker identity accepted: %v", err)
		}
	}
}
