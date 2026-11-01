package billing

import (
	api "aster/billing/internal/financeapi"
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type paymentRefundRuntime interface {
	RefundPayment(context.Context, paymentv1.Config, paymentv1.Refund) (paymentv1.RefundResult, error)
}
type refundReservation06 struct {
	ID         string           `json:"id"`
	Request    paymentv1.Refund `json:"request"`
	TopUpDebit bool             `json:"top_up_debit"`
}

// A committed reservation grants exactly one dispatch. Neither a retry nor an
// ambiguous response can dispatch again, even after a process restart.
func (m *MySQL) reserveOriginalRefund06(ctx context.Context, actor User, id string, input api.AdminRefundInput, key string, cfg paymentv1.Config) (refundReservation06, bool, error) {
	raw, _ := json.Marshal(input)
	fp := digest(id + ":" + string(raw))
	type result struct {
		reservation refundReservation06
		replay      bool
	}
	res, err := retryPayment(ctx, func() (result, error) {
		var out result
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		if err = financialRequestLock06(ctx, tx, actor.ID, "invoice:refund", key, fp); err != nil {
			return out, err
		}
		if _, err = lockActiveAdministrator(ctx, tx, actor.ID); err != nil {
			return out, err
		}
		record, err := lockInvoice06(ctx, tx, "", id, false)
		if err != nil {
			return out, err
		}
		if out.replay, err = requestReplay06(ctx, tx, actor.ID, "invoice:refund:reserve", key, fp, &out.reservation); err != nil || out.replay {
			return out, err
		}
		p, err := refundable06(ctx, tx, id, input.PaymentID, input.AmountMinor)
		if err != nil {
			return out, err
		}
		if p.method != "gateway" || p.attempt == nil || p.gateway == nil || *p.gateway != settlementGateway(cfg) {
			return out, ErrCommerceUnavailable
		}
		if !p.unallocated && record.Invoice.Status != "paid" && record.Invoice.Status != "partially_refunded" {
			return out, ErrPaymentState
		}
		var transaction, merchant string
		if err = tx.QueryRowContext(ctx, "SELECT s.transaction_id,a.merchant_account_id FROM payment_attempts a JOIN payment_settlements s ON s.attempt_id=a.id WHERE a.id=?", *p.attempt).Scan(&transaction, &merchant); err != nil {
			return out, err
		}
		if merchant != cfg.MerchantID {
			return out, ErrCommerceUnavailable
		}
		out.reservation = refundReservation06{ID: ID(), Request: paymentv1.Refund{TransactionID: transaction, AmountMinor: input.AmountMinor, Currency: record.Invoice.Total.Currency, CurrencyDecimalPlaces: int(record.Invoice.Total.DecimalPlaces)}}
		out.reservation.Request.IdempotencyKey = out.reservation.ID
		if !p.unallocated && record.Invoice.Kind == "top_up" {
			if _, err = appendCredit06(ctx, tx, record.Invoice.ClientID, "refund", -input.AmountMinor, &id, "Reserve original refund of credit top-up", time.Now().UTC()); err != nil {
				return out, err
			}
			out.reservation.TopUpDebit = true
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO invoice_refunds(id,invoice_id,payment_id,amount_minor,destination,status,reason,created_at,updated_at) VALUES(?,?,?,?,'original','pending',?,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))", out.reservation.ID, id, input.PaymentID, input.AmountMinor, input.Reason); err != nil {
			return out, err
		}
		if err = recordRequest06(ctx, tx, actor.ID, "invoice:refund:reserve", key, fp, out.reservation); err != nil {
			return out, err
		}
		return out, tx.Commit()
	})
	return res.reservation, res.replay, err
}

func (m *MySQL) refundReplay06(ctx context.Context, actor, id string, input api.AdminRefundInput, key string) (api.AdminInvoice, bool, error) {
	var out api.AdminInvoice
	raw, _ := json.Marshal(input)
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, false, err
	}
	defer tx.Rollback()
	ok, err := requestReplay06(ctx, tx, actor, "invoice:refund", key, digest(id+":"+string(raw)), &out)
	return out, ok, err
}

func (m *MySQL) finishOriginalRefund06(ctx context.Context, actor User, id string, input api.AdminRefundInput, key string, reservation refundReservation06, result paymentv1.RefundResult, callErr error) (api.AdminInvoice, error) {
	raw, _ := json.Marshal(input)
	fp := digest(id + ":" + string(raw))
	out, err := retryPayment(ctx, func() (api.AdminInvoice, error) {
		var out api.AdminInvoice
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		record, err := lockInvoice06(ctx, tx, "", id, false)
		if err != nil {
			return out, err
		}
		var status string
		if err = tx.QueryRowContext(ctx, "SELECT status FROM invoice_refunds WHERE id=? AND invoice_id=? FOR UPDATE", reservation.ID, id).Scan(&status); err != nil {
			return out, err
		}
		if status != "pending" {
			return out, ErrPaymentUncertain
		}
		status = "uncertain"
		var providerRef *string
		if callErr == nil && paymentv1.ValidateRefundResult(result) == nil {
			status, providerRef = result.Status, &result.RefundID
		}
		if status == "succeeded" {
			// Exclude our own reservation from the outstanding refund balance.
			if _, err = tx.ExecContext(ctx, "UPDATE invoice_refunds SET status='succeeded' WHERE id=?", reservation.ID); err != nil {
				return out, err
			}
			payment, err := refundable06(ctx, tx, id, input.PaymentID, input.AmountMinor)
			if err != nil {
				return out, err
			}
			out, err = recordRefund06(ctx, tx, record, payment, reservation.ID, input.AmountMinor, true)
			if err != nil {
				return out, err
			}
			if err = recordRequest06(ctx, tx, actor.ID, "invoice:refund", key, fp, out); err != nil {
				return out, err
			}
		} else if status == "failed" && reservation.TopUpDebit {
			if _, err = appendCredit06(ctx, tx, record.Invoice.ClientID, "refund", input.AmountMinor, &id, "Release failed original refund reservation", time.Now().UTC()); err != nil {
				return out, err
			}
		}
		if _, err = tx.ExecContext(ctx, "UPDATE invoice_refunds SET status=?,provider_reference=?,updated_at=UTC_TIMESTAMP(6) WHERE id=?", status, providerRef, reservation.ID); err != nil {
			return out, err
		}
		if err = insertCurrencyAudit(ctx, tx, catalogAudit(actor, "invoice.refund."+status, id, 0, input.Reason)); err != nil {
			return out, err
		}
		return out, tx.Commit()
	})
	if err != nil {
		return out, err
	}
	if out.ID != "" {
		return out, nil
	}
	if callErr == nil && result.Status == "failed" {
		return out, &apiProblem{409, "REFUND_FAILED", "The gateway rejected this refund"}
	}
	return out, ErrPaymentUncertain
}

func (a *App) refundOriginal06(ctx context.Context, actor User, id string, input api.AdminRefundInput, key string) (api.AdminInvoice, error) {
	m := a.Store.(*MySQL)
	if out, replay, err := m.refundReplay06(ctx, actor.ID, id, input, key); err != nil || replay {
		return out, err
	}
	runtime, ok := a.Config.PaymentRuntime.(paymentRefundRuntime)
	if !ok {
		return api.AdminInvoice{}, &apiProblem{501, "FEATURE_UNAVAILABLE", "This gateway does not support refunds"}
	}
	// The attempt identifies the historical gateway even before legacy payments
	// have been projected into invoice_payments.
	var gateway string
	err := m.DB.QueryRowContext(ctx, `SELECT a.provider_id FROM payment_attempts a JOIN payment_settlements s ON s.attempt_id=a.id WHERE a.invoice_id=? AND a.id=?`, id, input.PaymentID).Scan(&gateway)
	if errors.Is(err, sql.ErrNoRows) {
		return api.AdminInvoice{}, &apiProblem{501, "FEATURE_UNAVAILABLE", "This payment has no original gateway"}
	}
	if err != nil {
		return api.AdminInvoice{}, err
	}
	manifest, cfg, err := a.paymentSelectionFor(gateway)
	if err != nil || !manifest.HasCapability(pluginapi.PaymentRefundCapability) {
		return api.AdminInvoice{}, &apiProblem{501, "FEATURE_UNAVAILABLE", "This gateway does not support refunds"}
	}
	reservation, replay, err := m.reserveOriginalRefund06(ctx, actor, id, input, key, cfg)
	if err != nil {
		return api.AdminInvoice{}, err
	}
	if replay {
		return api.AdminInvoice{}, ErrPaymentUncertain
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result := paymentv1.RefundResult{}
	if err = paymentv1.ValidateRefund(reservation.Request, *manifest.Payment); err == nil {
		result, err = runtime.RefundPayment(callCtx, cfg, reservation.Request)
	} else {
		// Validation proves no dispatch occurred, so a held top-up can be released.
		result, err = paymentv1.RefundResult{RefundID: reservation.ID, Status: "failed"}, nil
	}
	recordCtx, recordCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer recordCancel()
	return m.finishOriginalRefund06(recordCtx, actor, id, input, key, reservation, result, err)
}
