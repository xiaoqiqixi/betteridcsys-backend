package billing

import (
	api "aster/billing/internal/financeapi"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// Production defaults to 0.6. The explicit selector preserves overlapping 0.5
// projections for one release; non-overlapping 0.6 routes need no header.
func (a *App) commerceContract06(r *http.Request) bool {
	return r.Header.Get("X-Aster-API-Version") == "0.6" || r.Header.Get("X-Aster-API-Version") == "" && a.Config.CommerceContractVersion == "0.6"
}
func commerceKey06(w http.ResponseWriter, r *http.Request) string {
	key := r.Header.Get("Idempotency-Key")
	if !keyPattern.MatchString(key) {
		fail(w, r, 400, "IDEMPOTENCY_KEY_REQUIRED", "Provide an 8–128 character Idempotency-Key")
		return ""
	}
	return key
}
func (a *App) commerceInvoiceHTTP(w http.ResponseWriter, r *http.Request, u User, p string) bool {
	admin := strings.HasPrefix(p, "/admin/")
	path := strings.TrimPrefix(p, "/admin")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if p != "/credit/top-up" && (len(parts) == 0 || parts[0] != "invoices") {
		return false
	}
	// Checkout and provider status polling retain their existing protocol handler.
	if len(parts) == 3 && parts[2] == "checkout" || len(parts) == 4 && parts[2] == "payments" && !admin {
		return false
	}
	if p != "/credit/top-up" && r.Method == "GET" && len(parts) <= 2 && !a.commerceContract06(r) {
		return false
	}
	m, ok := a.Store.(*MySQL)
	if !ok && len(parts) == 3 && parts[2] == "pay" && !admin {
		return false
	}
	if !ok {
		fail(w, r, 503, "FEATURE_UNAVAILABLE", "Invoice operations require persistent storage")
		return true
	}
	if p == "/credit/top-up" {
		if r.Method != "POST" {
			fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
			return true
		}
		key := commerceKey06(w, r)
		if key == "" {
			return true
		}
		var input api.CreateTopUpInput
		if !commerceBody(w, r, "createTopUp", &input) {
			return true
		}
		out, err := m.TopUp06(r.Context(), u, input.AmountMinor, key)
		if err != nil {
			a.commerceError(w, r, err)
		} else {
			jsonOut(w, 200, out)
		}
		return true
	}
	if len(parts) == 1 {
		if admin && r.Method == "POST" {
			key := commerceKey06(w, r)
			if key == "" {
				return true
			}
			var input api.AdminCreateInvoiceInput
			if !commerceBody(w, r, "adminCreateInvoice", &input) {
				return true
			}
			out, err := m.CreateInvoice06(r.Context(), u, input, key)
			if err != nil {
				a.commerceError(w, r, err)
			} else {
				jsonOut(w, 200, out)
			}
			return true
		}
		if r.Method == "GET" {
			a.invoiceList06HTTP(w, r, u, admin)
			return true
		}
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return true
	}
	id := parts[1]
	if !validCancellationID(id) {
		fail(w, r, 404, "NOT_FOUND", "Invoice not found")
		return true
	}
	if len(parts) == 2 && r.Method == "GET" {
		owner := u.ID
		if admin {
			owner = ""
		}
		out, err := m.Invoice06(r.Context(), owner, id)
		if err != nil {
			a.commerceError(w, r, err)
		} else if admin {
			jsonOut(w, 200, out)
		} else {
			jsonOut(w, 200, customerInvoice06(out))
		}
		return true
	}
	if !admin && len(parts) == 3 && parts[2] == "pay" && r.Method == "POST" {
		key := commerceKey06(w, r)
		if key == "" {
			return true
		}
		var input struct {
			Method    string `json:"method"`
			GatewayID string `json:"gateway_id,omitempty"`
		}
		if !commerceBody(w, r, "payInvoice", &input) {
			return true
		}
		if input.Method == "credit" {
			if input.GatewayID != "" {
				a.commerceError(w, r, ErrCommerceInput)
				return true
			}
			v, err := m.PayCredit06(r.Context(), u, id, key)
			if err != nil {
				a.commerceError(w, r, err)
			} else {
				jsonOut(w, 200, map[string]any{"status": "paid", "invoice": v})
			}
			return true
		}
		invoice, err := a.Store.Invoice(r.Context(), u.ID, id)
		if err != nil {
			a.paymentStoreError(w, r, err)
			return true
		}
		a.checkoutWithGateway(w, r, u, invoice, input.GatewayID, true)
		return true
	}
	if !admin {
		return false
	}
	if r.Method != "POST" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return true
	}
	if len(parts) == 3 {
		switch parts[2] {
		case "payments":
			key := commerceKey06(w, r)
			if key == "" {
				return true
			}
			var input api.AdminAddPaymentInput
			if !commerceBody(w, r, "adminAddPayment", &input) {
				return true
			}
			v, err := m.AddInvoicePayment06(r.Context(), u, id, input, key)
			if err != nil {
				a.commerceError(w, r, err)
			} else {
				jsonOut(w, 200, v)
			}
			return true
		case "cancel":
			var input api.AdminCancelInvoiceInput
			if !commerceBody(w, r, "adminCancelInvoice", &input) {
				return true
			}
			v, err := m.CancelInvoice06(r.Context(), u, id, input.Reason)
			if err != nil {
				a.commerceError(w, r, err)
			} else {
				jsonOut(w, 200, v)
			}
			return true
		case "refunds":
			key := commerceKey06(w, r)
			if key == "" {
				return true
			}
			var input api.AdminRefundInput
			if !commerceBody(w, r, "adminRefund", &input) {
				return true
			}
			var v api.AdminInvoice
			var err error
			if input.Destination == "original" {
				v, err = a.refundOriginal06(r.Context(), u, id, input, key)
			} else {
				v, err = m.RefundCredit06(r.Context(), u, id, input, key)
			}
			if err != nil {
				a.commerceError(w, r, err)
			} else {
				jsonOut(w, 200, v)
			}
			return true
		}
	}
	if len(parts) == 5 && parts[2] == "payments" && parts[4] == "void" {
		var input api.AdminVoidPaymentAttemptInput
		if !commerceBody(w, r, "adminVoidPaymentAttempt", &input) {
			return true
		}
		attempt, err := m.PaymentAttempt(r.Context(), u, parts[3])
		if err == nil && attempt.Expectation.InvoiceID != id {
			err = ErrNotFound
		}
		if err == nil {
			_, err = m.VoidPayment(r.Context(), u, parts[3], input.Reason)
		}
		if err != nil {
			a.commerceError(w, r, err)
			return true
		}
		v, err := m.Invoice06(r.Context(), "", id)
		if err != nil {
			a.commerceError(w, r, err)
		} else {
			jsonOut(w, 200, v)
		}
		return true
	}
	fail(w, r, 404, "NOT_FOUND", "Route not found")
	return true
}
func (a *App) invoiceList06HTTP(w http.ResponseWriter, r *http.Request, u User, admin bool) {
	limit := 50
	var err error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
	}
	if err != nil || limit < 1 || limit > 100 {
		a.commerceError(w, r, ErrCommerceInput)
		return
	}
	owner := u.ID
	if admin {
		owner = r.URL.Query().Get("client_id")
	}
	m := a.Store.(*MySQL)
	records, next, err := m.InvoicePage06(r.Context(), owner, r.URL.Query().Get("cursor"), r.URL.Query().Get("status"), r.URL.Query().Get("kind"), r.URL.Query().Get("q"), limit)
	if err != nil {
		a.commerceError(w, r, err)
		return
	}
	if admin {
		out := api.AdminListInvoicesResponse{Items: []api.AdminInvoiceSummary{}, NextCursor: next}
		for _, v := range records {
			raw, _ := json.Marshal(v)
			var item api.AdminInvoiceSummary
			_ = json.Unmarshal(raw, &item)
			out.Items = append(out.Items, item)
		}
		jsonOut(w, 200, out)
	} else {
		out := api.ListInvoicesResponse{Items: []api.InvoiceSummary{}, NextCursor: next}
		for _, v := range records {
			raw, _ := json.Marshal(v)
			var item api.InvoiceSummary
			_ = json.Unmarshal(raw, &item)
			out.Items = append(out.Items, item)
		}
		jsonOut(w, 200, out)
	}
}
