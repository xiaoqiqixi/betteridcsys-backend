package billing

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"testing"
	"time"
)

// Run in the existing disposable MySQL CI, without changing the CI-2 runner.
// A plain child binary keeps race instrumentation out of latency/profile data.
// GOMAXPROCS=1 limits the API runtime, not the MySQL host: this is diagnostic
// evidence, not a claim of passing the dedicated 1-vCPU acceptance target.
func TestMySQLCommerceFinanceRuntimePerformance(t *testing.T) {
	if os.Getenv("ASTER_MYSQL_TEST_DSN") == "" {
		t.Skip("ASTER_MYSQL_TEST_DSN not set; real MySQL performance not run")
	}
	if os.Getenv("ASTER_FINANCE_PERF_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		binary := filepath.Join(t.TempDir(), "finance-perf.test")
		build := exec.CommandContext(ctx, "go", "test", "-mod=readonly", "-c", "-o", binary, ".")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build uninstrumented measurement binary: %v\n%s", err, out)
		}
		run := exec.CommandContext(ctx, binary, "-test.run=^TestMySQLCommerceFinanceRuntimePerformance$", "-test.v", "-test.timeout=7m")
		run.Env = append(os.Environ(), "ASTER_FINANCE_PERF_CHILD=1", "GOMAXPROCS=1")
		out, err := run.CombinedOutput()
		t.Log(string(out))
		if err != nil {
			t.Fatal("finance measurement child failed", err)
		}
		return
	}
	m, migrationCtx := migrationTestDatabase(t)
	if err := m.Migrate(migrationCtx); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	f := &httpPerfFixture{m: m}
	if err := seedFinancePerformance(ctx, f); err != nil {
		t.Fatal(err)
	}
	// Refresh the optimizer's statistics after bulk loading, outside timing.
	for _, table := range []string{"users", "orders", "invoices", "invoice_lines", "invoice_payments", "audit_events", "services", "products"} {
		if _, err := m.DB.ExecContext(ctx, "ANALYZE TABLE "+table); err != nil {
			t.Fatal(err)
		}
	}
	financePerfExplain(t, ctx, f)
	app := NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: strings.Repeat("f", 64), CommerceContractVersion: "0.6", Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	client := server.Client()
	client.Timeout = 15 * time.Second
	type endpoint struct{ method, path, body string }
	endpoints := []endpoint{}
	for _, path := range []string{
		"/session", "/invoices", "/admin/invoices", "/orders", "/admin/orders",
		"/services", "/admin/services", "/audit-events", "/admin/audit-events",
		"/products", "/admin/products", "/products/perf-product-0", "/admin/products/perf-product-0",
		"/overview", "/admin/overview?currency=CNY", "/admin/customer-accounts",
		"/currencies", "/admin/currencies", "/catalog-taxonomy", "/admin/catalog-taxonomy",
		"/invoices/" + httpPerfID(financePerfSeed, "invoice-0"),
		"/orders/" + httpPerfID(financePerfSeed, "order-0"),
		"/services/" + httpPerfID(financePerfSeed, "service-0"),
	} {
		endpoints = append(endpoints, endpoint{http.MethodGet, "/api/v1" + path, ""})
	}
	endpoints = append(endpoints, endpoint{http.MethodPost, "/api/v1/credit/top-up", `{"amount_minor":1000}`})
	requestNumber := 0
	request := func(e endpoint) float64 {
		t.Helper()
		requestNumber++
		r, err := http.NewRequestWithContext(ctx, e.method, server.URL+e.path, strings.NewReader(e.body))
		if err != nil {
			t.Fatal(err)
		}
		token := f.cookie
		if strings.Contains(e.path, "/admin/") {
			token = f.adminCookie
		}
		r.AddCookie(&http.Cookie{Name: app.cookieName(), Value: token})
		r.Header.Set("X-Aster-API-Version", "0.6")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", app.csrf(token))
		r.Header.Set("Idempotency-Key", fmt.Sprintf("finance-perf-%08d", requestNumber))
		start := time.Now()
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(e.path, err)
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		response.Body.Close()
		elapsed := float64(time.Since(start)) / float64(time.Millisecond)
		if err != nil || response.StatusCode != http.StatusOK || !json.Valid(body) {
			t.Fatalf("%s %s: status=%d err=%v body=%.500s", e.method, e.path, response.StatusCode, err, body)
		}
		return elapsed
	}
	type measurement struct {
		Method   string    `json:"method"`
		Path     string    `json:"path"`
		P50      float64   `json:"p50_ms"`
		P95      float64   `json:"p95_ms"`
		N        int       `json:"samples"`
		SampleMS []float64 `json:"sample_ms"`
	}
	results := []measurement{}
	// Resolve every read route before timing; legacy migrated products can be
	// hidden and are not a valid public product-detail fixture.
	stopCapture := captureFinanceQueries(t, m)
	for _, e := range endpoints {
		if e.method == http.MethodGet {
			request(e)
		}
	}
	explainFinanceRequests(t, ctx, m, stopCapture())
	// Profiles are a separate phase, so they do not perturb these percentiles.
	for _, e := range endpoints {
		for range 5 {
			request(e)
		}
		samples := make([]float64, 100)
		for i := range samples {
			samples[i] = request(e)
		}
		sort.Float64s(samples)
		path := e.path
		for _, kind := range []string{"invoice", "order", "service"} {
			path = strings.ReplaceAll(path, httpPerfID(financePerfSeed, kind+"-0"), "{id}")
		}
		results = append(results, measurement{e.method, path, samples[49], samples[94], len(samples), samples})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].P95 > results[j].P95 })
	var mysqlVersion string
	if err := m.DB.QueryRowContext(ctx, "SELECT VERSION()").Scan(&mysqlVersion); err != nil {
		t.Fatal(err)
	}
	report, _ := json.Marshal(map[string]any{"seed": financePerfSeed, "go": runtime.Version(), "mysql": mysqlVersion, "gomaxprocs": runtime.GOMAXPROCS(0), "host_cpus": runtime.NumCPU(), "concurrency": 1, "warmup_requests": 5, "measurements": results})
	t.Log("PERF1_RESULT " + string(report))
	logFinanceStatementTimings(t, ctx, m)
	var cpu bytes.Buffer
	runtime.SetBlockProfileRate(1)
	previous := runtime.SetMutexProfileFraction(1)
	defer runtime.SetBlockProfileRate(0)
	defer runtime.SetMutexProfileFraction(previous)
	if err := pprof.StartCPUProfile(&cpu); err != nil {
		t.Fatal(err)
	}
	for start := time.Now(); time.Since(start) < 3*time.Second; {
		for _, e := range endpoints[:20] {
			request(e)
		}
	}
	pprof.StopCPUProfile()
	profiles := map[string][]byte{"cpu": cpu.Bytes()}
	for _, name := range []string{"block", "mutex"} {
		var b bytes.Buffer
		if err := pprof.Lookup(name).WriteTo(&b, 0); err != nil {
			t.Fatal(err)
		}
		profiles[name] = b.Bytes()
	}
	// Native pprof bytes survive in the existing JSON test artifact. These
	// profiles contain only the isolated synthetic process, never real accounts.
	for _, name := range []string{"cpu", "block", "mutex"} {
		t.Log("PERF1_PROFILE " + name + " " + base64.StdEncoding.EncodeToString(profiles[name]))
	}
}

func financePerfExplain(t *testing.T, ctx context.Context, f *httpPerfFixture) {
	t.Helper()
	queries := map[string]string{
		"invoices_admin":   "SELECT id FROM invoices ORDER BY issued_at DESC,id DESC LIMIT 51",
		"invoices_client":  "SELECT id FROM invoices WHERE user_id=? ORDER BY issued_at DESC,id DESC LIMIT 51",
		"invoices_status":  "SELECT id FROM invoices WHERE status='unpaid' ORDER BY issued_at DESC,id DESC LIMIT 51",
		"orders_admin":     "SELECT id FROM orders ORDER BY created_at DESC,id DESC LIMIT 51",
		"orders_client":    "SELECT id FROM orders WHERE user_id=? ORDER BY created_at DESC,id DESC LIMIT 51",
		"orders_status":    "SELECT id FROM orders WHERE status='pending_payment' ORDER BY created_at DESC,id DESC LIMIT 51",
		"services_admin":   "SELECT id FROM services ORDER BY id DESC LIMIT 51",
		"services_client":  "SELECT id FROM services WHERE user_id=? ORDER BY id DESC LIMIT 51",
		"services_status":  "SELECT id FROM services WHERE status='active' ORDER BY id DESC LIMIT 51",
		"audit_admin":      "SELECT id,user_id,action,target,created_at,actor_email,detail FROM audit_events WHERE created_at<=UTC_TIMESTAMP(6) ORDER BY created_at DESC,id DESC LIMIT 51",
		"audit_client":     "SELECT id,user_id,action,target,created_at,actor_email,detail FROM audit_events WHERE user_id=? AND created_at<=UTC_TIMESTAMP(6) ORDER BY created_at DESC,id DESC LIMIT 51",
		"products_admin":   "SELECT " + productList06Columns + productList06Joins + " ORDER BY p.sort_order,p.id",
		"invoice_lines":    "SELECT description,service_id,amount_minor,period_text FROM invoice_lines WHERE invoice_id=? ORDER BY position,id",
		"invoice_payments": "SELECT id,method,gateway,reference,amount_minor,status,created_at FROM invoice_payments WHERE invoice_id=? ORDER BY created_at,id",
	}
	names := make([]string, 0, len(queries))
	for name := range queries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		query := queries[name]
		args := []any{}
		if strings.Contains(query, "?") {
			id := f.customer
			if strings.HasPrefix(name, "invoice_") {
				id = httpPerfID(financePerfSeed, "invoice-0")
			}
			args = append(args, id)
		}
		var plan json.RawMessage
		if err := f.m.DB.QueryRowContext(ctx, "EXPLAIN FORMAT=JSON "+query, args...).Scan(&plan); err != nil {
			t.Fatal(name, err)
		}
		out, _ := json.Marshal(map[string]any{"name": name, "sql": query, "plan": plan})
		t.Log("PERF1_EXPLAIN " + string(out))
	}
}
