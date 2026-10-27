package billing

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Each case owns a uniquely named disposable database. No shared test schema is
// dropped or rewritten, and these tests deliberately do not run in parallel.
func migrationTestDatabase(t *testing.T) (*MySQL, context.Context) {
	t.Helper()
	dsn := os.Getenv("ASTER_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("ASTER_MYSQL_TEST_DSN not set; real MySQL migration acceptance not run")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil || !strings.Contains(cfg.DBName, "_test") {
		t.Fatal("integration database name must contain _test")
	}
	cfg.ParseTime = true
	cfg.MultiStatements = false
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 10 * time.Second
	cfg.WriteTimeout = 10 * time.Second
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open disposable database administrator", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	name := "aster_migration_test_" + ID()
	if _, err = admin.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"); err != nil {
		t.Fatal("create isolated migration database (test credential needs CREATE/DROP DATABASE)", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := admin.ExecContext(cleanupCtx, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error("clean up isolated migration database", err)
		}
	})
	cfg.DBName = name
	m, err := OpenMySQL(ctx, cfg.FormatDSN(), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.DB.Close() })
	return m, ctx
}

func migrationExec(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		t.Fatal("migration acceptance fixture SQL", err)
	}
}

func migrationStatements(source string) []string {
	var statements []string
	for _, statement := range strings.Split(source, ";") {
		if statement = strings.TrimSpace(statement); statement != "" {
			statements = append(statements, statement)
		}
	}
	return statements
}

// Build the historical database directly from the immutable migration files;
// do not use the current migrator to manufacture its own upgrade fixture.
func migrationHistoricalPrefix(t *testing.T, ctx context.Context, m *MySQL, versions int) {
	t.Helper()
	migrationExec(t, ctx, m.DB, "CREATE TABLE schema_migrations(version INT PRIMARY KEY, checksum CHAR(64) NOT NULL, applied_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)) ENGINE=InnoDB")
	for index := 0; index < versions; index++ {
		migrationPrelude(t, ctx, m, index+1)
		for _, statement := range migrationStatements(schemaVersions[index]) {
			migrationExec(t, ctx, m.DB, statement)
		}
		if index == 0 {
			// These are original v1 USD examples, independent of current catalog defaults.
			for _, plan := range []struct {
				id     string
				amount int
			}{{"starter", 600}, {"standard", 1200}, {"performance", 2400}} {
				migrationExec(t, ctx, m.DB, "INSERT INTO plans(id,name,description,cpu,memory_gb,disk_gb,transfer_tb,region,price_minor,currency,available) VALUES(?,?,'historical example',1,1,20,1,'historical region',?,'USD',FALSE)", plan.id, plan.id, plan.amount)
			}
		}
		if index == 1 {
			raw, err := json.Marshal(DefaultSettings())
			if err != nil {
				t.Fatal(err)
			}
			migrationExec(t, ctx, m.DB, "INSERT INTO site_settings(id,document) VALUES(1,?)", raw)
		}
		migrationExec(t, ctx, m.DB, "INSERT INTO schema_migrations(version,checksum) VALUES(?,?)", index+1, digest(schemaVersions[index]))
	}
}

func migrationPrelude(t *testing.T, ctx context.Context, m *MySQL, version int) {
	t.Helper()
	if version != 3 {
		return
	}
	// The v1 anonymous USD checks use these generated names on a fresh MySQL
	// fixture. Verify the expected expression before removing them for v3 DDL.
	for _, table := range []string{"orders", "invoices"} {
		name := table + "_chk_2"
		var clause string
		if err := m.DB.QueryRowContext(ctx, "SELECT CHECK_CLAUSE FROM information_schema.CHECK_CONSTRAINTS WHERE CONSTRAINT_SCHEMA=DATABASE() AND CONSTRAINT_NAME=?", name).Scan(&clause); err != nil {
			t.Fatal("historical USD constraint", err)
		}
		if !strings.Contains(clause, "currency") || !strings.Contains(clause, "USD") {
			t.Fatalf("unexpected historical constraint %s: %s", name, clause)
		}
		migrationExec(t, ctx, m.DB, "ALTER TABLE `"+table+"` DROP CHECK `"+name+"`")
	}
}

func migrationHistory(t *testing.T, ctx context.Context, m *MySQL) []string {
	t.Helper()
	rows, err := m.DB.QueryContext(ctx, "SELECT version,checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var history []string
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			t.Fatal(err)
		}
		history = append(history, fmt.Sprintf("%d:%s", version, checksum))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return history
}

func TestMySQLMigrationFreshAndRepeat(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal("fresh migration", err)
	}
	if err := m.Ping(ctx); err != nil {
		t.Fatal("fresh readiness", err)
	}
	if history := migrationHistory(t, ctx, m); len(history) != len(schemaVersions) {
		t.Fatal("missing migration versions", history)
	}
	settings, err := m.Settings(ctx)
	if err != nil || settings.DefaultLocale != "zh-CN" || settings.DefaultCurrency != "CNY" {
		t.Fatal("Chinese/CNY defaults", settings, err)
	}
	var sellable, unsupported, users int
	if err = m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM plan_prices WHERE available=TRUE").Scan(&sellable); err != nil {
		t.Fatal(err)
	}
	if err = m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM plan_prices WHERE currency <> 'CNY' OR amount_minor<=0").Scan(&unsupported); err != nil {
		t.Fatal(err)
	}
	if err = m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&users); err != nil {
		t.Fatal(err)
	}
	if sellable != 0 || unsupported != 0 || users != 0 {
		t.Fatalf("unsafe fresh seeds: sellable=%d unsupported=%d users=%d", sellable, unsupported, users)
	}
	settings.SiteName = "迁移重复执行测试"
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	migrationExec(t, ctx, m.DB, "UPDATE site_settings SET document=? WHERE id=1", raw)
	migrationExec(t, ctx, m.DB, "UPDATE plan_prices SET amount_minor=1777 WHERE plan_id='starter' AND currency='CNY'")
	before := migrationHistory(t, ctx, m)
	if err = m.Migrate(ctx); err != nil {
		t.Fatal("repeat migration", err)
	}
	if !reflect.DeepEqual(before, migrationHistory(t, ctx, m)) {
		t.Fatal("repeat migration rewrote history")
	}
	stored, err := m.Settings(ctx)
	if err != nil || stored.SiteName != settings.SiteName {
		t.Fatal("repeat migration overwrote operator settings", err)
	}
	var price int64
	if err = m.DB.QueryRowContext(ctx, "SELECT amount_minor FROM plan_prices WHERE plan_id='starter' AND currency='CNY'").Scan(&price); err != nil || price != 1777 {
		t.Fatal("repeat migration overwrote operator price", price, err)
	}
}

func TestMySQLMigrationHistoricalUSD(t *testing.T) {
	for _, removeExamples := range []bool{false, true} {
		t.Run(fmt.Sprintf("removed_examples_%t", removeExamples), func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			migrationHistoricalPrefix(t, ctx, m, 1)
			if removeExamples {
				migrationExec(t, ctx, m.DB, "DELETE FROM plans")
			}
			userID, orderID, invoiceID := ID(), ID(), ID()
			migrationExec(t, ctx, m.DB, "INSERT INTO users(id,email,name,role,password_hash) VALUES(?,'historical@integration.invalid','历史客户','customer','unused')", userID)
			migrationExec(t, ctx, m.DB, "INSERT INTO plans(id,name,description,cpu,memory_gb,disk_gb,transfer_tb,region,price_minor,currency,available) VALUES('legacy-custom','历史套餐','historical',1,1,20,1,'historical region',1777,'USD',TRUE)")
			migrationExec(t, ctx, m.DB, "INSERT INTO orders(id,number,user_id,plan_id,plan_name,hostname,status,total_minor,currency,created_at) VALUES(?,'ORD-HISTORICAL',?,'legacy-custom','下单时套餐名','legacy-host','pending_payment',1234,'USD','2025-01-02 03:04:05')", orderID, userID)
			migrationExec(t, ctx, m.DB, "INSERT INTO invoices(id,number,user_id,order_id,status,amount_minor,currency,issued_at,due_at) VALUES(?,'INV-HISTORICAL',?,?,'unpaid',1234,'USD','2025-01-02 03:04:05','2025-01-09 03:04:05')", invoiceID, userID, orderID)
			if err := m.Migrate(ctx); err != nil {
				t.Fatal("upgrade historical v1 database", err)
			}
			if err := m.Ping(ctx); err != nil {
				t.Fatal("upgraded readiness", err)
			}
			var orderAmount, invoiceAmount int64
			var orderCurrency, invoiceCurrency, planName, status string
			var missingIssuer bool
			if err := m.DB.QueryRowContext(ctx, "SELECT o.total_minor,o.currency,o.plan_name,i.amount_minor,i.currency,i.status,i.issuer_snapshot IS NULL FROM orders o JOIN invoices i ON i.order_id=o.id WHERE o.id=?", orderID).Scan(&orderAmount, &orderCurrency, &planName, &invoiceAmount, &invoiceCurrency, &status, &missingIssuer); err != nil {
				t.Fatal(err)
			}
			if orderAmount != 1234 || invoiceAmount != 1234 || orderCurrency != "USD" || invoiceCurrency != "USD" || planName != "下单时套餐名" || status != "unpaid" || !missingIssuer {
				t.Fatalf("historical billing changed: order=%d/%s invoice=%d/%s plan=%q status=%s issuer_missing=%t", orderAmount, orderCurrency, invoiceAmount, invoiceCurrency, planName, status, missingIssuer)
			}
			var price int64
			var available bool
			if err := m.DB.QueryRowContext(ctx, "SELECT amount_minor,available FROM plan_prices WHERE plan_id='legacy-custom' AND currency='USD'").Scan(&price, &available); err != nil || price != 1777 || !available {
				t.Fatal("existing USD price was not preserved", price, available, err)
			}
			var cny int
			if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM plan_prices WHERE plan_id='legacy-custom' AND currency='CNY'").Scan(&cny); err != nil || cny != 0 {
				t.Fatal("migration invented a converted CNY price", cny, err)
			}
			migrationExec(t, ctx, m.DB, "UPDATE orders SET currency='CNY' WHERE id=?", orderID)
			migrationExec(t, ctx, m.DB, "UPDATE invoices SET currency='CNY' WHERE id=?", invoiceID)
			if _, err := m.DB.ExecContext(ctx, "UPDATE invoices SET currency='usd' WHERE id=?", invoiceID); err == nil {
				t.Fatal("noncanonical invoice currency accepted by schema")
			}
		})
	}
}

func TestMySQLMigrationInterruptedRecovery(t *testing.T) {
	for index, source := range schemaVersions {
		if index == 6 {
			continue
		} // Migration 7 has its own exact-resumption boundary suite.
		statements := migrationStatements(source)
		boundaries := []int{0, len(statements) / 2, len(statements)}
		if index == 12 {
			boundaries = []int{0, 1, 2, len(statements)} // Schema13 has three additive creates; test each interruption.
		}
		previous := -1
		for _, boundary := range boundaries {
			if boundary == previous {
				continue
			}
			previous = boundary
			t.Run(fmt.Sprintf("v%d_after_%d_statements", index+1, boundary), func(t *testing.T) {
				m, ctx := migrationTestDatabase(t)
				migrationHistoricalPrefix(t, ctx, m, index)
				migrationPrelude(t, ctx, m, index+1)
				for _, statement := range statements[:boundary] {
					migrationExec(t, ctx, m.DB, statement)
				}
				if err := m.Migrate(ctx); err != nil {
					t.Fatal("resume interrupted migration", err)
				}
				if err := m.Ping(ctx); err != nil {
					t.Fatal("resumed readiness", err)
				}
				if err := m.Migrate(ctx); err != nil {
					t.Fatal("repeat resumed migration", err)
				}
			})
		}
	}
}

func TestMySQLMigrationRejectsInvalidHistory(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, context.Context, *MySQL)
	}{
		{"checksum", func(t *testing.T, ctx context.Context, m *MySQL) {
			migrationExec(t, ctx, m.DB, "UPDATE schema_migrations SET checksum=? WHERE version=1", strings.Repeat("0", 64))
		}},
		{"future_version", func(t *testing.T, ctx context.Context, m *MySQL) {
			migrationExec(t, ctx, m.DB, "INSERT INTO schema_migrations(version,checksum) VALUES(?,?)", len(schemaVersions)+1, strings.Repeat("0", 64))
		}},
		{"missing_first_version", func(t *testing.T, ctx context.Context, m *MySQL) {
			migrationExec(t, ctx, m.DB, "DELETE FROM schema_migrations WHERE version=1")
		}},
		{"gap", func(t *testing.T, ctx context.Context, m *MySQL) {
			migrationExec(t, ctx, m.DB, "DELETE FROM schema_migrations WHERE version=2")
		}},
		{"zero_version", func(t *testing.T, ctx context.Context, m *MySQL) {
			migrationExec(t, ctx, m.DB, "INSERT INTO schema_migrations(version,checksum) VALUES(0,?)", strings.Repeat("0", 64))
		}},
		{"negative_version", func(t *testing.T, ctx context.Context, m *MySQL) {
			migrationExec(t, ctx, m.DB, "INSERT INTO schema_migrations(version,checksum) VALUES(-1,?)", strings.Repeat("0", 64))
		}},
		{"gap_and_later_bad_checksum", func(t *testing.T, ctx context.Context, m *MySQL) {
			migrationExec(t, ctx, m.DB, "DELETE FROM schema_migrations WHERE version=1")
			migrationExec(t, ctx, m.DB, "UPDATE schema_migrations SET checksum=? WHERE version=2", strings.Repeat("0", 64))
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			if err := m.Migrate(ctx); err != nil {
				t.Fatal("fixture migration", err)
			}
			test.mutate(t, ctx, m)
			before := migrationHistory(t, ctx, m)
			if err := m.Ping(ctx); err == nil {
				t.Error("readiness accepted invalid migration history")
			}
			if err := m.Migrate(ctx); err == nil {
				t.Error("migrator accepted invalid migration history")
			}
			if after := migrationHistory(t, ctx, m); !reflect.DeepEqual(before, after) {
				t.Fatalf("migrator changed invalid history: before=%v after=%v", before, after)
			}
		})
	}
}

func TestMySQLMigrationRejectsConflictingPartialDDL(t *testing.T) {
	cases := []struct {
		name    string
		version int
		ddl     string
	}{
		{"issuer_wrong_type", 2, "ALTER TABLE invoices ADD COLUMN issuer_snapshot VARCHAR(100) NULL"},
		{"issuer_not_nullable", 2, "ALTER TABLE invoices ADD COLUMN issuer_snapshot JSON NOT NULL"},
		{"issuer_default_expression", 2, "ALTER TABLE invoices ADD COLUMN issuer_snapshot JSON NULL DEFAULT (JSON_OBJECT())"},
		{"currency_weakened", 3, "ALTER TABLE orders ADD CONSTRAINT orders_currency CHECK(currency IN ('CNY','USD','EUR'))"},
		{"currency_not_enforced", 3, "ALTER TABLE invoices ADD CONSTRAINT invoices_currency CHECK(currency IN ('CNY','USD')) NOT ENFORCED"},
		{"amount_weakened", 4, "ALTER TABLE invoices ADD CONSTRAINT invoices_exact_minor CHECK(amount_minor <= 9007199254740992)"},
		{"amount_not_enforced", 4, "ALTER TABLE orders ADD CONSTRAINT orders_exact_minor CHECK(total_minor <= 9007199254740991) NOT ENFORCED"},
		{"currency_cast_not_binary", 5, "ALTER TABLE orders ADD CONSTRAINT orders_currency_exact CHECK(CAST(currency AS CHAR CHARACTER SET utf8mb4) IN ('CNY','USD'))"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			migrationHistoricalPrefix(t, ctx, m, test.version-1)
			migrationExec(t, ctx, m.DB, test.ddl)
			before := migrationHistory(t, ctx, m)
			if err := m.Migrate(ctx); err == nil {
				t.Error("accepted a conflicting pre-existing schema object")
			}
			if after := migrationHistory(t, ctx, m); !reflect.DeepEqual(before, after) {
				t.Fatal("recorded success for a conflicting migration", before, after)
			}
			if err := m.Ping(ctx); err == nil {
				t.Error("readiness accepted an incomplete conflicting migration")
			}
		})
	}
}

func TestMySQLMigrationRetainsUnrelatedChecks(t *testing.T) {
	for _, sameLegacyName := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy_name_%t", sameLegacyName), func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			migrationHistoricalPrefix(t, ctx, m, 1)
			name := "operator_usd_cap"
			if sameLegacyName {
				name = "orders_chk_2"
				migrationExec(t, ctx, m.DB, "ALTER TABLE orders DROP CHECK orders_chk_2")
			}
			migrationExec(t, ctx, m.DB, "ALTER TABLE orders ADD CONSTRAINT `"+name+"` CHECK(currency='USD' AND total_minor<=1000)")
			if err := m.Migrate(ctx); err != nil {
				t.Fatal("migration with an independent operator check", err)
			}
			var clause string
			if err := m.DB.QueryRowContext(ctx, "SELECT CHECK_CLAUSE FROM information_schema.CHECK_CONSTRAINTS WHERE CONSTRAINT_SCHEMA=DATABASE() AND CONSTRAINT_NAME=?", name).Scan(&clause); err != nil {
				t.Fatal("migration removed an unrelated operator constraint", err)
			}
			actual, actualOK := normalizeMigrationCheck(clause)
			// MySQL adds parentheses around each simple predicate when rendering
			// this composite check; verify both remain without altering the SQL.
			if !actualOK || !strings.Contains(actual, "currency = 'USD'") || !strings.Contains(actual, "total_minor <= 1000") || !strings.Contains(actual, " and ") {
				t.Fatal("migration changed an unrelated operator constraint", clause)
			}
		})
	}
}

func TestNormalizeMigrationCheck(t *testing.T) {
	for _, test := range []struct {
		actual   string
		expected string
	}{
		{"(`currency` in (_utf8mb4\\'CNY\\',_utf8mb4\\'USD\\'))", "currency IN ('CNY','USD')"},
		{"((`CURRENCY` = _ascii'USD'))", "currency = 'USD'"},
		{"( `amount_minor` <= 9007199254740991 )", "amount_minor<=9007199254740991"},
		{"(cast(`currency` as char charset binary) in (_utf8mb4\\'CNY\\',_utf8mb4\\'USD\\'))", "CAST(currency AS BINARY) IN ('CNY','USD')"},
	} {
		actual, actualOK := normalizeMigrationCheck(test.actual)
		expected, expectedOK := normalizeMigrationCheck(test.expected)
		if !actualOK || !expectedOK || actual != expected {
			t.Errorf("equivalent MySQL check not recognized: %q -> %q; %q -> %q", test.actual, actual, test.expected, expected)
		}
	}
	expected, _ := normalizeMigrationCheck("currency IN ('CNY','USD')")
	for _, expression := range []string{
		"currency IN ('cny','usd')",
		"currency IN ('CNY','USD','EUR')",
		"currency IN ('CNY','USD') OR amount_minor=0",
		"currency IN ('CNY','USD') AND amount_minor>0",
		"currency IN ('CNY','USD') /* hidden predicate */",
		"currency IN ('CNY','USD'); DROP TABLE orders",
	} {
		actual, ok := normalizeMigrationCheck(expression)
		if ok && actual == expected {
			t.Errorf("different expression normalized to expected check: %q", expression)
		}
	}
	left, _ := normalizeMigrationCheck("(currency = 'USD' OR currency = 'CNY') AND amount_minor > 0")
	right, _ := normalizeMigrationCheck("currency = 'USD' OR (currency = 'CNY' AND amount_minor > 0)")
	if left == right {
		t.Fatal("normalization erased meaningful expression grouping")
	}
	binaryCheck, binaryOK := normalizeMigrationCheck("CAST(currency AS BINARY) IN ('CNY','USD')")
	if !binaryOK {
		t.Fatal("binary currency check not recognized")
	}
	for _, expression := range []string{
		"CAST(currency AS CHAR CHARSET utf8mb4) IN ('CNY','USD')",
		"CAST(currency AS CHAR(3) CHARSET BINARY) IN ('CNY','USD')",
		"CAST(LEFT(currency,3) AS BINARY) IN ('CNY','USD')",
		"currency IN ('CNY','USD')",
	} {
		actual, ok := normalizeMigrationCheck(expression)
		if ok && actual == binaryCheck {
			t.Errorf("weaker or different cast normalized to binary currency check: %q", expression)
		}
	}
}
