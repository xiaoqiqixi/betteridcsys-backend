package billing

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestMySQLCommerceOrderBatchesMatchDetailsAndOwnership(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	f := &httpPerfFixture{m: m}
	const seed = "order-page-equivalence"
	if err := httpPerfSeed(ctx, f, seed, httpPerfParameters{Users: 2, Orders: 16, Services: 4}); err != nil {
		t.Fatal(err)
	}
	migrationExec(t, ctx, m.DB, "UPDATE orders SET created_at='2025-01-01 00:00:00'")
	for i, options := range []string{`["Storage"]`, "Historical option text"} {
		iid := httpPerfID(seed, fmt.Sprintf("item-%d", i))
		migrationExec(t, ctx, m.DB, `INSERT INTO order_items(id,order_id,position,product_id,product_name,cycle,quantity,hostname,options_summary,recurring_minor,total_minor,stock_reserved,snapshot)
 VALUES(?,?,?,'starter','Historical server','monthly',1,NULL,?,1950,1950,0,JSON_OBJECT())`, iid, strings.ToUpper(httpPerfID(seed, "order-0")), i, options)
		migrationExec(t, ctx, m.DB, "INSERT INTO service_units VALUES(?,0,?)", strings.ToUpper(iid), httpPerfID(seed, fmt.Sprintf("service-%d", i*2)))
	}
	for _, owner := range []string{"", f.customer} {
		cursor, count := "", 0
		seen := map[string]bool{}
		for {
			page, next, err := m.OrderPage06(ctx, owner, cursor, "", "", 5)
			if err != nil {
				t.Fatal(err)
			}
			for _, got := range page {
				if seen[got.Order.ID] || owner != "" && got.ClientID != owner || cursor != "" && got.Order.ID >= cursor {
					t.Fatal("order cursor or owner mismatch", got.Order.ID)
				}
				seen[got.Order.ID] = true
				want, err := m.Order06(ctx, owner, got.Order.ID)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("batch differs from order detail: %#v / %#v: %v", got, want, err)
				}
				count++
			}
			if next == nil {
				break
			}
			if len(page) != 5 || *next != page[len(page)-1].Order.ID {
				t.Fatal("incorrect next order cursor")
			}
			cursor = *next
		}
		want := 16
		if owner != "" {
			want = 8
		}
		if count != want {
			t.Fatal("incomplete order pagination", count, want)
		}
	}
	if page, _, err := m.OrderPage06(ctx, f.customer, "", "paid", "", 5); err != nil || len(page) != 0 {
		t.Fatal("empty filtered order page changed", err)
	}
}
