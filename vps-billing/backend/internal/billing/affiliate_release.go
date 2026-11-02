package billing

import (
	"aster/billing/internal/commerce"
	"aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"time"
)

// Refunds use their successful completion time and the original clawback policy.
// Cumulative rounding avoids losing minor units across repeated partial refunds.
func affiliateRefundAmount(original, cash, refunded int64) (int64, error) {
	if cash <= 0 {
		return 0, nil
	}
	return commerce.FloorRatio(original, min(cash, refunded), cash)
}
func (a *App) releaseAffiliate(ctx context.Context, _ financeapi.AutomationSettings, now time.Time) (AutomationResult, error) {
	var result AutomationResult
	m, err := a.accountDB()
	if err != nil {
		return result, err
	}
	// Only unapplied successful cash refunds or matured holds are candidates.
	rows, err := m.DB.QueryContext(ctx, `SELECT DISTINCT c.id,c.affiliate_id FROM affiliate_commissions c JOIN affiliate_commission_rules s ON s.commission_id=c.id WHERE (c.status='pending' AND s.hold_until<=?) OR EXISTS(SELECT 1 FROM invoice_refunds r JOIN invoice_payments p ON p.id=r.payment_id WHERE r.invoice_id=c.invoice_id AND r.status='succeeded' AND p.method IN ('gateway','manual','bank_transfer','cash','other') AND NOT EXISTS(SELECT 1 FROM payment_settlements ps WHERE ps.attempt_id=p.attempt_id AND ps.disposition<>'settled') AND (s.clawback='reverse_all' OR r.updated_at<s.hold_until) AND NOT EXISTS(SELECT 1 FROM affiliate_adjustments a WHERE a.refund_id=r.id)) ORDER BY c.id LIMIT 200`, now.UTC())
	if err != nil {
		return result, err
	}
	type item struct{ id, uid string }
	items := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.id, &v.uid); err != nil {
			break
		}
		items = append(items, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return result, err
	}
	for _, v := range items {
		changed, e := retryPayment(ctx, func() (bool, error) {
			tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
			if err != nil {
				return false, err
			}
			defer tx.Rollback()
			if _, err = lockAccountUser(ctx, tx, v.uid); err != nil {
				return false, err
			}
			var invoice, state, clawback string
			var amount, cash int64
			var hold time.Time
			if err = tx.QueryRowContext(ctx, `SELECT c.invoice_id,c.status,c.amount_minor,s.cash_minor,s.hold_until,s.clawback FROM affiliate_commissions c JOIN affiliate_commission_rules s ON s.commission_id=c.id WHERE c.id=? FOR UPDATE`, v.id).Scan(&invoice, &state, &amount, &cash, &hold, &clawback); err != nil {
				return false, err
			}
			rr, err := tx.QueryContext(ctx, `SELECT r.id,r.amount_minor,r.updated_at,EXISTS(SELECT 1 FROM affiliate_adjustments a WHERE a.refund_id=r.id) FROM invoice_refunds r JOIN invoice_payments p ON p.id=r.payment_id WHERE r.invoice_id=? AND r.status='succeeded' AND p.method IN ('gateway','manual','bank_transfer','cash','other') AND NOT EXISTS(SELECT 1 FROM payment_settlements ps WHERE ps.attempt_id=p.attempt_id AND ps.disposition<>'settled') ORDER BY r.updated_at,r.id`, invoice)
			if err != nil {
				return false, err
			}
			type refund struct {
				id      string
				amount  int64
				at      time.Time
				applied bool
			}
			refunds := []refund{}
			for rr.Next() {
				var r refund
				if err = rr.Scan(&r.id, &r.amount, &r.at, &r.applied); err != nil {
					break
				}
				refunds = append(refunds, r)
			}
			if err == nil {
				err = rr.Err()
			}
			rr.Close()
			if err != nil {
				return false, err
			}
			var cumulative, previous int64
			changed := false
			// Total commission is the sum of the original per-line rounded snapshots.
			for _, r := range refunds {
				if clawback == "reverse_pending" && !r.at.Before(hold) {
					continue
				}
				cumulative, err = commerce.Add(cumulative, r.amount)
				if err != nil {
					return false, err
				}
				target, err := affiliateRefundAmount(amount, cash, cumulative)
				if err != nil {
					return false, err
				}
				delta := target - previous
				previous = target
				if r.applied {
					continue
				}
				if _, err = tx.ExecContext(ctx, "INSERT INTO affiliate_adjustments(id,commission_id,refund_id,amount_minor,created_at) VALUES(?,?,?,?,?)", ID(), v.id, r.id, -delta, now.UTC()); err != nil {
					return false, err
				}
				if err = insertAccountAudit(ctx, tx, v.uid, v.uid, "affiliate.reversed", r.id); err != nil {
					return false, err
				}
				changed = true
			}
			if state == "pending" && !hold.After(now) {
				state = "available"
				if previous >= amount {
					state = "cancelled"
				}
				if _, err = tx.ExecContext(ctx, "UPDATE affiliate_commissions SET status=?,released_at=? WHERE id=?", state, now.UTC(), v.id); err != nil {
					return false, err
				}
				if err = insertAccountAudit(ctx, tx, v.uid, v.uid, "affiliate.released", v.id); err != nil {
					return false, err
				}
				changed = true
			}
			return changed, tx.Commit()
		})
		if e != nil {
			result.Failed++
		} else if changed {
			result.Processed++
		}
	}
	return result, nil
}
