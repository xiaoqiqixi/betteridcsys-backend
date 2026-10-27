package billing

import (
	"aster/billing/internal/integrations"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
)

const paymentAttemptColumns = "id,request_key,user_id,invoice_id,order_id,provider_id,merchant_account_id,amount_minor,currency,currency_decimal_places,state,checkout,created_at,updated_at"

func readPaymentAttempt(row interface{ Scan(...any) error }) (PaymentAttempt, error) {
	var a PaymentAttempt
	var raw []byte
	e := row.Scan(&a.ID, &a.RequestKey, &a.Expectation.AccountID, &a.Expectation.InvoiceID, &a.Expectation.OrderID, &a.Expectation.ProviderID, &a.Expectation.MerchantAccountID, &a.Expectation.AmountMinor, &a.Expectation.Currency, &a.Expectation.CurrencyDecimalPlaces, &a.State, &raw, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if e != nil {
		return a, e
	}
	if len(raw) > 0 && json.Unmarshal(raw, &a.Checkout) != nil {
		return PaymentAttempt{}, ErrPaymentState
	}
	return a, nil
}

func paymentDBError(err error) error {
	var databaseErr *mysql.MySQLError
	if errors.As(err, &databaseErr) && databaseErr.Number == 1062 {
		return ErrPaymentConflict
	}
	return err
}

func retryPayment[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T
	for n := 0; n < 3; n++ {
		out, err := fn()
		var databaseErr *mysql.MySQLError
		if !errors.As(err, &databaseErr) || (databaseErr.Number != 1213 && databaseErr.Number != 1205) {
			return out, paymentDBError(err)
		}
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(time.Duration(n+1) * 30 * time.Millisecond):
		}
	}
	return zero, errors.New("transaction retry exhausted")
}

// All financial paths take currency settings, order, invoice, then attempt.
// The initial nonlocking lookup only finds the order lock; the locked pair is
// checked again before any write, including owner and original invoice ID.
func lockPaymentPair(ctx context.Context, tx *sql.Tx, actor User, invoiceID string, requireEnabled bool) (Order, Invoice, error) {
	config, _, e := readCurrencies(ctx, tx, " FOR SHARE")
	if e != nil {
		return Order{}, Invoice{}, e
	}
	var orderID string
	query := "SELECT order_id FROM invoices WHERE id=?"
	args := []any{invoiceID}
	if actor.Role != "admin" {
		query += " AND user_id=?"
		args = append(args, actor.ID)
	}
	if e = tx.QueryRowContext(ctx, query, args...).Scan(&orderID); errors.Is(e, sql.ErrNoRows) {
		return Order{}, Invoice{}, ErrNotFound
	} else if e != nil {
		return Order{}, Invoice{}, e
	}
	o, e := readCancellationOrder(tx.QueryRowContext(ctx, "SELECT "+orderColumns+" FROM orders WHERE id=? FOR UPDATE", orderID))
	if errors.Is(e, sql.ErrNoRows) || errors.Is(e, ErrCancellationState) {
		return Order{}, Invoice{}, ErrPaymentState
	}
	if e != nil {
		return Order{}, Invoice{}, e
	}
	i, e := readCancellationInvoice(ctx, tx, o.ID)
	if errors.Is(e, ErrCancellationState) {
		return Order{}, Invoice{}, ErrPaymentState
	}
	if e != nil {
		return Order{}, Invoice{}, e
	}
	if i.ID != invoiceID {
		return Order{}, Invoice{}, ErrPaymentState
	}
	if e = paymentPair(actor, o, i); e != nil {
		return Order{}, Invoice{}, e
	}
	if currency, known := config.find(i.Currency); !known || currency.DecimalPlaces != i.CurrencyFormat.DecimalPlaces {
		return Order{}, Invoice{}, ErrPaymentState
	} else if requireEnabled && !currency.Enabled {
		return Order{}, Invoice{}, ErrCurrency
	}
	return o, i, nil
}

func (m *MySQL) ReservePayment(ctx context.Context, actor User, invoiceID, key, provider, merchant string) (PaymentAttempt, bool, error) {
	if e := validatePaymentInput(actor, invoiceID, key, provider, merchant); e != nil {
		return PaymentAttempt{}, false, e
	}
	type reserved struct {
		a      PaymentAttempt
		replay bool
	}
	out, e := retryPayment(ctx, func() (reserved, error) {
		tx, e := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if e != nil {
			return reserved{}, e
		}
		defer tx.Rollback()
		o, i, e := lockPaymentPair(ctx, tx, actor, invoiceID, true)
		if e != nil {
			return reserved{}, e
		}
		// The unique invoice key prevents a second provider or request key from
		// issuing another live checkout for the same obligation.
		a, e := readPaymentAttempt(tx.QueryRowContext(ctx, "SELECT "+paymentAttemptColumns+" FROM payment_attempts WHERE invoice_id=? OR (user_id=? AND request_key=?) ORDER BY id LIMIT 1 FOR UPDATE", invoiceID, i.UserID, key))
		if e == nil {
			if a.RequestKey != key || a.Expectation != paymentExpectation(o, i, provider, merchant) {
				return reserved{}, ErrPaymentConflict
			}
			if e = validateAttemptPair(a, o, i); e != nil {
				return reserved{}, e
			}
			if e = tx.Commit(); e != nil {
				return reserved{}, e
			}
			return reserved{a, true}, nil
		}
		if !errors.Is(e, ErrNotFound) {
			return reserved{}, e
		}
		if o.Status != "pending_payment" || i.Status != "unpaid" || i.PaidAt != nil {
			return reserved{}, ErrPaymentState
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		a = PaymentAttempt{ID: ID(), RequestKey: key, Expectation: paymentExpectation(o, i, provider, merchant), State: "pending", CreatedAt: now, UpdatedAt: now}
		_, e = tx.ExecContext(ctx, "INSERT INTO payment_attempts(id,request_key,user_id,invoice_id,order_id,provider_id,merchant_account_id,amount_minor,currency,currency_decimal_places,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)", a.ID, key, i.UserID, i.ID, o.ID, provider, merchant, i.AmountMinor, i.Currency, i.CurrencyFormat.DecimalPlaces, a.State, now, now)
		if e != nil {
			return reserved{}, e
		}
		if e = tx.Commit(); e != nil {
			return reserved{}, e
		}
		return reserved{a, false}, nil
	})
	return out.a, out.replay, e
}

func (m *MySQL) PaymentAttempt(ctx context.Context, actor User, id string) (PaymentAttempt, error) {
	if !paymentActor(actor) || !validCancellationID(id) {
		return PaymentAttempt{}, ErrNotFound
	}
	return retryPayment(ctx, func() (PaymentAttempt, error) {
		tx, e := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if e != nil {
			return PaymentAttempt{}, e
		}
		defer tx.Rollback()
		a, e := readPaymentAttempt(tx.QueryRowContext(ctx, "SELECT "+paymentAttemptColumns+" FROM payment_attempts WHERE id=?", id))
		if e != nil {
			return PaymentAttempt{}, e
		}
		if actor.Role != "admin" && a.Expectation.AccountID != actor.ID {
			return PaymentAttempt{}, ErrNotFound
		}
		o, i, e := lockPaymentPair(ctx, tx, actor, a.Expectation.InvoiceID, false)
		if e != nil {
			return PaymentAttempt{}, e
		}
		a, e = readPaymentAttempt(tx.QueryRowContext(ctx, "SELECT "+paymentAttemptColumns+" FROM payment_attempts WHERE id=? FOR UPDATE", id))
		if e != nil {
			return PaymentAttempt{}, e
		}
		if e = validateAttemptPair(a, o, i); e != nil {
			return PaymentAttempt{}, e
		}
		if e = tx.Commit(); e != nil {
			return PaymentAttempt{}, e
		}
		return a, nil
	})
}

func (m *MySQL) CompletePaymentCheckout(ctx context.Context, id string, c integrations.Checkout, failed bool) (PaymentAttempt, error) {
	// This path never subsequently locks an order or invoice, so it cannot
	// invert the financial transaction lock order.
	type completed struct {
		a         PaymentAttempt
		uncertain bool
	}
	out, e := retryPayment(ctx, func() (completed, error) {
		tx, e := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if e != nil {
			return completed{}, e
		}
		defer tx.Rollback()
		a, e := readPaymentAttempt(tx.QueryRowContext(ctx, "SELECT "+paymentAttemptColumns+" FROM payment_attempts WHERE id=? FOR UPDATE", id))
		if e != nil {
			return completed{}, e
		}
		switch a.State {
		case "settled":
			return completed{a, false}, nil
		case "uncertain":
			return completed{a, true}, nil
		case "checkout":
			if !failed && a.Checkout != nil && *a.Checkout == c {
				return completed{a, false}, nil
			}
			return completed{}, ErrPaymentConflict
		case "pending":
		default:
			return completed{}, ErrPaymentState
		}
		var raw any
		uncertain := failed || validateCheckout(a, c) != nil
		if uncertain {
			a.State = "uncertain"
		} else {
			a.State = "checkout"
			a.Checkout = &c
			encoded, _ := json.Marshal(c)
			raw = encoded
		}
		a.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
		if _, e = tx.ExecContext(ctx, "UPDATE payment_attempts SET state=?,checkout=?,updated_at=? WHERE id=?", a.State, raw, a.UpdatedAt, id); e != nil {
			return completed{}, e
		}
		if e = tx.Commit(); e != nil {
			return completed{}, e
		}
		return completed{a, uncertain}, nil
	})
	if e != nil {
		return PaymentAttempt{}, e
	}
	if out.uncertain {
		return out.a, ErrPaymentUncertain
	}
	return out.a, nil
}

func (m *MySQL) SettlePayment(ctx context.Context, id string, v integrations.VerifiedPayment) (PaymentSettlement, error) {
	return retryPayment(ctx, func() (PaymentSettlement, error) {
		tx, e := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if e != nil {
			return PaymentSettlement{}, e
		}
		defer tx.Rollback()
		initial, e := readPaymentAttempt(tx.QueryRowContext(ctx, "SELECT "+paymentAttemptColumns+" FROM payment_attempts WHERE id=?", id))
		if e != nil {
			return PaymentSettlement{}, e
		}
		if e = validatePayment(initial, v); e != nil {
			return PaymentSettlement{}, e
		}
		o, i, e := lockPaymentPair(ctx, tx, User{ID: initial.Expectation.AccountID, Role: "customer"}, initial.Expectation.InvoiceID, false)
		if e != nil {
			return PaymentSettlement{}, e
		}
		a, e := readPaymentAttempt(tx.QueryRowContext(ctx, "SELECT "+paymentAttemptColumns+" FROM payment_attempts WHERE id=? FOR UPDATE", id))
		if e != nil {
			return PaymentSettlement{}, e
		}
		if e = validatePayment(a, v); e != nil {
			return PaymentSettlement{}, e
		}
		if e = validateAttemptPair(a, o, i); e != nil {
			return PaymentSettlement{}, e
		}
		rows, e := tx.QueryContext(ctx, "SELECT attempt_id,provider_id,event_id,transaction_id,settled_at,received_at FROM payment_settlements WHERE attempt_id=? OR (provider_id=? AND (event_id=? OR transaction_id=?)) FOR UPDATE", id, v.ProviderID, v.EventID, v.TransactionID)
		if e != nil {
			return PaymentSettlement{}, e
		}
		replayed := false
		for rows.Next() {
			var p paymentReceipt
			if e = rows.Scan(&p.AttemptID, &p.ProviderID, &p.EventID, &p.TransactionID, &p.SettledAt, &p.ReceivedAt); e != nil {
				rows.Close()
				return PaymentSettlement{}, e
			}
			if replayed || !samePaymentReceipt(p, a, v) || a.State != "settled" {
				rows.Close()
				return PaymentSettlement{}, ErrPaymentConflict
			}
			replayed = true
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return PaymentSettlement{}, e
		}
		if replayed {
			if e = tx.Commit(); e != nil {
				return PaymentSettlement{}, e
			}
			return PaymentSettlement{AttemptID: id, Invoice: i, Order: o, Replay: true}, nil
		}
		if a.State == "settled" {
			return PaymentSettlement{}, ErrPaymentState
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		paid := v.SettledAt.UTC().Truncate(time.Microsecond)
		if _, e = tx.ExecContext(ctx, "INSERT INTO payment_settlements(attempt_id,provider_id,event_id,transaction_id,settled_at,received_at) VALUES(?,?,?,?,?,?)", id, v.ProviderID, v.EventID, v.TransactionID, paid, now); e != nil {
			return PaymentSettlement{}, e
		}
		result, e := tx.ExecContext(ctx, "UPDATE orders SET status='paid' WHERE id=? AND status='pending_payment'", o.ID)
		if e = paymentUpdateResult(result, e); e != nil {
			return PaymentSettlement{}, e
		}
		result, e = tx.ExecContext(ctx, "UPDATE invoices SET status='paid',paid_at=? WHERE id=? AND order_id=? AND status='unpaid' AND paid_at IS NULL", paid, i.ID, o.ID)
		if e = paymentUpdateResult(result, e); e != nil {
			return PaymentSettlement{}, e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE payment_attempts SET state='settled',updated_at=? WHERE id=?", now, id); e != nil {
			return PaymentSettlement{}, e
		}
		o.Status = "paid"
		i.Status = "paid"
		i.PaidAt = &paid
		audit := paidAudit(o, a, now)
		if _, e = tx.ExecContext(ctx, "INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) VALUES(?,?,?,?,?,?,?)", audit.ID, audit.UserID, audit.Action, audit.Target, audit.CreatedAt, audit.ActorEmail, audit.Detail); e != nil {
			return PaymentSettlement{}, e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO outbox_events(id,aggregate_id,event_type,payload,created_at) VALUES(?,?,?,?,?)", ID(), o.ID, "payment.settled", paidEvent(o, i, a), now); e != nil {
			return PaymentSettlement{}, e
		}
		if e = tx.Commit(); e != nil {
			return PaymentSettlement{}, e
		}
		return PaymentSettlement{AttemptID: id, Invoice: i, Order: o}, nil
	})
}

func paymentUpdateResult(result sql.Result, e error) error {
	if e != nil {
		return e
	}
	count, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if count != 1 {
		return ErrPaymentState
	}
	return nil
}
