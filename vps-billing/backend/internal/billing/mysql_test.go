package billing

import (
	"context"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Uses a dedicated disposable database whose name contains _test. Never point at a real database.
func TestMySQLTransactionalOrders(t *testing.T) {
	dsn := os.Getenv("ASTER_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("ASTER_MYSQL_TEST_DSN not set; real MySQL integration not run")
	}
	cfg, e := mysql.ParseDSN(dsn)
	if e != nil || !strings.Contains(cfg.DBName, "_test") {
		t.Fatal("integration database name must contain _test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	m, e := OpenMySQL(ctx, dsn, true)
	if e != nil {
		t.Fatal(e)
	}
	defer m.DB.Close()
	if e = m.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = m.Migrate(ctx); e != nil {
		t.Fatal("repeat migration", e)
	}
	if e = m.Ping(ctx); e != nil {
		t.Fatal(e)
	}
	u := User{ID: ID(), Email: ID() + "@integration.invalid", Name: "Integration", Role: "customer", PasswordHash: "unused"}
	if e = m.AddUser(ctx, u); e != nil {
		t.Fatal(e)
	}
	p := Catalog()[0]
	p.ID = "test-" + ID()
	// Use a real migrated classification; current schema does not permit
	// creating an unbound product through legacy fixture SQL.
	if e = m.DB.QueryRowContext(ctx, "SELECT type_id,region_id,node_id,region FROM plans WHERE id='starter'").Scan(&p.TypeID, &p.RegionID, &p.NodeID, &p.Region); e != nil {
		t.Fatal(e)
	}
	_, e = m.DB.ExecContext(ctx, "INSERT INTO plans(id,name,description,cpu,memory_gb,disk_gb,transfer_tb,region,price_minor,currency,available,type_id,region_id,node_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)", p.ID, p.Name, p.Description, p.CPU, p.MemoryGB, p.DiskGB, p.TransferTB, p.Region, p.PriceMinor, p.Currency, true, p.TypeID, p.RegionID, p.NodeID)
	if e != nil {
		t.Fatal(e)
	}
	_, e = m.DB.ExecContext(ctx, "INSERT INTO plan_prices(plan_id,currency,amount_minor,available) VALUES(?,'CNY',3900,TRUE)", p.ID)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, q := range []string{"DELETE FROM provisioning_jobs WHERE order_id IN(SELECT id FROM orders WHERE user_id=?)", "DELETE FROM order_product_configurations WHERE order_id IN(SELECT id FROM orders WHERE user_id=?)", "DELETE FROM outbox_events WHERE aggregate_id IN(SELECT id FROM orders WHERE user_id=?)", "DELETE FROM order_requests WHERE user_id=?", "DELETE FROM audit_events WHERE user_id=?", "DELETE FROM invoices WHERE user_id=?", "DELETE FROM orders WHERE user_id=?", "DELETE FROM users WHERE id=?"} {
			if _, e := m.DB.ExecContext(c, q, u.ID); e != nil {
				t.Error("fixture cleanup", e)
			}
		}
		for _, q := range []string{"DELETE FROM product_configurations WHERE plan_id=?", "DELETE FROM plan_prices WHERE plan_id=?", "DELETE FROM plans WHERE id=?"} {
			if _, err := m.DB.ExecContext(c, q, p.ID); err != nil {
				t.Error("plan fixture cleanup", err)
			}
		}
	}()
	var wg sync.WaitGroup
	ids := make(chan string, 16)
	for n := 0; n < 16; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, _, e := m.CreateOrder(ctx, u, p.ID, "web-1", "same-key", "CNY")
			if e != nil {
				t.Error(e)
				return
			}
			ids <- o.ID
		}()
	}
	wg.Wait()
	close(ids)
	id := ""
	for v := range ids {
		if id == "" {
			id = v
		}
		if v != id {
			t.Fatal("duplicate order")
		}
	}
	if id == "" {
		t.Fatal("no order created")
	}
	for _, table := range []string{"orders", "invoices", "audit_events", "order_requests"} {
		var n int
		e = m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE user_id=?", u.ID).Scan(&n)
		if e != nil || n != 1 {
			t.Fatal(table, n, e)
		}
	}
	var n int
	e = m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM outbox_events WHERE aggregate_id=?", id).Scan(&n)
	if e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if _, _, e = m.CreateOrder(ctx, u, p.ID, "web-2", "same-key", "CNY"); e != ErrConflict {
		t.Fatal(e)
	}
	if _, _, e = m.CreateOrder(ctx, u, "missing-plan", "web-1", "unavailable-key", "CNY"); e != ErrUnavailable {
		t.Fatal(e)
	}
	e = m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM order_requests WHERE user_id=? AND request_key='unavailable-key'", u.ID).Scan(&n)
	if e != nil || n != 0 {
		t.Fatal("rolled back reservation", n, e)
	}
	is, e := m.Invoices(ctx, u.ID)
	if e != nil || len(is) != 1 || is[0].AmountMinor != 3900 {
		t.Fatal(fmt.Sprint(is), e)
	}
	if _, e = m.Invoice(ctx, ID(), is[0].ID); e != ErrNotFound {
		t.Fatal("owner filter", e)
	}
	summary, e := m.Summary(ctx, u.ID, "CNY")
	if e != nil || summary.PendingInvoices != 1 || summary.Revenue != 0 {
		t.Fatal(summary, e)
	}
	admin := User{ID: ID(), Email: ID() + "@integration.invalid", Name: "Settings admin", Role: "admin", PasswordHash: "unused"}
	if e = m.AddUser(ctx, admin); e != nil {
		t.Fatal(e)
	}
	original, e := m.Settings(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		current, e := m.Settings(ctx)
		if e == nil {
			original.Revision = current.Revision
			_, e = m.UpdateSettings(ctx, admin, original)
		}
		if e != nil {
			t.Error("settings restore", e)
		}
		for _, q := range []string{"DELETE FROM audit_events WHERE user_id=?", "DELETE FROM users WHERE id=?"} {
			if _, err := m.DB.ExecContext(ctx, q, admin.ID); err != nil {
				t.Error("administrator fixture cleanup", err)
			}
		}
	}()
	changed := original
	changed.InvoiceIssuer = "测试开票公司"
	updated, e := m.UpdateSettings(ctx, admin, changed)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.UpdateSettings(ctx, admin, changed); e != ErrSettingsConflict {
		t.Fatal("persisted revision conflict", e)
	}
	order, _, e := m.CreateOrder(ctx, u, p.ID, "issuer-test", "issuer-key", "CNY")
	if e != nil {
		t.Fatal(e)
	}
	updated.InvoiceIssuer = "后来公司"
	if _, e = m.UpdateSettings(ctx, admin, updated); e != nil {
		t.Fatal(e)
	}
	invoices, e := m.Invoices(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, invoice := range invoices {
		if invoice.OrderID == order.ID {
			found = true
			if invoice.Issuer == nil || invoice.Issuer.Name != "测试开票公司" {
				t.Fatal("mutable SQL invoice identity", invoice)
			}
		}
	}
	if !found {
		t.Fatal("missing snapshot invoice")
	}
	if invoice, e := m.Invoice(ctx, "", is[0].ID); e != nil || invoice.ID != is[0].ID {
		t.Fatal("admin direct detail", invoice, e)
	}
}
