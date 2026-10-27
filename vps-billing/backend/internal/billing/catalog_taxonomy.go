package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"
)

var ErrTaxonomy = errors.New("invalid catalog taxonomy")
var ErrTaxonomyConflict = errors.New("catalog taxonomy revision changed")
var ErrTaxonomyInUse = errors.New("catalog taxonomy is referenced")

type CatalogType struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	SortOrder int    `json:"sort_order"`
	Enabled   bool   `json:"enabled"`
}
type CatalogRegion struct {
	ID        string `json:"id"`
	TypeID    string `json:"type_id"`
	Name      string `json:"name"`
	SortOrder int    `json:"sort_order"`
	Enabled   bool   `json:"enabled"`
}
type CatalogNode struct {
	ID        string `json:"id"`
	RegionID  string `json:"region_id"`
	Name      string `json:"name"`
	SortOrder int    `json:"sort_order"`
	Enabled   bool   `json:"enabled"`
}
type TaxonomyConfiguration struct {
	Nodes    []CatalogNode   `json:"nodes"`
	Revision int64           `json:"revision"`
	Types    []CatalogType   `json:"types"`
	Regions  []CatalogRegion `json:"regions"`
}
type TaxonomyUpdate struct {
	Nodes    []CatalogNode   `json:"nodes"`
	Revision int64           `json:"revision"`
	Types    []CatalogType   `json:"types"`
	Regions  []CatalogRegion `json:"regions"`
	Reason   string          `json:"reason"`
}
type catalogTaxonomyStore interface {
	CatalogTaxonomy(context.Context, bool) (TaxonomyConfiguration, error)
	UpdateCatalogTaxonomy(context.Context, User, TaxonomyUpdate) (TaxonomyConfiguration, error)
}

func legacyRegionID(name string) string {
	sum := sha256.Sum256([]byte(name))
	return "r-" + hex.EncodeToString(sum[:])[:60]
}
func defaultNodeID(regionID string) string {
	sum := sha256.Sum256([]byte(regionID))
	return "n-" + hex.EncodeToString(sum[:])[:60]
}
func initialTaxonomy(plans []Plan) TaxonomyConfiguration {
	out := TaxonomyConfiguration{Revision: 1, Types: []CatalogType{{ID: "generic", Name: "通用服务器", Enabled: true}}, Regions: []CatalogRegion{}, Nodes: []CatalogNode{}}
	seen := map[string]bool{}
	for _, p := range plans {
		if !seen[p.Region] {
			seen[p.Region] = true
			out.Regions = append(out.Regions, CatalogRegion{ID: legacyRegionID(p.Region), TypeID: "generic", Name: p.Region, Enabled: true})
			out.Nodes = append(out.Nodes, CatalogNode{ID: defaultNodeID(legacyRegionID(p.Region)), RegionID: legacyRegionID(p.Region), Name: "默认节点", Enabled: true})
		}
	}
	return taxonomyProjection(out, true)
}
func taxonomyProjection(c TaxonomyConfiguration, admin bool) TaxonomyConfiguration {
	out := TaxonomyConfiguration{Revision: c.Revision, Types: []CatalogType{}, Regions: []CatalogRegion{}, Nodes: []CatalogNode{}}
	enabled := map[string]bool{}
	enabledRegions := map[string]bool{}
	for _, v := range c.Types {
		enabled[v.ID] = v.Enabled
		if admin || v.Enabled {
			out.Types = append(out.Types, v)
		}
	}
	for _, v := range c.Regions {
		enabledRegions[v.ID] = v.Enabled && enabled[v.TypeID]
		if admin || (v.Enabled && enabled[v.TypeID]) {
			out.Regions = append(out.Regions, v)
		}
	}
	for _, v := range c.Nodes {
		if admin || (v.Enabled && enabledRegions[v.RegionID]) {
			out.Nodes = append(out.Nodes, v)
		}
	}
	sort.Slice(out.Types, func(i, j int) bool {
		a, b := out.Types[i], out.Types[j]
		return a.SortOrder < b.SortOrder || a.SortOrder == b.SortOrder && a.ID < b.ID
	})
	sort.Slice(out.Regions, func(i, j int) bool {
		a, b := out.Regions[i], out.Regions[j]
		return a.SortOrder < b.SortOrder || a.SortOrder == b.SortOrder && a.ID < b.ID
	})
	sort.Slice(out.Nodes, func(i, j int) bool {
		a, b := out.Nodes[i], out.Nodes[j]
		return a.SortOrder < b.SortOrder || a.SortOrder == b.SortOrder && a.ID < b.ID
	})
	return out
}
func taxonomyRegion(c TaxonomyConfiguration, typeID, regionID string, live bool) (CatalogRegion, bool) {
	enabled := false
	found := false
	for _, v := range c.Types {
		if v.ID == typeID {
			enabled = v.Enabled
			found = true
			break
		}
	}
	if !found || live && !enabled {
		return CatalogRegion{}, false
	}
	for _, v := range c.Regions {
		if v.ID == regionID && v.TypeID == typeID && (!live || v.Enabled) {
			return v, true
		}
	}
	return CatalogRegion{}, false
}
func taxonomyNode(c TaxonomyConfiguration, typeID, regionID, nodeID string, live bool) (CatalogNode, bool) {
	if _, ok := taxonomyRegion(c, typeID, regionID, live); !ok {
		return CatalogNode{}, false
	}
	for _, n := range c.Nodes {
		if n.ID == nodeID && n.RegionID == regionID && (!live || n.Enabled) {
			return n, true
		}
	}
	return CatalogNode{}, false
}
func validTaxonomy(c TaxonomyConfiguration) bool {
	if c.Revision < 1 || c.Revision > MaxMinor || c.Types == nil || c.Regions == nil || c.Nodes == nil || len(c.Types) > 128 || len(c.Regions) > 512 || len(c.Nodes) > 2048 {
		return false
	}
	types, regions := map[string]bool{}, map[string]bool{}
	for _, v := range c.Types {
		if !validCatalogID(v.ID) || types[v.ID] || v.Name == "" || strings.TrimSpace(v.Name) != v.Name || !validText(v.Name, 120, false) || v.SortOrder < 0 || v.SortOrder > 1000000 {
			return false
		}
		types[v.ID] = true
	}
	for _, v := range c.Regions {
		if !validCatalogID(v.ID) || regions[v.ID] || !types[v.TypeID] || strings.TrimSpace(v.Name) == "" || !validText(v.Name, 480, false) || utf8.RuneCountInString(v.Name) > 120 || v.SortOrder < 0 || v.SortOrder > 1000000 {
			return false
		}
		regions[v.ID] = true
	}
	nodes := map[string]bool{}
	for _, v := range c.Nodes {
		if !validCatalogID(v.ID) || nodes[v.ID] || !regions[v.RegionID] || v.Name == "" || strings.TrimSpace(v.Name) != v.Name || !validText(v.Name, 120, false) || v.SortOrder < 0 || v.SortOrder > 1000000 {
			return false
		}
		nodes[v.ID] = true
	}
	return true
}
func applyTaxonomy(old TaxonomyConfiguration, input TaxonomyUpdate, plans []CatalogPlan) (TaxonomyConfiguration, error) {
	if input.Revision < 1 || input.Revision > MaxMinor {
		return TaxonomyConfiguration{}, ErrTaxonomy
	}
	if input.Revision != old.Revision || old.Revision == MaxMinor {
		return TaxonomyConfiguration{}, ErrTaxonomyConflict
	}
	if _, ok := catalogReason(input.Reason); !ok {
		return TaxonomyConfiguration{}, ErrTaxonomy
	}
	next := TaxonomyConfiguration{Revision: old.Revision + 1, Types: input.Types, Regions: input.Regions, Nodes: input.Nodes}
	if !validTaxonomy(next) {
		return TaxonomyConfiguration{}, ErrTaxonomy
	}
	// Deletion is permitted only when no live or draft product references the row.
	// Region and node parents are immutable while referenced; this avoids
	// silently moving an existing product into another purchase category.
	for _, p := range plans {
		oldRegion, _ := taxonomyRegion(old, p.TypeID, p.RegionID, false)
		nr, exists := taxonomyRegion(next, p.TypeID, p.RegionID, false)
		oldNode, _ := taxonomyNode(old, p.TypeID, p.RegionID, p.NodeID, false)
		nextNode, nodeExists := taxonomyNode(next, p.TypeID, p.RegionID, p.NodeID, false)
		if !exists || oldRegion.TypeID != nr.TypeID || !nodeExists || oldNode.RegionID != nextNode.RegionID {
			return TaxonomyConfiguration{}, ErrTaxonomyInUse
		}
		if p.Available {
			if _, live := taxonomyNode(next, p.TypeID, p.RegionID, p.NodeID, true); !live {
				return TaxonomyConfiguration{}, ErrTaxonomyInUse
			}
		}
	}
	return taxonomyProjection(next, true), nil
}
func (m *Memory) CatalogTaxonomy(ctx context.Context, admin bool) (TaxonomyConfiguration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return TaxonomyConfiguration{}, err
	}
	return taxonomyProjection(m.taxonomy, admin), nil
}
func (m *Memory) UpdateCatalogTaxonomy(ctx context.Context, actor User, input TaxonomyUpdate) (TaxonomyConfiguration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return TaxonomyConfiguration{}, err
	}
	actor, err := m.activeAdministratorLocked(actor.ID)
	if err != nil {
		return TaxonomyConfiguration{}, err
	}
	if m.pricesRevision == MaxMinor {
		return TaxonomyConfiguration{}, ErrCatalogConflict
	}
	plans := []CatalogPlan{}
	for _, p := range m.plans {
		plans = append(plans, catalogPlan(p, m.prices[p.ID]))
	}
	next, err := applyTaxonomy(m.taxonomy, input, plans)
	if err != nil {
		return TaxonomyConfiguration{}, err
	}
	for n, p := range m.plans {
		r, _ := taxonomyRegion(next, p.TypeID, p.RegionID, false)
		m.plans[n].Region = r.Name
	}
	m.taxonomy = next
	m.pricesRevision++
	m.audits = append(m.audits, catalogAudit(actor, "catalog.taxonomy_updated", "catalog-taxonomy", next.Revision, strings.TrimSpace(input.Reason)))
	return taxonomyProjection(next, true), nil
}
func (a *App) taxonomyHTTP(w http.ResponseWriter, r *http.Request, actor User, admin bool) {
	store, ok := a.Store.(catalogTaxonomyStore)
	if !ok {
		a.internal(w, r, errors.New("catalog taxonomy store unavailable"))
		return
	}
	if r.URL.RawQuery != "" {
		fail(w, r, 400, "INVALID_TAXONOMY", "分类请求不支持查询参数")
		return
	}
	if r.Method == "GET" {
		out, err := store.CatalogTaxonomy(r.Context(), admin)
		if err != nil {
			a.taxonomyError(w, r, err)
		} else {
			jsonOut(w, 200, out)
		}
		return
	}
	if !admin || r.Method != "POST" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "不支持此请求方式")
		return
	}
	var input TaxonomyUpdate
	if !financeBodyLimit(w, r, &input, 2<<20) {
		return
	}
	out, err := store.UpdateCatalogTaxonomy(r.Context(), actor, input)
	if err != nil {
		a.taxonomyError(w, r, err)
	} else {
		jsonOut(w, 200, out)
	}
}
func (a *App) taxonomyError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrTaxonomy):
		fail(w, r, 400, "INVALID_TAXONOMY", "请检查类型、地区、节点、排序、归属和修改原因")
	case errors.Is(err, ErrTaxonomyConflict):
		fail(w, r, 409, "TAXONOMY_CONFLICT", "分类已改变，请重新读取")
	case errors.Is(err, ErrTaxonomyInUse):
		fail(w, r, 409, "TAXONOMY_IN_USE", "分类仍被商品引用；请先下架或调整商品归属")
	case errors.Is(err, ErrAdminRequired):
		fail(w, r, 403, "FORBIDDEN", "需要管理员权限")
	default:
		a.catalogError(w, r, err)
	}
}
