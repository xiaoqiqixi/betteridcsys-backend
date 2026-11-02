package billing

import (
	"aster/billing/controllerapi"
	api "aster/billing/internal/financeapi"
	"aster/billing/serviceapi"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func (m *MySQL) prepareProvision06(ctx context.Context, s serviceRecord06, id string) (serviceActionPayload06, error) {
	p := serviceActionPayload06{Version: 1, Source: "provisioning"}
	if s.View.ControllerID == nil || s.Configuration.RemoteProductID == nil || len(s.Configuration.SSHKeys) > 1 {
		return p, serviceapi.ErrUnavailable
	}
	spec := controllerapi.ProductSpec{Mode: "preset", ControllerID: *s.View.ControllerID, RemoteProductID: *s.Configuration.RemoteProductID}
	hash, err := spec.Digest()
	if err != nil {
		return p, err
	}
	hostname := ""
	if s.View.Hostname != nil {
		hostname = *s.View.Hostname
	}
	request := controllerapi.Request{Protocol: controllerapi.Protocol, OperationID: id, OrderID: s.View.ID, OwnerRef: s.View.ClientID, Hostname: controllerHostname06(hostname, s.View.ID), ConfigurationRevision: s.View.Revision, ConfigurationSHA256: hash, Product: spec}
	_, p.RequestSHA256, err = request.BytesAndDigest()
	if err != nil {
		return p, err
	}
	p.Provision = &request
	if s.View.OSImage != nil {
		var controller string
		if err = m.DB.QueryRowContext(ctx, "SELECT controller_id,remote_image_id FROM os_images WHERE id=? AND enabled=TRUE", s.View.OSImage.ID).Scan(&controller, &p.Params.ImageID); err != nil {
			return p, serviceapi.ErrUnavailable
		}
		if controller != *s.View.ControllerID {
			return p, serviceapi.ErrUnavailable
		}
		p.Bootstrap = true
		p.Image = s.View.OSImage
		if len(s.Configuration.SSHKeys) == 1 {
			p.Params.SSHPublicKey = s.Configuration.SSHKeys[0]
		}
	} else if len(s.Configuration.SSHKeys) > 0 {
		return p, serviceapi.ErrUnavailable
	}
	return p, nil
}
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
		stale := payload.Reason == "overdue" && ((t.Action == "suspend" || t.Action == "terminate") && s.Due.After(time.Now().UTC()) || t.Action == "unsuspend" && (s.View.SuspensionReason == nil || *s.View.SuspensionReason != "overdue"))
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

// DispatchServiceTask06 is shared by the scheduler and the explicit root CLI.
// reconcileOnly never sends a mutation; a claimed/uncertain task is never resent.
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
	if s.View.Kind != "cloud_server" {
		if !manual {
			return task.View, serviceapi.ErrUnavailable
		}
		switch task.Action {
		case "provision", "suspend", "unsuspend", "terminate":
		default:
			return task.View, serviceapi.ErrUnavailable
		}
		claimed, fresh, err := m.claimServiceTask06(ctx, id, s.View.Revision, task.Payload)
		if err != nil || !fresh {
			return claimed.View, err
		}
		return m.finishServiceTask06(ctx, id, "succeeded", "MANUAL", "")
	}
	runtime, ok := a.Config.Services.(*ServiceControllers06)
	if !ok {
		return task.View, serviceapi.ErrUnavailable
	}
	payload := task.Payload
	if task.Action == "provision" {
		payload, err = m.prepareProvision06(ctx, s, id)
		if err != nil {
			return task.View, err
		}
		if runtime.Provisioning == nil {
			return task.View, serviceapi.ErrUnavailable
		}
		client, err := runtime.Provisioning(ctx, payload.Provision.Product.ControllerID)
		if err != nil {
			return task.View, err
		}
		claimed, fresh, err := m.claimServiceTask06(ctx, id, s.View.Revision, payload)
		if err != nil || !fresh {
			return claimed.View, err
		}
		result, sendErr := client.Send(ctx, *payload.Provision)
		return a.recordProvisionResult06(ctx, claimed, result, sendErr)
	}
	client, err := runtime.client(ctx, s.View)
	if err != nil {
		return task.View, err
	}
	remote, err := client.Service(ctx, *s.View.ResourceID, s.View.ClientID)
	if err != nil {
		return task.View, err
	}
	projected, err := serviceProjection06(s.View, remote)
	if err != nil {
		return task.View, err
	}
	if payload.Source == "bootstrap" {
		projected.Capabilities.Reinstall = remote.Capabilities.Reinstall
	}
	if !serviceCapability06(projected, task.Action) {
		return task.View, serviceapi.ErrUnavailable
	}
	if err = serviceRemoteState06(remote, task.Action); err != nil {
		return task.View, err
	}
	params := payload.Params
	switch task.Action {
	case "terminate":
		params.Confirmation = *s.View.ResourceID
	case "unsuspend":
		params.StartAfter = true
	case "resize":
		if payload.Change == nil {
			return task.View, ErrCommerceInput
		}
		specs := payload.Change.Product.Specs
		params = serviceapi.Params{CPUCores: int(specs.CPUCores), MemoryMB: int(specs.MemoryMB), DiskGB: int(specs.DiskGB), SetBandwidth: true, BandwidthMbps: int(specs.BandwidthMbps), InboundBandwidthMbps: int(specs.BandwidthMbps)}
	case "reinstall":
		// First setup may generate an ephemeral credential. The customer can set a
		// password later; neither this random value nor a retry copy is persisted.
		if payload.Source == "bootstrap" && params.SSHPublicKey == "" {
			params.Password = ID() + ID()
		}
	}
	request := serviceapi.ActionRequest{Protocol: serviceapi.Protocol, OperationID: id, OwnerRef: s.View.ClientID, Action: task.Action, Params: params}
	_, payload.RequestSHA256, err = request.BytesAndDigest()
	if err != nil {
		return task.View, err
	}
	payload.Params = params.Redacted()
	claimed, fresh, err := m.claimServiceTask06(ctx, id, s.View.Revision, payload)
	if err != nil || !fresh {
		return claimed.View, err
	}
	if err = a.sendServiceAction06(ctx, s.View, client, request); err != nil {
		return claimed.View, err
	}
	updated, err := readServiceTask06(ctx, m.DB, id, "")
	return updated.View, err
}
func (a *App) recordProvisionResult06(ctx context.Context, t serviceTaskRecord06, result controllerapi.Result, sendErr error) (api.ServiceTask, error) {
	status, resource := "uncertain", ""
	if sendErr == nil && t.Payload.Provision != nil && result.Matches(*t.Payload.Provision) {
		switch result.Status {
		case "accepted":
			status = "running"
		case "succeeded":
			if serviceapi.ValidID(result.ResourceID) {
				status = "succeeded"
				resource = result.ResourceID
			}
		case "rejected":
			status = "failed"
		}
	}
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return a.Store.(*MySQL).finishServiceTask06(finish, t.View.ID, status, "", resource)
}
func (a *App) reconcileServiceTask06(ctx context.Context, t serviceTaskRecord06, s serviceRecord06, manual bool) (api.ServiceTask, error) {
	if t.View.Status != "running" && t.View.Status != "uncertain" {
		return t.View, nil
	}
	m := a.Store.(*MySQL)
	if s.View.Kind != "cloud_server" {
		if !manual {
			return t.View, serviceapi.ErrUnavailable
		}
		return m.finishServiceTask06(ctx, t.View.ID, "succeeded", "MANUAL", "")
	}
	runtime, ok := a.Config.Services.(*ServiceControllers06)
	if !ok {
		return t.View, serviceapi.ErrUnavailable
	}
	if !t.OperationID.Valid || t.OperationID.String != t.View.ID {
		return t.View, serviceapi.ErrUncertain
	}
	if t.Action == "provision" {
		if t.Payload.Provision == nil || runtime.Provisioning == nil {
			return t.View, serviceapi.ErrUncertain
		}
		request := *t.Payload.Provision
		_, hash, err := request.BytesAndDigest()
		if err != nil || hash != t.Payload.RequestSHA256 || request.OwnerRef != s.View.ClientID || request.OrderID != s.View.ID {
			return t.View, serviceapi.ErrUncertain
		}
		client, err := runtime.Provisioning(ctx, request.Product.ControllerID)
		if err != nil {
			return t.View, err
		}
		result, err := client.Lookup(ctx, request)
		return a.recordProvisionResult06(ctx, t, result, err)
	}
	client, err := runtime.client(ctx, s.View)
	if err != nil {
		return t.View, err
	}
	result, err := client.Operation(ctx, *s.View.ResourceID, s.View.ClientID, t.View.ID)
	if err != nil {
		_, _ = m.finishServiceTask06(ctx, t.View.ID, "uncertain", "", "")
		return t.View, serviceapi.ErrUncertain
	}
	if result.OperationID != t.View.ID || result.Action != t.Action {
		return t.View, serviceapi.ErrUncertain
	}
	status := result.Status
	if status == "accepted" {
		status = "running"
	}
	return m.finishServiceTask06(ctx, t.View.ID, status, "", "")
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
		return api.ServiceTask{}, serviceapi.ErrUnavailable
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
		if action == "provision" && s.View.Status == "failed" && s.View.ResourceID == nil {
			var ambiguous int
			if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM service_tasks WHERE service_id=? AND status IN ('running','uncertain')", view.ID).Scan(&ambiguous); err != nil {
				return out, err
			}
			if ambiguous != 0 {
				return out, serviceConflict06()
			}
			if _, err = tx.ExecContext(ctx, "UPDATE services SET status='provisioning',revision=revision+1 WHERE id=?", view.ID); err != nil {
				return out, err
			}
			s.View.Status = "provisioning"
		}
		if !taskStateAllowed06(s, action, payload.Source) {
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
			out.Task, err = queueServiceTask06(ctx, tx, view.ID, action, payload, time.Now().UTC())
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
