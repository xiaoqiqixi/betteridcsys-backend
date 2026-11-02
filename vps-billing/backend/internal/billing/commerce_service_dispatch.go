package billing

import (
	api "aster/billing/internal/financeapi"
	"aster/billing/pkg/pluginapi/serverv1"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func (m *MySQL) claimServiceTask06(ctx context.Context, id string, revision int64, payload serviceActionPayload06) (serviceTaskRecord06, bool, error) {
	type claim struct {
		Task  serviceTaskRecord06
		Fresh bool
	}
	result, err := retryPayment(ctx, func() (claim, error) {
		var out claim
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		before, err := readServiceTask06(ctx, tx, id, "")
		if err != nil {
			return out, err
		}
		if err = lockServiceOwner06(ctx, tx, "", before.ServiceID); err != nil {
			return out, err
		}
		s, err := readService06(ctx, tx, "", before.ServiceID, " FOR UPDATE")
		if err != nil {
			return out, err
		}
		t, err := readServiceTask06(ctx, tx, id, " FOR UPDATE")
		out.Task = t
		if err != nil {
			return out, err
		}
		if t.View.Status != "queued" {
			return out, nil
		}
		if s.View.Revision != revision {
			return out, serviceConflict06()
		}
		busy, err := pendingServiceTasks06(ctx, tx, s.View.ID, id)
		if err != nil {
			return out, err
		}
		if busy {
			return out, serviceConflict06()
		}
		var first string
		if err = tx.QueryRowContext(ctx, "SELECT id FROM service_tasks WHERE service_id=? AND status='queued' ORDER BY created_at,id LIMIT 1", s.View.ID).Scan(&first); err != nil {
			return out, err
		}
		if first != id {
			return out, serviceConflict06()
		}
		stale := payload.Reason == "overdue" && (payload.Source == "automation" && (t.Action == "suspend" || t.Action == "terminate") && overdueRenewed06(s.Due, payload, time.Now().UTC()) ||
			t.Action == "unsuspend" && (s.View.SuspensionReason == nil || *s.View.SuspensionReason != "overdue"))
		if stale {
			if _, err = tx.ExecContext(ctx, "UPDATE service_tasks SET status='failed',message='Superseded by a billing or service state change',updated_at=UTC_TIMESTAMP(6),finished_at=UTC_TIMESTAMP(6) WHERE id=?", id); err != nil {
				return out, err
			}
			out.Task.View.Status = "failed"
			return out, tx.Commit()
		}
		if !taskStateAllowed06(s, t.Action, payload.Source) {
			return out, serviceConflict06()
		}
		payload.Version = 1
		payload.Params = payload.Params.Redacted()
		if payload.Request != nil {
			redacted := payload.Request.Redacted()
			payload.Request = &redacted
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return out, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE service_tasks SET status='running',operation_id=?,payload=?,updated_at=UTC_TIMESTAMP(6) WHERE id=?", id, raw, id); err != nil {
			return out, err
		}
		out.Task.View.Status = "running"
		out.Task.OperationID = sql.NullString{String: id, Valid: true}
		out.Task.Payload = payload
		err = tx.Commit()
		out.Fresh = err == nil
		return out, err
	})
	return result.Task, result.Fresh, err
}
func (m *MySQL) serviceTaskState06(ctx context.Context, id string) (serviceTaskRecord06, serviceRecord06, error) {
	t, err := readServiceTask06(ctx, m.DB, id, "")
	if err != nil {
		return t, serviceRecord06{}, err
	}
	s, err := readService06(ctx, m.DB, "", t.ServiceID, "")
	return t, s, err
}

// DispatchServiceTask06 claims a task before sending once. Every later call is
// read-only reconciliation, including a worker restart after an unknown outcome.
func (a *App) DispatchServiceTask06(ctx context.Context, id string, reconcileOnly, manual bool) (api.ServiceTask, error) {
	m, ok := a.Store.(*MySQL)
	if !ok {
		return api.ServiceTask{}, ErrCommerceUnavailable
	}
	task, s, err := m.serviceTaskState06(ctx, id)
	if err != nil {
		return task.View, err
	}
	if task.View.Status == "succeeded" || task.View.Status == "failed" {
		return task.View, nil
	}
	if reconcileOnly {
		return a.reconcileServiceTask06(ctx, task, s, manual)
	}
	if task.View.Status != "queued" {
		return task.View, nil
	}
	if s.View.ModuleID == nil {
		if !manual {
			return task.View, serverv1.ErrUnavailable
		}
		switch task.Action {
		case "provision", "suspend", "unsuspend", "terminate", "resize", "renew":
		default:
			return task.View, serverv1.ErrUnavailable
		}
		claimed, fresh, err := m.claimServiceTask06(ctx, id, s.View.Revision, task.Payload)
		if err != nil || !fresh {
			return claimed.View, err
		}
		return m.finishServiceTask06(ctx, id, "succeeded", "MANUAL", "")
	}
	runtime, ok := a.Config.Services.(*ServerModules06)
	if !ok || runtime == nil || s.View.ConnectionID == nil {
		return task.View, serverv1.ErrUnavailable
	}
	client, err := runtime.processor(ctx, *s.View.ConnectionID, *s.View.ModuleID)
	if err != nil {
		return task.View, err
	}
	if task.Action == "renew" {
		request := serverv1.Request{Protocol: serverv1.Protocol, Method: "describe"}
		result, err := client.Call(ctx, request)
		if err != nil || !result.Matches(request) || result.Description == nil || result.Description.Validate() != nil {
			return task.View, serverv1.ErrUnavailable
		}
		if !result.Description.Has("renew") {
			claimed, fresh, err := m.claimServiceTask06(ctx, id, s.View.Revision, task.Payload)
			if err != nil || !fresh {
				return claimed.View, err
			}
			return m.finishServiceTask06(ctx, id, "succeeded", "NO_RENEWAL_HOOK", "")
		}
	}
	payload := task.Payload
	config := s.Configuration
	if task.Action == "resize" {
		if payload.Change == nil {
			return task.View, ErrCommerceInput
		}
		config = payload.Change.Configuration
	}
	target := s
	target.Configuration = config
	request, err := serviceRequest06(target, id, task.Action, payload.Params)
	if err != nil {
		return task.View, err
	}
	if task.Action == "provision" {
		var enabled bool
		if err = m.DB.QueryRowContext(ctx, "SELECT enabled FROM server_connections WHERE id=? AND module_id=?", *s.View.ConnectionID, *s.View.ModuleID).Scan(&enabled); err != nil || !enabled {
			return task.View, ErrCommerceUnavailable
		}
		// Image IDs are scoped to the directly bound connection.
		if s.View.OSImage != nil {
			var image string
			err = m.DB.QueryRowContext(ctx, "SELECT remote_image_id FROM os_images WHERE id=? AND connection_id=? AND enabled=TRUE", s.View.OSImage.ID, *s.View.ConnectionID).Scan(&image)
			if err != nil {
				return task.View, ErrCommerceUnavailable
			}
			request.Input["image_id"] = image
		}
		if err = request.Seal(); err != nil {
			return task.View, err
		}
	} else {
		facade := &moduleService06{Processor: client, ServiceID: s.View.ID}
		remote, err := facade.Service(ctx, *s.View.ResourceID, s.View.ClientID)
		if err != nil {
			return task.View, err
		}
		projected, err := serviceProjection06(s.View, remote)
		if payload.Source == "bootstrap" {
			projected.Capabilities.Reinstall = remote.Capabilities.Reinstall
		}
		if err != nil {
			return task.View, err
		}
		if !serviceCapability06(projected, task.Action) {
			return task.View, serverv1.ErrUnavailable
		}
		if err = serviceRemoteState06(remote, task.Action); err != nil {
			return task.View, err
		}
	}
	payload.RequestSHA256 = request.Digest
	redacted := request.Redacted()
	payload.Request = &redacted
	claimed, fresh, err := m.claimServiceTask06(ctx, id, s.View.Revision, payload)
	if err != nil || !fresh {
		return claimed.View, err
	}
	result, sendErr := client.Call(ctx, request)
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return a.recordModuleResult06(finish, request, result, sendErr)
}
func (a *App) recordModuleResult06(ctx context.Context, request serverv1.Request, result serverv1.Response, sendErr error) (api.ServiceTask, error) {
	status, resource := "uncertain", ""
	if sendErr == nil && result.Matches(request) {
		if (request.Method == "create" || request.Method == "operation_status" && request.ResourceID == "") && serverv1.ValidID(result.ResourceID) {
			resource = result.ResourceID
		}
		switch result.State {
		case "accepted":
			status = "running"
		case "failed":
			status = "failed"
		case "done":
			status = "succeeded"
			if request.Method == "create" || request.Method == "operation_status" && request.ResourceID == "" {
				if !serverv1.ValidID(result.ResourceID) {
					status = "uncertain"
				} else {
					resource = result.ResourceID
				}
			}
		}
	}
	return a.Store.(*MySQL).finishServiceTask06(ctx, request.OperationID, status, "", resource)
}
func (a *App) reconcileServiceTask06(ctx context.Context, t serviceTaskRecord06, s serviceRecord06, manual bool) (api.ServiceTask, error) {
	if t.View.Status != "running" && t.View.Status != "uncertain" {
		return t.View, nil
	}
	m := a.Store.(*MySQL)
	if t.View.Status == "running" && !t.Payload.ReceiptKnown && time.Since(t.UpdatedAt) > 90*time.Second {
		if _, err := m.finishServiceTask06(ctx, t.View.ID, "uncertain", "", ""); err != nil {
			return t.View, err
		}
		t.View.Status = "uncertain"
	}
	if s.View.ModuleID == nil {
		if !manual {
			return t.View, serverv1.ErrUnavailable
		}
		return m.finishServiceTask06(ctx, t.View.ID, "succeeded", "MANUAL", "")
	}
	runtime, ok := a.Config.Services.(*ServerModules06)
	if !ok || runtime == nil || s.View.ConnectionID == nil || !t.OperationID.Valid || t.OperationID.String != t.View.ID || !serverv1.ValidDigest(t.Payload.RequestSHA256) || t.Payload.Request == nil {
		return t.View, serverv1.ErrUncertain
	}
	stored := t.Payload.Request
	if stored.OperationID != t.View.ID || stored.ServiceID != s.View.ID || stored.OwnerRef != s.View.ClientID || stored.Digest != t.Payload.RequestSHA256 {
		return t.View, serverv1.ErrUncertain
	}
	client, err := runtime.processor(ctx, *s.View.ConnectionID, *s.View.ModuleID)
	if err != nil {
		return t.View, err
	}
	// Never reconstruct a mutation from stored input or credentials.
	request := serverv1.Request{Protocol: serverv1.Protocol, Method: "operation_status", OperationID: t.View.ID, Digest: t.Payload.RequestSHA256, ServiceID: s.View.ID, OwnerRef: s.View.ClientID, ResourceID: stored.ResourceID}
	result, err := client.Call(ctx, request)
	return a.recordModuleResult06(ctx, request, result, err)
}
func (a *App) adminManagedService06(ctx context.Context, actor User, view api.AdminService, action string, payload serviceActionPayload06, key, fp string) (api.ServiceTask, error) {
	m := a.Store.(*MySQL)
	if action == "sync" {
		rows, err := m.DB.QueryContext(ctx, "SELECT id FROM service_tasks WHERE service_id=? ORDER BY created_at DESC,id DESC LIMIT 25", view.ID)
		if err != nil {
			return api.ServiceTask{}, err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return api.ServiceTask{}, err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return api.ServiceTask{}, err
		}
		if len(ids) == 0 {
			return api.ServiceTask{}, ErrNotFound
		}
		var latest api.ServiceTask
		for i, id := range ids {
			task, err := a.DispatchServiceTask06(ctx, id, true, true)
			if err != nil {
				return task, err
			}
			if i == 0 {
				latest = task
			}
		}
		tx, err := m.DB.BeginTx(ctx, nil)
		if err != nil {
			return latest, err
		}
		defer tx.Rollback()
		if _, err = lockActiveAdministrator(ctx, tx, actor.ID); err != nil {
			return latest, err
		}
		if replay, err := requestReplay06(ctx, tx, actor.ID, "service:action", key, fp, &latest); err != nil || replay {
			return latest, err
		}
		if err = recordRequest06(ctx, tx, actor.ID, "service:action", key, fp, latest); err != nil {
			return latest, err
		}
		return latest, tx.Commit()
	}
	switch action {
	case "provision", "suspend", "unsuspend", "terminate":
	default:
		return api.ServiceTask{}, serverv1.ErrUnavailable
	}
	type reserved struct {
		Task  api.ServiceTask
		Fresh bool
	}
	result, err := retryPayment(ctx, func() (reserved, error) {
		var out reserved
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		if _, err = lockActiveAdministrator(ctx, tx, actor.ID); err != nil {
			return out, err
		}
		if err = lockServiceOwner06(ctx, tx, "", view.ID); err != nil {
			return out, err
		}
		if replay, err := requestReplay06(ctx, tx, actor.ID, "service:action", key, fp, &out.Task); err != nil || replay {
			return out, err
		}
		s, err := readService06(ctx, tx, "", view.ID, " FOR UPDATE")
		if err != nil {
			return out, err
		}
		queuedAction, queuedPayload := action, payload
		if action == "provision" && s.View.Status == "failed" {
			var ambiguous int
			if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM service_tasks WHERE service_id=? AND status IN ('running','uncertain')", view.ID).Scan(&ambiguous); err != nil {
				return out, err
			}
			if ambiguous != 0 {
				return out, serviceConflict06()
			}
			if s.View.ResourceID != nil {
				// The resource was created but its initial image/credential setup
				// definitively failed. An explicit staff retry creates a new setup
				// operation, never another resource and never an uncertain replay.
				var previous string
				err = tx.QueryRowContext(ctx, "SELECT id FROM service_tasks WHERE service_id=? AND action='reinstall' AND status='failed' AND JSON_UNQUOTE(JSON_EXTRACT(payload,'$.source'))='bootstrap' ORDER BY created_at DESC,id DESC LIMIT 1", view.ID).Scan(&previous)
				if errors.Is(err, sql.ErrNoRows) {
					queuedPayload.Source = "module_retry"
					err = nil
				} else {
					if err != nil {
						return out, err
					}
					prior, err := readServiceTask06(ctx, tx, previous, "")
					if err != nil {
						return out, err
					}
					queuedAction, queuedPayload = "reinstall", prior.Payload
					queuedPayload.RequestSHA256, queuedPayload.ReceiptKnown = "", false
				}
			}
			if _, err = tx.ExecContext(ctx, "UPDATE services SET status='provisioning',revision=revision+1 WHERE id=?", view.ID); err != nil {
				return out, err
			}
			s.View.Status = "provisioning"
			s.View.Revision++
		}
		if !taskStateAllowed06(s, queuedAction, queuedPayload.Source) {
			return out, serviceConflict06()
		}
		var existing string
		err = tx.QueryRowContext(ctx, "SELECT id FROM service_tasks WHERE service_id=? AND status IN ('queued','running','uncertain') ORDER BY created_at,id LIMIT 1", view.ID).Scan(&existing)
		if err == nil {
			task, err := readServiceTask06(ctx, tx, existing, "")
			if err != nil {
				return out, err
			}
			if action != "provision" || task.Action != "provision" || task.View.Status != "queued" {
				return out, serviceConflict06()
			}
			out.Task = task.View
		} else if errors.Is(err, sql.ErrNoRows) {
			out.Task, err = queueServiceTask06(ctx, tx, view.ID, queuedAction, queuedPayload, time.Now().UTC())
			if err != nil {
				return out, err
			}
		} else {
			return out, err
		}
		if err = recordRequest06(ctx, tx, actor.ID, "service:action", key, fp, out.Task); err != nil {
			return out, err
		}
		if err = insertCurrencyAudit(ctx, tx, catalogAudit(actor, "service.action_queued", view.ID, s.View.Revision, "Service action: "+action)); err != nil {
			return out, err
		}
		err = tx.Commit()
		out.Fresh = err == nil
		return out, err
	})
	if err == nil && result.Fresh {
		_, err = a.DispatchServiceTask06(ctx, result.Task.ID, false, true)
	}
	return result.Task, err
}
