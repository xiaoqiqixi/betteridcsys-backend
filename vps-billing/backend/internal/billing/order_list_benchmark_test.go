package billing

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// This benchmark measures the production Orders/Rows.Scan/snapshot-decoding
// path, not MySQL or HTTP capacity. The connector supplies fixed read-only rows
// without parsing SQL, opening a socket, consulting a DSN, or executing writes.
type orderListBenchmarkConnector struct {
	columns []string
	values  [][]driver.Value
}

type orderListBenchmarkDriver struct{}
type orderListBenchmarkConn struct{ fixture *orderListBenchmarkConnector }

func (orderListBenchmarkDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("order decoding benchmark requires its connector")
}

func (c *orderListBenchmarkConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return orderListBenchmarkConn{c}, nil
}

func (*orderListBenchmarkConnector) Driver() driver.Driver { return orderListBenchmarkDriver{} }
func (orderListBenchmarkConn) Close() error                { return nil }
func (orderListBenchmarkConn) Begin() (driver.Tx, error) {
	return nil, errors.New("order decoding benchmark has no transactions")
}
func (orderListBenchmarkConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("order decoding benchmark has no prepared statements")
}
func (c orderListBenchmarkConn) QueryContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Reuse the existing fixed Rows scanner; it only copies driver values and
	// advances an index. Each query starts a fresh cursor over the same fixture.
	return &preflightRows{columns: c.fixture.columns, values: c.fixture.values}, nil
}

func orderListBenchmarkFixture(b *testing.B, uniqueSnapshots bool) (*orderListBenchmarkConnector, []Order) {
	b.Helper()
	fixture := &orderListBenchmarkConnector{columns: strings.Split(orderColumns, ",")}
	want := make([]Order, 0, 100)
	marshal := func(value any) []byte {
		b.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			b.Fatal("encode synthetic historical snapshot", err)
		}
		return raw
	}
	for n := 0; n < 100; n++ {
		issuer := InvoiceIdentity{Name: "Historical issuer", Address: "Recorded address", SupportEmail: "fixture@history.invalid"}
		format := DefaultCurrencyFormat("CNY")
		format.Name = "Historical yuan"
		plan := PlanSnapshot{PlanID: "starter", Name: "Recorded plan", CPU: 2, MemoryGB: 4, DiskGB: 80, TransferTB: 2, Region: "Recorded region", BillingCycle: "monthly"}
		if uniqueSnapshots {
			issuer.Name = fmt.Sprintf("Historical issuer %03d", n)
			format.Name = fmt.Sprintf("Historical yuan %03d", n)
			plan.Name = fmt.Sprintf("Recorded plan %03d", n)
		}
		order := Order{
			ID: fmt.Sprintf("%032x", 100-n), Number: fmt.Sprintf("BENCH-ORDER-%03d", 100-n), UserID: fmt.Sprintf("%032x", 1001),
			PlanName: fmt.Sprintf("Historical plan %03d", n), Status: "pending_payment", TotalMinor: int64(3900 + n), Currency: "CNY",
			CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(100-n) * time.Second),
			Hostname:  fmt.Sprintf("history-%03d.invalid", n), Issuer: &issuer, CurrencyFormat: format, PlanSnapshot: &plan,
		}
		want = append(want, order)
		// String columns use the byte slices returned by the MySQL text-row
		// driver; database/sql still performs the real Scan conversions/copies.
		fixture.values = append(fixture.values, []driver.Value{
			[]byte(order.ID), []byte(order.Number), []byte(order.UserID), []byte(order.PlanName), []byte(order.Status), order.TotalMinor,
			[]byte(order.Currency), order.CreatedAt, []byte(order.Hostname), marshal(issuer), marshal(format), marshal(plan),
		})
	}
	return fixture, want
}

func BenchmarkMySQLOrderListDecode(b *testing.B) {
	for _, scenario := range []struct {
		name   string
		unique bool
	}{{"RepeatedSnapshots", false}, {"UniqueSnapshots", true}} {
		b.Run(scenario.name, func(b *testing.B) {
			fixture, want := orderListBenchmarkFixture(b, scenario.unique)
			db := sql.OpenDB(fixture)
			db.SetMaxOpenConns(1)
			db.SetMaxIdleConns(1)
			b.Cleanup(func() { _ = db.Close() })
			store := &MySQL{DB: db}
			ctx, owner := b.Context(), want[0].UserID
			orders, err := store.Orders(ctx, owner)
			if err != nil || !reflect.DeepEqual(orders, want) {
				b.Fatal("production order decoder changed fixed historical rows", err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				orders, err = store.Orders(ctx, owner)
				if err != nil || len(orders) != 100 {
					b.Fatal("production order-list read failed", err)
				}
			}
			b.StopTimer()
			b.ReportMetric(100, "orders/op")
			if !reflect.DeepEqual(orders, want) {
				b.Fatal("production order decoder changed fixed historical rows")
			}
		})
	}
}
