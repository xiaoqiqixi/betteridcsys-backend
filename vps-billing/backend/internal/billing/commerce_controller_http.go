package billing

import (
	api "aster/billing/internal/financeapi"
	"aster/billing/serviceapi"
	"errors"
	"net/http"
	"strings"
	"time"
)

func (a *App) serviceControllerError06(w http.ResponseWriter, r *http.Request, err error) {
	var remote *serviceapi.HTTPError
	switch {
	case errors.Is(err, serviceapi.ErrUnavailable):
		fail(w, r, 501, "FEATURE_UNAVAILABLE", "Controller capability is unavailable")
	case errors.Is(err, serviceapi.ErrConfiguration):
		a.commerceError(w, r, ErrCommerceInput)
	case errors.As(err, &remote):
		switch remote.StatusCode {
		case 409:
			fail(w, r, 409, "SERVICE_STATE_CONFLICT", "Controller state does not permit this operation")
		case 501:
			fail(w, r, 501, "FEATURE_UNAVAILABLE", "Controller capability is unavailable")
		default:
			fail(w, r, 502, "CONTROLLER_UNAVAILABLE", "Controller request could not be completed")
		}
	case errors.Is(err, serviceapi.ErrUncertain):
		fail(w, r, 502, "CONTROLLER_UNAVAILABLE", "Controller response is unavailable; inspect the task before any further operation")
	default:
		a.commerceError(w, r, err)
	}
}
func (a *App) serviceReads06HTTP(w http.ResponseWriter, r *http.Request, u User, p string) bool {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) != 3 || parts[0] != "services" || !(parts[2] == "console" || parts[2] == "metrics" || parts[2] == "snapshots" && r.Method == "GET") {
		return false
	}
	op := parts[2]
	if op == "console" && r.Method != "POST" || op == "metrics" && r.Method != "GET" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return true
	}
	if op == "console" {
		var in struct{}
		if !commerceBody(w, r, "openServiceConsole", &in) {
			return true
		}
	}
	m, ok := a.Store.(*MySQL)
	if !ok {
		a.serviceControllerError06(w, r, serviceapi.ErrUnavailable)
		return true
	}
	v, err := m.Service06(r.Context(), u.ID, parts[1])
	if err != nil {
		a.commerceError(w, r, err)
		return true
	}
	runtime, ok := a.Config.Services.(*ServiceControllers06)
	if !ok {
		a.serviceControllerError06(w, r, serviceapi.ErrUnavailable)
		return true
	}
	client, err := runtime.client(r.Context(), v)
	if err != nil {
		a.serviceControllerError06(w, r, err)
		return true
	}
	remote, err := client.Service(r.Context(), *v.ResourceID, v.ClientID)
	if err != nil {
		a.serviceControllerError06(w, r, err)
		return true
	}
	projected, err := serviceProjection06(v, remote)
	if err != nil {
		a.serviceControllerError06(w, r, err)
		return true
	}
	capable := op == "console" && projected.Capabilities.Console || op == "metrics" && projected.Capabilities.Metrics || op == "snapshots" && projected.Capabilities.Snapshots
	if !capable {
		a.serviceControllerError06(w, r, serviceapi.ErrUnavailable)
		return true
	}
	var result any
	switch op {
	case "console":
		var ticket serviceapi.Console
		ticket, err = client.Console(r.Context(), *v.ResourceID, v.ClientID)
		if err == nil && (!ticket.ExpiresAt.After(time.Now()) || ticket.ExpiresAt.After(time.Now().Add(90*time.Second))) {
			err = serviceapi.ErrUncertain
		}
		result = api.ConsoleTicket{URL: ticket.URL, ExpiresAt: stamp06(ticket.ExpiresAt)}
		w.Header().Set("Cache-Control", "no-store")
	case "metrics":
		period := r.URL.Query().Get("range")
		if period == "" {
			period = "24h"
		}
		if !serviceapi.ValidRange(period) {
			a.commerceError(w, r, ErrCommerceInput)
			return true
		}
		var metrics serviceapi.Metrics
		metrics, err = client.Metrics(r.Context(), *v.ResourceID, v.ClientID, period)
		if err == nil {
			result, err = serviceMetrics06(period, metrics)
		}
	case "snapshots":
		var snapshots serviceapi.Snapshots
		snapshots, err = client.Snapshots(r.Context(), *v.ResourceID, v.ClientID)
		if err == nil {
			result, err = snapshots06(snapshots)
		}
	}
	if err != nil {
		a.serviceControllerError06(w, r, err)
	} else {
		jsonOut(w, 200, result)
	}
	return true
}
