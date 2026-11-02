package billing

import (
	api "aster/billing/internal/financeapi"
	"encoding/json"
	"reflect"
	"testing"
)

func TestMySQLCommerceServiceSummariesMatchDetailsAndOwnership(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	f := &httpPerfFixture{m: m}
	const seed = "service-summary-equivalence"
	if err := httpPerfSeed(ctx, f, seed, httpPerfParameters{Users: 2, Services: 12}); err != nil {
		t.Fatal(err)
	}
	oneTime := httpPerfID(seed, "service-0")
	migrationExec(t, ctx, m.DB, "UPDATE services SET cycle='one_time',recurring_minor=999,price_override_minor=120 WHERE id=?", oneTime)
	migrationExec(t, ctx, m.DB, `UPDATE services SET product_id='starter',configuration_snapshot=JSON_OBJECT('kind','cloud_server'),status='suspended' WHERE id=?`, httpPerfID(seed, "service-1"))
	migrationExec(t, ctx, m.DB, `INSERT INTO service_tasks(id,service_id,action,status,payload,created_at,updated_at) VALUES(?,?,'provision','queued',JSON_OBJECT(),UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))`, ID(), oneTime)
	for _, owner := range []string{"", f.customer} {
		cursor, count := "", 0
		seen := map[string]bool{}
		for {
			page, next, err := m.ServicePage06(ctx, owner, cursor, "", "", 4)
			if err != nil {
				t.Fatal(err)
			}
			for _, got := range page {
				if seen[got.ID] || owner != "" && got.ClientID != owner || cursor != "" && got.ID >= cursor {
					t.Fatal("service cursor or ownership changed", got.ID)
				}
				seen[got.ID] = true
				detail, err := m.Service06(ctx, owner, got.ID)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(detail)
				var want api.AdminServiceSummary
				if err := json.Unmarshal(raw, &want); err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("service summary differs from detail: %#v / %#v: %v", got, want, err)
				}
				var public api.ServiceSummary
				if err := json.Unmarshal(raw, &public); err != nil || !reflect.DeepEqual(customerServiceSummary06(got), public) {
					t.Fatal("public summary changed", err)
				}
				if got.ID == oneTime && (got.AutoRenew || got.NextDueAt != nil || got.Recurring.AmountMinor != 120 || got.Recurring.DecimalPlaces != 2) {
					t.Fatal("one-time service or override changed", got)
				}
				count++
			}
			if next == nil {
				break
			}
			if len(page) != 4 || *next != page[len(page)-1].ID {
				t.Fatal("incorrect next service cursor")
			}
			cursor = *next
		}
		want := 12
		if owner != "" {
			want = 6
		}
		if count != want {
			t.Fatal("incomplete service pagination", count, want)
		}
	}
	page, next, err := m.ServicePage06(ctx, "", "", "suspended", "", 4)
	if err != nil || next != nil || len(page) != 1 || page[0].Kind != "cloud_server" {
		t.Fatal("status filter or historical kind changed", page, next, err)
	}
	if page, _, err = m.ServicePage06(ctx, f.customer, "", "", "missing-service", 4); err != nil || len(page) != 0 {
		t.Fatal("empty service page changed", err)
	}
}
