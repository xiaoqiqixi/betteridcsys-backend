package billing

import (
	"aster/billing/internal/integrations"
	"aster/billing/plugins/smtp"
	"bufio"
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

type observationLog struct {
	mu     sync.Mutex
	output bytes.Buffer
}

func (l *observationLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.output.Write(data)
}
func (l *observationLog) text() string { l.mu.Lock(); defer l.mu.Unlock(); return l.output.String() }
func (l *observationLog) events(t *testing.T) []map[string]any {
	t.Helper()
	events := []map[string]any{}
	scanner := bufio.NewScanner(strings.NewReader(l.text()))
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal("invalid JSON log", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}
func observedApp(store Store) (*App, *observationLog) {
	log := &observationLog{}
	return NewApp(store, Config{Logger: slog.New(slog.NewJSONHandler(log, nil)), Demo: true, Origin: "http://localhost", Secret: ID() + ID()}), log
}
func onlyObservation(t *testing.T, log *observationLog) map[string]any {
	t.Helper()
	events := log.events(t)
	if len(events) != 1 {
		t.Fatalf("want one completion log, got %d", len(events))
	}
	event := events[0]
	if event["msg"] != "http_request_completed" {
		t.Fatal("unexpected event", event)
	}
	return event
}
func assertLogDoesNotContain(t *testing.T, log *observationLog, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(log.text(), secret) {
			t.Fatalf("private canary reached log: %q", secret)
		}
	}
}
func TestHTTPObservabilityCompletionRedaction(t *testing.T) {
	app, log := observedApp(NewMemory())
	r := httptest.NewRequest("GET", "http://localhost/api/v1/plans?token=QUERY_SECRET", strings.NewReader("BODY_SECRET"))
	r.Header.Set("Authorization", "Bearer AUTH_SECRET")
	r.Header.Set("Cookie", "SESSION_SECRET")
	r.Header.Set("X-CSRF-Token", "CSRF_SECRET")
	r.Header.Set("Idempotency-Key", "IDEMPOTENCY_SECRET")
	r.Header.Set("X-Request-ID", "CLIENT_REQUEST_ID")
	r.Header.Set("User-Agent", "AGENT_SECRET")
	r.RemoteAddr = "192.0.2.111:54321"
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	event := onlyObservation(t, log)
	if w.Code != 200 || event["level"] != "INFO" || event["route"] != "/api/v1/plans" || event["method"] != "GET" || event["status"] != float64(200) || event["bytes"] != float64(w.Body.Len()) || event["duration_ms"].(float64) < 0 {
		t.Fatal("wrong completion", event)
	}
	requestID := w.Header().Get("X-Request-ID")
	if len(requestID) != 32 || event["request_id"] != requestID {
		t.Fatal("request ID not server-owned/correlated", event)
	}
	assertLogDoesNotContain(t, log, "QUERY_SECRET", "BODY_SECRET", "AUTH_SECRET", "SESSION_SECRET", "CSRF_SECRET", "IDEMPOTENCY_SECRET", "CLIENT_REQUEST_ID", "AGENT_SECRET", "192.0.2.111")
}

type observationFailureStore struct {
	Store
	pingErr, servicesErr error
}

func (s observationFailureStore) Ping(context.Context) error { return s.pingErr }
func (s observationFailureStore) Services(context.Context, string) ([]Service, error) {
	return nil, s.servicesErr
}
func TestHTTPObservabilityReadinessFailure(t *testing.T) {
	app, log := observedApp(observationFailureStore{Store: NewMemory(), pingErr: errors.New("MYSQL_DSN_SECRET")})
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/readyz", nil))
	event := onlyObservation(t, log)
	if w.Code != 503 || event["level"] != "ERROR" || event["error_code"] != "NOT_READY" || event["error_class"] != "storage_error" {
		t.Fatal("failed readiness not observable", event)
	}
	assertLogDoesNotContain(t, log, "MYSQL_DSN_SECRET")
}
func TestHTTPObservabilityHealthAndEarlyReturns(t *testing.T) {
	for _, path := range []string{"/healthz", "/readyz"} {
		t.Run(path, func(t *testing.T) {
			app, log := observedApp(NewMemory())
			w := httptest.NewRecorder()
			app.Handler().ServeHTTP(w, httptest.NewRequest("GET", "http://localhost"+path, nil))
			if w.Code != 200 || len(log.events(t)) != 0 {
				t.Fatal("healthy probe log noise")
			}
		})
	}
	for _, tc := range []struct {
		path, method, host, route, code string
		status                          int
	}{
		{"/api/v1/services", "GET", "localhost", "/api/v1/services", "AUTH_REQUIRED", 401},
		{"/api/v1/orders", "POST", "localhost", "/api/v1/orders", "CSRF_INVALID", 403},
		{"/api/v1/plans", "PUT", "localhost", "/api/v1/plans", "METHOD_NOT_ALLOWED", 405},
		{"/api/v1/plans", "METHOD_SECRET", "localhost", "/api/v1/plans", "METHOD_NOT_ALLOWED", 405},
		{"/PATH_SECRET?query=QUERY_SECRET", "GET", "localhost", "unmatched", "NOT_FOUND", 404},
		{"/api/v1/plans", "GET", "HOST_SECRET.invalid", "/api/v1/plans", "INVALID_HOST", 421},
	} {
		t.Run(tc.code+tc.method, func(t *testing.T) {
			app, log := observedApp(NewMemory())
			r := httptest.NewRequest(tc.method, "http://localhost"+tc.path, nil)
			r.Host = tc.host
			w := httptest.NewRecorder()
			app.Handler().ServeHTTP(w, r)
			event := onlyObservation(t, log)
			if w.Code != tc.status || event["status"] != float64(tc.status) || event["level"] != "WARN" || event["route"] != tc.route || event["error_code"] != tc.code {
				t.Fatal("early-return completion", event)
			}
			if tc.method == "METHOD_SECRET" && event["method"] != "OTHER" {
				t.Fatal("unknown method logged verbatim")
			}
			assertLogDoesNotContain(t, log, "METHOD_SECRET", "PATH_SECRET", "QUERY_SECRET", "HOST_SECRET")
		})
	}
}
func TestHTTPObservabilityAuthenticationAndActors(t *testing.T) {
	m := NewMemory()
	app, log := observedApp(m)
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	customer := client(t, server.URL, "demo@aster.local", "DemoCustomer!2026")
	owner, err := m.UserByEmail(t.Context(), "demo@aster.local")
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := customer.request("GET", "/api/v1/services", nil, ""); status != 200 {
		t.Fatal(status)
	}
	if status, _ := customer.request("GET", "/api/v1/admin/services", nil, ""); status != 403 {
		t.Fatal(status)
	}
	if status, _ := customer.request("POST", "/api/v1/auth/logout", nil, ""); status != 200 {
		t.Fatal(status)
	}
	anonymous := client(t, server.URL, "", "")
	if status, _ := anonymous.request("POST", "/api/v1/auth/login", map[string]string{"email": "EMAIL_SECRET@example.invalid", "password": "PASSWORD_SECRET"}, ""); status != 401 {
		t.Fatal(status)
	}
	events := log.events(t)
	if len(events) != 7 {
		t.Fatalf("auth/session/owner completion events lost: %d", len(events))
	}
	seen := map[string]bool{}
	for _, event := range events {
		id := event["request_id"].(string)
		if seen[id] {
			t.Fatal("duplicate completion", id)
		}
		seen[id] = true
		switch event["route"] {
		case "/api/v1/auth/login":
			if event["status"] == float64(200) {
				if event["actor_id"] != owner.ID || event["actor_role"] != "customer" {
					t.Fatal("login actor missing", event)
				}
			} else if _, exists := event["actor_id"]; exists {
				t.Fatal("failed login fabricated identity", event)
			}
		case "/api/v1/services", "/api/v1/admin/services":
			if event["actor_id"] != owner.ID || event["actor_role"] != "customer" {
				t.Fatal("resolved actor not retained", event)
			}
		case "/api/v1/auth/logout":
			if _, exists := event["actor_id"]; exists {
				t.Fatal("logout fabricated identity", event)
			}
		}
	}
	assertLogDoesNotContain(t, log, "demo@aster.local", "DemoCustomer!2026", "EMAIL_SECRET", "PASSWORD_SECRET")
}
func TestHTTPObservabilityInternalPaymentAndCurrencyErrors(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		code, class string
		call        func(*App, http.ResponseWriter, *http.Request)
	}{
		{"internal", 500, "INTERNAL_ERROR", "database_deadlock", func(a *App, w http.ResponseWriter, r *http.Request) {
			a.internal(w, r, fmt.Errorf("SQL_SECRET: %w", &mysql.MySQLError{Number: 1213, Message: "MYSQL_SECRET"}))
		}},
		{"payment", 503, "PAYMENT_STORAGE_UNAVAILABLE", "canceled", func(a *App, w http.ResponseWriter, r *http.Request) {
			a.paymentStoreError(w, r, fmt.Errorf("PAYMENT_SECRET: %w", context.Canceled))
		}},
		{"currency", 409, "CURRENCY_IN_USE", "http_error", func(a *App, w http.ResponseWriter, r *http.Request) {
			a.currencyError(w, r, &CurrencyInUse{Code: "USD"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, log := observedApp(NewMemory())
			w := httptest.NewRecorder()
			app.observeHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { tc.call(app, w, r) })).ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/api/v1/services", nil))
			event := onlyObservation(t, log)
			if w.Code != tc.status || event["error_code"] != tc.code || event["error_class"] != tc.class {
				t.Fatal("diagnostic mismatch", event)
			}
			assertLogDoesNotContain(t, log, "SQL_SECRET", "MYSQL_SECRET", "PAYMENT_SECRET")
		})
	}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, "timeout"}, {context.Canceled, "canceled"}, {driver.ErrBadConn, "database_connection"},
		{&mysql.MySQLError{Number: 1205}, "database_lock_timeout"}, {&mysql.MySQLError{Number: 1062}, "database_duplicate"},
		{&mysql.MySQLError{Number: 1045}, "database_permission"}, {&mysql.MySQLError{Number: 1146}, "database_schema"},
		{&mysql.MySQLError{Number: 9999, Message: "MESSAGE_SECRET"}, "database_error"}, {errors.New("ERROR_SECRET"), "storage_error"},
	} {
		if got := errorKind(fmt.Errorf("WRAPPER_SECRET: %w", tc.err)); got != tc.want {
			t.Fatalf("error category %s != %s", got, tc.want)
		}
	}
}
func TestHTTPObservabilityRouteTemplates(t *testing.T) {
	for path, want := range map[string]string{
		"/api/v1/catalog-taxonomy":                  "/api/v1/catalog-taxonomy",
		"/api/v1/admin/catalog-taxonomy":            "/api/v1/admin/catalog-taxonomy",
		"/api/v1/admin/catalog-taxonomy/ID_SECRET":  "unmatched",
		"/api/v1/invoices/ID_SECRET":                "/api/v1/invoices/{invoice_id}",
		"/api/v1/invoices/ID_SECRET/checkout":       "/api/v1/invoices/{invoice_id}/checkout",
		"/api/v1/admin/invoices/ID_SECRET":          "/api/v1/admin/invoices/{invoice_id}",
		"/api/v1/orders/ID_SECRET/cancel":           "/api/v1/orders/{order_id}/cancel",
		"/api/v1/admin/orders/ID_SECRET/cancel":     "/api/v1/admin/orders/{order_id}/cancel",
		"/api/v1/payment-callbacks/PROVIDER_SECRET": "/api/v1/payment-callbacks/{provider_id}",
		"/api/v1/invoices/ID_SECRET/extra":          "unmatched", "/api/v1/orders/ID_SECRET/nope": "unmatched", "/UNKNOWN_SECRET": "unmatched",
	} {
		t.Run(path, func(t *testing.T) {
			app, log := observedApp(NewMemory())
			w := httptest.NewRecorder()
			app.Handler().ServeHTTP(w, httptest.NewRequest("GET", "http://localhost"+path+"?token=QUERY_SECRET", nil))
			if event := onlyObservation(t, log); event["route"] != want {
				t.Fatal("route template", event)
			}
			assertLogDoesNotContain(t, log, "ID_SECRET", "PROVIDER_SECRET", "UNKNOWN_SECRET", "QUERY_SECRET")
		})
	}
}
func TestHTTPObservabilityPanicBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name           string
		partial, abort bool
	}{
		{"before_headers", false, false}, {"partial_body", true, false}, {"abort_before_headers", false, true}, {"abort_after_headers", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, log := observedApp(NewMemory())
			w := httptest.NewRecorder()
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				app.observeHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if tc.partial {
						w.WriteHeader(202)
						_, _ = w.Write([]byte("partial"))
					}
					if tc.abort {
						panic(http.ErrAbortHandler)
					}
					panic("PANIC_SECRET")
				})).ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/api/v1/services", nil))
			}()
			event := onlyObservation(t, log)
			if event["level"] != "ERROR" {
				t.Fatal("panic not error", event)
			}
			if tc.partial || tc.abort {
				if recovered != http.ErrAbortHandler || event["aborted"] != true {
					t.Fatal("abort semantics lost", recovered, event)
				}
				if tc.partial {
					if w.Code != 202 || w.Body.String() != "partial" || event["status"] != float64(202) || event["bytes"] != float64(7) {
						t.Fatal("partial response was rewritten", event, w.Body.String())
					}
				}
				if tc.abort && !tc.partial {
					if w.Body.Len() != 0 || event["status"] != float64(0) {
						t.Fatal("uncommitted abort fabricated response", event)
					}
				}
			} else {
				if recovered != nil || w.Code != 500 || event["error_code"] != "INTERNAL_ERROR" {
					t.Fatal("uncommitted panic not safe 500", recovered, event)
				}
				var response map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal("not complete JSON500", err)
				}
				if response["error"].(map[string]any)["request_id"] != event["request_id"] {
					t.Fatal("panic response not correlated")
				}
			}
			if !tc.abort {
				frames := event["stack"].([]any)
				if event["panic"] != true || len(frames) == 0 || len(frames) > 16 {
					t.Fatal("safe panic stack missing", event)
				}
				for _, raw := range frames {
					frame := raw.(map[string]any)
					file := frame["file"].(string)
					if filepath.Base(file) != file || len(frame) != 3 || frame["function"] == "" {
						t.Fatal("unsafe frame", frame)
					}
				}
			} else if _, exists := event["stack"]; exists {
				t.Fatal("sentinel abort treated as application panic")
			}
			assertLogDoesNotContain(t, log, "PANIC_SECRET")
		})
	}
}

type shortObservationWriter struct {
	header http.Header
	status int
}

func (w *shortObservationWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *shortObservationWriter) WriteHeader(status int) { w.status = status }
func (w *shortObservationWriter) Write(data []byte) (int, error) {
	return min(len(data), 2), errors.New("WRITE_SECRET")
}
func TestHTTPObservabilityWriterAndController(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := &observedWriter{ResponseWriter: recorder}
	if writer.Unwrap() != recorder {
		t.Fatal("unwrap lost writer")
	}
	if _, ok := any(writer).(http.Flusher); ok {
		t.Fatal("wrapper invented optional Flusher")
	}
	writer.WriteHeader(103)
	if writer.status != 0 {
		t.Fatal("informational header counted as final")
	}
	writer.WriteHeader(201)
	writer.WriteHeader(500)
	if writer.status != 201 {
		t.Fatal("repeated status overwrote first")
	}
	recorder = httptest.NewRecorder()
	writer = &observedWriter{ResponseWriter: recorder}
	_, _ = writer.Write([]byte("hello"))
	if writer.status != 200 || writer.bytes != 5 || recorder.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatal("implicit response or content sniffing changed")
	}
	recorder = httptest.NewRecorder()
	writer = &observedWriter{ResponseWriter: recorder}
	if err := http.NewResponseController(writer).Flush(); err != nil || writer.status != 200 || !recorder.Flushed {
		t.Fatal("controller flush lost", err)
	}
	bare := &shortObservationWriter{}
	writer = &observedWriter{ResponseWriter: bare}
	if err := http.NewResponseController(writer).Flush(); !errors.Is(err, http.ErrNotSupported) || writer.status != 0 {
		t.Fatal("unsupported flush fabricated commit", err)
	}
	app, log := observedApp(NewMemory())
	app.observeHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("full body")) })).ServeHTTP(bare, httptest.NewRequest("GET", "http://localhost/api/v1/services", nil))
	event := onlyObservation(t, log)
	if event["status"] != float64(200) || event["bytes"] != float64(2) || event["write_failed"] != true || event["error_class"] != "response_write_error" || event["level"] != "ERROR" {
		t.Fatal("partial write not observable", event)
	}
	assertLogDoesNotContain(t, log, "WRITE_SECRET", "full body")
}
func TestHTTPObservabilityCancellationAndConcurrency(t *testing.T) {
	app, log := observedApp(NewMemory())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	app.observeHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://localhost/api/v1/services", nil).WithContext(ctx))
	event := onlyObservation(t, log)
	if event["error_class"] != "canceled" || event["status"] != float64(204) || event["level"] != "ERROR" {
		t.Fatal("cancellation concealed", event)
	}
	app, log = observedApp(NewMemory())
	var group sync.WaitGroup
	for n := 0; n < 32; n++ {
		group.Add(1)
		go func() {
			defer group.Done()
			app.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://localhost/api/v1/plans", nil))
		}()
	}
	group.Wait()
	events := log.events(t)
	if len(events) != 32 {
		t.Fatal("concurrent completion count", len(events))
	}
	ids := map[string]bool{}
	for _, event := range events {
		id := event["request_id"].(string)
		if ids[id] || event["status"] != float64(200) {
			t.Fatal("request state crossed requests", event)
		}
		ids[id] = true
	}
}
func TestHTTPObservabilitySMTPFinalAuditFailure(t *testing.T) {
	m := emailTestMemory()
	app, log := observedApp(m)
	app.SMTP = newSMTPService(m, emailTestCipher(t))
	if _, err := app.SMTP.Update(t.Context(), emailTestActor, emailTestUpdate()); err != nil {
		t.Fatal(err)
	}
	app.SMTP.store = emailAuditFailureStore{Store: m}
	calls := 0
	app.SMTP.sender = emailSenderFunc(func(context.Context, smtp.Config, smtp.Message) error { calls++; return nil })
	registry := integrations.Default().WithManagedEmail(app.SMTP)
	w := httptest.NewRecorder()
	app.observeHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observeActor(r.Context(), emailTestActor)
		if err := app.SMTP.Test(r.Context(), registry, emailTestActor, 2, "RECIPIENT_SECRET@example.invalid"); err != nil {
			t.Fatal("accepted send downgraded", err)
		}
		jsonOut(w, 200, map[string]bool{"accepted": true})
	})).ServeHTTP(w, httptest.NewRequest("POST", "http://localhost/api/v1/admin/email-settings/test", nil))
	if calls != 1 || w.Code != 200 {
		t.Fatal("audit failure retried/failed acceptance", calls, w.Code)
	}
	events := log.events(t)
	if len(events) != 2 {
		t.Fatal("audit failure and completion required", len(events))
	}
	audit, completion := events[0], events[1]
	if audit["msg"] != "smtp_test_final_audit_failed" || audit["level"] != "ERROR" || audit["outcome"] != "test_accepted" || audit["revision"] != float64(2) || audit["actor_id"] != emailTestActor.ID || audit["request_id"] != completion["request_id"] || audit["route"] != "/api/v1/admin/email-settings/test" || completion["status"] != float64(200) {
		t.Fatal("uncorrelated acceptance audit failure", events)
	}
	assertLogDoesNotContain(t, log, "RECIPIENT_SECRET", "admin@example.invalid", "synthetic-password", "synthetic-account", "synthetic audit failure")
}

func TestHTTPObservabilityRecoveryHTTPWire(t *testing.T) {
	for _, mode := range []string{"panic_before_headers", "panic_after_flush", "sentinel_abort"} {
		t.Run(mode, func(t *testing.T) {
			app, log := observedApp(NewMemory())
			server := httptest.NewServer(app.observeHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "sentinel_abort" {
					panic(http.ErrAbortHandler)
				}
				if mode == "panic_after_flush" {
					w.WriteHeader(202)
					_, _ = w.Write([]byte("partial"))
					if err := http.NewResponseController(w).Flush(); err != nil {
						panic("flush failed")
					}
				}
				panic("WIRE_PANIC_SECRET")
			})))
			defer server.Close()
			transport := &http.Transport{DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			response, err := (&http.Client{Transport: transport, Timeout: 2 * time.Second}).Get(server.URL + "/api/v1/services")
			if mode == "sentinel_abort" {
				if err == nil {
					response.Body.Close()
					t.Fatal("sentinel abort became HTTP response")
				}
			} else {
				if err != nil {
					t.Fatal("response headers unavailable", err)
				}
				defer response.Body.Close()
				raw, readErr := io.ReadAll(response.Body)
				if mode == "panic_after_flush" {
					if response.StatusCode != 202 || string(raw) != "partial" || readErr == nil {
						t.Fatal("partial panic not terminated on wire", response.StatusCode, string(raw), readErr)
					}
				} else {
					if response.StatusCode != 500 || readErr != nil || !json.Valid(raw) {
						t.Fatal("panic not complete HTTP JSON500", response.StatusCode, readErr)
					}
				}
			}
			event := onlyObservation(t, log)
			if mode != "panic_before_headers" && event["aborted"] != true {
				t.Fatal("abort missing", event)
			}
			assertLogDoesNotContain(t, log, "WIRE_PANIC_SECRET")
		})
	}
}
func TestHTTPObservabilityInformationalHTTPWire(t *testing.T) {
	app, log := observedApp(NewMemory())
	server := httptest.NewServer(app.observeHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(103)
		w.WriteHeader(201)
		_, _ = w.Write([]byte("created"))
	})))
	defer server.Close()
	response, err := server.Client().Get(server.URL + "/api/v1/services")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 201 || string(raw) != "created" {
		t.Fatal("informational/final wire changed", response.StatusCode, err)
	}
	if event := onlyObservation(t, log); event["status"] != float64(201) || event["bytes"] != float64(7) {
		t.Fatal("wrong final wire accounting", event)
	}
}
