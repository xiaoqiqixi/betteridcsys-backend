package pluginapi

import (
	"aster/billing/pkg/pluginapi/paymentv1"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStrictWireFormat(t *testing.T) {
	valid := `{"protocol":"aster.smtp/1","core_version":"0.5.0","nonce":"` + strings.Repeat("a", 64) + `"}`
	var hello Hello
	if err := DecodeStrict([]byte(valid), MaxRequestBytes, &hello); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"duplicate":         `{"protocol":"a","protocol":"b"}`,
		"escaped duplicate": `{"protocol":"a","\u0070rotocol":"b"}`,
		"case alias":        `{"Protocol":"a"}`, "unknown": `{"secret":"x"}`,
		"null": `{"nonce":null}`, "trailing": valid + ` {}`, "invalid utf8": string([]byte{0xff}),
		"deep":       strings.Repeat("[", 10) + "1" + strings.Repeat("]", 10),
		"long array": "[" + strings.Repeat("1,", 64) + "1]", "wrong type": `{"protocol":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			var got Hello
			if DecodeStrict([]byte(raw), MaxRequestBytes, &got) == nil {
				t.Fatal("accepted malformed input")
			}
		})
	}
	if DecodeStrict([]byte(valid), len(valid)-1, &hello) == nil {
		t.Fatal("accepted oversized input")
	}
	raw, _ := json.Marshal(Policy{Schema: 1, Revision: 1})
	var p Policy
	if DecodeStrict(raw, MaxPolicyBytes, &p) != nil || p.SMTP != nil {
		t.Fatal("empty disabled policy must round trip")
	}
}

func TestPaymentResponseFieldsMustBeExplicit(t *testing.T) {
	create := PaymentCreateResponse{Protocol: PaymentProtocol, RequestID: strings.Repeat("a", 64), Status: "accepted", Checkout: &paymentv1.Checkout{ProviderID: "payment.fixture", SessionID: "fixture-1", URL: "https://checkout.example.test/1", ExpiresAt: time.Now().Add(time.Hour)}}
	verify := PaymentVerifyResponse{Protocol: PaymentProtocol, RequestID: strings.Repeat("a", 64), Status: "verified", Event: &paymentv1.VerifiedEvent{AttemptID: "attempt-1", ProviderID: "payment.fixture", EventID: "event-1", TransactionID: "tx-1", MerchantAccountID: "merchant-1", AccountID: "account-1", OrderID: "order-1", InvoiceID: "invoice-1", AmountMinor: 1, Currency: "JPY", CurrencyDecimalPlaces: 0, SettledAt: time.Now(), Status: "settled"}}
	for _, value := range []any{create, verify} {
		raw, _ := json.Marshal(value)
		decode := func(raw []byte) error {
			switch value.(type) {
			case PaymentCreateResponse:
				var out PaymentCreateResponse
				return DecodeStrict(raw, MaxResponseBytes, &out)
			default:
				var out PaymentVerifyResponse
				return DecodeStrict(raw, MaxResponseBytes, &out)
			}
		}
		if decode(raw) != nil {
			t.Fatal("complete payment response rejected")
		}
		var fields map[string]json.RawMessage
		json.Unmarshal(raw, &fields)
		for key := range fields {
			copyFields := map[string]json.RawMessage{}
			for k, v := range fields {
				if k != key {
					copyFields[k] = v
				}
			}
			incomplete, _ := json.Marshal(copyFields)
			// Payload absence is rejected by the runtime result validator; every
			// envelope scalar must be explicit even when its valid value is empty.
			if key != "checkout" && key != "event" && decode(incomplete) == nil {
				t.Fatal("accepted missing payment field", key)
			}
			if key == "checkout" || key == "event" {
				var nested map[string]json.RawMessage
				json.Unmarshal(fields[key], &nested)
				for nestedKey := range nested {
					copyNested := map[string]json.RawMessage{}
					for k, v := range nested {
						if k != nestedKey {
							copyNested[k] = v
						}
					}
					copyFields[key], _ = json.Marshal(copyNested)
					incomplete, _ = json.Marshal(copyFields)
					if decode(incomplete) == nil {
						t.Fatal("accepted missing payment payload field", nestedKey)
					}
				}
			}
		}
	}
}

func TestManifestCompatibility(t *testing.T) {
	manifest := Manifest{Schema: 1, ID: "email.smtp", Version: "1.1.0", Protocol: SMTPProtocol, CoreMin: "0.4.0", CoreMaxExclusive: "0.6.0", Capabilities: []string{SMTPCapability}, Executable: "plugin", SHA256: strings.Repeat("a", 64)}
	for _, core := range []string{"0.4.0", "0.4.999", "0.5.0", "0.5.999"} {
		if manifest.Validate(core) != nil {
			t.Fatal(core)
		}
	}
	for _, core := range []string{"0.3.99", "0.6.0", "0.4.0-rc.1", "v0.4.0", "00.4.0", "1000000.0.0"} {
		if manifest.Validate(core) == nil {
			t.Fatal(core)
		}
	}
	for _, mutate := range []func(*Manifest){func(m *Manifest) { m.Executable = "../plugin" }, func(m *Manifest) { m.ID = "../../x" }, func(m *Manifest) { m.Capabilities = append(m.Capabilities, "database") }, func(m *Manifest) { m.Protocol = "aster.smtp/2" }, func(m *Manifest) { m.SHA256 = strings.Repeat("A", 64) }} {
		m := manifest
		mutate(&m)
		if m.Validate("0.5.10") == nil {
			t.Fatal("accepted incompatible manifest")
		}
	}
	if got, err := CompareVersion("0.4.10", "0.4.2"); err != nil || got != 1 {
		t.Fatal(got, err)
	}
}

func TestQuerySettlementRequiresExplicitZeroPrecision(t *testing.T) {
	event := paymentv1.VerifiedEvent{AttemptID: "attempt", ProviderID: "payment.example", GatewayID: "slot", EventID: "event", TransactionID: "transaction", MerchantAccountID: "merchant", AccountID: "account", OrderID: "order", InvoiceID: "invoice", AmountMinor: 100, Currency: "JPY", CurrencyDecimalPlaces: 0, SettledAt: time.Now(), Status: "settled"}
	response := PaymentQueryResponse{Protocol: PaymentProtocol, RequestID: strings.Repeat("a", 64), Status: "accepted", Result: &paymentv1.QueryResult{Status: "succeeded", Event: &event}}
	raw, _ := json.Marshal(response)
	var out PaymentQueryResponse
	if DecodeStrict(raw, MaxResponseBytes, &out) != nil {
		t.Fatal("complete zero-precision evidence rejected")
	}
	var fields map[string]any
	json.Unmarshal(raw, &fields)
	evidence := fields["result"].(map[string]any)["event"].(map[string]any)
	for _, key := range []string{"amount_minor", "currency", "currency_decimal_places", "settled_at"} {
		original := evidence[key]
		delete(evidence, key)
		incomplete, _ := json.Marshal(fields)
		if DecodeStrict(incomplete, MaxResponseBytes, &out) == nil {
			t.Fatal("query invented omitted evidence", key)
		}
		evidence[key] = original
	}
}

func TestLegacySMTPRangeIsNotGuessedForCurrentCore(t *testing.T) {
	legacy := Manifest{Schema: 1, ID: "email.smtp", Version: "1.0.0", Protocol: SMTPProtocol, CoreMin: "0.4.0", CoreMaxExclusive: "0.5.0", Capabilities: []string{SMTPCapability}, Executable: "plugin", SHA256: strings.Repeat("a", 64)}
	if legacy.Validate("0.4.0") != nil || legacy.Validate(CoreVersion) == nil {
		t.Fatal("legacy SMTP compatibility was broadened without its manifest approval")
	}
}

func TestPaymentProtocolCannotClaimEarlierCoreSupport(t *testing.T) {
	manifest := Manifest{Schema: 1, ID: "payment.fixture", Version: "0.1.0", Protocol: PaymentProtocol, CoreMin: PaymentMinCoreVersion, CoreMaxExclusive: "0.6.0", Capabilities: PaymentCapabilities(), Executable: "plugin", SHA256: strings.Repeat("a", 64), Payment: &paymentv1.Declarations{Currencies: []paymentv1.Currency{{Code: "CNY", Precision: 2}}, CheckoutHosts: []string{"checkout.example.test"}}}
	if err := manifest.Validate(PaymentMinCoreVersion); err != nil {
		t.Fatal("earliest supported core rejected", err)
	}
	if manifest.Validate(CoreVersion) == nil {
		t.Fatal("old payment manifest upper bound broadened on Core 0.6")
	}
	for _, core := range []string{"0.4.999", "0.5.0", "0.5.2"} {
		if manifest.Validate(core) == nil {
			t.Fatal("payment accepted on older core", core)
		}
		lowered := manifest
		lowered.CoreMin = "0.4.0"
		if lowered.Validate(core) == nil {
			t.Fatal("dishonest manifest enabled nonexistent protocol", core)
		}
	}
	manifest.CoreMin = "0.4.0"
	if manifest.Validate(CoreVersion) == nil {
		t.Fatal("accepted unsupported historical payment compatibility claim")
	}
}

func TestResponseMustExplicitlyDeclareOutcome(t *testing.T) {
	response := SendResponse{Protocol: SMTPProtocol, RequestID: strings.Repeat("a", 64), Status: "accepted", Code: "", DeliveryUncertain: false}
	raw, _ := json.Marshal(response)
	var decoded SendResponse
	if DecodeStrict(raw, MaxResponseBytes, &decoded) != nil {
		t.Fatal("complete accepted response rejected")
	}
	var fields map[string]json.RawMessage
	json.Unmarshal(raw, &fields)
	for key := range fields {
		copyFields := map[string]json.RawMessage{}
		for k, v := range fields {
			if k != key {
				copyFields[k] = v
			}
		}
		incomplete, _ := json.Marshal(copyFields)
		if DecodeStrict(incomplete, MaxResponseBytes, &decoded) == nil {
			t.Fatal("accepted response missing", key)
		}
	}
}
