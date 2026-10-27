package billing

import (
	"errors"
	"testing"
)

func TestMySQLCurrencySnapshotsAndRollback(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	u := User{ID: ID(), Email: ID() + "@test.invalid", Name: "Fixture", Role: "customer", PasswordHash: "unused"}
	admin := User{ID: ID(), Email: ID() + "@test.invalid", Name: "Fixture admin", Role: "admin", PasswordHash: "unused"}
	if err := m.AddUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := m.AddUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	configureTestCurrency(t, m, admin, "USD", 2, "6.00")
	migrationExec(t, ctx, m.DB, "UPDATE plans SET available=TRUE WHERE id='starter'")
	migrationExec(t, ctx, m.DB, "UPDATE plan_prices SET available=TRUE WHERE plan_id='starter'")
	first, _, err := m.CreateOrder(ctx, u, "starter", "cny-test", "default-order", "")
	if err != nil || first.Currency != "CNY" || first.TotalMinor != 3900 || first.Issuer == nil {
		t.Fatal(first, err)
	}
	usd, _, err := m.CreateOrder(ctx, u, "starter", "usd-test", "explicit-order", "USD")
	if err != nil || usd.Currency != "USD" || usd.TotalMinor != 600 {
		t.Fatal(usd, err)
	}
	if _, _, err = m.CreateOrder(ctx, u, "starter", "usd-test", "explicit-order", "CNY"); err != ErrConflict {
		t.Fatal("cross-currency replay", err)
	}
	if _, _, err = m.CreateOrder(ctx, u, "starter", "test", "invalid-order", "EUR"); err != ErrCurrency {
		t.Fatal(err)
	}
	settings, _ := m.Settings(ctx)
	settings.DefaultCurrency = "USD"
	settings.InvoiceIssuer = "新开具方"
	settings, err = m.UpdateSettings(ctx, admin, settings)
	if err != nil {
		t.Fatal(err)
	}
	migrationExec(t, ctx, m.DB, "UPDATE plan_prices SET amount_minor=4100 WHERE plan_id='starter' AND currency='CNY'")
	replayed, replay, err := m.CreateOrder(ctx, u, "starter", "cny-test", "default-order", "")
	if err != nil || !replay || replayed.TotalMinor != 3900 || replayed.Currency != "CNY" || replayed.Issuer.Name != first.Issuer.Name {
		t.Fatal("changed historical order", replayed, replay, err)
	}
	orders, err := m.Orders(ctx, u.ID)
	if err != nil || len(orders) != 2 {
		t.Fatal(orders, err)
	}
	invoices, err := m.Invoices(ctx, u.ID)
	if err != nil || len(invoices) != 2 {
		t.Fatal(invoices, err)
	}
	for _, invoice := range invoices {
		if invoice.OrderID == first.ID && (invoice.Currency != "CNY" || invoice.AmountMinor != 3900 || invoice.Issuer.Name != first.Issuer.Name) {
			t.Fatal("changed historical invoice", invoice)
		}
	}
	fresh, _, err := m.CreateOrder(ctx, u, "starter", "new-default", "new-default", "")
	if err != nil || fresh.Currency != "USD" || fresh.Issuer.Name != "新开具方" {
		t.Fatal(fresh, err)
	}
	migrationExec(t, ctx, m.DB, "DELETE FROM plan_prices WHERE plan_id='starter' AND currency='CNY'")
	plans, err := m.Plans(ctx, "CNY")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range plans {
		if p.ID == "starter" {
			found = true
			if p.Available || p.PriceMinor != 0 || p.Currency != "CNY" {
				t.Fatal("price fallback", p)
			}
		}
	}
	if !found {
		t.Fatal("missing plan projection")
	}
	if _, _, err = m.CreateOrder(ctx, u, "starter", "missing-price", "missing-price", "CNY"); err != ErrUnavailable {
		t.Fatal(err)
	}
	var count int
	if err = m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM order_requests WHERE user_id=? AND request_key='missing-price'", u.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("request reservation leaked", count, err)
	}
	// A late failure after order, invoice and audit insertion must roll back all writes.
	migrationExec(t, ctx, m.DB, "CREATE TRIGGER test_reject_outbox BEFORE INSERT ON outbox_events FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test failure'")
	if _, _, err = m.CreateOrder(ctx, u, "starter", "rollback", "rollback-key", "USD"); err == nil {
		t.Fatal("expected injected transaction failure")
	}
	migrationExec(t, ctx, m.DB, "DROP TRIGGER test_reject_outbox")
	for _, query := range []string{"SELECT COUNT(*) FROM orders WHERE hostname='rollback'", "SELECT COUNT(*) FROM order_requests WHERE request_key='rollback-key'", "SELECT COUNT(*) FROM invoices WHERE order_id NOT IN (SELECT id FROM orders)", "SELECT COUNT(*) FROM audit_events WHERE action='order.created' AND target NOT IN (SELECT id FROM orders)"} {
		if err = m.DB.QueryRowContext(ctx, query).Scan(&count); err != nil || count != 0 {
			t.Fatal("rollback leakage", count, err)
		}
	}
	// Settings and their audit event share a transaction, even on a late audit failure.
	migrationExec(t, ctx, m.DB, "CREATE TRIGGER test_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test failure'")
	changed := settings
	changed.SiteName = "Should roll back"
	if _, err = m.UpdateSettings(ctx, admin, changed); err == nil {
		t.Fatal("expected settings transaction failure")
	}
	migrationExec(t, ctx, m.DB, "DROP TRIGGER test_reject_audit")
	stored, err := m.Settings(ctx)
	if err != nil || stored.SiteName != settings.SiteName || stored.Revision != settings.Revision {
		t.Fatal("settings rollback", stored, err)
	}
	// Synthetic paid fixtures are test-only, no payment provider or settlement is invoked.
	migrationExec(t, ctx, m.DB, "UPDATE invoices SET status='paid',paid_at=UTC_TIMESTAMP(6) WHERE order_id IN (?,?)", first.ID, usd.ID)
	for _, currency := range []string{"CNY", "USD"} {
		amount := int64(3900)
		if currency == "USD" {
			amount = 600
		}
		migrationExec(t, ctx, m.DB, "INSERT INTO services(id,user_id,name,plan_name,region,status,next_due_at,price_minor,currency,cpu,memory_gb,disk_gb) VALUES(?,?,'fixture','test','test','active',UTC_TIMESTAMP(6),?,?,1,1,1)", ID(), u.ID, amount, currency)
		s, err := m.Summary(ctx, u.ID, currency)
		pending := 0
		if currency == "USD" {
			pending = 1
		}
		if err != nil || s.ActiveServices != 1 || s.TotalServices != 1 || s.MonthlyTotal != amount || s.Revenue != amount || s.PendingInvoices != pending || s.UnpaidTotal != int64(pending)*600 {
			t.Fatal("mixed-currency aggregate", currency, s, err)
		}
	}
	if _, err = m.Summary(ctx, u.ID, "EUR"); !errors.Is(err, ErrCurrency) {
		t.Fatal(err)
	}
	for _, table := range []string{"services", "invoices", "orders"} {
		for _, currency := range []string{"usd", "cny", "úsd", "cňy"} {
			if _, err = m.DB.ExecContext(ctx, "UPDATE "+table+" SET currency=? WHERE user_id=?", currency, u.ID); err == nil {
				t.Fatal("unsupported persisted currency", table, currency)
			}
		}
	}
	if _, err = m.DB.ExecContext(ctx, "UPDATE plan_prices SET amount_minor=? WHERE plan_id='starter'", MaxMinor+1); err == nil {
		t.Fatal("unsafe persisted price")
	}
	migrationExec(t, ctx, m.DB, "UPDATE services SET price_minor=? WHERE user_id=? AND currency='USD'", MaxMinor, u.ID)
	migrationExec(t, ctx, m.DB, "INSERT INTO services(id,user_id,name,plan_name,region,status,next_due_at,price_minor,currency,cpu,memory_gb,disk_gb) VALUES(?,?,'overflow','test','test','active',UTC_TIMESTAMP(6),1,'USD',1,1,1)", ID(), u.ID)
	if _, err = m.Summary(ctx, u.ID, "USD"); err == nil {
		t.Fatal("unsafe aggregate accepted")
	}
}
