package billing

import (
	"aster/billing/migrations"
	"testing"
)

func TestMySQLAccountSecurityMigration(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	prefix := 0
	for i, s := range schemaVersions {
		if s == migrations.AccountSecurity {
			prefix = i
			break
		}
	}
	if prefix == 0 {
		t.Fatal("account migration is not registered")
	}
	migrationHistoricalPrefix(t, ctx, m, prefix)
	id := ID()
	migrationExec(t, ctx, m.DB, "INSERT INTO users(id,email,name,role,password_hash) VALUES(?,'security-history@fixture.local','Historical','customer','unused')", id)
	token := ID() + ID()
	migrationExec(t, ctx, m.DB, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES(?,?,DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 1 HOUR))", token, id)
	before := migrationHistory(t, ctx, m)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Migrate(ctx); err != nil {
		t.Fatal("repeat", err)
	}
	after := migrationHistory(t, ctx, m)
	for k, v := range before {
		if after[k] != v {
			t.Fatal("historical checksum changed")
		}
	}
	var currency string
	var locked bool
	if err := m.DB.QueryRowContext(ctx, "SELECT currency,currency_locked FROM users WHERE id=?", id).Scan(&currency, &locked); err != nil || currency != "CNY" || locked {
		t.Fatal("historical currency", currency, locked, err)
	}
	var count int
	if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_sessions WHERE token_hash=?", token).Scan(&count); err != nil || count != 1 {
		t.Fatal("session backfill", count, err)
	}
	if status, err := m.SchemaStatus(ctx); err != nil || status.Status != "current" {
		t.Fatal("account schema inventory", status, err)
	}
	migrationExec(t, ctx, m.DB, "DELETE FROM sessions WHERE token_hash=?", token)
	if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_sessions WHERE token_hash=?", token).Scan(&count); err != nil || count != 0 {
		t.Fatal("session metadata survived revoke", count, err)
	}
}
func TestMySQLAccountMigrationRejectsCollidingColumn(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	prefix := 0
	for i, s := range schemaVersions {
		if s == migrations.AccountSecurity {
			prefix = i
			break
		}
	}
	migrationHistoricalPrefix(t, ctx, m, prefix)
	migrationExec(t, ctx, m.DB, "ALTER TABLE users ADD COLUMN currency VARCHAR(3) NOT NULL DEFAULT 'CNY'")
	if err := m.Migrate(ctx); err == nil {
		t.Fatal("adopted an incompatible account currency column")
	}
	var n int
	if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='account_profiles'").Scan(&n); err != nil || n != 0 {
		t.Fatal("mutated schema before validating collisions", n, err)
	}
}
