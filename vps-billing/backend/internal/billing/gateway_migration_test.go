package billing

import (
	"aster/billing/migrations"
	"testing"
)

func TestMySQLGatewayMigrationResumesAndRejectsCollisions(t *testing.T) {
	before := 0
	for i, s := range schemaVersions {
		if s == migrations.PaymentGateways {
			before = i
		}
	}
	if before == 0 {
		t.Fatal("gateway migration missing")
	}
	for _, mode := range []string{"clean", "resume", "collision"} {
		t.Run(mode, func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			migrationHistoricalPrefix(t, ctx, m, before)
			if mode == "resume" {
				migrationExec(t, ctx, m.DB, migrationStatements(migrations.PaymentGateways)[0])
			}
			if mode == "collision" {
				migrationExec(t, ctx, m.DB, "CREATE TABLE payment_gateway_settings(id BIGINT PRIMARY KEY,document JSON NOT NULL) ENGINE=InnoDB")
			}
			err := m.Migrate(ctx)
			if mode == "collision" {
				if err == nil {
					t.Fatal("incompatible gateway table accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = m.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			if err = m.Ping(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}
