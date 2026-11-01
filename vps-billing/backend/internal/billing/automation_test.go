package billing

import (
	"aster/billing/internal/contracttest"
	api "aster/billing/internal/financeapi"
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMySQLAutomationSingleExecutionAndInterruptedHistory(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	at := time.Now().UTC().Truncate(time.Minute)
	var count atomic.Int64
	job := func(context.Context, api.AutomationSettings, time.Time) (AutomationResult, error) {
		count.Add(1)
		return AutomationResult{Processed: 2, Failed: 1}, nil
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.RunAutomationJob(t.Context(), "close_tickets", at, job)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if count.Load() != 1 {
		t.Fatal("multiple instances ran the same minute", count.Load())
	}
	if ran, err := m.RunAutomationJob(t.Context(), "close_tickets", at, job); ran || err != nil {
		t.Fatal("sequential duplicate tick", ran, err)
	}
	runs, err := m.AutomationRuns(t.Context(), "", "close_tickets", 50)
	if err != nil || len(runs.Items) != 1 || runs.Items[0].Status != "failed" || runs.Items[0].Processed != 2 || runs.Items[0].Failed != 1 {
		t.Fatal(runs, err)
	}
	contracttest.Response(t, "GET", "/admin/automation/runs", map[string]any{"data": runs})
	migrationExec(t, t.Context(), m.DB, "INSERT INTO automation_runs(id,job,scheduled_at,status,started_at,processed,failed) VALUES(?,'purge_sessions',?,'running',?,0,0)", ID(), at.Add(-time.Minute), at.Add(-time.Minute))
	if ran, err := m.RunAutomationJob(t.Context(), "purge_sessions", at, job); !ran || err != nil {
		t.Fatal(ran, err)
	}
	runs, err = m.AutomationRuns(t.Context(), "", "purge_sessions", 50)
	if err != nil || len(runs.Items) != 2 || runs.Items[1].Status != "uncertain" {
		t.Fatal(runs, err)
	}
	contracttest.Response(t, "GET", "/admin/automation/runs", map[string]any{"data": runs})
	panicJob := func(context.Context, api.AutomationSettings, time.Time) (AutomationResult, error) {
		panic("not exposed")
	}
	if ran, err := m.RunAutomationJob(t.Context(), "release_affiliate", at, panicJob); !ran || err == nil {
		t.Fatal(ran, err)
	}
	if ran, err := m.RunAutomationJob(t.Context(), "release_affiliate", at.Add(time.Minute), job); !ran || err != nil {
		t.Fatal("panic leaked advisory lock", ran, err)
	}
}

func TestMySQLAutomationHandlersAndRegistration(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	app := NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()})
	if app.Automation.jobs["close_tickets"] == nil || app.Automation.jobs["email_outbox"] == nil {
		t.Fatal("account jobs were not registered")
	}
	registry := &App{Automation: &AutomationRegistry{jobs: map[string]AutomationJob{}}}
	if err := registry.RegisterAutomationJob("close_tickets", func(context.Context, api.AutomationSettings, time.Time) (AutomationResult, error) {
		return AutomationResult{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterAutomationJob("close_tickets", func(context.Context, api.AutomationSettings, time.Time) (AutomationResult, error) {
		return AutomationResult{}, nil
	}); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate registration", err)
	}
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	admin := client(t, srv.URL, f.admin.Email, "CatalogFixture!2026")
	customer := client(t, srv.URL, f.customer.Email, "CatalogFixture!2026")
	status, body := admin.request("GET", "/api/v1/admin/automation", nil, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "GET", "/admin/automation", body)
	settings := body["data"].(map[string]any)
	settings["auto_apply_credit"] = true
	status, body = admin.request("PATCH", "/api/v1/admin/automation", settings, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "PATCH", "/admin/automation", body)
	status, body = admin.request("PATCH", "/api/v1/admin/automation", settings, "")
	if status != 409 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
	for _, tc := range []struct {
		method, path string
		body         any
	}{{"GET", "/admin/automation", nil}, {"PATCH", "/admin/automation", settings}, {"GET", "/admin/automation/runs", nil}} {
		status, body = customer.request(tc.method, "/api/v1"+tc.path, tc.body, "")
		if status != 403 {
			t.Fatal(status, body)
		}
		contracttest.Error(t, body)
	}
	job := app.Automation.jobs["purge_sessions"]
	token := ID() + ID()
	migrationExec(t, t.Context(), m.DB, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES(?,?,?)", token, f.customer.ID, time.Now().Add(-time.Hour))
	if _, err := m.RunAutomationJob(t.Context(), "purge_sessions", time.Now(), job); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := m.DB.QueryRow("SELECT COUNT(*) FROM sessions WHERE token_hash=?", token).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal(remaining, err)
	}
	status, body = admin.request("GET", "/api/v1/admin/automation/runs?job=purge_sessions", nil, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "GET", "/admin/automation/runs", body)
}
