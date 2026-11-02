package billing

import (
	api "aster/billing/internal/financeapi"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *App) commerceServicesHTTP(w http.ResponseWriter, r *http.Request, u User, p string) bool {
	admin := strings.HasPrefix(p, "/admin/")
	path := strings.TrimPrefix(p, "/admin")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] != "services" {
		return false
	}
	if len(parts) == 1 && !a.commerceContract06(r) {
		if r.Method == "DELETE" {
			fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
			return true
		}
		return false
	}
	if len(parts) > 2 && parts[2] != "cancellation" && parts[2] != "renew" {
		return false
	}
	m, ok := a.Store.(*MySQL)
	if !ok {
		fail(w, r, 503, "FEATURE_UNAVAILABLE", "Service management requires persistent storage")
		return true
	}
	if len(parts) == 1 {
		if r.Method != "GET" {
			fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
			return true
		}
		limit := 50
		var err error
		if v := r.URL.Query().Get("limit"); v != "" {
			limit, err = strconv.Atoi(v)
		}
		if err != nil || limit < 1 || limit > 100 {
			a.commerceError(w, r, ErrCommerceInput)
			return true
		}
		owner := u.ID
		if admin {
			owner = r.URL.Query().Get("client_id")
		}
		rows, next, err := m.ServicePage06(r.Context(), owner, r.URL.Query().Get("cursor"), r.URL.Query().Get("status"), r.URL.Query().Get("q"), limit)
		if err != nil {
			a.commerceError(w, r, err)
			return true
		}
		if admin {
			jsonOut(w, 200, api.AdminListServicesResponse{Items: rows, NextCursor: next})
		} else {
			out := api.ListServicesResponse{Items: []api.ServiceSummary{}, NextCursor: next}
			for _, row := range rows {
				out.Items = append(out.Items, customerServiceSummary06(row))
			}
			jsonOut(w, 200, out)
		}
		return true
	}
	id := parts[1]
	if !validCancellationID(id) {
		a.commerceError(w, r, ErrNotFound)
		return true
	}
	owner := u.ID
	if admin {
		owner = ""
	}
	if len(parts) == 2 {
		var out api.AdminService
		var err error
		switch r.Method {
		case "GET":
			out, err = m.Service06(r.Context(), owner, id)
			if err == nil && a.Config.Services != nil {
				remote, inspectErr := a.Config.Services.InspectService(r.Context(), out)
				if inspectErr == nil {
					mergeServiceProjection06(&out, remote)
				}
			}
		case "PATCH":
			op := "updateService"
			if admin {
				op = "adminUpdateService"
			}
			var in servicePatch06
			if !commerceBody(w, r, op, &in) {
				return true
			}
			out, err = m.PatchService06(r.Context(), u, id, admin, in)
		default:
			fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
			return true
		}
		if err != nil {
			a.commerceError(w, r, err)
		} else if admin {
			jsonOut(w, 200, out)
		} else {
			jsonOut(w, 200, customerService06(out))
		}
		return true
	}
	if len(parts) == 3 && parts[2] == "cancellation" && !admin {
		var in *api.RequestCancellationInput
		switch r.Method {
		case "POST":
			in = &api.RequestCancellationInput{}
			if !commerceBody(w, r, "requestCancellation", in) {
				return true
			}
		case "DELETE":
		default:
			fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
			return true
		}
		out, err := m.CancelService06(r.Context(), u, id, in)
		if err != nil {
			a.commerceError(w, r, err)
		} else {
			jsonOut(w, 200, out)
		}
		return true
	}
	if len(parts) == 3 && parts[2] == "renew" && !admin && r.Method == "POST" {
		key := commerceKey06(w, r)
		if key == "" {
			return true
		}
		var in api.RenewServiceInput
		if !commerceBody(w, r, "renewService", &in) {
			return true
		}
		out, err := m.RenewService06(r.Context(), u, id, in.Cycle, key, time.Now().UTC())
		if err != nil {
			a.commerceError(w, r, err)
		} else {
			jsonOut(w, 200, out)
		}
		return true
	}
	fail(w, r, 404, "NOT_FOUND", "Route not found")
	return true
}
