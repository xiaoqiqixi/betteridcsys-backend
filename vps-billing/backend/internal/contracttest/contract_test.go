package contracttest

import "testing"

func TestMoneyValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		valid bool
	}{
		{"zero decimals", map[string]any{"currency": "JPY", "amount_minor": 12, "decimal_places": 0}, true},
		{"three decimals", map[string]any{"currency": "KWD", "amount_minor": 1234, "decimal_places": 3}, true},
		{"missing precision", map[string]any{"currency": "USD", "amount_minor": 1}, false},
		{"fractional money", map[string]any{"currency": "USD", "amount_minor": 1.5, "decimal_places": 2}, false},
		{"unsafe integer", map[string]any{"currency": "USD", "amount_minor": int64(9007199254740992), "decimal_places": 2}, false},
		{"extra field", map[string]any{"currency": "USD", "amount_minor": 1, "decimal_places": 2, "secret": "no"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validate("/components/schemas/Money", tc.value); (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
}

func TestContractResponseAndUnion(t *testing.T) {
	Response(t, "GET", "/products", map[string]any{"data": []any{}})
	Data(t, "Ok", map[string]any{"data": map[string]any{"ok": true}})
	if err := validate("/components/schemas/PayResult", map[string]any{"status": "redirect", "payment_id": "payment_1", "checkout_url": "https://payments.example/checkout", "expires_at": "2026-11-01T12:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := validate("/components/schemas/PayResult", map[string]any{"status": "redirect", "payment_id": "payment_1", "checkout_url": "not a uri", "expires_at": "yesterday"}); err == nil {
		t.Fatal("invalid union accepted")
	}
	if err := validate("/components/schemas/Absent", nil); err == nil {
		t.Fatal("unknown schema accepted")
	}
}
