package billing

import (
	"context"
	"errors"
	"golang.org/x/crypto/bcrypt"
	"strings"
	"sync"
	"time"
)

type memoRequest struct {
	Fingerprint string
	Order       Order
}
type Memory struct {
	productConfigurations      map[string]ProductConfiguration
	orderProductConfigurations map[string]orderProductConfiguration
	provisioningJobs           map[string]provisioningJobRecord
	taxonomy                   TaxonomyConfiguration
	currencies                 CurrencyConfiguration
	pricesRevision             int64
	emailSettings              EmailSettingsRecord
	settings                   SiteSettings
	mu                         sync.Mutex
	users                      map[string]User
	sessions                   map[string]Session
	plans                      []Plan
	prices                     map[string]map[string]PlanPrice
	orders                     []Order
	invoices                   []Invoice
	services                   []Service
	audits                     []Audit
	requests                   map[string]memoRequest
	orderEvents                []orderOutboxEvent
	paymentAttempts            map[string]PaymentAttempt
	paymentReceipts            []paymentReceipt
}

func NewMemory() *Memory {
	m := &Memory{users: map[string]User{}, sessions: map[string]Session{}, plans: Catalog(), orders: []Order{}, invoices: []Invoice{}, services: []Service{}, audits: []Audit{}, requests: map[string]memoRequest{}}
	m.taxonomy = initialTaxonomy(m.plans)
	for n := range m.plans {
		m.plans[n].TypeID = "generic"
		m.plans[n].RegionID = legacyRegionID(m.plans[n].Region)
		m.plans[n].NodeID = defaultNodeID(m.plans[n].RegionID)
	}
	m.settings = DefaultSettings()
	m.currencies = DefaultCurrencyConfiguration()
	m.pricesRevision = 1
	m.emailSettings = DefaultEmailSettings()
	m.prices = map[string]map[string]PlanPrice{
		"starter":     {"CNY": {3900, true}},
		"standard":    {"CNY": {7900, true}},
		"performance": {"CNY": {15900, true}},
	}
	for _, v := range []struct{ email, name, role, password string }{{"demo@aster.local", "林先生", "customer", "DemoCustomer!2026"}, {"admin@aster.local", "演示管理员", "admin", "DemoAdmin!2026"}, {"other@aster.local", "另一位客户", "customer", "DemoOther!2026"}} {
		h, _ := bcrypt.GenerateFromPassword([]byte(v.password), bcrypt.DefaultCost)
		u := User{ID: ID(), Email: v.email, Name: v.name, Role: v.role, PasswordHash: string(h), Revision: 1}
		m.users[u.ID] = u
	}
	return m
}
func (m *Memory) Ping(context.Context) error { return nil }
func (m *Memory) UserByEmail(_ context.Context, e string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if strings.EqualFold(u.Email, e) {
			return normalizedUserRevision(u), nil
		}
	}
	return User{}, ErrNotFound
}
func (m *Memory) UserByID(_ context.Context, id string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return u, ErrNotFound
	}
	return normalizedUserRevision(u), nil
}
func (m *Memory) AddUser(_ context.Context, u User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range m.users {
		if strings.EqualFold(v.Email, u.Email) || v.ID == u.ID {
			return ErrConflict
		}
	}
	m.users[u.ID] = normalizedUserRevision(u)
	return nil
}
func (m *Memory) Users(context.Context) ([]User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := []User{}
	for _, u := range m.users {
		r = append(r, normalizedUserRevision(u))
	}
	return r, nil
}
func (m *Memory) PutSession(_ context.Context, s Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[s.UserID]
	if !ok {
		return ErrNotFound
	}
	u = normalizedUserRevision(u)
	if u.Disabled || s.Revision != 0 && s.Revision != u.Revision {
		return ErrConflict
	}
	for k, v := range m.sessions {
		if v.Expires.Before(time.Now()) {
			delete(m.sessions, k)
		}
	}
	m.sessions[s.Hash] = s
	return nil
}
func (m *Memory) Session(_ context.Context, k string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[k]
	if !ok || s.Expires.Before(time.Now()) {
		return Session{}, ErrNotFound
	}
	return s, nil
}
func (m *Memory) DeleteSession(_ context.Context, k string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, k)
	return nil
}
func (m *Memory) Plans(_ context.Context, currency string) ([]Plan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, known := m.currencies.find(currency)
	if !known {
		return nil, ErrCurrency
	}
	// A draft is not a public listing. Currency-specific unavailability remains
	// a visible published product, so missing quotes never become fallback FX.
	out := make([]Plan, 0, len(m.plans))
	for _, plan := range m.plans {
		if _, live := taxonomyNode(m.taxonomy, plan.TypeID, plan.RegionID, plan.NodeID, true); plan.Available && live {
			out = append(out, plan)
		}
	}
	for n := range out {
		price := m.prices[out[n].ID][currency]
		if price.AmountMinor < 0 || price.AmountMinor > MaxMinor {
			return nil, ErrPlanPrices
		}
		out[n].Currency, out[n].PriceMinor = currency, price.AmountMinor
		out[n].CurrencyFormat = cur.CurrencyFormat
		out[n].PriceRevision = m.pricesRevision
		out[n].TaxonomyRevision = m.taxonomy.Revision
		out[n].Available = cur.Enabled && out[n].Available && price.Available && validAmount(price.AmountMinor)
	}
	return out, nil
}
func (m *Memory) Orders(_ context.Context, uid string) ([]Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := []Order{}
	for j := len(m.orders) - 1; j >= 0; j-- {
		v := m.orders[j]
		if uid == "" || v.UserID == uid {
			r = append(r, cloneOrder(v))
		}
	}
	return r, nil
}

func (m *Memory) RecentOrders(ctx context.Context, uid string) ([]Order, error) {
	if uid == "" {
		return nil, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Keep only the five newest candidates. Historical fixtures need not be
	// inserted in timestamp order; cloning the selected snapshots happens once.
	out := make([]Order, 0, recentOrderLimit)
	for _, order := range m.orders {
		if order.UserID != uid {
			continue
		}
		position := 0
		for position < len(out) && (out[position].CreatedAt.After(order.CreatedAt) || (out[position].CreatedAt.Equal(order.CreatedAt) && out[position].ID > order.ID)) {
			position++
		}
		if position >= recentOrderLimit {
			continue
		}
		if len(out) < recentOrderLimit {
			out = append(out, Order{})
		}
		copy(out[position+1:], out[position:len(out)-1])
		out[position] = order
	}
	for n := range out {
		out[n] = cloneOrder(out[n])
	}
	return out, nil
}

func (m *Memory) Invoices(_ context.Context, uid string) ([]Invoice, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := []Invoice{}
	for j := len(m.invoices) - 1; j >= 0; j-- {
		v := m.invoices[j]
		if uid == "" || v.UserID == uid {
			r = append(r, cloneInvoice(v))
		}
	}
	return r, nil
}
func (m *Memory) Services(_ context.Context, uid string) ([]Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := []Service{}
	for _, v := range m.services {
		if uid == "" || v.UserID == uid {
			r = append(r, v)
		}
	}
	return r, nil
}
func (m *Memory) Audits(_ context.Context, uid string) ([]Audit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := []Audit{}
	for j := len(m.audits) - 1; j >= 0; j-- {
		v := m.audits[j]
		if uid == "" || v.UserID == uid {
			r = append(r, v)
		}
	}
	return r, nil
}
func (m *Memory) CreateOrder(ctx context.Context, u User, pid, host, key, currency string) (Order, bool, error) {
	return m.CreateQuotedOrder(ctx, u, pid, host, key, currency, nil)
}
func (m *Memory) CreateQuotedOrder(_ context.Context, u User, pid, host, key, currency string, quote *PriceQuote) (Order, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if currency != "" && !SupportedCurrency(currency) {
		return Order{}, false, ErrCurrency
	}
	if quote != nil && (currency == "" || !quote.valid()) {
		return Order{}, false, ErrInvalidQuote
	}
	fp := orderFingerprint(pid, host, currency)
	k := u.ID + ":" + key
	if r, ok := m.requests[k]; ok {
		if r.Fingerprint != fp {
			return Order{}, false, ErrConflict
		}
		for _, existing := range m.orders {
			if existing.ID == r.Order.ID && existing.UserID == u.ID {
				return cloneOrder(existing), true, nil
			}
		}
		return Order{}, false, ErrNotFound
	}
	if currency == "" {
		currency = m.settings.DefaultCurrency
	}
	cur, known := m.currencies.find(currency)
	if !known {
		return Order{}, false, ErrCurrency
	}
	if !cur.Enabled {
		return Order{}, false, ErrCurrencyDisabled
	}
	var p Plan
	for _, v := range m.plans {
		if v.ID == pid {
			p = v
		}
	}
	price := m.prices[pid][currency]
	p.PriceMinor, p.Currency = price.AmountMinor, currency
	p.CurrencyFormat = cur.CurrencyFormat
	if _, live := taxonomyNode(m.taxonomy, p.TypeID, p.RegionID, p.NodeID, true); !p.Available || !live || !price.Available || !validAmount(price.AmountMinor) {
		return Order{}, false, ErrUnavailable
	}
	if quote != nil && !quote.matches(price.AmountMinor, cur.DecimalPlaces, m.pricesRevision) {
		return Order{}, false, ErrPriceChanged
	}
	configuration, err := m.productConfigurationLocked(pid)
	if err != nil {
		return Order{}, false, err
	}
	o, i, a := newRecords(u, p, host)
	i.Issuer = m.settings.Identity()
	o.Issuer = m.settings.Identity()
	if m.orderProductConfigurations == nil {
		m.orderProductConfigurations = map[string]orderProductConfiguration{}
	}
	m.orderProductConfigurations[o.ID] = productSnapshot(configuration, o.CreatedAt)
	m.orders = append(m.orders, o)
	m.invoices = append(m.invoices, i)
	m.audits = append(m.audits, a)
	m.requests[k] = memoRequest{fp, o}
	m.orderEvents = append(m.orderEvents, orderCreatedEvent(o, i))
	return cloneOrder(o), false, nil
}

func (m *Memory) Invoice(c context.Context, uid, id string) (Invoice, error) {
	vs, e := m.Invoices(c, uid)
	if e != nil {
		return Invoice{}, e
	}
	for _, v := range vs {
		if v.ID == id {
			return v, nil
		}
	}
	return Invoice{}, ErrNotFound
}
func (m *Memory) Summary(_ context.Context, uid, currency string) (Summary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out Summary
	if _, known := m.currencies.find(currency); !known {
		return out, ErrCurrency
	}
	for _, s := range m.services {
		if s.Currency != currency || (uid != "" && uid != s.UserID) {
			continue
		}
		out.TotalServices++
		if s.Status == "active" {
			out.ActiveServices++
			if !addAmount(&out.MonthlyTotal, s.PriceMinor) {
				return Summary{}, errors.New("amount overflow")
			}
		}
	}
	for _, i := range m.invoices {
		if i.Currency != currency || (uid != "" && uid != i.UserID) {
			continue
		}
		if i.Status == "unpaid" {
			out.PendingInvoices++
			if !addAmount(&out.UnpaidTotal, i.AmountMinor) {
				return Summary{}, errors.New("amount overflow")
			}
		}
		if i.Status == "paid" {
			if !addAmount(&out.Revenue, i.AmountMinor) {
				return Summary{}, errors.New("amount overflow")
			}
		}
	}
	for _, u := range m.users {
		if u.Role == "customer" {
			out.Customers++
		}
	}
	return out, nil
}

func (m *Memory) Settings(context.Context) (SiteSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings, nil
}

func (m *Memory) UpdateSettings(_ context.Context, actor User, settings SiteSettings) (SiteSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if actor.Role != "admin" || !ValidateSettings(settings) {
		return SiteSettings{}, errors.New("invalid settings update")
	}
	if settings.Revision != m.settings.Revision || settings.Revision == MaxMinor {
		return SiteSettings{}, ErrSettingsConflict
	}
	cur, known := m.currencies.find(settings.DefaultCurrency)
	if !known || !cur.Enabled {
		return SiteSettings{}, ErrCurrency
	}
	if settings.DefaultCurrency != m.currencies.DefaultCurrency {
		if m.currencies.Revision == MaxMinor {
			return SiteSettings{}, ErrCurrencyConflict
		}
		m.currencies.DefaultCurrency = settings.DefaultCurrency
		m.currencies.Revision++
	}
	settings.Revision++
	m.settings = settings
	m.audits = append(m.audits, settingsAudit(actor))
	return settings, nil
}

func cloneIdentity(i *InvoiceIdentity) *InvoiceIdentity {
	if i == nil {
		return nil
	}
	copy := *i
	return &copy
}
func cloneOrder(o Order) Order {
	o.Issuer = cloneIdentity(o.Issuer)
	if o.PlanSnapshot != nil {
		snapshot := *o.PlanSnapshot
		o.PlanSnapshot = &snapshot
	}
	return o
}
func cloneInvoice(i Invoice) Invoice {
	i.Issuer = cloneIdentity(i.Issuer)
	if i.PaidAt != nil {
		paid := *i.PaidAt
		i.PaidAt = &paid
	}
	return i
}
