package billing

import (
	"aster/billing/migrations"
	"fmt"
	"testing"
	"time"
)

func TestMySQLCommerceServicesMigrationResumesWithoutInventingResources(t *testing.T) {
	for _, cut := range []int{0, 12, 27, 30} {
		t.Run(fmt.Sprint(cut), func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			prefix := 0
			for i, s := range schemaVersions {
				if s == migrations.CommerceServices {
					prefix = i
					break
				}
			}
			if prefix == 0 {
				t.Fatal("missing service migration")
			}
			migrationHistoricalPrefix(t, ctx, m, prefix)
			uid, sid := ID(), ID()
			due := time.Date(2025, 1, 31, 12, 0, 0, 0, time.UTC)
			migrationExec(t, ctx, m.DB, "INSERT INTO users(id,email,name,role,password_hash) VALUES(?,'service-history@example.test','Historical customer','customer','unused')", uid)
			migrationExec(t, ctx, m.DB, "INSERT INTO services(id,user_id,name,plan_name,region,status,next_due_at,price_minor,currency,cpu,memory_gb,disk_gb) VALUES(?,?,'old.example','Historical plan','Old region','active',?,3900,'CNY',1,1,20)", sid, uid, due)
			for _, statement := range migrationStatements(migrations.CommerceServices)[:cut] {
				migrationExec(t, ctx, m.DB, statement)
			}
			for range 2 {
				if err := m.Migrate(ctx); err != nil {
					t.Fatal(err)
				}
			}
			var status string
			var price, anchor int64
			var resource *string
			if err := m.DB.QueryRowContext(ctx, "SELECT status,recurring_minor,anchor_day,resource_id FROM services WHERE id=?", sid).Scan(&status, &price, &anchor, &resource); err != nil || status != "active" || price != 3900 || anchor != 31 || resource != nil {
				t.Fatal(status, price, anchor, resource, err)
			}
			for _, table := range []string{"service_units", "service_tasks", "invoice_service_changes", "invoice_notifications"} {
				var n int
				if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil || n != 0 {
					t.Fatal(table, n, err)
				}
			}
			if v, err := m.SchemaStatus(ctx); err != nil || v.Status != "current" {
				t.Fatal(v, err)
			}
		})
	}
}
func TestMySQLCommerceServicesMigrationRejectsCollisionBeforeDDL(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	prefix := 0
	for i, s := range schemaVersions {
		if s == migrations.CommerceServices {
			prefix = i
			break
		}
	}
	migrationHistoricalPrefix(t, ctx, m, prefix)
	migrationExec(t, ctx, m.DB, "CREATE TABLE service_tasks(id INT PRIMARY KEY) ENGINE=InnoDB")
	if err := m.Migrate(ctx); err == nil {
		t.Fatal("accepted incompatible task table")
	}
	var n int
	if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='services' AND COLUMN_NAME='product_id'").Scan(&n); err != nil || n != 0 {
		t.Fatal("changed service before preflight", n, err)
	}
}

func TestMySQLCommerceServiceContractBoundsMigrationResumes(t *testing.T) {
	for _, applied := range []bool{false, true} {
		t.Run(fmt.Sprint(applied), func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			prefix := 0
			for i, source := range schemaVersions {
				if source == migrations.ServiceContractBounds {
					prefix = i
					break
				}
			}
			if prefix == 0 {
				t.Fatal("missing contract bounds migration")
			}
			migrationHistoricalPrefix(t, ctx, m, prefix)
			if applied {
				for _, statement := range migrationStatements(migrations.ServiceContractBounds) {
					migrationExec(t, ctx, m.DB, statement)
				}
			}
			for range 2 {
				if err := m.Migrate(ctx); err != nil {
					t.Fatal(err)
				}
			}
			var length int
			if err := m.DB.QueryRowContext(ctx, "SELECT CHARACTER_MAXIMUM_LENGTH FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='services' AND COLUMN_NAME='cancellation_reason'").Scan(&length); err != nil || length != 1000 {
				t.Fatal(length, err)
			}
			if status, err := m.SchemaStatus(ctx); err != nil || status.Status != "current" {
				t.Fatal(status, err)
			}
		})
	}
}
