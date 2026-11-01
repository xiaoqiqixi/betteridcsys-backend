package billing

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
)

const paymentExpiryGrace = 15 * time.Minute

func paymentPending(a PaymentAttempt) bool {
	return a.State == "pending" || a.State == "checkout" || a.State == "uncertain"
}
func expirePayment(a PaymentAttempt, now time.Time) PaymentAttempt {
	// A missing/invalid checkout or uncertain external outcome never expires.
	if a.State == "checkout" && a.Checkout != nil && !a.Checkout.ExpiresAt.IsZero() && !now.Before(a.Checkout.ExpiresAt.Add(paymentExpiryGrace)) {
		a.State, a.UpdatedAt = "expired", now.UTC().Truncate(time.Microsecond)
	}
	return a
}
func (m *Memory) expirePayments(invoiceID string) {
	now := time.Now()
	for id, a := range m.paymentAttempts {
		if a.Expectation.InvoiceID == invoiceID {
			m.paymentAttempts[id] = expirePayment(a, now)
		}
	}
}

// Caller holds the order and invoice locks, shared with reserve/cancel/settle.
func expirePaymentsSQL(ctx context.Context, tx *sql.Tx, invoiceID string) error {
	rows, err := tx.QueryContext(ctx, "SELECT "+paymentAttemptColumns+" FROM payment_attempts WHERE invoice_id=? AND state='checkout' FOR UPDATE", invoiceID)
	if err != nil {
		return err
	}
	var expired []PaymentAttempt
	for rows.Next() {
		a, err := readPaymentAttempt(rows)
		if err != nil {
			rows.Close()
			return err
		}
		a = expirePayment(a, time.Now())
		if a.State == "expired" {
			expired = append(expired, a)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, a := range expired {
		if _, err = tx.ExecContext(ctx, "UPDATE payment_attempts SET state='expired',updated_at=? WHERE id=?", a.UpdatedAt, a.ID); err != nil {
			return err
		}
	}
	return nil
}

type paymentResolutionStore interface {
	VoidPayment(context.Context, User, string, string) (PaymentAttempt, error)
}

func voidPaymentInput(actor User, id, reason string) (string, error) {
	if actor.Role != "admin" || actor.ID == "" {
		return "", ErrNotFound
	}
	reason, err := normalizeCancellationInput(actor, id, reason)
	if err != nil {
		return "", ErrPaymentInput
	}
	return reason, nil
}
func voidPaymentAudit(actor User, a PaymentAttempt, reason string) Audit {
	return Audit{ID: ID(), UserID: a.Expectation.AccountID, Action: "payment.voided", Target: a.ID, ActorEmail: actor.Email, Detail: reason, CreatedAt: a.UpdatedAt}
}
func (m *Memory) VoidPayment(ctx context.Context, actor User, id, reason string) (PaymentAttempt, error) {
	reason, err := voidPaymentInput(actor, id, reason)
	if err != nil {
		return PaymentAttempt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Re-read staff authority at the mutation boundary.
	current, ok := m.users[actor.ID]
	if !ok || current.Role != "admin" || current.Disabled {
		return PaymentAttempt{}, ErrNotFound
	}
	a, ok := m.paymentAttempts[id]
	if !ok {
		return PaymentAttempt{}, ErrNotFound
	}
	if !paymentPending(a) {
		return PaymentAttempt{}, ErrPaymentState
	}
	a.State, a.UpdatedAt = "voided", time.Now().UTC().Truncate(time.Microsecond)
	m.paymentAttempts[id] = a
	m.audits = append(m.audits, voidPaymentAudit(current, a, reason))
	return clonePaymentAttempt(a), nil
}
func (m *MySQL) VoidPayment(ctx context.Context, actor User, id, reason string) (PaymentAttempt, error) {
	reason, err := voidPaymentInput(actor, id, reason)
	if err != nil {
		return PaymentAttempt{}, err
	}
	return retryPayment(ctx, func() (PaymentAttempt, error) {
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return PaymentAttempt{}, err
		}
		defer tx.Rollback()
		current, err := lockActiveAdministrator(ctx, tx, actor.ID)
		if err != nil {
			return PaymentAttempt{}, err
		}
		a, err := readPaymentAttempt(tx.QueryRowContext(ctx, "SELECT "+paymentAttemptColumns+" FROM payment_attempts WHERE id=?", id))
		if err != nil {
			return PaymentAttempt{}, err
		}
		if _, _, err = lockPaymentPair(ctx, tx, current, a.Expectation.InvoiceID, false); err != nil {
			return PaymentAttempt{}, err
		}
		a, err = readPaymentAttempt(tx.QueryRowContext(ctx, "SELECT "+paymentAttemptColumns+" FROM payment_attempts WHERE id=? FOR UPDATE", id))
		if err != nil {
			return PaymentAttempt{}, err
		}
		if !paymentPending(a) {
			return PaymentAttempt{}, ErrPaymentState
		}
		a.State, a.UpdatedAt = "voided", time.Now().UTC().Truncate(time.Microsecond)
		if _, err = tx.ExecContext(ctx, "UPDATE payment_attempts SET state='voided',updated_at=? WHERE id=?", a.UpdatedAt, id); err != nil {
			return PaymentAttempt{}, err
		}
		audit := voidPaymentAudit(current, a, reason)
		if _, err = tx.ExecContext(ctx, "INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) VALUES(?,?,?,?,?,?,?)", audit.ID, audit.UserID, audit.Action, audit.Target, audit.CreatedAt, audit.ActorEmail, audit.Detail); err != nil {
			return PaymentAttempt{}, err
		}
		if err = tx.Commit(); err != nil {
			return PaymentAttempt{}, err
		}
		return a, nil
	})
}
func (a *App) voidPaymentHTTP(w http.ResponseWriter, r *http.Request, actor User, id string) {
	if r.Method != "POST" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "不支持此请求方式")
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if !financeBody(w, r, &input) {
		return
	}
	if _, err := voidPaymentInput(actor, id, input.Reason); err != nil {
		fail(w, r, 400, "INVALID_PAYMENT_VOID", "请填写 1–500 字节的纯文本作废原因")
		return
	}
	store, ok := a.Store.(paymentResolutionStore)
	if !ok {
		a.internal(w, r, errors.New("payment resolution unavailable"))
		return
	}
	out, err := store.VoidPayment(r.Context(), actor, id, input.Reason)
	if err != nil {
		a.paymentStoreError(w, r, err)
		return
	}
	// No checkout URL or provider credentials in the administrative response.
	jsonOut(w, 200, safeOrderAttempt(out))
}

func (a *App) reserveCheckout(ctx context.Context, actor User, invoiceID, key, provider, merchant string) (PaymentAttempt, bool, error) {
	legacy := key == ""
	if legacy {
		key = "invoice:" + invoiceID
	}
	if !keyPattern.MatchString(key) {
		return PaymentAttempt{}, false, ErrPaymentInput
	}
	for {
		if err := ctx.Err(); err != nil {
			return PaymentAttempt{}, false, err
		}
		attempt, replay, err := a.Store.ReservePayment(ctx, actor, invoiceID, key, provider, merchant)
		if err != nil || !replay || !legacy || (attempt.State != "expired" && attempt.State != "voided") {
			return attempt, replay, err
		}
		// Legacy clients have no explicit key. Each retired attempt deterministically
		// names its successor, retaining stable replay after lost HTTP/commit replies.
		key = "retry:" + attempt.ID
	}
}

func paymentVoidPath(p string) (string, bool) {
	const prefix = "/admin/payment-attempts/"
	if !strings.HasPrefix(p, prefix) || !strings.HasSuffix(p, "/void") {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(p, prefix), "/void"), true
}
