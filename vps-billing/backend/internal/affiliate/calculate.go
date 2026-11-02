package affiliate

import (
	"aster/billing/internal/commerce"
	"aster/billing/internal/financeapi"
	"time"
)

func CurrencyLimit(rows []financeapi.AffiliateCurrencyAmount, currency string) (int64, bool) {
	for _, r := range rows {
		if r.Currency == currency {
			return r.AmountMinor, true
		}
	}
	return 0, false
}
func Rate(s financeapi.AffiliateSettings, override *int64, product string) int64 {
	if override != nil {
		return *override
	}
	for _, r := range s.ProductOverrides {
		if r.ProductID == product {
			return r.RateBp
		}
	}
	return s.DefaultRateBp
}

type Line struct {
	ID, Product      string
	Amount           int64
	Start, Activated time.Time
}
type ComputedLine struct {
	Line
	Cash, Rate, Commission int64
}

// Cash is allocated proportionally across positive invoice lines (discounts and
// credit reduce the cash pool). Cumulative integer division preserves every
// minor unit. Each line then earns floor(cash * rate / 10000), without FX.
// The affiliate override, including zero, precedes product and default rates.
func Calculate(s financeapi.AffiliateSettings, override *int64, kind, currency string, cash, used int64, lines []Line) ([]ComputedLine, error) {
	out := []ComputedLine{}
	var total int64
	for _, l := range lines {
		if l.Amount > 0 {
			var err error
			total, err = commerce.Add(total, l.Amount)
			if err != nil {
				return nil, err
			}
		}
	}
	if total == 0 || cash == 0 {
		return out, nil
	}
	if cash < 0 || cash > total {
		return nil, commerce.ErrInput
	}
	remaining := commerce.MaxMinor
	if cap, ok := CurrencyLimit(s.MaxPerReferral, currency); ok {
		remaining = max(int64(0), cap-used)
	}
	var cumulative, allocated int64
	for _, l := range lines {
		if l.Amount <= 0 {
			continue
		}
		cumulative += l.Amount
		share, err := commerce.FloorRatio(cash, cumulative, total)
		if err != nil {
			return nil, err
		}
		v := ComputedLine{Line: l, Cash: share - allocated, Rate: Rate(s, override, l.Product)}
		allocated = share
		eligible := s.Enabled && (kind == "order" || kind == "renewal" || kind == "upgrade")
		switch s.Scope {
		case "first_payment":
			eligible = eligible && kind == "order"
		case "first_months":
			eligible = eligible && !l.Activated.IsZero() && !l.Start.Before(l.Activated) && l.Start.Before(commerce.CalendarMonths(l.Activated, int(s.ScopeMonths), l.Activated.Day()))
		}
		if eligible {
			v.Commission, err = commerce.FloorRatio(v.Cash, v.Rate, 10000)
			if err != nil {
				return nil, err
			}
			v.Commission = min(v.Commission, remaining)
			remaining -= v.Commission
		}
		out = append(out, v)
	}
	return out, nil
}
