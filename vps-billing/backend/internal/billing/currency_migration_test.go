package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The probes deliberately decode persisted JSON rather than calling the runtime
// registry accessor: a migration must leave independently readable storage.
type migrationCurrencyFormat struct {
	Code          string `json:"code"`
	Name          string `json:"name"`
	Prefix        string `json:"prefix"`
	Suffix        string `json:"suffix"`
	DecimalPlaces int    `json:"decimal_places"`
}

type migrationCurrencyEntry struct {
	migrationCurrencyFormat
	Enabled         bool `json:"enabled"`
	SortOrder       int  `json:"sort_order"`
	PrecisionLocked bool `json:"precision_locked"`
}

type migrationCurrencyConfiguration struct {
	Revision        int64                    `json:"revision"`
	DefaultCurrency string                   `json:"default_currency"`
	Currencies      []migrationCurrencyEntry `json:"currencies"`
}

func readMigrationCurrencyConfiguration(t *testing.T, ctx context.Context, m *MySQL) (migrationCurrencyConfiguration, int64) {
	t.Helper()
	var raw []byte
	var pricesRevision int64
	if err := m.DB.QueryRowContext(ctx, "SELECT document,prices_revision FROM currency_settings WHERE id=1").Scan(&raw, &pricesRevision); err != nil {
		t.Fatal("read persisted currency registry", err)
	}
	var config migrationCurrencyConfiguration
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal("decode persisted currency registry", err)
	}
	return config, pricesRevision
}

func legacyCurrencyMigrationFixture(t *testing.T, ctx context.Context, m *MySQL) string {
	t.Helper()
	migrationHistoricalPrefix(t, ctx, m, 6)
	// Freeze the actual pre-registry JSON shape instead of manufacturing a
	// historical database from whatever DefaultSettings returns in the future.
	settings := `{"revision":13,"default_locale":"en","default_currency":"USD","site_name":"Historical operator","company_name":"Historical company","logo":"cloud","theme":"teal","support_email":"support@historical.invalid","support_url":"","display_url":"","email_signature":"Historical signature","invoice_issuer":"Historical issuer","invoice_address":"Historical address"}`
	migrationExec(t, ctx, m.DB, "UPDATE site_settings SET document=? WHERE id=1", settings)
	userID := ID()
	migrationExec(t, ctx, m.DB, "INSERT INTO users(id,email,name,role,password_hash) VALUES(?,'currency-history@integration.invalid','Historical customer','customer','unused')", userID)
	migrationExec(t, ctx, m.DB, "INSERT INTO plans(id,name,description,cpu,memory_gb,disk_gb,transfer_tb,region,price_minor,currency,available) VALUES('registry-legacy','Historical plan','untouched',2,4,60,2,'Historical region',1777,'USD',TRUE)")
	issuer := `{"name":"Original invoice issuer","address":"Original address","support_email":"original@historical.invalid"}`
	for _, value := range []struct {
		code   string
		amount int64
	}{{"CNY", 2999}, {"USD", 1777}} {
		orderID := ID()
		migrationExec(t, ctx, m.DB, "INSERT INTO plan_prices(plan_id,currency,amount_minor,available) VALUES('registry-legacy',?,?,TRUE)", value.code, value.amount)
		migrationExec(t, ctx, m.DB, "INSERT INTO orders(id,number,user_id,plan_id,plan_name,hostname,status,total_minor,currency,created_at,issuer_snapshot) VALUES(?,?,?,'registry-legacy','Original plan name','historical-host','pending_payment',?,?,'2025-01-02 03:04:05',?)", orderID, "ORD-REGISTRY-"+value.code, userID, value.amount, value.code, issuer)
		migrationExec(t, ctx, m.DB, "INSERT INTO invoices(id,number,user_id,order_id,status,amount_minor,currency,issued_at,due_at,issuer_snapshot) VALUES(?,?,?,?,'unpaid',?,?,'2025-01-02 03:04:05','2025-01-09 03:04:05',?)", ID(), "INV-REGISTRY-"+value.code, userID, orderID, value.amount, value.code, issuer)
		migrationExec(t, ctx, m.DB, "INSERT INTO services(id,user_id,name,plan_name,region,status,next_due_at,price_minor,currency,cpu,memory_gb,disk_gb) VALUES(?,?,'Historical service','Original plan name','Historical region','active','2025-02-02 03:04:05',?,?,2,4,60)", ID(), userID, value.amount, value.code)
	}
	return userID
}

func currencyMigrationSchema(t *testing.T, ctx context.Context, m *MySQL) map[string]string {
	t.Helper()
	definitions := make(map[string]string)
	for _, table := range []string{"plans", "plan_prices", "orders", "invoices", "services", "currency_settings"} {
		var exists int
		if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists == 0 {
			definitions[table] = ""
			continue
		}
		var name, definition string
		if err := m.DB.QueryRowContext(ctx, "SHOW CREATE TABLE `"+table+"`").Scan(&name, &definition); err != nil {
			t.Fatal(err)
		}
		definitions[table] = definition
	}
	return definitions
}

func assertCurrencyMigrationRefusedWithoutDDL(t *testing.T, ctx context.Context, m *MySQL) {
	t.Helper()
	beforeHistory := migrationHistory(t, ctx, m)
	beforeSchema := currencyMigrationSchema(t, ctx, m)
	if err := m.Migrate(ctx); err == nil {
		t.Fatal("migration accepted incompatible legacy currency data or schema")
	}
	if after := migrationHistory(t, ctx, m); !reflect.DeepEqual(beforeHistory, after) {
		t.Errorf("failed migration changed history: before=%v after=%v", beforeHistory, after)
	}
	if after := currencyMigrationSchema(t, ctx, m); !reflect.DeepEqual(beforeSchema, after) {
		t.Errorf("preflight failure changed schema: before=%v after=%v", beforeSchema, after)
	}
	if err := m.Ping(ctx); err == nil {
		t.Error("readiness accepted incomplete currency migration")
	}
}

func TestMySQLCurrencyRegistryMigrationFresh(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	config, pricesRevision := readMigrationCurrencyConfiguration(t, ctx, m)
	if config.Revision != 1 || pricesRevision != 1 || config.DefaultCurrency != "CNY" || len(config.Currencies) != 1 {
		t.Fatalf("fresh currency defaults: config=%+v prices_revision=%d", config, pricesRevision)
	}
	currency := config.Currencies[0]
	if currency.migrationCurrencyFormat != migrationCurrencyFormat(DefaultCurrencyFormat("CNY")) || !currency.Enabled || !currency.PrecisionLocked {
		t.Fatalf("fresh CNY metadata: %+v", currency)
	}
	var usdPrices, sellable int
	if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM plan_prices WHERE currency='USD'").Scan(&usdPrices); err != nil {
		t.Fatal(err)
	}
	if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM plan_prices WHERE available=TRUE").Scan(&sellable); err != nil {
		t.Fatal(err)
	}
	if usdPrices != 0 || sellable != 0 {
		t.Fatalf("fresh migration invented USD pricing or enabled sales: USD prices=%d sellable=%d", usdPrices, sellable)
	}
	for _, table := range []string{"orders", "invoices", "services"} {
		var dataType, nullable string
		if err := m.DB.QueryRowContext(ctx, "SELECT DATA_TYPE,IS_NULLABLE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME='currency_snapshot'", table).Scan(&dataType, &nullable); err != nil {
			t.Fatal(table, err)
		}
		if dataType != "json" || nullable != "YES" {
			t.Fatalf("%s snapshot column=%s nullable=%s", table, dataType, nullable)
		}
	}
	if err := m.Ping(ctx); err != nil {
		t.Fatal("fresh registry readiness", err)
	}
}

func TestMySQLCurrencyRegistryMigrationPreservesV6(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	userID := legacyCurrencyMigrationFixture(t, ctx, m)
	var beforeSettings string
	if err := m.DB.QueryRowContext(ctx, "SELECT document FROM site_settings WHERE id=1").Scan(&beforeSettings); err != nil {
		t.Fatal(err)
	}
	if err := m.Migrate(ctx); err != nil {
		t.Fatal("upgrade v6 registry", err)
	}
	config, pricesRevision := readMigrationCurrencyConfiguration(t, ctx, m)
	if config.Revision != 1 || config.DefaultCurrency != "USD" || len(config.Currencies) != 2 || pricesRevision != 1 {
		t.Fatalf("legacy registry defaults: config=%+v prices_revision=%d", config, pricesRevision)
	}
	formats := make(map[string]migrationCurrencyFormat)
	for _, entry := range config.Currencies {
		if !entry.Enabled || !entry.PrecisionLocked || (entry.Code != "CNY" && entry.Code != "USD") || entry.migrationCurrencyFormat != migrationCurrencyFormat(DefaultCurrencyFormat(entry.Code)) {
			t.Fatalf("legacy registered currency: %+v", entry)
		}
		formats[entry.Code] = entry.migrationCurrencyFormat
	}
	if len(formats) != 2 {
		t.Fatal("duplicate or missing historical currency", config.Currencies)
	}
	var afterSettings string
	if err := m.DB.QueryRowContext(ctx, "SELECT document FROM site_settings WHERE id=1").Scan(&afterSettings); err != nil || beforeSettings != afterSettings {
		t.Fatal("migration rewrote operator site settings", beforeSettings, afterSettings, err)
	}
	for _, value := range []struct {
		code   string
		amount int64
	}{{"CNY", 2999}, {"USD", 1777}} {
		var price int64
		var available bool
		if err := m.DB.QueryRowContext(ctx, "SELECT amount_minor,available FROM plan_prices WHERE plan_id='registry-legacy' AND currency=?", value.code).Scan(&price, &available); err != nil || price != value.amount || !available {
			t.Fatal("migration rewrote explicit price", value.code, price, available, err)
		}
		for _, table := range []struct{ name, amountColumn string }{{"orders", "total_minor"}, {"invoices", "amount_minor"}, {"services", "price_minor"}} {
			var amount int64
			var code string
			var raw []byte
			query := "SELECT " + table.amountColumn + ",currency,currency_snapshot FROM " + table.name + " WHERE user_id=? AND currency=?"
			if err := m.DB.QueryRowContext(ctx, query, userID, value.code).Scan(&amount, &code, &raw); err != nil {
				t.Fatal(table.name, value.code, err)
			}
			var snapshot migrationCurrencyFormat
			if err := json.Unmarshal(raw, &snapshot); err != nil {
				t.Fatal("missing or invalid backfilled snapshot", table.name, value.code, err)
			}
			if amount != value.amount || code != value.code || snapshot != formats[value.code] {
				t.Fatalf("historical %s changed: amount=%d currency=%s snapshot=%+v", table.name, amount, code, snapshot)
			}
		}
		for _, table := range []string{"orders", "invoices"} {
			var issuer string
			if err := m.DB.QueryRowContext(ctx, "SELECT JSON_UNQUOTE(JSON_EXTRACT(issuer_snapshot,'$.name')) FROM "+table+" WHERE user_id=? AND currency=?", userID, value.code).Scan(&issuer); err != nil || issuer != "Original invoice issuer" {
				t.Fatal("migration rewrote issuer snapshot", table, issuer, err)
			}
		}
	}
	beforeHistory := migrationHistory(t, ctx, m)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal("repeat registry migration", err)
	}
	afterConfig, afterPricesRevision := readMigrationCurrencyConfiguration(t, ctx, m)
	if !reflect.DeepEqual(config, afterConfig) || pricesRevision != afterPricesRevision || !reflect.DeepEqual(beforeHistory, migrationHistory(t, ctx, m)) {
		t.Fatal("repeat migration changed registry or history")
	}
}

func TestMySQLCurrencyRegistryMigrationResumesPartialSnapshot(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	userID := legacyCurrencyMigrationFixture(t, ctx, m)
	migrationExec(t, ctx, m.DB, "ALTER TABLE orders ADD COLUMN currency_snapshot JSON NULL")
	originalSnapshot, err := json.Marshal(DefaultCurrencyFormat("USD"))
	if err != nil {
		t.Fatal(err)
	}
	migrationExec(t, ctx, m.DB, "UPDATE orders SET currency_snapshot=? WHERE user_id=? AND currency='USD'", originalSnapshot, userID)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal("resume partial snapshot migration", err)
	}
	var snapshot migrationCurrencyFormat
	var raw []byte
	if err := m.DB.QueryRowContext(ctx, "SELECT currency_snapshot FROM orders WHERE user_id=? AND currency='USD'", userID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil || snapshot != migrationCurrencyFormat(DefaultCurrencyFormat("USD")) {
		t.Fatal("resume overwrote existing historical snapshot", snapshot, err)
	}
	for _, table := range []string{"orders", "invoices", "services"} {
		var missing int
		if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE currency_snapshot IS NULL").Scan(&missing); err != nil || missing != 0 {
			t.Fatal("resume failed to backfill remaining rows", table, missing, err)
		}
	}
	config, _ := readMigrationCurrencyConfiguration(t, ctx, m)
	// Model interruption after all schema/data work but before the version is
	// recorded. Recovery recognizes exactly the expected initial registry.
	migrationExec(t, ctx, m.DB, "DELETE FROM schema_migrations WHERE version>=7")
	if err := m.Migrate(ctx); err != nil {
		t.Fatal("resume completed but unrecorded migration", err)
	}
	resumed, pricesRevision := readMigrationCurrencyConfiguration(t, ctx, m)
	if !reflect.DeepEqual(config, resumed) || pricesRevision != 1 {
		t.Fatal("resume changed initial registry", config, resumed, pricesRevision)
	}
	config.Revision = 9
	for i := range config.Currencies {
		if config.Currencies[i].Code == "CNY" {
			config.Currencies[i].Name = "Operator CNY label"
			config.Currencies[i].Prefix = "RMB "
		}
	}
	raw, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	migrationExec(t, ctx, m.DB, "UPDATE currency_settings SET document=?,prices_revision=11 WHERE id=1", raw)
	// Once migration 7 is recorded, later operator formatting edits and price
	// revisions must survive an ordinary migration invocation unchanged.
	if err := m.Migrate(ctx); err != nil {
		t.Fatal("repeat after operator registry changes", err)
	}
	resumed, pricesRevision = readMigrationCurrencyConfiguration(t, ctx, m)
	if !reflect.DeepEqual(config, resumed) || pricesRevision != 11 {
		t.Fatal("resume reset operator registry", config, resumed, pricesRevision)
	}
	if err := m.DB.QueryRowContext(ctx, "SELECT currency_snapshot FROM orders WHERE user_id=? AND currency='USD'", userID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var resumedSnapshot migrationCurrencyFormat
	if err := json.Unmarshal(raw, &resumedSnapshot); err != nil || resumedSnapshot != snapshot {
		t.Fatal("resume changed historical formatting", resumedSnapshot, err)
	}
}

func TestMySQLCurrencyRegistryMigrationEveryDDLBoundary(t *testing.T) {
	if len(schemaVersions) < 7 {
		t.Fatal("currency registry migration is not registered")
	}
	statements := migrationStatements(schemaVersions[6])
	for boundary := 0; boundary <= len(statements); boundary++ {
		t.Run(fmt.Sprintf("after_%d_statements", boundary), func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			legacyCurrencyMigrationFixture(t, ctx, m)
			for _, statement := range statements[:boundary] {
				migrationExec(t, ctx, m.DB, statement)
			}
			if err := m.Migrate(ctx); err != nil {
				t.Fatal("resume at currency DDL boundary", err)
			}
			config, pricesRevision := readMigrationCurrencyConfiguration(t, ctx, m)
			if config.DefaultCurrency != "USD" || config.Revision != 1 || pricesRevision != 1 || len(config.Currencies) != 2 {
				t.Fatal("recovery lost historical registry", config, pricesRevision)
			}
			for _, table := range []string{"orders", "invoices", "services"} {
				var missing int
				if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE currency_snapshot IS NULL").Scan(&missing); err != nil || missing != 0 {
					t.Fatal("recovery left missing snapshots", table, missing, err)
				}
			}
			if err := m.Ping(ctx); err != nil {
				t.Fatal("resumed currency migration readiness", err)
			}
			if err := m.Migrate(ctx); err != nil {
				t.Fatal("repeat resumed currency migration", err)
			}
		})
	}
}

func TestMySQLCurrencyRegistryMigrationRejectsUnexpectedPartialData(t *testing.T) {
	for _, test := range []struct{ name, snapshot string }{
		{"wrong_code", `{"code":"CNY","name":"US Dollar","prefix":"$","suffix":"","decimal_places":2}`},
		{"wrong_precision", `{"code":"USD","name":"US Dollar","prefix":"$","suffix":"","decimal_places":3}`},
		{"changed_format", `{"code":"USD","name":"US Dollar","prefix":"US$","suffix":"","decimal_places":2}`},
		{"unknown_field", `{"code":"USD","name":"US Dollar","prefix":"$","suffix":"","decimal_places":2,"unexpected":true}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			userID := legacyCurrencyMigrationFixture(t, ctx, m)
			migrationExec(t, ctx, m.DB, "ALTER TABLE orders ADD COLUMN currency_snapshot JSON NULL")
			migrationExec(t, ctx, m.DB, "UPDATE orders SET currency_snapshot=? WHERE user_id=? AND currency='USD'", test.snapshot, userID)
			assertCurrencyMigrationRefusedWithoutDDL(t, ctx, m)
			var snapshot string
			if err := m.DB.QueryRowContext(ctx, "SELECT currency_snapshot FROM orders WHERE user_id=? AND currency='USD'", userID).Scan(&snapshot); err != nil {
				t.Fatal(err)
			}
			var before, after any
			if err := json.Unmarshal([]byte(test.snapshot), &before); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(snapshot), &after); err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("rejected migration changed unexpected snapshot", snapshot, err)
			}
		})
	}
}

func TestMySQLCurrencyRegistryMigrationRejectsConflictingPartialSchema(t *testing.T) {
	for _, test := range []struct{ name, ddl string }{
		{"order_snapshot_text", "ALTER TABLE orders ADD COLUMN currency_snapshot VARCHAR(100) NULL"},
		{"invoice_snapshot_required", "ALTER TABLE invoices ADD COLUMN currency_snapshot JSON NOT NULL"},
		{"service_snapshot_default", "ALTER TABLE services ADD COLUMN currency_snapshot JSON NULL DEFAULT (JSON_OBJECT())"},
		{"registry_document_text", "CREATE TABLE currency_settings(id TINYINT PRIMARY KEY,document TEXT NOT NULL,prices_revision BIGINT NOT NULL DEFAULT 1,CONSTRAINT currency_settings_singleton CHECK(id=1),CONSTRAINT currency_settings_prices_revision CHECK(prices_revision>=1 AND prices_revision<=9007199254740991)) ENGINE=InnoDB"},
		{"registry_id_integer", "CREATE TABLE currency_settings(id INT PRIMARY KEY,document JSON NOT NULL,prices_revision BIGINT NOT NULL DEFAULT 1,CONSTRAINT currency_settings_singleton CHECK(id=1),CONSTRAINT currency_settings_prices_revision CHECK(prices_revision>=1 AND prices_revision<=9007199254740991)) ENGINE=InnoDB"},
		{"registry_missing_revision_cap", "CREATE TABLE currency_settings(id TINYINT PRIMARY KEY,document JSON NOT NULL,prices_revision BIGINT NOT NULL DEFAULT 1,CONSTRAINT currency_settings_singleton CHECK(id=1),CONSTRAINT currency_settings_prices_revision CHECK(prices_revision>=1)) ENGINE=InnoDB"},
		{"registry_wrong_primary_key", "CREATE TABLE currency_settings(id TINYINT NOT NULL,document JSON NOT NULL,prices_revision BIGINT PRIMARY KEY DEFAULT 1,CONSTRAINT currency_settings_singleton CHECK(id=1),CONSTRAINT currency_settings_prices_revision CHECK(prices_revision>=1 AND prices_revision<=9007199254740991)) ENGINE=InnoDB"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			migrationHistoricalPrefix(t, ctx, m, 6)
			migrationExec(t, ctx, m.DB, test.ddl)
			assertCurrencyMigrationRefusedWithoutDDL(t, ctx, m)
		})
	}
}

func TestMySQLCurrencyRegistryMigrationRejectsUnknownLegacyCodes(t *testing.T) {
	for _, table := range []string{"plans", "plan_prices", "orders", "invoices", "services"} {
		t.Run(table, func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			legacyCurrencyMigrationFixture(t, ctx, m)
			// Deliberately corrupt only this disposable fixture, after removing its
			// original currency checks; the migrator must fail before its own DDL.
			rows, err := m.DB.QueryContext(ctx, "SELECT tc.CONSTRAINT_NAME FROM information_schema.TABLE_CONSTRAINTS tc JOIN information_schema.CHECK_CONSTRAINTS cc ON cc.CONSTRAINT_SCHEMA=tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME=tc.CONSTRAINT_NAME WHERE tc.TABLE_SCHEMA=DATABASE() AND tc.TABLE_NAME=? AND tc.CONSTRAINT_TYPE='CHECK' AND LOWER(cc.CHECK_CLAUSE) LIKE '%currency%'", table)
			if err != nil {
				t.Fatal(err)
			}
			var checks []string
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					_ = rows.Close()
					t.Fatal(err)
				}
				checks = append(checks, name)
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
			for _, name := range checks {
				migrationExec(t, ctx, m.DB, "ALTER TABLE `"+table+"` DROP CHECK `"+strings.ReplaceAll(name, "`", "``")+"`")
			}
			migrationExec(t, ctx, m.DB, "UPDATE `"+table+"` SET currency='EUR' WHERE currency='USD'")
			assertCurrencyMigrationRefusedWithoutDDL(t, ctx, m)
		})
	}
}

func TestMySQLCurrencyRegistryMigrationRetainsOperatorCurrencyCheck(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	legacyCurrencyMigrationFixture(t, ctx, m)
	migrationExec(t, ctx, m.DB, "ALTER TABLE orders ADD CONSTRAINT operator_currency_amount CHECK(currency IN ('CNY','USD') AND total_minor<=5000)")
	var before string
	const query = "SELECT CHECK_CLAUSE FROM information_schema.CHECK_CONSTRAINTS WHERE CONSTRAINT_SCHEMA=DATABASE() AND CONSTRAINT_NAME='operator_currency_amount'"
	if err := m.DB.QueryRowContext(ctx, query).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := m.Migrate(ctx); err != nil {
		t.Fatal("migrate with operator currency constraint", err)
	}
	var after string
	if err := m.DB.QueryRowContext(ctx, query).Scan(&after); err != nil || before != after {
		t.Fatal("migration removed or rewrote operator currency constraint", before, after, err)
	}
	if _, err := m.DB.ExecContext(ctx, "UPDATE orders SET total_minor=5001"); err == nil {
		t.Fatal("operator constraint no longer enforced")
	}
	if err := m.Ping(ctx); err != nil {
		t.Fatal(fmt.Errorf("operator-check migration readiness: %w", err))
	}
}

func TestMySQLCurrencyRegistryMigrationRejectsUnexpectedPartialRegistry(t *testing.T) {
	for _, test := range []struct{ name, mutation string }{
		{"changed_config_revision", "UPDATE currency_settings SET document=JSON_SET(document,'$.revision',2) WHERE id=1"},
		{"changed_default", "UPDATE currency_settings SET document=JSON_SET(document,'$.default_currency','CNY') WHERE id=1"},
		{"changed_price_revision", "UPDATE currency_settings SET prices_revision=2 WHERE id=1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			legacyCurrencyMigrationFixture(t, ctx, m)
			if err := m.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			migrationExec(t, ctx, m.DB, "DELETE FROM schema_migrations WHERE version>=7")
			migrationExec(t, ctx, m.DB, test.mutation)
			before, beforePricesRevision := readMigrationCurrencyConfiguration(t, ctx, m)
			assertCurrencyMigrationRefusedWithoutDDL(t, ctx, m)
			after, afterPricesRevision := readMigrationCurrencyConfiguration(t, ctx, m)
			if !reflect.DeepEqual(before, after) || beforePricesRevision != afterPricesRevision {
				t.Fatal("rejected migration changed unexpected registry state", before, after)
			}
		})
	}
}

func TestCurrencyMigrationCheckMatches(t *testing.T) {
	codeCheck := currencyCodeMigrationCheck()
	const rendered = "((octet_length(`currency`) = 3) and (ascii(substr(`currency`,1,1)) between 65 and 90) and (ascii(substr(`currency`,2,1)) between 65 and 90) and (ascii(substr(`currency`,3,1)) between 65 and 90))"
	const revisionCheck = "prices_revision >= 1 AND prices_revision <= 9007199254740991"
	for _, test := range []struct {
		name, actual, expected string
		match                  bool
	}{
		{"source_expression", codeCheck, codeCheck, true},
		{"mysql_parenthesized_substr", rendered, codeCheck, true},
		{"mysql_length_synonym", strings.ReplaceAll(rendered, "octet_length", "length"), codeCheck, true},
		{"nested_conjunction", "(OCTET_LENGTH(currency) = 3 AND (ASCII(SUBSTRING(currency,1,1)) BETWEEN 65 AND 90 AND (ASCII(SUBSTRING(currency,2,1)) BETWEEN 65 AND 90 AND ASCII(SUBSTRING(currency,3,1)) BETWEEN 65 AND 90)))", codeCheck, true},
		{"or_instead_of_and", strings.Replace(codeCheck, " AND ", " OR ", 1), codeCheck, false},
		{"or_added_after_check", "(" + codeCheck + ") OR currency='usd'", codeCheck, false},
		{"missing_length_atom", strings.TrimPrefix(codeCheck, "OCTET_LENGTH(currency) = 3 AND "), codeCheck, false},
		{"missing_last_ascii_atom", strings.TrimSuffix(codeCheck, " AND ASCII(SUBSTRING(currency,3,1)) BETWEEN 65 AND 90"), codeCheck, false},
		{"repeated_ascii_atom", strings.Replace(codeCheck, "SUBSTRING(currency,3,1)", "SUBSTRING(currency,2,1)", 1), codeCheck, false},
		{"extra_repeated_atom", codeCheck + " AND OCTET_LENGTH(currency) = 3", codeCheck, false},
		{"comparison_inside_length", strings.Replace(codeCheck, "OCTET_LENGTH(currency) = 3", "OCTET_LENGTH(currency = 3)", 1), codeCheck, false},
		{"comparison_inside_ascii", strings.Replace(codeCheck, "ASCII(SUBSTRING(currency,1,1)) BETWEEN 65 AND 90", "ASCII(SUBSTRING(currency,1,1) BETWEEN 65 AND 90)", 1), codeCheck, false},
		{"wrong_ascii_upper_bound", strings.Replace(codeCheck, "BETWEEN 65 AND 90", "BETWEEN 65 AND 91", 1), codeCheck, false},
		{"wrong_ascii_lower_bound", strings.Replace(codeCheck, "BETWEEN 65 AND 90", "BETWEEN 64 AND 90", 1), codeCheck, false},
		{"character_length_not_bytes", strings.Replace(codeCheck, "OCTET_LENGTH", "CHAR_LENGTH", 1), codeCheck, false},
		{"wrong_character_position", strings.Replace(codeCheck, "SUBSTRING(currency,1,1)", "SUBSTRING(currency,0,1)", 1), codeCheck, false},
		{"hidden_comment", codeCheck + " /* changed */", codeCheck, false},
		{"revision_mysql_parentheses", "((`prices_revision` >= 1) and (`prices_revision` <= 9007199254740991))", revisionCheck, true},
		{"revision_or", "prices_revision >= 1 OR prices_revision <= 9007199254740991", revisionCheck, false},
		{"revision_missing_cap", "prices_revision >= 1", revisionCheck, false},
		{"revision_repeated_lower_bound", "prices_revision >= 1 AND prices_revision >= 1", revisionCheck, false},
		{"revision_unsafe_cap", "prices_revision >= 1 AND prices_revision <= 9007199254740992", revisionCheck, false},
		{"legacy_mysql_check", "(`currency` in (_utf8mb4'CNY',_utf8mb4'USD'))", "currency IN ('CNY','USD')", true},
		{"legacy_extra_currency", "currency IN ('CNY','USD','EUR')", "currency IN ('CNY','USD')", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			check := currencyMigrationCheck{kind: "CHECK", enforced: "YES", clause: test.actual}
			if actual := currencyMigrationCheckMatches(check, test.expected); actual != test.match {
				t.Fatalf("match=%t want=%t for %q against %q", actual, test.match, test.actual, test.expected)
			}
		})
	}
	for _, check := range []currencyMigrationCheck{
		{kind: "CHECK", enforced: "NO", clause: codeCheck},
		{kind: "UNIQUE", enforced: "YES", clause: codeCheck},
		{kind: "", enforced: "", clause: codeCheck},
	} {
		if currencyMigrationCheckMatches(check, codeCheck) {
			t.Fatalf("accepted ineffective constraint metadata: %+v", check)
		}
	}
}

func TestMySQLCurrencyMigrationPreservesMissingHistoricalPlanSnapshot(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	legacyCurrencyMigrationFixture(t, ctx, m)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM orders WHERE plan_snapshot IS NOT NULL").Scan(&count); err != nil || count != 0 {
		t.Fatal("invented historical plan snapshot", count, err)
	}
}
func TestMySQLCurrencyMigrationRejectsConflictingPlanSnapshot(t *testing.T) {
	for _, fixture := range []string{"ALTER TABLE orders ADD COLUMN plan_snapshot TEXT NULL", "ALTER TABLE orders ADD COLUMN plan_snapshot JSON NULL"} {
		t.Run(fixture, func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			legacyCurrencyMigrationFixture(t, ctx, m)
			migrationExec(t, ctx, m.DB, fixture)
			if strings.Contains(fixture, "JSON") {
				migrationExec(t, ctx, m.DB, "UPDATE orders SET plan_snapshot=JSON_OBJECT('name','unverified historical metadata')")
			}
			assertCurrencyMigrationRefusedWithoutDDL(t, ctx, m)
		})
	}
}
