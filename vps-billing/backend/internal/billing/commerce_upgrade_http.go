package billing

import (
	api "aster/billing/internal/financeapi"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

func (a *App) upgrade06HTTP(w http.ResponseWriter, r *http.Request, u User, p string) bool {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) < 3 || parts[0] != "services" || (parts[2] != "upgrade" && parts[2] != "upgrade-options") {
		return false
	}
	m, ok := a.Store.(*MySQL)
	if !ok {
		fail(w, r, 503, "FEATURE_UNAVAILABLE", "Service billing requires persistent storage")
		return true
	}
	id := parts[1]
	if !validCancellationID(id) {
		a.commerceError(w, r, ErrNotFound)
		return true
	}
	op := ""
	switch {
	case len(parts) == 3 && parts[2] == "upgrade-options" && r.Method == "GET":
		op = "options"
	case len(parts) == 4 && parts[2] == "upgrade" && parts[3] == "quote" && r.Method == "POST":
		op = "quote"
	case len(parts) == 3 && parts[2] == "upgrade" && r.Method == "POST":
		op = "change"
	default:
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return true
	}
	var input api.UpgradeServiceInput
	key := ""
	if op == "quote" {
		var in api.QuoteUpgradeInput
		if !commerceBody(w, r, "quoteUpgrade", &in) {
			return true
		}
		input.TargetProductID = in.TargetProductID
	}
	if op == "change" {
		key = commerceKey06(w, r)
		if key == "" {
			return true
		}
		if !commerceBody(w, r, "upgradeService", &input) {
			return true
		}
		// Successful idempotent responses survive a later controller outage.
		raw, _ := json.Marshal(input)
		fp := digest(id + ":" + string(raw))
		var stored string
		var response []byte
		err := m.DB.QueryRowContext(r.Context(), "SELECT fingerprint,response FROM commerce_requests WHERE user_id=? AND operation='service:upgrade' AND request_key=?", u.ID, key).Scan(&stored, &response)
		if err == nil {
			if stored != fp {
				a.commerceError(w, r, ErrConflict)
			} else {
				jsonOut(w, 200, json.RawMessage(response))
			}
			return true
		}
		if !errors.Is(err, sql.ErrNoRows) {
			a.commerceError(w, r, err)
			return true
		}
	}
	service, err := m.Service06(r.Context(), u.ID, id)
	if err != nil {
		a.commerceError(w, r, err)
		return true
	}
	if a.Config.Services == nil {
		fail(w, r, 501, "FEATURE_UNAVAILABLE", "Controller does not support plan changes")
		return true
	}
	remote, err := a.Config.Services.InspectService(r.Context(), service)
	if err != nil {
		a.serviceControllerError06(w, r, err)
		return true
	}
	if !remote.Capabilities.Upgrade {
		fail(w, r, 501, "FEATURE_UNAVAILABLE", "Controller does not support plan changes")
		return true
	}
	var out any
	switch op {
	case "options":
		out, err = m.UpgradeOptions06(r.Context(), u.ID, id, time.Now().UTC())
	case "quote":
		out, err = m.UpgradeQuote06(r.Context(), u.ID, id, input.TargetProductID, time.Now().UTC())
	case "change":
		out, err = m.UpgradeService06(r.Context(), u, id, input, key, time.Now().UTC())
	}
	if err != nil {
		a.commerceError(w, r, err)
	} else {
		jsonOut(w, 200, out)
	}
	return true
}
