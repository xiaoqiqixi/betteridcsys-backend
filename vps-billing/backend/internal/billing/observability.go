package billing

import (
	"context"
	"database/sql/driver"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

type observationKey struct{}
type requestObservation struct {
	logger                   *slog.Logger
	requestID, route, method string
	actorID, actorRole       string
	errorCode, errorClass    string
}

func observation(ctx context.Context) *requestObservation {
	value, _ := ctx.Value(observationKey{}).(*requestObservation)
	return value
}
func observeError(ctx context.Context, code, class string) {
	if value := observation(ctx); value != nil {
		if code != "" {
			value.errorCode = code
		}
		if class != "" {
			value.errorClass = class
		}
	}
}
func observeActor(ctx context.Context, actor User) {
	if value := observation(ctx); value != nil {
		value.actorID = actor.ID
		switch actor.Role {
		case "admin", "customer":
			value.actorRole = actor.Role
		}
	}
}
func errorKind(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, driver.ErrBadConn):
		return "database_connection"
	}
	var dbError *mysql.MySQLError
	if errors.As(err, &dbError) {
		// Numeric categories only: MySQLError.Message can contain SQL, identifiers or
		// credentials. Never serialize the error itself or its untrusted message.
		switch dbError.Number {
		case 1205:
			return "database_lock_timeout"
		case 1213:
			return "database_deadlock"
		case 1062:
			return "database_duplicate"
		case 1044, 1045, 1142:
			return "database_permission"
		case 1146:
			return "database_schema"
		default:
			return "database_error"
		}
	}
	return "storage_error"
}

func loggedMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace:
		return method
	default:
		return "OTHER"
	}
}
func loggedRoute(path string) string {
	switch path {
	case "/healthz", "/readyz", "/api/v1/session", "/api/v1/auth/login", "/api/v1/auth/logout",
		"/api/v1/currencies", "/api/v1/catalog-taxonomy", "/api/v1/admin/catalog-taxonomy", "/api/v1/plans", "/api/v1/site-settings", "/api/v1/orders", "/api/v1/invoices",
		"/api/v1/services", "/api/v1/audit", "/api/v1/audit-events", "/api/v1/admin/audit-events", "/api/v1/overview", "/api/v1/admin/overview",
		"/api/v1/admin/customer-accounts", "/api/v1/admin/catalog", "/api/v1/admin/customers", "/api/v1/admin/services", "/api/v1/admin/orders", "/api/v1/admin/invoices",
		"/api/v1/admin/integrations", "/api/v1/admin/currencies", "/api/v1/admin/plan-prices",
		"/api/v1/admin/site-settings", "/api/v1/admin/email-settings", "/api/v1/admin/email-settings/test",
		"/api/v1/admin/plugins", "/api/v1/admin/plugins/check", "/api/v1/admin/plugins/payment/check",
		"/api/v1/admin/updates", "/api/v1/admin/updates/check", "/api/v1/admin/updates/handoff", "/api/v1/admin/updates/operation":
		return path // Only literal allowlisted values; no arbitrary path is logged.
	}
	for _, prefix := range []string{"/api/v1/invoices/", "/api/v1/admin/invoices/"} {
		if id, ok := strings.CutPrefix(path, prefix); ok {
			suffix := ""
			if strings.HasSuffix(id, "/checkout") {
				id = strings.TrimSuffix(id, "/checkout")
				suffix = "/checkout"
			}
			if id != "" && !strings.Contains(id, "/") {
				return prefix + "{invoice_id}" + suffix
			}
		}
	}
	for _, route := range []struct {
		prefix, parameter string
		suffixes          []string
	}{
		{"/api/v1/orders/", "{order_id}", []string{"/cancel"}},
		{"/api/v1/admin/orders/", "{order_id}", []string{"/cancel", "/provisioning-jobs"}},
		{"/api/v1/admin/customer-accounts/", "{account_id}", []string{"/password", "/revoke-sessions"}},
		{"/api/v1/admin/catalog/", "{plan_id}", []string{"/duplicate", "/product-configuration"}},
		{"/api/v1/admin/provisioning-jobs/", "{job_id}", nil},
	} {
		if id, ok := strings.CutPrefix(path, route.prefix); ok {
			suffix := ""
			for _, candidate := range route.suffixes {
				if strings.HasSuffix(id, candidate) {
					id = strings.TrimSuffix(id, candidate)
					suffix = candidate
					break
				}
			}
			if id != "" && !strings.Contains(id, "/") {
				return route.prefix + route.parameter + suffix
			}
		}
	}
	if id, ok := strings.CutPrefix(path, "/api/v1/payment-callbacks/"); ok && id != "" && !strings.Contains(id, "/") {
		return "/api/v1/payment-callbacks/{provider_id}"
	}
	return "unmatched"
}

// The API has no streaming/hijacking handlers. Unwrap preserves controller
// capabilities without falsely exposing optional Flusher/Hijacker interfaces.
// FlushError also tracks a controller flush that commits an implicit HTTP 200.
type observedWriter struct {
	http.ResponseWriter
	status, bytes int
	writeFailed   bool
}

func (w *observedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *observedWriter) WriteHeader(status int) {
	w.ResponseWriter.WriteHeader(status) // Validate in the real writer first.
	if w.status == 0 && (status >= 200 || status == http.StatusSwitchingProtocols) {
		w.status = status
	}
}
func (w *observedWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.bytes += n
	if err != nil {
		w.writeFailed = true
	}
	return n, err
}
func (w *observedWriter) FlushError() error {
	err := http.NewResponseController(w.ResponseWriter).Flush()
	if errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if err != nil {
		w.writeFailed = true
	}
	return err
}

type panicFrame struct {
	Function string `json:"function"`
	File     string `json:"file"`
	Line     int    `json:"line"`
}

func safePanicFrames() []panicFrame {
	pcs := make([]uintptr, 16)
	count := runtime.Callers(3, pcs)
	frames := runtime.CallersFrames(pcs[:count])
	result := make([]panicFrame, 0, count)
	for {
		frame, more := frames.Next()
		result = append(result, panicFrame{frame.Function, filepath.Base(frame.File), frame.Line})
		if !more {
			break
		}
	}
	return result
}

func (a *App) observeHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		logger := a.Config.Logger
		if logger == nil {
			logger = slog.Default()
		}
		state := &requestObservation{logger: logger, requestID: ID(), route: loggedRoute(r.URL.Path), method: loggedMethod(r.Method)}
		r = r.WithContext(context.WithValue(r.Context(), observationKey{}, state))
		writer := &observedWriter{ResponseWriter: w}
		writer.Header().Set("X-Request-ID", state.requestID)
		defer func() {
			recovered := recover()
			aborted, panicked := false, false
			var stack []panicFrame
			if recovered != nil {
				if recovered == http.ErrAbortHandler {
					aborted = true
					state.errorClass = "aborted"
				} else {
					panicked = true
					stack = safePanicFrames()
					state.errorClass = "panic"
					if writer.status == 0 {
						fail(writer, r, 500, "INTERNAL_ERROR", "暂时无法完成请求")
					} else {
						// A committed response cannot be changed to a JSON 500. Let net/http
						// terminate the connection instead of appending a second JSON document.
						aborted = true
					}
				}
			}
			status := writer.status
			if status == 0 && !aborted {
				status = http.StatusOK
			}
			if state.errorClass == "" && r.Context().Err() != nil {
				state.errorClass = errorKind(r.Context().Err())
			}
			if writer.writeFailed && state.errorClass == "" {
				state.errorClass = "response_write_error"
			}
			if state.errorClass == "" && status >= 400 {
				state.errorClass = "http_error"
			}
			healthy := (state.route == "/healthz" || state.route == "/readyz") && status >= 200 && status < 300
			if !healthy || aborted || panicked || writer.writeFailed || state.errorClass != "" {
				level := slog.LevelInfo
				if status >= 400 {
					level = slog.LevelWarn
				}
				if status >= 500 || aborted || panicked || writer.writeFailed || state.errorClass == "timeout" || state.errorClass == "canceled" {
					level = slog.LevelError
				}
				attrs := []any{"request_id", state.requestID, "route", state.route, "method", state.method, "status", status, "bytes", writer.bytes, "duration_ms", float64(time.Since(started).Microseconds()) / 1000}
				if state.actorID != "" {
					attrs = append(attrs, "actor_id", state.actorID, "actor_role", state.actorRole)
				}
				if state.errorCode != "" {
					attrs = append(attrs, "error_code", state.errorCode)
				}
				if state.errorClass != "" {
					attrs = append(attrs, "error_class", state.errorClass)
				}
				if aborted {
					attrs = append(attrs, "aborted", true)
				}
				if writer.writeFailed {
					attrs = append(attrs, "write_failed", true)
				}
				if panicked {
					attrs = append(attrs, "panic", true, "stack", stack)
				}
				logger.Log(context.WithoutCancel(r.Context()), level, "http_request_completed", attrs...)
			}
			if aborted {
				panic(http.ErrAbortHandler)
			}
		}()
		next.ServeHTTP(writer, r)
	})
}
