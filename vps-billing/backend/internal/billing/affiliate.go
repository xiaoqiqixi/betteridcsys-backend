package billing

import (
	"aster/billing/internal/commerce"
	"aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Released earnings, refunds and payouts are independent immutable entries.
// Negative net earnings are exposed as debt, never as spendable negative money.
func affiliateBalances(ctx context.Context, q accountReader, uid string) ([]financeapi.AffiliateBalance, error) {
	balances := map[string]*financeapi.AffiliateBalance{}
	ensure := func(currency string, precision int64) (*financeapi.AffiliateBalance, error) {
		if b := balances[currency]; b != nil {
			if b.Available.DecimalPlaces != precision {
				return nil, ErrCurrency
			}
			return b, nil
		}
		m := financeapi.Money{Currency: currency, DecimalPlaces: precision}
		b := &financeapi.AffiliateBalance{Currency: currency, Pending: m, Available: m, PaidOut: m, Debt: m}
		balances[currency] = b
		return b, nil
	}
	money, err := accountMoney06(ctx, q, uid, "")
	if err != nil {
		return nil, err
	}
	if _, err = ensure(money.Currency, money.DecimalPlaces); err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT c.currency,c.decimal_places,c.status,SUM(c.amount_minor+COALESCE(a.amount,0)),SUM(c.withdrawn_minor) FROM affiliate_commissions c LEFT JOIN (SELECT commission_id,SUM(amount_minor) amount FROM affiliate_adjustments GROUP BY commission_id) a ON a.commission_id=c.id WHERE c.affiliate_id=? GROUP BY c.currency,c.decimal_places,c.status`, uid)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var currency, state string
		var precision, amount, withdrawn int64
		if err = rows.Scan(&currency, &precision, &state, &amount, &withdrawn); err != nil {
			break
		}
		var b *financeapi.AffiliateBalance
		b, err = ensure(currency, precision)
		if err != nil {
			break
		}
		if state == "pending" {
			b.Pending.AmountMinor, err = commerce.Add(b.Pending.AmountMinor, amount)
		} else if state == "available" || state == "withdrawn" {
			b.Available.AmountMinor, err = commerce.Add(b.Available.AmountMinor, amount)
		}
		if err == nil {
			b.PaidOut.AmountMinor, err = commerce.Add(b.PaidOut.AmountMinor, withdrawn)
		}
		if err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = q.QueryContext(ctx, "SELECT currency,decimal_places,SUM(amount_minor) FROM affiliate_payouts WHERE affiliate_id=? GROUP BY currency,decimal_places", uid)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var currency string
		var precision, amount int64
		if err = rows.Scan(&currency, &precision, &amount); err != nil {
			break
		}
		var b *financeapi.AffiliateBalance
		b, err = ensure(currency, precision)
		if err != nil {
			break
		}
		b.PaidOut.AmountMinor, err = commerce.Add(b.PaidOut.AmountMinor, amount)
		if err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []financeapi.AffiliateBalance{}
	for _, b := range balances {
		net := b.Available.AmountMinor - b.PaidOut.AmountMinor
		b.Available.AmountMinor = max(net, 0)
		b.Debt.AmountMinor = max(-net, 0)
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out, nil
}
func (a *App) affiliateSummary(ctx context.Context, q accountReader, uid string) (financeapi.AffiliateSummary, error) {
	var out financeapi.AffiliateSummary
	s, err := readAffiliateSettings(ctx, q, "")
	if err != nil {
		return out, err
	}
	out.RateBp = s.DefaultRateBp
	out.Scope = s.Scope
	out.ScopeMonths = s.ScopeMonths
	out.HoldDays = s.HoldDays
	out.MinWithdraw = s.MinWithdraw
	out.EarningEnabled = s.Enabled
	out.Balances, err = affiliateBalances(ctx, q, uid)
	if err != nil {
		return out, err
	}
	var code, status string
	var override *int64
	err = q.QueryRowContext(ctx, "SELECT a.code,COALESCE(p.status,'active'),p.rate_bp FROM affiliates a LEFT JOIN affiliate_profiles p ON p.user_id=a.user_id WHERE a.user_id=?", uid).Scan(&code, &status, &override)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Enabled = true
	out.EarningEnabled = s.Enabled && status == "active"
	out.Code = &code
	out.RateBp = affiliateRate(s, override, "")
	link := strings.TrimRight(a.Config.Origin, "/") + "/register?ref=" + url.QueryEscape(code)
	out.Link = &link
	err = q.QueryRowContext(ctx, "SELECT COUNT(*) FROM affiliate_referrals WHERE affiliate_id=?", uid).Scan(&out.Referrals)
	return out, err
}
func recordAffiliatePayout(ctx context.Context, tx *sql.Tx, uid, actor, currency string, amount int64, reference, destination string) (string, error) {
	balances, err := affiliateBalances(ctx, tx, uid)
	if err != nil {
		return "", err
	}
	var balance *financeapi.AffiliateBalance
	for i := range balances {
		if balances[i].Currency == currency {
			balance = &balances[i]
		}
	}
	if balance == nil || amount <= 0 || amount > balance.Available.AmountMinor {
		return "", problem(409, "INSUFFICIENT_COMMISSION", "Insufficient available commission")
	}
	s, err := readAffiliateSettings(ctx, tx, "")
	if err != nil {
		return "", err
	}
	if minimum, _ := affiliateCurrencyLimit(s.MinWithdraw, currency); amount < minimum {
		return "", problem(409, "MINIMUM_WITHDRAWAL", "Amount is below the minimum withdrawal")
	}
	id := ID()
	if _, err = tx.ExecContext(ctx, "INSERT INTO affiliate_payouts(id,affiliate_id,currency,decimal_places,amount_minor,reference,destination,created_at) VALUES(?,?,?,?,?,?,?,UTC_TIMESTAMP(6))", id, uid, currency, balance.Available.DecimalPlaces, amount, reference, destination); err != nil {
		return "", err
	}
	detail := fmt.Sprintf("%s %d minor; %s; reference %s", currency, amount, destination, reference)
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) SELECT ?,?,'affiliate.payout',?,UTC_TIMESTAMP(6),email,? FROM users WHERE id=?`, ID(), uid, id, detail, actor); err != nil {
		return "", err
	}
	return id, nil
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
			result, err := tx.ExecContext(r.Context(), "INSERT IGNORE INTO affiliates(user_id,code,created_at) VALUES(?,?,UTC_TIMESTAMP(6))", u.ID, ID())
			if err != nil {
				return nil, err
			}
			if n, _ := result.RowsAffected(); n > 0 {
				if err = insertAccountAudit(r.Context(), tx, u.ID, u.ID, "affiliate.activated", u.ID); err != nil {
					return nil, err
				}
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
			money, err := accountMoney06(r.Context(), tx, u.ID, "")
			if err != nil {
				return nil, err
			}
			id, err := recordAffiliatePayout(r.Context(), tx, u.ID, u.ID, money.Currency, in.AmountMinor, ID(), "credit")
			if err != nil {
				return nil, err
			}
			entry, err := appendCredit06(r.Context(), tx, u.ID, "affiliate_payout", in.AmountMinor, nil, "Affiliate payout "+id, time.Now().UTC())
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
	query := `SELECT r.client_id,u.email,r.created_at,r.converted FROM affiliate_referrals r JOIN users u ON u.id=r.client_id WHERE r.affiliate_id=?`
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
	out := financeapi.ListReferralsResponse{Items: []financeapi.AffiliateReferral{}}
	for rows.Next() {
		var v financeapi.AffiliateReferral
		var at time.Time
		var converted bool
		var email string
		if err = rows.Scan(&v.ID, &email, &at, &converted); err != nil {
			break
		}
		if len(out.Items) == limit {
			last := out.Items[len(out.Items)-1]
			at, _ = time.Parse(time.RFC3339Nano, last.JoinedAt)
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
		v.Commissions = []financeapi.Money{}
		out.Items = append(out.Items, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		a.internal(w, r, err)
		return
	}
	for i := range out.Items {
		rows, err = m.DB.QueryContext(r.Context(), `SELECT c.currency,c.decimal_places,SUM(c.amount_minor+COALESCE(a.amount,0)) FROM affiliate_commissions c LEFT JOIN (SELECT commission_id,SUM(amount_minor) amount FROM affiliate_adjustments GROUP BY commission_id) a ON a.commission_id=c.id WHERE c.affiliate_id=? AND c.client_id=? AND c.status<>'cancelled' GROUP BY c.currency,c.decimal_places ORDER BY c.currency`, u.ID, out.Items[i].ID)
		if err != nil {
			break
		}
		for rows.Next() {
			var v financeapi.Money
			if err = rows.Scan(&v.Currency, &v.DecimalPlaces, &v.AmountMinor); err != nil {
				break
			}
			out.Items[i].Commissions = append(out.Items[i].Commissions, v)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			break
		}
	}
	a.accountResult(w, r, out, err)
}
func maskReferralEmail(email string) string {
	local, domain, _ := strings.Cut(email, "@")
	first := []rune(local)
	if len(first) == 0 {
		return "***"
	}
	return string(first[:1]) + "***@" + domain
}
