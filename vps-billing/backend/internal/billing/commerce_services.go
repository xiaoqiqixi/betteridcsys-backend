package billing

import (
	api "aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type serviceRecord06 struct {
	View          api.AdminService
	ProductID     *string
	Due           time.Time
	Anchor        int
	LastChange    sql.NullTime
	Configuration serviceConfiguration06
}

func customerService06(v api.AdminService) api.Service {
	// Decode into the closed client DTO so staff ownership and controller IDs
	// cannot leak when the administrator projection gains another field.
	raw, _ := json.Marshal(v)
	var out api.Service
	_ = json.Unmarshal(raw, &out)
	return out
}

const service06Columns = `s.id,s.user_id,u.email,s.name,s.plan_name,s.region,s.status,s.product_id,s.cycle,s.recurring_minor,s.price_override_minor,s.currency,s.currency_snapshot,s.next_due_at,s.auto_renew,s.label,s.module_id,s.connection_id,s.resource_id,s.suspension_reason,s.cancellation_type,s.cancellation_reason,s.cancellation_requested_at,s.terminates_at,s.specs_snapshot,s.os_snapshot,s.configuration_snapshot,s.created_at,s.revision,s.anchor_day,s.last_change_at,s.cpu,s.memory_gb,s.disk_gb`

func scanService06(row interface{ Scan(...any) error }) (serviceRecord06, error) {
	var out serviceRecord06
	v := &out.View
	var formatRaw, specsRaw, osRaw, configRaw []byte
	var hostname string
	var override *int64
	var cancelType, cancelReason *string
	var created, cancelled, terminates sql.NullTime
	var memoryGB int64
	err := row.Scan(&v.ID, &v.ClientID, &v.ClientEmail, &hostname, &v.ProductName, &v.Region, &v.Status, &out.ProductID, &v.Cycle, &v.Recurring.AmountMinor, &override, &v.Recurring.Currency, &formatRaw, &out.Due, &v.AutoRenew, &v.Label, &v.ModuleID, &v.ConnectionID, &v.ResourceID, &v.SuspensionReason, &cancelType, &cancelReason, &cancelled, &terminates, &specsRaw, &osRaw, &configRaw, &created, &v.Revision, &out.Anchor, &out.LastChange, &v.Specs.CPUCores, &memoryGB, &v.Specs.DiskGB)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	format, err := decodeCurrencySnapshot(formatRaw, v.Recurring.Currency)
	if err != nil {
		return out, err
	}
	v.Recurring.DecimalPlaces = int64(format.DecimalPlaces)
	if override != nil {
		amount := v.Recurring
		amount.AmountMinor = *override
		v.PriceOverride = &amount
		v.Recurring = amount
	}
	v.CustomActions = []api.ServerCustomAction{}
	v.Kind = "generic"
	v.Specs.MemoryMB = memoryGB * 1024
	for _, item := range []struct {
		raw    []byte
		target any
	}{{specsRaw, &v.Specs}, {osRaw, &v.OSImage}, {configRaw, &out.Configuration}} {
		if len(item.raw) > 0 && string(item.raw) != "null" {
			if err = json.Unmarshal(item.raw, item.target); err != nil {
				return out, err
			}
		}
	}
	if out.Configuration.Kind != "" {
		v.Kind = out.Configuration.Kind
	}
	if hostname != "" {
		v.Hostname = &hostname
	}
	if v.Cycle != "one_time" {
		due := stamp06(out.Due)
		v.NextDueAt = &due
	} else {
		v.AutoRenew = false
	}
	if !created.Valid {
		created.Time = out.Due
	}
	v.CreatedAt = stamp06(created.Time)
	if terminates.Valid {
		at := stamp06(terminates.Time)
		v.TerminatesAt = &at
	}
	if cancelType != nil && cancelled.Valid {
		raw, _ := json.Marshal(map[string]any{"type": *cancelType, "reason": cancelReason, "requested_at": stamp06(cancelled.Time)})
		if err = json.Unmarshal(raw, &v.Cancellation); err != nil {
			return out, err
		}
	}
	v.IPv4 = []string{}
	v.IPv6 = []string{}
	v.Tasks = []api.ServiceTask{}
	v.AvailableImages = []api.OsImage{}
	v.RenewalOptions = make([]struct {
		Cycle  string    `json:"cycle"`
		Amount api.Money `json:"amount"`
	}, 0)
	if v.Cycle != "one_time" && (v.Status == "active" || v.Status == "suspended") && v.Cancellation == nil {
		v.RenewalOptions = append(v.RenewalOptions, struct {
			Cycle  string    `json:"cycle"`
			Amount api.Money `json:"amount"`
		}{v.Cycle, v.Recurring})
	}
	return out, nil
}

func readService06(ctx context.Context, q currencyQuerier, owner, id, lock string) (serviceRecord06, error) {
	query := "SELECT " + service06Columns + " FROM services s JOIN users u ON u.id=s.user_id WHERE s.id=?"
	args := []any{id}
	if owner != "" {
		query += " AND s.user_id=?"
		args = append(args, owner)
	}
	out, err := scanService06(q.QueryRowContext(ctx, query+lock, args...))
	if err != nil {
		return out, err
	}
	v := &out.View
	if out.Configuration.NodeID != "" {
		// Taxonomy is presentation only; historical services survive retirement.
		err = q.QueryRowContext(ctx, "SELECT name FROM catalog_nodes WHERE id=?", out.Configuration.NodeID).Scan(&v.NodeLabel)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
	}
	if out.ProductID != nil {
		rows, err := q.QueryContext(ctx, "SELECT i.id,i.family,i.name,i.version,i.min_disk_gb FROM os_images i JOIN product_os_images p ON p.image_id=i.id WHERE p.product_id=? AND i.enabled=TRUE ORDER BY i.id", *out.ProductID)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var image api.OsImage
			if err = rows.Scan(&image.ID, &image.Family, &image.Name, &image.Version, &image.MinDiskGB); err != nil {
				rows.Close()
				return out, err
			}
			v.AvailableImages = append(v.AvailableImages, image)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
	}
	rows, err := q.QueryContext(ctx, "SELECT id,action,status,message,created_at,finished_at FROM service_tasks WHERE service_id=? ORDER BY created_at DESC,id DESC LIMIT 25", id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var task api.ServiceTask
		var at time.Time
		var done sql.NullTime
		if err = rows.Scan(&task.ID, &task.Action, &task.Status, &task.Message, &at, &done); err != nil {
			rows.Close()
			return out, err
		}
		task.Action = serviceTaskAction06(task.Action)
		task.CreatedAt = stamp06(at)
		if done.Valid {
			at := stamp06(done.Time)
			task.FinishedAt = &at
		}
		v.Tasks = append(v.Tasks, task)
	}
	err = rows.Err()
	rows.Close()
	return out, err
}

// Customer locking is shared with invoice settlement, credit, and all service
// transitions. It prevents a late payment racing cancellation or termination.
func lockServiceOwner06(ctx context.Context, tx *sql.Tx, owner, id string) error {
	var uid string
	query := "SELECT user_id FROM services WHERE id=?"
	args := []any{id}
	if owner != "" {
		query += " AND user_id=?"
		args = append(args, owner)
	}
	err := tx.QueryRowContext(ctx, query, args...).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return tx.QueryRowContext(ctx, "SELECT id FROM users WHERE id=? FOR UPDATE", uid).Scan(&uid)
}
func (m *MySQL) Service06(ctx context.Context, owner, id string) (api.AdminService, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return api.AdminService{}, err
	}
	defer tx.Rollback()
	v, err := readService06(ctx, tx, owner, id, "")
	if err != nil {
		return v.View, err
	}
	return v.View, tx.Commit()
}
func (m *MySQL) ServicePage06(ctx context.Context, owner, cursor, status, search string, limit int) ([]api.AdminServiceSummary, *string, error) {
	out := []api.AdminServiceSummary{}
	if limit < 1 || limit > 100 || len(search) > 120 {
		return out, nil, ErrCommerceInput
	}
	if status != "" && !strings.Contains("|pending|provisioning|active|suspended|terminated|cancelled|failed|", "|"+status+"|") {
		return out, nil, ErrCommerceInput
	}
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return out, nil, err
	}
	defer tx.Rollback()
	query := "SELECT id FROM services WHERE 1=1"
	args := []any{}
	if owner != "" {
		query += " AND user_id=?"
		args = append(args, owner)
	}
	if status != "" {
		query += " AND status=?"
		args = append(args, status)
	}
	if search != "" {
		query += " AND (label LIKE ? OR name LIKE ? OR plan_name LIKE ?)"
		for range 3 {
			args = append(args, "%"+search+"%")
		}
	}
	if cursor != "" {
		if !validCancellationID(cursor) {
			return out, nil, ErrCommerceInput
		}
		query += " AND id<?"
		args = append(args, cursor)
	}
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, query+" ORDER BY id DESC LIMIT ?", args...)
	if err != nil {
		return out, nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return out, nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, nil, err
	}
	var next *string
	if len(ids) > limit {
		last := ids[limit-1]
		next = &last
		ids = ids[:limit]
	}
	out, err = readServiceSummaries06(ctx, tx, ids, owner)
	if err != nil {
		return out, nil, err
	}
	return out, next, tx.Commit()
}

type servicePatch06 struct {
	Revision      int64           `json:"revision,omitempty"`
	Label         *string         `json:"label,omitempty"`
	AutoRenew     *bool           `json:"auto_renew,omitempty"`
	NextDueAt     *string         `json:"next_due_at,omitempty"`
	PriceOverride json.RawMessage `json:"price_override_minor,omitempty"`
}

func serviceConflict06() error {
	return problem(409, "SERVICE_STATE_CONFLICT", "Service state does not permit this operation")
}
func retryCommerceWrite06(ctx context.Context, fn func() error) error {
	_, err := retryPayment(ctx, func() (bool, error) {
		err := fn()
		return err == nil, err
	})
	return err
}
func (m *MySQL) PatchService06(ctx context.Context, actor User, id string, admin bool, in servicePatch06) (api.AdminService, error) {
	var out api.AdminService
	err := retryCommerceWrite06(ctx, func() error {
		tx, err := m.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if admin {
			if _, err = lockActiveAdministrator(ctx, tx, actor.ID); err != nil {
				return err
			}
		}
		owner := actor.ID
		if admin {
			owner = ""
		}
		if err = lockServiceOwner06(ctx, tx, owner, id); err != nil {
			return err
		}
		s, err := readService06(ctx, tx, owner, id, " FOR UPDATE")
		if err != nil {
			return err
		}
		if admin && s.View.Revision != in.Revision {
			return ErrRevisionConflict
		}
		if s.View.Status == "terminated" || s.View.Status == "cancelled" {
			return serviceConflict06()
		}
		if in.Label != nil {
			s.View.Label = *in.Label
		}
		if in.AutoRenew != nil {
			if *in.AutoRenew && s.View.Cycle == "one_time" {
				return ErrCommerceInput
			}
			s.View.AutoRenew = *in.AutoRenew
		}
		if admin && in.NextDueAt != nil {
			var pending int
			if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM invoice_service_changes c JOIN invoices i ON i.id=c.invoice_id WHERE c.service_id=? AND i.status='unpaid'", id).Scan(&pending); err != nil {
				return err
			}
			if pending != 0 {
				return serviceConflict06()
			}
			s.Due, err = time.Parse(time.RFC3339Nano, *in.NextDueAt)
			if err != nil {
				return ErrCommerceInput
			}
			s.Anchor = s.Due.Day()
		}
		var override any
		if s.View.PriceOverride != nil {
			override = s.View.PriceOverride.AmountMinor
		}
		if admin && len(in.PriceOverride) > 0 {
			var amount *int64
			if json.Unmarshal(in.PriceOverride, &amount) != nil {
				return ErrCommerceInput
			}
			override = amount
		}
		_, err = tx.ExecContext(ctx, "UPDATE services SET label=?,auto_renew=?,next_due_at=?,anchor_day=?,next_invoice_at=IF(cycle='one_time',NULL,?),price_override_minor=?,revision=revision+1 WHERE id=?", s.View.Label, s.View.AutoRenew, s.Due, s.Anchor, s.Due, override, id)
		if err != nil {
			return err
		}
		if err = insertAccountAudit(ctx, tx, s.View.ClientID, actor.ID, "service.updated", id); err != nil {
			return err
		}
		updated, err := readService06(ctx, tx, owner, id, "")
		if err != nil {
			return err
		}
		out = updated.View
		return tx.Commit()
	})
	return out, err
}

func (m *MySQL) CancelService06(ctx context.Context, actor User, id string, in *api.RequestCancellationInput) (api.Service, error) {
	var out api.Service
	err := retryCommerceWrite06(ctx, func() error {
		tx, err := m.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err = lockServiceOwner06(ctx, tx, actor.ID, id); err != nil {
			return err
		}
		s, err := readService06(ctx, tx, actor.ID, id, " FOR UPDATE")
		if err != nil {
			return err
		}
		if s.View.Status != "active" && s.View.Status != "suspended" {
			return serviceConflict06()
		}
		var executing int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM service_tasks WHERE service_id=? AND action='terminate' AND status IN ('running','uncertain','succeeded')", id).Scan(&executing); err != nil {
			return err
		}
		if executing != 0 {
			return serviceConflict06()
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		if in == nil {
			if s.View.Cancellation == nil {
				out = customerService06(s.View)
				return tx.Commit()
			}
			_, err = tx.ExecContext(ctx, "UPDATE service_tasks SET status='failed',message='Cancellation withdrawn before dispatch',finished_at=?,updated_at=? WHERE service_id=? AND action='terminate' AND status='queued' AND JSON_UNQUOTE(JSON_EXTRACT(payload,'$.source'))='cancellation'", now, now, id)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, "UPDATE services SET cancellation_type=NULL,cancellation_reason=NULL,cancellation_requested_at=NULL,terminates_at=NULL,revision=revision+1 WHERE id=?", id)
		} else {
			if s.View.Cancellation != nil {
				return serviceConflict06()
			}
			if in.Type == "end_of_period" && s.View.Cycle == "one_time" {
				return ErrCommerceInput
			}
			when := s.Due
			if in.Type == "immediate" {
				when = now
			}
			_, err = tx.ExecContext(ctx, "UPDATE services SET cancellation_type=?,cancellation_reason=?,cancellation_requested_at=?,terminates_at=?,revision=revision+1 WHERE id=?", in.Type, in.Reason, now, when, id)
			if err != nil {
				return err
			}
			if !when.After(now) {
				_, err = queueServiceTask06(ctx, tx, id, "terminate", map[string]string{"source": "cancellation", "reason": in.Reason}, now)
			}
		}
		if err != nil {
			return err
		}
		action := "service.cancellation_requested"
		if in == nil {
			action = "service.cancellation_withdrawn"
		}
		if err = insertAccountAudit(ctx, tx, s.View.ClientID, actor.ID, action, id); err != nil {
			return err
		}
		updated, err := readService06(ctx, tx, actor.ID, id, "")
		if err != nil {
			return err
		}
		out = customerService06(updated.View)
		return tx.Commit()
	})
	return out, err
}

// The controller wire action is resize; the public billing task is upgrade.
func serviceTaskAction06(action string) string {
	if action == "resize" {
		return "upgrade"
	}
	return action
}
