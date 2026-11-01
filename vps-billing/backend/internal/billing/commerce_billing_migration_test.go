package billing

import (
	"aster/billing/migrations"
	"fmt"
	"testing"
	"time"
)

func TestMySQLCommerceBillingMigrationPreservesPaidHistoryAndResumes(t *testing.T) {
	for _, cut := range []int{0, 8, 18, 22} {
		t.Run(fmt.Sprint(cut), func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			prefix := 0
			for i, s := range schemaVersions {
				if s == migrations.CommerceBilling {
					prefix = i
					break
				}
			}
			if prefix == 0 {
				t.Fatal("missing billing migration")
			}
			migrationHistoricalPrefix(t, ctx, m, prefix)
			uid, oid, iid := ID(), ID(), ID()
			at := time.Date(2025, 1, 31, 12, 0, 0, 0, time.UTC)
			migrationExec(t, ctx, m.DB, "INSERT INTO users(id,email,name,role,password_hash) VALUES(?,'history@billing.test','Historical customer','customer','unused')", uid)
			migrationExec(t, ctx, m.DB, "INSERT INTO orders(id,number,user_id,plan_id,plan_name,hostname,status,total_minor,currency,created_at) VALUES(?,'HIST-ORDER',?,'starter','Historical plan','old.example','paid',3900,'CNY',?)", oid, uid, at)
			migrationExec(t, ctx, m.DB, "INSERT INTO invoices(id,number,user_id,order_id,status,amount_minor,currency,issued_at,due_at,paid_at) VALUES(?,'HIST-INVOICE',?,?,'paid',3900,'CNY',?,?,?)", iid, uid, oid, at, at.Add(24*time.Hour), at)
			for _, statement := range migrationStatements(migrations.CommerceBilling)[:cut] {
				migrationExec(t, ctx, m.DB, statement)
			}
			if err := m.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			if err := m.Migrate(ctx); err != nil {
				t.Fatal("repeat", err)
			}
			var status, kind string
			var total, subtotal, paid, version int64
			if err := m.DB.QueryRowContext(ctx, "SELECT status,kind,amount_minor,subtotal_minor,amount_paid_minor,commerce_version FROM invoices WHERE id=?", iid).Scan(&status, &kind, &total, &subtotal, &paid, &version); err != nil || status != "paid" || kind != "order" || total != 3900 || subtotal != 3900 || paid != 3900 || version != 0 {
				t.Fatal(status, kind, total, subtotal, paid, version, err)
			}
			for _, table := range []string{"invoice_lines", "order_items"} {
				var n int
				if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil || n != 1 {
					t.Fatal(table, n, err)
				}
			}
			var services int
			if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM services").Scan(&services); err != nil || services != 0 {
				t.Fatal("migration invented services", services, err)
			}
			if v, err := m.SchemaStatus(ctx); err != nil || v.Status != "current" {
				t.Fatal(v, err)
			}
		})
	}
}

func TestMySQLCommerceBillingRejectsCollisionBeforeChanges(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	prefix := 0
	for i, s := range schemaVersions {
		if s == migrations.CommerceBilling {
			prefix = i
			break
		}
	}
	migrationHistoricalPrefix(t, ctx, m, prefix)
	migrationExec(t, ctx, m.DB, "CREATE TABLE invoice_payments(id INT PRIMARY KEY) ENGINE=InnoDB")
	if err := m.Migrate(ctx); err == nil {
		t.Fatal("incompatible payment table adopted")
	}
	var nullable string
	if err := m.DB.QueryRowContext(ctx, "SELECT IS_NULLABLE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='orders' AND COLUMN_NAME='plan_id'").Scan(&nullable); err != nil || nullable != "NO" {
		t.Fatal("changed schema before collision validation", nullable, err)
	}
}
