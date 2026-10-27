package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// These historical query fixtures exist only in the unique disposable database
// owned by newCancellationSQLFixture. No payment or provisioning is exercised.
func mysqlRecentFixtureOrder(t *testing.T, f cancellationSQLFixture, uid, id, code, status string, at time.Time) Order {
	t.Helper()
	format := CurrencyFormat{Code: code, Name: "Historical " + code, Prefix: "old ", Suffix: " archived", DecimalPlaces: 2}
	amount := int64(3900)
	switch code {
	case "JPY":
		format.DecimalPlaces, amount = 0, 1250
	case "KWD":
		format.DecimalPlaces, amount = 3, 1234
	}
	order := Order{
		ID: id, Number: "RECENT-" + id, UserID: uid, PlanName: "Historical plan",
		Status: status, TotalMinor: amount, Currency: code, CurrencyFormat: format,
		CreatedAt: at, Hostname: "recent-" + id,
		Issuer:       &InvoiceIdentity{Name: "Historical issuer", Address: "Historical address", SupportEmail: "history@test.invalid"},
		PlanSnapshot: &PlanSnapshot{PlanID: "starter", Name: "Historical plan", CPU: 3, MemoryGB: 5, DiskGB: 75, TransferTB: 2, Region: "Historical region", BillingCycle: "monthly"},
	}
	marshal := func(value any) []byte {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal("encode historical recent-order snapshot", err)
		}
		return raw
	}
	migrationExec(t, f.ctx, f.m.DB,
		"INSERT INTO orders(id,number,user_id,plan_id,plan_name,hostname,status,total_minor,currency,created_at,issuer_snapshot,currency_snapshot,plan_snapshot) VALUES(?,?,?,'starter',?,?,?,?,?,?,?,?,?)",
		order.ID, order.Number, uid, order.PlanName, order.Hostname, order.Status, order.TotalMinor, order.Currency, at, marshal(order.Issuer), marshal(format), marshal(order.PlanSnapshot))
	return order
}

func mysqlRecentAssertOrders(t *testing.T, f cancellationSQLFixture, uid string, expected []Order) {
	t.Helper()
	actual, err := f.m.RecentOrders(f.ctx, uid)
	if err != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("recent orders changed owner, ordering, limit or historical records: got=%+v want=%+v error=%v", actual, expected, err)
	}
}

func TestMySQLRecentOrdersOwnerLimitOrderingAndSnapshots(t *testing.T) {
	f := newCancellationSQLFixture(t)
	for _, currency := range []struct {
		code     string
		decimals int
	}{{"USD", 2}, {"JPY", 0}, {"KWD", 3}} {
		configureTestCurrency(t, f.m, f.admin, currency.code, currency.decimals, "")
	}
	if orders, err := f.m.RecentOrders(f.ctx, ""); !errors.Is(err, ErrNotFound) || len(orders) != 0 {
		t.Fatal("empty owner exposed a global recent-order list", orders, err)
	}
	mysqlRecentAssertOrders(t, f, f.owner.ID, []Order{})
	at := time.Date(2025, 2, 3, 4, 5, 6, 123456000, time.UTC)
	// Insertion order deliberately differs from created_at order. IDs are fixed,
	// valid lowercase hex and expected ordering is written explicitly below.
	b := mysqlRecentFixtureOrder(t, f, f.owner.ID, "00000000000000000000000000000020", "JPY", "paid", at.Add(3*time.Minute))
	a := mysqlRecentFixtureOrder(t, f, f.owner.ID, "00000000000000000000000000000010", "CNY", "pending_payment", at.Add(time.Minute))
	c := mysqlRecentFixtureOrder(t, f, f.owner.ID, "00000000000000000000000000000030", "KWD", "cancelled", at.Add(2*time.Minute))
	other := mysqlRecentFixtureOrder(t, f, f.other.ID, "fffffffffffffffffffffffffffffff0", "USD", "paid", at.Add(99*time.Minute))
	mysqlRecentAssertOrders(t, f, f.owner.ID, []Order{b, c, a})
	mysqlRecentAssertOrders(t, f, f.other.ID, []Order{other})

	fifth := mysqlRecentFixtureOrder(t, f, f.owner.ID, "00000000000000000000000000000060", "KWD", "pending_payment", at.Add(5*time.Minute))
	fourth := mysqlRecentFixtureOrder(t, f, f.owner.ID, "00000000000000000000000000000040", "USD", "paid", at.Add(5*time.Minute))
	e := mysqlRecentFixtureOrder(t, f, f.owner.ID, "00000000000000000000000000000050", "CNY", "cancelled", at.Add(4*time.Minute))
	expected := []Order{fifth, fourth, e, b, c}
	mysqlRecentAssertOrders(t, f, f.owner.ID, expected)

	// Catalog and current issuer metadata cannot replace historical snapshots.
	migrationExec(t, f.ctx, f.m.DB, "UPDATE plans SET name='Current catalog',cpu=16,region='Current region' WHERE id='starter'")
	migrationExec(t, f.ctx, f.m.DB, "UPDATE site_settings SET document=JSON_SET(document,'$.invoice_issuer','Current issuer','$.invoice_address','Current address') WHERE id=1")
	mysqlRecentAssertOrders(t, f, f.owner.ID, expected)
	returned, err := f.m.RecentOrders(f.ctx, f.owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	returned[0].Issuer.Name = "caller mutation"
	returned[0].PlanSnapshot.Region = "caller mutation"
	returned[0].CurrencyFormat.Prefix = "caller mutation"
	mysqlRecentAssertOrders(t, f, f.owner.ID, expected)

	// Fill to 100 owner records with older rows: the list API retains its own
	// larger contract while the overview query stays at exactly five records.
	for n := 0; n < 94; n++ {
		mysqlRecentFixtureOrder(t, f, f.owner.ID, fmt.Sprintf("%032x", 0x100+n), "CNY", "pending_payment", at.Add(-time.Duration(n+1)*time.Minute))
	}
	all, err := f.m.Orders(f.ctx, f.owner.ID)
	if err != nil || len(all) != 100 {
		t.Fatal("overview limit changed the original order-list contract", len(all), err)
	}
	mysqlRecentAssertOrders(t, f, f.owner.ID, expected)
	// An older row is outside the selected window. Decoding the original large
	// list and then truncating would incorrectly fail on this unrelated record.
	migrationExec(t, f.ctx, f.m.DB, "UPDATE orders SET currency_snapshot=JSON_ARRAY() WHERE id=?", fmt.Sprintf("%032x", 0x100+93))
	mysqlRecentAssertOrders(t, f, f.owner.ID, expected)
	mysqlRecentAssertOrders(t, f, ID(), []Order{})
}

func TestMySQLRecentOrdersSelectedSnapshotAndContextErrors(t *testing.T) {
	f := newCancellationSQLFixture(t)
	at := time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC)
	order := mysqlRecentFixtureOrder(t, f, f.owner.ID, "00000000000000000000000000000001", "CNY", "pending_payment", at)
	var issuerJSON, currencyJSON, planJSON []byte
	if err := f.m.DB.QueryRowContext(f.ctx, "SELECT issuer_snapshot,currency_snapshot,plan_snapshot FROM orders WHERE id=?", order.ID).Scan(&issuerJSON, &currencyJSON, &planJSON); err != nil {
		t.Fatal(err)
	}
	legacyCurrency, err := json.Marshal(DefaultCurrencyFormat("CNY"))
	if err != nil {
		t.Fatal(err)
	}
	// Migration 7 backfills the old CNY currency format, while issuer and plan
	// snapshots remain absent when the historical record never contained them.
	migrationExec(t, f.ctx, f.m.DB, "UPDATE orders SET issuer_snapshot=NULL,currency_snapshot=?,plan_snapshot=NULL WHERE id=?", legacyCurrency, order.ID)
	legacy := order
	legacy.Issuer, legacy.PlanSnapshot, legacy.CurrencyFormat = nil, nil, DefaultCurrencyFormat("CNY")
	mysqlRecentAssertOrders(t, f, f.owner.ID, []Order{legacy})
	migrationExec(t, f.ctx, f.m.DB, "UPDATE orders SET issuer_snapshot=?,currency_snapshot=?,plan_snapshot=? WHERE id=?", issuerJSON, currencyJSON, planJSON, order.ID)
	mysqlRecentAssertOrders(t, f, f.owner.ID, []Order{order})
	for _, column := range []string{"issuer_snapshot", "currency_snapshot", "plan_snapshot"} {
		t.Run(column, func(t *testing.T) {
			var original []byte
			if err := f.m.DB.QueryRowContext(f.ctx, "SELECT "+column+" FROM orders WHERE id=?", order.ID).Scan(&original); err != nil {
				t.Fatal(err)
			}
			// The value is legal JSON for MySQL but invalid for the corresponding
			// Go snapshot decoder. A selected corrupt row must not be omitted.
			migrationExec(t, f.ctx, f.m.DB, "UPDATE orders SET "+column+"=JSON_ARRAY() WHERE id=?", order.ID)
			orders, err := f.m.RecentOrders(f.ctx, f.owner.ID)
			if err == nil || len(orders) != 0 {
				t.Fatal("selected malformed snapshot was silently accepted or omitted", orders, err)
			}
			migrationExec(t, f.ctx, f.m.DB, "UPDATE orders SET "+column+"=? WHERE id=?", original, order.ID)
			mysqlRecentAssertOrders(t, f, f.owner.ID, []Order{order})
		})
	}
	cancelled, cancel := context.WithCancel(f.ctx)
	cancel()
	if orders, err := f.m.RecentOrders(cancelled, f.owner.ID); !errors.Is(err, context.Canceled) || len(orders) != 0 {
		t.Fatal("recent orders did not propagate the cancelled request context", orders, err)
	}
}
