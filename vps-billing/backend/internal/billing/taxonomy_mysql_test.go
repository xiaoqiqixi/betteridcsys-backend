package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func taxonomyMigrationBusinessRows(t *testing.T, ctx context.Context, m *MySQL) map[string][]string {
	t.Helper()
	// Explicit pre-v11 columns prove the new classification migration does not
	// relabel history, modify independent quotations or touch replay fingerprints.
	queries := map[string]string{
		"plans":          "SELECT JSON_ARRAY(id,name,description,cpu,memory_gb,disk_gb,transfer_tb,region,price_minor,currency,available) FROM plans ORDER BY BINARY id",
		"plan_prices":    "SELECT JSON_ARRAY(plan_id,currency,amount_minor,available) FROM plan_prices ORDER BY BINARY plan_id,BINARY currency",
		"orders":         "SELECT JSON_ARRAY(id,number,user_id,plan_id,plan_name,hostname,status,total_minor,currency,created_at,issuer_snapshot,currency_snapshot,plan_snapshot) FROM orders ORDER BY id",
		"invoices":       "SELECT JSON_ARRAY(id,number,user_id,order_id,status,amount_minor,currency,issued_at,due_at,paid_at,issuer_snapshot,currency_snapshot) FROM invoices ORDER BY id",
		"services":       "SELECT JSON_ARRAY(id,user_id,name,plan_name,region,ipv4,status,next_due_at,price_minor,currency,cpu,memory_gb,disk_gb,currency_snapshot) FROM services ORDER BY id",
		"order_requests": "SELECT JSON_ARRAY(user_id,request_key,fingerprint,order_id,created_at) FROM order_requests ORDER BY user_id,request_key",
		"currencies":     "SELECT JSON_ARRAY(id,document,prices_revision) FROM currency_settings ORDER BY id",
		"site_settings":  "SELECT JSON_ARRAY(id,document) FROM site_settings ORDER BY id",
		"users":          "SELECT JSON_ARRAY(id,email,name,role,password_hash,disabled,revision,created_at) FROM users ORDER BY id",
	}
	out := map[string][]string{}
	for name, query := range queries {
		rows, err := m.DB.QueryContext(ctx, query)
		if err != nil {
			t.Fatal(name, err)
		}
		out[name] = []string{}
		for rows.Next() {
			var raw string
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				t.Fatal(name, err)
			}
			out[name] = append(out[name], raw)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(name, err)
		}
	}
	return out
}
func taxonomyLegacyV10Fixture(t *testing.T, ctx context.Context, m *MySQL) map[string]string {
	t.Helper()
	migrationHistoricalPrefix(t, ctx, m, 10)
	currencies := DefaultCurrencyConfiguration()
	currencies.Revision = 17
	currencies.Currencies = append(currencies.Currencies, Currency{CurrencyFormat: DefaultCurrencyFormat("USD"), Enabled: true, PrecisionLocked: true})
	document, err := json.Marshal(currencies)
	if err != nil {
		t.Fatal(err)
	}
	migrationExec(t, ctx, m.DB, "INSERT INTO currency_settings(id,document,prices_revision) VALUES(1,?,29)", document)
	regions := map[string]string{
		"legacy-hkg":      "香港 · HKG",
		"legacy-spaces":   " 香港 · HKG ",
		"legacy-upper":    "Tokyo",
		"legacy-lower":    "tokyo",
		"legacy-trailing": "Tokyo ",
		"legacy-unicode":  strings.Repeat("地区", 60),
	}
	for id, region := range regions {
		migrationExec(t, ctx, m.DB, "INSERT INTO plans(id,name,description,cpu,memory_gb,disk_gb,transfer_tb,region,price_minor,currency,available) VALUES(?,?,'Historical operator product',2,4,80,2,?,600,'USD',TRUE)", id, id, region)
		migrationExec(t, ctx, m.DB, "INSERT INTO plan_prices(plan_id,currency,amount_minor,available) VALUES(?,'CNY',3900,TRUE),(?,'USD',600,TRUE)", id, id)
	}
	userID := ID()
	migrationExec(t, ctx, m.DB, "INSERT INTO users(id,email,name,role,password_hash,disabled,revision) VALUES(?,'taxonomy-history@fixture.invalid','Historical customer','customer','unchanged-history-password',FALSE,7)", userID)
	currencySnapshot := `{"code":"CNY","name":"人民币","prefix":"¥","suffix":"","decimal_places":2}`
	planSnapshot := `{"plan_id":"legacy-hkg","name":"Frozen historical plan","cpu":1,"memory_gb":2,"disk_gb":40,"transfer_tb":1,"region":"Frozen historical region","billing_cycle":"monthly"}`
	issuer := `{"name":"Frozen historical issuer","address":"Historical address","support_email":"history@fixture.invalid"}`
	orderID := ID()
	migrationExec(t, ctx, m.DB, "INSERT INTO orders(id,number,user_id,plan_id,plan_name,hostname,status,total_minor,currency,created_at,issuer_snapshot,currency_snapshot,plan_snapshot) VALUES(?,'ORD-TAXONOMY-HISTORY',?,'legacy-hkg','Frozen historical plan','frozen-host','pending_payment',3900,'CNY','2025-01-02 03:04:05',?,?,?)", orderID, userID, issuer, currencySnapshot, planSnapshot)
	migrationExec(t, ctx, m.DB, "INSERT INTO invoices(id,number,user_id,order_id,status,amount_minor,currency,issued_at,due_at,issuer_snapshot,currency_snapshot) VALUES(?,'INV-TAXONOMY-HISTORY',?,?,'unpaid',3900,'CNY','2025-01-02 03:04:05','2025-01-09 03:04:05',?,?)", ID(), userID, orderID, issuer, currencySnapshot)
	migrationExec(t, ctx, m.DB, "INSERT INTO services(id,user_id,name,plan_name,region,ipv4,status,next_due_at,price_minor,currency,cpu,memory_gb,disk_gb,currency_snapshot) VALUES(?,?,'frozen-host','Frozen historical plan','Frozen historical region','198.51.100.22','active','2025-02-02 03:04:05',3900,'CNY',1,2,40,?)", ID(), userID, currencySnapshot)
	migrationExec(t, ctx, m.DB, "INSERT INTO order_requests(user_id,request_key,fingerprint,order_id,created_at) VALUES(?,'original-replay-key',?,?,'2025-01-02 03:04:05')", userID, orderFingerprint("legacy-hkg", "frozen-host", "CNY"), orderID)
	return regions
}
func TestMySQLCatalogTaxonomyMigrationPreservesV10HistoryAndPrices(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	regions := taxonomyLegacyV10Fixture(t, ctx, m)
	historyBefore := migrationHistory(t, ctx, m)
	businessBefore := taxonomyMigrationBusinessRows(t, ctx, m)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal("upgrade actual v10 schema", err)
	}
	if after := taxonomyMigrationBusinessRows(t, ctx, m); !reflect.DeepEqual(businessBefore, after) {
		for table, before := range businessBefore {
			if !reflect.DeepEqual(before, after[table]) {
				t.Errorf("migration rewrote %s\nbefore=%v\nafter=%v", table, before, after[table])
			}
		}
	}
	taxonomy, err := m.CatalogTaxonomy(ctx, true)
	if err != nil || taxonomy.Revision != 1 || len(taxonomy.Types) != 1 || taxonomy.Types[0].ID != "generic" || taxonomy.Types[0].Name != "通用服务器" || !taxonomy.Types[0].Enabled {
		t.Fatal("migration invented unsupported product types", taxonomy, err)
	}
	// Original v1 example rows all have one additional literal region.
	if len(taxonomy.Regions) != len(regions)+1 {
		t.Fatal("case/space-distinct historical regions were merged", taxonomy.Regions)
	}
	for id, region := range regions {
		sum := sha256.Sum256([]byte(region))
		expectedID := "r-" + hex.EncodeToString(sum[:])[:60]
		var typeID, regionID, storedName string
		if err := m.DB.QueryRowContext(ctx, "SELECT type_id,region_id,region FROM plans WHERE id=?", id).Scan(&typeID, &regionID, &storedName); err != nil || typeID != "generic" || regionID != expectedID || storedName != region {
			t.Fatal("legacy plan lost exact region binding", id, typeID, regionID, storedName, err)
		}
		var label, parent string
		if err := m.DB.QueryRowContext(ctx, "SELECT name,type_id FROM catalog_regions WHERE id=?", expectedID).Scan(&label, &parent); err != nil || label != region || parent != "generic" {
			t.Fatal("migrated label altered exact text", label, parent, err)
		}
	}
	historyAfter := migrationHistory(t, ctx, m)
	if len(historyAfter) != len(schemaVersions) || !reflect.DeepEqual(historyBefore, historyAfter[:10]) {
		t.Fatal("upgrade changed applied historical migration checksums")
	}
	if err = m.Ping(ctx); err != nil {
		t.Fatal("upgraded migration readiness", err)
	}
	if err = m.Migrate(ctx); err != nil {
		t.Fatal("repeat taxonomy migration", err)
	}
	repeated, err := m.CatalogTaxonomy(ctx, true)
	if err != nil || !reflect.DeepEqual(taxonomy, repeated) || !reflect.DeepEqual(historyAfter, migrationHistory(t, ctx, m)) || !reflect.DeepEqual(businessBefore, taxonomyMigrationBusinessRows(t, ctx, m)) {
		t.Fatal("repeat taxonomy migration changed registry/history/quotations", err)
	}
}
func TestMySQLCatalogTaxonomyAuditFailureRollsBackAllMutations(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	ctx := t.Context()
	taxonomyAddFixture(t, f)
	input := taxonomyPlanInput(t, f, false)
	created, err := f.catalog.CreateCatalogPlan(ctx, f.admin, input)
	if err != nil {
		t.Fatal(err)
	}
	before := testReadTaxonomy(t, f)
	catalogBefore := readCatalog(t, f)
	currenciesBefore, err := f.store.Currencies(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	auditsBefore, err := f.store.Audits(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	update := taxonomyUpdateFrom(before)
	taxonomyChangeRegion(&update, "compute-hkg", func(v *CatalogRegion) { v.Name = "Edited label must roll back" })
	update.Types = append(update.Types, CatalogType{ID: "rollback-only", Name: "Not committed", Enabled: true})
	migrationExec(t, ctx, m.DB, "CREATE TRIGGER taxonomy_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='fixture failure'")
	t.Cleanup(func() { migrationExec(t, context.Background(), m.DB, "DROP TRIGGER IF EXISTS taxonomy_reject_audit") })
	if _, err = m.UpdateCatalogTaxonomy(ctx, f.admin, update); err == nil {
		t.Fatal("late audit failure accepted")
	}
	after := testReadTaxonomy(t, f)
	catalogAfter := readCatalog(t, f)
	currenciesAfter, _ := f.store.Currencies(ctx, true)
	auditsAfter, _ := f.store.Audits(ctx, "")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(catalogBefore, catalogAfter) || !reflect.DeepEqual(currenciesBefore, currenciesAfter) || !reflect.DeepEqual(auditsBefore, auditsAfter) {
		t.Fatal("audit failure partially committed registry, current label, price revision or currencies")
	}
	if catalogFind(t, catalogAfter, created.Plan.ID).Region != "香港" {
		t.Fatal("rolled back label escaped into product")
	}
}
func TestCatalogTaxonomyMigrationSourceContract(t *testing.T) {
	if len(schemaVersions) < 11 {
		t.Fatal("taxonomy migration not registered")
	}
	source := strings.ToLower(schemaVersions[10])
	for _, table := range []string{"orders", "invoices", "services", "plan_prices", "order_requests"} {
		for _, verb := range []string{"update ", "delete from ", "insert into ", "alter table "} {
			if strings.Contains(source, verb+table) {
				t.Fatal("taxonomy DDL unexpectedly rewrites financial or historical data", fmt.Sprintf("%s%s", verb, table))
			}
		}
	}
}

func TestMySQLCatalogTaxonomyPreflightValidatesActualPhysicalSchema(t *testing.T) {
	cases := []struct{ name, ddl string }{
		{"current", ""},
		{"missing_sort_bound", "ALTER TABLE catalog_regions DROP CHECK catalog_regions_sort"},
		{"weakened_sort_disjunction", "ALTER TABLE catalog_regions DROP CHECK catalog_regions_sort, ADD CONSTRAINT catalog_regions_sort CHECK(sort_order >= 0 OR sort_order <= 1000000)"},
		{"missing_revision_cap", "ALTER TABLE catalog_taxonomy_settings DROP CHECK catalog_taxonomy_revision, ADD CONSTRAINT catalog_taxonomy_revision CHECK(revision >= 1)"},
		{"missing_product_region_fk", "ALTER TABLE plans DROP FOREIGN KEY plans_catalog_region"},
		{"wrong_region_label_width", "ALTER TABLE catalog_regions MODIFY COLUMN name VARCHAR(119) NOT NULL"},
		{"nullable_product_type", "ALTER TABLE plans MODIFY COLUMN type_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			if err := m.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			status, err := m.SchemaStatus(ctx)
			if err != nil || status.Status != "current" || status.SchemaVersion != len(schemaVersions) || status.RequiredSchemaVersion != len(schemaVersions) || len(status.MigrationChecksums) != len(schemaVersions) || len(status.PendingMigrations) != 0 {
				t.Fatal("fresh current schema must be usable", status, err)
			}
			if test.ddl == "" {
				if err = m.Migrate(ctx); err != nil {
					t.Fatal("repeat", err)
				}
				status, err = m.SchemaStatus(ctx)
				if err != nil || status.Status != "current" {
					t.Fatal("repeat current schema unusable", status, err)
				}
				return
			}
			migrationExec(t, ctx, m.DB, test.ddl)
			status, err = m.SchemaStatus(ctx)
			if err == nil || status.Status != "incompatible" || status.SchemaVersion != len(schemaVersions) {
				t.Fatal("unsafe taxonomy physical schema accepted", status, err)
			}
		})
	}
}
