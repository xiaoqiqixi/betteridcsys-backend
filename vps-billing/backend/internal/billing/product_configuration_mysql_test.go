package billing

import (
	"errors"
	"strings"
	"testing"
)

func TestMySQLProductConfigurationSnapshotInsertionIsAtomic(t *testing.T) {
	f, pid, config := productConfigurationFixture(t, mysqlCatalogFixture)
	ctx := t.Context()
	m := f.store.(*MySQL)
	if _, err := config.UpdateProductConfiguration(ctx, f.admin, pid, ProductConfigurationInput{Revision: 1, Spec: presetProductSpec(), Reason: "manual"}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"order_product_configurations", "invoices", "outbox_events"} {
		t.Run(table, func(t *testing.T) {
			migrationExec(t, ctx, m.DB, "CREATE TRIGGER product_snapshot_reject BEFORE INSERT ON "+table+" FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='fixture insert refusal'")
			key := "private-snapshot-rollback-" + ID()
			host := "rollback-" + ID()
			if _, _, err := m.CreateOrder(ctx, f.customer, pid, host, key, "CNY"); err == nil {
				t.Fatal("expected injected late failure")
			}
			migrationExec(t, ctx, m.DB, "DROP TRIGGER product_snapshot_reject")
			for _, query := range []string{"SELECT COUNT(*) FROM orders WHERE hostname=?", "SELECT COUNT(*) FROM order_product_configurations c JOIN orders o ON c.order_id=o.id WHERE o.hostname=?"} {
				var n int
				if err := m.DB.QueryRowContext(ctx, query, host).Scan(&n); err != nil || n != 0 {
					t.Fatal("snapshot/order transaction leaked", n, err)
				}
			}
			var n int
			if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM order_requests WHERE user_id=? AND request_key=?", f.customer.ID, key).Scan(&n); err != nil || n != 0 {
				t.Fatal("request reservation leaked", n, err)
			}
			order, _, err := m.CreateOrder(ctx, f.customer, pid, host, key, "CNY")
			if err != nil {
				t.Fatal("rollback retry failed", err)
			}
			snapshot, ok := privateOrderConfiguration(t, f, order.ID)
			if !ok || snapshot.Spec.RemoteProductID != "plan-123" {
				t.Fatal("successful retry missing snapshot", snapshot)
			}
		})
	}
	// Tampering in current private config fails closed and cannot reserve a new key.
	migrationExec(t, ctx, m.DB, "UPDATE product_configurations SET configuration_sha256=REPEAT('a',64) WHERE plan_id=?", pid)
	if _, _, err := m.CreateOrder(ctx, f.customer, pid, "corrupt", "corrupt-private-config", "CNY"); !errors.Is(err, ErrProductConfiguration) {
		t.Fatal("corrupt private config accepted", err)
	}
	var n int
	if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM order_requests WHERE user_id=? AND request_key='corrupt-private-config'", f.customer.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("bad config retained key", n, err)
	}
}

func TestMySQLProductConfigurationCanonicalStoredPlanID(t *testing.T) {
	f := mysqlCatalogFixture(t)
	ctx := t.Context()
	m := f.store.(*MySQL)
	view := readCatalog(t, f)
	if _, err := f.catalog.UpdateCatalogPlan(ctx, f.admin, "starter", catalogTestInput(t, f, view.Revision)); err != nil {
		t.Fatal(err)
	}
	const pid = "starter"
	alias := strings.ToUpper(pid)
	initial, err := m.ProductConfiguration(ctx, f.admin, alias)
	if err != nil || initial.PlanID != pid || initial.Revision != 1 {
		t.Fatal("admin alias GET did not resolve stored ID", initial, err)
	}
	configured, err := m.UpdateProductConfiguration(ctx, f.admin, alias, ProductConfigurationInput{Revision: 1, Spec: presetProductSpec(), Reason: "Configure using administrator alias"})
	if err != nil || configured.PlanID != pid || configured.Revision != 2 {
		t.Fatal("admin alias save did not resolve stored ID", configured, err)
	}
	reread, err := m.ProductConfiguration(ctx, f.admin, pid)
	if err != nil || reread.PlanID != pid || reread.ConfigurationSHA256 != configured.ConfigurationSHA256 || reread.Revision != configured.Revision {
		t.Fatal("canonical lookup lost alias save", reread, err)
	}
	var storedID, auditTarget string
	if err = m.DB.QueryRowContext(ctx, "SELECT plan_id FROM product_configurations WHERE plan_id=?", pid).Scan(&storedID); err != nil || storedID != pid {
		t.Fatal("config persisted request spelling instead of canonical ID", storedID, err)
	}
	if err = m.DB.QueryRowContext(ctx, "SELECT target FROM audit_events WHERE action='product.configuration.updated' AND user_id=? ORDER BY created_at DESC,id DESC LIMIT 1", f.admin.ID).Scan(&auditTarget); err != nil || auditTarget != pid {
		t.Fatal("configuration audit used alias", auditTarget, err)
	}
	_, quote := quotedPlan(t, m, "CNY")
	order, replay, err := m.CreateQuotedOrder(ctx, f.customer, alias, "canonical-plan-alias", "canonical-plan-alias-key", "CNY", &quote)
	if err != nil || replay || order.PlanSnapshot == nil || order.PlanSnapshot.PlanID != pid {
		t.Fatal("alias order lost financial plan identity", order, replay, err)
	}
	snapshot, ok := privateOrderConfiguration(t, f, order.ID)
	if !ok || snapshot.PlanID != pid || snapshot.SHA256 != configured.ConfigurationSHA256 {
		t.Fatal("alias order froze noncanonical private plan", snapshot, ok)
	}
	existing, replay, err := m.CreateQuotedOrder(ctx, f.customer, alias, "canonical-plan-alias", "canonical-plan-alias-key", "CNY", &quote)
	if err != nil || !replay || existing.ID != order.ID {
		t.Fatal("original alias fingerprint replay changed", existing, replay, err)
	}
	if _, _, err = m.CreateQuotedOrder(ctx, f.customer, pid, "canonical-plan-alias", "canonical-plan-alias-key", "CNY", &quote); !errors.Is(err, ErrConflict) {
		t.Fatal("legacy raw plan fingerprint was normalized", err)
	}
	invoices, err := m.Invoices(ctx, f.customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	var invoice Invoice
	for _, v := range invoices {
		if v.OrderID == order.ID {
			invoice = v
			break
		}
	}
	attempt, _, err := m.ReservePayment(ctx, f.customer, invoice.ID, "canonical-plan-alias-payment", "payment.synthetic", "merchant-synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.SettlePayment(ctx, attempt.ID, syntheticPayment(attempt)); err != nil {
		t.Fatal(err)
	}
	job, replay, err := m.CreateProvisioningJob(ctx, f.admin, order.ID, "Review canonical alias paid order")
	if err != nil || replay || job.State != "queued" {
		t.Fatal("paid alias order failed safe canonical queue", job, replay, err)
	}
	request, err := m.ProvisioningRequest(ctx, job.ID)
	if err != nil || request.OrderID != order.ID || request.ConfigurationSHA256 != snapshot.SHA256 {
		t.Fatal("canonical alias job request mismatch", request, err)
	}
}
