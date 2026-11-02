package billing

import (
	"aster/billing/internal/commerce"
	api "aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrInsufficientCredit = errors.New("insufficient credit")

type commerceCreditStore interface {
	Credit06(context.Context, string) (api.CreditSummary, error)
	CreditEntries06(context.Context, string, int64, int) (api.ListCreditEntriesResponse, error)
}

func stamp06(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func accountMoney06(ctx context.Context, q currencyQuerier, uid, lock string) (api.Money, error) {
	config, _, err := readCurrencies(ctx, q, "")
	if err != nil {
		return api.Money{}, err
	}
	var currency string
	var disabled bool
	err = q.QueryRowContext(ctx, "SELECT currency,disabled FROM users WHERE id=?"+lock, uid).Scan(&currency, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Money{}, ErrNotFound
	}
	if err != nil {
		return api.Money{}, err
	}
	c, ok := config.find(currency)
	if !ok {
		return api.Money{}, ErrCurrency
	}
	return commerce.Money(currency, int64(c.DecimalPlaces), 0), nil
}

func creditBalance06(ctx context.Context, q currencyQuerier, uid string, money api.Money) (api.Money, int64, error) {
	var sequence int64
	err := q.QueryRowContext(ctx, "SELECT sequence,balance_after_minor,currency,decimal_places FROM credit_ledger WHERE user_id=? ORDER BY sequence DESC LIMIT 1", uid).Scan(&sequence, &money.AmountMinor, &money.Currency, &money.DecimalPlaces)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return money, sequence, err
}

// Caller holds users(uid) FOR UPDATE. Every credit mutation, including invoice
// settlement and affiliate payouts, must use that same lock before this append.
func appendCredit06(ctx context.Context, tx *sql.Tx, uid, kind string, delta int64, invoiceID *string, description string, now time.Time) (api.CreditEntry, error) {
	var out api.CreditEntry
	money, err := accountMoney06(ctx, tx, uid, "")
	if err != nil {
		return out, err
	}
	balance, sequence, err := creditBalance06(ctx, tx, uid, money)
	if err != nil {
		return out, err
	}
	if balance.Currency != money.Currency || balance.DecimalPlaces != money.DecimalPlaces || sequence >= MaxMinor {
		return out, ErrPaymentState
	}
	next, err := commerce.Add(balance.AmountMinor, delta)
	if err != nil {
		return out, err
	}
	if next < 0 {
		return out, ErrInsufficientCredit
	}
	out = api.CreditEntry{ID: ID(), Kind: kind, Amount: commerce.Money(money.Currency, money.DecimalPlaces, delta), BalanceAfter: commerce.Money(money.Currency, money.DecimalPlaces, next), InvoiceID: invoiceID, Description: description, CreatedAt: stamp06(now)}
	_, err = tx.ExecContext(ctx, "INSERT INTO credit_ledger(user_id,sequence,id,kind,amount_minor,balance_after_minor,currency,decimal_places,invoice_id,description,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)", uid, sequence+1, out.ID, kind, delta, next, money.Currency, money.DecimalPlaces, invoiceID, description, now.UTC())
	if err == nil {
		// Credit is denominated in the account currency. An adjustment before
		// the first invoice must not permit a later implicit currency conversion.
		_, err = tx.ExecContext(ctx, "UPDATE users SET currency_locked=TRUE WHERE id=?", uid)
	}
	return out, err
}

// AdjustCredit06 exposes the same append-only, idempotent operation to account
// administration. Negative adjustments cannot take an account below zero.
func (m *MySQL) AdjustCredit06(ctx context.Context, actor User, uid string, delta int64, reason, key string) (api.CreditEntry, error) {
	if utf8.RuneCountInString(reason) < 3 || utf8.RuneCountInString(reason) > 500 || delta < -MaxMinor || delta > MaxMinor || !validCancellationID(uid) || !keyPattern.MatchString(key) {
		return api.CreditEntry{}, ErrCommerceInput
	}
	raw, _ := json.Marshal([]any{uid, delta, reason})
	fingerprint := digest(string(raw))
	return retryPayment(ctx, func() (api.CreditEntry, error) {
		actor := actor // Reset authorization input for every transaction attempt.
		var out api.CreditEntry
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		actor, err = lockActiveAdministrator(ctx, tx, actor.ID)
		if err != nil {
			return out, err
		}
		config, revision, err := readCurrencies(ctx, tx, " FOR UPDATE")
		if err != nil {
			return out, err
		}
		money, err := accountMoney06(ctx, tx, uid, " FOR UPDATE")
		if err != nil {
			return out, err
		}
		var role string
		if err = tx.QueryRowContext(ctx, "SELECT role FROM users WHERE id=?", uid).Scan(&role); err != nil {
			return out, err
		}
		if role != "customer" {
			return out, ErrNotFound
		}
		replay, err := requestReplay06(ctx, tx, actor.ID, "credit:"+uid, key, fingerprint, &out)
		if err != nil || replay {
			return out, err
		}
		for i := range config.Currencies {
			if config.Currencies[i].Code == money.Currency {
				config.Currencies[i].PrecisionLocked = true
			}
		}
		if err = writeCurrencies(ctx, tx, config, revision); err != nil {
			return out, err
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		out, err = appendCredit06(ctx, tx, uid, "adjustment", delta, nil, reason, now)
		if err != nil {
			return out, err
		}
		if err = insertCurrencyAudit(ctx, tx, catalogAudit(actor, "credit.adjusted", uid, 0, reason)); err != nil {
			return out, err
		}
		if err = recordRequest06(ctx, tx, actor.ID, "credit:"+uid, key, fingerprint, out); err != nil {
			return out, err
		}
		return out, tx.Commit()
	})
}

func creditSummary06(ctx context.Context, q currencyQuerier, uid string) (api.CreditSummary, error) {
	var out api.CreditSummary
	money, err := accountMoney06(ctx, q, uid, "")
	if err != nil {
		return out, err
	}
	out.Balance, _, err = creditBalance06(ctx, q, uid, money)
	if err != nil {
		return out, err
	}
	scale := int64(1)
	for range money.DecimalPlaces {
		scale *= 10
	}
	low, high := 10*scale, 10000*scale
	presets := []int64{10 * scale, 50 * scale, 100 * scale}
	var raw []byte
	err = q.QueryRowContext(ctx, "SELECT min_minor,max_minor,presets FROM credit_limits WHERE currency=?", money.Currency).Scan(&low, &high, &raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err == nil {
		if err = json.Unmarshal(raw, &presets); err != nil {
			return out, err
		}
	}
	out.TopUp.Min = commerce.Money(money.Currency, money.DecimalPlaces, low)
	out.TopUp.Max = commerce.Money(money.Currency, money.DecimalPlaces, high)
	out.TopUp.Presets = []api.Money{}
	for _, n := range presets {
		if n < low || n > high {
			return out, ErrCommerceInput
		}
		out.TopUp.Presets = append(out.TopUp.Presets, commerce.Money(money.Currency, money.DecimalPlaces, n))
	}
	return out, nil
}
func (m *MySQL) Credit06(ctx context.Context, uid string) (api.CreditSummary, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return api.CreditSummary{}, err
	}
	defer tx.Rollback()
	out, err := creditSummary06(ctx, tx, uid)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
func (m *MySQL) CreditEntries06(ctx context.Context, uid string, before int64, limit int) (api.ListCreditEntriesResponse, error) {
	out := api.ListCreditEntriesResponse{Items: []api.CreditEntry{}}
	if limit < 1 || limit > 100 || before < 0 {
		return out, ErrCommerceInput
	}
	if _, err := accountMoney06(ctx, m.DB, uid, ""); err != nil {
		return out, err
	}
	query := "SELECT id,sequence,kind,amount_minor,balance_after_minor,currency,decimal_places,invoice_id,description,created_at FROM credit_ledger WHERE user_id=?"
	args := []any{uid}
	if before > 0 {
		query += " AND sequence<?"
		args = append(args, before)
	}
	args = append(args, limit+1)
	rows, err := m.DB.QueryContext(ctx, query+" ORDER BY sequence DESC LIMIT ?", args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	var last int64
	for rows.Next() {
		var e api.CreditEntry
		var sequence int64
		var at time.Time
		if err = rows.Scan(&e.ID, &sequence, &e.Kind, &e.Amount.AmountMinor, &e.BalanceAfter.AmountMinor, &e.Amount.Currency, &e.Amount.DecimalPlaces, &e.InvoiceID, &e.Description, &at); err != nil {
			return out, err
		}
		if len(out.Items) == limit {
			cursor := strconv.FormatInt(last, 10)
			out.NextCursor = &cursor
			break
		}
		e.BalanceAfter.Currency, e.BalanceAfter.DecimalPlaces = e.Amount.Currency, e.Amount.DecimalPlaces
		e.CreatedAt = stamp06(at)
		out.Items = append(out.Items, e)
		last = sequence
	}
	return out, rows.Err()
}

func (a *App) credit06HTTP(w http.ResponseWriter, r *http.Request, u User, p string) bool {
	admin := strings.HasPrefix(p, "/admin/clients/") && strings.HasSuffix(p, "/credit")
	if p != "/credit" && p != "/credit/entries" && !admin {
		return false
	}
	if r.Method != "GET" {
		return false
	}
	uid := u.ID
	if admin {
		uid = strings.TrimSuffix(strings.TrimPrefix(p, "/admin/clients/"), "/credit")
		if !validCancellationID(uid) {
			fail(w, r, 404, "NOT_FOUND", "Client not found")
			return true
		}
	}
	s, ok := a.Store.(commerceCreditStore)
	if !ok {
		fail(w, r, 503, "FEATURE_UNAVAILABLE", "Credit storage is unavailable")
		return true
	}
	if p == "/credit" {
		out, err := s.Credit06(r.Context(), uid)
		if err != nil {
			a.commerceError(w, r, err)
		} else {
			jsonOut(w, 200, out)
		}
		return true
	}
	limit := 50
	before := int64(0)
	var err error
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
	}
	if err == nil {
		if v := r.URL.Query().Get("cursor"); v != "" {
			before, err = strconv.ParseInt(v, 10, 64)
			if before <= 0 {
				err = ErrCommerceInput
			}
		}
	}
	if err != nil || limit < 1 || limit > 100 {
		a.commerceError(w, r, ErrCommerceInput)
		return true
	}
	out, err := s.CreditEntries06(r.Context(), uid, before, limit)
	if err != nil {
		a.commerceError(w, r, err)
	} else {
		jsonOut(w, 200, out)
	}
	return true
}
