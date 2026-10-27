package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

func nodeMigrationLegacyRegistryRows(t *testing.T, ctx context.Context, m *MySQL) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for table, query := range map[string]string{
		"types":         "SELECT JSON_ARRAY(id,name,sort_order,enabled) FROM catalog_types ORDER BY id",
		"regions":       "SELECT JSON_ARRAY(id,type_id,name,sort_order,enabled) FROM catalog_regions ORDER BY id",
		"settings":      "SELECT JSON_ARRAY(id,revision) FROM catalog_taxonomy_settings ORDER BY id",
		"plan_bindings": "SELECT JSON_ARRAY(id,type_id,region_id) FROM plans ORDER BY id",
	} {
		rows, err := m.DB.QueryContext(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		out[table] = []string{}
		for rows.Next() {
			var raw string
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			out[table] = append(out[table], raw)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}
func TestMySQLCatalogNodesMigrationPreservesV11RegistryAndHistory(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	taxonomyLegacyV10Fixture(t, ctx, m)
	// Manufacture an actual immutable schema11 prefix, not schema12 with objects
	// removed after the current migrator has already modified the fixture.
	for _, q := range migrationStatements(schemaVersions[10]) {
		migrationExec(t, ctx, m.DB, q)
	}
	migrationExec(t, ctx, m.DB, "INSERT INTO schema_migrations(version,checksum) VALUES(11,?)", digest(schemaVersions[10]))
	migrationExec(t, ctx, m.DB, "UPDATE catalog_taxonomy_settings SET revision=23 WHERE id=1")
	migrationExec(t, ctx, m.DB, "INSERT INTO catalog_regions(id,type_id,name,sort_order,enabled) VALUES('operator-region','generic','Operator exact region',7,FALSE)")
	migrationExec(t, ctx, m.DB, "INSERT INTO plans(id,name,description,cpu,memory_gb,disk_gb,transfer_tb,region,price_minor,currency,available,type_id,region_id) VALUES('operator-node-plan','Operator plan','Preserve existing mapping',2,4,80,2,'Operator exact region',1234,'USD',FALSE,'generic','operator-region')")
	migrationExec(t, ctx, m.DB, "INSERT INTO plan_prices(plan_id,currency,amount_minor,available) VALUES('operator-node-plan','USD',1234,TRUE)")
	beforeBusiness := taxonomyMigrationBusinessRows(t, ctx, m)
	beforeRegistry := nodeMigrationLegacyRegistryRows(t, ctx, m)
	beforeHistory := migrationHistory(t, ctx, m)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal("upgrade real schema11", err)
	}
	if !reflect.DeepEqual(beforeBusiness, taxonomyMigrationBusinessRows(t, ctx, m)) || !reflect.DeepEqual(beforeRegistry, nodeMigrationLegacyRegistryRows(t, ctx, m)) {
		t.Fatal("node migration rewrote prior prices, history, classification or revisions")
	}
	c, err := m.CatalogTaxonomy(ctx, true)
	if err != nil || c.Revision != 23 || len(c.Nodes) != len(c.Regions) {
		t.Fatal("node migration changed registry revision or default-node cardinality", c, err)
	}
	for _, region := range c.Regions {
		sum := sha256.Sum256([]byte(region.ID))
		want := "n-" + hex.EncodeToString(sum[:])[:60]
		found := false
		for _, node := range c.Nodes {
			if node.RegionID == region.ID {
				if found || node.ID != want || node.Name != "默认节点" || !node.Enabled {
					t.Fatal("default node not derived from exact region ID", region, node)
				}
				found = true
			}
		}
		if !found {
			t.Fatal("missing migrated node", region)
		}
	}
	var invalidBindings int
	if err = m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM plans p LEFT JOIN catalog_nodes n ON n.id=p.node_id WHERE n.id IS NULL OR n.region_id<>p.region_id OR p.node_id<>CONCAT('n-',LEFT(SHA2(BINARY p.region_id,256),60))").Scan(&invalidBindings); err != nil || invalidBindings != 0 {
		t.Fatal("plans lost real node relationships", invalidBindings, err)
	}
	afterHistory := migrationHistory(t, ctx, m)
	if len(afterHistory) != len(schemaVersions) || !reflect.DeepEqual(beforeHistory, afterHistory[:11]) {
		t.Fatal("node migration changed immutable schema11 checksums")
	}
	if err = m.Migrate(ctx); err != nil {
		t.Fatal("repeat node migration", err)
	}
	repeated, err := m.CatalogTaxonomy(ctx, true)
	if err != nil || !reflect.DeepEqual(c, repeated) || !reflect.DeepEqual(afterHistory, migrationHistory(t, ctx, m)) || !reflect.DeepEqual(beforeBusiness, taxonomyMigrationBusinessRows(t, ctx, m)) || !reflect.DeepEqual(beforeRegistry, nodeMigrationLegacyRegistryRows(t, ctx, m)) {
		t.Fatal("repeat node migration changed data", err)
	}
}
func TestMySQLCatalogNodePreflightValidatesPhysicalSchema(t *testing.T) {
	for _, tc := range []struct{ name, ddl string }{
		{"missing_node_sort", "ALTER TABLE catalog_nodes DROP CHECK catalog_nodes_sort"},
		{"weaker_node_sort", "ALTER TABLE catalog_nodes DROP CHECK catalog_nodes_sort, ADD CONSTRAINT catalog_nodes_sort CHECK(sort_order >= 0 OR sort_order <= 1000000)"},
		{"wrong_primary_unique_id_lookalike", "ALTER TABLE catalog_nodes DROP PRIMARY KEY, ADD PRIMARY KEY(region_id)"},
		{"missing_node_enabled", "ALTER TABLE catalog_nodes DROP CHECK catalog_nodes_enabled"},
		{"missing_node_parent_fk", "ALTER TABLE catalog_nodes DROP FOREIGN KEY catalog_nodes_region"},
		{"missing_plan_node_fk", "ALTER TABLE plans DROP FOREIGN KEY plans_catalog_node"},
		{"nullable_plan_node", "ALTER TABLE plans MODIFY COLUMN node_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL"},
		{"wrong_node_label_width", "ALTER TABLE catalog_nodes MODIFY COLUMN name VARCHAR(119) NOT NULL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			if err := m.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			if tc.name == "wrong_primary_unique_id_lookalike" {
				migrationExec(t, ctx, m.DB, "ALTER TABLE catalog_nodes ADD UNIQUE KEY node_id_lookalike(id)")
			}
			migrationExec(t, ctx, m.DB, tc.ddl)
			status, err := m.SchemaStatus(ctx)
			if err == nil || status.Status != "incompatible" || status.SchemaVersion != len(schemaVersions) {
				t.Fatal("physical node schema damage hidden by history row", status, err)
			}
		})
	}
}
func TestCatalogNodeMigrationSourceContract(t *testing.T) {
	if len(schemaVersions) < 12 {
		t.Fatal("node migration not registered")
	}
	source := strings.ToLower(schemaVersions[11])
	for _, table := range []string{"orders", "invoices", "services", "plan_prices", "order_requests", "catalog_taxonomy_settings", "catalog_types", "catalog_regions"} {
		for _, verb := range []string{"update ", "delete from ", "alter table "} {
			if strings.Contains(source, verb+table) {
				t.Fatal("node migration unexpectedly changes old business tables/revisions", verb+table)
			}
		}
	}
}

func TestMySQLCatalogNodeMigrationRejectsCollisionsBeforeAnyNewDDL(t *testing.T) {
	if len(schemaVersions) < 12 {
		t.Fatal("node migration not registered")
	}
	create := migrationStatements(schemaVersions[11])[0]
	cases := []struct{ name, ddl string }{
		{"wrong_primary_with_unique_id_lookalike", strings.Replace(strings.Replace(create, "NOT NULL PRIMARY KEY,", "NOT NULL,", 1), "CONSTRAINT catalog_nodes_region", "PRIMARY KEY(region_id), UNIQUE KEY node_id_lookalike(id), CONSTRAINT catalog_nodes_region", 1)},
		{"missing_node_parent_fk", strings.Replace(create, " CONSTRAINT catalog_nodes_region FOREIGN KEY(region_id) REFERENCES catalog_regions(id),\n", "", 1)},
		{"weaker_node_sort", strings.Replace(create, "sort_order >= 0 AND sort_order <= 1000000", "sort_order >= 0 OR sort_order <= 1000000", 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			migrationHistoricalPrefix(t, ctx, m, 11)
			migrationExec(t, ctx, m.DB, tc.ddl)
			history := migrationHistory(t, ctx, m)
			var name, beforeDDL string
			if err := m.DB.QueryRowContext(ctx, "SHOW CREATE TABLE catalog_nodes").Scan(&name, &beforeDDL); err != nil {
				t.Fatal(err)
			}
			if err := m.Migrate(ctx); err == nil {
				t.Fatal("incompatible node table accepted as resumable migration")
			}
			var count int
			if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='plans' AND COLUMN_NAME='node_id'").Scan(&count); err != nil || count != 0 {
				t.Fatal("collision caused partial new plan-column DDL", count, err)
			}
			var afterDDL string
			if err := m.DB.QueryRowContext(ctx, "SHOW CREATE TABLE catalog_nodes").Scan(&name, &afterDDL); err != nil || beforeDDL != afterDDL || !reflect.DeepEqual(history, migrationHistory(t, ctx, m)) {
				t.Fatal("refused collision changed schema or migration history", err)
			}
		})
	}
}
