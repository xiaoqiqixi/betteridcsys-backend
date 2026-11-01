package billing

import (
	"aster/billing/internal/commerce"
	"aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type affiliatePolicy struct {
	BasisPoints int64 `json:"commission_basis_points"`
	Recurring   bool  `json:"recurring"`
}

func readAffiliatePolicy(ctx context.Context, q accountQueryer) (affiliatePolicy, error) {
	var p affiliatePolicy
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT document FROM account_settings WHERE name='affiliate_policy'").Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &p)
	}
	if err == nil && (p.BasisPoints < 0 || p.BasisPoints > 10000) {
		err = errors.New("invalid affiliate commission policy")
	}
	return p, err
}

// recordAffiliateCommissionTx runs only after the invoice becomes paid, inside
// the caller's settlement transaction. The unique invoice key makes delivery
// replay harmless; the referral row serializes first-invoice eligibility.
func recordAffiliateCommissionTx(ctx context.Context, tx *sql.Tx, invoiceID string, now time.Time) error {
	var uid, kind, status, currency string
	var total, refunded int64
	if err := tx.QueryRowContext(ctx, "SELECT user_id,kind,status,currency,amount_minor,refunded_minor FROM invoices WHERE id=?", invoiceID).Scan(&uid, &kind, &status, &currency, &total, &refunded); err != nil {
		return err
	}
	if status != "paid" || (kind != "order" && kind != "renewal" && kind != "upgrade") {
		return nil
	}
	var affiliate string
	var converted bool
	err := tx.QueryRowContext(ctx, "SELECT affiliate_id,converted FROM affiliate_referrals WHERE client_id=? FOR UPDATE", uid).Scan(&affiliate, &converted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if affiliate == uid {
		return errors.New("self referral")
	}
	var exists int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM affiliate_commissions WHERE invoice_id=?", invoiceID).Scan(&exists); err != nil || exists != 0 {
		return err
	}
	policy, err := readAffiliatePolicy(ctx, tx)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE affiliate_referrals SET converted=TRUE WHERE client_id=?", uid); err != nil {
		return err
	}
	if policy.BasisPoints == 0 || (converted && !policy.Recurring) {
		return nil
	}
	var accountCurrency, role string
	var disabled bool
	if err = tx.QueryRowContext(ctx, "SELECT currency,role,disabled FROM users WHERE id=? FOR UPDATE", affiliate).Scan(&accountCurrency, &role, &disabled); err != nil {
		return err
	}
	// There is no implicit foreign exchange in account credit. Preserve the
	// conversion count but do not mislabel a foreign-currency commission.
	if currency != accountCurrency || role != "customer" || disabled {
		return nil
	}
	var precision int64
	if err = tx.QueryRowContext(ctx, "SELECT JSON_EXTRACT(currency_snapshot,'$.decimal_places') FROM invoices WHERE id=?", invoiceID).Scan(&precision); err != nil {
		return err
	}
	amount, err := commerce.FloorRatio(total-refunded, policy.BasisPoints, 10000)
	if err != nil || amount == 0 {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO affiliate_commissions(id,affiliate_id,client_id,invoice_id,currency,decimal_places,basis_points,amount_minor,withdrawn_minor,status,created_at) VALUES(?,?,?,?,?,?,?,?,0,'pending',?)`, ID(), affiliate, uid, invoiceID, currency, precision, policy.BasisPoints, amount, now.UTC())
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE users SET currency_locked=TRUE WHERE id=?", affiliate)
	return err
}

func (a *App) affiliateSummary(ctx context.Context, q accountReader, uid string) (financeapi.AffiliateSummary, error) {
	var out financeapi.AffiliateSummary
	policy, err := readAffiliatePolicy(ctx, q)
	if err != nil {
		return out, err
	}
	out.CommissionPercent = float64(policy.BasisPoints) / 100
	settings, err := readAutomation(ctx, q, "")
	if err != nil {
		return out, err
	}
	out.HoldDays = settings.AffiliateHoldDays
	money, err := accountMoney06(ctx, q, uid, "")
	if err != nil {
		return out, err
	}
	out.Pending, out.Available, out.PaidOut = money, money, money
	var code string
	err = q.QueryRowContext(ctx, "SELECT code FROM affiliates WHERE user_id=?", uid).Scan(&code)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Enabled = true
	out.Code = &code
	link := strings.TrimRight(a.Config.Origin, "/") + "/register?ref=" + url.QueryEscape(code)
	out.Link = &link
	if err = q.QueryRowContext(ctx, "SELECT COUNT(*) FROM affiliate_referrals WHERE affiliate_id=?", uid).Scan(&out.Referrals); err != nil {
		return out, err
	}
	rows, err := q.QueryContext(ctx, "SELECT currency,decimal_places,status,SUM(amount_minor),SUM(withdrawn_minor) FROM affiliate_commissions WHERE affiliate_id=? GROUP BY currency,decimal_places,status", uid)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var currency, state string
		var precision, amount, withdrawn int64
		if err = rows.Scan(&currency, &precision, &state, &amount, &withdrawn); err != nil {
			return out, err
		}
		if currency != money.Currency || precision != money.DecimalPlaces {
			return out, ErrCurrency
		}
		if state == "pending" {
			out.Pending.AmountMinor, err = commerce.Add(out.Pending.AmountMinor, amount)
		}
		if state == "available" {
			out.Available.AmountMinor, err = commerce.Add(out.Available.AmountMinor, amount-withdrawn)
		}
		if err != nil {
			return out, err
		}
		out.PaidOut.AmountMinor, err = commerce.Add(out.PaidOut.AmountMinor, withdrawn)
		if err != nil {
			return out, err
		}
	}
	return out, rows.Err()
}

func (a *App) affiliateHTTP(w http.ResponseWriter, r *http.Request, u User, path string) bool {
	if path != "/affiliate" && !strings.HasPrefix(path, "/affiliate/") {
		return false
	}
	if u.Role != "customer" {
		fail(w, r, 403, "FORBIDDEN", "Client account required")
		return true
	}
	m, err := a.accountDB()
	if err != nil {
		a.accountResult(w, r, nil, err)
		return true
	}
	switch {
	case path == "/affiliate" && r.Method == "GET":
		out, err := a.affiliateSummary(r.Context(), m.DB, u.ID)
		a.accountResult(w, r, out, err)
	case path == "/affiliate/referrals" && r.Method == "GET":
		a.affiliateReferrals(w, r, u)
	case path == "/affiliate/activate" && r.Method == "POST":
		var in struct{}
		if !accountBody(w, r, "activateAffiliate", &in) {
			return true
		}
		out, err := a.accountTransaction(r, u.ID, "activate-affiliate", in, false, func(tx *sql.Tx) (any, error) {
			if _, err := lockAccountUser(r.Context(), tx, u.ID); err != nil {
				return nil, err
			}
			_, err := tx.ExecContext(r.Context(), "INSERT INTO affiliates(user_id,code,created_at) VALUES(?,?,UTC_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE user_id=user_id", u.ID, ID())
			if err != nil {
				return nil, err
			}
			return a.affiliateSummary(r.Context(), tx, u.ID)
		})
		a.accountResult(w, r, out, err)
	case path == "/affiliate/withdraw" && r.Method == "POST":
		var in financeapi.WithdrawAffiliateInput
		if !accountBody(w, r, "withdrawAffiliate", &in) {
			return true
		}
		out, err := a.accountTransaction(r, u.ID, "withdraw-affiliate", in, true, func(tx *sql.Tx) (any, error) {
			if _, _, err := readCurrencies(r.Context(), tx, " FOR SHARE"); err != nil {
				return nil, err
			}
			if _, err := lockAccountUser(r.Context(), tx, u.ID); err != nil {
				return nil, err
			}
			summary, err := a.affiliateSummary(r.Context(), tx, u.ID)
			if err != nil {
				return nil, err
			}
			if !summary.Enabled || in.AmountMinor > summary.Available.AmountMinor {
				return nil, problem(409, "INSUFFICIENT_COMMISSION", "Insufficient available commission")
			}
			rows, err := tx.QueryContext(r.Context(), "SELECT id,amount_minor-withdrawn_minor FROM affiliate_commissions WHERE affiliate_id=? AND status='available' ORDER BY created_at,id FOR UPDATE", u.ID)
			if err != nil {
				return nil, err
			}
			type part struct {
				id     string
				amount int64
			}
			parts := []part{}
			left := in.AmountMinor
			for rows.Next() {
				var p part
				if err = rows.Scan(&p.id, &p.amount); err != nil {
					rows.Close()
					return nil, err
				}
				p.amount = min(p.amount, left)
				parts = append(parts, p)
				left -= p.amount
				if left == 0 {
					break
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
			if left != 0 {
				return nil, ErrConflict
			}
			for _, p := range parts {
				if _, err = tx.ExecContext(r.Context(), "UPDATE affiliate_commissions SET status=IF(withdrawn_minor+?=amount_minor,'withdrawn','available'),withdrawn_minor=withdrawn_minor+? WHERE id=?", p.amount, p.amount, p.id); err != nil {
					return nil, err
				}
			}
			entry, err := appendCredit06(r.Context(), tx, u.ID, "affiliate_payout", in.AmountMinor, nil, "Affiliate commission payout", time.Now().UTC())
			if err != nil {
				return nil, err
			}
			if err = insertAccountAudit(r.Context(), tx, u.ID, u.ID, "affiliate.withdrawn", entry.ID); err != nil {
				return nil, err
			}
			return a.affiliateSummary(r.Context(), tx, u.ID)
		})
		a.accountResult(w, r, out, err)
	default:
		fail(w, r, 404, "NOT_FOUND", "Affiliate endpoint not found")
	}
	return true
}

func (a *App) affiliateReferrals(w http.ResponseWriter, r *http.Request, u User) {
	if _, err := parseAccountQuery(r, "limit", "cursor"); err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	limit, err := queryLimit(r)
	if err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	cursor, err := a.readAccountCursor(r, u.ID)
	if err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	m, err := a.accountDB()
	if err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	money, err := accountMoney06(r.Context(), m.DB, u.ID, "")
	if err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	query := `SELECT r.client_id,u.email,r.created_at,r.converted,COALESCE((SELECT SUM(c.amount_minor) FROM affiliate_commissions c WHERE c.client_id=r.client_id AND c.affiliate_id=r.affiliate_id AND c.status<>'cancelled'),0) FROM affiliate_referrals r JOIN users u ON u.id=r.client_id WHERE r.affiliate_id=?`
	args := []any{u.ID}
	if !cursor.At.IsZero() {
		query += " AND (r.created_at<? OR (r.created_at=? AND r.client_id<?))"
		args = append(args, cursor.At, cursor.At, cursor.ID)
	}
	args = append(args, limit+1)
	rows, err := m.DB.QueryContext(r.Context(), query+" ORDER BY r.created_at DESC,r.client_id DESC LIMIT ?", args...)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	defer rows.Close()
	out := financeapi.ListReferralsResponse{Items: []financeapi.AffiliateReferral{}}
	for rows.Next() {
		var v financeapi.AffiliateReferral
		var email string
		var at time.Time
		var converted bool
		v.Commissions = money
		if err = rows.Scan(&v.ID, &email, &at, &converted, &v.Commissions.AmountMinor); err != nil {
			a.internal(w, r, err)
			return
		}
		if len(out.Items) == limit {
			last := out.Items[len(out.Items)-1]
			at, _ := time.Parse(time.RFC3339Nano, last.JoinedAt)
			next := a.makeAccountCursor(r, u.ID, at, last.ID)
			out.NextCursor = &next
			break
		}
		v.Client = maskReferralEmail(email)
		v.JoinedAt = stamp(at)
		v.Status = "signed_up"
		if converted {
			v.Status = "converted"
		}
		out.Items = append(out.Items, v)
	}
	a.accountResult(w, r, out, rows.Err())
}

func maskReferralEmail(email string) string {
	local, domain, _ := strings.Cut(email, "@")
	first := []rune(local)
	if len(first) == 0 {
		return "***"
	}
	return string(first[:1]) + "***@" + domain
}

func (a *App) releaseAffiliate(ctx context.Context, settings financeapi.AutomationSettings, now time.Time) (AutomationResult, error) {
	var result AutomationResult
	m, err := a.accountDB()
	if err != nil {
		return result, err
	}
	cutoff := now.Add(-time.Duration(settings.AffiliateHoldDays) * 24 * time.Hour)
	rows, err := m.DB.QueryContext(ctx, "SELECT id,affiliate_id FROM affiliate_commissions WHERE status='pending' AND created_at<=? ORDER BY created_at,id LIMIT 200", cutoff)
	if err != nil {
		return result, err
	}
	type item struct{ id, uid string }
	items := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.id, &v.uid); err != nil {
			rows.Close()
			return result, err
		}
		items = append(items, v)
	}
	err = rows.Err()
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
			var uid string
			if err = tx.QueryRowContext(ctx, "SELECT id FROM users WHERE id=? FOR UPDATE", v.uid).Scan(&uid); err != nil {
				return false, err
			}
			var invoice, state string
			var bp, amount int64
			var created time.Time
			if err = tx.QueryRowContext(ctx, "SELECT invoice_id,status,basis_points,amount_minor,created_at FROM affiliate_commissions WHERE id=? FOR UPDATE", v.id).Scan(&invoice, &state, &bp, &amount, &created); err != nil {
				return false, err
			}
			if state != "pending" || created.After(cutoff) {
				return false, nil
			}
			var total, refunded int64
			if err = tx.QueryRowContext(ctx, "SELECT amount_minor,refunded_minor,status FROM invoices WHERE id=?", invoice).Scan(&total, &refunded, &state); err != nil {
				return false, err
			}
			remaining, err := commerce.FloorRatio(total-refunded, bp, 10000)
			if err != nil {
				return false, err
			}
			amount = min(amount, remaining)
			if state != "paid" && state != "partially_refunded" {
				amount = 0
			}
			state = "available"
			if amount == 0 {
				state = "cancelled"
			}
			if _, err = tx.ExecContext(ctx, "UPDATE affiliate_commissions SET amount_minor=?,status=?,released_at=? WHERE id=?", amount, state, now.UTC(), v.id); err != nil {
				return false, err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) VALUES(?,?,'affiliate.released',?,?,'system','Commission hold completed')", ID(), uid, v.id, now.UTC()); err != nil {
				return false, err
			}
			return true, tx.Commit()
		})
		if e != nil {
			result.Failed++
		} else if changed {
			result.Processed++
		}
	}
	return result, nil
}
