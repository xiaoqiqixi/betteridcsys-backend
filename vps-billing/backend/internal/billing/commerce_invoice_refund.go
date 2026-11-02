package billing

import (
	api "aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func (m *MySQL) CancelInvoice06(ctx context.Context, actor User, id, reason string) (api.AdminInvoice, error) {
	if actor.Role != "admin" {
		return api.AdminInvoice{}, ErrAdminRequired
	}
	if !accountText(reason, 500) || len([]rune(reason)) < 3 {
		return api.AdminInvoice{}, ErrCommerceInput
	}
	var orderID *string
	if err := m.DB.QueryRowContext(ctx, "SELECT order_id FROM invoices WHERE id=?", id).Scan(&orderID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrNotFound
		}
		return api.AdminInvoice{}, err
	}
	if orderID != nil {
		if _, err := m.CancelOrder06(ctx, actor, *orderID, reason); err != nil {
			return api.AdminInvoice{}, err
		}
		return m.Invoice06(ctx, "", id)
	}
	return retryPayment(ctx, func() (api.AdminInvoice, error) {
		var out api.AdminInvoice
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		actor, err = lockActiveAdministrator(ctx, tx, actor.ID)
		if err != nil {
			return out, err
		}
		record, err := lockInvoice06(ctx, tx, "", id, false)
		if err != nil {
			return out, err
		}
		out = record.Invoice
		if out.Status == "cancelled" {
			return out, nil
		}
		if out.Status != "unpaid" || out.AmountPaid.AmountMinor != 0 {
			return out, ErrPaymentState
		}
		if out.OrderID != nil {
			return out, ErrCommerceUnavailable
		}
		if err = invoiceAttemptBarrier06(ctx, tx, id, false); err != nil {
			return out, err
		}
		if err = releaseUpgradeStock06(ctx, tx, out); err != nil {
			return out, err
		}
		if out.CreditApplied.AmountMinor > 0 {
			if _, err = appendCredit06(ctx, tx, out.ClientID, "refund", out.CreditApplied.AmountMinor, &id, "Credit released from cancelled invoice", time.Now().UTC()); err != nil {
				return out, err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE invoice_payments SET status='refunded',refunded_minor=amount_minor WHERE invoice_id=? AND method='credit'", id); err != nil {
				return out, err
			}
		}
		if _, err = tx.ExecContext(ctx, "UPDATE invoices SET status='cancelled',credit_applied_minor=0 WHERE id=?", id); err != nil {
			return out, err
		}
		if err = insertCurrencyAudit(ctx, tx, catalogAudit(actor, "invoice.cancelled", id, 0, reason)); err != nil {
			return out, err
		}
		record, err = readInvoice06(ctx, tx, id, "", "")
		if err != nil {
			return out, err
		}
		return record.Invoice, tx.Commit()
	})
}

type refundablePayment06 struct {
	id, method, status string
	gateway, attempt   *string
	amount, refunded   int64
	unallocated        bool
}

func refundable06(ctx context.Context, tx *sql.Tx, invoiceID, paymentID string, amount int64) (refundablePayment06, error) {
	var out refundablePayment06
	// Legacy receipts remain immutable; materialize their payment projection only
	// when a financial mutation needs it. The owning customer/invoice are locked.
	if _, err := tx.ExecContext(ctx, `INSERT INTO invoice_payments(id,invoice_id,attempt_id,method,gateway,reference,amount_minor,refunded_minor,status,created_at)
SELECT a.id,a.invoice_id,a.id,'gateway',a.provider_id,s.transaction_id,a.amount_minor,0,IF(s.disposition='settled','succeeded','duplicate'),s.settled_at
FROM payment_attempts a JOIN payment_settlements s ON s.attempt_id=a.id
WHERE a.invoice_id=? AND a.id=? AND NOT EXISTS(SELECT 1 FROM invoice_payments p WHERE p.id=a.id)`, invoiceID, paymentID); err != nil {
		return out, err
	}
	var disposition string
	err := tx.QueryRowContext(ctx, `SELECT p.id,p.method,p.status,p.gateway,p.attempt_id,p.amount_minor,p.refunded_minor,COALESCE(s.disposition,'settled') FROM invoice_payments p LEFT JOIN payment_settlements s ON s.attempt_id=p.attempt_id WHERE p.invoice_id=? AND p.id=? FOR UPDATE`, invoiceID, paymentID).Scan(&out.id, &out.method, &out.status, &out.gateway, &out.attempt, &out.amount, &out.refunded, &disposition)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	out.unallocated = disposition != "settled"
	var reserved int64
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(amount_minor),0) FROM invoice_refunds WHERE payment_id=? AND status IN ('pending','uncertain')", paymentID).Scan(&reserved); err != nil {
		return out, err
	}
	if amount <= 0 || amount > out.amount-out.refunded-reserved {
		return out, ErrPaymentState
	}
	return out, nil
}
func recordRefund06(ctx context.Context, tx *sql.Tx, record invoiceRecord06, payment refundablePayment06, refundID string, amount int64, original bool) (api.AdminInvoice, error) {
	v := record.Invoice
	if !payment.unallocated && v.Status != "paid" && v.Status != "partially_refunded" {
		return v, ErrPaymentState
	}
	if !payment.unallocated && v.Kind == "top_up" && !original {
		if _, err := appendCredit06(ctx, tx, v.ClientID, "refund", -amount, &v.ID, "Reverse refunded credit top-up", time.Now().UTC()); err != nil {
			return v, err
		}
	}
	if !original {
		if _, err := appendCredit06(ctx, tx, v.ClientID, "refund", amount, &v.ID, "Invoice payment refunded to credit", time.Now().UTC()); err != nil {
			return v, err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE invoice_payments SET refunded_minor=refunded_minor+?,status='refunded' WHERE id=?", amount, payment.id); err != nil {
		return v, err
	}
	if !payment.unallocated {
		refunded := record.Refunded + amount
		status := "partially_refunded"
		if refunded == v.Total.AmountMinor {
			status = "refunded"
		}
		if _, err := tx.ExecContext(ctx, "UPDATE invoices SET refunded_minor=?,status=? WHERE id=?", refunded, status, v.ID); err != nil {
			return v, err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE invoice_refunds SET status='succeeded',updated_at=UTC_TIMESTAMP(6) WHERE id=?", refundID); err != nil {
		return v, err
	}
	next, err := readInvoice06(ctx, tx, v.ID, "", "")
	return next.Invoice, err
}
func (m *MySQL) RefundCredit06(ctx context.Context, actor User, id string, input api.AdminRefundInput, key string) (api.AdminInvoice, error) {
	raw, _ := json.Marshal(input)
	if input.Destination != "credit" || api.ValidateInput("adminRefund", raw) != nil || !keyPattern.MatchString(key) {
		return api.AdminInvoice{}, ErrCommerceInput
	}
	fp := digest(id + ":" + string(raw))
	return retryPayment(ctx, func() (api.AdminInvoice, error) {
		var out api.AdminInvoice
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		if err = financialRequestLock06(ctx, tx, actor.ID, "invoice:refund", key, fp); err != nil {
			return out, err
		}
		actor, err = lockActiveAdministrator(ctx, tx, actor.ID)
		if err != nil {
			return out, err
		}
		record, err := lockInvoice06(ctx, tx, "", id, false)
		if err != nil {
			return out, err
		}
		replay, err := requestReplay06(ctx, tx, actor.ID, "invoice:refund", key, fp, &out)
		if err != nil || replay {
			return out, err
		}
		var pending refundReservation06
		if replay, err = requestReplay06(ctx, tx, actor.ID, "invoice:refund:reserve", key, fp, &pending); err != nil || replay {
			if err == nil {
				err = ErrPaymentUncertain
			}
			return out, err
		}
		payment, err := refundable06(ctx, tx, id, input.PaymentID, input.AmountMinor)
		if err != nil {
			return out, err
		}
		refundID := ID()
		if _, err = tx.ExecContext(ctx, "INSERT INTO invoice_refunds(id,invoice_id,payment_id,amount_minor,destination,status,reason,created_at,updated_at) VALUES(?,?,?,?,'credit','pending',?,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))", refundID, id, payment.id, input.AmountMinor, input.Reason); err != nil {
			return out, err
		}
		out, err = recordRefund06(ctx, tx, record, payment, refundID, input.AmountMinor, false)
		if err != nil {
			return out, err
		}
		if err = insertCurrencyAudit(ctx, tx, catalogAudit(actor, "invoice.refunded", id, 0, input.Reason)); err != nil {
			return out, err
		}
		if err = recordRequest06(ctx, tx, actor.ID, "invoice:refund", key, fp, out); err != nil {
			return out, err
		}
		return out, tx.Commit()
	})
}
