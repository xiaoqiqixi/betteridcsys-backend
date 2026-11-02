package billing

import (
	"aster/billing/internal/affiliate"
	"aster/billing/internal/commerce"
	"aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func readAffiliateSettings(ctx context.Context, q accountQueryer, lock string) (financeapi.AffiliateSettings, error) {
	var s financeapi.AffiliateSettings
	var raw []byte
	var revision int64
	err := q.QueryRowContext(ctx, "SELECT document,revision FROM affiliate_settings WHERE id=1"+lock).Scan(&raw, &revision)
	if err != nil {
		return s, err
	}
	if err = json.Unmarshal(raw, &s); err != nil {
		return s, err
	}
	s.Revision = revision
	raw, err = json.Marshal(s)
	if err == nil {
		err = financeapi.ValidateInput("adminUpdateAffiliateSettings", raw)
	}
	if err == nil {
		err = validateAffiliateSettings(s)
	}
	return s, err
}
func validateAffiliateSettings(s financeapi.AffiliateSettings) error {
	for _, rows := range [][]financeapi.AffiliateCurrencyAmount{s.MinWithdraw, s.MaxPerReferral} {
		seen := map[string]bool{}
		for _, v := range rows {
			if seen[v.Currency] {
				return ErrCommerceInput
			}
			seen[v.Currency] = true
		}
	}
	seen := map[string]bool{}
	for _, v := range s.ProductOverrides {
		if seen[v.ProductID] {
			return ErrCommerceInput
		}
		seen[v.ProductID] = true
	}
	return nil
}

var affiliateCurrencyLimit = affiliate.CurrencyLimit
var affiliateRate = affiliate.Rate
var calculateAffiliate = affiliate.Calculate

type affiliateLine = affiliate.Line

// Settlement already holds the invoice/customer locks. The affiliate user lock
// serializes caps, balances, refunds and payouts; a duplicate invoice is a no-op.
func recordAffiliateCommissionTx(ctx context.Context, tx *sql.Tx, invoiceID string, now time.Time) error {
	var uid, kind, state, currency string
	var total, precision int64
	var paid sql.NullTime
	if err := tx.QueryRowContext(ctx, "SELECT user_id,kind,status,currency,amount_minor,paid_at FROM invoices WHERE id=?", invoiceID).Scan(&uid, &kind, &state, &currency, &total, &paid); err != nil {
		return err
	}
	if state != "paid" || (kind != "order" && kind != "renewal" && kind != "upgrade") {
		return nil
	}
	var affiliate string
	err := tx.QueryRowContext(ctx, "SELECT affiliate_id FROM affiliate_referrals WHERE client_id=? FOR UPDATE", uid).Scan(&affiliate)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var role string
	var disabled bool
	if err = tx.QueryRowContext(ctx, "SELECT role,disabled FROM users WHERE id=? FOR UPDATE", affiliate).Scan(&role, &disabled); err != nil {
		return err
	}
	var exists int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM affiliate_commissions WHERE invoice_id=?", invoiceID).Scan(&exists); err != nil || exists != 0 {
		return err
	}
	if err = tx.QueryRowContext(ctx, "SELECT JSON_EXTRACT(currency_snapshot,'$.decimal_places') FROM invoices WHERE id=?", invoiceID).Scan(&precision); err != nil {
		return err
	}
	s, err := readAffiliateSettings(ctx, tx, "")
	if err != nil {
		return err
	}
	var override *int64
	var status string
	err = tx.QueryRowContext(ctx, "SELECT rate_bp,status FROM affiliate_profiles WHERE user_id=?", affiliate).Scan(&override, &status)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	s.Enabled = s.Enabled && status != "suspended" && !disabled && role == "customer" && (s.AllowSelfReferral || affiliate != uid)
	if _, err = tx.ExecContext(ctx, "UPDATE affiliate_referrals SET converted=TRUE WHERE client_id=?", uid); err != nil {
		return err
	}
	// A legacy receipt may not yet have an invoice_payments projection. Count it
	// once, excluding duplicate/unallocated settlement receipts and credit.
	var cash int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT SUM(amount_minor) FROM invoice_payments WHERE invoice_id=? AND method IN ('gateway','manual','bank_transfer','cash','other') AND status IN ('succeeded','refunded')),0) + COALESCE((SELECT SUM(a.amount_minor) FROM payment_attempts a JOIN payment_settlements s ON s.attempt_id=a.id WHERE a.invoice_id=? AND s.disposition='settled' AND NOT EXISTS(SELECT 1 FROM invoice_payments p WHERE p.attempt_id=a.id)),0)`, invoiceID, invoiceID).Scan(&cash); err != nil {
		return err
	}
	cash = min(cash, total)
	if cash == 0 {
		return nil
	}
	// Keep an actual source line for legacy invoices, too.
	if _, err = tx.ExecContext(ctx, `INSERT INTO invoice_lines(id,invoice_id,position,description,amount_minor) SELECT ?,i.id,0,'Legacy order',i.amount_minor FROM invoices i WHERE i.id=? AND i.commerce_version=0 AND NOT EXISTS(SELECT 1 FROM invoice_lines l WHERE l.invoice_id=i.id)`, ID(), invoiceID); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT l.id,COALESCE(s.product_id,lp.product_id,oi.product_id,o.plan_id,''),l.amount_minor,COALESCE(l.period_start,ch.period_start,i.paid_at),COALESCE(s.created_at,(SELECT MIN(su.created_at) FROM service_units u JOIN services su ON su.id=u.service_id WHERE u.order_item_id=oi.id),CASE WHEN i.kind='order' THEN i.paid_at END) FROM invoice_lines l JOIN invoices i ON i.id=l.invoice_id LEFT JOIN orders o ON o.id=i.order_id LEFT JOIN affiliate_invoice_lines lp ON lp.invoice_line_id=l.id LEFT JOIN order_items oi ON oi.id=lp.order_item_id OR (lp.invoice_line_id IS NULL AND oi.order_id=i.order_id AND oi.position=l.position) LEFT JOIN services s ON s.id=l.service_id LEFT JOIN invoice_service_changes ch ON ch.invoice_id=i.id AND ch.service_id=l.service_id WHERE l.invoice_id=? ORDER BY l.position,l.id`, invoiceID)
	if err != nil {
		return err
	}
	lines := []affiliateLine{}
	for rows.Next() {
		var l affiliateLine
		var start, active sql.NullTime
		if err = rows.Scan(&l.ID, &l.Product, &l.Amount, &start, &active); err != nil {
			rows.Close()
			return err
		}
		l.Start = start.Time
		l.Activated = active.Time
		lines = append(lines, l)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var used int64
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(amount_minor),0) FROM affiliate_commissions WHERE client_id=? AND affiliate_id=? AND currency=?", uid, affiliate, currency).Scan(&used); err != nil {
		return err
	}
	computed, err := calculateAffiliate(s, override, kind, currency, cash, used, lines)
	if err != nil {
		return err
	}
	var amount int64
	for _, l := range computed {
		amount, err = commerce.Add(amount, l.Commission)
		if err != nil {
			return err
		}
	}
	id := ID()
	state = "pending"
	if amount == 0 {
		state = "cancelled"
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO affiliate_commissions(id,affiliate_id,client_id,invoice_id,currency,decimal_places,basis_points,amount_minor,withdrawn_minor,status,created_at) VALUES(?,?,?,?,?,?,?,?,0,?,?)`, id, affiliate, uid, invoiceID, currency, precision, affiliateRate(s, override, ""), amount, state, now.UTC()); err != nil {
		return err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO affiliate_commission_rules(commission_id,cash_minor,hold_until,clawback,settings_snapshot) VALUES(?,?,?,?,?)", id, cash, now.Add(time.Duration(s.HoldDays)*24*time.Hour).UTC(), s.Clawback, raw); err != nil {
		return err
	}
	for _, l := range computed {
		if _, err = tx.ExecContext(ctx, `INSERT INTO affiliate_commission_lines(id,commission_id,invoice_line_id,product_id,cash_minor,rate_bp,amount_minor,scope,scope_months,settings_revision) VALUES(?,?,?,?,?,?,?,?,?,?)`, ID(), id, l.ID, l.Product, l.Cash, l.Rate, l.Commission, s.Scope, s.ScopeMonths, s.Revision); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE users SET currency_locked=TRUE WHERE id=?", affiliate); err != nil {
		return err
	}
	return insertAccountAudit(ctx, tx, affiliate, uid, "affiliate.earned", id)
}
