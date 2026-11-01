package billing

import (
	api "aster/billing/internal/financeapi"
	"context"
	"encoding/json"
	"sort"
)

type memoryCommerceRequest struct {
	fingerprint string
	response    []byte
}
type memoryCommerceCatalog struct {
	coupons  map[string]api.Coupon
	products map[string]api.AdminProduct
	images   map[string]api.AdminOsImage
	requests map[string]memoryCommerceRequest
}

func clone06[T any](value T) T {
	raw, _ := json.Marshal(value)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}
func (m *Memory) catalog06Locked() *memoryCommerceCatalog {
	if m.commerceCatalog != nil {
		return m.commerceCatalog
	}
	c := &memoryCommerceCatalog{coupons: map[string]api.Coupon{}, products: map[string]api.AdminProduct{}, images: map[string]api.AdminOsImage{}, requests: map[string]memoryCommerceRequest{}}
	for _, p := range m.plans {
		visibility := "hidden"
		if p.Available {
			visibility = "public"
		}
		traffic := int64(p.TransferTB) * 1024
		item := api.AdminProduct{ID: p.ID, Slug: p.ID, Kind: "generic", TypeID: p.TypeID, RegionID: p.RegionID, NodeID: p.NodeID, Name: api.LocalizedText{"en": p.Name, "zh-CN": p.Name}, Description: api.LocalizedText{"en": p.Description, "zh-CN": p.Description}, Badges: []string{}, Specs: api.ProductSpecs{CPUCores: int64(p.CPU), MemoryMB: int64(p.MemoryGB) * 1024, DiskGB: int64(p.DiskGB), TrafficGB: &traffic, IPv4: 1}, Prices: []api.PricePoint{}, Options: []api.ProductOption{}, OSImages: []api.OsImage{}, Visibility: visibility, Revision: 1}
		for currency, price := range m.prices[p.ID] {
			config, ok := m.currencies.find(currency)
			if price.Available && ok {
				item.Prices = append(item.Prices, api.PricePoint{Cycle: "monthly", Currency: currency, RecurringMinor: price.AmountMinor, DecimalPlaces: int64(config.DecimalPlaces)})
			}
		}
		c.products[p.ID] = item
	}
	m.commerceCatalog = c
	return c
}
func (m *Memory) projectProduct06Locked(p api.AdminProduct, admin bool, currency string) (api.AdminProduct, bool) {
	p = clone06(p)
	_, visible := taxonomyNode(m.taxonomy, p.TypeID, p.RegionID, p.NodeID, true)
	if !admin && (p.Visibility != "public" || !visible) {
		return p, false
	}
	filter := func(prices []api.PricePoint) []api.PricePoint {
		out := []api.PricePoint{}
		for _, v := range prices {
			c, ok := m.currencies.find(v.Currency)
			if (currency == "" || currency == v.Currency) && (admin || ok && c.Enabled) {
				out = append(out, v)
			}
		}
		return out
	}
	p.Prices = filter(p.Prices)
	for i := range p.Options {
		if p.Options[i].Kind == "quantity" {
			p.Options[i].UnitPrices = filter(p.Options[i].UnitPrices)
		}
		for j := range p.Options[i].Choices {
			p.Options[i].Choices[j].Prices = filter(p.Options[i].Choices[j].Prices)
		}
	}
	if !admin {
		images := []api.OsImage{}
		for _, image := range p.OSImages {
			if m.catalog06Locked().images[image.ID].Enabled {
				images = append(images, image)
			}
		}
		p.OSImages = images
	}
	p.Available = p.Visibility == "public" && visible && len(p.Prices) > 0 && (p.Stock == nil || *p.Stock > 0)
	return p, true
}
func (m *Memory) Products06(ctx context.Context, admin bool, currency, typeID, regionID string) ([]api.AdminProduct, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if currency != "" {
		c, ok := m.currencies.find(currency)
		if !ok || !c.Enabled {
			return nil, ErrCommerceInput
		}
	}
	out := []api.AdminProduct{}
	for _, p := range m.catalog06Locked().products {
		if typeID != "" && p.TypeID != typeID || regionID != "" && p.RegionID != regionID {
			continue
		}
		if p, ok := m.projectProduct06Locked(p, admin, currency); ok {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].SortOrder < out[j].SortOrder || out[i].SortOrder == out[j].SortOrder && out[i].ID < out[j].ID
	})
	return out, nil
}
func (m *Memory) Product06(ctx context.Context, admin bool, id string) (api.AdminProduct, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return api.AdminProduct{}, err
	}
	p, ok := m.catalog06Locked().products[id]
	if !ok {
		return p, ErrNotFound
	}
	p, ok = m.projectProduct06Locked(p, admin, "")
	if !ok {
		return p, ErrNotFound
	}
	return p, nil
}
func (m *Memory) SaveProduct06(ctx context.Context, actor User, id string, in api.AdminCreateProductInput, key string, duplicate bool) (api.AdminProduct, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out api.AdminProduct
	if err := ctx.Err(); err != nil {
		return out, err
	}
	actor, err := m.activeAdministratorLocked(actor.ID)
	if err != nil {
		return out, err
	}
	c := m.catalog06Locked()
	raw, _ := json.Marshal(in)
	fingerprint := digest(string(raw))
	operation := "products:create"
	if id != "" {
		operation = "products:" + id
	}
	if duplicate {
		operation += "/duplicate"
		fingerprint = digest("{}")
	}
	memoKey := actor.ID + "\x00" + operation + "\x00" + key
	if key != "" {
		if stored, ok := c.requests[memoKey]; ok {
			if stored.fingerprint != fingerprint {
				return out, ErrConflict
			}
			err := json.Unmarshal(stored.response, &out)
			return out, err
		}
	}
	revision := int64(1)
	create := id == "" || duplicate
	if id != "" {
		old, ok := c.products[id]
		if !ok {
			return out, ErrNotFound
		}
		if duplicate {
			in = productInput06(old)
			in.Slug = "copy-" + ID()
			in.Visibility = "hidden"
		} else {
			if in.Revision != old.Revision || in.Revision >= MaxMinor {
				return out, ErrRevisionConflict
			}
			revision = in.Revision + 1
		}
	}
	if err = validateProduct06(in, m.currencies, m.taxonomy); err != nil {
		return out, err
	}
	for pid, p := range c.products {
		if p.Slug == in.Slug && (create || pid != id) {
			return out, ErrCommerceInput
		}
	}
	images := []api.OsImage{}
	for _, iid := range in.OSImageIDs {
		image, ok := c.images[iid]
		if !ok || image.MinDiskGB > in.Specs.DiskGB || in.Visibility == "public" && !image.Enabled {
			return out, ErrCommerceInput
		}
		images = append(images, imagePublic(image))
	}
	if create {
		id = ID()
	}
	out = api.AdminProduct{ID: id, Slug: in.Slug, Kind: in.Kind, NodeID: in.NodeID, Name: in.Name, Description: in.Description, Badges: in.Badges, Specs: in.Specs, Prices: in.Prices, Options: in.Options, OSImages: images, Stock: in.Stock, SortOrder: in.SortOrder, Visibility: in.Visibility, ControllerID: in.ControllerID, RemoteProductID: in.RemoteProductID, Revision: revision}
	for _, n := range m.taxonomy.Nodes {
		if n.ID == in.NodeID {
			out.RegionID = n.RegionID
			for _, r := range m.taxonomy.Regions {
				if r.ID == n.RegionID {
					out.TypeID = r.TypeID
				}
			}
		}
	}
	for i := range m.currencies.Currencies {
		for _, p := range in.Prices {
			if m.currencies.Currencies[i].Code == p.Currency {
				m.currencies.Currencies[i].PrecisionLocked = true
			}
		}
	}
	out, _ = m.projectProduct06Locked(out, true, "")
	c.products[id] = clone06(out)
	m.pricesRevision++
	if key != "" {
		raw, _ = json.Marshal(out)
		c.requests[memoKey] = memoryCommerceRequest{fingerprint, raw}
	}
	m.audits = append(m.audits, catalogAudit(actor, "product.saved", id, revision, "Catalog product saved"))
	return clone06(out), nil
}
func (m *Memory) Images06(ctx context.Context) ([]api.AdminOsImage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := []api.AdminOsImage{}
	for _, image := range m.catalog06Locked().images {
		out = append(out, image)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (m *Memory) PatchImage06(ctx context.Context, actor User, id string, name *string, enabled *bool) (api.AdminOsImage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out api.AdminOsImage
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if _, err := m.activeAdministratorLocked(actor.ID); err != nil {
		return out, err
	}
	c := m.catalog06Locked()
	out, ok := c.images[id]
	if !ok {
		return out, ErrNotFound
	}
	if name != nil {
		if *name == "" {
			return out, ErrCommerceInput
		}
		out.Name = *name
	}
	if enabled != nil {
		out.Enabled = *enabled
	}
	c.images[id] = out
	return out, nil
}
