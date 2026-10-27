package billing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
)

// CancelOrder changes only an unfulfilled local order and its paired invoice.
// Reservation and settlement take the same order/invoice locks. Payment
// attempts remain reservations even when an adapter times out or expires.
func (m *MySQL) CancelOrder(ctx context.Context, actor User, orderID, reason string) (OrderCancellation, error) {
	reason, err := normalizeCancellationInput(actor, orderID, reason)
	if err != nil {
		return OrderCancellation{}, err
	}
	for n := 0; n < 3; n++ {
		out, err := m.cancelOrderOnce(ctx, actor, orderID, reason)
		var databaseErr *mysql.MySQLError
		if !errors.As(err, &databaseErr) || (databaseErr.Number != 1213 && databaseErr.Number != 1205) {
			return out, err
		}
		select {
		case <-ctx.Done():
			return OrderCancellation{}, ctx.Err()
		case <-time.After(time.Duration(n+1) * 30 * time.Millisecond):
		}
	}
	return OrderCancellation{}, errors.New("transaction retry exhausted")
}

func (m *MySQL) cancelOrderOnce(ctx context.Context, actor User, orderID, reason string) (OrderCancellation, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return OrderCancellation{}, err
	}
	defer tx.Rollback()
	// Currency edits count live obligations while holding this singleton lock.
	// Take it before resource locks, matching all other billing mutations.
	config, _, err := readCurrencies(ctx, tx, " FOR SHARE")
	if err != nil {
		return OrderCancellation{}, err
	}
	query := "SELECT " + orderColumns + " FROM orders WHERE id=?"
	args := []any{orderID}
	if actor.Role == "customer" {
		query += " AND user_id=?"
		args = append(args, actor.ID)
	}
	order, err := readCancellationOrder(tx.QueryRowContext(ctx, query+" FOR UPDATE", args...))
	if errors.Is(err, sql.ErrNoRows) {
		return OrderCancellation{}, ErrNotFound
	}
	if err != nil {
		return OrderCancellation{}, err
	}
	if currency, known := config.find(order.Currency); !known || currency.DecimalPlaces != order.CurrencyFormat.DecimalPlaces {
		return OrderCancellation{}, ErrCancellationState
	}
	invoice, err := readCancellationInvoice(ctx, tx, order.ID)
	if err != nil {
		return OrderCancellation{}, err
	}
	replay, err := validateCancellationPair(actor, order, invoice)
	if err != nil {
		return OrderCancellation{}, err
	}
	var attemptID string
	err = tx.QueryRowContext(ctx, "SELECT id FROM payment_attempts WHERE order_id=? LIMIT 1 FOR UPDATE", order.ID).Scan(&attemptID)
	if err == nil {
		return OrderCancellation{}, ErrCancellationState
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return OrderCancellation{}, err
	}
	// There is no order_id on services in this schema. A matching owner/name
	// is therefore a conservative association; unrelated services stay usable.
	var serviceID string
	err = tx.QueryRowContext(ctx, "SELECT id FROM services WHERE user_id=? AND name=? ORDER BY id LIMIT 1 FOR UPDATE", order.UserID, order.Hostname).Scan(&serviceID)
	if err == nil {
		return OrderCancellation{}, ErrCancellationState
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return OrderCancellation{}, err
	}
	if err = lockCancellationOutbox(ctx, tx, order, invoice, replay); err != nil {
		return OrderCancellation{}, err
	}
	if replay {
		if err = tx.Commit(); err != nil {
			return OrderCancellation{}, err
		}
		return OrderCancellation{Order: order, Invoice: invoice}, nil
	}
	result, err := tx.ExecContext(ctx, "UPDATE orders SET status='cancelled' WHERE id=? AND status='pending_payment'", order.ID)
	if err = cancellationUpdateResult(result, err); err != nil {
		return OrderCancellation{}, err
	}
	result, err = tx.ExecContext(ctx, "UPDATE invoices SET status='void' WHERE id=? AND order_id=? AND status='unpaid' AND paid_at IS NULL", invoice.ID, order.ID)
	if err = cancellationUpdateResult(result, err); err != nil {
		return OrderCancellation{}, err
	}
	order.Status, invoice.Status = "cancelled", "void"
	audit := cancellationAudit(actor, order, reason)
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) VALUES(?,?,?,?,?,?,?)", audit.ID, audit.UserID, audit.Action, audit.Target, audit.CreatedAt, audit.ActorEmail, audit.Detail); err != nil {
		return OrderCancellation{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO outbox_events(id,aggregate_id,event_type,payload,created_at) VALUES(?,?,?,?,?)", ID(), order.ID, "order.cancelled", cancellationEvent(order, invoice, actor, reason), audit.CreatedAt); err != nil {
		return OrderCancellation{}, err
	}
	if err = tx.Commit(); err != nil {
		return OrderCancellation{}, err
	}
	return OrderCancellation{Order: order, Invoice: invoice}, nil
}

func readCancellationOrder(row *sql.Row) (Order, error) {
	var order Order
	var issuerRaw, currencyRaw, planRaw []byte
	err := row.Scan(&order.ID, &order.Number, &order.UserID, &order.PlanName, &order.Status, &order.TotalMinor, &order.Currency, &order.CreatedAt, &order.Hostname, &issuerRaw, &currencyRaw, &planRaw)
	if err != nil {
		return Order{}, err
	}
	if len(issuerRaw) > 0 && json.Unmarshal(issuerRaw, &order.Issuer) != nil {
		return Order{}, ErrCancellationState
	}
	order.CurrencyFormat, err = decodeCurrencySnapshot(currencyRaw, order.Currency)
	if err != nil {
		return Order{}, ErrCancellationState
	}
	if len(planRaw) > 0 && json.Unmarshal(planRaw, &order.PlanSnapshot) != nil {
		return Order{}, ErrCancellationState
	}
	return order, nil
}

func readCancellationInvoice(ctx context.Context, tx *sql.Tx, orderID string) (Invoice, error) {
	var invoice Invoice
	var issuerRaw, currencyRaw []byte
	err := tx.QueryRowContext(ctx, "SELECT id,number,user_id,order_id,status,amount_minor,currency,issued_at,due_at,paid_at,issuer_snapshot,currency_snapshot FROM invoices WHERE order_id=? FOR UPDATE", orderID).Scan(&invoice.ID, &invoice.Number, &invoice.UserID, &invoice.OrderID, &invoice.Status, &invoice.AmountMinor, &invoice.Currency, &invoice.IssuedAt, &invoice.DueAt, &invoice.PaidAt, &issuerRaw, &currencyRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return Invoice{}, ErrCancellationState
	}
	if err != nil {
		return Invoice{}, err
	}
	if len(issuerRaw) > 0 {
		if err = json.Unmarshal(issuerRaw, &invoice.Issuer); err != nil {
			return Invoice{}, ErrCancellationState
		}
	}
	invoice.CurrencyFormat, err = decodeCurrencySnapshot(currencyRaw, invoice.Currency)
	if err != nil {
		return Invoice{}, ErrCancellationState
	}
	return invoice, nil
}

func lockCancellationOutbox(ctx context.Context, tx *sql.Tx, order Order, invoice Invoice, replay bool) error {
	rows, err := tx.QueryContext(ctx, "SELECT event_type,payload,processed_at,attempts FROM outbox_events WHERE aggregate_id=? ORDER BY event_type,id FOR UPDATE", order.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	created, cancelled := false, false
	for rows.Next() {
		var eventType string
		var payload []byte
		var processedAt sql.NullTime
		var attempts int
		if err = rows.Scan(&eventType, &payload, &processedAt, &attempts); err != nil {
			return err
		}
		switch eventType {
		case "order.created":
			if created || processedAt.Valid || attempts != 0 || !validCancellationEventPayload(payload, order, invoice, false) {
				return ErrCancellationState
			}
			created = true
		case "order.cancelled":
			if !replay || cancelled || !validCancellationEventPayload(payload, order, invoice, true) {
				return ErrCancellationState
			}
			cancelled = true
		default:
			return ErrCancellationState
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if !created || (replay && !cancelled) {
		return ErrCancellationState
	}
	return nil
}

func cancellationUpdateResult(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrCancellationState
	}
	return nil
}
