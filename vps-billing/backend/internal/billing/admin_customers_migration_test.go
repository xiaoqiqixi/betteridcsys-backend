package billing

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMySQLCustomerAccountMigrationPreservesHistoricalUsersAndSessions(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	migrationHistoricalPrefix(t, ctx, m, 9)
	id := ID()
	hash := "historical-password-hash"
	token := ID() + ID()
	when := time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC)
	migrationExec(t, ctx, m.DB, "INSERT INTO users(id,email,name,role,password_hash,created_at) VALUES(?,?,'Historical','admin',?,?)", id, "historic@fixture.local", hash, when)
	migrationExec(t, ctx, m.DB, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES(?,?,?)", token, id, when.AddDate(1, 0, 0))
	migrationExec(t, ctx, m.DB, "INSERT INTO audit_events(id,user_id,action,target,actor_email,detail,created_at) VALUES(?,?,'legacy.action',?,'historic@fixture.local','immutable historical detail',?)", ID(), id, id, when)
	beforeHistory := migrationHistory(t, ctx, m)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	u, err := m.UserByID(ctx, id)
	if err != nil || u.ID != id || u.Email != "historic@fixture.local" || u.Name != "Historical" || u.Role != "admin" || u.PasswordHash != hash || u.Disabled || u.Revision != 1 {
		t.Fatal("historical user rewritten", u, err)
	}
	var storedWhen time.Time
	if err = m.DB.QueryRowContext(ctx, "SELECT created_at FROM users WHERE id=?", id).Scan(&storedWhen); err != nil || !storedWhen.Equal(when) {
		t.Fatal(storedWhen, err)
	}
	session, err := m.Session(ctx, token)
	if err != nil || session.UserID != id || !session.Expires.Equal(when.AddDate(1, 0, 0)) {
		t.Fatal("historical session rewritten", session, err)
	}
	audits, err := m.Audits(ctx, id)
	if err != nil || len(audits) != 1 || audits[0].Detail != "immutable historical detail" || !audits[0].CreatedAt.Equal(when) {
		t.Fatal(audits, err)
	}
	afterHistory := migrationHistory(t, ctx, m)
	if len(afterHistory) != len(schemaVersions) || !reflect.DeepEqual(beforeHistory, afterHistory[:9]) {
		t.Fatal("historical migration checksums changed")
	}
	if err = m.Migrate(ctx); err != nil {
		t.Fatal("repeat", err)
	}
	if !reflect.DeepEqual(afterHistory, migrationHistory(t, ctx, m)) {
		t.Fatal("repeat changed history")
	}
	for _, update := range []string{"disabled=2", "revision=0", "revision=9007199254740992"} {
		if _, err = m.DB.ExecContext(ctx, "UPDATE users SET "+update+" WHERE id=?", id); err == nil {
			t.Fatal("unsafe authentication metadata accepted", update)
		}
	}
	status, err := m.SchemaStatus(ctx)
	if err != nil || status.Status != "current" || status.SchemaVersion != len(schemaVersions) {
		t.Fatal(status, err)
	}
}
func TestMySQLCustomerAccountMigrationRejectsCollisionsBeforeAnyDDL(t *testing.T) {
	for _, test := range []struct {
		name, ddl string
		columns   int
	}{
		{"disabled_nullable", "ALTER TABLE users ADD COLUMN disabled BOOLEAN NULL DEFAULT FALSE", 1},
		{"disabled_wrong_default", "ALTER TABLE users ADD COLUMN disabled BOOLEAN NOT NULL DEFAULT TRUE", 1},
		{"revision_unsigned", "ALTER TABLE users ADD COLUMN revision BIGINT UNSIGNED NOT NULL DEFAULT 1", 1},
		{"revision_wrong_default", "ALTER TABLE users ADD COLUMN revision BIGINT NOT NULL DEFAULT 2", 1},
		{"late_constraint_wrong_expression", "ALTER TABLE users ADD CONSTRAINT users_revision_max CHECK(id <> 'bad')", 0},
		{"late_constraint_unenforced", "ALTER TABLE users ADD CONSTRAINT users_revision_max CHECK(id <> 'bad') NOT ENFORCED", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			migrationHistoricalPrefix(t, ctx, m, 9)
			migrationExec(t, ctx, m.DB, test.ddl)
			before := migrationHistory(t, ctx, m)
			if err := m.Migrate(ctx); err == nil {
				t.Fatal("colliding authentication object accepted")
			}
			var columns int
			if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='users' AND COLUMN_NAME IN ('disabled','revision')").Scan(&columns); err != nil || columns != test.columns {
				t.Fatal("migration partially added earlier objects", columns, err)
			}
			if !reflect.DeepEqual(before, migrationHistory(t, ctx, m)) {
				t.Fatal("collision recorded migration success")
			}
		})
	}
}
func TestMySQLCustomerAccountPreflightChecksPhysicalMetadata(t *testing.T) {
	for _, test := range []struct {
		name string
		ddl  []string
	}{
		{"missing_check", []string{"ALTER TABLE users DROP CHECK users_revision_max"}},
		{"wrong_default", []string{"ALTER TABLE users ALTER COLUMN revision SET DEFAULT 2"}},
		{"unenforced", []string{"ALTER TABLE users ALTER CHECK users_disabled_boolean NOT ENFORCED"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			if err := m.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			for _, ddl := range test.ddl {
				migrationExec(t, ctx, m.DB, ddl)
			}
			status, err := m.SchemaStatus(ctx)
			if err == nil || status.Status != "incompatible" || status.SchemaVersion != len(schemaVersions) {
				t.Fatal("history row hid physical authentication schema damage", status, err)
			}
		})
	}
}
func TestCustomerAccountMigrationSourceContract(t *testing.T) {
	if len(schemaVersions) < 10 {
		t.Fatal("account migration not registered")
	}
	statements := migrationStatements(schemaVersions[9])
	if len(statements) != 5 {
		t.Fatal("unexpected migration shape", statements)
	}
	for _, s := range statements {
		if !strings.HasPrefix(s, "ALTER TABLE users ADD ") {
			t.Fatal("account migration touches other business tables", s)
		}
	}
}
