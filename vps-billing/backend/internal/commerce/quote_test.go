package commerce

import (
	"aster/billing/internal/contracttest"
	api "aster/billing/internal/financeapi"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func quoteProduct(currency string, decimals int64) api.AdminProduct {
	return api.AdminProduct{ID: "p1", Slug: "product", Kind: "generic", Name: api.LocalizedText{"en": "Product"}, Prices: []api.PricePoint{{Cycle: "monthly", Currency: currency, RecurringMinor: 101, SetupMinor: 19, DecimalPlaces: decimals}}, Options: []api.ProductOption{}, OSImages: []api.OsImage{}, Visibility: "public", Available: true, Revision: 1}
}
func quoteInput(currency string) api.CreateQuoteInput {
	return api.CreateQuoteInput{Currency: currency, Items: []api.CartItemInput{{ProductID: "p1", Cycle: "monthly", Quantity: 2, Options: map[string]any{}}}}
}
func percentCoupon(bp int64) api.Coupon {
	return api.Coupon{ID: "coupon", Code: "SAVE", Kind: "percent", PercentBp: &bp, Fixed: []struct {
		Currency    string `json:"currency"`
		AmountMinor int64  `json:"amount_minor"`
	}{}, AppliesTo: struct {
		ProductIDs []string `json:"product_ids"`
		Cycles     []string `json:"cycles"`
	}{[]string{}, []string{}}, Active: true, Revision: 1}
}

func TestQuoteCurrenciesCouponsAndExpiry(t *testing.T) {
	now := time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC)
	for _, currency := range []struct {
		code     string
		decimals int64
	}{{"JPY", 0}, {"USD", 2}, {"KWD", 3}} {
		t.Run(currency.code, func(t *testing.T) {
			p := quoteProduct(currency.code, currency.decimals)
			input := quoteInput(currency.code)
			input.Coupon = "SAVE"
			coupon := percentCoupon(3333)
			q, err := CalculateQuote(input, map[string]api.AdminProduct{p.ID: p}, currency.decimals, &coupon, CouponEligibility{}, now)
			if err != nil {
				t.Fatal(err)
			}
			// Product 101*2=202 and setup 19*2=38 are separately floored: 67+12.
			if q.Quote.Subtotal.AmountMinor != 240 || q.Quote.Discount.AmountMinor != 79 || q.Quote.Total.AmountMinor != 161 || q.Quote.Renewals[0].Amount.AmountMinor != 202 {
				t.Fatal(q.Quote)
			}
			contracttest.Data(t, "Quote", map[string]any{"data": q.Quote})
			if err = MatchQuote(q, q.Quote.QuoteDigest, now.Add(599*time.Second)); err != nil {
				t.Fatal(err)
			}
			if err = MatchQuote(q, q.Quote.QuoteDigest, now.Add(600*time.Second)); !errors.Is(err, ErrQuoteChanged) {
				t.Fatal("expired", err)
			}
			input.Items[0].Quantity = 3
			changed, err := CalculateQuote(input, map[string]api.AdminProduct{p.ID: p}, currency.decimals, &coupon, CouponEligibility{}, now)
			if err != nil {
				t.Fatal(err)
			}
			if MatchQuote(changed, q.Quote.QuoteDigest, now) == nil {
				t.Fatal("quantity change accepted")
			}
		})
	}
}

func TestCouponRestrictionsAndFixedAllocation(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := quoteProduct("USD", 2)
	input := quoteInput("USD")
	input.Coupon = "SAVE"
	one := int64(1)
	past := now.Add(-time.Hour).Format(time.RFC3339)
	future := now.Add(time.Hour).Format(time.RFC3339)
	for _, tc := range []struct {
		name  string
		edit  func(*api.Coupon)
		facts CouponEligibility
		valid bool
	}{
		{"valid", func(c *api.Coupon) {}, CouponEligibility{}, true},
		{"exhausted", func(c *api.Coupon) { c.MaxUses = &one; c.Used = 1 }, CouponEligibility{}, false},
		{"client limit", func(c *api.Coupon) { c.MaxUsesPerClient = &one }, CouponEligibility{Authenticated: true, ClientUses: 1}, false},
		{"existing client", func(c *api.Coupon) { c.NewClientsOnly = true }, CouponEligibility{Authenticated: true, HasPriorOrder: true}, false},
		{"guest eligibility unknown", func(c *api.Coupon) { c.NewClientsOnly = true }, CouponEligibility{}, false},
		{"new client", func(c *api.Coupon) { c.NewClientsOnly = true }, CouponEligibility{Authenticated: true}, true},
		{"not started", func(c *api.Coupon) { c.StartsAt = &future }, CouponEligibility{}, false},
		{"expired", func(c *api.Coupon) { c.EndsAt = &past }, CouponEligibility{}, false},
		{"wrong product", func(c *api.Coupon) { c.AppliesTo.ProductIDs = []string{"another"} }, CouponEligibility{}, false},
		{"wrong cycle", func(c *api.Coupon) { c.AppliesTo.Cycles = []string{"annually"} }, CouponEligibility{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := percentCoupon(5000)
			tc.edit(&c)
			q, err := CalculateQuote(input, map[string]api.AdminProduct{p.ID: p}, 2, &c, tc.facts, now)
			if err != nil || q.Quote.Coupon.Valid != tc.valid {
				t.Fatal(q.Quote, err)
			}
			contracttest.Data(t, "Quote", map[string]any{"data": q.Quote})
		})
	}
	c := percentCoupon(5000)
	c.Kind = "fixed"
	c.PercentBp = nil
	c.Fixed = append(c.Fixed, struct {
		Currency    string `json:"currency"`
		AmountMinor int64  `json:"amount_minor"`
	}{"USD", 300})
	input.Items = append(input.Items, input.Items[0])
	q, err := CalculateQuote(input, map[string]api.AdminProduct{p.ID: p}, 2, &c, CouponEligibility{}, now)
	if err != nil || q.Quote.Discount.AmountMinor != 300 || q.Quote.Total.AmountMinor != 180 {
		t.Fatal(q, err)
	}
}

func TestQuoteOptionsStockNoFallbackAndNoSecrets(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := quoteProduct("USD", 2)
	p.Prices[0].RecurringMinor = 1
	p.Prices[0].SetupMinor = 0
	p.Options = []api.ProductOption{{ID: "extra", Key: "extra", Kind: "quantity", Label: api.LocalizedText{"en": "Extra"}, Required: true, Choices: []api.OptionChoice{}, Min: 1, Max: 10, UnitPrices: []api.PricePoint{{Currency: "USD", Cycle: "monthly", RecurringMinor: 1, DecimalPlaces: 2}}}}
	in := quoteInput("USD")
	in.Items[0].Quantity = 1
	in.Items[0].Options["extra"] = json.Number("1")
	in.Items[0].RootPassword = "secret-never-persisted"
	coupon := percentCoupon(5000)
	coupon.Recurring = true
	in.Coupon = "SAVE"
	q, err := CalculateQuote(in, map[string]api.AdminProduct{p.ID: p}, 2, &coupon, CouponEligibility{}, now)
	if err != nil || q.Quote.Total.AmountMinor != 2 || q.Quote.Renewals[0].Amount.AmountMinor != 2 {
		t.Fatal(q, err)
	}
	if q.Items[0].Input.RootPassword != "" || q.pricingInput[0].RootPassword != "" {
		t.Fatal("password retained")
	}
	same := in
	same.Items = append([]api.CartItemInput{}, in.Items...)
	same.Items[0].RootPassword = "changed-secret"
	q2, err := CalculateQuote(same, map[string]api.AdminProduct{p.ID: p}, 2, &coupon, CouponEligibility{}, now)
	if err != nil || q2.Quote.QuoteDigest != q.Quote.QuoteDigest {
		t.Fatal("secret affected pricing", err)
	}
	for _, tc := range []struct {
		name string
		edit func(*api.CreateQuoteInput, *api.AdminProduct)
	}{
		{"no FX", func(i *api.CreateQuoteInput, p *api.AdminProduct) { i.Currency = "EUR" }},
		{"no cycle fallback", func(i *api.CreateQuoteInput, p *api.AdminProduct) { i.Items[0].Cycle = "annually" }},
		{"stock across duplicate rows", func(i *api.CreateQuoteInput, p *api.AdminProduct) {
			stock := int64(1)
			p.Stock = &stock
			i.Items = append(i.Items, i.Items[0])
		}},
		{"required option", func(i *api.CreateQuoteInput, p *api.AdminProduct) { i.Items[0].Options = map[string]any{} }},
		{"unknown option", func(i *api.CreateQuoteInput, p *api.AdminProduct) {
			i.Items[0].Options = map[string]any{"extra": int64(1), "unknown": int64(1)}
		}},
		{"quantity limit", func(i *api.CreateQuoteInput, p *api.AdminProduct) {
			i.Items[0].Options = map[string]any{"extra": int64(11)}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := in
			i.Items = append([]api.CartItemInput{}, in.Items...)
			product := p
			tc.edit(&i, &product)
			if _, err := CalculateQuote(i, map[string]api.AdminProduct{p.ID: product}, 2, &coupon, CouponEligibility{}, now); err == nil {
				t.Fatal("invalid quote accepted")
			}
		})
	}
}

func TestModuleOrderFieldsAreValidatedAndSecretsNeverEnterQuotes(t *testing.T) {
	p := quoteProduct("USD", 2)
	p.OrderSchema = &api.ModuleSchema{Type: "object", Properties: map[string]api.SchemaField{
		"hostname_suffix": map[string]any{"type": "string", "enum": []any{"example.test"}},
		"password":        map[string]any{"type": "string", "writeOnly": true},
	}, Required: []string{"hostname_suffix"}}
	input := quoteInput("USD")
	input.Items[0].ModuleFields = map[string]any{"hostname_suffix": "example.test", "password": "NEVER-PERSIST-MODULE-SECRET"}
	q, err := CalculateQuote(input, map[string]api.AdminProduct{p.ID: p}, 2, nil, CouponEligibility{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(q)
	if strings.Contains(string(raw), "NEVER-PERSIST") || q.Items[0].Input.ModuleFields["password"] != nil || q.pricingInput[0].ModuleFields["password"] != nil {
		t.Fatal("module credential entered quote")
	}
	input.Items[0].ModuleFields["hostname_suffix"] = "unapproved.test"
	if _, err = CalculateQuote(input, map[string]api.AdminProduct{p.ID: p}, 2, nil, CouponEligibility{}, time.Now()); err == nil {
		t.Fatal("invalid module enum accepted")
	}
	delete(input.Items[0].ModuleFields, "hostname_suffix")
	if _, err = CalculateQuote(input, map[string]api.AdminProduct{p.ID: p}, 2, nil, CouponEligibility{}, time.Now()); err == nil {
		t.Fatal("missing required module field accepted")
	}
}
