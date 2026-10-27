package billing

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type cancellationSQLFixture struct {
	m                   *MySQL
	ctx                 context.Context
	owner, other, admin User
}

func newCancellationSQLFixture(t *testing.T) cancellationSQLFixture {
	t.Helper()
	m, ctx := migrationTestDatabase(t)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	f := cancellationSQLFixture{m: m, ctx: ctx}
	f.owner = User{ID: ID(), Email: ID() + "@test.invalid", Name: "Cancellation owner", Role: "customer", PasswordHash: "unused"}
	f.other = User{ID: ID(), Email: ID() + "@test.invalid", Name: "Other owner", Role: "customer", PasswordHash: "unused"}
	f.admin = User{ID: ID(), Email: ID() + "@test.invalid", Name: "Cancellation admin", Role: "admin", PasswordHash: "unused"}
	for _, user := range []User{f.owner, f.other, f.admin} {
		if err := m.AddUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	migrationExec(t, ctx, m.DB, "UPDATE plans SET available=TRUE WHERE id='starter'")
	migrationExec(t, ctx, m.DB, "UPDATE plan_prices SET available=TRUE WHERE plan_id='starter' AND currency='CNY'")
	return f
}

func (f cancellationSQLFixture) order(t *testing.T) (Order, Invoice, string) {
	t.Helper()
	key := "cancel-" + ID()
	order, _, err := f.m.CreateOrder(f.ctx, f.owner, "starter", "cancel-"+ID(), key, "CNY")
	if err != nil {
		t.Fatal(err)
	}
	// Unsafe-state cases deliberately retain malformed neighboring records.
	// Reload only this pair to normalize MySQL timestamp precision without
	// decoding unrelated history through the owner-wide list endpoints.
	order, err = scanOrder(f.m.DB.QueryRowContext(f.ctx, "SELECT "+orderColumns+" FROM orders WHERE id=? AND user_id=?", order.ID, f.owner.ID))
	if err != nil {
		t.Fatal(err)
	}
	var invoiceID string
	if err = f.m.DB.QueryRowContext(f.ctx, "SELECT id FROM invoices WHERE order_id=? AND user_id=?", order.ID, f.owner.ID).Scan(&invoiceID); err != nil {
		t.Fatal(err)
	}
	invoice, err := f.m.Invoice(f.ctx, f.owner.ID, invoiceID)
	if err != nil {
		t.Fatal(err)
	}
	return order, invoice, key
}

// Compare persisted history, including raw snapshots and the complete creation
// outbox row. Only the two lifecycle status columns may change.
func cancellationSQLHistory(t *testing.T, f cancellationSQLFixture, orderID string) []any {
	t.Helper()
	var orderNumber, ownerID, planID, planName, hostname, currency string
	var total int64
	var createdAt time.Time
	var issuer, format, plan []byte
	err := f.m.DB.QueryRowContext(f.ctx, "SELECT number,user_id,plan_id,plan_name,hostname,total_minor,currency,created_at,issuer_snapshot,currency_snapshot,plan_snapshot FROM orders WHERE id=?", orderID).Scan(&orderNumber, &ownerID, &planID, &planName, &hostname, &total, &currency, &createdAt, &issuer, &format, &plan)
	if err != nil {
		t.Fatal(err)
	}
	out := []any{orderNumber, ownerID, planID, planName, hostname, total, currency, createdAt, string(issuer), string(format), string(plan)}
	var invoiceID, invoiceNumber, invoiceOwner, invoiceCurrency string
	var amount int64
	var issuedAt, dueAt time.Time
	var paidAt sql.NullTime
	if err = f.m.DB.QueryRowContext(f.ctx, "SELECT id,number,user_id,amount_minor,currency,issued_at,due_at,paid_at,issuer_snapshot,currency_snapshot FROM invoices WHERE order_id=?", orderID).Scan(&invoiceID, &invoiceNumber, &invoiceOwner, &amount, &invoiceCurrency, &issuedAt, &dueAt, &paidAt, &issuer, &format); err != nil {
		t.Fatal(err)
	}
	out = append(out, invoiceID, invoiceNumber, invoiceOwner, amount, invoiceCurrency, issuedAt, dueAt, paidAt, string(issuer), string(format))
	var requestKey, fingerprint, requestOwner string
	if err = f.m.DB.QueryRowContext(f.ctx, "SELECT user_id,request_key,fingerprint,created_at FROM order_requests WHERE order_id=?", orderID).Scan(&requestOwner, &requestKey, &fingerprint, &createdAt); err != nil {
		t.Fatal(err)
	}
	out = append(out, requestOwner, requestKey, fingerprint, createdAt)
	var eventID, aggregateID, eventType string
	var payload []byte
	var processedAt sql.NullTime
	var attempts int
	if err = f.m.DB.QueryRowContext(f.ctx, "SELECT id,aggregate_id,event_type,payload,created_at,processed_at,attempts FROM outbox_events WHERE aggregate_id=? AND event_type='order.created'", orderID).Scan(&eventID, &aggregateID, &eventType, &payload, &createdAt, &processedAt, &attempts); err != nil {
		t.Fatal(err)
	}
	return append(out, eventID, aggregateID, eventType, string(payload), createdAt, processedAt, attempts)
}

func cancellationSQLCount(t *testing.T, f cancellationSQLFixture, query string, args ...any) int {
	t.Helper()
	var count int
	if err := f.m.DB.QueryRowContext(f.ctx, query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func cancellationSQLRecords(t *testing.T, f cancellationSQLFixture, orderID string) [][][]any {
	t.Helper()
	var records [][][]any
	for _, query := range []string{
		"SELECT * FROM orders WHERE id=?",
		"SELECT * FROM invoices WHERE order_id=?",
		"SELECT * FROM order_requests WHERE order_id=? ORDER BY request_key",
		"SELECT * FROM audit_events WHERE target=? ORDER BY id",
		"SELECT * FROM outbox_events WHERE aggregate_id=? ORDER BY id",
	} {
		rows, err := f.m.DB.QueryContext(f.ctx, query, orderID)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		var table [][]any
		for rows.Next() {
			values := make([]any, len(columns))
			args := make([]any, len(columns))
			for n := range values {
				args[n] = &values[n]
			}
			if err = rows.Scan(args...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			for n, value := range values {
				if raw, ok := value.([]byte); ok {
					values[n] = string(raw)
				}
			}
			table = append(table, values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, table)
	}
	return records
}

func assertCancellationSQLState(t *testing.T, f cancellationSQLFixture, orderID, orderStatus, invoiceStatus string) {
	t.Helper()
	var gotOrder, gotInvoice string
	if err := f.m.DB.QueryRowContext(f.ctx, "SELECT o.status,i.status FROM orders o JOIN invoices i ON i.order_id=o.id WHERE o.id=?", orderID).Scan(&gotOrder, &gotInvoice); err != nil {
		t.Fatal(err)
	}
	if gotOrder != orderStatus || gotInvoice != invoiceStatus {
		t.Fatalf("state pair = %s/%s, want %s/%s", gotOrder, gotInvoice, orderStatus, invoiceStatus)
	}
}

func assertNoCancellationSQLEffects(t *testing.T, f cancellationSQLFixture, orderID string) {
	t.Helper()
	for _, query := range []string{"SELECT COUNT(*) FROM audit_events WHERE target=? AND action='order.cancelled'", "SELECT COUNT(*) FROM outbox_events WHERE aggregate_id=? AND event_type='order.cancelled'"} {
		if count := cancellationSQLCount(t, f, query, orderID); count != 0 {
			t.Fatal("cancellation evidence leaked", count)
		}
	}
}

func TestMySQLCancellationPreservesHistoryAndReplays(t *testing.T) {
	f := newCancellationSQLFixture(t)
	order, invoice, key := f.order(t)
	before := cancellationSQLHistory(t, f, order.ID)
	// Later catalog and issuer changes must not affect historical cancellation.
	migrationExec(t, f.ctx, f.m.DB, "UPDATE plan_prices SET amount_minor=9900 WHERE plan_id='starter' AND currency='CNY'")
	settings, err := f.m.Settings(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.InvoiceIssuer = "Later issuer"
	if _, err = f.m.UpdateSettings(f.ctx, f.admin, settings); err != nil {
		t.Fatal(err)
	}
	// An unrelated service owned by the same customer is not associated.
	migrationExec(t, f.ctx, f.m.DB, "INSERT INTO services(id,user_id,name,plan_name,region,status,next_due_at,price_minor,currency,cpu,memory_gb,disk_gb) VALUES(?,?,'unrelated-service','fixture','fixture','active',UTC_TIMESTAMP(6),3900,'CNY',1,1,1)", ID(), f.owner.ID)
	out, err := f.m.CancelOrder(f.ctx, f.owner, order.ID, "  Customer changed plans  ")
	if err != nil || out.Order.Status != "cancelled" || out.Invoice.Status != "void" || out.Invoice.ID != invoice.ID {
		t.Fatal(out, err)
	}
	wantOrder, wantInvoice := order, invoice
	wantOrder.Status, wantInvoice.Status = "cancelled", "void"
	if !reflect.DeepEqual(out, OrderCancellation{Order: wantOrder, Invoice: wantInvoice}) {
		t.Fatal("cancellation changed historical values", out)
	}
	if after := cancellationSQLHistory(t, f, order.ID); !reflect.DeepEqual(before, after) {
		t.Fatal("persisted history changed", before, after)
	}
	replayed, err := f.m.CancelOrder(f.ctx, f.owner, order.ID, "A different retry reason")
	if err != nil || !reflect.DeepEqual(out, replayed) {
		t.Fatal("cancellation retry changed result", replayed, err)
	}
	recreated, replay, err := f.m.CreateOrder(f.ctx, f.owner, "starter", order.Hostname, key, "CNY")
	if err != nil || !replay || !reflect.DeepEqual(recreated, out.Order) {
		t.Fatal("original request did not replay cancelled order", recreated, replay, err)
	}
	for _, query := range []string{"SELECT COUNT(*) FROM audit_events WHERE target=? AND action='order.cancelled'", "SELECT COUNT(*) FROM outbox_events WHERE aggregate_id=? AND event_type='order.cancelled'"} {
		if count := cancellationSQLCount(t, f, query, order.ID); count != 1 {
			t.Fatal("duplicate cancellation evidence", count)
		}
	}
	var auditOwner, actorEmail, detail string
	if err = f.m.DB.QueryRowContext(f.ctx, "SELECT user_id,actor_email,detail FROM audit_events WHERE target=? AND action='order.cancelled'", order.ID).Scan(&auditOwner, &actorEmail, &detail); err != nil || auditOwner != f.owner.ID || actorEmail != f.owner.Email || !strings.HasSuffix(detail, "Customer changed plans") || strings.Contains(detail, "A different retry reason") {
		t.Fatal("incorrect cancellation audit", auditOwner, actorEmail, detail, err)
	}
	summary, err := f.m.Summary(f.ctx, f.owner.ID, "CNY")
	if err != nil || summary.PendingInvoices != 0 || summary.UnpaidTotal != 0 || summary.Revenue != 0 || summary.ActiveServices != 1 {
		t.Fatal("incorrect financial projection", summary, err)
	}
}

func TestMySQLCancellationAuthorizationAndUnsafeStates(t *testing.T) {
	f := newCancellationSQLFixture(t)
	order, _, _ := f.order(t)
	if _, err := f.m.CancelOrder(f.ctx, f.other, order.ID, "Other customer"); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-customer cancellation", err)
	}
	if _, err := f.m.CancelOrder(f.ctx, User{ID: f.owner.ID, Role: "unknown"}, order.ID, "Invalid actor"); !errors.Is(err, ErrNotFound) {
		t.Fatal("invalid role cancellation", err)
	}
	assertCancellationSQLState(t, f, order.ID, "pending_payment", "unpaid")
	assertNoCancellationSQLEffects(t, f, order.ID)
	if _, err := f.m.CancelOrder(f.ctx, f.admin, order.ID, "Administrator cancelled"); err != nil {
		t.Fatal(err)
	}
	var owner, actorEmail string
	if err := f.m.DB.QueryRowContext(f.ctx, "SELECT user_id,actor_email FROM audit_events WHERE target=? AND action='order.cancelled'", order.ID).Scan(&owner, &actorEmail); err != nil || owner != f.owner.ID || actorEmail != f.admin.Email {
		t.Fatal("admin audit identity", owner, actorEmail, err)
	}
	tests := []struct {
		name   string
		mutate func(Order, Invoice)
	}{
		{"paid", func(o Order, i Invoice) {
			migrationExec(t, f.ctx, f.m.DB, "UPDATE orders SET status='paid' WHERE id=?", o.ID)
			migrationExec(t, f.ctx, f.m.DB, "UPDATE invoices SET status='paid',paid_at=UTC_TIMESTAMP(6) WHERE id=?", i.ID)
		}},
		{"paid timestamp", func(_ Order, i Invoice) {
			migrationExec(t, f.ctx, f.m.DB, "UPDATE invoices SET paid_at=UTC_TIMESTAMP(6) WHERE id=?", i.ID)
		}},
		{"mismatched state", func(o Order, _ Invoice) {
			migrationExec(t, f.ctx, f.m.DB, "UPDATE orders SET status='cancelled' WHERE id=?", o.ID)
		}},
		{"missing invoice", func(_ Order, i Invoice) { migrationExec(t, f.ctx, f.m.DB, "DELETE FROM invoices WHERE id=?", i.ID) }},
		{"invoice owner", func(_ Order, i Invoice) {
			migrationExec(t, f.ctx, f.m.DB, "UPDATE invoices SET user_id=? WHERE id=?", f.other.ID, i.ID)
		}},
		{"invoice amount", func(_ Order, i Invoice) {
			migrationExec(t, f.ctx, f.m.DB, "UPDATE invoices SET amount_minor=amount_minor+1 WHERE id=?", i.ID)
		}},
		{"invoice currency", func(_ Order, i Invoice) {
			migrationExec(t, f.ctx, f.m.DB, "UPDATE invoices SET currency='USD',currency_snapshot=JSON_SET(currency_snapshot,'$.code','USD') WHERE id=?", i.ID)
		}},
		{"invoice precision", func(_ Order, i Invoice) {
			migrationExec(t, f.ctx, f.m.DB, "UPDATE invoices SET currency_snapshot=JSON_SET(currency_snapshot,'$.decimal_places',3) WHERE id=?", i.ID)
		}},
		{"invoice issuer", func(_ Order, i Invoice) {
			migrationExec(t, f.ctx, f.m.DB, "UPDATE invoices SET issuer_snapshot=JSON_SET(issuer_snapshot,'$.name','Different issuer') WHERE id=?", i.ID)
		}},
		{"missing invoice currency snapshot", func(_ Order, i Invoice) {
			migrationExec(t, f.ctx, f.m.DB, "UPDATE invoices SET currency_snapshot=NULL WHERE id=?", i.ID)
		}},
		{"missing order currency snapshot", func(o Order, _ Invoice) {
			migrationExec(t, f.ctx, f.m.DB, "UPDATE orders SET currency_snapshot=NULL WHERE id=?", o.ID)
		}},
		{"malformed invoice issuer snapshot", func(_ Order, i Invoice) {
			migrationExec(t, f.ctx, f.m.DB, "UPDATE invoices SET issuer_snapshot=JSON_ARRAY(1) WHERE id=?", i.ID)
		}},
		{"malformed order issuer snapshot", func(o Order, _ Invoice) {
			migrationExec(t, f.ctx, f.m.DB, "UPDATE orders SET issuer_snapshot=JSON_ARRAY(1) WHERE id=?", o.ID)
		}},
		{"malformed plan snapshot", func(o Order, _ Invoice) {
			migrationExec(t, f.ctx, f.m.DB, "UPDATE orders SET plan_snapshot=JSON_ARRAY(1) WHERE id=?", o.ID)
		}},
		{"issued time mismatch", func(_ Order, i Invoice) {
			migrationExec(t, f.ctx, f.m.DB, "UPDATE invoices SET issued_at=DATE_ADD(issued_at,INTERVAL 1 SECOND) WHERE id=?", i.ID)
		}},
		{"cancelled without cancellation evidence", func(o Order, i Invoice) {
			migrationExec(t, f.ctx, f.m.DB, "UPDATE orders SET status='cancelled' WHERE id=?", o.ID)
			migrationExec(t, f.ctx, f.m.DB, "UPDATE invoices SET status='void' WHERE id=?", i.ID)
		}},
		{"missing creation event", func(o Order, _ Invoice) {
			migrationExec(t, f.ctx, f.m.DB, "DELETE FROM outbox_events WHERE aggregate_id=?", o.ID)
		}},
		{"processed creation event", func(o Order, _ Invoice) {
			migrationExec(t, f.ctx, f.m.DB, "UPDATE outbox_events SET processed_at=UTC_TIMESTAMP(6) WHERE aggregate_id=?", o.ID)
		}},
		{"attempted creation event", func(o Order, _ Invoice) {
			migrationExec(t, f.ctx, f.m.DB, "UPDATE outbox_events SET attempts=1 WHERE aggregate_id=?", o.ID)
		}},
		{"wrong creation association", func(o Order, _ Invoice) {
			migrationExec(t, f.ctx, f.m.DB, "UPDATE outbox_events SET payload=JSON_SET(payload,'$.invoice_id',?) WHERE aggregate_id=?", ID(), o.ID)
		}},
		{"other lifecycle event", func(o Order, _ Invoice) {
			migrationExec(t, f.ctx, f.m.DB, "INSERT INTO outbox_events(id,aggregate_id,event_type,payload,created_at) VALUES(?,?,'order.fulfilled',JSON_OBJECT(),UTC_TIMESTAMP(6))", ID(), o.ID)
		}},
		{"matching service", func(o Order, _ Invoice) {
			migrationExec(t, f.ctx, f.m.DB, "INSERT INTO services(id,user_id,name,plan_name,region,status,next_due_at,price_minor,currency,cpu,memory_gb,disk_gb) VALUES(?,?,?,'another plan','fixture','terminated',UTC_TIMESTAMP(6),3900,'CNY',1,1,1)", ID(), o.UserID, o.Hostname)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o, i, _ := f.order(t)
			tc.mutate(o, i)
			before := cancellationSQLRecords(t, f, o.ID)
			if _, err := f.m.CancelOrder(f.ctx, f.owner, o.ID, "Unsafe fixture"); !errors.Is(err, ErrCancellationState) {
				t.Fatal("unsafe cancellation accepted", err)
			}
			if after := cancellationSQLRecords(t, f, o.ID); !reflect.DeepEqual(before, after) {
				t.Fatal("rejected cancellation changed records", before, after)
			}
			assertNoCancellationSQLEffects(t, f, o.ID)
		})
	}
}

func TestMySQLCancellationIgnoresUnrelatedMalformedHistory(t *testing.T) {
	f := newCancellationSQLFixture(t)
	malformed, invoice, _ := f.order(t)
	migrationExec(t, f.ctx, f.m.DB, "UPDATE orders SET currency_snapshot=NULL WHERE id=?", malformed.ID)
	migrationExec(t, f.ctx, f.m.DB, "UPDATE invoices SET currency_snapshot=NULL WHERE id=?", invoice.ID)
	before := cancellationSQLRecords(t, f, malformed.ID)
	// Constructing and cancelling a healthy pair must not decode or repair the
	// malformed neighbor. Its own cancellation must still fail closed.
	healthy, healthyInvoice, _ := f.order(t)
	out, err := f.m.CancelOrder(f.ctx, f.owner, healthy.ID, "Healthy order is no longer needed")
	if err != nil || out.Order.ID != healthy.ID || out.Invoice.ID != healthyInvoice.ID || out.Order.Status != "cancelled" || out.Invoice.Status != "void" {
		t.Fatal("unrelated malformed history blocked healthy cancellation", out, err)
	}
	if _, err = f.m.CancelOrder(f.ctx, f.owner, malformed.ID, "Malformed history requires review"); !errors.Is(err, ErrCancellationState) {
		t.Fatal("malformed pair did not fail closed", err)
	}
	if after := cancellationSQLRecords(t, f, malformed.ID); !reflect.DeepEqual(before, after) {
		t.Fatal("unrelated malformed history was changed", before, after)
	}
	assertNoCancellationSQLEffects(t, f, malformed.ID)
}

func TestMySQLCancellationConcurrentRetries(t *testing.T) {
	f := newCancellationSQLFixture(t)
	order, invoice, _ := f.order(t)
	const workers = 12
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for n := 0; n < workers; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := f.m.CancelOrder(f.ctx, f.owner, order.ID, "Concurrent cancellation")
			if err == nil && (out.Order.ID != order.ID || out.Invoice.ID != invoice.ID || out.Order.Status != "cancelled" || out.Invoice.Status != "void") {
				err = errors.New("incorrect concurrent cancellation result")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertCancellationSQLState(t, f, order.ID, "cancelled", "void")
	for _, query := range []string{"SELECT COUNT(*) FROM audit_events WHERE target=? AND action='order.cancelled'", "SELECT COUNT(*) FROM outbox_events WHERE aggregate_id=? AND event_type='order.cancelled'"} {
		if count := cancellationSQLCount(t, f, query, order.ID); count != 1 {
			t.Fatal("concurrent duplicate cancellation effects", count)
		}
	}
}

func TestMySQLCancellationLocksConfigurationBeforeOrder(t *testing.T) {
	f := newCancellationSQLFixture(t)
	order, _, _ := f.order(t)
	configTx, err := f.m.DB.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer configTx.Rollback()
	var revision int64
	if err = configTx.QueryRowContext(f.ctx, "SELECT prices_revision FROM currency_settings WHERE id=1 FOR UPDATE").Scan(&revision); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := f.m.CancelOrder(f.ctx, f.owner, order.ID, "Wait for currency edit")
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatal("cancellation bypassed exclusive currency lock", err)
	case <-time.After(50 * time.Millisecond):
	}
	// A blocked cancellation must not already hold its order lock.
	lockCtx, cancel := context.WithTimeout(f.ctx, time.Second)
	defer cancel()
	orderTx, err := f.m.DB.BeginTx(lockCtx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer orderTx.Rollback()
	var id string
	if err = orderTx.QueryRowContext(lockCtx, "SELECT id FROM orders WHERE id=? FOR UPDATE", order.ID).Scan(&id); err != nil {
		t.Fatal("cancellation locked order before currency configuration", err)
	}
	if err = orderTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = configTx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
}

func TestMySQLCancellationRechecksPaidStateAfterLockWait(t *testing.T) {
	f := newCancellationSQLFixture(t)
	order, invoice, _ := f.order(t)
	tx, err := f.m.DB.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var id string
	if err = tx.QueryRowContext(f.ctx, "SELECT id FROM orders WHERE id=? FOR UPDATE", order.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := f.m.CancelOrder(f.ctx, f.owner, order.ID, "Payment race fixture")
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatal("cancellation bypassed locked order", err)
	case <-time.After(50 * time.Millisecond):
	}
	// Synthetic local fixture only: no provider callback or payment is invoked.
	if _, err = tx.ExecContext(f.ctx, "UPDATE orders SET status='paid' WHERE id=?", order.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(f.ctx, "UPDATE invoices SET status='paid',paid_at=UTC_TIMESTAMP(6) WHERE id=?", invoice.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrCancellationState) {
			t.Fatal("cancellation overwrote newly paid state", err)
		}
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	assertCancellationSQLState(t, f, order.ID, "paid", "paid")
	assertNoCancellationSQLEffects(t, f, order.ID)
}

func TestMySQLCancellationRollsBackLateFailures(t *testing.T) {
	f := newCancellationSQLFixture(t)
	for _, table := range []string{"audit_events", "outbox_events"} {
		t.Run(table, func(t *testing.T) {
			order, _, _ := f.order(t)
			before := cancellationSQLHistory(t, f, order.ID)
			trigger := "test_reject_cancel_" + table
			migrationExec(t, f.ctx, f.m.DB, "CREATE TRIGGER "+trigger+" BEFORE INSERT ON "+table+" FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='cancellation failure fixture'")
			defer func() { migrationExec(t, f.ctx, f.m.DB, "DROP TRIGGER IF EXISTS "+trigger) }()
			if _, err := f.m.CancelOrder(f.ctx, f.owner, order.ID, "Rollback fixture"); err == nil {
				t.Fatal("injected failure was accepted")
			}
			assertCancellationSQLState(t, f, order.ID, "pending_payment", "unpaid")
			assertNoCancellationSQLEffects(t, f, order.ID)
			if after := cancellationSQLHistory(t, f, order.ID); !reflect.DeepEqual(before, after) {
				t.Fatal("failed cancellation changed history", before, after)
			}
			migrationExec(t, f.ctx, f.m.DB, "DROP TRIGGER "+trigger)
			if _, err := f.m.CancelOrder(f.ctx, f.owner, order.ID, "Retry after fixture failure"); err != nil {
				t.Fatal("rollback prevented subsequent cancellation", err)
			}
		})
	}
}
