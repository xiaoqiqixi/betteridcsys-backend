package billing

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/go-sql-driver/mysql"
)

type financeQuery struct {
	SQL  string
	Args []any
}
type financeQueryCapture struct {
	sync.Mutex
	queries map[string]financeQuery
}
type financeCaptureConnector struct {
	driver.Connector
	capture *financeQueryCapture
}
type financeCaptureConn struct {
	driver.Conn
	capture *financeQueryCapture
}
type financeCaptureStmt struct {
	driver.Stmt
	query   string
	capture *financeQueryCapture
}

func (c financeCaptureConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &financeCaptureConn{conn, c.capture}, nil
}
func (c *financeCaptureConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
}
func (c *financeCaptureConn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}
func (c *financeCaptureConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	stmt, err := c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return &financeCaptureStmt{stmt, query, c.capture}, nil
}
func (s *financeCaptureStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(s.query)), "SELECT ") {
		s.capture.Lock()
		if _, exists := s.capture.queries[s.query]; !exists {
			q := financeQuery{SQL: s.query, Args: make([]any, len(args))}
			for i, arg := range args {
				q.Args[i] = arg.Value
				if raw, ok := arg.Value.([]byte); ok {
					q.Args[i] = append([]byte{}, raw...)
				}
			}
			s.capture.queries[s.query] = q
		}
		s.capture.Unlock()
	}
	return s.Stmt.(driver.StmtQueryContext).QueryContext(ctx, args)
}
func (s *financeCaptureStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return s.Stmt.(driver.StmtExecContext).ExecContext(ctx, args)
}

// Trace only the preflight requests. Restore the ordinary driver and original
// connection pool before timing, so the observer cannot inflate the comparison.
func captureFinanceQueries(t *testing.T, m *MySQL) func() []financeQuery {
	t.Helper()
	cfg, err := mysqlConfiguration(os.Getenv("ASTER_MYSQL_TEST_DSN"), true)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBName = m.database
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	capture := &financeQueryCapture{queries: map[string]financeQuery{}}
	original := m.DB
	traced := sql.OpenDB(financeCaptureConnector{connector, capture})
	traced.SetMaxOpenConns(original.Stats().MaxOpenConnections)
	m.DB = traced
	var once sync.Once
	var out []financeQuery
	stop := func() []financeQuery {
		once.Do(func() {
			m.DB = original
			if err := traced.Close(); err != nil {
				t.Error(err)
			}
			capture.Lock()
			defer capture.Unlock()
			for _, q := range capture.queries {
				out = append(out, q)
			}
			sort.Slice(out, func(i, j int) bool { return out[i].SQL < out[j].SQL })
		})
		return out
	}
	t.Cleanup(func() { stop() })
	return stop
}

func explainFinanceRequests(t *testing.T, ctx context.Context, m *MySQL, queries []financeQuery) {
	t.Helper()
	if len(queries) < 20 {
		t.Fatal("incomplete preflight SQL capture", len(queries))
	}
	for i, q := range queries {
		var plan json.RawMessage
		if err := m.DB.QueryRowContext(ctx, "EXPLAIN FORMAT=JSON "+q.SQL, q.Args...).Scan(&plan); err != nil {
			t.Fatalf("explain preflight query %d: %v", i, err)
		}
		out, _ := json.Marshal(map[string]any{"name": fmt.Sprintf("request_%02d", i), "sql": q.SQL, "plan": plan})
		t.Log("PERF1_EXPLAIN " + string(out))
	}
}
