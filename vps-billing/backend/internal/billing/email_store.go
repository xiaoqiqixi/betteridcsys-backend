package billing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func (m *Memory) EmailSettings(context.Context) (EmailSettingsRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !validEmailRecord(m.emailSettings) {
		return EmailSettingsRecord{}, ErrEmailSettings
	}
	return m.emailSettings, nil
}
func (m *Memory) UpdateEmailSettings(_ context.Context, actor User, next EmailSettingsRecord) (EmailSettingsRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if actor.Role != "admin" || !validEmailRecord(next) {
		return EmailSettingsRecord{}, ErrEmailSettings
	}
	if next.Revision != m.emailSettings.Revision || next.Revision == MaxMinor {
		return EmailSettingsRecord{}, ErrSettingsConflict
	}
	next.Revision++
	next.UpdatedAt = time.Now().UTC()
	m.emailSettings = next
	m.audits = append(m.audits, emailAudit(actor, next.Revision, "settings_updated"))
	return next, nil
}
func (m *Memory) RecordEmailTest(_ context.Context, actor User, revision int64, outcome string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if actor.Role != "admin" || revision < 1 || revision > MaxMinor || !validEmailOutcome(outcome) {
		return ErrEmailSettings
	}
	if outcome == "test_requested" {
		if revision != m.emailSettings.Revision {
			return ErrSettingsConflict
		}
		if !m.emailSettings.Enabled {
			return ErrSMTPDisabled
		}
	}
	m.audits = append(m.audits, emailAudit(actor, revision, outcome))
	return nil
}

func (m *MySQL) EmailSettings(ctx context.Context) (EmailSettingsRecord, error) {
	var raw []byte
	if err := m.DB.QueryRowContext(ctx, "SELECT document FROM email_settings WHERE id=1").Scan(&raw); err != nil {
		return EmailSettingsRecord{}, err
	}
	return decodeEmailRecord(raw)
}
func insertEmailAudit(ctx context.Context, tx *sql.Tx, a Audit) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) VALUES(?,?,?,?,?,?,?)", a.ID, a.UserID, a.Action, a.Target, a.CreatedAt, a.ActorEmail, a.Detail)
	return err
}
func (m *MySQL) UpdateEmailSettings(ctx context.Context, actor User, next EmailSettingsRecord) (EmailSettingsRecord, error) {
	if actor.Role != "admin" || !validEmailRecord(next) {
		return EmailSettingsRecord{}, ErrEmailSettings
	}
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return EmailSettingsRecord{}, err
	}
	defer tx.Rollback()
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT document FROM email_settings WHERE id=1 FOR UPDATE").Scan(&raw); err != nil {
		return EmailSettingsRecord{}, err
	}
	old, err := decodeEmailRecord(raw)
	if err != nil {
		return EmailSettingsRecord{}, err
	}
	if old.Revision != next.Revision || next.Revision == MaxMinor {
		return EmailSettingsRecord{}, ErrSettingsConflict
	}
	next.Revision++
	next.UpdatedAt = time.Now().UTC()
	raw, err = json.Marshal(next)
	if err != nil {
		return EmailSettingsRecord{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE email_settings SET document=? WHERE id=1", raw); err != nil {
		return EmailSettingsRecord{}, err
	}
	if err = insertEmailAudit(ctx, tx, emailAudit(actor, next.Revision, "settings_updated")); err != nil {
		return EmailSettingsRecord{}, err
	}
	if err = tx.Commit(); err != nil {
		return EmailSettingsRecord{}, err
	}
	return next, nil
}
func (m *MySQL) RecordEmailTest(ctx context.Context, actor User, revision int64, outcome string) error {
	if actor.Role != "admin" || revision < 1 || revision > MaxMinor || !validEmailOutcome(outcome) {
		return ErrEmailSettings
	}
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if outcome == "test_requested" {
		var raw []byte
		if err = tx.QueryRowContext(ctx, "SELECT document FROM email_settings WHERE id=1 FOR UPDATE").Scan(&raw); err != nil {
			return err
		}
		record, e := decodeEmailRecord(raw)
		if e != nil {
			return e
		}
		if record.Revision != revision {
			return ErrSettingsConflict
		}
		if !record.Enabled {
			return ErrSMTPDisabled
		}
	}
	if err = insertEmailAudit(ctx, tx, emailAudit(actor, revision, outcome)); err != nil {
		return err
	}
	return tx.Commit()
}

// Migration 6 can resume after CREATE TABLE, but never adopts a differently
// shaped pre-existing table or malformed singleton document as a valid schema.
func initializeEmailSettings(ctx context.Context, conn *sql.Conn) error {
	var engine string
	if err := conn.QueryRowContext(ctx, "SELECT ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='email_settings'").Scan(&engine); err != nil || engine != "InnoDB" {
		return errors.New("invalid email settings table")
	}
	rows, err := conn.QueryContext(ctx, "SELECT COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE,COLUMN_KEY,EXTRA FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='email_settings' ORDER BY ORDINAL_POSITION")
	if err != nil {
		return err
	}
	n := 0
	for rows.Next() {
		var name, typ, nullable, key, extra string
		if err = rows.Scan(&name, &typ, &nullable, &key, &extra); err != nil {
			rows.Close()
			return err
		}
		if n == 0 && (name != "id" || typ != "tinyint" || nullable != "NO" || key != "PRI" || extra != "") {
			rows.Close()
			return errors.New("invalid email settings id")
		}
		if n == 1 && (name != "document" || typ != "json" || nullable != "NO" || key != "" || extra != "") {
			rows.Close()
			return errors.New("invalid email settings document")
		}
		n++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if n != 2 {
		return errors.New("invalid email settings columns")
	}
	var clause, enforced string
	if err = conn.QueryRowContext(ctx, "SELECT cc.CHECK_CLAUSE,tc.ENFORCED FROM information_schema.TABLE_CONSTRAINTS tc JOIN information_schema.CHECK_CONSTRAINTS cc ON cc.CONSTRAINT_SCHEMA=tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME=tc.CONSTRAINT_NAME WHERE tc.TABLE_SCHEMA=DATABASE() AND tc.TABLE_NAME='email_settings' AND tc.CONSTRAINT_NAME='email_settings_singleton'").Scan(&clause, &enforced); err != nil {
		return errors.New("missing email settings singleton check")
	}
	actual, ok := normalizeMigrationCheck(clause)
	expected, _ := normalizeMigrationCheck("id = 1")
	if !ok || actual != expected || enforced != "YES" {
		return errors.New("invalid email settings singleton check")
	}
	raw, _ := json.Marshal(DefaultEmailSettings())
	if _, err = conn.ExecContext(ctx, "INSERT IGNORE INTO email_settings(id,document) VALUES(1,?)", raw); err != nil {
		return err
	}
	if err = conn.QueryRowContext(ctx, "SELECT document FROM email_settings WHERE id=1").Scan(&raw); err != nil {
		return err
	}
	_, err = decodeEmailRecord(raw)
	return err
}
