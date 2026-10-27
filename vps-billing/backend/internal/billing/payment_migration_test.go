package billing

import (
	"reflect"
	"strings"
	"testing"
)

func TestMySQLPaymentMigrationRejectsCollidingTables(t *testing.T) {
	statements := migrationStatements(schemaVersions[7])
	for _, test := range []struct {
		name, table, before, after string
	}{
		{"attempt_amount_type", "payment_attempts", "amount_minor BIGINT", "amount_minor DECIMAL(20,0)"},
		{"attempt_request_collation", "payment_attempts", "request_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin", "request_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_general_ci"},
		{"attempt_default", "payment_attempts", "amount_minor BIGINT NOT NULL", "amount_minor BIGINT NOT NULL DEFAULT 1"},
		{"attempt_missing_unique", "payment_attempts", "UNIQUE KEY payment_attempts_invoice(invoice_id),", "KEY payment_attempts_invoice(invoice_id),"},
		{"attempt_missing_primary", "payment_attempts", "NOT NULL PRIMARY KEY,", "NOT NULL UNIQUE,"},
		{"attempt_weakened_cap", "payment_attempts", "amount_minor <= 9007199254740991", "amount_minor <= 9007199254740992"},
		{"attempt_unenforced_cap", "payment_attempts", "CHECK(amount_minor <= 9007199254740991)", "CHECK(amount_minor <= 9007199254740991) NOT ENFORCED"},
		{"attempt_delete_cascade", "payment_attempts", "FOREIGN KEY(user_id) REFERENCES users(id)", "FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE"},
		{"settlement_event_collation", "payment_settlements", "event_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin", "event_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_general_ci"},
		{"settlement_missing_unique", "payment_settlements", "UNIQUE KEY payment_settlements_transaction(provider_id,transaction_id),", "KEY payment_settlements_transaction(provider_id,transaction_id),"},
		{"settlement_update_cascade", "payment_settlements", "FOREIGN KEY(attempt_id) REFERENCES payment_attempts(id)", "FOREIGN KEY(attempt_id) REFERENCES payment_attempts(id) ON UPDATE CASCADE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			migrationHistoricalPrefix(t, ctx, m, 7)
			statement := statements[0]
			if test.table == "payment_settlements" {
				migrationExec(t, ctx, m.DB, statements[0])
				statement = statements[1]
			}
			mutated := strings.Replace(statement, test.before, test.after, 1)
			if mutated == statement {
				t.Fatal("payment migration collision fixture did not change its source")
			}
			migrationExec(t, ctx, m.DB, mutated)
			beforeSchema := currencyMigrationSchema(t, ctx, m)
			beforeHistory := migrationHistory(t, ctx, m)
			if err := m.Migrate(ctx); err == nil {
				t.Fatal("accepted colliding payment table")
			}
			if after := migrationHistory(t, ctx, m); !reflect.DeepEqual(beforeHistory, after) {
				t.Fatal("rejected payment migration changed history", beforeHistory, after)
			}
			if after := currencyMigrationSchema(t, ctx, m); !reflect.DeepEqual(beforeSchema, after) {
				t.Fatal("rejected payment migration changed existing schema")
			}
		})
	}
	t.Run("second_table_collision_does_not_create_first", func(t *testing.T) {
		m, ctx := migrationTestDatabase(t)
		migrationHistoricalPrefix(t, ctx, m, 7)
		malformed := strings.Replace(statements[1], ",\n CONSTRAINT payment_settlements_attempt FOREIGN KEY(attempt_id) REFERENCES payment_attempts(id)", "", 1)
		if malformed == statements[1] {
			t.Fatal("fixture did not remove dependency")
		}
		migrationExec(t, ctx, m.DB, malformed)
		before := currencyMigrationSchema(t, ctx, m)
		if e := m.Migrate(ctx); e == nil {
			t.Fatal("accepted unrelated settlement table")
		}
		if after := currencyMigrationSchema(t, ctx, m); !reflect.DeepEqual(before, after) {
			t.Fatal("rejected migration created earlier table")
		}
	})
}

func TestMySQLPreflightRejectsPaymentSchemaChanges(t *testing.T) {
	for name, mutation := range map[string]string{
		"missing_event_uniqueness":    "ALTER TABLE payment_settlements DROP INDEX payment_settlements_event",
		"changed_request_collation":   "ALTER TABLE payment_attempts MODIFY request_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_general_ci NOT NULL",
		"missing_amount_check":        "ALTER TABLE payment_attempts DROP CHECK payment_attempts_positive_minor",
		"missing_attempt_foreign_key": "ALTER TABLE payment_settlements DROP FOREIGN KEY payment_settlements_attempt",
		"deletion_cascade":            "ALTER TABLE payment_settlements DROP FOREIGN KEY payment_settlements_attempt, ADD CONSTRAINT payment_settlements_attempt_cascade FOREIGN KEY(attempt_id) REFERENCES payment_attempts(id) ON DELETE CASCADE",
	} {
		t.Run(name, func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			if err := m.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			migrationExec(t, ctx, m.DB, mutation)
			if status, err := m.SchemaStatus(ctx); err == nil || status.Status != "incompatible" {
				t.Fatal("preflight accepted changed payment schema", status, err)
			}
		})
	}
	t.Run("unrecorded_payment_table", func(t *testing.T) {
		m, ctx := migrationTestDatabase(t)
		migrationHistoricalPrefix(t, ctx, m, 7)
		migrationExec(t, ctx, m.DB, migrationStatements(schemaVersions[7])[0])
		before := migrationHistory(t, ctx, m)
		if status, err := m.SchemaStatus(ctx); err == nil || status.Status != "incompatible" {
			t.Fatal("preflight accepted an unrecorded partial payment migration", status, err)
		}
		if after := migrationHistory(t, ctx, m); !reflect.DeepEqual(before, after) {
			t.Fatal("preflight changed history for an unrecorded payment table")
		}
	})
}
