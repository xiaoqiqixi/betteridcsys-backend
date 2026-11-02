package billing

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"
)

// Exercise the proposed migration in an owned disposable database. Production
// registration also needs the protected harness's migration inventory updated
// by CI-2; this experiment does not change any deployed migration or schema.
func TestMySQLCommerceFinanceListIndexCandidates(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	f := &httpPerfFixture{m: m}
	if err := seedFinancePerformance(ctx, f); err != nil {
		t.Fatal(err)
	}
	migrationExec(t, ctx, m.DB, "ANALYZE TABLE orders,invoices")
	type timing struct {
		P50 float64 `json:"p50_ms"`
		P95 float64 `json:"p95_ms"`
	}
	type candidate struct {
		SQL        string          `json:"sql"`
		DDL        string          `json:"ddl"`
		Before     timing          `json:"before"`
		After      timing          `json:"after"`
		BeforePlan json.RawMessage `json:"before_plan"`
		AfterPlan  json.RawMessage `json:"after_plan"`
	}
	candidates := []candidate{
		{SQL: "SELECT id FROM orders ORDER BY created_at DESC,id DESC LIMIT 51", DDL: "CREATE INDEX ix_orders_date_id ON orders(created_at,id)"},
		{SQL: "SELECT id FROM orders WHERE status='pending_payment' ORDER BY created_at DESC,id DESC LIMIT 51", DDL: "CREATE INDEX ix_orders_status_date_id ON orders(status,created_at,id)"},
		{SQL: "SELECT id FROM invoices ORDER BY issued_at DESC,id DESC LIMIT 51", DDL: "CREATE INDEX ix_invoices_date_id ON invoices(issued_at,id)"},
		{SQL: "SELECT id FROM invoices WHERE status='unpaid' ORDER BY issued_at DESC,id DESC LIMIT 51", DDL: "CREATE INDEX ix_invoices_status_date_id ON invoices(status,issued_at,id)"},
	}
	measure := func(query string) (timing, json.RawMessage, []string) {
		t.Helper()
		var plan json.RawMessage
		if err := m.DB.QueryRowContext(ctx, "EXPLAIN FORMAT=JSON "+query).Scan(&plan); err != nil {
			t.Fatal(err)
		}
		samples := make([]float64, 0, 100)
		var result []string
		for n := 0; n < 105; n++ {
			start := time.Now()
			ids, err := overviewIDs06(ctx, m.DB, query)
			elapsed := float64(time.Since(start)) / float64(time.Millisecond)
			if err != nil || len(ids) != 51 {
				t.Fatal("candidate selector", len(ids), err)
			}
			result = ids
			if n >= 5 {
				samples = append(samples, elapsed)
			}
		}
		sort.Float64s(samples)
		return timing{samples[49], samples[94]}, plan, result
	}
	beforeIDs := make([][]string, len(candidates))
	for i := range candidates {
		v := &candidates[i]
		v.Before, v.BeforePlan, beforeIDs[i] = measure(v.SQL)
	}
	for _, v := range candidates {
		migrationExec(t, ctx, m.DB, v.DDL)
	}
	migrationExec(t, ctx, m.DB, "ANALYZE TABLE orders,invoices")
	for i := range candidates {
		v := &candidates[i]
		var afterIDs []string
		v.After, v.AfterPlan, afterIDs = measure(v.SQL)
		if !reflect.DeepEqual(beforeIDs[i], afterIDs) {
			t.Fatal("index changed ordered selector results", v.SQL)
		}
	}
	raw, err := json.Marshal(map[string]any{"seed": financePerfSeed, "samples": 100, "warmup_requests": 5, "scope": "SQL selector experiment; indexes are not deployed", "candidates": candidates})
	if err != nil {
		t.Fatal(err)
	}
	t.Log("PERF1_INDEX_RESULT " + string(raw))
}
