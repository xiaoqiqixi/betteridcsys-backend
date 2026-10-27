package billing

import (
	"aster/billing/internal/integrations"
	"errors"
	"reflect"
	"testing"
)

func TestPaymentMySQLContract(t *testing.T) {
	f := newCancellationSQLFixture(t)
	paymentStoreContract(t, paymentFixture{store: f.m, owner: f.owner, other: f.other, ctx: f.ctx})
}

func TestPaymentMySQLDurabilityAndRollback(t *testing.T) {
	f := newCancellationSQLFixture(t)
	p := paymentFixture{store: f.m, owner: f.owner, other: f.other, ctx: f.ctx}
	t.Run("uncertain attempt survives store reconstruction", func(t *testing.T) {
		o, i := p.order(t)
		a := p.reserve(t, i)
		if _, e := f.m.CompletePaymentCheckout(f.ctx, a.ID, integrations.Checkout{}, true); !errors.Is(e, ErrPaymentUncertain) {
			t.Fatal(e)
		}
		reloaded := &MySQL{DB: f.m.DB}
		stored, e := reloaded.PaymentAttempt(f.ctx, f.owner, a.ID)
		if e != nil || stored.State != "uncertain" || stored.Expectation != a.Expectation {
			t.Fatal(stored, e)
		}
		if _, e = reloaded.CancelOrder(f.ctx, f.owner, o.ID, "restart must not release reservation"); !errors.Is(e, ErrCancellationState) {
			t.Fatal(e)
		}
		v := syntheticPayment(a)
		if _, e = reloaded.SettlePayment(f.ctx, a.ID, v); e != nil {
			t.Fatal(e)
		}
		reloaded = &MySQL{DB: f.m.DB}
		result, e := reloaded.SettlePayment(f.ctx, a.ID, v)
		if e != nil || !result.Replay {
			t.Fatal("receipt replay lost after reload", result, e)
		}
		if n := cancellationSQLCount(t, f, "SELECT COUNT(*) FROM payment_settlements WHERE attempt_id=?", a.ID); n != 1 {
			t.Fatal("duplicate receipt", n)
		}
		if n := cancellationSQLCount(t, f, "SELECT COUNT(*) FROM outbox_events WHERE aggregate_id=? AND event_type='payment.settled'", o.ID); n != 1 {
			t.Fatal("duplicate event", n)
		}
	})
	t.Run("late outbox failure rolls back receipt statuses and audit", func(t *testing.T) {
		o, i := p.order(t)
		a := p.reserve(t, i)
		v := syntheticPayment(a)
		migrationExec(t, f.ctx, f.m.DB, "INSERT INTO outbox_events(id,aggregate_id,event_type,payload,created_at) VALUES(?,?,?,?,?)", ID(), o.ID, "payment.settled", []byte(`{"synthetic_conflict":true}`), a.CreatedAt)
		before := cancellationSQLRecords(t, f, o.ID)
		if _, e := f.m.SettlePayment(f.ctx, a.ID, v); !errors.Is(e, ErrPaymentConflict) {
			t.Fatal("expected transaction failure", e)
		}
		assertCancellationSQLState(t, f, o.ID, "pending_payment", "unpaid")
		if n := cancellationSQLCount(t, f, "SELECT COUNT(*) FROM payment_settlements WHERE attempt_id=?", a.ID); n != 0 {
			t.Fatal("rollback retained receipt", n)
		}
		if n := cancellationSQLCount(t, f, "SELECT COUNT(*) FROM audit_events WHERE target=? AND action='payment.settled'", o.ID); n != 0 {
			t.Fatal("rollback retained audit", n)
		}
		stored, e := f.m.PaymentAttempt(f.ctx, f.owner, a.ID)
		if e != nil || stored.State != "pending" {
			t.Fatal("rollback retained settled attempt", stored, e)
		}
		after := cancellationSQLRecords(t, f, o.ID)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("failed settlement changed existing history")
		}
	})
}

func TestPaymentMySQLHistoricalInvoiceWithoutSnapshots(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	migrationHistoricalPrefix(t, ctx, m, 1)
	owner := User{ID: ID(), Email: ID() + "@test.invalid", Name: "Legacy payment owner", Role: "customer", PasswordHash: "unused"}
	// Seed the actual v1 shape before upgrading. Current AddUser intentionally
	// requires the current account columns; it must not accept old schemas.
	migrationExec(t, ctx, m.DB, "INSERT INTO users(id,email,name,role,password_hash) VALUES(?,?,?,?,?)", owner.ID, owner.Email, owner.Name, owner.Role, owner.PasswordHash)
	orderID, invoiceID := ID(), ID()
	migrationExec(t, ctx, m.DB, "INSERT INTO orders(id,number,user_id,plan_id,plan_name,hostname,status,total_minor,currency,created_at) VALUES(?,?,?,'starter','Historical plan','legacy-pay-host','pending_payment',1234,'USD','2025-01-02 03:04:05')", orderID, "ORD-"+orderID, owner.ID)
	migrationExec(t, ctx, m.DB, "INSERT INTO invoices(id,number,user_id,order_id,status,amount_minor,currency,issued_at,due_at) VALUES(?,?,?,?,'unpaid',1234,'USD','2025-01-02 03:04:05','2025-01-09 03:04:05')", invoiceID, "INV-"+invoiceID, owner.ID, orderID)
	if e := m.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	before, e := m.Invoice(ctx, owner.ID, invoiceID)
	if e != nil || before.Issuer != nil {
		t.Fatal(before, e)
	}
	a, _, e := m.ReservePayment(ctx, owner, invoiceID, "invoice:"+invoiceID, "payment.synthetic", "legacy-merchant")
	if e != nil {
		t.Fatal(e)
	}
	if a.Expectation.AmountMinor != 1234 || a.Expectation.Currency != "USD" || a.Expectation.CurrencyDecimalPlaces != 2 {
		t.Fatal("legacy invoice converted", a)
	}
	result, e := m.SettlePayment(ctx, a.ID, syntheticPayment(a))
	if e != nil || result.Order.Status != "paid" || result.Invoice.Status != "paid" || result.Invoice.Issuer != nil {
		t.Fatal(result, e)
	}
	var missingOrderSnapshot, missingInvoiceSnapshot bool
	if e = m.DB.QueryRowContext(ctx, "SELECT o.plan_snapshot IS NULL,i.issuer_snapshot IS NULL FROM orders o JOIN invoices i ON i.order_id=o.id WHERE o.id=?", orderID).Scan(&missingOrderSnapshot, &missingInvoiceSnapshot); e != nil || !missingOrderSnapshot || !missingInvoiceSnapshot {
		t.Fatal("payment invented legacy snapshots", missingOrderSnapshot, missingInvoiceSnapshot, e)
	}
}
