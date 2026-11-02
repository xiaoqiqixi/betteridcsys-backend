package affiliate

import (
	"aster/billing/internal/financeapi"
	"encoding/json"
	"testing"
	"time"
)

func policy() financeapi.AffiliateSettings {
	var s financeapi.AffiliateSettings
	_ = json.Unmarshal([]byte(`{"enabled":true,"default_rate_bp":1000,"scope":"all_payments","scope_months":2,"hold_days":30,"attribution_days":30,"min_withdraw":[],"max_per_referral":[],"allow_self_referral":false,"clawback":"reverse_all","product_overrides":[{"product_id":"special","rate_bp":2000}],"revision":1}`), &s)
	return s
}
func TestScopeAndActivationBoundaries(t *testing.T) {
	active := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, scope, kind string
		start             time.Time
		want              int64
	}{
		{"first order", "first_payment", "order", active, 100}, {"first excludes renewal", "first_payment", "renewal", active, 0}, {"first excludes upgrade", "first_payment", "upgrade", active, 0},
		{"all order", "all_payments", "order", active, 100}, {"all renewal", "all_payments", "renewal", active, 100}, {"all upgrade", "all_payments", "upgrade", active, 100}, {"top up excluded", "all_payments", "top_up", active, 0}, {"manual invoice excluded", "all_payments", "manual", active, 0},
		{"activation inclusive", "first_months", "order", active, 100}, {"last instant", "first_months", "renewal", active.AddDate(0, 2, 0).Add(-time.Nanosecond), 100}, {"boundary exclusive", "first_months", "upgrade", active.AddDate(0, 2, 0), 0}, {"before activation", "first_months", "renewal", active.Add(-time.Second), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := policy()
			s.Scope = tc.scope
			out, err := Calculate(s, nil, tc.kind, "USD", 1000, 0, []Line{{ID: "line", Amount: 1000, Start: tc.start, Activated: active}})
			if err != nil || len(out) != 1 || out[0].Commission != tc.want {
				t.Fatal(out, err)
			}
		})
	}
}
func TestRatePrecedenceCashRoundingCapsAndCurrency(t *testing.T) {
	for _, tc := range []struct {
		name, product, currency string
		override                *int64
		cash, used, cap, want   int64
		enabled                 bool
	}{
		{"default", "normal", "USD", nil, 1000, 0, -1, 100, true},
		{"product", "special", "USD", nil, 1000, 0, -1, 200, true},
		{"affiliate", "special", "USD", ptr(3000), 1000, 0, -1, 300, true},
		{"explicit zero", "special", "USD", ptr(0), 1000, 0, -1, 0, true},
		{"credit excluded", "normal", "USD", nil, 101, 0, -1, 10, true},
		{"credit only", "normal", "USD", nil, 0, 0, -1, 0, true},
		{"truncate cap", "normal", "USD", nil, 1000, 90, 150, 60, true},
		{"exhausted cap", "normal", "USD", nil, 1000, 150, 150, 0, true},
		{"zero cap", "normal", "USD", nil, 1000, 0, 0, 0, true},
		{"foreign cap cannot truncate", "normal", "CNY", nil, 1000, 0, 0, 100, true},
		{"disabled", "normal", "USD", nil, 1000, 0, -1, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := policy()
			s.Enabled = tc.enabled
			if tc.cap >= 0 {
				s.MaxPerReferral = []financeapi.AffiliateCurrencyAmount{{Currency: "USD", AmountMinor: tc.cap}}
			}
			out, err := Calculate(s, tc.override, "order", tc.currency, tc.cash, tc.used, []Line{{ID: "l", Product: tc.product, Amount: 1000}})
			var got int64
			for _, l := range out {
				got += l.Commission
			}
			if err != nil || got != tc.want {
				t.Fatal(out, err, tc.want)
			}
		})
	}
	t.Run("per line floor and exact cash allocation", func(t *testing.T) {
		s := policy()
		out, err := Calculate(s, nil, "order", "USD", 19, 0, []Line{{ID: "1", Amount: 10}, {ID: "2", Amount: 10}, {ID: "discount", Amount: -1}})
		if err != nil || len(out) != 2 || out[0].Cash != 9 || out[1].Cash != 10 || out[0].Commission != 0 || out[1].Commission != 1 {
			t.Fatal(out, err)
		}
	})
	t.Run("settings are not mutated", func(t *testing.T) {
		s := policy()
		before, _ := json.Marshal(s)
		_, _ = Calculate(s, nil, "order", "USD", 1000, 0, []Line{{ID: "l", Amount: 1000}})
		after, _ := json.Marshal(s)
		if string(before) != string(after) {
			t.Fatal("mutated policy")
		}
	})
}
func ptr(v int64) *int64 { return &v }
