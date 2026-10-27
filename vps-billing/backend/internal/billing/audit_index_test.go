package billing

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func auditTableDefinition(t *testing.T, ctx context.Context, m *MySQL) string {
	t.Helper()
	var name, definition string
	if err := m.DB.QueryRowContext(ctx, "SHOW CREATE TABLE audit_events").Scan(&name, &definition); err != nil {
		t.Fatal(err)
	}
	return definition
}
func auditIndexCount(t *testing.T, ctx context.Context, m *MySQL) int {
	t.Helper()
	var count int
	if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(DISTINCT INDEX_NAME) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='audit_events' AND INDEX_NAME IN ('ix_audit_date_id','ix_audit_actor_date_id','ix_audit_action_date_id')").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
func TestMySQLAuditIndexUpgradePreservesHistoryAndBusiness(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	migrationHistoricalPrefix(t, ctx, m, 8)
	owner, orderID, invoiceID := ID(), ID(), ID()
	migrationExec(t, ctx, m.DB, "INSERT INTO users(id,email,name,role,password_hash) VALUES(?,'audit-upgrade@example.invalid','Historic owner','customer','unused')", owner)
	migrationExec(t, ctx, m.DB, "INSERT INTO orders(id,number,user_id,plan_id,plan_name,hostname,status,total_minor,currency,created_at,plan_snapshot) VALUES(?,'ORD-AUDIT-UPGRADE',?,'starter','Historic plan','historic-host','pending_payment',123456,'USD','2025-01-02 03:04:05.123456',JSON_OBJECT('historic','plan snapshot'))", orderID, owner)
	migrationExec(t, ctx, m.DB, "INSERT INTO invoices(id,number,user_id,order_id,status,amount_minor,currency,issued_at,due_at,issuer_snapshot) VALUES(?,'INV-AUDIT-UPGRADE',?,?,'unpaid',123456,'USD','2025-01-02 03:04:05.123456','2025-02-02 03:04:05.123456',JSON_OBJECT('historic','issuer snapshot'))", invoiceID, owner, orderID)
	migrationExec(t, ctx, m.DB, "INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) VALUES(?,?,'order.created',?,'2025-01-02 03:04:05.123456','Admin@Historical.invalid','不可改写的历史审计')", ID(), owner, orderID)
	business := func() []string {
		var amount, currency, plan, status, invoiceAmount, invoiceCurrency, invoiceStatus, issuer, auditID, auditOwner, auditTime, auditTarget, action, actor, detail string
		err := m.DB.QueryRowContext(ctx, "SELECT CAST(o.total_minor AS CHAR),o.currency,CAST(o.plan_snapshot AS CHAR),o.status,CAST(i.amount_minor AS CHAR),i.currency,i.status,CAST(i.issuer_snapshot AS CHAR),a.id,a.user_id,CAST(a.created_at AS CHAR),a.target,a.action,a.actor_email,a.detail FROM orders o JOIN invoices i ON i.order_id=o.id JOIN audit_events a ON a.target=o.id WHERE o.id=?", orderID).Scan(&amount, &currency, &plan, &status, &invoiceAmount, &invoiceCurrency, &invoiceStatus, &issuer, &auditID, &auditOwner, &auditTime, &auditTarget, &action, &actor, &detail)
		if err != nil {
			t.Fatal(err)
		}
		return []string{amount, currency, plan, status, invoiceAmount, invoiceCurrency, invoiceStatus, issuer, auditID, auditOwner, auditTime, auditTarget, action, actor, detail}
	}
	before, beforeHistory := business(), migrationHistory(t, ctx, m)
	if auditIndexCount(t, ctx, m) != 0 {
		t.Fatal("historical prefix unexpectedly has new indexes")
	}
	if err := m.Migrate(ctx); err != nil {
		t.Fatal("upgrade 8 to 9", err)
	}
	afterHistory := migrationHistory(t, ctx, m)
	if len(afterHistory) != len(schemaVersions) || !reflect.DeepEqual(beforeHistory, afterHistory[:8]) || auditIndexCount(t, ctx, m) != 3 || !reflect.DeepEqual(before, business()) {
		t.Fatal("index-only upgrade changed history or business")
	}
	if err := m.Migrate(ctx); err != nil {
		t.Fatal("repeat upgrade", err)
	}
	if !reflect.DeepEqual(afterHistory, migrationHistory(t, ctx, m)) || !reflect.DeepEqual(before, business()) {
		t.Fatal("repeat upgrade changed history")
	}
}
func TestMySQLAuditIndexInterruptedRecovery(t *testing.T) {
	statements := migrationStatements(schemaVersions[8])
	for boundary := 0; boundary <= len(statements); boundary++ {
		t.Run(string(rune('0'+boundary))+"_indexes", func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			migrationHistoricalPrefix(t, ctx, m, 8)
			for _, statement := range statements[:boundary] {
				migrationExec(t, ctx, m.DB, statement)
			}
			if auditIndexCount(t, ctx, m) != boundary {
				t.Fatal("fixture boundary not installed")
			}
			if err := m.Migrate(ctx); err != nil {
				t.Fatal("resume known partial indexes", err)
			}
			if auditIndexCount(t, ctx, m) != 3 || len(migrationHistory(t, ctx, m)) != len(schemaVersions) {
				t.Fatal("resumed migration incomplete")
			}
			if err := m.Migrate(ctx); err != nil {
				t.Fatal("repeat resumed indexes", err)
			}
		})
	}
}
func TestMySQLAuditIndexRejectsCollisionsBeforeAnyDDL(t *testing.T) {
	for name, statement := range map[string]string{
		"wrong_columns": "CREATE INDEX ix_audit_action_date_id ON audit_events(actor_email,created_at,id)",
		"unique":        "CREATE UNIQUE INDEX ix_audit_action_date_id ON audit_events(action,created_at,id)",
		"prefix":        "CREATE INDEX ix_audit_action_date_id ON audit_events(action(10),created_at,id)",
		"invisible":     "CREATE INDEX ix_audit_action_date_id ON audit_events(action,created_at,id) INVISIBLE",
		"descending":    "CREATE INDEX ix_audit_action_date_id ON audit_events(action,created_at DESC,id)",
		"extra_column":  "CREATE INDEX ix_audit_action_date_id ON audit_events(action,created_at,id,target)",
	} {
		t.Run(name, func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			migrationHistoricalPrefix(t, ctx, m, 8)
			// Collide with the last statement: a one-pass migration would create the
			// earlier indexes before discovering this incompatible named object.
			migrationExec(t, ctx, m.DB, statement)
			before, history := auditTableDefinition(t, ctx, m), migrationHistory(t, ctx, m)
			if err := m.Migrate(ctx); err == nil {
				t.Fatal("accepted conflicting named audit index", name)
			}
			if after := auditTableDefinition(t, ctx, m); after != before {
				t.Fatal("refusal partially created earlier indexes")
			}
			if !reflect.DeepEqual(history, migrationHistory(t, ctx, m)) || auditIndexCount(t, ctx, m) != 1 {
				t.Fatal("refusal changed history or index inventory")
			}
		})
	}
}
func TestMySQLAuditPreflightRejectsMissingOrChangedIndex(t *testing.T) {
	for _, mutation := range []string{
		"DROP INDEX ix_audit_date_id ON audit_events",
		"ALTER TABLE audit_events ALTER INDEX ix_audit_actor_date_id INVISIBLE",
	} {
		t.Run(strings.Fields(mutation)[0], func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			if err := m.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			status, err := m.SchemaStatus(ctx)
			if err != nil || status.Status != "current" || status.SchemaVersion != len(schemaVersions) {
				t.Fatal("unmutated schema not current9", status, err)
			}
			history := migrationHistory(t, ctx, m)
			migrationExec(t, ctx, m.DB, mutation)
			before := auditTableDefinition(t, ctx, m)
			if status, err = m.SchemaStatus(ctx); err == nil || status.Status != "incompatible" || status.SchemaVersion != len(schemaVersions) {
				t.Fatal("claimed version9 masked missing/changed index", status, err)
			}
			if !reflect.DeepEqual(history, migrationHistory(t, ctx, m)) || auditTableDefinition(t, ctx, m) != before {
				t.Fatal("read-only preflight changed schema/history")
			}
		})
	}
}
