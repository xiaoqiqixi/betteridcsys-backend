package billing

import (
	api "aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"
)

func legacyTaxonomyName06(name api.LocalizedText) string {
	s := strings.TrimSpace(text06(name))
	if len(s) > 120 {
		s = s[:120]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}
func legacyTaxonomy06(v api.CatalogTaxonomy) TaxonomyUpdate {
	out := TaxonomyUpdate{Revision: v.Revision, Reason: "Catalog taxonomy updated", Types: []CatalogType{}, Regions: []CatalogRegion{}, Nodes: []CatalogNode{}}
	// Legacy INT sort columns receive a rank; the full contract integer and
	// localized metadata are retained in the commerce document.
	for i, t := range v.Types {
		out.Types = append(out.Types, CatalogType{ID: t.ID, Name: legacyTaxonomyName06(t.Name), Enabled: t.Visible, SortOrder: i})
	}
	for i, r := range v.Regions {
		out.Regions = append(out.Regions, CatalogRegion{ID: r.ID, TypeID: r.TypeID, Name: legacyTaxonomyName06(r.Name), Enabled: r.Visible, SortOrder: i})
	}
	for i, n := range v.Nodes {
		out.Nodes = append(out.Nodes, CatalogNode{ID: n.ID, RegionID: n.RegionID, Name: legacyTaxonomyName06(n.Name), Enabled: n.Visible, SortOrder: i})
	}
	return out
}
func sortTaxonomy06(v *api.CatalogTaxonomy) {
	sort.Slice(v.Types, func(i, j int) bool {
		a, b := v.Types[i], v.Types[j]
		return a.SortOrder < b.SortOrder || a.SortOrder == b.SortOrder && a.ID < b.ID
	})
	sort.Slice(v.Regions, func(i, j int) bool {
		a, b := v.Regions[i], v.Regions[j]
		return a.SortOrder < b.SortOrder || a.SortOrder == b.SortOrder && a.ID < b.ID
	})
	sort.Slice(v.Nodes, func(i, j int) bool {
		a, b := v.Nodes[i], v.Nodes[j]
		return a.SortOrder < b.SortOrder || a.SortOrder == b.SortOrder && a.ID < b.ID
	})
}
func readTaxonomy06(ctx context.Context, q currencyQuerier, admin bool) (api.CatalogTaxonomy, error) {
	var stored api.CatalogTaxonomy
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT document FROM commerce_taxonomy WHERE id=1").Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &stored)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return stored, err
	}
	legacy, err := readTaxonomy(ctx, q, "")
	if err != nil {
		return stored, err
	}
	legacy = taxonomyProjection(legacy, admin)
	out := api.CatalogTaxonomy{Revision: legacy.Revision, Types: []api.CatalogType{}, Regions: []api.CatalogRegion{}, Nodes: []api.CatalogNode{}}
	types := map[string]api.CatalogType{}
	for _, v := range stored.Types {
		types[v.ID] = v
	}
	regions := map[string]api.CatalogRegion{}
	for _, v := range stored.Regions {
		regions[v.ID] = v
	}
	nodes := map[string]api.CatalogNode{}
	for _, v := range stored.Nodes {
		nodes[v.ID] = v
	}
	for _, old := range legacy.Types {
		v, exists := types[old.ID]
		if !exists {
			v = api.CatalogType{ID: old.ID, Slug: "type-" + digest(old.ID)[:12], Icon: "server"}
		}
		if legacyTaxonomyName06(v.Name) != old.Name {
			v.Name = api.LocalizedText{"en": old.Name}
		}
		if !exists || stored.Revision != legacy.Revision {
			v.SortOrder = int64(old.SortOrder)
		}
		v.Visible = old.Enabled
		out.Types = append(out.Types, v)
	}
	for _, old := range legacy.Regions {
		v, exists := regions[old.ID]
		if !exists {
			v = api.CatalogRegion{ID: old.ID, Country: "ZZ"}
		}
		if legacyTaxonomyName06(v.Name) != old.Name {
			v.Name = api.LocalizedText{"en": old.Name}
		}
		if !exists || stored.Revision != legacy.Revision {
			v.SortOrder = int64(old.SortOrder)
		}
		v.Visible = old.Enabled
		v.TypeID = old.TypeID
		out.Regions = append(out.Regions, v)
	}
	for _, old := range legacy.Nodes {
		v, exists := nodes[old.ID]
		if !exists {
			v.ID = old.ID
		}
		if legacyTaxonomyName06(v.Name) != old.Name {
			v.Name = api.LocalizedText{"en": old.Name}
		}
		if !exists || stored.Revision != legacy.Revision {
			v.SortOrder = int64(old.SortOrder)
		}
		v.Visible = old.Enabled
		v.RegionID = old.RegionID
		out.Nodes = append(out.Nodes, v)
	}
	sortTaxonomy06(&out)
	return out, nil
}
func (m *MySQL) Taxonomy06(ctx context.Context, admin bool) (api.CatalogTaxonomy, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return api.CatalogTaxonomy{}, err
	}
	defer tx.Rollback()
	v, err := readTaxonomy06(ctx, tx, admin)
	if err != nil {
		return v, err
	}
	return v, tx.Commit()
}
func (m *MySQL) SaveTaxonomy06(ctx context.Context, actor User, input api.CatalogTaxonomy) (api.CatalogTaxonomy, error) {
	raw, _ := json.Marshal(input)
	if api.ValidateInput("adminUpdateTaxonomy", raw) != nil {
		return input, ErrCommerceInput
	}
	slugs := map[string]bool{}
	for _, v := range input.Types {
		if slugs[v.Slug] {
			return input, ErrCommerceInput
		}
		slugs[v.Slug] = true
	}
	sortTaxonomy06(&input)
	err := retryCommerceWrite06(ctx, func() error {
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return err
		}
		defer tx.Rollback()
		updated, err := updateCatalogTaxonomyTx06(ctx, tx, actor, legacyTaxonomy06(input), true)
		if errors.Is(err, ErrTaxonomyConflict) {
			return ErrRevisionConflict
		}
		if err != nil {
			return err
		}
		next := input
		next.Revision = updated.Revision
		raw, _ := json.Marshal(next)
		if _, err = tx.ExecContext(ctx, "INSERT INTO commerce_taxonomy(id,document) VALUES(1,?) ON DUPLICATE KEY UPDATE document=VALUES(document)", raw); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return input, err
	}
	input.Revision++
	return input, nil
}
func (a *App) taxonomy06HTTP(w http.ResponseWriter, r *http.Request, actor User, admin bool) {
	m, ok := a.Store.(*MySQL)
	if !ok {
		fail(w, r, 503, "FEATURE_UNAVAILABLE", "Catalog taxonomy requires persistent storage")
		return
	}
	var out api.CatalogTaxonomy
	var err error
	switch r.Method {
	case "GET":
		out, err = m.Taxonomy06(r.Context(), admin)
	case "POST":
		if !admin {
			fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
			return
		}
		var in api.CatalogTaxonomy
		if !commerceBody(w, r, "adminUpdateTaxonomy", &in) {
			return
		}
		out, err = m.SaveTaxonomy06(r.Context(), actor, in)
	default:
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}
	if err != nil {
		if errors.Is(err, ErrTaxonomyInUse) || errors.Is(err, ErrTaxonomy) {
			a.taxonomyError(w, r, err)
		} else {
			a.commerceError(w, r, err)
		}
	} else {
		jsonOut(w, 200, out)
	}
}
