package billing

import (
	"aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

func adminAffiliate(ctx context.Context, q accountReader, id string) (financeapi.AdminAffiliate, error) {
	var out financeapi.AdminAffiliate
	var at time.Time
	err := q.QueryRowContext(ctx, `SELECT a.user_id,u.name,u.email,a.code,a.created_at,p.rate_bp,COALESCE(p.status,'active'),COALESCE(p.note,''),(SELECT COUNT(*) FROM affiliate_referrals r WHERE r.affiliate_id=a.user_id) FROM affiliates a JOIN users u ON u.id=a.user_id LEFT JOIN affiliate_profiles p ON p.user_id=a.user_id WHERE a.user_id=?`, id).Scan(&out.ID, &out.Name, &out.Email, &out.Code, &at, &out.RateBp, &out.Status, &out.Note, &out.Referrals)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	if err != nil {
		return out, err
	}
	out.CreatedAt = stamp(at)
	out.Balances, err = affiliateBalances(ctx, q, id)
	return out, err
}
func (a *App) adminAffiliateHTTP(w http.ResponseWriter, r *http.Request, u User, path string) bool {
	if path != "/admin/affiliate/settings" && path != "/admin/affiliates" && !strings.HasPrefix(path, "/admin/affiliates/") {
		return false
	}
	m, err := a.accountDB()
	if err != nil {
		a.accountResult(w, r, nil, err)
		return true
	}
	ctx := r.Context()
	if path == "/admin/affiliate/settings" {
		switch r.Method {
		case "GET":
			out, err := readAffiliateSettings(ctx, m.DB, "")
			a.accountResult(w, r, out, err)
		case "PATCH":
			var in financeapi.AffiliateSettings
			if !accountBody(w, r, "adminUpdateAffiliateSettings", &in) {
				return true
			}
			out, err := a.accountTransaction(r, u.ID, "affiliate-settings", in, false, func(tx *sql.Tx) (any, error) {
				if err := staffWriteTx(ctx, tx, u.ID, "settings.write"); err != nil {
					return nil, err
				}
				old, err := readAffiliateSettings(ctx, tx, " FOR UPDATE")
				if err != nil {
					return nil, err
				}
				if old.Revision != in.Revision || in.Revision >= MaxMinor {
					return nil, ErrConflict
				}
				if err = validateAffiliateSettings(in); err != nil {
					return nil, err
				}
				registry, _, err := readCurrencies(ctx, tx, " FOR SHARE")
				if err != nil {
					return nil, err
				}
				for _, rows := range [][]financeapi.AffiliateCurrencyAmount{in.MinWithdraw, in.MaxPerReferral} {
					for _, v := range rows {
						if _, ok := registry.find(v.Currency); !ok {
							return nil, ErrCurrency
						}
					}
				}
				for _, v := range in.ProductOverrides {
					var id string
					if err = tx.QueryRowContext(ctx, "SELECT id FROM products WHERE id=?", v.ProductID).Scan(&id); err != nil {
						return nil, problem(400, "INVALID_PRODUCT", "Unknown affiliate product override")
					}
				}
				next := in
				next.Revision++
				raw, err := json.Marshal(next)
				if err != nil {
					return nil, err
				}
				if _, err = tx.ExecContext(ctx, "UPDATE affiliate_settings SET revision=?,document=? WHERE id=1", next.Revision, raw); err != nil {
					return nil, err
				}
				if err = insertCurrencyAudit(ctx, tx, catalogAudit(u, "affiliate.settings.updated", "affiliate", next.Revision, "Settings SHA256: "+digest(string(raw)))); err != nil {
					return nil, err
				}
				return next, nil
			})
			a.accountResult(w, r, out, err)
		default:
			fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		}
		return true
	}
	if path == "/admin/affiliates" && r.Method == "GET" {
		a.adminAffiliates(w, r, u)
		return true
	}
	parts := strings.Split(strings.TrimPrefix(path, "/admin/affiliates/"), "/")
	if len(parts) == 0 || !validCancellationID(parts[0]) {
		fail(w, r, 404, "NOT_FOUND", "Affiliate not found")
		return true
	}
	id := parts[0]
	switch {
	case len(parts) == 1 && r.Method == "GET":
		out, err := adminAffiliate(ctx, m.DB, id)
		a.accountResult(w, r, out, err)
	case len(parts) == 1 && r.Method == "PATCH":
		var in financeapi.AffiliatePatch
		if !accountBody(w, r, "adminUpdateAffiliate", &in) {
			return true
		}
		out, err := a.accountTransaction(r, u.ID, "affiliate-update:"+id, in, false, func(tx *sql.Tx) (any, error) {
			if err := staffWriteTx(ctx, tx, u.ID, "clients.write"); err != nil {
				return nil, err
			}
			if _, err := lockAccountUser(ctx, tx, id); err != nil {
				return nil, err
			}
			if _, err := adminAffiliate(ctx, tx, id); err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO affiliate_profiles(user_id,rate_bp,status,note) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE rate_bp=VALUES(rate_bp),status=VALUES(status),note=VALUES(note)`, id, in.RateBp, in.Status, in.Note); err != nil {
				return nil, err
			}
			raw, _ := json.Marshal(in)
			if err := insertCurrencyAudit(ctx, tx, catalogAudit(u, "affiliate.updated", id, 0, string([]rune(string(raw))[:min(900, len([]rune(string(raw))))]))); err != nil {
				return nil, err
			}
			return adminAffiliate(ctx, tx, id)
		})
		a.accountResult(w, r, out, err)
	case len(parts) == 2 && parts[1] == "payouts" && r.Method == "POST":
		var in financeapi.AffiliatePayout
		if !accountBody(w, r, "adminAffiliatePayout", &in) {
			return true
		}
		if strings.TrimSpace(in.Reference) == "" {
			a.accountResult(w, r, nil, ErrCommerceInput)
			return true
		}
		out, err := a.accountTransaction(r, u.ID, "affiliate-payout:"+id, in, true, func(tx *sql.Tx) (any, error) {
			if err := staffWriteTx(ctx, tx, u.ID, "clients.write"); err != nil {
				return nil, err
			}
			if _, err := lockAccountUser(ctx, tx, id); err != nil {
				return nil, err
			}
			if _, err := adminAffiliate(ctx, tx, id); err != nil {
				return nil, err
			}
			if _, err := recordAffiliatePayout(ctx, tx, id, u.ID, in.Currency, in.AmountMinor, strings.TrimSpace(in.Reference), "manual"); err != nil {
				return nil, err
			}
			return adminAffiliate(ctx, tx, id)
		})
		a.accountResult(w, r, out, err)
	default:
		fail(w, r, 404, "NOT_FOUND", "Affiliate endpoint not found")
	}
	return true
}
func (a *App) adminAffiliates(w http.ResponseWriter, r *http.Request, u User) {
	if _, err := parseAccountQuery(r, "limit", "cursor", "q", "status"); err != nil {
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
	q := r.URL.Query().Get("q")
	status := r.URL.Query().Get("status")
	if len([]rune(q)) > 120 || (status != "" && status != "active" && status != "suspended") {
		a.accountResult(w, r, nil, ErrCommerceInput)
		return
	}
	m, err := a.accountDB()
	if err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	query := `SELECT a.user_id,a.created_at FROM affiliates a JOIN users u ON u.id=a.user_id LEFT JOIN affiliate_profiles p ON p.user_id=a.user_id WHERE 1=1`
	args := []any{}
	if q != "" {
		query += " AND (u.email LIKE ? OR u.name LIKE ? OR a.code LIKE ?)"
		args = append(args, "%"+q+"%", "%"+q+"%", "%"+q+"%")
	}
	if status != "" {
		query += " AND COALESCE(p.status,'active')=?"
		args = append(args, status)
	}
	if !cursor.At.IsZero() {
		query += " AND (a.created_at<? OR (a.created_at=? AND a.user_id<?))"
		args = append(args, cursor.At, cursor.At, cursor.ID)
	}
	args = append(args, limit+1)
	rows, err := m.DB.QueryContext(r.Context(), query+" ORDER BY a.created_at DESC,a.user_id DESC LIMIT ?", args...)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	type row struct {
		id string
		at time.Time
	}
	ids := []row{}
	for rows.Next() {
		var v row
		if err = rows.Scan(&v.id, &v.at); err != nil {
			break
		}
		ids = append(ids, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		a.internal(w, r, err)
		return
	}
	out := financeapi.AdminListAffiliatesResponse{Items: []financeapi.AdminAffiliateSummary{}}
	if len(ids) > limit {
		last := ids[limit-1]
		next := a.makeAccountCursor(r, u.ID, last.at, last.id)
		out.NextCursor = &next
		ids = ids[:limit]
	}
	for _, id := range ids {
		v, e := adminAffiliate(r.Context(), m.DB, id.id)
		if e != nil {
			err = e
			break
		}
		out.Items = append(out.Items, financeapi.AdminAffiliateSummary{ID: v.ID, Name: v.Name, Email: v.Email, Code: v.Code, Referrals: v.Referrals, Balances: v.Balances, RateBp: v.RateBp, Status: v.Status, CreatedAt: v.CreatedAt})
	}
	a.accountResult(w, r, out, err)
}
