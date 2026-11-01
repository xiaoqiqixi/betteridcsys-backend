package commerce

import (
	api "aster/billing/internal/financeapi"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"
)

var ErrProductUnavailable = errors.New("product unavailable")
var ErrQuoteChanged = errors.New("quote changed or expired")

type CouponEligibility struct {
	Authenticated, HasPriorOrder bool
	ClientUses                   int64
}
type QuotedItem struct {
	Product                                                  api.AdminProduct
	Input                                                    api.CartItemInput
	SubtotalMinor, DiscountMinor, TotalMinor, RecurringMinor int64
	OptionsSummary                                           []string
	OSImage                                                  *api.OsImage
}
type CalculatedQuote struct {
	Quote        api.Quote
	Items        []QuotedItem
	pricingInput []api.CartItemInput
	revisions    []int64
}

func localText(text api.LocalizedText) string {
	if text["en"] != "" {
		return text["en"]
	}
	return text["zh-CN"]
}
func findPrice(prices []api.PricePoint, currency, cycle string) (api.PricePoint, bool) {
	for _, p := range prices {
		if p.Currency == currency && p.Cycle == cycle {
			return p, true
		}
	}
	return api.PricePoint{}, false
}
func couponValid(c api.Coupon, currency string, facts CouponEligibility, now time.Time) bool {
	if !c.Active || c.MaxUses != nil && c.Used >= *c.MaxUses || c.MaxUsesPerClient != nil && facts.ClientUses >= *c.MaxUsesPerClient || c.NewClientsOnly && (!facts.Authenticated || facts.HasPriorOrder) {
		return false
	}
	if c.StartsAt != nil {
		at, err := time.Parse(time.RFC3339Nano, *c.StartsAt)
		if err != nil || now.Before(at) {
			return false
		}
	}
	if c.EndsAt != nil {
		at, err := time.Parse(time.RFC3339Nano, *c.EndsAt)
		if err != nil || !now.Before(at) {
			return false
		}
	}
	if c.Kind == "percent" {
		return c.PercentBp != nil && *c.PercentBp > 0 && *c.PercentBp <= 10000
	}
	if c.Kind == "fixed" {
		for _, v := range c.Fixed {
			if v.Currency == currency && v.AmountMinor > 0 {
				return true
			}
		}
	}
	return false
}
func couponApplies(c api.Coupon, pid, cycle string) bool {
	return (len(c.AppliesTo.ProductIDs) == 0 || slices.Contains(c.AppliesTo.ProductIDs, pid)) && (len(c.AppliesTo.Cycles) == 0 || slices.Contains(c.AppliesTo.Cycles, cycle))
}
func fixedCoupon(c api.Coupon, currency string) int64 {
	for _, v := range c.Fixed {
		if v.Currency == currency {
			return v.AmountMinor
		}
	}
	return 0
}

// CalculateQuote is pure: it neither reserves inventory nor redeems coupons.
// All inputs are snapshots. Prices are independently selected by currency and
// cycle; unavailable combinations never acquire a converted or fallback price.
func CalculateQuote(input api.CreateQuoteInput, products map[string]api.AdminProduct, decimals int64, coupon *api.Coupon, facts CouponEligibility, now time.Time) (CalculatedQuote, error) {
	var out CalculatedQuote
	if len(input.Items) < 1 || len(input.Items) > 20 || decimals < 0 || decimals > 6 {
		return out, ErrInput
	}
	out.Quote = api.Quote{Currency: input.Currency, Lines: []api.QuoteLine{}, Renewals: make([]struct {
		ItemIndex int64     `json:"item_index"`
		Cycle     string    `json:"cycle"`
		Amount    api.Money `json:"amount"`
	}, 0), Subtotal: Money(input.Currency, decimals, 0), Discount: Money(input.Currency, decimals, 0), Total: Money(input.Currency, decimals, 0), ExpiresAt: now.UTC().Truncate(time.Second).Add(10 * time.Minute).Format(time.RFC3339)}
	out.Items = []QuotedItem{}
	out.pricingInput = []api.CartItemInput{}
	stockUsed := map[string]int64{}
	for index, cart := range input.Items {
		p, ok := products[cart.ProductID]
		if !ok || !p.Available || p.Visibility != "public" || cart.Quantity < 1 || cart.Quantity > 20 {
			return out, ErrProductUnavailable
		}
		stockUsed[p.ID] += cart.Quantity
		if p.Stock != nil && stockUsed[p.ID] > *p.Stock {
			return out, ErrProductUnavailable
		}
		base, ok := findPrice(p.Prices, input.Currency, cart.Cycle)
		if !ok || base.DecimalPlaces != decimals {
			return out, ErrProductUnavailable
		}
		item := QuotedItem{Product: p, Input: cart, OptionsSummary: []string{}}
		// Secrets are never part of a quote, snapshots, digests or diagnostics.
		item.Input.RootPassword = ""
		cart.RootPassword = ""
		out.pricingInput = append(out.pricingInput, cart)
		out.revisions = append(out.revisions, p.Revision)
		idx := int64(index)
		appendLine := func(kind, description string, amount int64) error {
			if amount < 0 || amount > MaxMinor {
				return ErrMoney
			}
			var err error
			item.SubtotalMinor, err = Add(item.SubtotalMinor, amount)
			if err != nil {
				return err
			}
			out.Quote.Lines = append(out.Quote.Lines, api.QuoteLine{Kind: kind, ItemIndex: &idx, Description: description, Amount: Money(input.Currency, decimals, amount)})
			return nil
		}
		recurring, err := Multiply(base.RecurringMinor, cart.Quantity)
		if err != nil {
			return out, err
		}
		item.RecurringMinor = recurring
		if err = appendLine("product", localText(p.Name), recurring); err != nil {
			return out, err
		}
		setup, err := Multiply(base.SetupMinor, cart.Quantity)
		if err != nil {
			return out, err
		}
		used := map[string]bool{}
		for _, option := range p.Options {
			value, exists := cart.Options[option.Key]
			if exists {
				used[option.Key] = true
			}
			if alternate, ok := cart.Options[option.ID]; ok && option.ID != option.Key {
				if exists {
					return out, ErrInput
				}
				value, exists = alternate, true
				used[option.ID] = true
			}
			if !exists {
				if option.Required {
					return out, ErrInput
				}
				continue
			}
			var price api.PricePoint
			multiplier := int64(1)
			charge := false
			switch option.Kind {
			case "select":
				choiceID, ok := value.(string)
				if !ok {
					return out, ErrInput
				}
				found := false
				for _, choice := range option.Choices {
					if choice.ID == choiceID {
						price, found = findPrice(choice.Prices, input.Currency, cart.Cycle)
						item.OptionsSummary = append(item.OptionsSummary, localText(option.Label)+": "+localText(choice.Label))
						break
					}
				}
				if !found {
					return out, ErrProductUnavailable
				}
				charge = true
			case "quantity":
				var quantity int64
				switch v := value.(type) {
				case json.Number:
					quantity, err = strconv.ParseInt(string(v), 10, 64)
				case float64:
					if v < 0 || v > float64(MaxMinor) || v != float64(int64(v)) {
						return out, ErrInput
					}
					quantity = int64(v)
				case int64:
					quantity = v
				case int:
					quantity = int64(v)
				default:
					return out, ErrInput
				}
				if err != nil || quantity < option.Min || quantity > option.Max {
					return out, ErrInput
				}
				price, ok = findPrice(option.UnitPrices, input.Currency, cart.Cycle)
				if !ok {
					return out, ErrProductUnavailable
				}
				multiplier = quantity
				charge = true
				item.OptionsSummary = append(item.OptionsSummary, fmt.Sprintf("%s: %d", localText(option.Label), quantity))
			case "os_image":
				imageID, ok := value.(string)
				if !ok {
					return out, ErrInput
				}
				for _, image := range p.OSImages {
					if image.ID == imageID && image.MinDiskGB <= p.Specs.DiskGB {
						copy := image
						item.OSImage = &copy
						item.OptionsSummary = append(item.OptionsSummary, image.Name)
						break
					}
				}
				if item.OSImage == nil {
					return out, ErrProductUnavailable
				}
			default:
				return out, ErrInput
			}
			if charge {
				if price.DecimalPlaces != decimals {
					return out, ErrMoney
				}
				units, err := Multiply(multiplier, cart.Quantity)
				if err != nil {
					return out, err
				}
				amount, err := Multiply(price.RecurringMinor, units)
				if err != nil {
					return out, err
				}
				if err = appendLine("option", localText(option.Label), amount); err != nil {
					return out, err
				}
				item.RecurringMinor, err = Add(item.RecurringMinor, amount)
				if err != nil {
					return out, err
				}
				fee, err := Multiply(price.SetupMinor, units)
				if err != nil {
					return out, err
				}
				setup, err = Add(setup, fee)
				if err != nil {
					return out, err
				}
			}
		}
		if len(used) != len(cart.Options) {
			return out, ErrInput
		}
		if setup > 0 {
			if err = appendLine("setup", "Setup", setup); err != nil {
				return out, err
			}
		}
		item.TotalMinor = item.SubtotalMinor
		out.Items = append(out.Items, item)
	}
	eligible := coupon != nil && couponValid(*coupon, input.Currency, facts, now)
	if input.Coupon != "" {
		out.Quote.Coupon = &struct {
			Code    string `json:"code"`
			Valid   bool   `json:"valid"`
			Message string `json:"message,omitempty"`
		}{Code: input.Coupon, Valid: false, Message: "Coupon is not eligible"}
	}
	fixedRemaining := int64(0)
	if eligible {
		fixedRemaining = fixedCoupon(*coupon, input.Currency)
	}
	discountLines := []api.QuoteLine{}
	applies := false
	for _, line := range out.Quote.Lines {
		var err error
		out.Quote.Subtotal.AmountMinor, err = Add(out.Quote.Subtotal.AmountMinor, line.Amount.AmountMinor)
		if err != nil {
			return out, err
		}
		if !eligible {
			continue
		}
		idx := int(*line.ItemIndex)
		item := &out.Items[idx]
		if !couponApplies(*coupon, item.Product.ID, item.Input.Cycle) {
			continue
		}
		applies = true
		discount := int64(0)
		if coupon.Kind == "percent" {
			discount, err = Discount(line.Amount.AmountMinor, "percent", *coupon.PercentBp, 0)
		} else {
			discount = min(line.Amount.AmountMinor, fixedRemaining)
			fixedRemaining -= discount
		}
		if err != nil {
			return out, err
		}
		item.DiscountMinor += discount
		item.TotalMinor -= discount
		out.Quote.Discount.AmountMinor += discount
		if discount > 0 {
			discountLines = append(discountLines, api.QuoteLine{Kind: "discount", ItemIndex: line.ItemIndex, Description: coupon.Code, Amount: Money(input.Currency, decimals, -discount)})
		}
	}
	out.Quote.Lines = append(out.Quote.Lines, discountLines...)
	if out.Quote.Coupon != nil && applies {
		out.Quote.Coupon.Valid = true
		out.Quote.Coupon.Message = ""
	}
	out.Quote.Total.AmountMinor = out.Quote.Subtotal.AmountMinor - out.Quote.Discount.AmountMinor
	renewalFixed := int64(0)
	if eligible && coupon.Recurring {
		renewalFixed = fixedCoupon(*coupon, input.Currency)
	}
	for idx := range out.Items {
		item := &out.Items[idx]
		if item.Input.Cycle == "one_time" {
			continue
		}
		amount := item.RecurringMinor
		if eligible && coupon.Recurring && couponApplies(*coupon, item.Product.ID, item.Input.Cycle) {
			if coupon.Kind == "percent" {
				for _, line := range out.Quote.Lines {
					if line.ItemIndex == nil || int(*line.ItemIndex) != idx || (line.Kind != "product" && line.Kind != "option") {
						continue
					}
					discount, err := Discount(line.Amount.AmountMinor, "percent", *coupon.PercentBp, 0)
					if err != nil {
						return out, err
					}
					amount -= discount
				}
			} else {
				discount := min(amount, renewalFixed)
				amount -= discount
				renewalFixed -= discount
			}
		}
		item.RecurringMinor = amount
		out.Quote.Renewals = append(out.Quote.Renewals, struct {
			ItemIndex int64     `json:"item_index"`
			Cycle     string    `json:"cycle"`
			Amount    api.Money `json:"amount"`
		}{int64(idx), item.Input.Cycle, Money(input.Currency, decimals, amount)})
	}
	out.Quote.QuoteDigest = quoteDigest(out, out.Quote.ExpiresAt)
	return out, nil
}

func quoteDigest(quote CalculatedQuote, expires string) string {
	q := quote.Quote
	q.QuoteDigest = ""
	q.ExpiresAt = expires
	raw, _ := json.Marshal(struct {
		Items     []api.CartItemInput `json:"items"`
		Revisions []int64             `json:"revisions"`
		Quote     api.Quote           `json:"quote"`
	}{quote.pricingInput, quote.revisions, q})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// MatchQuote needs no quote cache or database writes. The request contract only
// echoes a digest, so test each second in the ten-minute validity interval using
// the newly calculated financial values. A price, stock, option or eligibility
// change cannot reuse a previous quote. Expiration itself is included in SHA256.
func MatchQuote(current CalculatedQuote, digest string, now time.Time) error {
	if len(digest) != 64 {
		return ErrQuoteChanged
	}
	for seconds := 1; seconds <= 600; seconds++ {
		expires := now.UTC().Truncate(time.Second).Add(time.Duration(seconds) * time.Second).Format(time.RFC3339)
		if quoteDigest(current, expires) == digest {
			return nil
		}
	}
	return ErrQuoteChanged
}
