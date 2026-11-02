package billing

import (
	"context"
	"encoding/json"
	"testing"
)

// Read server-side diagnostics after latency measurement, scoped to this
// disposable schema. Never reset the shared server's instrumentation tables.
func logFinanceStatementTimings(t *testing.T, ctx context.Context, m *MySQL) {
	t.Helper()
	stats := m.DB.Stats()
	rows, err := m.DB.QueryContext(ctx, `SELECT DIGEST_TEXT,COUNT_STAR,SUM_TIMER_WAIT/1000000000,MAX_TIMER_WAIT/1000000000,SUM_LOCK_TIME/1000000000,SUM_ROWS_EXAMINED
FROM performance_schema.events_statements_summary_by_digest WHERE SCHEMA_NAME=? AND DIGEST_TEXT LIKE 'SELECT%' ORDER BY MAX_TIMER_WAIT DESC LIMIT 20`, m.database)
	if err != nil {
		t.Fatal("read isolated finance statement timings", err)
	}
	defer rows.Close()
	type statement struct {
		SQL          string  `json:"sql"`
		Count        uint64  `json:"count"`
		TotalMS      float64 `json:"total_ms"`
		MaxMS        float64 `json:"max_ms"`
		LockMS       float64 `json:"lock_ms"`
		RowsExamined uint64  `json:"rows_examined"`
	}
	statements := []statement{}
	for rows.Next() {
		var v statement
		if err := rows.Scan(&v.SQL, &v.Count, &v.TotalMS, &v.MaxMS, &v.LockMS, &v.RowsExamined); err != nil {
			t.Fatal(err)
		}
		statements = append(statements, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"scope": "all SELECTs in the disposable schema before profiling", "pool_wait_count": stats.WaitCount, "pool_wait_ms": float64(stats.WaitDuration) / 1000000, "statements": statements})
	if err != nil {
		t.Fatal(err)
	}
	t.Log("PERF1_STATEMENTS " + string(raw))
}
