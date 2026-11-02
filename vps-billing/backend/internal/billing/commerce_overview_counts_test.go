package billing

import (
	"testing"
	"time"
)

func TestMySQLCommerceOverviewCountsKeepEmptySetsDatesAndCurrencies(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	empty, err := m.AdminOverview06(ctx, "CNY", at)
	if err != nil || empty.Counts.Clients != 0 || empty.Counts.NewClientsMonth != 0 || empty.Counts.OverdueInvoices != 0 || empty.Outstanding.AmountMinor != 0 {
		t.Fatal("empty aggregates", empty, err)
	}
	f := &httpPerfFixture{m: m}
	const seed = "overview-count-boundaries"
	if err := httpPerfSeed(ctx, f, seed, httpPerfParameters{Users: 4, Orders: 8, Services: 8}); err != nil {
		t.Fatal(err)
	}
	month := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	migrationExec(t, ctx, m.DB, "UPDATE users SET created_at=?", month.Add(-time.Second))
	migrationExec(t, ctx, m.DB, "UPDATE users SET created_at=? WHERE id=?", month, f.customer)
	migrationExec(t, ctx, m.DB, "UPDATE users SET created_at=? WHERE id IN (?,?)", at, httpPerfID(seed, "user-1"), f.adminID)
	migrationExec(t, ctx, m.DB, "UPDATE users SET created_at=? WHERE id=?", at.Add(time.Second), httpPerfID(seed, "user-2"))
	migrationExec(t, ctx, m.DB, "UPDATE invoices SET amount_minor=1000,amount_paid_minor=100,credit_applied_minor=200,due_at=?", at.Add(-time.Second))
	migrationExec(t, ctx, m.DB, "UPDATE invoices SET due_at=? WHERE id=?", at, httpPerfID(seed, "invoice-1"))
	migrationExec(t, ctx, m.DB, "UPDATE invoices SET due_at=? WHERE id=?", at.Add(time.Second), httpPerfID(seed, "invoice-2"))
	migrationExec(t, ctx, m.DB, "UPDATE invoices SET status='cancelled' WHERE id=?", httpPerfID(seed, "invoice-3"))
	migrationExec(t, ctx, m.DB, "UPDATE invoices SET status='paid',amount_paid_minor=800 WHERE id=?", httpPerfID(seed, "invoice-4"))
	migrationExec(t, ctx, m.DB, "UPDATE invoices SET currency='USD' WHERE id=?", httpPerfID(seed, "invoice-5"))
	migrationExec(t, ctx, m.DB, "UPDATE orders SET currency='USD' WHERE id=?", httpPerfID(seed, "order-7"))
	migrationExec(t, ctx, m.DB, "UPDATE services SET status='suspended' WHERE id=?", httpPerfID(seed, "service-0"))
	migrationExec(t, ctx, m.DB, "UPDATE services SET currency='USD' WHERE id=?", httpPerfID(seed, "service-1"))
	out, err := m.AdminOverview06(ctx, "CNY", at)
	if err != nil {
		t.Fatal(err)
	}
	c := out.Counts
	if c.Clients != 4 || c.NewClientsMonth != 2 || c.ActiveServices != 6 || c.PendingOrders != 7 || c.OpenTickets != 0 || c.OverdueInvoices != 3 || out.Outstanding.AmountMinor != 3500 {
		t.Fatal("date, currency, status or balance boundary changed", c, out.Outstanding)
	}
	migrationExec(t, ctx, m.DB, "UPDATE invoices SET status='paid',amount_paid_minor=800 WHERE currency='CNY'")
	out, err = m.AdminOverview06(ctx, "CNY", at)
	if err != nil || out.Counts.OverdueInvoices != 0 || out.Outstanding.AmountMinor != 0 {
		t.Fatal("no unpaid invoices", out, err)
	}
}
