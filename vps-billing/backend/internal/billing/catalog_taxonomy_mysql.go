package billing

import (
	"context"
	"database/sql"
	"strings"
)

func readTaxonomy(ctx context.Context, q currencyQuerier, lock string) (TaxonomyConfiguration, error) {
	out := TaxonomyConfiguration{Types: []CatalogType{}, Regions: []CatalogRegion{}, Nodes: []CatalogNode{}}
	if err := q.QueryRowContext(ctx, "SELECT revision FROM catalog_taxonomy_settings WHERE id=1"+lock).Scan(&out.Revision); err != nil {
		return out, err
	}
	rows, err := q.QueryContext(ctx, "SELECT id,name,sort_order,enabled FROM catalog_types ORDER BY id"+lock)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v CatalogType
		if err = rows.Scan(&v.ID, &v.Name, &v.SortOrder, &v.Enabled); err != nil {
			rows.Close()
			return out, err
		}
		out.Types = append(out.Types, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = q.QueryContext(ctx, "SELECT id,type_id,name,sort_order,enabled FROM catalog_regions ORDER BY id"+lock)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v CatalogRegion
		if err = rows.Scan(&v.ID, &v.TypeID, &v.Name, &v.SortOrder, &v.Enabled); err != nil {
			rows.Close()
			return out, err
		}
		out.Regions = append(out.Regions, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = q.QueryContext(ctx, "SELECT id,region_id,name,sort_order,enabled FROM catalog_nodes ORDER BY id"+lock)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v CatalogNode
		if err = rows.Scan(&v.ID, &v.RegionID, &v.Name, &v.SortOrder, &v.Enabled); err != nil {
			rows.Close()
			return out, err
		}
		out.Nodes = append(out.Nodes, v)
	}
	err = rows.Err()
	rows.Close()
	if err == nil && !validTaxonomy(out) {
		err = ErrTaxonomy
	}
	return out, err
}
func (m *MySQL) CatalogTaxonomy(ctx context.Context, admin bool) (TaxonomyConfiguration, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return TaxonomyConfiguration{}, err
	}
	defer tx.Rollback()
	out, err := readTaxonomy(ctx, tx, "")
	if err != nil {
		return out, err
	}
	return taxonomyProjection(out, admin), tx.Commit()
}
func (m *MySQL) UpdateCatalogTaxonomy(ctx context.Context, actor User, input TaxonomyUpdate) (TaxonomyConfiguration, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return TaxonomyConfiguration{}, err
	}
	defer tx.Rollback()
	actor, err = lockActiveAdministrator(ctx, tx, actor.ID)
	if err != nil {
		return TaxonomyConfiguration{}, err
	}
	config, priceRevision, err := readCurrencies(ctx, tx, " FOR UPDATE")
	if err != nil {
		return TaxonomyConfiguration{}, err
	}
	if priceRevision == MaxMinor {
		return TaxonomyConfiguration{}, ErrCatalogConflict
	}
	old, err := readTaxonomy(ctx, tx, " FOR UPDATE")
	if err != nil {
		return TaxonomyConfiguration{}, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+catalogPlanColumns+" FROM plans ORDER BY id FOR UPDATE")
	if err != nil {
		return TaxonomyConfiguration{}, err
	}
	plans := []CatalogPlan{}
	for rows.Next() {
		p, e := scanCatalogPlan(rows)
		if e != nil {
			rows.Close()
			return TaxonomyConfiguration{}, e
		}
		plans = append(plans, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return TaxonomyConfiguration{}, err
	}
	next, err := applyTaxonomy(old, input, plans)
	if err != nil {
		return TaxonomyConfiguration{}, err
	}
	// Upsert parents first, then children. Delete only after reference validation.
	for _, v := range next.Types {
		if _, err = tx.ExecContext(ctx, "INSERT INTO catalog_types(id,name,sort_order,enabled) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE name=VALUES(name),sort_order=VALUES(sort_order),enabled=VALUES(enabled)", v.ID, v.Name, v.SortOrder, v.Enabled); err != nil {
			return TaxonomyConfiguration{}, err
		}
	}
	for _, v := range next.Regions {
		if _, err = tx.ExecContext(ctx, "INSERT INTO catalog_regions(id,type_id,name,sort_order,enabled) VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE type_id=VALUES(type_id),name=VALUES(name),sort_order=VALUES(sort_order),enabled=VALUES(enabled)", v.ID, v.TypeID, v.Name, v.SortOrder, v.Enabled); err != nil {
			return TaxonomyConfiguration{}, err
		}
	}
	for _, v := range next.Nodes {
		if _, err = tx.ExecContext(ctx, "INSERT INTO catalog_nodes(id,region_id,name,sort_order,enabled) VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE region_id=VALUES(region_id),name=VALUES(name),sort_order=VALUES(sort_order),enabled=VALUES(enabled)", v.ID, v.RegionID, v.Name, v.SortOrder, v.Enabled); err != nil {
			return TaxonomyConfiguration{}, err
		}
	}
	nodeIDs := map[string]bool{}
	for _, v := range next.Nodes {
		nodeIDs[v.ID] = true
	}
	for _, v := range old.Nodes {
		if !nodeIDs[v.ID] {
			if _, err = tx.ExecContext(ctx, "DELETE FROM catalog_nodes WHERE id=?", v.ID); err != nil {
				return TaxonomyConfiguration{}, err
			}
		}
	}
	regionIDs, typeIDs := map[string]bool{}, map[string]bool{}
	for _, v := range next.Regions {
		regionIDs[v.ID] = true
	}
	for _, v := range next.Types {
		typeIDs[v.ID] = true
	}
	for _, v := range old.Regions {
		if !regionIDs[v.ID] {
			if _, err = tx.ExecContext(ctx, "DELETE FROM catalog_regions WHERE id=?", v.ID); err != nil {
				return TaxonomyConfiguration{}, err
			}
		}
	}
	for _, v := range old.Types {
		if !typeIDs[v.ID] {
			if _, err = tx.ExecContext(ctx, "DELETE FROM catalog_types WHERE id=?", v.ID); err != nil {
				return TaxonomyConfiguration{}, err
			}
		}
	}
	// This changes current catalogue labels only; all historical JSON stays intact.
	if _, err = tx.ExecContext(ctx, "UPDATE plans p JOIN catalog_regions r ON r.id=p.region_id SET p.region=r.name WHERE BINARY p.region<>BINARY r.name"); err != nil {
		return TaxonomyConfiguration{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE catalog_taxonomy_settings SET revision=? WHERE id=1", next.Revision); err != nil {
		return TaxonomyConfiguration{}, err
	}
	if err = writeCurrencies(ctx, tx, config, priceRevision+1); err != nil {
		return TaxonomyConfiguration{}, err
	}
	if err = insertCurrencyAudit(ctx, tx, catalogAudit(actor, "catalog.taxonomy_updated", "catalog-taxonomy", next.Revision, strings.TrimSpace(input.Reason))); err != nil {
		return TaxonomyConfiguration{}, err
	}
	return taxonomyProjection(next, true), tx.Commit()
}
