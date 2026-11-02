package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

const financePerfSeed = "finance-perf-v1"

// seedFinancePerformance extends the existing HTTP fixture without changing its
// schema, runner, or assertions. It requires a newly migrated, disposable DB.
// All IDs, dates, prices and ownership are deterministic. No provider is called.
func seedFinancePerformance(ctx context.Context, f *httpPerfFixture) error {
	if err := httpPerfSeed(ctx, f, financePerfSeed, httpPerfParameters{Users: 10000}); err != nil {
		return err
	}
	tx, err := f.m.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "CREATE TEMPORARY TABLE finance_perf_numbers(n INT PRIMARY KEY)"); err != nil {
		return err
	}
	defer tx.ExecContext(context.Background(), "DROP TEMPORARY TABLE IF EXISTS finance_perf_numbers")
	for start := 0; start < 50000; start += 500 {
		values, args := make([]string, 500), make([]any, 500)
		for i := range values {
			values[i], args[i] = "(?)", start+i
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO finance_perf_numbers VALUES "+strings.Join(values, ","), args...); err != nil {
			return err
		}
	}
	id := func(label, number string) string {
		return "LEFT(SHA2(CONCAT('" + financePerfSeed + ":" + label + "-'," + number + "),256),32)"
	}
	// One established client has 500 invoices; all other clients have history.
	// This exercises full client pages as well as full staff pages.
	owner := id("user", "IF(MOD(n,100)=0,0,1+MOD(n,9999))")
	order, invoice := id("order", "n"), id("invoice", "n")
	at := "TIMESTAMPADD(SECOND,n,'2025-01-01 00:00:00')"
	format, _ := json.Marshal(DefaultCurrencyFormat("CNY"))
	issuer, _ := json.Marshal(DefaultSettings().Identity())
	queries := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO products(id,slug,kind,node_id,name_i18n,description_i18n,specs,badges,stock,visibility,sort_order,revision)
 SELECT CONCAT('perf-product-',n),CONCAT('perf-product-',n),p.kind,p.node_id,p.name_i18n,p.description_i18n,p.specs,p.badges,NULL,'public',n+10,1
 FROM finance_perf_numbers CROSS JOIN products p WHERE p.id='starter' AND n<197`, nil},
		{`INSERT INTO product_prices(product_id,currency,cycle,recurring_minor,setup_minor,decimal_places)
 SELECT id,'CNY','monthly',3900,0,2 FROM products`, nil},
		{`INSERT INTO orders(id,number,user_id,plan_id,plan_name,hostname,status,total_minor,currency,created_at,paid_at,issuer_snapshot,currency_snapshot,plan_snapshot)
 SELECT ` + order + `,CONCAT('PERF-ORD-',n),` + owner + `,'starter','Performance server',CONCAT('perf-',n,'.invalid'),
 IF(MOD(n,20)<15,'paid',IF(MOD(n,20)=19,'cancelled','pending_payment')),3900,'CNY',` + at + `,IF(MOD(n,20)<15,` + at + `,NULL),?,?,
 JSON_OBJECT('id','starter','name','Performance server','cpu',1,'memory_gb',1,'disk_gb',20,'transfer_tb',1,'region','fixture','cycle','monthly') FROM finance_perf_numbers`, []any{issuer, format}},
		{`INSERT INTO order_items(id,order_id,position,product_id,product_name,cycle,quantity,hostname,options_summary,recurring_minor,total_minor,stock_reserved,snapshot)
 SELECT ` + id("item", "n") + `,` + order + `,0,'starter','Performance server','monthly',1,CONCAT('perf-',n,'.invalid'),'',3900,3900,0,JSON_OBJECT('legacy',JSON_EXTRACT('true','$')) FROM finance_perf_numbers`, nil},
		{`INSERT INTO invoices(id,number,user_id,order_id,status,amount_minor,currency,issued_at,due_at,paid_at,issuer_snapshot,currency_snapshot,kind,commerce_version,subtotal_minor,amount_paid_minor,billed_to_snapshot)
 SELECT ` + invoice + `,CONCAT('PERF-INV-',n),` + owner + `,` + order + `,IF(MOD(n,20)<15,'paid',IF(MOD(n,20)=19,'cancelled','unpaid')),3900,'CNY',` + at + `,TIMESTAMPADD(DAY,7,` + at + `),IF(MOD(n,20)<15,` + at + `,NULL),?,?,'order',1,3900,IF(MOD(n,20)<15,3900,0),
 JSON_OBJECT('name','Synthetic customer','company','Fixture','address','Example Street') FROM finance_perf_numbers`, []any{issuer, format}},
		{`INSERT INTO invoice_lines(id,invoice_id,position,description,amount_minor)
 SELECT ` + id("line-0", "n") + `,` + invoice + `,0,'Server monthly charge',3500 FROM finance_perf_numbers`, nil},
		{`INSERT INTO invoice_lines(id,invoice_id,position,description,amount_minor)
 SELECT ` + id("line-1", "n") + `,` + invoice + `,1,'Additional storage',400 FROM finance_perf_numbers`, nil},
		{`INSERT INTO invoice_payments(id,invoice_id,method,reference,amount_minor,refunded_minor,status,created_at)
 SELECT ` + id("payment", "n") + `,` + invoice + `,'manual',CONCAT('perf-receipt-',n),3900,0,'succeeded',` + at + ` FROM finance_perf_numbers WHERE MOD(n,20)<15`, nil},
		{`INSERT INTO services(id,user_id,name,plan_name,region,status,next_due_at,price_minor,currency,cpu,memory_gb,disk_gb,currency_snapshot,product_id,cycle,recurring_minor,created_at)
 SELECT ` + id("service", "n") + `,` + owner + `,CONCAT('service-',n,'.invalid'),'Performance server','fixture',IF(MOD(n,10)=9,'suspended','active'),TIMESTAMPADD(YEAR,10,` + at + `),3900,'CNY',1,1,20,?,'starter','monthly',3900,` + at + ` FROM finance_perf_numbers WHERE n<2000`, []any{format}},
	}
	for phase := 0; phase < 2; phase++ {
		queries = append(queries, struct {
			sql  string
			args []any
		}{`INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) SELECT ` + id(fmt.Sprintf("audit-%d", phase), "n") + `,` + owner + `,?,` + invoice + `,` + at + `,CONCAT('fixture-',IF(MOD(n,100)=0,0,1+MOD(n,9999)),'@performance.invalid'),'Synthetic finance performance history' FROM finance_perf_numbers`, []any{[]string{"order.created", "invoice.viewed"}[phase]}})
	}
	for i, query := range queries {
		if _, err = tx.ExecContext(ctx, query.sql, query.args...); err != nil {
			return fmt.Errorf("finance fixture step %d: %w", i, err)
		}
	}
	if _, err = tx.ExecContext(ctx, "DROP TEMPORARY TABLE finance_perf_numbers"); err != nil {
		return err
	}
	return tx.Commit()
}

func TestMySQLFinanceScaleFixture(t *testing.T) {
	m, migrationCtx := migrationTestDatabase(t)
	if err := m.Migrate(migrationCtx); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	f := &httpPerfFixture{m: m}
	if err := seedFinancePerformance(ctx, f); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int{"users": 10001, "orders": 50000, "invoices": 50000, "invoice_lines": 100000, "invoice_payments": 37500, "audit_events": 100000, "services": 2000, "products": 200} {
		var got int
		if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: got %d, want %d: %v", table, got, want, err)
		}
	}
	var inconsistent int
	if err := m.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM invoices i
 LEFT JOIN (SELECT invoice_id,SUM(amount_minor) total FROM invoice_lines GROUP BY invoice_id) l ON l.invoice_id=i.id
 LEFT JOIN (SELECT invoice_id,SUM(amount_minor) total FROM invoice_payments GROUP BY invoice_id) p ON p.invoice_id=i.id
 WHERE l.total<>i.amount_minor OR COALESCE(p.total,0)<>i.amount_paid_minor`).Scan(&inconsistent); err != nil || inconsistent != 0 {
		t.Fatal("inconsistent invoice lines or receipts", inconsistent, err)
	}
}
