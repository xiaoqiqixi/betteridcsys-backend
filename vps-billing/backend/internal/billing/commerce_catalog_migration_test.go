package billing

import (
	"aster/billing/migrations"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestMySQLCommerceCatalogMigrationResumesAndPreservesPlans(t *testing.T) {
	before := 0
	for i, source := range schemaVersions {
		if source == migrations.CommerceCatalog {
			before = i
			break
		}
	}
	if before == 0 {
		t.Fatal("catalog migration missing")
	}
	for _, cut := range []int{0, 3, 9} {
		t.Run(fmt.Sprint(cut), func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			migrationHistoricalPrefix(t, ctx, m, before)
			// The immutable SQL prefix creates the registry table; the historical
			// Go migration also wrote its document. Reproduce that metadata here.
			registry, err := json.Marshal(DefaultCurrencyConfiguration())
			if err != nil {
				t.Fatal(err)
			}
			migrationExec(t, ctx, m.DB, "INSERT INTO currency_settings(id,document,prices_revision) VALUES(1,?,1)", registry)
			migrationExec(t, ctx, m.DB, "UPDATE plans SET available=TRUE,memory_gb=2 WHERE id='starter'")
			migrationExec(t, ctx, m.DB, "UPDATE plan_prices SET available=TRUE WHERE plan_id='starter' AND currency='CNY'")
			statements := migrationStatements(migrations.CommerceCatalog)
			for _, statement := range statements[:cut] {
				migrationExec(t, ctx, m.DB, statement)
			}
			if err := m.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			if err := m.Migrate(ctx); err != nil {
				t.Fatal("repeat", err)
			}
			if err := m.Ping(ctx); err != nil {
				t.Fatal(err)
			}
			p, err := m.Product06(ctx, true, "starter")
			if err != nil {
				t.Fatal(err)
			}
			if p.ID != "starter" || p.Visibility != "public" || p.Revision != 1 || p.Specs.MemoryMB != 2048 {
				t.Fatal(p)
			}
			var planCount, services int
			if err = m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM plans").Scan(&planCount); err != nil || planCount != 3 {
				t.Fatal("legacy plans", planCount, err)
			}
			if err = m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM services").Scan(&services); err != nil || services != 0 {
				t.Fatal("invented historical services", services, err)
			}
		})
	}
}

func TestMySQLCommerceCatalogRejectsCollidingSchemaBeforeDDL(t *testing.T) {
	before := 0
	for i, source := range schemaVersions {
		if source == migrations.CommerceCatalog {
			before = i
			break
		}
	}
	m, ctx := migrationTestDatabase(t)
	migrationHistoricalPrefix(t, ctx, m, before)
	migrationExec(t, ctx, m.DB, "CREATE TABLE os_images(id INT PRIMARY KEY) ENGINE=InnoDB")
	if err := m.Migrate(ctx); err == nil {
		t.Fatal("incompatible collision accepted")
	}
	var n int
	if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='products'").Scan(&n); err != nil || n != 0 {
		t.Fatal("DDL before validation", n, err)
	}
	migrationExec(t, ctx, m.DB, "DROP TABLE os_images")
	for _, q := range migrationStatements(migrations.CommerceCatalog) {
		if strings.HasPrefix(q, "CREATE TABLE IF NOT EXISTS products (") {
			migrationExec(t, ctx, m.DB, q)
			break
		}
	}
	migrationExec(t, ctx, m.DB, "ALTER TABLE products DROP CHECK product_stock_max")
	if err := m.Migrate(ctx); err == nil {
		t.Fatal("missing stock constraint accepted")
	}
}
