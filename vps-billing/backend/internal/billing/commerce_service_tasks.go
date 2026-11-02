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

// No password-bearing request may be stored here. RequestSHA256 is the SDK's
// canonical digest; Params is always redacted before it crosses the SQL boundary.
type serviceActionPayload06 struct {
	Version       int               `json:"version"`
	Source        string            `json:"source,omitempty"`
	Reason        string            `json:"reason,omitempty"`
	Params        serviceParams06   `json:"params"`
	RequestSHA256 string            `json:"request_sha256,omitempty"`
	ReceiptKnown  bool              `json:"receipt_known,omitempty"`
	Request       *serverv1.Request `json:"request,omitempty"`
	Change        *planChange06     `json:"change,omitempty"`
	Bootstrap     bool              `json:"bootstrap,omitempty"`
	Image         *api.OsImage      `json:"image,omitempty"`
}
type serviceTaskRecord06 struct {
	View              api.ServiceTask
	ServiceID, Action string
	OperationID       sql.NullString
	Payload           serviceActionPayload06
	UpdatedAt         time.Time
}

func readServiceTask06(ctx context.Context, q currencyQuerier, id, lock string) (serviceTaskRecord06, error) {
	var t serviceTaskRecord06
	var raw []byte
	var at time.Time
	var done sql.NullTime
	err := q.QueryRowContext(ctx, "SELECT id,service_id,action,status,operation_id,message,payload,created_at,updated_at,finished_at FROM service_tasks WHERE id=?"+lock, id).Scan(&t.View.ID, &t.ServiceID, &t.Action, &t.View.Status, &t.OperationID, &t.View.Message, &raw, &at, &t.UpdatedAt, &done)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	t.View.Action = serviceTaskAction06(t.Action)
	t.View.CreatedAt = stamp06(at)
	if done.Valid {
		d := stamp06(done.Time)
		t.View.FinishedAt = &d
	}
	if err = json.Unmarshal(raw, &t.Payload); err != nil {
		return t, err
	}
	if t.Action == "resize" && t.Payload.Version == 0 {
		var change planChange06
		if err = json.Unmarshal(raw, &change); err != nil {
			return t, err
		}
		t.Payload.Change = &change
	}
	return t, nil
}
func taskStateAllowed06(s serviceRecord06, action, source string) bool {
	switch action {
	case "provision":
		return s.View.Status == "provisioning" && s.View.ResourceID == nil
	case "suspend":
		return s.View.Status == "active"
	case "unsuspend":
		return s.View.Status == "suspended"
	case "terminate":
		return s.View.Status == "active" || s.View.Status == "suspended"
	case "reinstall":
		return s.View.Status == "active" || source == "bootstrap" && s.View.Status == "provisioning" && s.View.ResourceID != nil
	default:
		return s.View.Status == "active"
	}
}
func pendingServiceTasks06(ctx context.Context, tx *sql.Tx, sid, except string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM service_tasks WHERE service_id=? AND id<>? AND status IN ('running','uncertain')", sid, except).Scan(&n)
	return n > 0, err
}
func (m *MySQL) reserveServiceAction06(ctx context.Context, actor User, admin bool, expected api.AdminService, action string, params serviceParams06, payload serviceActionPayload06, key, fp string) (api.ServiceTask, serverv1.Request, bool, error) {
	type reservation struct {
		Task    api.ServiceTask
		Request serverv1.Request
		Fresh   bool
	}
	reserved, err := retryPayment(ctx, func() (reservation, error) {
		var out reservation
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		owner := actor.ID
		if admin {
			if _, err = lockActiveAdministrator(ctx, tx, actor.ID); err != nil {
				return out, err
			}
			owner = ""
		}
		if err = lockServiceOwner06(ctx, tx, owner, expected.ID); err != nil {
			return out, err
		}
		if replay, err := requestReplay06(ctx, tx, actor.ID, "service:action", key, fp, &out.Task); err != nil || replay {
			return out, err
		}
		s, err := readService06(ctx, tx, owner, expected.ID, " FOR UPDATE")
		if err != nil {
			return out, err
		}
		if s.View.Revision != expected.Revision || !taskStateAllowed06(s, action, payload.Source) {
			return out, serviceConflict06()
		}
		var n int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM service_tasks WHERE service_id=? AND status IN ('queued','running','uncertain')", s.View.ID).Scan(&n); err != nil {
			return out, err
		}
		if n != 0 {
			return out, serviceConflict06()
		}
		id := ID()
		now := time.Now().UTC()
		out.Request, err = serviceRequest06(s, id, action, params)
		hash := out.Request.Digest
		if err != nil {
			return out, err
		}
		payload.Version = 1
		payload.Params = params.Redacted()
		payload.RequestSHA256 = hash
		redacted := out.Request.Redacted()
		payload.Request = &redacted
		raw, err := json.Marshal(payload)
		if err != nil {
			return out, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO service_tasks(id,service_id,action,status,operation_id,payload,created_at,updated_at) VALUES(?,?,?,'running',?,?,?,?)", id, s.View.ID, action, id, raw, now, now); err != nil {
			return out, err
		}
		out.Task = api.ServiceTask{ID: id, Action: serviceTaskAction06(action), Status: "running", CreatedAt: stamp06(now)}
		if err = recordRequest06(ctx, tx, actor.ID, "service:action", key, fp, out.Task); err != nil {
			return out, err
		}
		if err = insertCurrencyAudit(ctx, tx, catalogAudit(actor, "service.action_queued", s.View.ID, s.View.Revision, "Service action: "+serviceTaskAction06(action))); err != nil {
			return out, err
		}
		err = tx.Commit()
		out.Fresh = err == nil
		return out, err
	})
	return reserved.Task, reserved.Request, reserved.Fresh, err
}
func serviceEvent06(ctx context.Context, tx *sql.Tx, s api.AdminService, key, reason string) error {
	hostname := ""
	if s.Hostname != nil {
		hostname = *s.Hostname
	}
	raw, _ := json.Marshal(map[string]string{"service_name": s.ProductName, "hostname": hostname, "reason": reason})
	_, err := tx.ExecContext(ctx, "INSERT INTO email_outbox(id,user_id,template_key,variables,attempts,next_attempt_at,last_error,created_at) VALUES(?,?,?,?,0,UTC_TIMESTAMP(6),'',UTC_TIMESTAMP(6))", ID(), s.ClientID, key, raw)
	return err
}
func activateService06(ctx context.Context, tx *sql.Tx, s serviceRecord06) error {
	if s.View.Status == "active" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "UPDATE services SET status='active',suspension_reason=NULL,revision=revision+1 WHERE id=?", s.View.ID); err != nil {
		return err
	}
	if err := serviceEvent06(ctx, tx, s.View, "service_active", ""); err != nil {
		return err
	}
	// All quantity units must have completed provisioning before the order is active.
	_, err := tx.ExecContext(ctx, `UPDATE orders o SET o.status='active' WHERE o.status='paid' AND EXISTS(SELECT 1 FROM order_items i JOIN service_units u ON u.order_item_id=i.id WHERE i.order_id=o.id AND u.service_id=?) AND NOT EXISTS(SELECT 1 FROM order_items i LEFT JOIN service_units u ON u.order_item_id=i.id LEFT JOIN services s ON s.id=u.service_id WHERE i.order_id=o.id AND (s.id IS NULL OR s.status NOT IN ('active','suspended','terminated')))`, s.View.ID)
	return err
}

// finishServiceTask06 accepts confirmed results only. Repeating a lookup cannot
// duplicate lifecycle notifications, bootstrap operations or order activation.
func (m *MySQL) finishServiceTask06(ctx context.Context, id, status, code, resource string) (api.ServiceTask, error) {
	return retryPayment(ctx, func() (api.ServiceTask, error) {
		var out api.ServiceTask
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
		task, err := readServiceTask06(ctx, tx, id, " FOR UPDATE")
		if err != nil {
			return out, err
		}
		out = task.View
		if task.View.Status == "succeeded" || task.View.Status == "failed" {
			return out, nil
		}
		if task.View.Status != "running" && task.View.Status != "uncertain" {
			return out, serviceConflict06()
		}
		if status != "running" && status != "succeeded" && status != "failed" && status != "uncertain" {
			return out, ErrCommerceInput
		}
		if task.View.Status == "uncertain" && status == "running" {
			return out, nil
		}
		now := time.Now().UTC()
		if status == "succeeded" {
			switch task.Action {
			case "provision":
				if s.View.ModuleID != nil {
					if !serverv1.ValidID(resource) {
						return out, serverv1.ErrUncertain
					}
					if s.View.ResourceID != nil && *s.View.ResourceID != resource {
						return out, serverv1.ErrUncertain
					}
					if _, err = tx.ExecContext(ctx, "UPDATE services SET resource_id=?,revision=revision+1 WHERE id=?", resource, s.View.ID); err != nil {
						return out, err
					}
					if task.Payload.Bootstrap {
						payload := serviceActionPayload06{Version: 1, Source: "bootstrap", Params: task.Payload.Params.Redacted(), Image: task.Payload.Image}
						if _, err = queueServiceTask06(ctx, tx, s.View.ID, "reinstall", payload, now); err != nil {
							return out, err
						}
						break
					}
				}
				if err = activateService06(ctx, tx, s); err != nil {
					return out, err
				}
			case "reinstall":
				if task.Payload.Image != nil {
					raw, _ := json.Marshal(task.Payload.Image)
					if _, err = tx.ExecContext(ctx, "UPDATE services SET os_snapshot=?,revision=revision+1 WHERE id=?", raw, s.View.ID); err != nil {
						return out, err
					}
				}
				if task.Payload.Source == "bootstrap" {
					if err = activateService06(ctx, tx, s); err != nil {
						return out, err
					}
				}
			case "suspend":
				if s.View.Status == "active" {
					reason := task.Payload.Reason
					if reason == "" {
						reason = "administrator"
					}
					if _, err = tx.ExecContext(ctx, "UPDATE services SET status='suspended',suspension_reason=?,revision=revision+1 WHERE id=?", reason, s.View.ID); err != nil {
						return out, err
					}
					if err = serviceEvent06(ctx, tx, s.View, "service_suspended", reason); err != nil {
						return out, err
					}
					// A renewal can settle while the module is suspending. Reverse only
					// an overdue suspension, using a new durable operation after confirmation.
					if reason == "overdue" && s.Due.After(now) {
						if _, err = queueServiceTask06(ctx, tx, s.View.ID, "unsuspend", serviceActionPayload06{Version: 1, Source: "payment", Reason: "overdue", Params: serviceParams06{StartAfter: true}}, now); err != nil {
							return out, err
						}
					}
				}
			case "unsuspend":
				if s.View.Status == "suspended" {
					if err = activateService06(ctx, tx, s); err != nil {
						return out, err
					}
				}
			case "terminate":
				if s.View.Status == "active" || s.View.Status == "suspended" {
					if _, err = tx.ExecContext(ctx, "UPDATE services SET status='terminated',auto_renew=FALSE,next_invoice_at=NULL,revision=revision+1 WHERE id=?", s.View.ID); err != nil {
						return out, err
					}
					if err = serviceEvent06(ctx, tx, s.View, "service_terminated", task.Payload.Reason); err != nil {
						return out, err
					}
				}
			case "resize":
				if task.Payload.Change == nil {
					return out, serverv1.ErrUncertain
				}
				change := task.Payload.Change
				specs, _ := json.Marshal(change.Product.Specs)
				config, _ := json.Marshal(change.Configuration)
				if _, err = tx.ExecContext(ctx, "UPDATE services SET specs_snapshot=?,configuration_snapshot=?,cpu=?,memory_gb=?,disk_gb=?,revision=revision+1 WHERE id=?", specs, config, change.Product.Specs.CPUCores, (change.Product.Specs.MemoryMB+1023)/1024, change.Product.Specs.DiskGB, s.View.ID); err != nil {
					return out, err
				}
			}
		}
		if status == "failed" && (task.Action == "provision" || task.Payload.Source == "bootstrap") {
			if _, err = tx.ExecContext(ctx, "UPDATE services SET status='failed',revision=revision+1 WHERE id=? AND status='provisioning'", s.View.ID); err != nil {
				return out, err
			}
		}
		var finished any
		if status == "succeeded" || status == "failed" {
			finished = now
			at := stamp06(now)
			out.FinishedAt = &at
		}
		// Only locally chosen codes are recorded; provider messages never enter SQL.
		message := "Module operation pending"
		switch status {
		case "succeeded":
			message = "Module operation completed"
		case "failed":
			message = "Module operation rejected or failed"
		case "uncertain":
			message = "Module outcome uncertain; read-only reconciliation required"
		}
		if code == "MANUAL" {
			message = "Administrator confirmed the manual service operation"
		}
		if status == "running" {
			if _, err = tx.ExecContext(ctx, "UPDATE service_tasks SET payload=JSON_SET(payload,'$.receipt_known',JSON_EXTRACT('true','$')) WHERE id=?", id); err != nil {
				return out, err
			}
		}
		if _, err = tx.ExecContext(ctx, "UPDATE service_tasks SET status=?,message=?,updated_at=?,finished_at=? WHERE id=?", status, message, now, finished, id); err != nil {
			return out, err
		}
		out.Status = status
		out.Message = &message
		return out, tx.Commit()
	})
}
func (a *App) sendServiceAction06(ctx context.Context, s api.AdminService, client serverv1.Processor, request serverv1.Request) error {
	result, err := client.Call(ctx, request)
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err = a.recordModuleResult06(finish, request, result, err)
	return err
}
func serviceConfirmation06(s api.AdminService, confirmation string) bool {
	return confirmation == s.ID || s.Hostname != nil && confirmation == *s.Hostname
}
func serviceCapability06(v api.Service, action string) bool {
	switch action {
	case "start", "stop", "reboot", "force_off":
		return v.Capabilities.Power
	case "reinstall":
		return v.Capabilities.Reinstall
	case "reset_password":
		return v.Capabilities.ResetPassword
	case "rescue_enter", "rescue_exit":
		return v.Capabilities.Rescue
	case "snapshot_create", "snapshot_restore", "snapshot_delete":
		return v.Capabilities.Snapshots
	case "resize":
		return v.Capabilities.Upgrade
	case "action":
		return len(v.CustomActions) > 0
	case "renew":
		return true
	case "suspend", "unsuspend", "terminate":
		return true
	}
	return false
}
func serviceRemoteState06(remote serverv1.Service, action string) error {
	if remote.RescueActive && (action == "reinstall" || action == "reset_password" || action == "resize") {
		return serviceConflict06()
	}
	if remote.Suspended && action != "unsuspend" && action != "terminate" {
		return serviceConflict06()
	}
	switch action {
	case "start":
		if remote.Power != "stopped" || remote.RescueActive {
			return serviceConflict06()
		}
	case "stop", "force_off", "reboot":
		if remote.Power != "running" {
			return serviceConflict06()
		}
	case "snapshot_create", "snapshot_restore", "snapshot_delete":
		if remote.Power != "stopped" || remote.RescueActive {
			return serviceConflict06()
		}
	}
	return nil
}
