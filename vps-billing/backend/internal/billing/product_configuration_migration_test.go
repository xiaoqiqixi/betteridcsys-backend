package billing

import (
	"reflect"
	"strings"
	"testing"
)

func TestProductConfigurationMigrationIsAdditiveAndPrivate(t *testing.T) {
	if len(schemaVersions) != 13 {
		t.Fatal("schema13 not registered", len(schemaVersions))
	}
	statements := migrationStatements(schemaVersions[12])
	if len(statements) != 3 {
		t.Fatal("expected exactly three additive creates", len(statements))
	}
	for n, table := range []string{"product_configurations", "order_product_configurations", "provisioning_jobs"} {
		if !strings.HasPrefix(statements[n], "CREATE TABLE IF NOT EXISTS "+table+" (") {
			t.Fatal("migration changed old history", statements[n])
		}
	}
	if len(preflightColumns(13)) != 23 || len(preflightColumns(12)) != 20 {
		t.Fatal("schema inventory drift")
	}
}
func TestMySQLProductConfigurationMigrationPreservesHistoryWithoutBackfill(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	taxonomyLegacyV10Fixture(t, ctx, m)
	for version := 11; version <= 12; version++ {
		for _, q := range migrationStatements(schemaVersions[version-1]) {
			migrationExec(t, ctx, m.DB, q)
		}
		migrationExec(t, ctx, m.DB, "INSERT INTO schema_migrations(version,checksum) VALUES(?,?)", version, digest(schemaVersions[version-1]))
	}
	before := taxonomyMigrationBusinessRows(t, ctx, m)
	registry := nodeMigrationLegacyRegistryRows(t, ctx, m)
	history := migrationHistory(t, ctx, m)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, taxonomyMigrationBusinessRows(t, ctx, m)) || !reflect.DeepEqual(registry, nodeMigrationLegacyRegistryRows(t, ctx, m)) || !reflect.DeepEqual(history, migrationHistory(t, ctx, m)[:12]) {
		t.Fatal("schema13 changed money, snapshots, idempotency or catalog revisions")
	}
	for _, table := range []string{"product_configurations", "order_product_configurations", "provisioning_jobs"} {
		var n int
		if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil || n != 0 {
			t.Fatal("migration fabricated private configuration for old data", table, n, err)
		}
	}
	if err := m.Migrate(ctx); err != nil {
		t.Fatal("repeat additive migration", err)
	}
	status, err := m.SchemaStatus(ctx)
	if err != nil || status.Status != "current" || status.SchemaVersion != 13 {
		t.Fatal(status, err)
	}
}
func TestMySQLProductConfigurationMigrationRejectsCollisionBeforeAnyNewDDL(t *testing.T) {
	create := migrationStatements(schemaVersions[12])[0]
	for _, tc := range []struct{ name, ddl string }{
		{"hash_collation", strings.Replace(create, "CHARACTER SET ascii COLLATE ascii_bin", "CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci", 1)},
		{"weak_revision", strings.Replace(create, "revision >= 1 AND revision <= 9007199254740991", "revision >= 0 AND revision <= 9007199254740991", 1)},
		{"missing_plan_fk", strings.Replace(create, " CONSTRAINT product_configuration_plan FOREIGN KEY(plan_id) REFERENCES plans(id),\n", "", 1)},
		{"nullable_spec", strings.Replace(create, "spec JSON NOT NULL", "spec JSON NULL", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			migrationHistoricalPrefix(t, ctx, m, 12)
			migrationExec(t, ctx, m.DB, tc.ddl)
			before := migrationHistory(t, ctx, m)
			if err := m.Migrate(ctx); err == nil {
				t.Fatal("colliding schema accepted")
			}
			var n int
			err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME IN ('order_product_configurations','provisioning_jobs')").Scan(&n)
			if err != nil || n != 0 || !reflect.DeepEqual(before, migrationHistory(t, ctx, m)) {
				t.Fatal("bad collision caused partial DDL/history", n, err)
			}
		})
	}
	// Validate the second colliding object before creating the first new one.
	t.Run("second_object_view", func(t *testing.T) {
		m, ctx := migrationTestDatabase(t)
		migrationHistoricalPrefix(t, ctx, m, 12)
		migrationExec(t, ctx, m.DB, "CREATE VIEW order_product_configurations AS SELECT id AS order_id FROM orders")
		if err := m.Migrate(ctx); err == nil {
			t.Fatal("view accepted as table")
		}
		var n int
		if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='product_configurations'").Scan(&n); err != nil || n != 0 {
			t.Fatal("did not prevalidate all colliding creates", n, err)
		}
	})
}
func TestMySQLProductConfigurationPreflightValidatesPhysicalSchema(t *testing.T) {
	for _, tc := range []struct{ name, ddl string }{
		{"weaker_attempts", "ALTER TABLE provisioning_jobs DROP CHECK provisioning_job_attempts, ADD CONSTRAINT provisioning_job_attempts CHECK(attempts >= 0 AND attempts <= 2)"},
		{"wrong_hash_collation", "ALTER TABLE provisioning_jobs MODIFY request_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_general_ci NOT NULL"},
		{"missing_snapshot_fk", "ALTER TABLE provisioning_jobs DROP FOREIGN KEY provisioning_job_snapshot"},
		{"missing_order_fk", "ALTER TABLE order_product_configurations DROP FOREIGN KEY order_product_configuration_order"},
		{"nullable_spec", "ALTER TABLE product_configurations MODIFY spec JSON NULL"},
		{"default_attempts", "ALTER TABLE provisioning_jobs ALTER attempts SET DEFAULT 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			if err := m.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			migrationExec(t, ctx, m.DB, tc.ddl)
			status, err := m.SchemaStatus(ctx)
			if err == nil || status.Status != "incompatible" {
				t.Fatal("schema damage hidden by version13 history", status, err)
			}
		})
	}
}

func TestProductConfigurationSchema13ExactCheckNormalization(t *testing.T) {
	for _, tc := range []struct{ table, name, column, lower, upper string }{
		{"product_configurations", "product_configuration_revision", "revision", "1", "9007199254740991"},
		{"order_product_configurations", "order_product_configuration_revision", "configuration_revision", "1", "9007199254740991"},
		{"provisioning_jobs", "provisioning_job_attempts", "attempts", "0", "1"},
	} {
		t.Run(tc.table, func(t *testing.T) {
			expected := tc.column + " >= " + tc.lower + " AND " + tc.column + " <= " + tc.upper
			rendered := "((`" + tc.column + "` >= " + tc.lower + ") and (`" + tc.column + "` <= " + tc.upper + "))"
			for _, valid := range []string{expected, rendered} {
				if !productConfigurationMigrationCheckMatches(tc.table, tc.name, currencyMigrationCheck{"CHECK", "YES", valid}, expected) {
					t.Fatal("exact shipped MySQL CHECK refused", valid)
				}
			}
			for _, invalid := range []string{
				strings.Replace(rendered, " and ", " or ", 1),
				strings.Replace(rendered, ">= "+tc.lower, ">= 2", 1),
				strings.Replace(rendered, "<= "+tc.upper, "<= 9007199254740992", 1),
				rendered + " OR 1=1", rendered + " AND " + tc.column + " >= " + tc.lower,
			} {
				if productConfigurationMigrationCheckMatches(tc.table, tc.name, currencyMigrationCheck{"CHECK", "YES", invalid}, expected) {
					t.Fatal("weaker/extra check accepted", invalid)
				}
			}
			if productConfigurationMigrationCheckMatches(tc.table, tc.name, currencyMigrationCheck{"CHECK", "NO", rendered}, expected) || productConfigurationMigrationCheckMatches("other", tc.name, currencyMigrationCheck{"CHECK", "YES", rendered}, expected) || productConfigurationMigrationCheckMatches(tc.table, "other", currencyMigrationCheck{"CHECK", "YES", rendered}, expected) {
				t.Fatal("wrong constraint identity/enforcement accepted")
			}
		})
	}
}
