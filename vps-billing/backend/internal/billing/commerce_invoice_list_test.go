package billing

import (
	api "aster/billing/internal/financeapi"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestMySQLCommerceInvoiceSummariesMatchDetailsAndCursorOwnership(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	f := &httpPerfFixture{m: m}
	const seed = "invoice-summary-equivalence"
	if err := httpPerfSeed(ctx, f, seed, httpPerfParameters{Users: 2, Orders: 64}); err != nil {
		t.Fatal(err)
	}
	// Tied timestamps exercise the ID tie-breaker and stable cursor order.
	migrationExec(t, ctx, m.DB, "UPDATE invoices SET issued_at='2025-01-01 00:00:00'")
	migrationExec(t, ctx, m.DB, "UPDATE invoices SET status='paid',paid_at='2025-01-02 00:00:00' WHERE id=?", httpPerfID(seed, "invoice-0"))
	migrationExec(t, ctx, m.DB, "UPDATE invoices SET status='void' WHERE id=?", httpPerfID(seed, "invoice-2"))
	migrationExec(t, ctx, m.DB, "UPDATE invoices SET commerce_version=1,kind='manual',amount_paid_minor=200,credit_applied_minor=100 WHERE id=?", httpPerfID(seed, "invoice-3"))
	format, _ := json.Marshal(CurrencyFormat{Code: "KWD", Name: "Historical dinar", Prefix: "", Suffix: " KWD", DecimalPlaces: 3})
	migrationExec(t, ctx, m.DB, "UPDATE invoices SET currency='KWD',currency_snapshot=? WHERE id=?", format, httpPerfID(seed, "invoice-4"))
	for _, owner := range []string{"", f.customer} {
		cursor, count := "", 0
		seen := map[string]bool{}
		for {
			page, next, err := m.InvoicePage06(ctx, owner, cursor, "", "", "", 7)
			if err != nil {
				t.Fatal(err)
			}
			for _, got := range page {
				if seen[got.ID] || owner != "" && got.ClientID != owner || cursor != "" && got.ID >= cursor {
					t.Fatal("cursor duplication, order or ownership mismatch", got.ID)
				}
				seen[got.ID] = true
				detail, err := m.Invoice06(ctx, owner, got.ID)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(detail)
				var want api.AdminInvoiceSummary
				if err := json.Unmarshal(raw, &want); err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("summary differs from detail: got %#v want %#v: %v", got, want, err)
				}
				var public api.InvoiceSummary
				if err := json.Unmarshal(raw, &public); err != nil || !reflect.DeepEqual(customerInvoiceSummary06(got), public) {
					t.Fatal("public summary projection changed", err)
				}
				count++
			}
			if next == nil {
				break
			}
			if len(page) != 7 || *next != page[len(page)-1].ID {
				t.Fatal("cursor does not identify the last returned invoice")
			}
			cursor = *next
		}
		want := 64
		if owner != "" {
			want = 32
		}
		if count != want {
			t.Fatal("incomplete pagination", count, want)
		}
	}
	if _, _, err := m.InvoicePage06(ctx, f.customer, httpPerfID(seed, "invoice-1"), "", "", "", 7); !errors.Is(err, ErrCommerceInput) {
		t.Fatal("accepted another customer's cursor", err)
	}
	page, next, err := m.InvoicePage06(ctx, "", "", "cancelled", "", "", 7)
	if err != nil || next != nil || len(page) != 1 || page[0].Status != "cancelled" {
		t.Fatal("legacy void filter changed", page, next, err)
	}
	if page, _, err = m.InvoicePage06(ctx, "", "", "", "", "missing_%", 7); err != nil || len(page) != 0 {
		t.Fatal("literal search or empty page changed", err)
	}
}
