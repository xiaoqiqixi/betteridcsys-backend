package billing

import (
	"aster/billing/internal/commerce"
	api "aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/go-sql-driver/mysql"
)

func validateCoupon06(in api.AdminCreateCouponInput, config CurrencyConfiguration) error {
	raw, _ := json.Marshal(in)
	if api.ValidateInput("adminCreateCoupon", raw) != nil {
		return ErrCommerceInput
	}
	if in.Kind == "percent" && (in.PercentBp == nil || len(in.Fixed) != 0) || in.Kind == "fixed" && (in.PercentBp != nil || len(in.Fixed) == 0) {
		return ErrCommerceInput
	}
	if in.StartsAt != nil && in.EndsAt != nil {
		start, _ := time.Parse(time.RFC3339Nano, *in.StartsAt)
		end, _ := time.Parse(time.RFC3339Nano, *in.EndsAt)
		if !end.After(start) {
			return ErrCommerceInput
		}
	}
	seen := map[string]bool{}
	for _, v := range in.Fixed {
		c, ok := config.find(v.Currency)
		if !ok || !c.Enabled || seen[v.Currency] {
			return ErrCommerceInput
		}
		seen[v.Currency] = true
	}
	seen = map[string]bool{}
	for _, id := range in.AppliesTo.ProductIDs {
		if seen[id] {
			return ErrCommerceInput
		}
		seen[id] = true
	}
	seen = map[string]bool{}
	for _, cycle := range in.AppliesTo.Cycles {
		if seen[cycle] {
			return ErrCommerceInput
		}
		seen[cycle] = true
	}
	return nil
}
func readCoupons06(ctx context.Context, q currencyQuerier, code, lock string) ([]api.Coupon, error) {
	query := "SELECT id,document,revision,(SELECT COUNT(*) FROM coupon_redemptions r WHERE r.coupon_id=c.id) FROM coupons c"
	args := []any{}
	if code != "" {
		query += " WHERE code=?"
		args = append(args, code)
	}
	query += " ORDER BY code" + lock
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Coupon{}
	for rows.Next() {
		var id string
		var raw []byte
		var revision, used int64
		if err = rows.Scan(&id, &raw, &revision, &used); err != nil {
			return nil, err
		}
		var c api.Coupon
		if err = json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		c.ID, c.Revision, c.Used = id, revision, used
		out = append(out, c)
	}
	return out, rows.Err()
}
func (m *MySQL) Coupons06(ctx context.Context) ([]api.Coupon, error) {
	return readCoupons06(ctx, m.DB, "", "")
}
func (m *MySQL) SaveCoupon06(ctx context.Context, actor User, id string, in api.AdminCreateCouponInput) (api.Coupon, error) {
	var out api.Coupon
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	actor, err = lockActiveAdministrator(ctx, tx, actor.ID)
	if err != nil {
		return out, err
	}
	config, pricesRevision, err := readCurrencies(ctx, tx, " FOR UPDATE")
	if err != nil {
		return out, err
	}
	if err = validateCoupon06(in, config); err != nil {
		return out, err
	}
	revision, used := int64(1), int64(0)
	create := id == ""
	if !create {
		if err = tx.QueryRowContext(ctx, "SELECT revision FROM coupons WHERE id=? FOR UPDATE", id).Scan(&revision); errors.Is(err, sql.ErrNoRows) {
			return out, ErrNotFound
		} else if err != nil {
			return out, err
		}
		if revision != in.Revision || revision >= MaxMinor {
			return out, ErrRevisionConflict
		}
		revision++
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM coupon_redemptions WHERE coupon_id=?", id).Scan(&used); err != nil {
			return out, err
		}
	} else {
		id = ID()
	}
	for _, pid := range in.AppliesTo.ProductIDs {
		var exists string
		if err = tx.QueryRowContext(ctx, "SELECT id FROM products WHERE BINARY id=BINARY ? FOR SHARE", pid).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return out, ErrCommerceInput
		} else if err != nil {
			return out, err
		}
	}
	raw, _ := json.Marshal(in)
	if err = json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	out.ID, out.Revision, out.Used = id, revision, used
	raw, _ = json.Marshal(out)
	if create {
		_, err = tx.ExecContext(ctx, "INSERT INTO coupons(id,code,document,revision) VALUES(?,?,?,?)", id, out.Code, raw, revision)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE coupons SET code=?,document=?,revision=? WHERE id=?", out.Code, raw, revision, id)
	}
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return out, ErrCommerceInput
		}
		return out, err
	}
	for i := range config.Currencies {
		for _, fixed := range in.Fixed {
			if config.Currencies[i].Code == fixed.Currency {
				config.Currencies[i].PrecisionLocked = true
			}
		}
	}
	if err = writeCurrencies(ctx, tx, config, pricesRevision); err != nil {
		return out, err
	}
	if err = insertCurrencyAudit(ctx, tx, catalogAudit(actor, "coupon.saved", id, revision, "Coupon rules saved")); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
func quote06SQL(ctx context.Context, q currencyQuerier, actor User, in api.CreateQuoteInput, now time.Time, lock string) (commerce.CalculatedQuote, error) {
	var out commerce.CalculatedQuote
	config, _, err := readCurrencies(ctx, q, "")
	if err != nil {
		return out, err
	}
	currency, ok := config.find(in.Currency)
	if !ok || !currency.Enabled {
		return out, ErrCommerceInput
	}
	facts := commerce.CouponEligibility{Authenticated: actor.ID != ""}
	var coupon *api.Coupon
	if in.Coupon != "" {
		coupons, err := readCoupons06(ctx, q, in.Coupon, lock)
		if err != nil {
			return out, err
		}
		if len(coupons) == 1 {
			coupon = &coupons[0]
		}
		if actor.ID != "" && coupon != nil {
			if err = q.QueryRowContext(ctx, "SELECT COUNT(*) FROM coupon_redemptions WHERE coupon_id=? AND user_id=?", coupon.ID, actor.ID).Scan(&facts.ClientUses); err != nil {
				return out, err
			}
			if err = q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM orders WHERE user_id=? AND status<>'cancelled')", actor.ID).Scan(&facts.HasPriorOrder); err != nil {
				return out, err
			}
		}
	}
	// Lock the coupon before the selected products. Checkout uses the same order
	// so two carts containing the same products in different orders cannot deadlock.
	ids := make([]string, 0, len(in.Items))
	seen := map[string]bool{}
	for _, item := range in.Items {
		if !seen[item.ProductID] {
			ids = append(ids, item.ProductID)
			seen[item.ProductID] = true
		}
	}
	sort.Strings(ids)
	catalog := map[string]api.AdminProduct{}
	for _, id := range ids {
		products, err := readProducts06(ctx, q, false, id, in.Currency, "", "", lock)
		if err != nil {
			return out, err
		}
		for _, p := range products {
			catalog[p.ID] = p
		}
	}
	return commerce.CalculateQuote(in, catalog, int64(currency.DecimalPlaces), coupon, facts, now)
}
func (m *MySQL) Quote06(ctx context.Context, actor User, in api.CreateQuoteInput, now time.Time) (commerce.CalculatedQuote, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return commerce.CalculatedQuote{}, err
	}
	defer tx.Rollback()
	out, err := quote06SQL(ctx, tx, actor, in, now, "")
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
func (m *Memory) Coupons06(ctx context.Context) ([]api.Coupon, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := []api.Coupon{}
	for _, c := range m.catalog06Locked().coupons {
		out = append(out, clone06(c))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}
func (m *Memory) SaveCoupon06(ctx context.Context, actor User, id string, in api.AdminCreateCouponInput) (api.Coupon, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out api.Coupon
	if err := ctx.Err(); err != nil {
		return out, err
	}
	actor, err := m.activeAdministratorLocked(actor.ID)
	if err != nil {
		return out, err
	}
	if err = validateCoupon06(in, m.currencies); err != nil {
		return out, err
	}
	state := m.catalog06Locked()
	revision, used := int64(1), int64(0)
	if id != "" {
		old, ok := state.coupons[id]
		if !ok {
			return out, ErrNotFound
		}
		if old.Revision != in.Revision || in.Revision >= MaxMinor {
			return out, ErrRevisionConflict
		}
		revision, used = old.Revision+1, old.Used
	} else {
		id = ID()
	}
	for cid, c := range state.coupons {
		if cid != id && c.Code == in.Code {
			return out, ErrCommerceInput
		}
	}
	for _, pid := range in.AppliesTo.ProductIDs {
		if _, ok := state.products[pid]; !ok {
			return out, ErrCommerceInput
		}
	}
	raw, _ := json.Marshal(in)
	_ = json.Unmarshal(raw, &out)
	out.ID, out.Revision, out.Used = id, revision, used
	state.coupons[id] = clone06(out)
	for i := range m.currencies.Currencies {
		for _, p := range in.Fixed {
			if m.currencies.Currencies[i].Code == p.Currency {
				m.currencies.Currencies[i].PrecisionLocked = true
			}
		}
	}
	m.audits = append(m.audits, catalogAudit(actor, "coupon.saved", id, revision, "Coupon rules saved"))
	return out, nil
}
func (m *Memory) Quote06(ctx context.Context, actor User, in api.CreateQuoteInput, now time.Time) (commerce.CalculatedQuote, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out commerce.CalculatedQuote
	if err := ctx.Err(); err != nil {
		return out, err
	}
	currency, ok := m.currencies.find(in.Currency)
	if !ok || !currency.Enabled {
		return out, ErrCommerceInput
	}
	state := m.catalog06Locked()
	products := map[string]api.AdminProduct{}
	for _, p := range state.products {
		if p, ok := m.projectProduct06Locked(p, false, in.Currency); ok {
			products[p.ID] = p
		}
	}
	var coupon *api.Coupon
	for _, c := range state.coupons {
		if c.Code == in.Coupon {
			copy := c
			coupon = &copy
			break
		}
	}
	facts := commerce.CouponEligibility{Authenticated: actor.ID != ""}
	if actor.ID != "" {
		for _, order := range m.orders {
			if order.UserID == actor.ID && order.Status != "cancelled" {
				facts.HasPriorOrder = true
				break
			}
		}
	}
	return commerce.CalculateQuote(in, products, int64(currency.DecimalPlaces), coupon, facts, now)
}
