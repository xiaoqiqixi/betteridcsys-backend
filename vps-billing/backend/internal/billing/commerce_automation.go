package billing

import (
	api "aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"fmt"
	"time"
)

type automationTarget06 struct{ ID, Owner, Cycle string }

func automationTargets06(ctx context.Context, m *MySQL, query string, args ...any) ([]automationTarget06, error) {
	rows, err := m.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []automationTarget06{}
	for rows.Next() {
		var t automationTarget06
		if err = rows.Scan(&t.ID, &t.Owner, &t.Cycle); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func eachAutomationTarget06(ctx context.Context, targets []automationTarget06, fn func(automationTarget06) (bool, error)) (AutomationResult, error) {
	result := AutomationResult{}
	for _, target := range targets {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		processed, err := fn(target)
		if err != nil {
			result.Failed++
		} else if processed {
			result.Processed++
		}
	}
	return result, nil
}
func (a *App) registerBillingAutomation06() {
	a.registerServiceDispatch06()
	_ = a.RegisterAutomationJob("generate_renewals", func(ctx context.Context, s api.AutomationSettings, at time.Time) (AutomationResult, error) {
		m, ok := a.Store.(*MySQL)
		if !ok {
			return AutomationResult{}, ErrCommerceUnavailable
		}
		targets, err := automationTargets06(ctx, m, `SELECT s.id,s.user_id,s.cycle FROM services s WHERE s.status IN ('active','suspended') AND s.cycle<>'one_time' AND s.auto_renew=TRUE AND s.cancellation_type IS NULL AND s.next_invoice_at<=? AND NOT EXISTS(SELECT 1 FROM invoice_service_changes c WHERE c.service_id=s.id AND c.kind='renewal' AND c.period_start=s.next_due_at) ORDER BY s.next_invoice_at,s.id LIMIT 200`, at.Add(time.Duration(s.InvoiceDaysBeforeDue)*24*time.Hour))
		if err != nil {
			return AutomationResult{}, err
		}
		return eachAutomationTarget06(ctx, targets, func(t automationTarget06) (bool, error) {
			_, err := m.renewService06(ctx, User{ID: t.Owner}, t.ID, t.Cycle, "automation:"+t.ID+":"+at.UTC().Format("200601021504"), at, true)
			return err == nil, err
		})
	})
	_ = a.RegisterAutomationJob("apply_credit", func(ctx context.Context, s api.AutomationSettings, at time.Time) (AutomationResult, error) {
		if !s.AutoApplyCredit {
			return AutomationResult{}, nil
		}
		m, ok := a.Store.(*MySQL)
		if !ok {
			return AutomationResult{}, ErrCommerceUnavailable
		}
		targets, err := automationTargets06(ctx, m, `SELECT i.id,i.user_id,'' FROM invoices i WHERE i.commerce_version=1 AND i.status='unpaid' AND i.kind<>'top_up' AND i.amount_minor>i.amount_paid_minor+i.credit_applied_minor AND (SELECT l.balance_after_minor FROM credit_ledger l WHERE l.user_id=i.user_id ORDER BY l.sequence DESC LIMIT 1)>0 ORDER BY i.due_at,i.id LIMIT 200`)
		if err != nil {
			return AutomationResult{}, err
		}
		return eachAutomationTarget06(ctx, targets, func(t automationTarget06) (bool, error) { return m.autoCredit06(ctx, t.ID) })
	})
	_ = a.RegisterAutomationJob("send_reminders", func(ctx context.Context, s api.AutomationSettings, at time.Time) (AutomationResult, error) {
		m, ok := a.Store.(*MySQL)
		if !ok {
			return AutomationResult{}, ErrCommerceUnavailable
		}
		// The database filters delivered stages so old unpaid invoices do not
		// starve later invoices in the bounded batch.
		result := AutomationResult{}
		for _, overdue := range []bool{false, true} {
			days := s.ReminderDaysBeforeDue
			if overdue {
				days = s.OverdueReminderDays
			}
			for _, day := range days {
				stage := fmt.Sprintf("before:%d", day)
				key := "invoice_reminder"
				start := at.UTC().Truncate(24 * time.Hour).Add(time.Duration(day) * 24 * time.Hour)
				end := start.Add(24 * time.Hour)
				if overdue {
					stage = fmt.Sprintf("overdue:%d", day)
					key = "invoice_overdue"
					end = at.Add(-time.Duration(day) * 24 * time.Hour)
					start = time.Date(1000, 1, 1, 0, 0, 0, 0, time.UTC)
				}
				targets, err := automationTargets06(ctx, m, `SELECT i.id,i.user_id,'' FROM invoices i WHERE i.status='unpaid' AND i.due_at>=? AND i.due_at<? AND NOT EXISTS(SELECT 1 FROM invoice_notifications n WHERE n.invoice_id=i.id AND n.stage=?) ORDER BY i.due_at,i.id LIMIT 200`, start, end, stage)
				if err != nil {
					return result, err
				}
				batch, err := eachAutomationTarget06(ctx, targets, func(t automationTarget06) (bool, error) { return m.remindInvoice06(ctx, t.ID, stage, key, at) })
				result.Processed += batch.Processed
				result.Failed += batch.Failed
				if err != nil {
					return result, err
				}
			}
		}
		return result, nil
	})
	for _, name := range []string{"suspend_overdue", "terminate_overdue"} {
		_ = a.RegisterAutomationJob(name, func(ctx context.Context, s api.AutomationSettings, at time.Time) (AutomationResult, error) {
			m, ok := a.Store.(*MySQL)
			if !ok {
				return AutomationResult{}, ErrCommerceUnavailable
			}
			days := s.SuspendAfterDays
			action := "suspend"
			states := "s.status='active'"
			condition := "s.cycle<>'one_time' AND s.next_due_at<=?"
			if name == "terminate_overdue" {
				days = s.TerminateAfterDays
				action = "terminate"
				states = "s.status IN ('active','suspended')"
				condition = "((s.cycle<>'one_time' AND s.next_due_at<=?) OR (s.cancellation_type IS NOT NULL AND s.terminates_at<=?))"
			}
			args := []any{at.Add(-time.Duration(days) * 24 * time.Hour)}
			if action == "terminate" {
				args = append(args, at)
			}
			args = append(args, action)
			targets, err := automationTargets06(ctx, m, "SELECT s.id,s.user_id,s.cycle FROM services s WHERE "+states+" AND "+condition+" AND NOT EXISTS(SELECT 1 FROM service_tasks t WHERE t.service_id=s.id AND t.action=? AND t.status IN ('queued','running','uncertain')) ORDER BY s.next_due_at,s.id LIMIT 200", args...)
			if err != nil {
				return AutomationResult{}, err
			}
			return eachAutomationTarget06(ctx, targets, func(t automationTarget06) (bool, error) { return m.queueOverdue06(ctx, t.ID, action, s, at) })
		})
	}
}
func (m *MySQL) autoCredit06(ctx context.Context, id string) (bool, error) {
	return retryPayment(ctx, func() (bool, error) {
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return false, err
		}
		defer tx.Rollback()
		record, err := lockInvoice06(ctx, tx, "", id, false)
		if err != nil {
			return false, err
		}
		if record.Invoice.Status != "unpaid" {
			return false, nil
		}
		out, err := applyInvoiceCredit06(ctx, tx, record, false)
		if err != nil {
			return false, err
		}
		return out.CreditApplied.AmountMinor > record.Invoice.CreditApplied.AmountMinor, tx.Commit()
	})
}
func (m *MySQL) remindInvoice06(ctx context.Context, id, stage, key string, at time.Time) (bool, error) {
	return retryPayment(ctx, func() (bool, error) {
		tx, err := m.DB.BeginTx(ctx, nil)
		if err != nil {
			return false, err
		}
		defer tx.Rollback()
		record, err := lockInvoice06(ctx, tx, "", id, false)
		if err != nil {
			return false, err
		}
		if record.Invoice.Status != "unpaid" || record.Invoice.BalanceDue.AmountMinor <= 0 {
			return false, nil
		}
		res, err := tx.ExecContext(ctx, "INSERT IGNORE INTO invoice_notifications(invoice_id,stage,created_at) VALUES(?,?,?)", id, stage, at)
		if err != nil {
			return false, err
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			return false, err
		}
		notice := record.Invoice
		notice.Total = notice.BalanceDue
		if err = financialEmail06(ctx, tx, notice, key); err != nil {
			return false, err
		}
		return true, tx.Commit()
	})
}
func (m *MySQL) queueOverdue06(ctx context.Context, id, action string, settings api.AutomationSettings, at time.Time) (bool, error) {
	return retryPayment(ctx, func() (bool, error) {
		tx, err := m.DB.BeginTx(ctx, nil)
		if err != nil {
			return false, err
		}
		defer tx.Rollback()
		if err = lockServiceOwner06(ctx, tx, "", id); err != nil {
			return false, err
		}
		s, err := readService06(ctx, tx, "", id, " FOR UPDATE")
		if err != nil {
			return false, err
		}
		if s.View.Status != "active" && s.View.Status != "suspended" || action == "suspend" && s.View.Status != "active" {
			return false, nil
		}
		days := settings.SuspendAfterDays
		source, reason := "automation", "overdue"
		if action == "terminate" {
			days = settings.TerminateAfterDays
		}
		eligible := s.View.Cycle != "one_time" && !s.Due.Add(time.Duration(days)*24*time.Hour).After(at)
		if action == "terminate" && s.View.Cancellation != nil && s.View.TerminatesAt != nil {
			when, err := time.Parse(time.RFC3339Nano, *s.View.TerminatesAt)
			if err != nil {
				return false, err
			}
			if !when.After(at) {
				eligible = true
				source = "cancellation"
				reason = s.View.Cancellation.Reason
			}
		}
		if !eligible {
			return false, nil
		}
		var pending int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM service_tasks WHERE service_id=? AND action=? AND status IN ('queued','running','uncertain')", id, action).Scan(&pending); err != nil {
			return false, err
		}
		if pending > 0 {
			return false, nil
		}
		if _, err = queueServiceTask06(ctx, tx, id, action, serviceActionPayload06{Version: 1, Source: source, Reason: reason, BillingDueAt: &s.Due}, at); err != nil {
			return false, err
		}
		if action == "suspend" && s.View.Cancellation == nil {
			if _, err = tx.ExecContext(ctx, "UPDATE services SET terminates_at=?,revision=revision+1 WHERE id=?", s.Due.Add(time.Duration(settings.TerminateAfterDays)*24*time.Hour), id); err != nil {
				return false, err
			}
		}
		return true, tx.Commit()
	})
}
