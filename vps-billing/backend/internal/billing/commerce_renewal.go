package billing

import (
	"aster/billing/internal/commerce"
	api "aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func serviceInvoicePayable06(ctx context.Context, tx *sql.Tx, v api.AdminInvoice) (bool, error) {
	if v.Kind != "renewal" && v.Kind != "upgrade" {
		return true, nil
	}
	var sid, kind string
	var start sql.NullTime
	err := tx.QueryRowContext(ctx, "SELECT service_id,kind,period_start FROM invoice_service_changes WHERE invoice_id=?", v.ID).Scan(&sid, &kind, &start)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s, err := readService06(ctx, tx, v.ClientID, sid, " FOR UPDATE")
	if err != nil {
		return false, err
	}
	if (s.View.Status != "active" && s.View.Status != "suspended") || s.View.Cancellation != nil {
		return false, nil
	}
	if kind == "upgrade" && (s.View.Status != "active" || !s.Due.After(time.Now().UTC())) {
		return false, nil
	}
	if kind == "renewal" && (!start.Valid || !s.Due.Equal(start.Time)) {
		return false, nil
	}
	var terminating int
	err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM service_tasks WHERE service_id=? AND action='terminate' AND (status IN ('running','uncertain') OR (status='queued' AND (COALESCE(JSON_UNQUOTE(JSON_EXTRACT(payload,'$.source')),'')<>'automation' OR COALESCE(JSON_UNQUOTE(JSON_EXTRACT(payload,'$.reason')),'')<>'overdue')))", sid).Scan(&terminating)
	return terminating == 0, err
}

func (m *MySQL) RenewService06(ctx context.Context, actor User, id, cycle, key string, at time.Time) (api.Invoice, error) {
	return m.renewService06(ctx, actor, id, cycle, key, at, false)
}
func (m *MySQL) renewService06(ctx context.Context, actor User, id, cycle, key string, at time.Time, automatic bool) (api.Invoice, error) {
	if !keyPattern.MatchString(key) {
		return api.Invoice{}, ErrCommerceInput
	}
	months, err := commerce.CycleMonths(cycle)
	if err != nil {
		return api.Invoice{}, ErrCommerceInput
	}
	fp := digest(id + ":" + cycle)
	return retryPayment(ctx, func() (api.Invoice, error) {
		var out api.Invoice
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		if _, _, err = readCurrencies(ctx, tx, " FOR UPDATE"); err != nil {
			return out, err
		}
		if err = lockServiceOwner06(ctx, tx, actor.ID, id); err != nil {
			return out, err
		}
		if replay, err := requestReplay06(ctx, tx, actor.ID, "service:renew", key, fp, &out); err != nil || replay {
			return out, err
		}
		s, err := readService06(ctx, tx, actor.ID, id, " FOR UPDATE")
		if err != nil {
			return out, err
		}
		if (s.View.Status != "active" && s.View.Status != "suspended") || s.View.Cancellation != nil {
			return out, serviceConflict06()
		}
		settings, err := readAutomation(ctx, tx, "")
		if err != nil {
			return out, err
		}
		if automatic && (!s.View.AutoRenew || s.Due.After(at.Add(time.Duration(settings.InvoiceDaysBeforeDue)*24*time.Hour))) {
			return out, serviceConflict06()
		}
		// Renewal uses the agreed recurring snapshot, including recurring coupons
		// and per-unit rounding. Only cycles offered on the service are accepted.
		if cycle != s.View.Cycle {
			return out, ErrCommerceInput
		}
		var pending int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM service_tasks WHERE service_id=? AND ((action='resize' AND status IN ('queued','running','uncertain')) OR (action='terminate' AND (status IN ('running','uncertain') OR (status='queued' AND (COALESCE(JSON_UNQUOTE(JSON_EXTRACT(payload,'$.source')),'')<>'automation' OR COALESCE(JSON_UNQUOTE(JSON_EXTRACT(payload,'$.reason')),'')<>'overdue')))))", id).Scan(&pending); err != nil {
			return out, err
		}
		if pending != 0 {
			return out, serviceConflict06()
		}
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM invoice_service_changes c JOIN invoices i ON i.id=c.invoice_id WHERE c.service_id=? AND c.kind='upgrade' AND i.status='unpaid'", id).Scan(&pending); err != nil {
			return out, err
		}
		if pending != 0 {
			return out, serviceConflict06()
		}
		var existing string
		err = tx.QueryRowContext(ctx, "SELECT invoice_id FROM invoice_service_changes WHERE service_id=? AND kind='renewal' AND period_start=? FOR UPDATE", id, s.Due).Scan(&existing)
		if err == nil {
			record, err := readInvoice06(ctx, tx, existing, actor.ID, " FOR UPDATE")
			if err != nil {
				return out, err
			}
			if record.Invoice.Status != "unpaid" {
				return out, serviceConflict06()
			}
			out = customerInvoice06(record.Invoice)
		} else if errors.Is(err, sql.ErrNoRows) {
			end := commerce.CalendarMonths(s.Due, months, s.Anchor)
			period := s.Due.UTC().Format("2006-01-02") + " – " + end.UTC().Format("2006-01-02")
			invoice, err := newInvoice06(ctx, tx, actor.ID, "renewal", nil, []api.InvoiceLine{{ServiceID: &id, Description: s.View.ProductName + " renewal", Amount: s.View.Recurring, Period: &period}}, s.Due, at)
			if err != nil {
				return out, err
			}
			payload, _ := json.Marshal(map[string]any{"cycle": cycle, "anchor_day": s.Anchor})
			if _, err = tx.ExecContext(ctx, "INSERT INTO invoice_service_changes(invoice_id,service_id,kind,period_start,period_end,payload) VALUES(?,?,'renewal',?,?,?)", invoice.ID, id, s.Due, end, payload); err != nil {
				return out, err
			}
			if invoice.Total.AmountMinor == 0 {
				invoice, err = completeInvoice06(ctx, tx, invoiceRecord06{Invoice: invoice, Version: 1}, at)
			} else if settings.AutoApplyCredit {
				invoice, err = applyInvoiceCredit06(ctx, tx, invoiceRecord06{Invoice: invoice, Version: 1}, false)
			}
			if err != nil {
				return out, err
			}
			out = customerInvoice06(invoice)
		} else {
			return out, err
		}
		if err = recordRequest06(ctx, tx, actor.ID, "service:renew", key, fp, out); err != nil {
			return out, err
		}
		if err = insertAccountAudit(ctx, tx, actor.ID, actor.ID, "service.renewal_invoiced", id); err != nil {
			return out, err
		}
		return out, tx.Commit()
	})
}

func fulfillRenewal06(ctx context.Context, tx *sql.Tx, v api.AdminInvoice, paid time.Time) error {
	var sid string
	var start, end time.Time
	if err := tx.QueryRowContext(ctx, "SELECT service_id,period_start,period_end FROM invoice_service_changes WHERE invoice_id=? AND kind='renewal'", v.ID).Scan(&sid, &start, &end); err != nil {
		return err
	}
	s, err := readService06(ctx, tx, v.ClientID, sid, " FOR UPDATE")
	if err != nil {
		return err
	}
	if !s.Due.Equal(start) || end.Before(start) {
		return serviceConflict06()
	}
	if _, err = tx.ExecContext(ctx, "UPDATE services SET next_due_at=?,next_invoice_at=?,revision=revision+1 WHERE id=?", end, end, sid); err != nil {
		return err
	}
	// A queued overdue suspension has not reached the controller and can safely
	// be cancelled. An executing operation must finish or be looked up first.
	if end.After(time.Now().UTC()) {
		if _, err = tx.ExecContext(ctx, "UPDATE service_tasks SET status='failed',message='Overdue balance paid before dispatch',updated_at=?,finished_at=? WHERE service_id=? AND action IN ('suspend','terminate') AND status='queued' AND JSON_UNQUOTE(JSON_EXTRACT(payload,'$.source'))='automation' AND JSON_UNQUOTE(JSON_EXTRACT(payload,'$.reason'))='overdue'", paid, paid, sid); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE services SET terminates_at=NULL WHERE id=? AND cancellation_type IS NULL AND (status='active' OR suspension_reason='overdue')", sid); err != nil {
			return err
		}
		if s.View.Status == "suspended" && s.View.SuspensionReason != nil && *s.View.SuspensionReason == "overdue" {
			var pending int
			if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM service_tasks WHERE service_id=? AND action='unsuspend' AND status IN ('queued','running','uncertain')", sid).Scan(&pending); err != nil {
				return err
			}
			if pending == 0 {
				_, err = queueServiceTask06(ctx, tx, sid, "unsuspend", map[string]string{"source": "payment", "reason": "overdue"}, paid)
			}
		}
	}
	return err
}
