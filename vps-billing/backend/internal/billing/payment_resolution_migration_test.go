package billing

import (
	"reflect"
	"strings"
	"testing"
)

func TestMySQLPaymentResolutionMigrationResumesEveryDDLBoundary(t *testing.T) {
	statements := migrationStatements(schemaVersions[13])
	for completed := 0; completed <= len(statements); completed++ {
		t.Run(strings.Repeat("applied-", completed)+"resume", func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			migrationHistoricalPrefix(t, ctx, m, 13)
			before := migrationHistory(t, ctx, m)
			for _, q := range statements[:completed] {
				migrationExec(t, ctx, m.DB, q)
			}
			if err := m.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			if err := m.Migrate(ctx); err != nil {
				t.Fatal("repeat", err)
			}
			if !reflect.DeepEqual(before, migrationHistory(t, ctx, m)[:13]) {
				t.Fatal("shipped history changed")
			}
			status, err := m.SchemaStatus(ctx)
			if err != nil || status.Status != "current" || status.SchemaVersion != 14 {
				t.Fatal(status, err)
			}
		})
	}
}
func TestMySQLPaymentResolutionRejectsCollisionsBeforeDDL(t *testing.T) {
	for name, ddl := range map[string]string{
		"invoice flag":        "ALTER TABLE invoices ADD COLUMN settlement_review BOOLEAN NULL DEFAULT FALSE",
		"receipt disposition": "ALTER TABLE payment_settlements ADD COLUMN disposition VARCHAR(30) NOT NULL DEFAULT 'settled'",
		"extra unique":        "CREATE UNIQUE INDEX unexpected_invoice_only ON payment_attempts(invoice_id)",
		"history index":       "CREATE INDEX payment_attempts_invoice_history ON payment_attempts(order_id)",
		"attempt state":       "ALTER TABLE payment_attempts MODIFY state VARCHAR(30) NOT NULL",
	} {
		t.Run(name, func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			migrationHistoricalPrefix(t, ctx, m, 13)
			migrationExec(t, ctx, m.DB, ddl)
			before, history := currencyMigrationSchema(t, ctx, m), migrationHistory(t, ctx, m)
			if err := m.Migrate(ctx); err == nil {
				t.Fatal("collision accepted")
			}
			if !reflect.DeepEqual(before, currencyMigrationSchema(t, ctx, m)) || !reflect.DeepEqual(history, migrationHistory(t, ctx, m)) {
				t.Fatal("prevalidation made partial changes")
			}
		})
	}
}
