package billing

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type currencyQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Required stored fields must be explicit even when their valid value is zero.
// In particular, a missing precision must never reinterpret existing prices.
func decodeStoredCurrencies(raw []byte) (CurrencyConfiguration, error) {
	invalid := errors.New("invalid stored currency configuration")
	shape := func(raw []byte, keys []string) (map[string]json.RawMessage, bool) {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || len(fields) != len(keys) {
			return nil, false
		}
		for _, key := range keys {
			value, ok := fields[key]
			if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return nil, false
			}
		}
		return fields, true
	}
	fields, ok := shape(raw, []string{"revision", "default_currency", "currencies"})
	if !ok {
		return CurrencyConfiguration{}, invalid
	}
	var currencies []json.RawMessage
	if json.Unmarshal(fields["currencies"], &currencies) != nil || len(currencies) < 1 || len(currencies) > 32 {
		return CurrencyConfiguration{}, invalid
	}
	for _, currency := range currencies {
		if _, ok := shape(currency, []string{"code", "name", "prefix", "suffix", "decimal_places", "enabled", "sort_order", "precision_locked"}); !ok {
			return CurrencyConfiguration{}, invalid
		}
	}
	var config CurrencyConfiguration
	if json.Unmarshal(raw, &config) != nil || !validCurrencyConfiguration(config) {
		return CurrencyConfiguration{}, invalid
	}
	return config, nil
}

func readCurrencies(ctx context.Context, q currencyQuerier, lock string) (CurrencyConfiguration, int64, error) {
	var config CurrencyConfiguration
	var revision int64
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT document,prices_revision FROM currency_settings WHERE id=1"+lock).Scan(&raw, &revision)
	if err == nil {
		config, err = decodeStoredCurrencies(raw)
	}
	if err == nil && (revision < 1 || revision > MaxMinor) {
		err = errors.New("invalid stored currency configuration")
	}
	return config, revision, err
}
func writeCurrencies(ctx context.Context, tx *sql.Tx, config CurrencyConfiguration, pricesRevision int64) error {
	raw, err := json.Marshal(cloneCurrencies(config, nil))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE currency_settings SET document=?,prices_revision=? WHERE id=1", raw, pricesRevision)
	return err
}
func currencyUsageSQL(ctx context.Context, q currencyQuerier) (map[string]CurrencyUsage, error) {
	out := map[string]CurrencyUsage{}
	for _, table := range []string{"plan_prices", "orders", "invoices", "services"} {
		condition := "0"
		switch table {
		case "orders":
			condition = "status='pending_payment'"
		case "invoices":
			condition = "status='unpaid'"
		case "services":
			condition = "status<>'terminated'"
		}
		rows, err := q.QueryContext(ctx, "SELECT currency,COUNT(*),COALESCE(SUM("+condition+"),0) FROM "+table+" GROUP BY currency")
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var code string
			var count, obligations int
			if err = rows.Scan(&code, &count, &obligations); err != nil {
				rows.Close()
				return nil, err
			}
			u := out[code]
			switch table {
			case "plan_prices":
				u.PlanPrices = count
			case "orders":
				u.Orders = count
				u.PendingOrders = obligations
			case "invoices":
				u.Invoices = count
				u.UnpaidInvoices = obligations
			case "services":
				u.Services = count
				u.OngoingServices = obligations
			}
			out[code] = u
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func insertCurrencyAudit(ctx context.Context, tx *sql.Tx, a Audit) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) VALUES(?,?,?,?,?,?,?)", a.ID, a.UserID, a.Action, a.Target, a.CreatedAt, a.ActorEmail, a.Detail)
	return err
}
func (m *MySQL) Currencies(ctx context.Context, admin bool) (CurrencyConfiguration, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return CurrencyConfiguration{}, err
	}
	defer tx.Rollback()
	config, _, err := readCurrencies(ctx, tx, "")
	if err != nil {
		return CurrencyConfiguration{}, err
	}
	var usage map[string]CurrencyUsage
	if admin {
		usage, err = currencyUsageSQL(ctx, tx)
		if err != nil {
			return CurrencyConfiguration{}, err
		}
	}
	err = tx.Commit()
	return cloneCurrencies(config, usage), err
}
func (m *MySQL) UpdateCurrencies(ctx context.Context, actor User, input CurrencyUpdate) (CurrencyConfiguration, error) {
	if actor.Role != "admin" {
		return CurrencyConfiguration{}, ErrCurrencyConfig
	}
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CurrencyConfiguration{}, err
	}
	defer tx.Rollback()
	old, pricesRevision, err := readCurrencies(ctx, tx, " FOR UPDATE")
	if err != nil {
		return CurrencyConfiguration{}, err
	}
	usage, err := currencyUsageSQL(ctx, tx)
	if err != nil {
		return CurrencyConfiguration{}, err
	}
	next, err := applyCurrencyUpdate(old, input, usage)
	if err != nil {
		return CurrencyConfiguration{}, err
	}
	var raw []byte
	var settings SiteSettings
	if err = tx.QueryRowContext(ctx, "SELECT document FROM site_settings WHERE id=1 FOR UPDATE").Scan(&raw); err != nil {
		return CurrencyConfiguration{}, err
	}
	if err = json.Unmarshal(raw, &settings); err != nil {
		return CurrencyConfiguration{}, err
	}
	if !ValidateSettings(settings) || settings.DefaultCurrency != old.DefaultCurrency {
		return CurrencyConfiguration{}, errors.New("invalid stored site settings")
	}
	if next.DefaultCurrency != settings.DefaultCurrency {
		if settings.Revision == MaxMinor {
			return CurrencyConfiguration{}, ErrSettingsConflict
		}
		settings.DefaultCurrency = next.DefaultCurrency
		settings.Revision++
		raw, _ = json.Marshal(settings)
		if _, err = tx.ExecContext(ctx, "UPDATE site_settings SET document=? WHERE id=1", raw); err != nil {
			return CurrencyConfiguration{}, err
		}
	}
	if err = writeCurrencies(ctx, tx, next, pricesRevision); err != nil {
		return CurrencyConfiguration{}, err
	}
	if err = insertCurrencyAudit(ctx, tx, currencyAudit(actor, "currencies.updated", "currencies", next.Revision)); err != nil {
		return CurrencyConfiguration{}, err
	}
	err = tx.Commit()
	return cloneCurrencies(next, usage), err
}
func readPlanPrices(ctx context.Context, q currencyQuerier, config CurrencyConfiguration, revision int64, code string) (PlanPricesView, error) {
	currency, ok := config.find(code)
	if !ok {
		return PlanPricesView{}, ErrCurrency
	}
	out := PlanPricesView{Revision: revision, Currency: code, CurrencyFormat: currency.CurrencyFormat, Plans: []PlanPriceView{}}
	rows, err := q.QueryContext(ctx, "SELECT p.id,p.name,p.available,pp.amount_minor,COALESCE(pp.available,FALSE) FROM plans p LEFT JOIN plan_prices pp ON pp.plan_id=p.id AND pp.currency=? ORDER BY p.id", code)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v PlanPriceView
		var amount sql.NullInt64
		if err = rows.Scan(&v.PlanID, &v.PlanName, &v.PlanAvailable, &amount, &v.Available); err != nil {
			return out, err
		}
		if amount.Valid {
			if !validAmount(amount.Int64) {
				return out, ErrPlanPrices
			}
			v.AmountMinor = amount.Int64
			v.Amount = priceString(amount.Int64, currency.DecimalPlaces)
		}
		out.Plans = append(out.Plans, v)
	}
	return out, rows.Err()
}
func (m *MySQL) PlanPrices(ctx context.Context, code string) (PlanPricesView, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return PlanPricesView{}, err
	}
	defer tx.Rollback()
	config, revision, err := readCurrencies(ctx, tx, "")
	if err != nil {
		return PlanPricesView{}, err
	}
	out, err := readPlanPrices(ctx, tx, config, revision, code)
	if err != nil {
		return out, err
	}
	err = tx.Commit()
	return out, err
}
func (m *MySQL) UpdatePlanPrices(ctx context.Context, actor User, input PlanPricesUpdate) (PlanPricesView, error) {
	if actor.Role != "admin" {
		return PlanPricesView{}, ErrPlanPrices
	}
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return PlanPricesView{}, err
	}
	defer tx.Rollback()
	config, revision, err := readCurrencies(ctx, tx, " FOR UPDATE")
	if err != nil {
		return PlanPricesView{}, err
	}
	if input.Revision != revision || revision == MaxMinor {
		return PlanPricesView{}, ErrPriceConflict
	}
	currency, ok := config.find(input.Currency)
	if !ok {
		return PlanPricesView{}, ErrCurrency
	}
	amounts, err := validatePriceUpdate(input, currency)
	if err != nil {
		return PlanPricesView{}, err
	}
	taxonomy, err := readTaxonomy(ctx, tx, " FOR SHARE")
	if err != nil {
		return PlanPricesView{}, err
	}
	for _, v := range input.Prices {
		var id, typeID, regionID, nodeID string
		if err = tx.QueryRowContext(ctx, "SELECT id,type_id,region_id,node_id FROM plans WHERE id=? FOR UPDATE", v.PlanID).Scan(&id, &typeID, &regionID, &nodeID); errors.Is(err, sql.ErrNoRows) {
			return PlanPricesView{}, ErrPlanPrices
		} else if err != nil {
			return PlanPricesView{}, err
		}
		if _, live := taxonomyNode(taxonomy, typeID, regionID, nodeID, true); v.PlanAvailable && !live {
			return PlanPricesView{}, ErrTaxonomy
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO plan_prices(plan_id,currency,amount_minor,available) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE amount_minor=VALUES(amount_minor),available=VALUES(available)", v.PlanID, input.Currency, amounts[v.PlanID], v.Available); err != nil {
			return PlanPricesView{}, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE plans SET available=? WHERE id=?", v.PlanAvailable, v.PlanID); err != nil {
			return PlanPricesView{}, err
		}
	}
	if !currency.PrecisionLocked {
		if config.Revision == MaxMinor {
			return PlanPricesView{}, ErrCurrencyConflict
		}
		for n := range config.Currencies {
			if config.Currencies[n].Code == currency.Code {
				config.Currencies[n].PrecisionLocked = true
			}
		}
		config.Revision++
	}
	revision++
	if err = writeCurrencies(ctx, tx, config, revision); err != nil {
		return PlanPricesView{}, err
	}
	if err = insertCurrencyAudit(ctx, tx, currencyAudit(actor, "plan.prices.updated", input.Currency, revision)); err != nil {
		return PlanPricesView{}, err
	}
	out, err := readPlanPrices(ctx, tx, config, revision, input.Currency)
	if err != nil {
		return out, err
	}
	err = tx.Commit()
	return out, err
}
func decodeCurrencySnapshot(raw []byte, code string) (CurrencyFormat, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 5 {
		return CurrencyFormat{}, errors.New("invalid stored currency snapshot")
	}
	for _, key := range []string{"code", "name", "prefix", "suffix", "decimal_places"} {
		value, ok := fields[key]
		if !ok || string(value) == "null" {
			return CurrencyFormat{}, errors.New("invalid stored currency snapshot")
		}
	}
	var f CurrencyFormat
	err := json.Unmarshal(raw, &f)
	if err != nil || !validCurrencyFormat(f) || f.Code != code {
		return CurrencyFormat{}, errors.New("invalid stored currency snapshot")
	}
	return f, nil
}
