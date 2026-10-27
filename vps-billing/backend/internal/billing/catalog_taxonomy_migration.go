package billing

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// DDL resumes only after inspecting existing physical definitions. Existing
// classification rows must match the deterministic legacy mapping before DML.
func prepareTaxonomyMigration(ctx context.Context, conn *sql.Conn, q string) (string, bool, error) {
	for _, table := range []string{"catalog_types", "catalog_regions", "catalog_taxonomy_settings"} {
		if strings.HasPrefix(q, "CREATE TABLE IF NOT EXISTS "+table+" (") {
			var kind, engine, collation string
			err := conn.QueryRowContext(ctx, "SELECT TABLE_TYPE,COALESCE(ENGINE,''),@@collation_database FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", table).Scan(&kind, &engine, &collation)
			if errors.Is(err, sql.ErrNoRows) {
				return q, false, nil
			}
			if err != nil {
				return "", false, err
			}
			if kind != "BASE TABLE" || engine != "InnoDB" {
				return "", false, errors.New("taxonomy object differs from migration")
			}
			if err = validatePreflightTableShape(ctx, conn, 11, map[string]string{table: engine}, collation, true); err != nil {
				return "", false, err
			}
			return q, true, nil
		}
	}
	for _, name := range []string{"type_id", "region_id"} {
		if strings.HasPrefix(q, "ALTER TABLE plans ADD COLUMN "+name+" ") || strings.HasPrefix(q, "ALTER TABLE plans MODIFY COLUMN "+name+" ") {
			var kind, nullable, extra, generated string
			var def, charset, collation sql.NullString
			err := conn.QueryRowContext(ctx, "SELECT COLUMN_TYPE,IS_NULLABLE,EXTRA,GENERATION_EXPRESSION,COLUMN_DEFAULT,CHARACTER_SET_NAME,COLLATION_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='plans' AND COLUMN_NAME=?", name).Scan(&kind, &nullable, &extra, &generated, &def, &charset, &collation)
			if errors.Is(err, sql.ErrNoRows) {
				return q, false, nil
			}
			if err != nil {
				return "", false, err
			}
			if kind != "varchar(64)" || (nullable != "YES" && nullable != "NO") || extra != "" || generated != "" || def.Valid || !charset.Valid || charset.String != "ascii" || !collation.Valid || collation.String != "ascii_bin" {
				return "", false, errors.New("taxonomy plan column differs from migration")
			}
			return q, strings.Contains(q, " ADD COLUMN ") || nullable == "NO", nil
		}
	}
	for name, column := range map[string]string{"plans_catalog_type": "type_id", "plans_catalog_region": "region_id"} {
		if strings.HasPrefix(q, "ALTER TABLE plans ADD CONSTRAINT "+name+" ") {
			var actual, refTable, refColumn, deletion, update string
			var same bool
			err := conn.QueryRowContext(ctx, "SELECT k.COLUMN_NAME,k.REFERENCED_TABLE_NAME,k.REFERENCED_COLUMN_NAME,r.DELETE_RULE,r.UPDATE_RULE,k.REFERENCED_TABLE_SCHEMA=DATABASE() FROM information_schema.KEY_COLUMN_USAGE k JOIN information_schema.REFERENTIAL_CONSTRAINTS r ON r.CONSTRAINT_SCHEMA=k.CONSTRAINT_SCHEMA AND r.CONSTRAINT_NAME=k.CONSTRAINT_NAME WHERE k.CONSTRAINT_SCHEMA=DATABASE() AND k.TABLE_NAME='plans' AND k.CONSTRAINT_NAME=?", name).Scan(&actual, &refTable, &refColumn, &deletion, &update, &same)
			if errors.Is(err, sql.ErrNoRows) {
				return q, false, nil
			}
			if err != nil {
				return "", false, err
			}
			want := "catalog_types"
			if column == "region_id" {
				want = "catalog_regions"
			}
			if actual != column || refTable != want || refColumn != "id" || !same || (deletion != "RESTRICT" && deletion != "NO ACTION") || (update != "RESTRICT" && update != "NO ACTION") {
				return "", false, errors.New("taxonomy foreign key differs from migration")
			}
			return q, true, nil
		}
	}
	if strings.HasPrefix(q, "INSERT INTO catalog_") || strings.HasPrefix(q, "UPDATE plans SET type_id=") {
		return q, false, nil
	}
	return "", false, errors.New("unsupported taxonomy migration")
}
func prevalidateTaxonomyMigration(ctx context.Context, conn *sql.Conn, source string) error {
	// Validate legacy labels and registry bounds before any new DDL. Distinct
	// binary text preserves case, leading/trailing spaces and Unicode exactly.
	rows, err := conn.QueryContext(ctx, "SELECT DISTINCT BINARY region FROM plans ORDER BY BINARY region")
	if err != nil {
		return err
	}
	regions := []CatalogRegion{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		regions = append(regions, CatalogRegion{ID: legacyRegionID(name), TypeID: "generic", Name: name, Enabled: true})
		if len(regions) > 512 {
			rows.Close()
			return errors.New("legacy regions exceed taxonomy capacity")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !validTaxonomy(TaxonomyConfiguration{Revision: 1, Types: []CatalogType{{ID: "generic", Name: "通用服务器", Enabled: true}}, Regions: regions, Nodes: []CatalogNode{}}) {
		return errors.New("legacy regions require explicit repair before taxonomy migration")
	}

	for _, q := range strings.Split(source, ";") {
		if q = strings.TrimSpace(q); q != "" {
			if _, _, err := prepareTaxonomyMigration(ctx, conn, q); err != nil {
				return err
			}
		}
	}
	// A pre-existing table may be a resumed migration, never arbitrary registry
	// data. No financial or historical table is written by this migration.
	for table, query := range map[string]string{
		"catalog_types":             "SELECT COUNT(*) FROM catalog_types WHERE BINARY id<>BINARY 'generic' OR BINARY name<>BINARY '通用服务器' OR sort_order<>0 OR enabled<>1",
		"catalog_regions":           "SELECT COUNT(*) FROM catalog_regions r WHERE r.type_id<>'generic' OR r.sort_order<>0 OR r.enabled<>1 OR NOT EXISTS (SELECT 1 FROM plans p WHERE BINARY p.region=BINARY r.name AND r.id=CONCAT('r-',LEFT(SHA2(BINARY p.region,256),60)))",
		"catalog_taxonomy_settings": "SELECT COUNT(*) FROM catalog_taxonomy_settings WHERE id<>1 OR revision<>1",
	} {
		var exists int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", table).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			continue
		}
		var bad int
		if err := conn.QueryRowContext(ctx, query).Scan(&bad); err != nil {
			return err
		}
		if bad != 0 {
			return errors.New("taxonomy migration contains unexpected existing data")
		}
	}
	for _, name := range []string{"type_id", "region_id"} {
		var exists int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='plans' AND COLUMN_NAME=?", name).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			continue
		}
		want := "'generic'"
		if name == "region_id" {
			want = "CONCAT('r-',LEFT(SHA2(BINARY region,256),60))"
		}
		var bad int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM plans WHERE "+name+" IS NOT NULL AND BINARY "+name+"<>BINARY "+want).Scan(&bad); err != nil {
			return err
		}
		if bad != 0 {
			return errors.New("taxonomy migration contains unexpected plan assignments")
		}
	}
	return nil
}
