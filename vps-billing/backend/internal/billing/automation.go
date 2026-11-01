package billing

import (
	"aster/billing/internal/commerce"
	api "aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
)

type AutomationResult struct{ Processed, Failed int64 }

// AutomationJob processes a bounded batch. Object failures belong in Failed;
// return an error only when the entire batch cannot continue. The scheduler does
// not retry a failed or uncertain tick. Object transitions must be idempotent.
type AutomationJob func(context.Context, api.AutomationSettings, time.Time) (AutomationResult, error)
type AutomationRegistry struct {
	mu   sync.RWMutex
	jobs map[string]AutomationJob
}

var automationJobNames = map[string]bool{"generate_renewals": true, "send_reminders": true, "suspend_overdue": true, "terminate_overdue": true, "cancel_unpaid_orders": true, "apply_credit": true, "dispatch_provisioning": true, "purge_sessions": true, "close_tickets": true, "release_affiliate": true}

// RegisterAutomationJob is shared with account/support jobs. Duplicate names
// are rejected so a later integration cannot silently replace a billing job.
func (a *App) RegisterAutomationJob(name string, job AutomationJob) error {
	if (!automationJobNames[name] && name != "email_outbox") || job == nil {
		return ErrCommerceInput
	}
	a.Automation.mu.Lock()
	defer a.Automation.mu.Unlock()
	if _, ok := a.Automation.jobs[name]; ok {
		return ErrConflict
	}
	a.Automation.jobs[name] = job
	return nil
}
func readAutomation(ctx context.Context, q currencyQuerier, lock string) (api.AutomationSettings, error) {
	var v api.AutomationSettings
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT document FROM automation_settings WHERE id=1"+lock).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &v)
	}
	if err == nil {
		err = commerce.ValidateAutomation(v)
	}
	return v, err
}
func (m *MySQL) AutomationSettings(ctx context.Context) (api.AutomationSettings, error) {
	return readAutomation(ctx, m.DB, "")
}
func (m *MySQL) UpdateAutomation(ctx context.Context, actor User, in api.AutomationSettings) (api.AutomationSettings, error) {
	if err := commerce.ValidateAutomation(in); err != nil {
		return in, err
	}
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return in, err
	}
	defer tx.Rollback()
	actor, err = lockActiveAdministrator(ctx, tx, actor.ID)
	if err != nil {
		return in, err
	}
	old, err := readAutomation(ctx, tx, " FOR UPDATE")
	if err != nil {
		return in, err
	}
	if old.Revision != in.Revision {
		return in, ErrRevisionConflict
	}
	in.Revision++
	raw, _ := json.Marshal(in)
	if _, err = tx.ExecContext(ctx, "UPDATE automation_settings SET document=? WHERE id=1", raw); err != nil {
		return in, err
	}
	if err = insertCurrencyAudit(ctx, tx, catalogAudit(actor, "automation.updated", "automation", in.Revision, "Automation settings saved")); err != nil {
		return in, err
	}
	return in, tx.Commit()
}

// RunAutomationJob holds a dedicated connection for the advisory lock. A unique
// minute key also prevents sequential duplicate execution after lock release.
func (m *MySQL) RunAutomationJob(ctx context.Context, name string, at time.Time, job AutomationJob) (bool, error) {
	if (!automationJobNames[name] && name != "email_outbox") || job == nil {
		return false, ErrCommerceInput
	}
	conn, err := m.DB.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	var acquired sql.NullInt64
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK(?,0)", "aster:job:"+name).Scan(&acquired); err != nil {
		return false, err
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return false, nil
	}
	defer func() {
		release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(release, "SELECT RELEASE_LOCK(?)", "aster:job:"+name)
	}()
	settings, err := readAutomation(ctx, conn, "")
	if err != nil {
		return false, err
	}
	minute := at.UTC().Truncate(time.Minute)
	now := time.Now().UTC().Truncate(time.Microsecond)
	id := ID()
	// A previous process lost its advisory lock before recording completion.
	// Preserve that ambiguity rather than treating the earlier run as successful.
	if _, err = conn.ExecContext(ctx, "UPDATE automation_runs SET status='uncertain',finished_at=?,message='Previous worker stopped before recording completion' WHERE job=? AND status='running' AND scheduled_at<?", now, name, minute); err != nil {
		return false, err
	}
	_, err = conn.ExecContext(ctx, "INSERT INTO automation_runs(id,job,scheduled_at,status,started_at,processed,failed) VALUES(?,?,?,'running',?,0,0)", id, name, minute, now)
	var dbErr *mysql.MySQLError
	if errors.As(err, &dbErr) && dbErr.Number == 1062 {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	result, runErr := invokeAutomationJob(ctx, job, settings, at)
	state := "succeeded"
	var message any
	if result.Failed > 0 {
		state = "failed"
		message = "Some objects require review"
	}
	if runErr != nil {
		state = "failed"
		message = "Job could not complete"
		result.Failed++
		if ctx.Err() != nil {
			state = "uncertain"
			message = "Worker interrupted; inspect object outcomes"
		}
	}
	if result.Processed < 0 || result.Failed < 0 || result.Processed > MaxMinor || result.Failed > MaxMinor {
		result = AutomationResult{Failed: 1}
		state = "failed"
		message = "Invalid job result"
	}
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_, err = conn.ExecContext(finish, "UPDATE automation_runs SET status=?,finished_at=?,processed=?,failed=?,message=? WHERE id=? AND status='running'", state, time.Now().UTC(), result.Processed, result.Failed, message, id)
	if err != nil {
		return true, err
	}
	return true, runErr
}
func invokeAutomationJob(ctx context.Context, job AutomationJob, s api.AutomationSettings, at time.Time) (result AutomationResult, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("automation job panicked")
		}
	}()
	return job(ctx, s, at)
}

// RunAutomation runs inside aster-api and stops before the database is closed.
// Jobs run sequentially to reserve only one database connection for GET_LOCK.
func (a *App) RunAutomation(ctx context.Context) {
	m, ok := a.Store.(*MySQL)
	if !ok {
		return
	}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	run := func(at time.Time) {
		a.Automation.mu.RLock()
		names := make([]string, 0, len(a.Automation.jobs))
		jobs := map[string]AutomationJob{}
		for n, j := range a.Automation.jobs {
			names = append(names, n)
			jobs[n] = j
		}
		a.Automation.mu.RUnlock()
		sort.Strings(names)
		for _, n := range names {
			if ctx.Err() != nil {
				return
			}
			jobCtx, cancel := context.WithTimeout(ctx, 55*time.Second)
			_, err := m.RunAutomationJob(jobCtx, n, at, jobs[n])
			cancel()
			if err != nil && ctx.Err() == nil {
				a.Config.Logger.Error("automation job failed", "job", n)
			}
		}
	}
	run(time.Now().UTC())
	for {
		select {
		case <-ctx.Done():
			return
		case at := <-tick.C:
			run(at)
		}
	}
}
func (a *App) registerCommerceAutomation() {
	_ = a.RegisterAutomationJob("purge_sessions", func(ctx context.Context, _ api.AutomationSettings, at time.Time) (AutomationResult, error) {
		m, ok := a.Store.(*MySQL)
		if !ok {
			return AutomationResult{}, ErrCommerceUnavailable
		}
		result, err := m.DB.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at<=? LIMIT 1000", at.UTC())
		if err != nil {
			return AutomationResult{}, err
		}
		n, err := result.RowsAffected()
		return AutomationResult{Processed: n}, err
	})
	_ = a.RegisterAutomationJob("cancel_unpaid_orders", func(ctx context.Context, s api.AutomationSettings, at time.Time) (AutomationResult, error) {
		m, ok := a.Store.(*MySQL)
		if !ok {
			return AutomationResult{}, ErrCommerceUnavailable
		}
		rows, err := m.DB.QueryContext(ctx, "SELECT id,user_id FROM orders WHERE status='pending_payment' AND created_at<=? ORDER BY created_at,id LIMIT 200", at.Add(-time.Duration(s.CancelUnpaidOrdersAfterDays)*24*time.Hour))
		if err != nil {
			return AutomationResult{}, err
		}
		type target struct{ id, owner string }
		targets := []target{}
		for rows.Next() {
			var t target
			if err = rows.Scan(&t.id, &t.owner); err != nil {
				rows.Close()
				return AutomationResult{}, err
			}
			targets = append(targets, t)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return AutomationResult{}, err
		}
		result := AutomationResult{}
		for _, t := range targets {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			_, err = m.CancelOrder(ctx, User{ID: t.owner, Role: "customer"}, t.id, "Unpaid order exceeded the payment window")
			if err != nil {
				result.Failed++
			} else {
				result.Processed++
			}
		}
		return result, nil
	})
}

func (a *App) automationHTTP(w http.ResponseWriter, r *http.Request, u User, p string) bool {
	if p != "/admin/automation" && p != "/admin/automation/runs" {
		return false
	}
	m, ok := a.Store.(*MySQL)
	if !ok {
		fail(w, r, 503, "FEATURE_UNAVAILABLE", "Automation requires persistent storage")
		return true
	}
	if p == "/admin/automation" {
		switch r.Method {
		case "GET":
			v, err := m.AutomationSettings(r.Context())
			if err != nil {
				a.commerceError(w, r, err)
			} else {
				jsonOut(w, 200, v)
			}
		case "PATCH":
			var in api.AutomationSettings
			if commerceBody(w, r, "adminUpdateAutomation", &in) {
				v, err := m.UpdateAutomation(r.Context(), u, in)
				if err != nil {
					a.commerceError(w, r, err)
				} else {
					jsonOut(w, 200, v)
				}
			}
		default:
			fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		}
		return true
	}
	if r.Method != "GET" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return true
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			a.commerceError(w, r, ErrCommerceInput)
			return true
		}
		limit = n
	}
	v, err := m.AutomationRuns(r.Context(), r.URL.Query().Get("cursor"), r.URL.Query().Get("job"), limit)
	if err != nil {
		a.commerceError(w, r, err)
	} else {
		jsonOut(w, 200, v)
	}
	return true
}
func (m *MySQL) AutomationRuns(ctx context.Context, cursor, job string, limit int) (api.AdminListAutomationRunsResponse, error) {
	out := api.AdminListAutomationRunsResponse{Items: []api.AutomationRun{}}
	query := "SELECT id,job,status,started_at,finished_at,processed,failed,message FROM automation_runs WHERE job<>'email_outbox'"
	args := []any{}
	if job != "" {
		if !automationJobNames[job] {
			return out, ErrCommerceInput
		}
		query += " AND job=?"
		args = append(args, job)
	}
	if cursor != "" {
		if !validCancellationID(cursor) {
			return out, ErrCommerceInput
		}
		var at time.Time
		err := m.DB.QueryRowContext(ctx, "SELECT started_at FROM automation_runs WHERE id=?", cursor).Scan(&at)
		if errors.Is(err, sql.ErrNoRows) {
			return out, ErrCommerceInput
		}
		if err != nil {
			return out, err
		}
		query += " AND (started_at<? OR (started_at=? AND id<?))"
		args = append(args, at, at, cursor)
	}
	args = append(args, limit+1)
	rows, err := m.DB.QueryContext(ctx, query+" ORDER BY started_at DESC,id DESC LIMIT ?", args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v api.AutomationRun
		var started time.Time
		var finished sql.NullTime
		if err = rows.Scan(&v.ID, &v.Job, &v.Status, &started, &finished, &v.Processed, &v.Failed, &v.Message); err != nil {
			return out, err
		}
		if len(out.Items) == limit {
			last := out.Items[len(out.Items)-1].ID
			out.NextCursor = &last
			break
		}
		v.StartedAt = stamp06(started)
		if finished.Valid {
			at := stamp06(finished.Time)
			v.FinishedAt = &at
		}
		out.Items = append(out.Items, v)
	}
	return out, rows.Err()
}
