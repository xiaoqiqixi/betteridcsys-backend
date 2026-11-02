package billing

import (
	api "aster/billing/internal/financeapi"
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/serverv1"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

func (a *App) syncModuleSlots06(ctx context.Context) error {
	m, ok := a.Store.(*MySQL)
	if !ok {
		return ErrCommerceUnavailable
	}
	runtime, ok := a.Config.Services.(*ServerModules06)
	if !ok || runtime == nil || runtime.Selections == nil {
		return nil
	}
	slots, err := runtime.Selections()
	if err != nil {
		return serverv1.ErrUnavailable
	}
	for _, s := range slots {
		if !pluginapi.ValidConnectionID(s.Slot) {
			return serverv1.ErrConfiguration
		}
		_, err = m.DB.ExecContext(ctx, "INSERT IGNORE INTO server_connections(id,module_id,name,enabled) VALUES(?,?,?,?)", s.Slot, s.ID, s.Slot, s.Enabled)
		if err != nil {
			return err
		}
		var module string
		if err = m.DB.QueryRowContext(ctx, "SELECT module_id FROM server_connections WHERE id=?", s.Slot).Scan(&module); err != nil {
			return err
		}
		// An existing service's identity must never silently change when a host
		// operator replaces a slot. Use a new connection for a different module.
		if module != s.ID {
			return serverv1.ErrConfiguration
		}
	}
	return nil
}
func (m *MySQL) connections06(ctx context.Context, id string) ([]api.ServerConnection, error) {
	q := "SELECT c.id,c.module_id,c.name,c.enabled,c.status,c.last_checked_at,c.revision,(SELECT COUNT(*) FROM services s WHERE s.connection_id=c.id AND s.status NOT IN ('terminated','cancelled')) FROM server_connections c"
	args := []any{}
	if id != "" {
		q += " WHERE BINARY c.id=BINARY ?"
		args = append(args, id)
	}
	q += " ORDER BY c.id"
	rows, err := m.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.ServerConnection{}
	for rows.Next() {
		var v api.ServerConnection
		var at sql.NullTime
		if err = rows.Scan(&v.ID, &v.ModuleID, &v.Name, &v.Enabled, &v.Status, &at, &v.Revision, &v.ServiceCount); err != nil {
			return nil, err
		}
		if at.Valid {
			t := stamp06(at.Time)
			v.LastCheckedAt = &t
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (a *App) moduleDescription06(ctx context.Context, c api.ServerConnection) (serverv1.Description, error) {
	runtime, ok := a.Config.Services.(*ServerModules06)
	if !ok || runtime == nil || runtime.Prepare == nil {
		return serverv1.Description{}, serverv1.ErrUnavailable
	}
	p, err := runtime.Prepare(ctx, c.ID)
	if err != nil {
		return serverv1.Description{}, serverv1.ErrUnavailable
	}
	req := serverv1.Request{Protocol: serverv1.Protocol, Method: "describe"}
	r, err := p.Call(ctx, req)
	if err != nil || !r.Matches(req) || r.State != "done" || r.Description == nil || r.Description.ID != c.ModuleID || r.Description.Validate() != nil {
		return serverv1.Description{}, serverv1.ErrUnavailable
	}
	if _, err = a.Store.(*MySQL).DB.ExecContext(ctx, "UPDATE server_connections SET description=? WHERE id=? AND module_id=?", jsonValue06(r.Description), c.ID, c.ModuleID); err != nil {
		return serverv1.Description{}, err
	}
	return *r.Description, nil
}
func (a *App) moduleList06(ctx context.Context) ([]api.ServerModule, error) {
	m := a.Store.(*MySQL)
	connections, err := m.connections06(ctx, "")
	if err != nil {
		return nil, err
	}
	byID := map[string]api.ServerModule{}
	for _, c := range connections {
		if previous, ok := byID[c.ModuleID]; ok && previous.Status == "ready" {
			continue
		}
		empty := api.ModuleSchema{}
		_ = json.Unmarshal(jsonValue06(serverv1.EmptySchema()), &empty)
		out := api.ServerModule{ID: c.ModuleID, Name: api.LocalizedText{"en": c.ModuleID, "zh-CN": c.ModuleID}, Version: "unknown", Status: "stopped", Capabilities: []string{}, ProductSchema: empty, OrderSchema: empty, CustomActions: []api.ServerCustomAction{}}
		check, cancel := context.WithTimeout(ctx, 2*time.Second)
		d, e := a.moduleDescription06(check, c)
		cancel()
		if e == nil {
			_ = json.Unmarshal(jsonValue06(d), &out)
			out.Status = "ready"
		} else {
			var raw []byte
			if m.DB.QueryRowContext(ctx, "SELECT description FROM server_connections WHERE id=?", c.ID).Scan(&raw) == nil && len(raw) > 0 {
				_ = json.Unmarshal(raw, &out)
			}
			out.Status = "stopped"
		}
		if out.CustomActions == nil {
			out.CustomActions = []api.ServerCustomAction{}
		}
		if out.Capabilities == nil {
			out.Capabilities = []string{}
		}
		byID[c.ModuleID] = out
	}
	out := []api.ServerModule{}
	for _, m := range byID {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (a *App) syncConnectionImages06(ctx context.Context, c api.ServerConnection) error {
	runtime, ok := a.Config.Services.(*ServerModules06)
	if !ok || !c.Enabled {
		return serverv1.ErrUnavailable
	}
	p, err := runtime.processor(ctx, c.ID, c.ModuleID)
	if err != nil {
		return err
	}
	request := serverv1.Request{Protocol: serverv1.Protocol, Method: "images"}
	r, err := p.Call(ctx, request)
	if err != nil || !r.Matches(request) || r.State != "done" {
		return serverv1.ErrUnavailable
	}
	return a.Store.(*MySQL).SyncImages06(ctx, c.ID, r.Images)
}
func (a *App) serverModulesHTTP(w http.ResponseWriter, r *http.Request, actor User, p string) bool {
	if p != "/admin/integrations/modules" && p != "/admin/integrations/connections" && !strings.HasPrefix(p, "/admin/integrations/connections/") {
		return false
	}
	m, ok := a.Store.(*MySQL)
	if !ok {
		a.commerceError(w, r, ErrCommerceUnavailable)
		return true
	}
	if err := a.syncModuleSlots06(r.Context()); err != nil {
		a.serviceControllerError06(w, r, err)
		return true
	}
	if p == "/admin/integrations/modules" && r.Method == "GET" {
		out, err := a.moduleList06(r.Context())
		if err != nil {
			a.commerceError(w, r, err)
		} else {
			jsonOut(w, 200, out)
		}
		return true
	}
	if p == "/admin/integrations/connections" && r.Method == "GET" {
		out, err := m.connections06(r.Context(), "")
		if err != nil {
			a.commerceError(w, r, err)
		} else {
			jsonOut(w, 200, out)
		}
		return true
	}
	parts := strings.Split(strings.TrimPrefix(p, "/admin/integrations/connections/"), "/")
	if len(parts) > 2 || !pluginapi.ValidConnectionID(parts[0]) {
		a.commerceError(w, r, ErrNotFound)
		return true
	}
	list, err := m.connections06(r.Context(), parts[0])
	if err != nil || len(list) != 1 {
		if err == nil {
			err = ErrNotFound
		}
		a.commerceError(w, r, err)
		return true
	}
	c := list[0]
	switch {
	case len(parts) == 1 && r.Method == "PATCH":
		var in struct {
			Revision int64   `json:"revision"`
			Name     *string `json:"name"`
			Enabled  *bool   `json:"enabled"`
		}
		if !commerceBody(w, r, "adminUpdateServerConnection", &in) {
			return true
		}
		if in.Revision != c.Revision || in.Revision >= MaxMinor {
			a.commerceError(w, r, ErrRevisionConflict)
			return true
		}
		if in.Name != nil {
			c.Name = *in.Name
		}
		if in.Enabled != nil {
			c.Enabled = *in.Enabled
		}
		tx, e := m.DB.BeginTx(r.Context(), nil)
		if e != nil {
			a.commerceError(w, r, e)
			return true
		}
		defer tx.Rollback()
		if _, err = lockActiveAdministrator(r.Context(), tx, actor.ID); err == nil {
			var result sql.Result
			result, err = tx.ExecContext(r.Context(), "UPDATE server_connections SET name=?,enabled=?,revision=revision+1 WHERE id=? AND revision=?", c.Name, c.Enabled, c.ID, in.Revision)
			if err == nil {
				n, _ := result.RowsAffected()
				if n != 1 {
					err = ErrRevisionConflict
				}
			}
		}
		if err == nil {
			err = insertCurrencyAudit(r.Context(), tx, catalogAudit(actor, "server.connection_updated", c.ID, c.Revision+1, "Server connection updated"))
		}
		if err == nil {
			err = tx.Commit()
		}
	case len(parts) == 2 && r.Method == "POST" && parts[1] == "test":
		var empty struct{}
		if !commerceBody(w, r, "adminTestServerConnection", &empty) {
			return true
		}
		_, checkErr := a.moduleDescription06(r.Context(), c)
		if checkErr == nil {
			runtime := a.Config.Services.(*ServerModules06)
			client, e := runtime.processor(r.Context(), c.ID, c.ModuleID)
			checkErr = e
			if e == nil {
				req := serverv1.Request{Protocol: serverv1.Protocol, Method: "test_connection"}
				res, e := client.Call(r.Context(), req)
				if e != nil || !res.Matches(req) || res.State != "done" {
					checkErr = serverv1.ErrUnavailable
				}
			}
		}
		status := "healthy"
		if checkErr != nil {
			status = "unreachable"
		}
		_, err = m.DB.ExecContext(r.Context(), "UPDATE server_connections SET status=?,last_checked_at=UTC_TIMESTAMP(6) WHERE id=?", status, c.ID)
	case len(parts) == 2 && r.Method == "POST" && parts[1] == "sync-images":
		var empty struct{}
		if !commerceBody(w, r, "adminSyncConnectionImages", &empty) {
			return true
		}
		err = a.syncConnectionImages06(r.Context(), c)
		if err == nil {
			all, e := m.Images06(r.Context())
			err = e
			if e == nil {
				out := []api.AdminOsImage{}
				for _, image := range all {
					if image.ConnectionID == c.ID {
						out = append(out, image)
					}
				}
				jsonOut(w, 200, out)
				return true
			}
		}
	default:
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return true
	}
	if err != nil {
		a.serviceControllerError06(w, r, err)
		return true
	}
	list, err = m.connections06(r.Context(), c.ID)
	if err != nil || len(list) != 1 {
		a.commerceError(w, r, errors.Join(err, ErrNotFound))
	} else {
		jsonOut(w, 200, list[0])
	}
	return true
}
