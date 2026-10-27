package billing

import (
	"aster/billing/internal/integrations"
	"aster/billing/pkg/pluginapi/paymentv1"
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// This connector only wraps a database already created by migrationTestDatabase.
// SQL and COMMIT go to real MySQL. Only after COMMIT returns success does the
// armed test transaction close its connection and lose the acknowledgement.
// There is no global driver registration and no production DSN fallback.
type storePaymentAckFault struct {
	armed     atomic.Bool
	injected  atomic.Int32
	committed atomic.Int32
}

type storePaymentAckConnector struct {
	driver.Connector
	fault *storePaymentAckFault
}

func (c *storePaymentAckConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &storePaymentAckConn{Conn: conn, fault: c.fault}, nil
}

type storePaymentAckConn struct {
	driver.Conn
	fault  *storePaymentAckFault
	closed atomic.Bool
}

func (c *storePaymentAckConn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	return c.Conn.Close()
}

func (c *storePaymentAckConn) IsValid() bool {
	if c.closed.Load() {
		return false
	}
	if validator, ok := c.Conn.(driver.Validator); ok {
		return validator.IsValid()
	}
	return true
}

func (c *storePaymentAckConn) ResetSession(ctx context.Context) error {
	if c.closed.Load() {
		return driver.ErrBadConn
	}
	if resetter, ok := c.Conn.(driver.SessionResetter); ok {
		return resetter.ResetSession(ctx)
	}
	return nil
}

func (c *storePaymentAckConn) Begin() (driver.Tx, error) {
	tx, err := c.Conn.Begin()
	if err != nil {
		return nil, err
	}
	return &storePaymentAckTx{Tx: tx, conn: c}, nil
}

func (c *storePaymentAckConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	beginner, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, errors.New("MySQL test connection does not support transactional context")
	}
	tx, err := beginner.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &storePaymentAckTx{Tx: tx, conn: c}, nil
}

type storePaymentAckTx struct {
	driver.Tx
	conn *storePaymentAckConn
}

func (tx *storePaymentAckTx) Commit() error {
	if err := tx.Tx.Commit(); err != nil {
		return err
	}
	tx.conn.fault.committed.Add(1)
	if tx.conn.fault.armed.CompareAndSwap(true, false) {
		tx.conn.fault.injected.Add(1)
		_ = tx.conn.Close()
		// Do not return driver.ErrBadConn: an ambiguous committed write is not
		// safe for database/sql to repeat as a generic bad-connection operation.
		return &net.OpError{Op: "read", Net: "mysql-test-commit-ack", Err: io.ErrUnexpectedEOF}
	}
	return nil
}

func storePaymentAckDatabase(t *testing.T, f cancellationSQLFixture) (*MySQL, *storePaymentAckFault) {
	t.Helper()
	// The existing helper has already rejected an absent/non-test DSN and
	// created this isolated name. Never wrap the shared DSN's original DB.
	if !strings.HasPrefix(f.m.database, "aster_migration_test_") || !validCancellationID(strings.TrimPrefix(f.m.database, "aster_migration_test_")) {
		t.Fatal("commit acknowledgement test requires its own disposable database")
	}
	cfg, err := mysql.ParseDSN(os.Getenv("ASTER_MYSQL_TEST_DSN"))
	if err != nil || !strings.Contains(cfg.DBName, "_test") {
		t.Fatal("commit acknowledgement test requires the existing synthetic test DSN")
	}
	cfg.DBName = f.m.database
	cfg, err = mysqlConfiguration(cfg.FormatDSN(), true)
	if err != nil || cfg.Net != "unix" {
		t.Fatal("commit acknowledgement test requires the fixture's local Unix socket")
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		t.Fatal("construct real MySQL fixture connector")
	}
	fault := &storePaymentAckFault{}
	db := sql.OpenDB(&storePaymentAckConnector{Connector: connector, fault: fault})
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	var selected string
	if err := db.QueryRowContext(f.ctx, "SELECT DATABASE()").Scan(&selected); err != nil || selected != f.m.database {
		t.Fatal("wrapped connection did not select its owned disposable database")
	}
	return &MySQL{DB: db, transport: f.m.transport, database: f.m.database}, fault
}

func storePaymentAckCheckout(app *App, owner User, invoice Invoice) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/invoices/"+invoice.ID+"/checkout", nil)
	app.checkout(response, request, owner, invoice)
	return response
}

func storePaymentAckNotify(app *App, raw []byte, signature string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/payment-callbacks/payment.fixture", bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Fixture-Signature", signature)
	app.paymentCallbackHTTP(response, request, "payment.fixture")
	return response
}

func storePaymentAckErrorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal("decode commit acknowledgement error response", err)
	}
	return body.Error.Code
}

func TestPaymentMySQLReserveCommitAcknowledgementLost(t *testing.T) {
	f := newCancellationSQLFixture(t)
	order, invoice, _ := f.order(t)
	wrapped, fault := storePaymentAckDatabase(t, f)
	runtime := newPaymentHTTPRuntime(t)
	app := NewApp(wrapped, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), PaymentRuntime: runtime})
	fault.armed.Store(true)
	first := storePaymentAckCheckout(app, f.owner, invoice)
	if first.Code != 503 || storePaymentAckErrorCode(t, first) != "PAYMENT_STORAGE_UNAVAILABLE" || fault.injected.Load() != 1 || fault.committed.Load() != 1 {
		t.Fatal("lost reserve acknowledgement was not reported as ambiguous storage failure", first.Code)
	}
	if calls, _ := runtime.counts(); calls != 0 {
		t.Fatal("reserve with lost commit acknowledgement dispatched a checkout", calls)
	}
	// Read through the independent original connection pool, proving that the
	// real server committed before the simulated acknowledgement was lost.
	attempt, err := readPaymentAttempt(f.m.DB.QueryRowContext(f.ctx, "SELECT "+paymentAttemptColumns+" FROM payment_attempts WHERE invoice_id=?", invoice.ID))
	if err != nil || attempt.State != "pending" || attempt.Expectation != paymentExpectation(order, invoice, runtime.selection.ID, runtime.selection.MerchantID) {
		t.Fatal("ambiguous reserve did not leave its immutable durable cancellation barrier", attempt, err)
	}
	second := storePaymentAckCheckout(app, f.owner, invoice)
	if second.Code != 409 || storePaymentAckErrorCode(t, second) != "PAYMENT_PENDING" {
		t.Fatal("retry did not retain the existing pending reservation", second.Code)
	}
	if calls, _ := runtime.counts(); calls != 0 || fault.injected.Load() != 1 {
		t.Fatal("retry dispatched a second checkout or repeated the injected fault", calls)
	}
	if _, err := f.m.CancelOrder(f.ctx, f.owner, order.ID, "synthetic commit acknowledgement lost"); !errors.Is(err, ErrCancellationState) {
		t.Fatal("lost reserve acknowledgement allowed cancellation", err)
	}
	assertCancellationSQLState(t, f, order.ID, "pending_payment", "unpaid")
	if count := cancellationSQLCount(t, f, "SELECT COUNT(*) FROM payment_attempts WHERE invoice_id=?", invoice.ID); count != 1 {
		t.Fatal("retry duplicated the durable reservation", count)
	}
	if count := cancellationSQLCount(t, f, "SELECT COUNT(*) FROM payment_settlements WHERE attempt_id=?", attempt.ID); count != 0 {
		t.Fatal("ambiguous checkout created a settlement", count)
	}
}

func TestPaymentMySQLCheckoutCommitAcknowledgementLost(t *testing.T) {
	f := newCancellationSQLFixture(t)
	order, invoice, _ := f.order(t)
	wrapped, fault := storePaymentAckDatabase(t, f)
	runtime := newPaymentHTTPRuntime(t)
	runtime.duringCreate = func(context.Context, paymentv1.Config, paymentv1.Intent) {
		// Reservation has already committed before the worker is called. Arm
		// only its subsequent checkout-result transaction, never reservation.
		if fault.committed.Load() != 1 || fault.injected.Load() != 0 {
			t.Fatal("checkout reached worker without one acknowledged reservation")
		}
		fault.armed.Store(true)
	}
	app := NewApp(wrapped, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), PaymentRuntime: runtime})
	first := storePaymentAckCheckout(app, f.owner, invoice)
	if first.Code != 503 || storePaymentAckErrorCode(t, first) != "PAYMENT_STORAGE_UNAVAILABLE" || fault.injected.Load() != 1 || fault.committed.Load() != 2 {
		t.Fatal("lost checkout-result acknowledgement was not reported as ambiguous storage failure", first.Code)
	}
	if calls, _ := runtime.counts(); calls != 1 {
		t.Fatal("checkout-result fault did not follow exactly one worker call", calls)
	}
	// The original, independent pool must observe the real committed result,
	// although the caller received an error instead of its checkout URL.
	attempt, err := f.m.PaymentAttempt(f.ctx, f.owner, runtime.event(t, 0).AttemptID)
	if err != nil || attempt.State != "checkout" || attempt.Checkout == nil || attempt.Expectation != paymentExpectation(order, invoice, runtime.selection.ID, runtime.selection.MerchantID) {
		t.Fatal("lost acknowledgement discarded the committed checkout or expectation", attempt, err)
	}
	assertCancellationSQLState(t, f, order.ID, "pending_payment", "unpaid")
	if _, err := f.m.CancelOrder(f.ctx, f.owner, order.ID, "synthetic checkout-result acknowledgement lost"); !errors.Is(err, ErrCancellationState) {
		t.Fatal("committed checkout with lost acknowledgement allowed cancellation", err)
	}
	if count := cancellationSQLCount(t, f, "SELECT COUNT(*) FROM payment_settlements WHERE attempt_id=?", attempt.ID); count != 0 {
		t.Fatal("lost checkout acknowledgement created a settlement", count)
	}
	second := storePaymentAckCheckout(app, f.owner, invoice)
	var resumed struct {
		Data integrations.Checkout `json:"data"`
	}
	if second.Code != 200 || json.Unmarshal(second.Body.Bytes(), &resumed) != nil || resumed.Data != *attempt.Checkout {
		t.Fatal("retry did not return the original committed checkout", second.Code)
	}
	stored, err := f.m.PaymentAttempt(f.ctx, f.owner, attempt.ID)
	if err != nil || !reflect.DeepEqual(stored, attempt) || fault.injected.Load() != 1 {
		t.Fatal("checkout retry rewrote the durable attempt or repeated the fault", stored, err)
	}
	if count := cancellationSQLCount(t, f, "SELECT COUNT(*) FROM payment_attempts WHERE invoice_id=?", invoice.ID); count != 1 {
		t.Fatal("checkout retry created another payment attempt", count)
	}
	event := runtime.event(t, 0)
	raw, signature := runtime.signed(t, event)
	for range 2 {
		response := storePaymentAckNotify(app, raw, signature)
		var received struct {
			Data struct {
				Received bool `json:"received"`
			} `json:"data"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &received) != nil || !received.Data.Received {
			t.Fatal("verified settlement or exact replay failed after checkout acknowledgement loss", response.Code)
		}
	}
	assertCancellationSQLState(t, f, order.ID, "paid", "paid")
	for _, check := range []struct {
		query string
		id    string
	}{
		{"SELECT COUNT(*) FROM payment_settlements WHERE attempt_id=?", attempt.ID},
		{"SELECT COUNT(*) FROM audit_events WHERE target=? AND action='payment.settled'", order.ID},
		{"SELECT COUNT(*) FROM outbox_events WHERE aggregate_id=? AND event_type='payment.settled'", order.ID},
	} {
		if count := cancellationSQLCount(t, f, check.query, check.id); count != 1 {
			t.Fatal("checkout acknowledgement loss or notification replay duplicated financial records", count)
		}
	}
	if calls, _ := runtime.counts(); calls != 1 || fault.injected.Load() != 1 {
		t.Fatal("checkout retry or callback replay dispatched another worker checkout", calls)
	}
}

func TestPaymentMySQLSettlementCommitAcknowledgementLost(t *testing.T) {
	f := newCancellationSQLFixture(t)
	order, invoice, _ := f.order(t)
	wrapped, fault := storePaymentAckDatabase(t, f)
	runtime := newPaymentHTTPRuntime(t)
	app := NewApp(wrapped, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), PaymentRuntime: runtime})
	if response := storePaymentAckCheckout(app, f.owner, invoice); response.Code != 200 {
		t.Fatal("synthetic initial checkout failed", response.Code)
	}
	event := runtime.event(t, 0)
	raw, signature := runtime.signed(t, event)
	fault.armed.Store(true)
	first := storePaymentAckNotify(app, raw, signature)
	if first.Code != 503 || storePaymentAckErrorCode(t, first) != "PAYMENT_STORAGE_UNAVAILABLE" || fault.injected.Load() != 1 {
		t.Fatal("lost settlement acknowledgement was incorrectly confirmed", first.Code)
	}
	assertCancellationSQLState(t, f, order.ID, "paid", "paid")
	assertOnce := func() {
		t.Helper()
		for _, check := range []struct {
			query string
			id    string
		}{
			{"SELECT COUNT(*) FROM payment_settlements WHERE attempt_id=?", event.AttemptID},
			{"SELECT COUNT(*) FROM audit_events WHERE target=? AND action='payment.settled'", order.ID},
			{"SELECT COUNT(*) FROM outbox_events WHERE aggregate_id=? AND event_type='payment.settled'", order.ID},
		} {
			if count := cancellationSQLCount(t, f, check.query, check.id); count != 1 {
				t.Fatal("ambiguous settlement/replay did not persist exactly one financial record", count)
			}
		}
		stored, err := f.m.Invoice(f.ctx, f.owner.ID, invoice.ID)
		expected := cloneInvoice(invoice)
		paid := event.SettledAt.UTC().Truncate(time.Microsecond)
		expected.Status, expected.PaidAt = "paid", &paid
		if err != nil || !reflect.DeepEqual(stored, expected) {
			t.Fatal("settlement changed canonical amount, currency, owner or invoice history", stored, err)
		}
	}
	assertOnce()
	beforeReplay := cancellationSQLRecords(t, f, order.ID)
	beforeAttempt, err := f.m.PaymentAttempt(f.ctx, f.owner, event.AttemptID)
	if err != nil || beforeAttempt.State != "settled" {
		t.Fatal("committed settlement did not finalize its durable attempt", beforeAttempt, err)
	}
	readReceipt := func() paymentReceipt {
		t.Helper()
		var receipt paymentReceipt
		if err := f.m.DB.QueryRowContext(f.ctx, "SELECT attempt_id,provider_id,event_id,transaction_id,settled_at,received_at FROM payment_settlements WHERE attempt_id=?", event.AttemptID).
			Scan(&receipt.AttemptID, &receipt.ProviderID, &receipt.EventID, &receipt.TransactionID, &receipt.SettledAt, &receipt.ReceivedAt); err != nil {
			t.Fatal("read independently committed payment receipt", err)
		}
		return receipt
	}
	beforeReceipt := readReceipt()
	replay := storePaymentAckNotify(app, raw, signature)
	var received struct {
		Data struct {
			Received bool `json:"received"`
		} `json:"data"`
	}
	if replay.Code != 200 || json.Unmarshal(replay.Body.Bytes(), &received) != nil || !received.Data.Received || fault.injected.Load() != 1 {
		t.Fatal("exact verified replay did not confirm the committed settlement", replay.Code)
	}
	assertOnce()
	afterAttempt, err := f.m.PaymentAttempt(f.ctx, f.owner, event.AttemptID)
	if err != nil || !reflect.DeepEqual(beforeAttempt, afterAttempt) || !reflect.DeepEqual(beforeReceipt, readReceipt()) ||
		!reflect.DeepEqual(beforeReplay, cancellationSQLRecords(t, f, order.ID)) {
		t.Fatal("exact replay rewrote committed payment or business history", err)
	}
	if calls, _ := runtime.counts(); calls != 1 {
		t.Fatal("settlement acknowledgement loss created another external checkout", calls)
	}
	if _, err := f.m.CancelOrder(f.ctx, f.owner, order.ID, "synthetic committed settlement"); !errors.Is(err, ErrCancellationState) {
		t.Fatal("committed settlement allowed cancellation", err)
	}
}
