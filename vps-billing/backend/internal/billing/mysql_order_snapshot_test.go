package billing

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestMySQLOrderSnapshotMemoPreservesHistoricalRows(t *testing.T) {
	f := newCancellationSQLFixture(t)
	for _, currency := range []struct {
		code      string
		precision int
	}{{"JPY", 0}, {"KWD", 3}} {
		configureTestCurrency(t, f.m, f.admin, currency.code, currency.precision, "")
	}
	stamp := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	older := mysqlRecentFixtureOrder(t, f, f.owner.ID, "00000000000000000000000000000001", "JPY", "paid", stamp)
	middle := mysqlRecentFixtureOrder(t, f, f.owner.ID, "00000000000000000000000000000002", "KWD", "cancelled", stamp.Add(time.Second))
	first := mysqlRecentFixtureOrder(t, f, f.owner.ID, "00000000000000000000000000000003", "CNY", "pending_payment", stamp.Add(2*time.Second))
	second := mysqlRecentFixtureOrder(t, f, f.owner.ID, "00000000000000000000000000000004", "CNY", "paid", stamp.Add(3*time.Second))
	mysqlRecentFixtureOrder(t, f, f.other.ID, "00000000000000000000000000000005", "CNY", "paid", stamp.Add(time.Hour))
	expected := []Order{second, first, middle, older}
	orders, err := f.m.Orders(f.ctx, f.owner.ID)
	if err != nil || !reflect.DeepEqual(orders, expected) {
		t.Fatal("order-list memo changed ownership or historical rows", err)
	}
	orders[0].Issuer.Name = "caller mutation"
	orders[0].PlanSnapshot.Name = "caller mutation"
	orders[0].CurrencyFormat.Name = "caller mutation"
	if !reflect.DeepEqual(orders[1], expected[1]) {
		t.Fatal("adjacent orders share mutable snapshots")
	}
	again, err := f.m.Orders(f.ctx, f.owner.ID)
	if err != nil || !reflect.DeepEqual(again, expected) {
		t.Fatal("returned order mutation reached later reads", err)
	}

	// A later invocation must read changed historical bytes even when the row
	// and currency identities are unchanged. This modifies only synthetic data.
	second.CurrencyFormat.Prefix = "changed archive "
	raw, err := json.Marshal(second.CurrencyFormat)
	if err != nil {
		t.Fatal(err)
	}
	migrationExec(t, f.ctx, f.m.DB, "UPDATE orders SET currency_snapshot=? WHERE id=?", raw, second.ID)
	expected[0] = second
	again, err = f.m.Orders(f.ctx, f.owner.ID)
	if err != nil || !reflect.DeepEqual(again, expected) {
		t.Fatal("order-list memo outlived its query or reused different raw bytes", err)
	}
}

func TestMySQLOrderSnapshotMemoDoesNotHideInvalidFollowingRows(t *testing.T) {
	f := newCancellationSQLFixture(t)
	configureTestCurrency(t, f.m, f.admin, "USD", 2, "")
	stamp := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	older := mysqlRecentFixtureOrder(t, f, f.owner.ID, "00000000000000000000000000000001", "CNY", "pending_payment", stamp)
	mysqlRecentFixtureOrder(t, f, f.owner.ID, "00000000000000000000000000000002", "CNY", "pending_payment", stamp.Add(time.Second))
	var issuer, currency, plan []byte
	if err := f.m.DB.QueryRowContext(f.ctx, "SELECT issuer_snapshot,currency_snapshot,plan_snapshot FROM orders WHERE id=?", older.ID).Scan(&issuer, &currency, &plan); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, mutation string }{
		{"same_snapshot_different_currency", "UPDATE orders SET currency='USD' WHERE id=?"},
		{"missing_precision", "UPDATE orders SET currency_snapshot=JSON_REMOVE(currency_snapshot,'$.decimal_places') WHERE id=?"},
		{"invalid_issuer", "UPDATE orders SET issuer_snapshot=JSON_ARRAY() WHERE id=?"},
		{"invalid_plan", "UPDATE orders SET plan_snapshot=JSON_ARRAY() WHERE id=?"},
	} {
		t.Run(test.name, func(t *testing.T) {
			migrationExec(t, f.ctx, f.m.DB, "UPDATE orders SET currency='CNY',issuer_snapshot=?,currency_snapshot=?,plan_snapshot=? WHERE id=?", issuer, currency, plan, older.ID)
			migrationExec(t, f.ctx, f.m.DB, test.mutation, older.ID)
			if orders, err := f.m.Orders(f.ctx, f.owner.ID); err == nil || len(orders) != 0 {
				t.Fatal("successful preceding snapshot hid an invalid following row", err)
			}
			// RecentOrders retains its original strict uncached decoding path.
			if orders, err := f.m.RecentOrders(f.ctx, f.owner.ID); err == nil || len(orders) != 0 {
				t.Fatal("strict recent-order path accepted an invalid row", err)
			}
		})
	}
}
