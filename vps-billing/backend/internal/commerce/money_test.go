package commerce

import (
	"aster/billing/internal/contracttest"
	"testing"
	"time"
)

func TestMoneyCurrenciesAndDiscount(t *testing.T) {
	for _, tc := range []struct {
		currency         string
		decimals, amount int64
	}{{"JPY", 0, 999}, {"USD", 2, 999}, {"KWD", 3, 999}, {"USD", 2, MaxMinor}} {
		t.Run(tc.currency+string(rune('0'+tc.decimals)), func(t *testing.T) {
			contracttest.Data(t, "Money", map[string]any{"data": Money(tc.currency, tc.decimals, tc.amount)})
			got, err := Discount(tc.amount, "percent", 3333, 0)
			want, _ := FloorRatio(tc.amount, 3333, 10000)
			if err != nil || got != want {
				t.Fatal(got, err)
			}
		})
	}
	for _, tc := range []struct {
		name, kind              string
		amount, bp, fixed, want int64
		invalid                 bool
	}{
		{"floor per line", "percent", 1, 5000, 0, 0, false},
		{"full discount", "percent", 999, 10000, 0, 999, false},
		{"fixed cap", "fixed", 50, 0, 100, 50, false},
		{"zero line", "percent", 0, 10000, 0, 0, false},
		{"bad percent", "percent", 100, 10001, 0, 0, true},
		{"negative line", "fixed", -1, 0, 100, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Discount(tc.amount, tc.kind, tc.bp, tc.fixed)
			if (err != nil) != tc.invalid || got != tc.want {
				t.Fatal(got, err)
			}
		})
	}
	if _, err := Multiply(MaxMinor, 2); err == nil {
		t.Fatal("overflow accepted")
	}
	if _, err := Add(MaxMinor, 1); err == nil {
		t.Fatal("sum overflow accepted")
	}
}

func TestCalendarAlignment(t *testing.T) {
	for _, tc := range []struct {
		at             string
		months, anchor int
		want           string
	}{
		{"2025-01-31", 1, 31, "2025-02-28"}, {"2024-01-31", 1, 31, "2024-02-29"},
		{"2025-02-28", 1, 31, "2025-03-31"}, {"2024-02-29", 12, 29, "2025-02-28"},
		{"2025-03-31", -1, 31, "2025-02-28"}, {"2025-01-30", 1, 30, "2025-02-28"},
	} {
		t.Run(tc.at+tc.want, func(t *testing.T) {
			at, _ := time.Parse(time.DateOnly, tc.at)
			if got := CalendarMonths(at, tc.months, tc.anchor).Format(time.DateOnly); got != tc.want {
				t.Fatal(got)
			}
		})
	}
}

func TestProratingFloorAndPartialCredit(t *testing.T) {
	due := time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ old, next, want int64 }{{100, 200, 3}, {200, 100, -4}, {100, 100, 0}} {
		got, days, err := Prorate(tc.old, tc.next, "monthly", due, due.Add(-time.Hour))
		if err != nil || days != 1 || got != tc.want {
			t.Fatal(got, days, err)
		}
	}
	for _, tc := range []struct{ total, paid, applied, balance, used, left int64 }{
		{100, 30, 20, 100, 50, 50}, {100, 0, 0, 30, 30, 0}, {0, 0, 0, 50, 0, 50}, {100, 100, 0, 50, 0, 50},
	} {
		got, left, err := ApplyCredit(tc.total, tc.paid, tc.applied, tc.balance)
		if err != nil || got != tc.used || left != tc.left {
			t.Fatal(got, left, err)
		}
	}
	if _, err := BalanceDue(100, 80, 30); err == nil {
		t.Fatal("over-applied credit")
	}
	for _, tc := range []struct {
		paid, refunded, request int64
		valid                   bool
	}{{100, 0, 100, true}, {100, 30, 70, true}, {100, 30, 71, false}, {100, 0, 0, false}} {
		if err := Refundable(tc.paid, tc.refunded, tc.request); (err == nil) != tc.valid {
			t.Fatal(tc, err)
		}
	}
}
