package billing

// Explicit, isolated performance experiment (never part of ordinary go test):
// go test -run '^$' -bench '^BenchmarkHTTPPerformance$' -benchtime=1x -count=1 -timeout=15m
// ASTER_MYSQL_TEST_DSN must point to the disposable native Unix-socket fixture.
// No real credentials, channels, email, provisioning, or debug HTTP endpoints are used.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const httpPerfSchema = "aster.http-performance/2"
const httpPerfSamplePeriod = 25 * time.Millisecond

var httpPerfCurrentStage string

// Only these fixed markers may leave the private benchmark log on failure.
func httpPerfStage(stage string) {
	switch stage {
	case "configuration", "database", "seed", "hash", "worker", "preflight", "warmup", "measurement", "cleanup", "report":
		httpPerfCurrentStage = stage
		fmt.Println("ASTER_HTTP_PERF_STAGE=" + stage)
	}
}

type httpPerfParameters struct {
	Users          int              `json:"users"`
	Orders         int              `json:"orders"`
	Services       int              `json:"services"`
	Concurrency    []int            `json:"concurrency"`
	Repeats        int              `json:"repeats"`
	WarmupNS       int64            `json:"warmup_ns"`
	DurationNS     int64            `json:"duration_ns"`
	SamplingNS     int64            `json:"sampling_ns"`
	HTTPTimeoutsNS map[string]int64 `json:"http_timeouts_ns"`
}

type httpPerfPool struct {
	WaitCount           int64 `json:"wait_count"`
	WaitDurationNS      int64 `json:"wait_duration_ns"`
	InUsePeak           int   `json:"in_use_peak"`
	OpenConnectionsPeak int   `json:"open_connections_peak"`
}

type httpPerfAPI struct {
	CPUSeconds    float64      `json:"cpu_seconds"`
	RSSPeakBytes  uint64       `json:"rss_peak_bytes"`
	HeapPeakBytes uint64       `json:"heap_peak_bytes"`
	Pool          httpPerfPool `json:"pool"`
}

type httpPerfLatency struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
}

type httpPerfMeasurement struct {
	Path             string            `json:"path"`
	Concurrency      int               `json:"concurrency"`
	Repeat           int               `json:"repeat"`
	Requests         int64             `json:"requests"`
	DurationNS       int64             `json:"duration_ns"`
	QPS              float64           `json:"qps"`
	LatencyMS        httpPerfLatency   `json:"latency_ms"`
	TransportErrors  int64             `json:"transport_errors"`
	HTTPErrors       int64             `json:"http_errors"`
	ErrorRate        float64           `json:"error_rate"`
	HTTPStatusCounts map[string]int64  `json:"http_status_counts"`
	API              httpPerfAPI       `json:"api"`
	MySQLLockDelta   map[string]uint64 `json:"mysql_lock_delta"`
}

type httpPerfEpoch struct {
	Label         string                `json:"label"`
	SourceSHA     string                `json:"source_sha"`
	BinarySHA256  string                `json:"binary_sha256"`
	GoVersion     string                `json:"go_version"`
	GOMAXPROCS    int                   `json:"gomaxprocs"`
	DatasetSHA256 string                `json:"dataset_sha256"`
	Measurements  []httpPerfMeasurement `json:"measurements"`
}

type httpPerfExplain struct {
	Name string          `json:"name"`
	SQL  string          `json:"sql"`
	Plan json.RawMessage `json:"plan"`
}

type httpPerfDataset struct {
	Counts           map[string]int64 `json:"counts"`
	SHA256           string           `json:"sha256"`
	BeforeAfterEqual bool             `json:"before_after_equal"`
}

type httpPerfCleanup struct {
	DatabaseDropped bool `json:"database_dropped"`
	WorkersStopped  bool `json:"workers_stopped"`
}

type httpPerfReport struct {
	Schema        string             `json:"schema"`
	SourceSHA     string             `json:"source_sha"`
	HarnessSHA256 string             `json:"harness_sha256"`
	Seed          string             `json:"seed"`
	Parameters    httpPerfParameters `json:"parameters"`
	Machine       map[string]string  `json:"machine"`
	MySQLVersion  string             `json:"mysql_version"`
	Dataset       httpPerfDataset    `json:"dataset"`
	Epochs        []httpPerfEpoch    `json:"epochs"`
	Explain       []httpPerfExplain  `json:"explain"`
	Cleanup       httpPerfCleanup    `json:"cleanup"`
}

func httpPerfInt(b *testing.B, name string, fallback, min, max int) int {
	b.Helper()
	if raw := os.Getenv(name); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < min || value > max {
			b.Fatalf("%s outside the bounded performance fixture range", name)
		}
		return value
	}
	return fallback
}

func httpPerfDuration(b *testing.B, name string, fallback, min, max time.Duration) time.Duration {
	b.Helper()
	if raw := os.Getenv(name); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil || value < min || value > max {
			b.Fatalf("%s outside the bounded performance fixture range", name)
		}
		return value
	}
	return fallback
}

func httpPerfHex(value string, size int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == size && strings.ToLower(value) == value
}

func httpPerfID(seed, label string) string {
	sum := sha256.Sum256([]byte(seed + ":" + label))
	return hex.EncodeToString(sum[:16])
}

func httpPerfFileHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err = io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func httpPerfHarnessHash() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("harness source location unavailable")
	}
	return httpPerfFileHash(file)
}

type httpPerfFixture struct {
	m                   *MySQL
	admin               *sql.DB
	name, dsn           string
	customer, adminID   string
	cookie, adminCookie string
	seed                string
	closed              bool
}

func httpPerfCheckMySQL(ctx context.Context, db *sql.DB, socket, root string) error {
	if !filepath.IsAbs(root) || !strings.HasPrefix(filepath.Base(root), "aster-mysql.") || !strings.HasPrefix(filepath.Base(filepath.Dir(root)), "aster-native.") || filepath.Clean(socket) != filepath.Join(root, "mysql.sock") {
		return errors.New("performance database is outside the owned native fixture")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("native MySQL fixture directory is not private")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Getuid()) {
		return errors.New("native MySQL fixture has another owner")
	}
	var dataDir, actualSocket string
	if db.QueryRowContext(ctx, "SELECT @@datadir,@@socket").Scan(&dataDir, &actualSocket) != nil || filepath.Clean(dataDir) != filepath.Join(root, "data") || filepath.Clean(actualSocket) != filepath.Clean(socket) {
		return errors.New("MySQL server is not the owned disposable native instance")
	}
	return nil
}

func httpPerfDatabase(b *testing.B, ctx context.Context) *httpPerfFixture {
	b.Helper()
	dsn := os.Getenv("ASTER_MYSQL_TEST_DSN")
	if dsn == "" {
		b.Fatal("ASTER_MYSQL_TEST_DSN required for explicit performance benchmark; no in-memory fallback")
	}
	cfg, err := mysqlConfiguration(dsn, true)
	if err != nil || !strings.Contains(cfg.DBName, "_test") || cfg.Net != "unix" {
		b.Fatal("performance benchmark requires the disposable _test database and local Unix socket")
	}
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		b.Fatal("open disposable database controller")
	}
	if err := httpPerfCheckMySQL(ctx, admin, cfg.Addr, os.Getenv("ASTER_NATIVE_MYSQL_FIXTURE_ROOT")); err != nil {
		_ = admin.Close()
		b.Fatal(err)
	}
	f := &httpPerfFixture{admin: admin, name: "aster_http_perf_test_" + ID()}
	b.Cleanup(func() {
		if !f.closed {
			failedStage := httpPerfCurrentStage
			httpPerfStage("cleanup")
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := f.close(cleanupCtx); err != nil {
				b.Error("clean up owned performance database")
			} else {
				// Successful fallback cleanup must not hide the original
				// failure stage from the runner's fixed-marker projection.
				httpPerfStage(failedStage)
			}
		}
	})
	if _, err = admin.ExecContext(ctx, "CREATE DATABASE `"+f.name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"); err != nil {
		b.Fatal("create unique owned performance database")
	}
	cfg.DBName = f.name
	f.dsn = cfg.FormatDSN()
	f.m, err = OpenMySQL(ctx, f.dsn, true)
	if err != nil {
		b.Fatal("open owned performance database")
	}
	if err = f.m.Migrate(ctx); err != nil {
		b.Fatal("migrate owned performance database")
	}
	return f
}

func (f *httpPerfFixture) close(ctx context.Context) error {
	if f.closed {
		return nil
	}
	if !strings.HasPrefix(f.name, "aster_http_perf_test_") || !httpPerfHex(strings.TrimPrefix(f.name, "aster_http_perf_test_"), 16) {
		return errors.New("refuse unowned database cleanup")
	}
	if f.m != nil {
		_ = f.m.DB.Close()
	}
	if _, err := f.admin.ExecContext(ctx, "DROP DATABASE IF EXISTS `"+f.name+"`"); err != nil {
		return errors.New("drop owned performance database")
	}
	var remaining int
	if err := f.admin.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME=?", f.name).Scan(&remaining); err != nil || remaining != 0 {
		return errors.New("owned performance database cleanup not confirmed")
	}
	f.closed = true
	return f.admin.Close()
}

// The seed is synthetic historical data, not an exercise of real payment or
// provisioning. Independent active historical services have no order linkage;
// pending orders are never represented as provisioned. Precreated sessions make
// every measured request read-only, including the authenticated /session path.
func httpPerfSeed(ctx context.Context, f *httpPerfFixture, seed string, p httpPerfParameters) error {
	tx, err := f.m.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stamp := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err = tx.ExecContext(ctx, "UPDATE schema_migrations SET applied_at=?", stamp); err != nil {
		return err
	}
	userStmt, err := tx.PrepareContext(ctx, "INSERT INTO users(id,email,name,role,password_hash,created_at) VALUES(?,?,?,?,?,?)")
	if err != nil {
		return err
	}
	defer userStmt.Close()
	for n := 0; n <= p.Users; n++ {
		id, role := httpPerfID(seed, fmt.Sprintf("user-%d", n)), "customer"
		if n == p.Users {
			role, f.adminID = "admin", id
		} else if n == 0 {
			f.customer = id
		}
		if _, err = userStmt.ExecContext(ctx, id, fmt.Sprintf("fixture-%d@performance.invalid", n), "Synthetic performance fixture", role, "!no-password-login-performance-fixture", stamp); err != nil {
			return err
		}
	}
	f.cookie = httpPerfID(seed, "customer-cookie-a") + httpPerfID(seed, "customer-cookie-b")
	f.adminCookie = httpPerfID(seed, "admin-cookie-a") + httpPerfID(seed, "admin-cookie-b")
	for _, session := range []Session{{Hash: digest(f.cookie), UserID: f.customer, Expires: stamp.AddDate(40, 0, 0)}, {Hash: digest(f.adminCookie), UserID: f.adminID, Expires: stamp.AddDate(40, 0, 0)}} {
		if _, err = tx.ExecContext(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES(?,?,?)", session.Hash, session.UserID, session.Expires); err != nil {
			return err
		}
	}
	plan := Catalog()[0]
	issuer, _ := json.Marshal(DefaultSettings().Identity())
	currency, _ := json.Marshal(DefaultCurrencyFormat("CNY"))
	planSnapshot, _ := json.Marshal(PlanSnapshot{plan.ID, plan.Name, plan.CPU, plan.MemoryGB, plan.DiskGB, plan.TransferTB, plan.Region, "monthly"})
	statements := []string{
		"INSERT INTO orders(id,number,user_id,plan_id,plan_name,hostname,status,total_minor,currency,created_at,issuer_snapshot,currency_snapshot,plan_snapshot) VALUES(?,?,?,?,?,?,'pending_payment',?,'CNY',?,?,?,?)",
		"INSERT INTO invoices(id,number,user_id,order_id,status,amount_minor,currency,issued_at,due_at,issuer_snapshot,currency_snapshot) VALUES(?,?,?,?,'unpaid',?,'CNY',?,?,?,?)",
		"INSERT INTO order_requests(user_id,request_key,fingerprint,order_id,created_at) VALUES(?,?,?,?,?)",
		"INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) VALUES(?,?,'order.created',?,?,?,'Synthetic performance fixture; no external operations')",
		"INSERT INTO outbox_events(id,aggregate_id,event_type,payload,created_at) VALUES(?,?,'order.created',?,?)",
	}
	prepared := make([]*sql.Stmt, 0, len(statements))
	for _, query := range statements {
		statement, err := tx.PrepareContext(ctx, query)
		if err != nil {
			return err
		}
		defer statement.Close()
		prepared = append(prepared, statement)
	}
	for n := 0; n < p.Orders; n++ {
		user := n % p.Users
		uid := httpPerfID(seed, fmt.Sprintf("user-%d", user))
		oid, iid := httpPerfID(seed, fmt.Sprintf("order-%d", n)), httpPerfID(seed, fmt.Sprintf("invoice-%d", n))
		at := stamp.Add(time.Duration(n) * time.Second)
		payload, _ := json.Marshal(map[string]string{"order_id": oid, "invoice_id": iid, "status": "pending_payment"})
		args := [][]any{
			{oid, "PERF-ORD-" + oid, uid, plan.ID, plan.Name, fmt.Sprintf("fixture-%d.invalid", n), plan.PriceMinor, at, issuer, currency, planSnapshot},
			{iid, "PERF-INV-" + iid, uid, oid, plan.PriceMinor, at, at.AddDate(0, 0, 7), issuer, currency},
			{uid, fmt.Sprintf("fixture-order-%d", n), digest(fmt.Sprintf("fixture:%s:%d", seed, n)), oid, at},
			{httpPerfID(seed, fmt.Sprintf("audit-%d", n)), uid, oid, at, fmt.Sprintf("fixture-%d@performance.invalid", user)},
			{httpPerfID(seed, fmt.Sprintf("outbox-%d", n)), oid, payload, at},
		}
		for j, statement := range prepared {
			if _, err = statement.ExecContext(ctx, args[j]...); err != nil {
				return err
			}
		}
	}
	service, err := tx.PrepareContext(ctx, "INSERT INTO services(id,user_id,name,plan_name,region,status,next_due_at,price_minor,currency,cpu,memory_gb,disk_gb,currency_snapshot) VALUES(?,?,?,?,?,'active',?,?,'CNY',?,?,?,?)")
	if err != nil {
		return err
	}
	defer service.Close()
	for n := 0; n < p.Services; n++ {
		if _, err = service.ExecContext(ctx, httpPerfID(seed, fmt.Sprintf("service-%d", n)), httpPerfID(seed, fmt.Sprintf("user-%d", n%p.Users)), fmt.Sprintf("historical-service-%d.invalid", n), plan.Name, plan.Region, stamp.AddDate(1, 0, 0).Add(time.Duration(n)*time.Second), plan.PriceMinor, plan.CPU, plan.MemoryGB, plan.DiskGB, currency); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	f.seed = seed
	return nil
}

func httpPerfDatasetHash(ctx context.Context, db *sql.DB) (httpPerfDataset, error) {
	result := httpPerfDataset{Counts: map[string]int64{}}
	tables, err := db.QueryContext(ctx, "SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_TYPE='BASE TABLE' ORDER BY TABLE_NAME")
	if err != nil {
		return result, err
	}
	var names []string
	for tables.Next() {
		var name string
		if err = tables.Scan(&name); err != nil {
			break
		}
		if strings.Trim(name, "abcdefghijklmnopqrstuvwxyz_") != "" {
			err = errors.New("unexpected fixture table identifier")
			break
		}
		names = append(names, name)
	}
	if err == nil {
		err = tables.Err()
	}
	_ = tables.Close()
	if err != nil {
		return result, err
	}
	h := sha256.New()
	encoder := json.NewEncoder(h)
	for _, name := range names {
		// Every core table has a primary key; ordering all columns also gives a
		// deterministic traversal for the composite order_requests primary key.
		rows, err := db.QueryContext(ctx, "SELECT * FROM `"+name+"` ORDER BY 1,2")
		if err != nil {
			return result, err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return result, err
		}
		_ = encoder.Encode([]any{name, columns})
		result.Counts[name] = 0
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for n := range values {
				pointers[n] = &values[n]
			}
			if err = rows.Scan(pointers...); err != nil {
				break
			}
			for n, value := range values {
				if raw, ok := value.([]byte); ok {
					values[n] = string(raw)
				}
			}
			if err = encoder.Encode(values); err != nil {
				break
			}
			result.Counts[name]++
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return result, err
		}
	}
	result.SHA256 = hex.EncodeToString(h.Sum(nil))
	return result, nil
}

type httpPerfWorkerConfig struct {
	DSN       string `json:"dsn"`
	Database  string `json:"database"`
	SourceSHA string `json:"source_sha"`
}

type httpPerfWorkerMessage struct {
	Kind          string      `json:"kind"`
	URL           string      `json:"url"`
	SourceSHA     string      `json:"source_sha"`
	HarnessSHA256 string      `json:"harness_sha256"`
	GoVersion     string      `json:"go_version"`
	GOMAXPROCS    int         `json:"gomaxprocs"`
	API           httpPerfAPI `json:"api"`
}

func httpPerfCPUSeconds() float64 {
	var usage syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &usage) != nil {
		return -1
	}
	return float64(usage.Utime.Sec+usage.Stime.Sec) + float64(usage.Utime.Usec+usage.Stime.Usec)/1e6
}

func httpPerfRSS() uint64 {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if fields := strings.Fields(line); len(fields) == 3 && fields[0] == "VmRSS:" && fields[2] == "kB" {
			value, _ := strconv.ParseUint(fields[1], 10, 64)
			return value * 1024
		}
	}
	return 0
}

type httpPerfSampler struct {
	mu       sync.Mutex
	db       *sql.DB
	active   bool
	base     sql.DBStats
	cpuStart float64
	api      httpPerfAPI
}

func (s *httpPerfSampler) sampleLocked() {
	stats := s.db.Stats()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	s.api.HeapPeakBytes = max(s.api.HeapPeakBytes, memory.HeapAlloc)
	s.api.RSSPeakBytes = max(s.api.RSSPeakBytes, httpPerfRSS())
	s.api.Pool.InUsePeak = max(s.api.Pool.InUsePeak, stats.InUse)
	s.api.Pool.OpenConnectionsPeak = max(s.api.Pool.OpenConnectionsPeak, stats.OpenConnections)
}

func (s *httpPerfSampler) start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.api, s.base = httpPerfAPI{}, s.db.Stats()
	s.cpuStart = httpPerfCPUSeconds()
	s.active = true
	s.sampleLocked()
}

func (s *httpPerfSampler) finish() httpPerfAPI {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sampleLocked()
	s.active = false
	stats := s.db.Stats()
	s.api.CPUSeconds = httpPerfCPUSeconds() - s.cpuStart
	s.api.Pool.WaitCount = stats.WaitCount - s.base.WaitCount
	s.api.Pool.WaitDurationNS = int64(stats.WaitDuration - s.base.WaitDuration)
	return s.api
}

// Child mode has a private pipe protocol, not an HTTP debug route. The parent
// supplies only an owned fixture DSN over stdin; it is never put in argv/env.
func httpPerfWorker(b *testing.B) {
	b.Helper()
	pipe := os.NewFile(3, "http-performance-control")
	if pipe == nil {
		b.Fatal("performance worker control pipe unavailable")
	}
	defer pipe.Close()
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20))
	decoder.DisallowUnknownFields()
	encoder := json.NewEncoder(pipe)
	var config httpPerfWorkerConfig
	if decoder.Decode(&config) != nil || !httpPerfHex(config.SourceSHA, 20) || !strings.HasPrefix(config.Database, "aster_http_perf_test_") || !httpPerfHex(strings.TrimPrefix(config.Database, "aster_http_perf_test_"), 16) {
		b.Fatal("invalid performance worker configuration")
	}
	cfg, err := mysqlConfiguration(config.DSN, true)
	if err != nil || cfg.DBName != config.Database || cfg.Net != "unix" {
		b.Fatal("performance worker refused database outside its owned Unix fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	m, err := OpenMySQL(ctx, config.DSN, true)
	if err != nil {
		b.Fatal("performance worker cannot open owned database")
	}
	defer m.DB.Close()
	if err := httpPerfCheckMySQL(ctx, m.DB, cfg.Addr, filepath.Dir(cfg.Addr)); err != nil {
		b.Fatal(err)
	}
	var database string
	if m.DB.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&database) != nil || database != config.Database {
		b.Fatal("performance worker selected a different database")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		b.Fatal("performance worker cannot listen on loopback")
	}
	defer listener.Close()
	origin := "http://" + listener.Addr().String()
	app := NewApp(m, Config{Origin: origin, Secret: ID() + ID()})
	server := &http.Server{Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	defer server.Close()
	sampler := &httpPerfSampler{db: m.DB}
	stopSamples, samplesDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(samplesDone)
		ticker := time.NewTicker(httpPerfSamplePeriod)
		defer ticker.Stop()
		for {
			select {
			case <-stopSamples:
				return
			case <-ticker.C:
				sampler.mu.Lock()
				if sampler.active {
					sampler.sampleLocked()
				}
				sampler.mu.Unlock()
			}
		}
	}()
	defer func() { close(stopSamples); <-samplesDone }()
	harness, err := httpPerfHarnessHash()
	if err != nil {
		b.Fatal("performance worker cannot identify harness source")
	}
	ready := httpPerfWorkerMessage{Kind: "ready", URL: origin, SourceSHA: config.SourceSHA, HarnessSHA256: harness, GoVersion: runtime.Version(), GOMAXPROCS: runtime.GOMAXPROCS(0)}
	if encoder.Encode(ready) != nil {
		b.Fatal("performance worker cannot acknowledge readiness")
	}
	for {
		var command struct {
			Kind string `json:"kind"`
		}
		if decoder.Decode(&command) != nil {
			b.Fatal("performance worker control channel closed unexpectedly")
		}
		message := httpPerfWorkerMessage{Kind: command.Kind}
		switch command.Kind {
		case "start":
			sampler.start()
		case "finish":
			message.API = sampler.finish()
		case "shutdown":
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := server.Shutdown(shutdownCtx)
			shutdownCancel()
			if err != nil || !errors.Is(<-served, http.ErrServerClosed) {
				b.Fatal("performance worker did not stop cleanly")
			}
			if encoder.Encode(message) != nil {
				b.Fatal("performance worker cannot acknowledge shutdown")
			}
			return
		default:
			b.Fatal("unknown performance worker control command")
		}
		if encoder.Encode(message) != nil {
			b.Fatal("performance worker control write failed")
		}
	}
}

type httpPerfChild struct {
	command *exec.Cmd
	input   io.WriteCloser
	output  *os.File
	encoder *json.Encoder
	decoder *json.Decoder
	ready   httpPerfWorkerMessage
	waited  bool
}

func httpPerfStartChild(b *testing.B, ctx context.Context, binary, source, harness string, f *httpPerfFixture) *httpPerfChild {
	b.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		b.Fatal("create private performance worker pipe")
	}
	command := exec.CommandContext(ctx, binary, "-test.run=^$", "-test.bench=^BenchmarkHTTPPerformance$", "-test.benchtime=1x", "-test.timeout=15m")
	command.Env = []string{"ASTER_HTTP_PERF_WORKER=1", "LANG=C", "TZ=UTC"}
	// Explicit GOMAXPROCS, when requested by the fixture runner, applies equally
	// to the API and load generator and is recorded in the report.
	if value := os.Getenv("GOMAXPROCS"); value != "" {
		command.Env = append(command.Env, "GOMAXPROCS="+value)
	}
	command.ExtraFiles = []*os.File{writer}
	command.Stdout, command.Stderr = io.Discard, io.Discard
	input, err := command.StdinPipe()
	if err != nil {
		reader.Close()
		writer.Close()
		b.Fatal("create performance worker input")
	}
	child := &httpPerfChild{command: command, input: input, output: reader, encoder: json.NewEncoder(input), decoder: json.NewDecoder(reader)}
	if err := command.Start(); err != nil {
		input.Close()
		reader.Close()
		writer.Close()
		b.Fatal("start isolated API child process")
	}
	writer.Close()
	b.Cleanup(func() {
		if !child.waited {
			_ = child.command.Process.Kill()
			_ = child.command.Wait()
			_ = child.input.Close()
			_ = child.output.Close()
			child.waited = true
		}
	})
	if child.encoder.Encode(httpPerfWorkerConfig{f.dsn, f.name, source}) != nil || child.decoder.Decode(&child.ready) != nil || child.ready.Kind != "ready" || child.ready.SourceSHA != source || child.ready.HarnessSHA256 != harness || child.ready.GoVersion == "" || child.ready.GOMAXPROCS < 1 {
		b.Fatal("API child did not prove matching performance harness readiness")
	}
	address, err := net.ResolveTCPAddr("tcp4", strings.TrimPrefix(child.ready.URL, "http://"))
	if err != nil || !address.IP.Equal(net.IPv4(127, 0, 0, 1)) || address.Port == 0 {
		b.Fatal("API child returned a non-loopback listener")
	}
	return child
}

func (c *httpPerfChild) control(kind string) (httpPerfWorkerMessage, error) {
	var message httpPerfWorkerMessage
	if err := c.encoder.Encode(struct {
		Kind string `json:"kind"`
	}{kind}); err != nil {
		return message, errors.New("performance worker command failed")
	}
	if err := c.decoder.Decode(&message); err != nil || message.Kind != kind {
		return message, errors.New("performance worker response failed")
	}
	return message, nil
}

func (c *httpPerfChild) stop() error {
	if _, err := c.control("shutdown"); err != nil {
		return err
	}
	_ = c.input.Close()
	err := c.command.Wait()
	c.waited = true
	_ = c.output.Close()
	if err != nil {
		return errors.New("performance worker did not exit successfully")
	}
	return nil
}

func httpPerfClient(concurrency int) (*http.Client, *http.Transport) {
	transport := &http.Transport{
		Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns: concurrency, MaxIdleConnsPerHost: concurrency, MaxConnsPerHost: concurrency,
		IdleConnTimeout: 60 * time.Second, ResponseHeaderTimeout: 20 * time.Second, DisableCompression: true,
	}
	return &http.Client{Transport: transport, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, transport
}

func httpPerfRequest(client *http.Client, address, path, cookie string) (*http.Response, error) {
	request, err := http.NewRequest(http.MethodGet, address+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Cookie", "aster_demo_session="+cookie)
	request.Header.Set("Accept-Language", "en")
	return client.Do(request)
}

func httpPerfProbe(client *http.Client, address, path, cookie string, f *httpPerfFixture, p httpPerfParameters) error {
	response, err := httpPerfRequest(client, address, path, cookie)
	if err != nil {
		return errors.New("performance HTTP preflight transport failed")
	}
	defer response.Body.Close()
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&envelope) != nil || len(envelope.Data) == 0 {
		return errors.New("performance HTTP preflight did not return data")
	}
	switch path {
	case "/api/v1/session":
		var data struct {
			User User `json:"user"`
		}
		if json.Unmarshal(envelope.Data, &data) != nil || data.User.ID != f.customer {
			return errors.New("performance session is not authenticated")
		}
	case "/api/v1/orders":
		var orders []Order
		if json.Unmarshal(envelope.Data, &orders) != nil || len(orders) != min(1000, (p.Orders+p.Users-1)/p.Users) {
			return errors.New("performance order response does not match fixture")
		}
	case "/api/v1/admin/orders":
		// A successful admin-only response with this fixture's admin cookie,
		// plus the exact seeded order IDs, rules out measuring the smaller
		// owner-filtered customer list. Order JSON intentionally omits UserID;
		// unique fixture timestamps determine each expected order and its owner.
		var orders []Order
		if cookie != f.adminCookie || cookie == f.cookie || f.seed == "" || p.Users < 1 || p.Orders < 1 || json.Unmarshal(envelope.Data, &orders) != nil || len(orders) != min(1000, p.Orders) {
			return errors.New("performance admin order response does not match fixture")
		}
		owners := map[int]struct{}{}
		for index, order := range orders {
			ordinal := p.Orders - 1 - index
			if order.ID != httpPerfID(f.seed, fmt.Sprintf("order-%d", ordinal)) || (index > 0 && order.CreatedAt.After(orders[index-1].CreatedAt)) {
				return errors.New("performance admin order scope or ordering does not match fixture")
			}
			owners[ordinal%p.Users] = struct{}{}
		}
		if len(owners) != min(p.Users, len(orders)) {
			return errors.New("performance admin order customer coverage does not match fixture")
		}
	case "/api/v1/overview?currency=CNY":
		var data struct {
			Currency string  `json:"currency"`
			Pending  int     `json:"pending_invoices"`
			Recent   []Order `json:"recent_orders"`
		}
		if json.Unmarshal(envelope.Data, &data) != nil || data.Currency != "CNY" || data.Pending != (p.Orders+p.Users-1)/p.Users || len(data.Recent) != min(5, (p.Orders+p.Users-1)/p.Users) {
			return errors.New("performance customer overview does not match fixture")
		}
	case "/api/v1/admin/overview?currency=CNY":
		var data struct {
			Currency  string `json:"currency"`
			Customers int    `json:"customers"`
			Invoices  int    `json:"unpaid_invoices"`
			Services  int    `json:"total_services"`
		}
		if json.Unmarshal(envelope.Data, &data) != nil || data.Currency != "CNY" || data.Customers != p.Users || data.Invoices != p.Orders || data.Services != p.Services {
			return errors.New("performance admin overview does not match fixture")
		}
	default:
		return errors.New("unknown performance scenario")
	}
	return nil
}

// Closed-loop concurrency, fixed wall-clock issuance, then drain. QPS uses the
// actual elapsed interval including that drain; error latencies are included.
// The load generator is a different process from the API resource sampler.
func httpPerfLoad(client *http.Client, address, path, cookie string, concurrency int, duration time.Duration) httpPerfMeasurement {
	start := time.Now()
	deadline := start.Add(duration)
	type workerResult struct {
		latencies []float64
		transport int64
		http      int64
		statuses  map[string]int64
	}
	results := make(chan workerResult, concurrency)
	for n := 0; n < concurrency; n++ {
		go func() {
			result := workerResult{statuses: map[string]int64{}}
			for time.Now().Before(deadline) {
				requestStart := time.Now()
				response, err := httpPerfRequest(client, address, path, cookie)
				if err == nil {
					length, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, (16<<20)+1))
					closeErr := response.Body.Close()
					if readErr != nil || closeErr != nil || length > 16<<20 {
						err = errors.New("bounded HTTP response read failed")
					} else {
						// Status counts describe complete responses. A response
						// with an incomplete body is one transport failure, so
						// HTTP and transport errors are mutually exclusive.
						result.statuses[strconv.Itoa(response.StatusCode)]++
						if response.StatusCode != 200 {
							result.http++
						}
					}
				}
				if err != nil {
					result.transport++
				}
				result.latencies = append(result.latencies, float64(time.Since(requestStart))/float64(time.Millisecond))
			}
			results <- result
		}()
	}
	measurement := httpPerfMeasurement{Path: path, Concurrency: concurrency, HTTPStatusCounts: map[string]int64{}}
	var latencies []float64
	for n := 0; n < concurrency; n++ {
		result := <-results
		measurement.TransportErrors += result.transport
		measurement.HTTPErrors += result.http
		for code, count := range result.statuses {
			measurement.HTTPStatusCounts[code] += count
		}
		latencies = append(latencies, result.latencies...)
	}
	measurement.DurationNS = int64(time.Since(start))
	measurement.Requests = int64(len(latencies))
	measurement.QPS = float64(measurement.Requests) / time.Duration(measurement.DurationNS).Seconds()
	if measurement.Requests > 0 {
		measurement.ErrorRate = float64(measurement.HTTPErrors+measurement.TransportErrors) / float64(measurement.Requests)
		sort.Float64s(latencies)
		percentile := func(percent int) float64 { return latencies[(len(latencies)*percent+99)/100-1] }
		measurement.LatencyMS = httpPerfLatency{percentile(50), percentile(95), percentile(99)}
	}
	return measurement
}

func httpPerfLockStatus(ctx context.Context, db *sql.DB) (map[string]uint64, error) {
	rows, err := db.QueryContext(ctx, "SHOW GLOBAL STATUS WHERE Variable_name IN ('Innodb_row_lock_waits','Innodb_row_lock_time','Table_locks_waited')")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]uint64{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		number, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return nil, err
		}
		result[name] = number
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) != 3 {
		return nil, errors.New("MySQL lock counters unavailable")
	}
	return result, nil
}

type httpPerfPlanTable struct {
	Table      string  `json:"table_name"`
	Access     string  `json:"access_type"`
	Key        string  `json:"key"`
	Examined   uint64  `json:"rows_examined_per_scan"`
	Produced   uint64  `json:"rows_produced_per_join"`
	Filtered   float64 `json:"filtered"`
	UsingIndex bool    `json:"using_index"`
}

// Do not retain attached_condition, messages, literals, or arbitrary plan
// strings: EXPLAIN can otherwise disclose authentication parameter values.
func httpPerfSafePlan(raw []byte) (json.RawMessage, error) {
	var input any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&input); err != nil {
		return nil, err
	}
	output := struct {
		Cost   float64             `json:"query_cost"`
		Tables []httpPerfPlanTable `json:"tables"`
	}{Tables: []httpPerfPlanTable{}}
	number := func(value any) float64 { parsed, _ := strconv.ParseFloat(fmt.Sprint(value), 64); return parsed }
	identifier := func(value any) string {
		text, _ := value.(string)
		if text == "" || len(text) > 64 || strings.Trim(text, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_") != "" {
			return ""
		}
		return text
	}
	var walk func(any)
	walk = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			if cost, ok := node["query_cost"]; ok {
				output.Cost = number(cost)
			}
			if table := identifier(node["table_name"]); table != "" {
				usingIndex, _ := node["using_index"].(bool)
				output.Tables = append(output.Tables, httpPerfPlanTable{table, identifier(node["access_type"]), identifier(node["key"]), uint64(number(node["rows_examined_per_scan"])), uint64(number(node["rows_produced_per_join"])), number(node["filtered"]), usingIndex})
			}
			keys := make([]string, 0, len(node))
			for key := range node {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				walk(node[key])
			}
		case []any:
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(input)
	return json.Marshal(output)
}

func httpPerfPlans(ctx context.Context, f *httpPerfFixture) ([]httpPerfExplain, error) {
	service := "SELECT COUNT(*),COALESCE(SUM(status='active'),0),COALESCE(SUM(CASE WHEN status='active' THEN price_minor ELSE 0 END),0) FROM services WHERE currency=?"
	invoice := "SELECT COALESCE(SUM(status='unpaid'),0),COALESCE(SUM(CASE WHEN status='paid' THEN amount_minor ELSE 0 END),0),COALESCE(SUM(CASE WHEN status='unpaid' THEN amount_minor ELSE 0 END),0) FROM invoices WHERE currency=?"
	queries := []struct {
		name, query string
		args        []any
	}{
		{"session_lookup", "SELECT token_hash,user_id,expires_at FROM sessions WHERE token_hash=? AND expires_at>UTC_TIMESTAMP(6)", []any{digest(f.cookie)}},
		{"user_lookup", "SELECT id,email,name,role,password_hash FROM users WHERE id=?", []any{f.customer}},
		{"currency_configuration", "SELECT document,prices_revision FROM currency_settings WHERE id=1", nil},
		{"customer_orders", "SELECT " + orderColumns + " FROM orders WHERE user_id=? ORDER BY created_at DESC LIMIT 1000", []any{f.customer}},
		{"admin_orders", "SELECT " + orderColumns + " FROM orders ORDER BY created_at DESC LIMIT 1000", nil},
		{"customer_recent_orders", "SELECT " + orderColumns + " FROM orders WHERE user_id=? ORDER BY created_at DESC,id DESC LIMIT 5", []any{f.customer}},
		{"customer_services_summary", service + " AND user_id=?", []any{"CNY", f.customer}},
		{"customer_invoices_summary", invoice + " AND user_id=?", []any{"CNY", f.customer}},
		{"admin_services_summary", service, []any{"CNY"}},
		{"admin_invoices_summary", invoice, []any{"CNY"}},
		{"admin_customer_count", "SELECT COUNT(*) FROM users WHERE role='customer'", nil},
	}
	var plans []httpPerfExplain
	for _, query := range queries {
		var raw []byte
		if err := f.m.DB.QueryRowContext(ctx, "EXPLAIN FORMAT=JSON "+query.query, query.args...).Scan(&raw); err != nil {
			return nil, err
		}
		plan, err := httpPerfSafePlan(raw)
		if err != nil {
			return nil, err
		}
		plans = append(plans, httpPerfExplain{query.name, query.query, plan})
	}
	return plans, nil
}

func httpPerfMachine() map[string]string {
	read := func(path string) string {
		raw, err := os.ReadFile(path)
		if err != nil {
			return "unavailable"
		}
		value := strings.TrimSpace(string(raw))
		if len(value) > 256 {
			value = value[:256]
		}
		return value
	}
	field := func(path, key string) string {
		file, err := os.Open(path)
		if err != nil {
			return "unavailable"
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			name, value, ok := strings.Cut(scanner.Text(), ":")
			if ok && strings.TrimSpace(name) == key {
				return strings.TrimSpace(value)
			}
		}
		return "unavailable"
	}
	return map[string]string{
		"os": runtime.GOOS, "arch": runtime.GOARCH, "cpu_model": field("/proc/cpuinfo", "model name"),
		"logical_cpus": strconv.Itoa(runtime.NumCPU()), "mem_total": field("/proc/meminfo", "MemTotal"),
		"cgroup_cpu_max": read("/sys/fs/cgroup/cpu.max"), "cgroup_memory_max": read("/sys/fs/cgroup/memory.max"), "cgroup_cpuset": read("/sys/fs/cgroup/cpuset.cpus.effective"),
		"loadgen_go_version": runtime.Version(), "loadgen_gomaxprocs": strconv.Itoa(runtime.GOMAXPROCS(0)),
	}
}

func BenchmarkHTTPPerformance(b *testing.B) {
	b.StopTimer()
	if os.Getenv("ASTER_HTTP_PERF_WORKER") == "1" {
		httpPerfWorker(b)
		return
	}
	httpPerfStage("configuration")
	if b.N != 1 || runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		b.Fatal("performance fixture requires Linux amd64 and -benchtime=1x")
	}
	source := os.Getenv("ASTER_HTTP_PERF_SOURCE_SHA")
	if !httpPerfHex(source, 20) {
		b.Fatal("ASTER_HTTP_PERF_SOURCE_SHA must identify the candidate production source")
	}
	harness, err := httpPerfHarnessHash()
	if err != nil {
		b.Fatal("identify performance harness source")
	}
	seed := os.Getenv("ASTER_HTTP_PERF_SEED")
	if seed == "" {
		seed = "aster-http-performance-v1"
	}
	if len(seed) > 64 || !paymentToken(seed, 64) {
		b.Fatal("performance seed must be bounded printable ASCII")
	}
	users := httpPerfInt(b, "ASTER_HTTP_PERF_USERS", 100, 10, 1000)
	parameters := httpPerfParameters{
		Users: users, Orders: httpPerfInt(b, "ASTER_HTTP_PERF_ORDERS", 10000, 100, 100000), Services: users * 10, Concurrency: []int{1, 8, 32}, Repeats: httpPerfInt(b, "ASTER_HTTP_PERF_REPEATS", 2, 2, 5),
		WarmupNS: int64(httpPerfDuration(b, "ASTER_HTTP_PERF_WARMUP", time.Second, 100*time.Millisecond, 5*time.Second)), DurationNS: int64(httpPerfDuration(b, "ASTER_HTTP_PERF_DURATION", 3*time.Second, 500*time.Millisecond, 30*time.Second)), SamplingNS: int64(httpPerfSamplePeriod),
		HTTPTimeoutsNS: map[string]int64{"read_header": int64(5 * time.Second), "read": int64(15 * time.Second), "write": int64(20 * time.Second), "idle": int64(60 * time.Second), "request": int64(12 * time.Second)},
	}
	resultPath := os.Getenv("ASTER_HTTP_PERF_RESULT_PATH")
	if !filepath.IsAbs(resultPath) {
		b.Fatal("ASTER_HTTP_PERF_RESULT_PATH must be an absolute new report file")
	}
	if _, err := os.Lstat(resultPath); !errors.Is(err, os.ErrNotExist) {
		b.Fatal("performance report must not replace an existing file")
	}
	binary, err := os.Executable()
	if err != nil {
		b.Fatal("identify candidate test binary")
	}
	binaryHash, err := httpPerfFileHash(binary)
	if err != nil {
		b.Fatal("hash candidate test binary")
	}
	type epochSpec struct{ label, binary, source, hash string }
	specs := []epochSpec{{"candidate", binary, source, binaryHash}}
	baseline := os.Getenv("ASTER_HTTP_PERF_BASELINE_BINARY")
	if baseline != "" {
		baselineSource, expectedHash := os.Getenv("ASTER_HTTP_PERF_BASELINE_SOURCE_SHA"), os.Getenv("ASTER_HTTP_PERF_BASELINE_BINARY_SHA256")
		info, statErr := os.Lstat(baseline)
		actualHash, hashErr := httpPerfFileHash(baseline)
		if !filepath.IsAbs(baseline) || statErr != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 || !httpPerfHex(baselineSource, 20) || !httpPerfHex(expectedHash, 32) || hashErr != nil || actualHash != expectedHash {
			b.Fatal("baseline must be the runner-built source/harness-bound executable with matching SHA256")
		}
		specs = []epochSpec{{"baseline_before", baseline, baselineSource, actualHash}, specs[0], {"baseline_after", baseline, baselineSource, actualHash}}
	} else if os.Getenv("ASTER_HTTP_PERF_BASELINE_SOURCE_SHA") != "" || os.Getenv("ASTER_HTTP_PERF_BASELINE_BINARY_SHA256") != "" {
		b.Fatal("incomplete baseline identity")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()
	httpPerfStage("database")
	f := httpPerfDatabase(b, ctx)
	httpPerfStage("seed")
	if err := httpPerfSeed(ctx, f, seed, parameters); err != nil {
		b.Fatal("seed deterministic synthetic performance history")
	}
	httpPerfStage("hash")
	dataset, err := httpPerfDatasetHash(ctx, f.m.DB)
	if err != nil {
		b.Fatal("hash complete seeded performance database")
	}
	report := httpPerfReport{Schema: httpPerfSchema, SourceSHA: source, HarnessSHA256: harness, Seed: seed, Parameters: parameters, Machine: httpPerfMachine(), Dataset: dataset, Epochs: []httpPerfEpoch{}}
	if f.m.DB.QueryRowContext(ctx, "SELECT VERSION()").Scan(&report.MySQLVersion) != nil {
		b.Fatal("read synthetic MySQL version")
	}
	report.Explain, err = httpPerfPlans(ctx, f)
	if err != nil {
		b.Fatal("collect reviewed read-only SQL plans")
	}
	paths := []string{"/api/v1/session", "/api/v1/orders", "/api/v1/overview?currency=CNY", "/api/v1/admin/overview?currency=CNY", "/api/v1/admin/orders"}
	hadErrors := false
	for _, spec := range specs {
		httpPerfStage("hash")
		before, err := httpPerfDatasetHash(ctx, f.m.DB)
		if err != nil || before.SHA256 != dataset.SHA256 {
			b.Fatal("performance database changed before epoch")
		}
		httpPerfStage("worker")
		child := httpPerfStartChild(b, ctx, spec.binary, spec.source, harness, f)
		epoch := httpPerfEpoch{Label: spec.label, SourceSHA: spec.source, BinarySHA256: spec.hash, GoVersion: child.ready.GoVersion, GOMAXPROCS: child.ready.GOMAXPROCS, DatasetSHA256: before.SHA256, Measurements: []httpPerfMeasurement{}}
		for _, path := range paths {
			cookie := f.cookie
			if strings.HasPrefix(path, "/api/v1/admin/") {
				cookie = f.adminCookie
			}
			for _, concurrency := range parameters.Concurrency {
				client, transport := httpPerfClient(concurrency)
				httpPerfStage("preflight")
				if err := httpPerfProbe(client, child.ready.URL, path, cookie, f, parameters); err != nil {
					transport.CloseIdleConnections()
					b.Fatal(err)
				}
				for repeat := 1; repeat <= parameters.Repeats; repeat++ {
					httpPerfStage("warmup")
					warmup := httpPerfLoad(client, child.ready.URL, path, cookie, concurrency, time.Duration(parameters.WarmupNS))
					if warmup.Requests == 0 || warmup.TransportErrors != 0 || warmup.HTTPErrors != 0 {
						transport.CloseIdleConnections()
						b.Fatal("HTTP warmup failed; benchmark is not a valid success baseline")
					}
					httpPerfStage("measurement")
					beforeLocks, err := httpPerfLockStatus(ctx, f.m.DB)
					if err != nil {
						b.Fatal("read initial isolated MySQL global lock counters")
					}
					if _, err := child.control("start"); err != nil {
						b.Fatal(err)
					}
					measurement := httpPerfLoad(client, child.ready.URL, path, cookie, concurrency, time.Duration(parameters.DurationNS))
					metrics, err := child.control("finish")
					if err != nil {
						b.Fatal(err)
					}
					measurement.API, measurement.Repeat = metrics.API, repeat
					afterLocks, err := httpPerfLockStatus(ctx, f.m.DB)
					if err != nil {
						b.Fatal("read final isolated MySQL global lock counters")
					}
					measurement.MySQLLockDelta = map[string]uint64{}
					for key, after := range afterLocks {
						if after < beforeLocks[key] {
							b.Fatal("MySQL global lock counters reset during experiment")
						}
						measurement.MySQLLockDelta[key] = after - beforeLocks[key]
					}
					if measurement.Requests == 0 || measurement.TransportErrors != 0 || measurement.HTTPErrors != 0 || measurement.API.RSSPeakBytes == 0 || measurement.API.HeapPeakBytes == 0 || measurement.API.CPUSeconds < 0 {
						hadErrors = true
					}
					epoch.Measurements = append(epoch.Measurements, measurement)
				}
				transport.CloseIdleConnections()
			}
		}
		httpPerfStage("cleanup")
		if child.stop() != nil {
			b.Fatal("join isolated API child")
		}
		httpPerfStage("hash")
		after, err := httpPerfDatasetHash(ctx, f.m.DB)
		if err != nil || after.SHA256 != dataset.SHA256 {
			b.Fatal("read-only HTTP benchmark changed persisted data")
		}
		report.Epochs = append(report.Epochs, epoch)
	}
	report.Dataset.BeforeAfterEqual = true
	report.Cleanup.WorkersStopped = true
	httpPerfStage("cleanup")
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	err = f.close(cleanupCtx)
	cleanupCancel()
	if err != nil {
		b.Fatal("confirm owned performance database removal")
	}
	report.Cleanup.DatabaseDropped = true
	httpPerfStage("report")
	output, err := os.OpenFile(resultPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		b.Fatal("create private new performance report")
	}
	encodeErr := json.NewEncoder(output).Encode(report)
	closeErr := output.Close()
	if encodeErr != nil || closeErr != nil {
		b.Fatal("write performance report")
	}
	b.Log("ASTER_HTTP_PERF_RESULT", resultPath)
	if hadErrors {
		b.Fatal("performance report contains request errors or unavailable API resource measurements")
	}
}
