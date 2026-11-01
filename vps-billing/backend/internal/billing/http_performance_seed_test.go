package billing

import (
	"aster/billing/internal/staffaccess"
	"errors"
	"testing"
)

func TestMySQLHTTPPerformanceSeedUsesLiveStaffMembershipAndCurrentSchema(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	f := &httpPerfFixture{m: m}
	if err := httpPerfSeed(ctx, f, "aster-http-performance-seed-test", httpPerfParameters{Users: 2, Orders: 4, Services: 2}); err != nil {
		t.Fatal(err)
	}
	policy, err := m.StaffPolicy(ctx, f.adminID)
	if err != nil || !staffaccess.Allows(policy.RoleID, policy.Builtin, policy.Permissions, "overview.read") || !staffaccess.Allows(policy.RoleID, policy.Builtin, policy.Permissions, "orders.read") {
		t.Fatal("performance admin lacks live measured-route permissions", err)
	}
	if _, err := m.StaffPolicy(ctx, f.customer); !errors.Is(err, ErrNotFound) {
		t.Fatal("performance customer received staff authority", err)
	}
	before, err := httpPerfDatasetHash(ctx, m.DB)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Counts) != 67 {
		t.Fatalf("performance fixture has %d tables, want 67", len(before.Counts))
	}
	for name, count := range map[string]int64{"schema_migrations": 26, "staff_roles": 1, "staff_members": 1, "products": 3, "product_prices": 0, "users": 3, "orders": 4, "invoices": 4, "sessions": 2, "account_sessions": 0, "account_settings": 2, "email_outbox": 0, "automation_settings": 1, "automation_runs": 0, "support_departments": 2, "tickets": 0, "ticket_messages": 0, "announcements": 0, "api_tokens": 0, "ssh_keys": 0, "order_items": 0, "invoice_lines": 0, "invoice_payments": 0, "invoice_refunds": 0, "affiliate_commissions": 0, "service_units": 0, "service_tasks": 0, "invoice_service_changes": 0, "invoice_notifications": 0} {
		if before.Counts[name] != count {
			t.Fatalf("performance fixture table %s has %d rows, want %d", name, before.Counts[name], count)
		}
	}
	after, err := httpPerfDatasetHash(ctx, m.DB)
	if err != nil || before.SHA256 != after.SHA256 {
		t.Fatal("performance dataset hash is unstable", err)
	}
}
