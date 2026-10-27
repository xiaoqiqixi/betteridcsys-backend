package billing

import "context"

func (m *Memory) currencyUsage() map[string]CurrencyUsage {
	out := map[string]CurrencyUsage{}
	for _, prices := range m.prices {
		for code := range prices {
			u := out[code]
			u.PlanPrices++
			out[code] = u
		}
	}
	for _, o := range m.orders {
		u := out[o.Currency]
		u.Orders++
		if o.Status == "pending_payment" {
			u.PendingOrders++
		}
		out[o.Currency] = u
	}
	for _, i := range m.invoices {
		u := out[i.Currency]
		u.Invoices++
		if i.Status == "unpaid" {
			u.UnpaidInvoices++
		}
		out[i.Currency] = u
	}
	for _, s := range m.services {
		u := out[s.Currency]
		u.Services++
		if s.Status != "terminated" {
			u.OngoingServices++
		}
		out[s.Currency] = u
	}
	return out
}
func (m *Memory) Currencies(_ context.Context, admin bool) (CurrencyConfiguration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var usage map[string]CurrencyUsage
	if admin {
		usage = m.currencyUsage()
	}
	return cloneCurrencies(m.currencies, usage), nil
}
func (m *Memory) UpdateCurrencies(_ context.Context, actor User, input CurrencyUpdate) (CurrencyConfiguration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if actor.Role != "admin" {
		return CurrencyConfiguration{}, ErrCurrencyConfig
	}
	usage := m.currencyUsage()
	next, err := applyCurrencyUpdate(m.currencies, input, usage)
	if err != nil {
		return CurrencyConfiguration{}, err
	}
	if next.DefaultCurrency != m.settings.DefaultCurrency {
		if m.settings.Revision == MaxMinor {
			return CurrencyConfiguration{}, ErrSettingsConflict
		}
		m.settings.DefaultCurrency = next.DefaultCurrency
		m.settings.Revision++
	}
	m.currencies = next
	m.audits = append(m.audits, currencyAudit(actor, "currencies.updated", "currencies", next.Revision))
	return cloneCurrencies(next, usage), nil
}
func (m *Memory) planPrices(currency string) (PlanPricesView, error) {
	cur, ok := m.currencies.find(currency)
	if !ok {
		return PlanPricesView{}, ErrCurrency
	}
	out := PlanPricesView{Revision: m.pricesRevision, Currency: currency, CurrencyFormat: cur.CurrencyFormat, Plans: []PlanPriceView{}}
	for _, p := range m.plans {
		v := PlanPriceView{PlanID: p.ID, PlanName: p.Name, PlanAvailable: p.Available}
		if price, ok := m.prices[p.ID][currency]; ok {
			if !validAmount(price.AmountMinor) {
				return PlanPricesView{}, ErrPlanPrices
			}
			v.AmountMinor = price.AmountMinor
			v.Amount = priceString(price.AmountMinor, cur.DecimalPlaces)
			v.Available = price.Available
		}
		out.Plans = append(out.Plans, v)
	}
	return out, nil
}
func (m *Memory) PlanPrices(_ context.Context, currency string) (PlanPricesView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.planPrices(currency)
}
func (m *Memory) UpdatePlanPrices(_ context.Context, actor User, input PlanPricesUpdate) (PlanPricesView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if actor.Role != "admin" {
		return PlanPricesView{}, ErrPlanPrices
	}
	if input.Revision != m.pricesRevision || m.pricesRevision == MaxMinor {
		return PlanPricesView{}, ErrPriceConflict
	}
	cur, ok := m.currencies.find(input.Currency)
	if !ok {
		return PlanPricesView{}, ErrCurrency
	}
	amounts, err := validatePriceUpdate(input, cur)
	if err != nil {
		return PlanPricesView{}, err
	}
	planIndexes := map[string]int{}
	for n, p := range m.plans {
		planIndexes[p.ID] = n
	}
	for id := range amounts {
		if _, ok := planIndexes[id]; !ok {
			return PlanPricesView{}, ErrPlanPrices
		}
	}
	for _, v := range input.Prices {
		p := m.plans[planIndexes[v.PlanID]]
		if _, live := taxonomyNode(m.taxonomy, p.TypeID, p.RegionID, p.NodeID, true); v.PlanAvailable && !live {
			return PlanPricesView{}, ErrTaxonomy
		}
	}
	if !cur.PrecisionLocked && m.currencies.Revision == MaxMinor {
		return PlanPricesView{}, ErrCurrencyConflict
	}
	for _, v := range input.Prices {
		if m.prices[v.PlanID] == nil {
			m.prices[v.PlanID] = map[string]PlanPrice{}
		}
		m.prices[v.PlanID][input.Currency] = PlanPrice{amounts[v.PlanID], v.Available}
		m.plans[planIndexes[v.PlanID]].Available = v.PlanAvailable
	}
	if !cur.PrecisionLocked {
		for n := range m.currencies.Currencies {
			if m.currencies.Currencies[n].Code == cur.Code {
				m.currencies.Currencies[n].PrecisionLocked = true
			}
		}
		m.currencies.Revision++
	}
	m.pricesRevision++
	m.audits = append(m.audits, currencyAudit(actor, "plan.prices.updated", input.Currency, m.pricesRevision))
	return m.planPrices(input.Currency)
}
