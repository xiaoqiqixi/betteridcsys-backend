package billing

import (
	api "aster/billing/internal/financeapi"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func (a *App) commerceOrdersHTTP(w http.ResponseWriter, r *http.Request, u User, p string) bool {
	admin := strings.HasPrefix(p, "/admin/")
	path := strings.TrimPrefix(p, "/admin")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] != "orders" {
		return false
	}
	modern := a.commerceContract06(r)
	if len(parts) == 1 && r.Method == "POST" && !admin && !modern && r.Header.Get("X-Aster-API-Version") != "0.5" {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
		if err != nil {
			a.commerceError(w, r, ErrCommerceInput)
			return true
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) == nil {
			_, modern = fields["items"]
		}
	}
	if !modern {
		return false
	}
	m, ok := a.Store.(*MySQL)
	if !ok {
		fail(w, r, 503, "FEATURE_UNAVAILABLE", "Order operations require persistent storage")
		return true
	}
	if len(parts) == 1 {
		if r.Method == "POST" && !admin {
			key := commerceKey06(w, r)
			if key == "" {
				return true
			}
			var input api.CreateOrderInput
			if !commerceBody(w, r, "createOrder", &input) {
				return true
			}
			out, err := m.CreateOrder06(r.Context(), u, input, key)
			if err != nil {
				a.commerceError(w, r, err)
			} else {
				jsonOut(w, 200, out)
			}
			return true
		}
		if r.Method == "GET" {
			a.orderList06HTTP(w, r, u, admin)
			return true
		}
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
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
	if len(parts) == 2 && r.Method == "GET" {
		out, err := m.Order06(r.Context(), owner, id)
		if err != nil {
			a.commerceError(w, r, err)
		} else if admin {
			jsonOut(w, 200, adminOrder06(out))
		} else {
			jsonOut(w, 200, out.Order)
		}
		return true
	}
	if len(parts) == 3 && parts[2] == "cancel" && r.Method == "POST" {
		op := "cancelOrder"
		if admin {
			op = "adminCancelOrder"
		}
		var input api.CancelOrderInput
		if !commerceBody(w, r, op, &input) {
			return true
		}
		actor := u
		if !admin {
			actor.Role = "customer"
		}
		out, err := m.CancelOrder06(r.Context(), actor, id, input.Reason)
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
func adminOrder06(record orderRecord06) api.AdminGetOrderResponse {
	v := record.Order
	return api.AdminGetOrderResponse{ID: v.ID, Number: v.Number, Status: v.Status, Items: v.Items, Total: v.Total, InvoiceID: v.InvoiceID, CouponCode: v.CouponCode, CreatedAt: v.CreatedAt, PaidAt: v.PaidAt, CancelledAt: v.CancelledAt, ClientID: record.ClientID, ClientEmail: record.ClientEmail}
}
func (a *App) orderList06HTTP(w http.ResponseWriter, r *http.Request, u User, admin bool) {
	limit := 50
	var err error
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
	}
	if err != nil || limit < 1 || limit > 100 {
		a.commerceError(w, r, ErrCommerceInput)
		return
	}
	owner := u.ID
	if admin {
		owner = r.URL.Query().Get("client_id")
	}
	rows, next, err := a.Store.(*MySQL).OrderPage06(r.Context(), owner, r.URL.Query().Get("cursor"), r.URL.Query().Get("status"), r.URL.Query().Get("q"), limit)
	if err != nil {
		a.commerceError(w, r, err)
		return
	}
	if admin {
		out := api.AdminListOrdersResponse{Items: []api.AdminOrderSummary{}, NextCursor: next}
		for _, row := range rows {
			v := row.Order
			quantity := int64(0)
			for _, item := range v.Items {
				quantity += item.Quantity
			}
			out.Items = append(out.Items, api.AdminOrderSummary{ID: v.ID, Number: v.Number, Status: v.Status, Total: v.Total, Items: quantity, CreatedAt: v.CreatedAt, ClientID: row.ClientID, ClientEmail: row.ClientEmail})
		}
		jsonOut(w, 200, out)
	} else {
		out := api.ListOrdersResponse{Items: []api.Order{}, NextCursor: next}
		for _, row := range rows {
			out.Items = append(out.Items, row.Order)
		}
		jsonOut(w, 200, out)
	}
}
