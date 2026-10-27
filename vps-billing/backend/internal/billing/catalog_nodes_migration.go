package billing

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Migration12 is additive. Its deterministic default nodes preserve the existing
// selected region and quotation; neither taxonomy nor price revisions change.
func prepareCatalogNodesMigration(ctx context.Context, conn *sql.Conn, q string) (string, bool, error) {
	if strings.HasPrefix(q, "CREATE TABLE IF NOT EXISTS catalog_nodes (") {
		var kind, engine, collation string
		err := conn.QueryRowContext(ctx, "SELECT TABLE_TYPE,COALESCE(ENGINE,''),@@collation_database FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='catalog_nodes'").Scan(&kind, &engine, &collation)
		if errors.Is(err, sql.ErrNoRows) {
			return q, false, nil
		}
		if err != nil {
			return "", false, err
		}
		if kind != "BASE TABLE" || engine != "InnoDB" {
			return "", false, errors.New("catalog node object differs from migration")
		}
		if err = validatePreflightTableShape(ctx, conn, 12, map[string]string{"catalog_nodes": engine}, collation, true); err != nil {
			return "", false, err
		}
		return q, true, nil
	}
	if q == "ALTER TABLE plans ADD COLUMN node_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL" || q == "ALTER TABLE plans MODIFY COLUMN node_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL" {
		var kind, nullable, extra, generated string
		var def, charset, collation sql.NullString
		err := conn.QueryRowContext(ctx, "SELECT COLUMN_TYPE,IS_NULLABLE,EXTRA,GENERATION_EXPRESSION,COLUMN_DEFAULT,CHARACTER_SET_NAME,COLLATION_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='plans' AND COLUMN_NAME='node_id'").Scan(&kind, &nullable, &extra, &generated, &def, &charset, &collation)
		if errors.Is(err, sql.ErrNoRows) {
			return q, false, nil
		}
		if err != nil {
			return "", false, err
		}
		if kind != "varchar(64)" || (nullable != "YES" && nullable != "NO") || extra != "" || generated != "" || def.Valid || !charset.Valid || charset.String != "ascii" || !collation.Valid || collation.String != "ascii_bin" {
			return "", false, errors.New("catalog node column differs from migration")
		}
		return q, strings.Contains(q, " ADD COLUMN ") || nullable == "NO", nil
	}
	if q == "ALTER TABLE plans ADD CONSTRAINT plans_catalog_node FOREIGN KEY(node_id) REFERENCES catalog_nodes(id)" {
		var column, table, key, deletion, update string
		var same bool
		err := conn.QueryRowContext(ctx, "SELECT k.COLUMN_NAME,k.REFERENCED_TABLE_NAME,k.REFERENCED_COLUMN_NAME,r.DELETE_RULE,r.UPDATE_RULE,k.REFERENCED_TABLE_SCHEMA=DATABASE() FROM information_schema.KEY_COLUMN_USAGE k JOIN information_schema.REFERENTIAL_CONSTRAINTS r ON r.CONSTRAINT_SCHEMA=k.CONSTRAINT_SCHEMA AND r.CONSTRAINT_NAME=k.CONSTRAINT_NAME WHERE k.CONSTRAINT_SCHEMA=DATABASE() AND k.TABLE_NAME='plans' AND k.CONSTRAINT_NAME='plans_catalog_node'").Scan(&column, &table, &key, &deletion, &update, &same)
		if errors.Is(err, sql.ErrNoRows) {
			return q, false, nil
		}
		if err != nil {
			return "", false, err
		}
		if column != "node_id" || table != "catalog_nodes" || key != "id" || !same || (deletion != "RESTRICT" && deletion != "NO ACTION") || (update != "RESTRICT" && update != "NO ACTION") {
			return "", false, errors.New("catalog node foreign key differs from migration")
		}
		return q, true, nil
	}
	if q == "INSERT INTO catalog_nodes(id,region_id,name,sort_order,enabled) SELECT CONCAT('n-',LEFT(SHA2(BINARY id,256),60)),id,'默认节点',0,TRUE FROM catalog_regions ON DUPLICATE KEY UPDATE id=catalog_nodes.id" || q == "UPDATE plans SET node_id=CONCAT('n-',LEFT(SHA2(BINARY region_id,256),60)) WHERE node_id IS NULL" {
		return q, false, nil
	}
	return "", false, errors.New("unsupported catalog node migration")
}
func prevalidateCatalogNodesMigration(ctx context.Context, conn *sql.Conn, source string) error {
	// Check parent data and all colliding objects before the first new DDL.
	rows, err := conn.QueryContext(ctx, "SELECT id FROM catalog_regions ORDER BY id")
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		count++
		if count > 512 || !validCatalogID(id) {
			rows.Close()
			return errors.New("existing regions require repair before node migration")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var bad int
	if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM plans p LEFT JOIN catalog_regions r ON r.id=p.region_id WHERE r.id IS NULL OR p.type_id<>r.type_id").Scan(&bad); err != nil {
		return err
	}
	if bad != 0 {
		return errors.New("existing product taxonomy differs from stored parents")
	}
	for _, q := range strings.Split(source, ";") {
		if q = strings.TrimSpace(q); q != "" {
			if _, _, err = prepareCatalogNodesMigration(ctx, conn, q); err != nil {
				return err
			}
		}
	}
	var exists int
	if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='catalog_nodes'").Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM catalog_nodes n WHERE BINARY n.name<>BINARY '默认节点' OR n.sort_order<>0 OR n.enabled<>1 OR n.id<>CONCAT('n-',LEFT(SHA2(BINARY n.region_id,256),60)) OR NOT EXISTS (SELECT 1 FROM catalog_regions r WHERE r.id=n.region_id)").Scan(&bad); err != nil {
			return err
		}
		if bad != 0 {
			return errors.New("node migration contains unexpected existing nodes")
		}
	}
	if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='plans' AND COLUMN_NAME='node_id'").Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM plans WHERE node_id IS NOT NULL AND BINARY node_id<>BINARY CONCAT('n-',LEFT(SHA2(BINARY region_id,256),60))").Scan(&bad); err != nil {
			return err
		}
		if bad != 0 {
			return errors.New("node migration contains unexpected product assignments")
		}
	}
	return nil
}
