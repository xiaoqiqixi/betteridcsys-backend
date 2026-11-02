package billing

import (
	"aster/billing/internal/commerce"
	api "aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"
)

func overviewIDs06(ctx context.Context, q currencyQuerier, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (m *MySQL) ClientOverview06(ctx context.Context, uid string, at time.Time) (api.ClientOverview, error) {
	out := api.ClientOverview{Services: []api.ServiceSummary{}, Invoices: []api.InvoiceSummary{}, Announcements: []api.Announcement{}}
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	money, err := accountMoney06(ctx, tx, uid, "")
	if err != nil {
		return out, err
	}
	out.Credit, _, err = creditBalance06(ctx, tx, uid, money)
	if err != nil {
		return out, err
	}
	out.AmountDue = money
	err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM services WHERE user_id=? AND status='active'),(SELECT COUNT(*) FROM invoices WHERE user_id=? AND status='unpaid'),(SELECT COUNT(*) FROM tickets WHERE user_id=? AND status<>'closed'),(SELECT COUNT(*) FROM services WHERE user_id=? AND status IN ('active','suspended') AND cycle<>'one_time' AND next_due_at>=? AND next_due_at<=?),(SELECT COALESCE(SUM(amount_minor-amount_paid_minor-credit_applied_minor),0) FROM invoices WHERE user_id=? AND currency=? AND status='unpaid')`, uid, uid, uid, uid, at, at.Add(7*24*time.Hour), uid, money.Currency).Scan(&out.Counts.ActiveServices, &out.Counts.UnpaidInvoices, &out.Counts.OpenTickets, &out.Counts.ExpiringSoon, &out.AmountDue.AmountMinor)
	if err != nil {
		return out, err
	}
	if _, err = commerce.Add(0, out.AmountDue.AmountMinor); err != nil {
		return out, err
	}
	ids, err := overviewIDs06(ctx, tx, "SELECT id FROM services WHERE user_id=? ORDER BY (status='active') DESC,next_due_at,id LIMIT 5", uid)
	if err != nil {
		return out, err
	}
	for _, id := range ids {
		record, err := readService06(ctx, tx, uid, id, "")
		if err != nil {
			return out, err
		}
		raw, _ := json.Marshal(record.View)
		var v api.ServiceSummary
		_ = json.Unmarshal(raw, &v)
		out.Services = append(out.Services, v)
	}
	ids, err = overviewIDs06(ctx, tx, "SELECT id FROM invoices WHERE user_id=? ORDER BY (status='unpaid') DESC,due_at DESC,id DESC LIMIT 5", uid)
	if err != nil {
		return out, err
	}
	summaries, err := readInvoiceSummaries06(ctx, tx, ids, uid)
	if err != nil {
		return out, err
	}
	for _, summary := range summaries {
		out.Invoices = append(out.Invoices, customerInvoiceSummary06(summary))
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,title,body,pinned,published,published_at FROM announcements WHERE published=TRUE AND published_at<=? ORDER BY pinned DESC,published_at DESC,id DESC LIMIT 5", at)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		v, err := scanAnnouncement(rows)
		if err != nil {
			rows.Close()
			return out, err
		}
		out.Announcements = append(out.Announcements, api.Announcement{ID: v.ID, Title: v.Title, Body: v.Body, Pinned: v.Pinned, PublishedAt: v.PublishedAt})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

// Revenue is allocated service/manual invoice receipts (including credit),
// less successful refunds at their completion date. Top-ups and unallocated
// duplicate receipts are excluded, so adding and later spending credit is not
// counted twice. Legacy paid invoices without a payment projection remain visible.
const revenueEvents06 = `SELECT p.amount_minor amount,p.created_at at FROM invoice_payments p JOIN invoices i ON i.id=p.invoice_id LEFT JOIN payment_settlements s ON s.attempt_id=p.attempt_id WHERE i.currency=? AND i.kind<>'top_up' AND i.status<>'cancelled' AND p.status IN ('succeeded','refunded') AND COALESCE(s.disposition,'settled')='settled'
UNION ALL SELECT -r.amount_minor,r.updated_at FROM invoice_refunds r JOIN invoices i ON i.id=r.invoice_id JOIN invoice_payments p ON p.id=r.payment_id LEFT JOIN payment_settlements s ON s.attempt_id=p.attempt_id WHERE i.currency=? AND i.kind<>'top_up' AND r.status='succeeded' AND COALESCE(s.disposition,'settled')='settled'
UNION ALL SELECT i.amount_minor,i.paid_at FROM invoices i WHERE i.currency=? AND i.kind<>'top_up' AND i.paid_at IS NOT NULL AND i.status IN ('paid','partially_refunded','refunded') AND NOT EXISTS(SELECT 1 FROM invoice_payments p LEFT JOIN payment_settlements s ON s.attempt_id=p.attempt_id WHERE p.invoice_id=i.id AND p.status IN ('succeeded','refunded') AND COALESCE(s.disposition,'settled')='settled')`

func (m *MySQL) AdminOverview06(ctx context.Context, currency string, at time.Time) (api.AdminOverview, error) {
	var out api.AdminOverview
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	config, _, err := readCurrencies(ctx, tx, "")
	if err != nil {
		return out, err
	}
	if currency == "" {
		currency = config.DefaultCurrency
	}
	c, ok := config.find(currency)
	if !ok {
		return out, ErrCurrency
	}
	out.Currency = currency
	money := commerce.Money(currency, int64(c.DecimalPlaces), 0)
	out.RevenueToday = money
	out.RevenueMonth = money
	out.RevenuePrevMonth = money
	out.Outstanding = money
	today := time.Date(at.UTC().Year(), at.UTC().Month(), at.UTC().Day(), 0, 0, 0, 0, time.UTC)
	month := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	previous := month.AddDate(0, -1, 0)
	err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM users WHERE role='customer'),(SELECT COUNT(*) FROM users WHERE role='customer' AND created_at>=? AND created_at<=?),(SELECT COUNT(*) FROM services WHERE currency=? AND status='active'),(SELECT COUNT(*) FROM orders WHERE currency=? AND status='pending_payment'),(SELECT COUNT(*) FROM tickets WHERE status<>'closed'),(SELECT COUNT(*) FROM invoices WHERE currency=? AND status='unpaid' AND due_at<?),(SELECT COALESCE(SUM(amount_minor-amount_paid_minor-credit_applied_minor),0) FROM invoices WHERE currency=? AND status='unpaid')`, month, at, currency, currency, currency, at, currency).Scan(&out.Counts.Clients, &out.Counts.NewClientsMonth, &out.Counts.ActiveServices, &out.Counts.PendingOrders, &out.Counts.OpenTickets, &out.Counts.OverdueInvoices, &out.Outstanding.AmountMinor)
	if err != nil {
		return out, err
	}
	if _, err = commerce.Add(0, out.Outstanding.AmountMinor); err != nil {
		return out, err
	}
	seriesStart := today.AddDate(0, 0, -29)
	start := previous
	if seriesStart.Before(start) {
		start = seriesStart
	}
	rows, err := tx.QueryContext(ctx, "SELECT DATE_FORMAT(at,'%Y-%m-%d'),SUM(amount) FROM ("+revenueEvents06+") revenue WHERE at>=? AND at<=? GROUP BY DATE_FORMAT(at,'%Y-%m-%d') ORDER BY DATE_FORMAT(at,'%Y-%m-%d')", currency, currency, currency, start, at)
	if err != nil {
		return out, err
	}
	byDate := map[string]int64{}
	for rows.Next() {
		var date string
		var amount int64
		if err = rows.Scan(&date, &amount); err != nil {
			rows.Close()
			return out, err
		}
		if _, err = commerce.Add(0, amount); err != nil {
			rows.Close()
			return out, err
		}
		byDate[date] = amount
		if date == today.Format("2006-01-02") {
			out.RevenueToday.AmountMinor = amount
		}
		if date >= month.Format("2006-01-02") {
			out.RevenueMonth.AmountMinor, err = commerce.Add(out.RevenueMonth.AmountMinor, amount)
		} else if date >= previous.Format("2006-01-02") {
			out.RevenuePrevMonth.AmountMinor, err = commerce.Add(out.RevenuePrevMonth.AmountMinor, amount)
		}
		if err != nil {
			rows.Close()
			return out, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	for i := 0; i < 30; i++ {
		date := seriesStart.AddDate(0, 0, i).Format("2006-01-02")
		amount := money
		amount.AmountMinor = byDate[date]
		out.RevenueSeries = append(out.RevenueSeries, struct {
			Date   string    `json:"date"`
			Amount api.Money `json:"amount"`
		}{date, amount})
	}
	out.Attention = make([]struct {
		Kind  string `json:"kind"`
		Title string `json:"title"`
		RefID string `json:"ref_id"`
		At    string `json:"at"`
	}, 0)
	rows, err = tx.QueryContext(ctx, `SELECT kind,title,ref_id,at FROM (
SELECT 'provisioning_failed' kind,plan_name title,id ref_id,created_at at FROM services WHERE status='failed'
UNION ALL SELECT 'task_uncertain',CONCAT(action,' requires review'),service_id,updated_at FROM service_tasks WHERE status='uncertain'
UNION ALL SELECT 'duplicate_payment',CONCAT('Review invoice ',number),id,issued_at FROM invoices WHERE settlement_review=TRUE
UNION ALL SELECT 'ticket_waiting',subject,id,last_reply_at FROM tickets WHERE status IN ('open','customer_reply')
UNION ALL SELECT 'cancellation_request',plan_name,id,cancellation_requested_at FROM services WHERE cancellation_type IS NOT NULL AND status NOT IN ('terminated','cancelled')
) attention ORDER BY at DESC,ref_id DESC LIMIT 30`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var kind, title, id string
		var date time.Time
		if err = rows.Scan(&kind, &title, &id, &date); err != nil {
			rows.Close()
			return out, err
		}
		out.Attention = append(out.Attention, struct {
			Kind  string `json:"kind"`
			Title string `json:"title"`
			RefID string `json:"ref_id"`
			At    string `json:"at"`
		}{kind, title, id, stamp06(date)})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
func (a *App) overview06HTTP(w http.ResponseWriter, r *http.Request, u User, p string) bool {
	if (p != "/overview" && p != "/admin/overview") || !a.commerceContract06(r) {
		return false
	}
	if r.Method != "GET" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return true
	}
	m, ok := a.Store.(*MySQL)
	if !ok {
		fail(w, r, 503, "FEATURE_UNAVAILABLE", "Overview requires persistent storage")
		return true
	}
	if p == "/admin/overview" {
		out, err := m.AdminOverview06(r.Context(), r.URL.Query().Get("currency"), time.Now().UTC())
		if err != nil {
			a.commerceError(w, r, err)
		} else {
			jsonOut(w, 200, out)
		}
	} else {
		out, err := m.ClientOverview06(r.Context(), u.ID, time.Now().UTC())
		if err != nil {
			a.commerceError(w, r, err)
		} else {
			jsonOut(w, 200, out)
		}
	}
	return true
}
