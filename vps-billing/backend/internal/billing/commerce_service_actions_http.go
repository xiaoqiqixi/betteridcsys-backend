package billing

import (
	api "aster/billing/internal/financeapi"
	"aster/billing/pkg/pluginapi/serverv1"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

func (a *App) serviceActionFingerprint06(sid, action string, input any) string {
	raw, _ := json.Marshal(input)
	h := hmac.New(sha256.New, []byte(a.Config.Secret))
	_, _ = h.Write([]byte("service-action:" + sid + ":" + action + ":"))
	_, _ = h.Write(raw)
	clear(raw)
	return hex.EncodeToString(h.Sum(nil))
}
func (m *MySQL) serviceActionReplay06(ctx context.Context, uid, key, fp string) (api.ServiceTask, bool, error) {
	var out api.ServiceTask
	var stored string
	var raw []byte
	err := m.DB.QueryRowContext(ctx, "SELECT fingerprint,response FROM commerce_requests WHERE user_id=? AND operation='service:action' AND request_key=?", uid, key).Scan(&stored, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	if stored != fp {
		return out, false, ErrConflict
	}
	return out, true, json.Unmarshal(raw, &out)
}
func (m *MySQL) actionImage06(ctx context.Context, s api.AdminService, id string) (api.OsImage, string, error) {
	var image api.OsImage
	var remote string
	if s.ConnectionID == nil {
		return image, remote, serverv1.ErrUnavailable
	}
	err := m.DB.QueryRowContext(ctx, `SELECT i.id,i.family,i.name,i.version,i.min_disk_gb,i.remote_image_id FROM os_images i JOIN product_os_images p ON p.image_id=i.id JOIN services s ON s.product_id=p.product_id WHERE s.id=? AND i.id=? AND i.enabled=TRUE AND i.connection_id=?`, s.ID, id, *s.ConnectionID).Scan(&image.ID, &image.Family, &image.Name, &image.Version, &image.MinDiskGB, &remote)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrCommerceInput
	}
	if err == nil && image.MinDiskGB > s.Specs.DiskGB {
		err = ErrCommerceInput
	}
	return image, remote, err
}
func (a *App) serviceActions06HTTP(w http.ResponseWriter, r *http.Request, u User, p string) bool {
	admin := strings.HasPrefix(p, "/admin/")
	parts := strings.Split(strings.Trim(strings.TrimPrefix(p, "/admin"), "/"), "/")
	if len(parts) < 3 || parts[0] != "services" {
		return false
	}
	if admin && !(len(parts) == 3 && parts[2] == "actions") {
		return false
	}
	op := parts[2]
	if !admin && op != "power" && op != "reinstall" && op != "reset-password" && op != "rescue" && op != "snapshots" && op != "actions" {
		return false
	}
	if !admin && op == "snapshots" && len(parts) == 3 && r.Method == "GET" {
		return false
	}
	action := ""
	var input any
	var params serviceParams06
	payload := serviceActionPayload06{Source: "customer"}
	requiredKey := true
	switch {
	case !admin && len(parts) == 4 && op == "actions" && r.Method == "POST":
		var in api.RunServiceActionInput
		if !commerceBody(w, r, "runServiceAction", &in) {
			return true
		}
		input = in
		action = "action"
		params.ActionID = parts[3]
		params.Input = in.Input
	case admin && r.Method == "POST":
		in := api.AdminServiceActionInput{}
		if !commerceBody(w, r, "adminServiceAction", &in) {
			return true
		}
		input = in
		action = in.Action
		payload.Source = "administrator"
		payload.Reason = in.Reason
	case !admin && len(parts) == 3 && op == "power" && r.Method == "POST":
		in := api.PowerServiceInput{}
		if !commerceBody(w, r, "powerService", &in) {
			return true
		}
		input = in
		action = in.Action
	case !admin && len(parts) == 3 && op == "reinstall" && r.Method == "POST":
		in := api.ReinstallServiceInput{}
		if !commerceBody(w, r, "reinstallService", &in) {
			return true
		}
		input = in
		action = "reinstall"
		params.Password = in.RootPassword
	case !admin && len(parts) == 3 && op == "reset-password" && r.Method == "POST":
		in := api.ResetServicePasswordInput{}
		if !commerceBody(w, r, "resetServicePassword", &in) {
			return true
		}
		input = in
		action = "reset_password"
		params.Password = in.RootPassword
	case !admin && len(parts) == 3 && op == "rescue" && r.Method == "POST":
		in := api.EnterRescueInput{}
		if !commerceBody(w, r, "enterRescue", &in) {
			return true
		}
		input = in
		action = "rescue_enter"
		params.Password = in.RootPassword

	case !admin && len(parts) == 3 && op == "rescue" && r.Method == "DELETE":
		action = "rescue_exit"
		requiredKey = false
	case !admin && len(parts) == 3 && op == "snapshots" && r.Method == "POST":
		in := api.CreateSnapshotInput{}
		if !commerceBody(w, r, "createSnapshot", &in) {
			return true
		}
		input = in
		action = "snapshot_create"
		params.Name = in.Name
	case !admin && len(parts) == 4 && op == "snapshots" && r.Method == "DELETE":
		action = "snapshot_delete"
		params.SnapshotID = parts[3]
		input = parts[3]
		requiredKey = false
	case !admin && len(parts) == 5 && op == "snapshots" && parts[4] == "restore" && r.Method == "POST":
		in := api.RestoreSnapshotInput{}
		if !commerceBody(w, r, "restoreSnapshot", &in) {
			return true
		}
		input = struct {
			SnapshotID string
			Confirm    string
		}{parts[3], in.Confirm}
		action = "snapshot_restore"
		params.SnapshotID = parts[3]
	default:
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return true
	}
	m, ok := a.Store.(*MySQL)
	if !ok {
		a.serviceControllerError06(w, r, serverv1.ErrUnavailable)
		return true
	}
	owner := u.ID
	if admin {
		owner = ""
	}
	s, err := m.Service06(r.Context(), owner, parts[1])
	if err != nil {
		a.commerceError(w, r, err)
		return true
	}
	key := r.Header.Get("Idempotency-Key")
	if requiredKey || key != "" {
		key = commerceKey06(w, r)
		if key == "" {
			return true
		}
	}
	fp := a.serviceActionFingerprint06(s.ID, action+":"+params.ActionID, input)
	if key != "" {
		out, replay, err := m.serviceActionReplay06(r.Context(), u.ID, key, fp)
		if err != nil {
			a.commerceError(w, r, err)
			return true
		}
		if replay {
			jsonOut(w, 200, out)
			return true
		}
	}
	if admin {
		in := input.(api.AdminServiceActionInput)
		if action == "terminate" && !serviceConfirmation06(s, in.Confirm) {
			a.commerceError(w, r, ErrCommerceInput)
			return true
		}
		if action == "provision" || action == "sync" || s.ModuleID == nil {
			out, err := a.adminManagedService06(r.Context(), u, s, action, payload, key, fp)
			if err != nil {
				a.serviceControllerError06(w, r, err)
			} else {
				jsonOut(w, 200, out)
			}
			return true
		}
	}
	runtime, ok := a.Config.Services.(*ServerModules06)
	if !ok || runtime == nil {
		a.serviceControllerError06(w, r, serverv1.ErrUnavailable)
		return true
	}
	client, err := runtime.client(r.Context(), s)
	if err != nil {
		a.serviceControllerError06(w, r, err)
		return true
	}
	remote, err := client.Service(r.Context(), *s.ResourceID, s.ClientID)
	if err != nil {
		a.serviceControllerError06(w, r, err)
		return true
	}
	projected, err := serviceProjection06(s, remote)
	if err != nil {
		a.serviceControllerError06(w, r, err)
		return true
	}
	if !serviceCapability06(projected, action) {
		a.serviceControllerError06(w, r, serverv1.ErrUnavailable)
		return true
	}
	if err = serviceRemoteState06(remote, action); err != nil {
		a.commerceError(w, r, err)
		return true
	}
	switch action {
	case "action":
		var definition *serverv1.CustomAction
		for i := range remote.CustomActions {
			if remote.CustomActions[i].ID == params.ActionID {
				definition = &remote.CustomActions[i]
				break
			}
		}
		if definition == nil {
			a.commerceError(w, r, ErrNotFound)
			return true
		}
		normalized, _, err := definition.InputSchema.Normalize(params.Input)
		if err != nil {
			a.commerceError(w, r, ErrCommerceInput)
			return true
		}
		params.Input, params.Secrets = definition.InputSchema.Split(normalized)
	case "reinstall":
		in := input.(api.ReinstallServiceInput)
		// Reinstall needs at least one credential: a password, SSH keys, or both.
		if !serviceConfirmation06(s, in.Confirm) || in.RootPassword == "" && len(in.SSHKeyIDs) == 0 {
			a.commerceError(w, r, ErrCommerceInput)
			return true
		}
		image, remote, err := m.actionImage06(r.Context(), s, in.OSImageID)
		if err != nil {
			a.commerceError(w, r, err)
			return true
		}
		payload.Image = &image
		params.ImageID = remote
		for _, keyID := range in.SSHKeyIDs {
			var publicKey string
			if err = m.DB.QueryRowContext(r.Context(), "SELECT public_key FROM ssh_keys WHERE user_id=? AND id=?", u.ID, keyID).Scan(&publicKey); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					err = ErrCommerceInput
				}
				a.commerceError(w, r, err)
				return true
			}
			params.SSHKeys = append(params.SSHKeys, publicKey)
		}
	case "snapshot_restore":
		in := input.(struct {
			SnapshotID string
			Confirm    string
		})
		if !serviceConfirmation06(s, in.Confirm) {
			a.commerceError(w, r, ErrCommerceInput)
			return true
		}
	case "terminate":
		params.Confirmation = *s.ResourceID
	case "unsuspend":
		params.StartAfter = true
	}
	if action == "snapshot_create" || action == "snapshot_restore" || action == "snapshot_delete" {
		list, err := client.Snapshots(r.Context(), *s.ResourceID, s.ClientID)
		if err == nil {
			err = serviceSnapshotState06(remote, list)
		}
		if err != nil {
			a.serviceControllerError06(w, r, err)
			return true
		}
		if action == "snapshot_create" && len(list.Items) >= list.Limit {
			fail(w, r, 409, "SNAPSHOT_LIMIT", "Snapshot limit reached")
			return true
		}
		if action != "snapshot_create" {
			found := false
			for _, snapshot := range list.Items {
				found = found || snapshot.ID == params.SnapshotID && snapshot.Status == "ready"
			}
			if !found {
				a.commerceError(w, r, ErrNotFound)
				return true
			}
		}
	}
	if !params.Valid(action) {
		a.commerceError(w, r, ErrCommerceInput)
		return true
	}
	task, request, fresh, err := m.reserveServiceAction06(r.Context(), u, admin, s, action, params, payload, key, fp)
	if err != nil {
		a.serviceControllerError06(w, r, err)
		return true
	}
	if fresh {
		if err = a.sendServiceAction06(r.Context(), s, client, request); err != nil {
			a.serviceControllerError06(w, r, err)
			return true
		}
	}
	jsonOut(w, 200, task)
	return true
}
