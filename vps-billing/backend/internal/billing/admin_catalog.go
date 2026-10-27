package billing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"
)

var ErrCatalog = errors.New("invalid catalog product")
var ErrCatalogConflict = errors.New("catalog revision changed")

var errStoredCatalog = errors.New("invalid stored catalog quotation")

// Catalog prices are independent minor-unit quotations, never exchange rates.
type CatalogPrice struct {
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
	Available   bool   `json:"available"`
}
type CatalogPlan struct {
	NodeID      string         `json:"node_id"`
	TypeID      string         `json:"type_id"`
	RegionID    string         `json:"region_id"`
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	CPU         int            `json:"cpu"`
	MemoryGB    int            `json:"memory_gb"`
	DiskGB      int            `json:"disk_gb"`
	TransferTB  int            `json:"transfer_tb"`
	Region      string         `json:"region"`
	Available   bool           `json:"available"`
	Prices      []CatalogPrice `json:"prices"`
}
type CatalogView struct {
	TaxonomyRevision int64         `json:"taxonomy_revision"`
	CurrencyRevision int64         `json:"currency_revision"`
	Revision         int64         `json:"revision"`
	Items            []CatalogPlan `json:"items"`
}
type CatalogResult struct {
	TaxonomyRevision int64       `json:"taxonomy_revision"`
	CurrencyRevision int64       `json:"currency_revision"`
	Revision         int64       `json:"revision"`
	Plan             CatalogPlan `json:"plan"`
}
type CatalogInput struct {
	NodeID           string         `json:"node_id"`
	TypeID           string         `json:"type_id"`
	RegionID         string         `json:"region_id"`
	TaxonomyRevision int64          `json:"taxonomy_revision"`
	CurrencyRevision int64          `json:"currency_revision"`
	Revision         int64          `json:"revision"`
	Reason           string         `json:"reason"`
	Name             string         `json:"name"`
	Description      string         `json:"description"`
	CPU              int            `json:"cpu"`
	MemoryGB         int            `json:"memory_gb"`
	DiskGB           int            `json:"disk_gb"`
	TransferTB       int            `json:"transfer_tb"`
	Region           string         `json:"region"`
	Available        bool           `json:"available"`
	Prices           []CatalogPrice `json:"prices"`
}
type CatalogDuplicateInput struct {
	TaxonomyRevision int64  `json:"taxonomy_revision"`
	CurrencyRevision int64  `json:"currency_revision"`
	Revision         int64  `json:"revision"`
	Reason           string `json:"reason"`
}
type adminCatalogStore interface {
	AdminCatalog(context.Context, User) (CatalogView, error)
	CreateCatalogPlan(context.Context, User, CatalogInput) (CatalogResult, error)
	UpdateCatalogPlan(context.Context, User, string, CatalogInput) (CatalogResult, error)
	DuplicateCatalogPlan(context.Context, User, string, CatalogDuplicateInput) (CatalogResult, error)
}

// A minor unit is meaningful only with the precision revision actually read by
// its author. Catalog and currency GETs may be different concurrent snapshots.
func validCatalogRevisions(revision, currencyRevision int64) bool {
	return revision >= 1 && revision <= MaxMinor && currencyRevision >= 1 && currencyRevision <= MaxMinor
}
func catalogReason(reason string) (string, bool) {
	reason = strings.TrimSpace(reason)
	return reason, reason != "" && validText(reason, 500, true)
}
func validCatalogID(id string) bool {
	if len(id) < 1 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return id != "." && id != ".."
}
func normalizeCatalog(input CatalogInput, config CurrencyConfiguration) (CatalogInput, CurrencyConfiguration, error) {
	input.Name = strings.TrimSpace(input.Name)
	var ok bool
	input.Reason, ok = catalogReason(input.Reason)
	if !ok || input.Name == "" || input.Region == "" || !validText(input.Name, 120, false) || !validText(input.Region, 480, false) || utf8.RuneCountInString(input.Region) > 120 || !validText(input.Description, 500, true) ||
		input.CPU < 1 || input.CPU > 4096 || input.MemoryGB < 1 || input.MemoryGB > 1048576 || input.DiskGB < 1 || input.DiskGB > 16777216 || input.TransferTB < 1 || input.TransferTB > 1048576 || len(input.Prices) > 32 || input.Prices == nil {
		return CatalogInput{}, config, ErrCatalog
	}
	input.Prices = append([]CatalogPrice{}, input.Prices...)
	next := cloneCurrencies(config, nil)
	seen, publishable, changed := map[string]bool{}, false, false
	for _, price := range input.Prices {
		cur, exists := config.find(price.Currency)
		if !exists || !validCurrencyCode(price.Currency) || seen[price.Currency] || !validAmount(price.AmountMinor) {
			return CatalogInput{}, config, ErrCatalog
		}
		seen[price.Currency] = true
		publishable = publishable || cur.Enabled && price.Available
		for n := range next.Currencies {
			if next.Currencies[n].Code == price.Currency && !next.Currencies[n].PrecisionLocked {
				next.Currencies[n].PrecisionLocked, changed = true, true
			}
		}
	}
	if input.Available && !publishable {
		return CatalogInput{}, config, ErrCatalog
	}
	if changed {
		if next.Revision == MaxMinor {
			return CatalogInput{}, config, ErrCurrencyConflict
		}
		next.Revision++
	}
	sort.Slice(input.Prices, func(i, j int) bool { return input.Prices[i].Currency < input.Prices[j].Currency })
	return input, next, nil
}
func catalogPlan(p Plan, prices map[string]PlanPrice) CatalogPlan {
	out := CatalogPlan{NodeID: p.NodeID, TypeID: p.TypeID, RegionID: p.RegionID, ID: p.ID, Name: p.Name, Description: p.Description, CPU: p.CPU, MemoryGB: p.MemoryGB, DiskGB: p.DiskGB, TransferTB: p.TransferTB, Region: p.Region, Available: p.Available, Prices: []CatalogPrice{}}
	for code, price := range prices {
		out.Prices = append(out.Prices, CatalogPrice{code, price.AmountMinor, price.Available})
	}
	sort.Slice(out.Prices, func(i, j int) bool { return out.Prices[i].Currency < out.Prices[j].Currency })
	return out
}
func validateCatalogView(view CatalogView, config CurrencyConfiguration) error {
	if view.Revision < 1 || view.Revision > MaxMinor || view.CurrencyRevision != config.Revision {
		return errStoredCatalog
	}
	for _, plan := range view.Items {
		if len(plan.Prices) > 32 {
			return errStoredCatalog
		}
		for _, price := range plan.Prices {
			if _, exists := config.find(price.Currency); !exists || !validCurrencyCode(price.Currency) || !validAmount(price.AmountMinor) {
				return errStoredCatalog
			}
		}
	}
	return nil
}
func catalogInputFrom(p CatalogPlan, revision, currencyRevision int64, reason string) CatalogInput {
	return CatalogInput{NodeID: p.NodeID, TypeID: p.TypeID, RegionID: p.RegionID, Revision: revision, CurrencyRevision: currencyRevision, Reason: reason, Name: p.Name, Description: p.Description, CPU: p.CPU, MemoryGB: p.MemoryGB, DiskGB: p.DiskGB, TransferTB: p.TransferTB, Region: p.Region, Available: p.Available, Prices: p.Prices}
}
func applyCatalogPlan(p Plan, input CatalogInput) Plan {
	p.TypeID, p.RegionID, p.NodeID = input.TypeID, input.RegionID, input.NodeID
	p.Name, p.Description, p.CPU, p.MemoryGB, p.DiskGB, p.TransferTB, p.Region, p.Available = input.Name, input.Description, input.CPU, input.MemoryGB, input.DiskGB, input.TransferTB, input.Region, input.Available
	return p
}
func catalogAudit(actor User, action, id string, revision int64, reason string) Audit {
	out := currencyAudit(actor, action, id, revision)
	out.Detail = fmt.Sprintf("catalog revision %d; reason: %s", revision, reason)
	return out
}
func catalogAction(oldAvailable, available bool) string {
	if !oldAvailable && available {
		return "catalog.published"
	}
	if oldAvailable && !available {
		return "catalog.unpublished"
	}
	return "catalog.updated"
}
func (m *Memory) AdminCatalog(ctx context.Context, actor User) (CatalogView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return CatalogView{}, err
	}
	if _, err := m.activeAdministratorLocked(actor.ID); err != nil {
		return CatalogView{}, err
	}
	out := CatalogView{TaxonomyRevision: m.taxonomy.Revision, Revision: m.pricesRevision, CurrencyRevision: m.currencies.Revision, Items: []CatalogPlan{}}
	for _, p := range m.plans {
		out.Items = append(out.Items, catalogPlan(p, m.prices[p.ID]))
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].ID < out.Items[j].ID })
	return out, validateCatalogView(out, m.currencies)
}
func (m *Memory) CreateCatalogPlan(ctx context.Context, actor User, input CatalogInput) (CatalogResult, error) {
	return m.mutateCatalog(ctx, actor, "", input, false)
}
func (m *Memory) UpdateCatalogPlan(ctx context.Context, actor User, id string, input CatalogInput) (CatalogResult, error) {
	if !validCatalogID(id) {
		return CatalogResult{}, ErrNotFound
	}
	return m.mutateCatalog(ctx, actor, id, input, false)
}
func (m *Memory) DuplicateCatalogPlan(ctx context.Context, actor User, id string, input CatalogDuplicateInput) (CatalogResult, error) {
	if !validCatalogID(id) {
		return CatalogResult{}, ErrNotFound
	}
	return m.mutateCatalog(ctx, actor, id, CatalogInput{TaxonomyRevision: input.TaxonomyRevision, Revision: input.Revision, CurrencyRevision: input.CurrencyRevision, Reason: input.Reason}, true)
}
func (m *Memory) mutateCatalog(ctx context.Context, actor User, id string, input CatalogInput, duplicate bool) (CatalogResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return CatalogResult{}, err
	}
	actor, err := m.activeAdministratorLocked(actor.ID)
	if err != nil {
		return CatalogResult{}, err
	}
	if !validCatalogRevisions(input.Revision, input.CurrencyRevision) {
		return CatalogResult{}, ErrCatalog
	}
	if input.TaxonomyRevision < 1 || input.TaxonomyRevision > MaxMinor {
		return CatalogResult{}, ErrTaxonomy
	}
	if input.TaxonomyRevision != m.taxonomy.Revision {
		return CatalogResult{}, ErrTaxonomyConflict
	}
	if input.Revision != m.pricesRevision || input.CurrencyRevision != m.currencies.Revision || m.pricesRevision == MaxMinor {
		return CatalogResult{}, ErrCatalogConflict
	}
	index, p := -1, Plan{ID: ID(), Currency: m.currencies.DefaultCurrency}
	for n, candidate := range m.plans {
		if candidate.ID == id {
			index, p = n, candidate
			break
		}
	}
	if id != "" && index < 0 {
		return CatalogResult{}, ErrNotFound
	}
	action := catalogAction(p.Available, input.Available)
	if id == "" {
		action = "catalog.created"
	}
	if duplicate {
		taxonomyRevision := input.TaxonomyRevision
		input = catalogInputFrom(catalogPlan(p, m.prices[id]), input.Revision, input.CurrencyRevision, input.Reason)
		input.TaxonomyRevision = taxonomyRevision
		input.Available = false
		p.ID, index, action = ID(), -1, "catalog.duplicated"
	}
	region, exists := taxonomyRegion(m.taxonomy, input.TypeID, input.RegionID, input.Available)
	_, nodeExists := taxonomyNode(m.taxonomy, input.TypeID, input.RegionID, input.NodeID, input.Available)
	if !exists || !nodeExists || input.Region != region.Name {
		return CatalogResult{}, ErrTaxonomy
	}
	input, next, err := normalizeCatalog(input, m.currencies)
	if err != nil {
		return CatalogResult{}, err
	}
	p = applyCatalogPlan(p, input)
	prices := map[string]PlanPrice{}
	for _, price := range input.Prices {
		prices[price.Currency] = PlanPrice{price.AmountMinor, price.Available}
	}
	if index < 0 {
		m.plans = append(m.plans, p)
	} else {
		m.plans[index] = p
	}
	m.prices[p.ID], m.currencies = prices, next
	m.pricesRevision++
	m.audits = append(m.audits, catalogAudit(actor, action, p.ID, m.pricesRevision, input.Reason))
	return CatalogResult{TaxonomyRevision: m.taxonomy.Revision, Revision: m.pricesRevision, CurrencyRevision: m.currencies.Revision, Plan: catalogPlan(p, prices)}, nil
}

func (a *App) adminCatalogHTTP(w http.ResponseWriter, r *http.Request, actor User, p string) {
	store, ok := a.Store.(adminCatalogStore)
	if !ok {
		a.internal(w, r, errors.New("catalog store unavailable"))
		return
	}
	if r.URL.RawQuery != "" {
		fail(w, r, 400, "INVALID_CATALOG", "商品请求不支持查询参数")
		return
	}
	base := "/admin/catalog"
	if p == base && r.Method == "GET" {
		out, err := store.AdminCatalog(r.Context(), actor)
		if err != nil {
			a.catalogError(w, r, err)
		} else {
			jsonOut(w, 200, out)
		}
		return
	}
	if r.Method != "POST" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "不支持此请求方式")
		return
	}
	id, duplicate := "", false
	if p != base {
		if !strings.HasPrefix(p, base+"/") {
			fail(w, r, 404, "NOT_FOUND", "未找到此商品")
			return
		}
		id = strings.TrimPrefix(p, base+"/")
		if strings.HasSuffix(id, "/duplicate") {
			id = strings.TrimSuffix(id, "/duplicate")
			duplicate = true
		}
		if !validCatalogID(id) {
			fail(w, r, 404, "NOT_FOUND", "未找到此商品")
			return
		}
	}
	var out CatalogResult
	var err error
	status := 200
	if duplicate {
		var input CatalogDuplicateInput
		if !financeBody(w, r, &input) {
			return
		}
		out, err = store.DuplicateCatalogPlan(r.Context(), actor, id, input)
		status = 201
	} else {
		var input CatalogInput
		if !financeBody(w, r, &input) {
			return
		}
		if id == "" {
			out, err = store.CreateCatalogPlan(r.Context(), actor, input)
			status = 201
		} else {
			out, err = store.UpdateCatalogPlan(r.Context(), actor, id, input)
		}
	}
	if err != nil {
		a.catalogError(w, r, err)
	} else {
		jsonOut(w, status, out)
	}
}
func (a *App) catalogError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrTaxonomy), errors.Is(err, ErrTaxonomyConflict), errors.Is(err, ErrTaxonomyInUse):
		a.taxonomyError(w, r, err)
	case errors.Is(err, ErrAdminRequired):
		fail(w, r, 403, "FORBIDDEN", "需要管理员权限")
	case errors.Is(err, ErrCatalog):
		fail(w, r, 400, "INVALID_CATALOG", "请检查商品名称、规格、报价和修改原因；上架至少需要一种启用币种的有效报价")
	case errors.Is(err, ErrCatalogConflict):
		fail(w, r, 409, "CATALOG_CONFLICT", "商品或报价已改变，请重新读取后再编辑")
	case errors.Is(err, ErrNotFound):
		fail(w, r, 404, "NOT_FOUND", "未找到此商品")
	default:
		a.currencyError(w, r, err)
	}
}
