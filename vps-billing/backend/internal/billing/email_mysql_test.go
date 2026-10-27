package billing

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestMySQLEmailEncryptedPersistenceAndAuditRollback(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	actor := User{ID: ID(), Email: "smtp-admin@example.invalid", Role: "admin", Name: "SMTP test", PasswordHash: "unused"}
	if err := m.AddUser(ctx, actor); err != nil {
		t.Fatal(err)
	}
	service := newSMTPService(m, emailTestCipher(t))
	input := emailTestUpdate()
	view, err := service.Update(ctx, actor, input)
	if err != nil || view.Revision != 2 {
		t.Fatal(view, err)
	}
	var raw []byte
	if err = m.DB.QueryRowContext(ctx, "SELECT document FROM email_settings WHERE id=1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{input.Username, input.Password} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("plaintext in persistent config")
		}
	}
	stored, err := m.EmailSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := service.credentials(stored)
	if err != nil || decrypted.Username != input.Username || decrypted.Password != input.Password {
		t.Fatal("encrypted credentials cannot restore", err)
	}
	if _, err = service.Update(ctx, actor, input); !errors.Is(err, ErrSettingsConflict) {
		t.Fatal("SQL revision conflict", err)
	}
	input.Revision = 2
	input.Password = "replacement-synthetic-secret"
	input.Username = "replacement-synthetic-account"
	invalidActor := actor
	invalidActor.ID = ID()
	if _, err = service.Update(ctx, invalidActor, input); err == nil {
		t.Fatal("invalid audit actor should roll back")
	}
	after, err := m.EmailSettings(ctx)
	if err != nil || after.Revision != 2 || after.CredentialsCiphertext != stored.CredentialsCiphertext {
		t.Fatal("audit failure committed settings", err)
	}
	if err = m.RecordEmailTest(ctx, actor, 1, "test_requested"); err != ErrSettingsConflict {
		t.Fatal("stale test audit", err)
	}
	if err = m.RecordEmailTest(ctx, actor, 2, "test_requested"); err != nil {
		t.Fatal(err)
	}
	if err = m.RecordEmailTest(ctx, actor, 2, "test_accepted"); err != nil {
		t.Fatal(err)
	}
	rows, err := m.Audits(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	auditRaw, _ := json.Marshal(rows)
	for _, secret := range []string{"synthetic-account", "synthetic-password", stored.CredentialsCiphertext, "sender@example.invalid"} {
		if bytes.Contains(auditRaw, []byte(secret)) {
			t.Fatal("secret in audit")
		}
	}
	if err = m.Migrate(ctx); err != nil {
		t.Fatal("repeat changed SMTP migration", err)
	}
	again, _ := m.EmailSettings(ctx)
	if again != stored {
		t.Fatal("repeat migration rewrote stored config")
	}
}

func TestMySQLEmailMigrationRejectsUnexpectedState(t *testing.T) {
	for _, test := range []struct{ name, sql string }{
		{"wrong_column", "CREATE TABLE email_settings(id INT PRIMARY KEY,document JSON NOT NULL,CONSTRAINT email_settings_singleton CHECK(id=1)) ENGINE=InnoDB"},
		{"missing_check", "CREATE TABLE email_settings(id TINYINT PRIMARY KEY,document JSON NOT NULL) ENGINE=InnoDB"},
		{"unenforced_check", "CREATE TABLE email_settings(id TINYINT PRIMARY KEY,document JSON NOT NULL,CONSTRAINT email_settings_singleton CHECK(id=1) NOT ENFORCED) ENGINE=InnoDB"},
		{"invalid_document", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			migrationHistoricalPrefix(t, ctx, m, 5)
			if test.sql != "" {
				migrationExec(t, ctx, m.DB, test.sql)
			} else {
				migrationExec(t, ctx, m.DB, strings.TrimSuffix(strings.TrimSpace(schemaVersions[5]), ";"))
				migrationExec(t, ctx, m.DB, "INSERT INTO email_settings(id,document) VALUES(1,JSON_OBJECT('revision',0))")
			}
			if err := m.Migrate(ctx); err == nil {
				t.Fatal("malformed SMTP schema accepted")
			}
			var count int
			if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version=6").Scan(&count); err != nil || count != 0 {
				t.Fatal("failed migration marked applied", err)
			}
			if err := m.Ping(ctx); err == nil {
				t.Fatal("failed migration ready")
			}
		})
	}
}

func TestMySQLEmailConcurrentRevision(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	actor := User{ID: ID(), Email: "smtp-concurrent@example.invalid", Name: "SMTP concurrency", Role: "admin", PasswordHash: "unused"}
	if err := m.AddUser(ctx, actor); err != nil {
		t.Fatal(err)
	}
	service := newSMTPService(m, emailTestCipher(t))
	input := emailTestUpdate()
	var wait sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() { defer wait.Done(); <-start; _, err := service.Update(ctx, actor, input); results <- err }()
	}
	close(start)
	wait.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrSettingsConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("same revision committed more than once", success, conflict)
	}
	stored, err := m.EmailSettings(ctx)
	if err != nil || stored.Revision != 2 {
		t.Fatal(stored.Revision, err)
	}
	var audits int
	if err = m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_events WHERE action='email.settings_updated'").Scan(&audits); err != nil || audits != 1 {
		t.Fatal("audit count", audits, err)
	}
	var tables int
	if err = m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_TYPE='BASE TABLE'").Scan(&tables); err != nil || tables != 23 {
		t.Fatal("expected current schema with23 tables including private product configurations", tables, err)
	}
}
