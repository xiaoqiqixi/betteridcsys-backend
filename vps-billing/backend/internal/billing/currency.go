package billing

import (
	"encoding/json"
	"errors"
)

// Keep omitted currency distinct from an explicit selection. An omitted value
// resolves only for a new order, so a retry survives a changed site default.
// Omitting the field also preserves fingerprints written before currency support.
func orderFingerprint(plan, hostname, currency string) string {
	raw, _ := json.Marshal(struct {
		PlanID   string `json:"plan_id"`
		Hostname string `json:"hostname"`
		Currency string `json:"currency,omitempty"`
	}{plan, hostname, currency})
	return digest(string(raw))
}

func validAmount(amount int64) bool { return amount > 0 && amount <= MaxMinor }

func addAmount(total *int64, amount int64) bool {
	if amount < 0 || amount > MaxMinor || *total > MaxMinor-amount {
		return false
	}
	*total += amount
	return true
}

var ErrPriceChanged = errors.New("quoted price changed")
var ErrInvalidQuote = errors.New("invalid price quote")

type PriceQuote struct {
	PriceMinor    int64 `json:"price_minor"`
	DecimalPlaces int   `json:"decimal_places"`
	PriceRevision int64 `json:"price_revision"`
}

func (q PriceQuote) valid() bool {
	return validAmount(q.PriceMinor) && q.DecimalPlaces >= 0 && q.DecimalPlaces <= 6 && q.PriceRevision >= 1 && q.PriceRevision <= MaxMinor
}
func (q PriceQuote) matches(amount int64, decimals int, revision int64) bool {
	return q.PriceMinor == amount && q.DecimalPlaces == decimals && q.PriceRevision == revision
}
