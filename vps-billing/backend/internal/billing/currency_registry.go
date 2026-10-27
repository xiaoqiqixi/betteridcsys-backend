package billing

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	ErrCurrencyConfig    = errors.New("invalid currency configuration")
	ErrCurrencyConflict  = errors.New("currency configuration changed")
	ErrCurrencyDisabled  = errors.New("currency disabled")
	ErrCurrencyPrecision = errors.New("currency precision locked")
	ErrPlanPrices        = errors.New("invalid plan prices")
	ErrPriceConflict     = errors.New("plan prices changed")
)

type CurrencyFormat struct {
	Code          string `json:"code"`
	Name          string `json:"name"`
	Prefix        string `json:"prefix"`
	Suffix        string `json:"suffix"`
	DecimalPlaces int    `json:"decimal_places"`
}
type CurrencyUsage struct {
	PlanPrices      int `json:"plan_prices"`
	Orders          int `json:"orders"`
	Invoices        int `json:"invoices"`
	Services        int `json:"services"`
	PendingOrders   int `json:"pending_orders"`
	UnpaidInvoices  int `json:"unpaid_invoices"`
	OngoingServices int `json:"ongoing_services"`
}

func (u CurrencyUsage) used() bool {
	return u.PlanPrices > 0 || u.Orders > 0 || u.Invoices > 0 || u.Services > 0
}
func (u CurrencyUsage) obligated() bool {
	return u.PendingOrders > 0 || u.UnpaidInvoices > 0 || u.OngoingServices > 0
}

type Currency struct {
	CurrencyFormat
	Enabled         bool           `json:"enabled"`
	SortOrder       int            `json:"sort_order"`
	PrecisionLocked bool           `json:"precision_locked"`
	Usage           *CurrencyUsage `json:"usage,omitempty"`
}
type CurrencyConfiguration struct {
	Revision        int64      `json:"revision"`
	DefaultCurrency string     `json:"default_currency"`
	Currencies      []Currency `json:"currencies"`
}
type CurrencyInput struct {
	Code          string `json:"code"`
	Name          string `json:"name"`
	Prefix        string `json:"prefix"`
	Suffix        string `json:"suffix"`
	DecimalPlaces int    `json:"decimal_places"`
	Enabled       bool   `json:"enabled"`
	SortOrder     int    `json:"sort_order"`
}
type CurrencyUpdate struct {
	Revision        int64           `json:"revision"`
	DefaultCurrency string          `json:"default_currency"`
	Currencies      []CurrencyInput `json:"currencies"`
}
type CurrencyInUse struct {
	Code  string
	Usage CurrencyUsage
}

func (e *CurrencyInUse) Error() string { return "currency has outstanding obligations" }

func DefaultCurrencyFormat(code string) CurrencyFormat {
	switch code {
	case "CNY":
		return CurrencyFormat{"CNY", "人民币", "¥", "", 2}
	case "USD":
		return CurrencyFormat{"USD", "US Dollar", "$", "", 2}
	}
	return CurrencyFormat{}
}
func DefaultCurrencyConfiguration() CurrencyConfiguration {
	return CurrencyConfiguration{Revision: 1, DefaultCurrency: "CNY", Currencies: []Currency{{CurrencyFormat: DefaultCurrencyFormat("CNY"), Enabled: true, PrecisionLocked: true}}}
}
func validCurrencyCode(code string) bool {
	if len(code) != 3 {
		return false
	}
	for _, c := range []byte(code) {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}
func validCurrencyFormat(f CurrencyFormat) bool {
	return validCurrencyCode(f.Code) && strings.TrimSpace(f.Name) != "" && validText(f.Name, 80, false) && validText(f.Prefix, 24, false) && validText(f.Suffix, 24, false) && f.DecimalPlaces >= 0 && f.DecimalPlaces <= 6
}
func (c CurrencyConfiguration) find(code string) (Currency, bool) {
	for _, v := range c.Currencies {
		if v.Code == code {
			return v, true
		}
	}
	return Currency{}, false
}
func validCurrencyConfiguration(c CurrencyConfiguration) bool {
	if c.Revision < 1 || c.Revision > MaxMinor || len(c.Currencies) < 1 || len(c.Currencies) > 32 {
		return false
	}
	seen := map[string]bool{}
	for _, v := range c.Currencies {
		if seen[v.Code] || !validCurrencyFormat(v.CurrencyFormat) || v.SortOrder < 0 || v.SortOrder > 10000 {
			return false
		}
		seen[v.Code] = true
	}
	d, ok := c.find(c.DefaultCurrency)
	return ok && d.Enabled
}
func cloneCurrencies(c CurrencyConfiguration, usage map[string]CurrencyUsage) CurrencyConfiguration {
	c.Currencies = append([]Currency{}, c.Currencies...)
	for n := range c.Currencies {
		c.Currencies[n].Usage = nil
		if usage != nil {
			u := usage[c.Currencies[n].Code]
			c.Currencies[n].Usage = &u
		}
	}
	sort.Slice(c.Currencies, func(i, j int) bool {
		a, b := c.Currencies[i], c.Currencies[j]
		if a.SortOrder != b.SortOrder {
			return a.SortOrder < b.SortOrder
		}
		return a.Code < b.Code
	})
	return c
}
func applyCurrencyUpdate(old CurrencyConfiguration, input CurrencyUpdate, usage map[string]CurrencyUsage) (CurrencyConfiguration, error) {
	if input.Revision != old.Revision || old.Revision == MaxMinor {
		return CurrencyConfiguration{}, ErrCurrencyConflict
	}
	next := CurrencyConfiguration{Revision: old.Revision + 1, DefaultCurrency: input.DefaultCurrency, Currencies: []Currency{}}
	for _, v := range input.Currencies {
		f := CurrencyFormat{v.Code, v.Name, v.Prefix, v.Suffix, v.DecimalPlaces}
		prior, exists := old.find(v.Code)
		if !exists && usage[v.Code].used() {
			return CurrencyConfiguration{}, ErrCurrencyConfig
		}
		locked := exists && (prior.PrecisionLocked || usage[v.Code].used())
		if locked && prior.DecimalPlaces != v.DecimalPlaces {
			return CurrencyConfiguration{}, ErrCurrencyPrecision
		}
		if exists && prior.Enabled && !v.Enabled && usage[v.Code].obligated() {
			return CurrencyConfiguration{}, &CurrencyInUse{v.Code, usage[v.Code]}
		}
		next.Currencies = append(next.Currencies, Currency{CurrencyFormat: f, Enabled: v.Enabled, SortOrder: v.SortOrder, PrecisionLocked: locked})
	}
	if !validCurrencyConfiguration(next) {
		return CurrencyConfiguration{}, ErrCurrencyConfig
	}
	for _, prior := range old.Currencies {
		if _, exists := next.find(prior.Code); !exists {
			return CurrencyConfiguration{}, ErrCurrencyConfig
		}
	}
	return cloneCurrencies(next, nil), nil
}

// Parse a positive operator price without floating point, exponents or rounding.
func ParsePrice(value string, decimals int) (int64, error) {
	if decimals < 0 || decimals > 6 || len(value) < 1 || len(value) > 32 {
		return 0, ErrPlanPrices
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || len(parts[0]) == 0 || (len(parts[0]) > 1 && parts[0][0] == '0') {
		return 0, ErrPlanPrices
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if decimals == 0 || len(fraction) < 1 || len(fraction) > decimals {
			return 0, ErrPlanPrices
		}
	}
	for _, part := range parts {
		for _, c := range []byte(part) {
			if c < '0' || c > '9' {
				return 0, ErrPlanPrices
			}
		}
	}
	digits := parts[0] + fraction + strings.Repeat("0", decimals-len(fraction))
	amount, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || !validAmount(amount) {
		return 0, ErrPlanPrices
	}
	return amount, nil
}
func priceString(amount int64, decimals int) string {
	raw := strconv.FormatInt(amount, 10)
	if decimals == 0 {
		return raw
	}
	if len(raw) <= decimals {
		raw = strings.Repeat("0", decimals+1-len(raw)) + raw
	}
	return raw[:len(raw)-decimals] + "." + raw[len(raw)-decimals:]
}

type PlanPriceView struct {
	PlanID        string `json:"plan_id"`
	PlanName      string `json:"plan_name"`
	PlanAvailable bool   `json:"plan_available"`
	Amount        string `json:"amount"`
	AmountMinor   int64  `json:"amount_minor"`
	Available     bool   `json:"available"`
}
type PlanPricesView struct {
	Revision       int64           `json:"revision"`
	Currency       string          `json:"currency"`
	CurrencyFormat CurrencyFormat  `json:"currency_format"`
	Plans          []PlanPriceView `json:"plans"`
}
type PlanPriceInput struct {
	PlanID        string `json:"plan_id"`
	Amount        string `json:"amount"`
	Available     bool   `json:"available"`
	PlanAvailable bool   `json:"plan_available"`
}
type PlanPricesUpdate struct {
	Revision int64            `json:"revision"`
	Currency string           `json:"currency"`
	Prices   []PlanPriceInput `json:"prices"`
}

func validatePriceUpdate(input PlanPricesUpdate, currency Currency) (map[string]int64, error) {
	if input.Revision < 1 || input.Revision >= MaxMinor || len(input.Prices) < 1 || len(input.Prices) > 500 || input.Currency != currency.Code {
		return nil, ErrPlanPrices
	}
	out := map[string]int64{}
	for _, v := range input.Prices {
		if v.PlanID == "" || len(v.PlanID) > 64 {
			return nil, ErrPlanPrices
		}
		if _, ok := out[v.PlanID]; ok {
			return nil, ErrPlanPrices
		}
		n, err := ParsePrice(v.Amount, currency.DecimalPlaces)
		if err != nil {
			return nil, err
		}
		out[v.PlanID] = n
	}
	return out, nil
}
func currencyAudit(actor User, action, target string, revision int64) Audit {
	return Audit{ID: ID(), UserID: actor.ID, Action: action, Target: target, CreatedAt: time.Now().UTC(), ActorEmail: actor.Email, Detail: fmt.Sprintf("configuration revision %d; historical amounts and currency snapshots unchanged", revision)}
}
