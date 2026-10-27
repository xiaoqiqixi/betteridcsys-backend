package billing

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestMySQLTransportExceptionIsLocalOnly(t *testing.T) {
	for _, dsn := range []string{"user:secret@tcp(127.0.0.1:3306)/aster", "user:secret@tcp(db.invalid:3306)/aster?tls=true", "user:secret@unix(relative.sock)/aster", "user:secret@unix(/run/mysql.sock)/aster?tls=skip-verify", "user:secret@unix(/run/mysql.sock)/aster?allowFallbackToPlaintext=true"} {
		if _, err := mysqlConfiguration(dsn, true); err == nil {
			t.Fatal("accepted unsafe local exception", dsn)
		}
	}
	for _, dsn := range []string{"user:secret@tcp(db.invalid:3306)/aster", "user:secret@tcp(db.invalid:3306)/aster?tls=preferred", "user:secret@tcp(db.invalid:3306)/aster?tls=skip-verify", "user:secret@tcp(db.invalid:3306)/aster?tls=true&allowFallbackToPlaintext=true"} {
		if _, err := mysqlConfiguration(dsn, false); err == nil {
			t.Fatal("accepted insecure network configuration")
		}
	}
	if _, err := mysqlConfiguration("user:secret@unix(/run/mysql.sock)/aster", true); err != nil {
		t.Fatal(err)
	}
	if _, err := mysqlConfiguration("user:secret@tcp(db.invalid:3306)/aster?tls=true", false); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLDSNCannotRunConnectionSetupSQL(t *testing.T) {
	for _, options := range []string{"GLOBAL max_connections=999", "GLOBAL%20max_connections=999", "sql_mode=%27%27", "init_command=DROP+TABLE+users", "sessionVariables=autocommit=0", "autocommit=false", "charset=utf8mb4", "charset=", "charset=utf8mb4&collation=utf8mb4_0900_ai_ci", "collation=utf8mb4_0900_ai_ci"} {
		if _, err := mysqlConfiguration("user:secret@unix(/run/mysql.sock)/aster?"+options, true); err == nil {
			t.Fatal("DSN can execute connection setup SQL", options)
		}
	}
	for _, options := range []string{"parseTime=true", "timeout=5s&readTimeout=10s", "multiStatements=true"} {
		cfg, err := mysqlConfiguration("user:secret@unix(/run/mysql.sock)/aster?"+options, true)
		if err != nil || len(cfg.Params) != 0 || cfg.MultiStatements {
			t.Fatal("safe normalized configuration rejected", options, err)
		}
	}
	if _, err := mysqlConfiguration("user:secret?/slash@unix(/run/mysql.sock)/aster?parseTime=true", true); err != nil {
		t.Fatal("password punctuation changed DSN option validation", err)
	}
}

type preflightReadOnlyDriver struct{}
type preflightReadOnlyConn struct{ scenario string }
type preflightRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (preflightReadOnlyDriver) Open(scenario string) (driver.Conn, error) {
	return preflightReadOnlyConn{scenario}, nil
}
func (preflightReadOnlyConn) Close() error { return nil }
func (preflightReadOnlyConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (preflightReadOnlyConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepared statement")
}
func (conn preflightReadOnlyConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	switch {
	case query == "SELECT CURRENT_ROLE()":
		if conn.scenario == "active_role" {
			return &preflightRows{columns: []string{"role"}, values: [][]driver.Value{{"`reader`@`%`"}}}, nil
		}
		return &preflightRows{columns: []string{"role"}, values: [][]driver.Value{{"NONE"}}}, nil
	case query == "SHOW GRANTS FOR CURRENT_USER":
		if conn.scenario == "table_scope" {
			return &preflightRows{columns: []string{"grants"}, values: [][]driver.Value{{"GRANT SELECT ON `aster_test`.`orders` TO `aster_test`@`localhost`"}}}, nil
		}
		if conn.scenario == "column_scope" {
			return &preflightRows{columns: []string{"grants"}, values: [][]driver.Value{{"GRANT SELECT (`id`) ON `aster_test`.`orders` TO `aster_test`@`localhost`"}}}, nil
		}
		if conn.scenario == "partial_revoke" {
			return &preflightRows{columns: []string{"grants"}, values: [][]driver.Value{{"GRANT SELECT ON *.* TO `aster_test`@`localhost`"}, {"REVOKE SELECT ON `aster_test`.* FROM `aster_test`@`localhost`"}}}, nil
		}
		return &preflightRows{columns: []string{"grants"}, values: [][]driver.Value{{"GRANT SELECT ON `aster_test`.* TO `aster_test`@`localhost`"}}}, nil
	case query == "SELECT VERSION(),@@version_comment,DATABASE(),@@character_set_database,@@collation_database":
		if conn.scenario == "different_vendor" {
			return &preflightRows{columns: []string{"version", "comment", "db", "charset", "collation"}, values: [][]driver.Value{{"8.4.11", "MySQL compatible service", "aster_test", "utf8mb4", "utf8mb4_0900_ai_ci"}}}, nil
		}
		return &preflightRows{columns: []string{"version", "comment", "db", "charset", "collation"}, values: [][]driver.Value{{"8.4.11", "MySQL Community Server - GPL", "aster_test", "utf8mb4", "utf8mb4_0900_ai_ci"}}}, nil
	case strings.HasPrefix(query, "SELECT TABLE_NAME,TABLE_TYPE,"):
		return &preflightRows{columns: []string{"table", "type", "engine"}}, nil
	case strings.HasPrefix(query, "SELECT (SELECT COUNT(*) FROM information_schema.TRIGGERS"):
		return &preflightRows{columns: []string{"count"}, values: [][]driver.Value{{int64(0)}}}, nil
	default:
		return nil, errors.New("unexpected query: only the five empty-target reads are permitted")
	}
}
func (r *preflightRows) Columns() []string { return r.columns }
func (r *preflightRows) Close() error      { return nil }
func (r *preflightRows) Next(dst []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dst, r.values[r.index])
	r.index++
	return nil
}
func init() { sql.Register("aster-preflight-read-only", preflightReadOnlyDriver{}) }

func TestPreflightEmptyDatabaseNeedsOnlyReads(t *testing.T) {
	db, err := sql.Open("aster-preflight-read-only", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := &MySQL{DB: db, transport: "local-unix-socket", database: "aster_test"}
	status, err := m.SchemaStatus(context.Background())
	if err != nil || !status.ReadOnly || status.Status != "empty" || !status.EmptyTarget || status.AdminCount != nil || len(status.PendingMigrations) != len(schemaVersions) || len(status.MigrationChecksums) != 0 {
		t.Fatalf("empty inspection: %+v %v", status, err)
	}
}

func TestPreflightCannotCallHiddenTablesEmpty(t *testing.T) {
	for _, scenario := range []string{"table_scope", "column_scope", "partial_revoke", "active_role"} {
		t.Run(scenario, func(t *testing.T) {
			db, err := sql.Open("aster-preflight-read-only", scenario)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			m := &MySQL{DB: db, transport: "local-unix-socket", database: "aster_test"}
			status, err := m.SchemaStatus(context.Background())
			if err == nil || status.EmptyTarget || status.TableVisibilityVerified || status.Status != "incompatible" {
				t.Fatalf("false empty target: %+v %v", status, err)
			}
		})
	}
}

func TestPreflightRequiresReviewedMySQLVendor(t *testing.T) {
	db, err := sql.Open("aster-preflight-read-only", "different_vendor")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := &MySQL{DB: db, transport: "local-unix-socket", database: "aster_test"}
	status, err := m.SchemaStatus(context.Background())
	if err == nil || status.Vendor != "unknown" || status.Status != "incompatible" {
		t.Fatalf("unreviewed vendor accepted: %+v %v", status, err)
	}
}

func TestPreflightServiceCurrencyCheckStartsAtMigration4(t *testing.T) {
	statement := "ALTER TABLE services ADD CONSTRAINT services_currency CHECK(currency IN ('CNY','USD'))"
	if strings.Contains(schemaVersions[2], statement) || !strings.Contains(schemaVersions[3], statement) {
		t.Fatal("service currency check introduction differs from the immutable migration sources")
	}
	for version := 1; version <= len(schemaVersions); version++ {
		expected := preflightChecks(version)["services"]["services_currency"]
		if version >= 4 && version < 7 {
			if expected != "currency IN ('CNY','USD')" {
				t.Fatalf("schema %d must require services_currency", version)
			}
		} else if expected != "" {
			t.Fatalf("schema %d must not require services_currency", version)
		}
	}
}

func TestMySQLPreflightFreshAndPrefixes(t *testing.T) {
	for version := 0; version <= len(schemaVersions); version++ {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			if version == len(schemaVersions) {
				if err := m.Migrate(ctx); err != nil {
					t.Fatal(err)
				}
			} else if version != 0 {
				migrationHistoricalPrefix(t, ctx, m, version)
			}
			status, err := m.SchemaStatus(ctx)
			if err != nil {
				t.Fatalf("known prefix %d: %+v %v", version, status, err)
			}
			want := "pending_migrations"
			if version == 0 {
				want = "empty"
			}
			if version == len(schemaVersions) {
				want = "current"
			}
			if status.Status != want || status.SchemaVersion != version || len(status.PendingMigrations) != len(schemaVersions)-version {
				t.Fatalf("wrong status: %+v", status)
			}
			if version == 0 {
				var tables int
				if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE()").Scan(&tables); err != nil || tables != 0 {
					t.Fatal("preflight created tables", tables, err)
				}
			} else {
				before := migrationHistory(t, ctx, m)
				if _, err = m.SchemaStatus(ctx); err != nil {
					t.Fatal(err)
				}
				after := migrationHistory(t, ctx, m)
				if strings.Join(before, ",") != strings.Join(after, ",") {
					t.Fatal("preflight changed migration history")
				}
			}
		})
	}
}

func TestMySQLPreflightRejectsIncompatibleSchema(t *testing.T) {
	for name, mutation := range map[string]string{
		"unknown_version":     fmt.Sprintf("INSERT INTO schema_migrations(version,checksum) VALUES(%d,REPEAT('a',64))", len(schemaVersions)+1),
		"modified_checksum":   "UPDATE schema_migrations SET checksum=REPEAT('a',64) WHERE version=1",
		"missing_history":     "DELETE FROM schema_migrations WHERE version=4",
		"unknown_table":       "CREATE TABLE unexpected(id INT) ENGINE=InnoDB",
		"modified_column":     "ALTER TABLE users MODIFY name VARCHAR(10) NOT NULL",
		"missing_column":      "ALTER TABLE orders DROP COLUMN plan_snapshot",
		"missing_check":       "ALTER TABLE orders DROP CHECK orders_currency_code",
		"modified_check":      "ALTER TABLE orders DROP CHECK orders_exact_minor, ADD CONSTRAINT orders_exact_minor CHECK(total_minor <= 9007199254740992)",
		"missing_unique_key":  "ALTER TABLE users DROP INDEX email",
		"missing_foreign_key": "ALTER TABLE orders DROP FOREIGN KEY orders_ibfk_2",
	} {
		t.Run(name, func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			if err := m.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			migrationExec(t, ctx, m.DB, mutation)
			status, err := m.SchemaStatus(ctx)
			if err == nil || status.Status != "incompatible" || status.Error == "" {
				t.Fatalf("accepted incompatible schema: %+v %v", status, err)
			}
		})
	}
	t.Run("partial_next_migration", func(t *testing.T) {
		m, ctx := migrationTestDatabase(t)
		migrationHistoricalPrefix(t, ctx, m, 6)
		migrationExec(t, ctx, m.DB, "ALTER TABLE orders ADD COLUMN currency_snapshot JSON NULL")
		if _, err := m.SchemaStatus(ctx); err == nil {
			t.Fatal("accepted partial migration 7")
		}
	})
	t.Run("partial_check_only_migration", func(t *testing.T) {
		m, ctx := migrationTestDatabase(t)
		migrationHistoricalPrefix(t, ctx, m, 4)
		migrationExec(t, ctx, m.DB, "ALTER TABLE orders ADD CONSTRAINT orders_currency_exact CHECK(CAST(currency AS BINARY) IN ('CNY','USD'))")
		if _, err := m.SchemaStatus(ctx); err == nil {
			t.Fatal("accepted partial check-only migration 5")
		}
	})
	t.Run("partial_service_currency_migration", func(t *testing.T) {
		m, ctx := migrationTestDatabase(t)
		migrationHistoricalPrefix(t, ctx, m, 3)
		migrationExec(t, ctx, m.DB, "ALTER TABLE services ADD CONSTRAINT services_currency CHECK(currency IN ('CNY','USD'))")
		if _, err := m.SchemaStatus(ctx); err == nil {
			t.Fatal("accepted migration 4 service check under schema 3 history")
		}
	})
}

func TestMySQLPreflightRetainsCompatibleOperatorObjects(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	migrationExec(t, ctx, m.DB, "ALTER TABLE plans ADD INDEX operator_cpu(cpu), ADD CONSTRAINT operator_cpu_positive CHECK(cpu >= 0)")
	migrationExec(t, ctx, m.DB, "INSERT INTO users(id,email,name,role,password_hash) VALUES(?,'operator@preflight.invalid','Operator','admin','unused')", ID())
	status, err := m.SchemaStatus(ctx)
	if err != nil || status.Status != "current" || status.AdminCount == nil || *status.AdminCount != 1 {
		t.Fatalf("compatible schema/admin count: %+v %v", status, err)
	}
}
