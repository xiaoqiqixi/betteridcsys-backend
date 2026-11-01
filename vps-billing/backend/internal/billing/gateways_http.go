package billing

import (
	"aster/billing/internal/runtimeplugins"
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

type GatewayRuntime interface {
	PaymentRuntime
	PaymentGatewaySelection(string) (pluginapi.Manifest, pluginapi.Selection, error)
	PaymentGateways(context.Context) ([]runtimeplugins.Gateway, error)
	QueryPayment(context.Context, paymentv1.Config, paymentv1.Query) (paymentv1.QueryResult, error)
}
type publicGateway struct {
	ID          string                     `json:"id"`
	Name        map[string]string          `json:"name"`
	Description map[string]string          `json:"description,omitempty"`
	Logo        *string                    `json:"logo"`
	Flow        string                     `json:"flow"`
	Currencies  []string                   `json:"currencies"`
	Fee         *paymentv1.Fee             `json:"fee"`
	Min         []paymentv1.CurrencyAmount `json:"min"`
	Max         []paymentv1.CurrencyAmount `json:"max"`
}
type gatewayStatus struct {
	ID            string            `json:"id"`
	Name          map[string]string `json:"name"`
	PluginID      string            `json:"plugin_id"`
	PluginVersion string            `json:"plugin_version"`
	Flow          string            `json:"flow"`
	Enabled       bool              `json:"enabled"`
	Status        string            `json:"status"`
	Currencies    []string          `json:"currencies"`
	DisplayOrder  int64             `json:"display_order"`
	LastCheckedAt *time.Time        `json:"last_checked_at"`
	Revision      int64             `json:"revision"`
}
type gatewayView struct {
	publicGateway
	admin gatewayStatus
}

func (a *App) gateways(ctx context.Context) ([]gatewayView, error) {
	out := []gatewayView{}
	runtime, ok := a.Config.PaymentRuntime.(GatewayRuntime)
	if !ok {
		return out, nil
	}
	store, ok := a.Store.(gatewayStore)
	if !ok {
		return nil, ErrUnavailable
	}
	slots, err := runtime.PaymentGateways(ctx)
	if err != nil {
		return nil, err
	}
	for _, slot := range slots {
		id := slot.Selection.Slot
		if id == "" {
			id = slot.Selection.ID
		}
		settings, err := store.GatewaySettings(ctx, id)
		if err != nil {
			return nil, err
		}
		d := slot.Manifest.Payment
		if d == nil {
			d = &paymentv1.Declarations{}
		}
		name := d.Name
		if len(name) == 0 || paymentv1.ValidateLocalized(name, 4000) != nil {
			name = map[string]string{"en": slot.Selection.ID}
		}
		if len(settings.Name) > 0 {
			name = settings.Name
		}
		flow := d.CheckoutFlow()
		if flow != "redirect" && flow != "qr" && flow != "instructions" {
			flow = "redirect"
		}
		public := publicGateway{ID: id, Name: name, Description: d.Description, Flow: flow, Currencies: []string{}, Fee: d.Fee, Min: append([]paymentv1.CurrencyAmount{}, d.Min...), Max: append([]paymentv1.CurrencyAmount{}, d.Max...)}
		if d.Logo != "" {
			logo := d.Logo
			public.Logo = &logo
		}
		if public.Fee != nil && public.Fee.Fixed == nil {
			fee := *public.Fee
			fee.Fixed = []paymentv1.CurrencyAmount{}
			public.Fee = &fee
		}
		for _, currency := range d.Currencies {
			public.Currencies = append(public.Currencies, currency.Code)
		}
		status := slot.Status
		if !settings.Enabled && status == "ready" {
			status = "stopped"
		}
		admin := gatewayStatus{ID: id, Name: name, PluginID: slot.Selection.ID, PluginVersion: slot.Selection.Version, Flow: flow, Enabled: slot.Selection.Enabled && settings.Enabled, Status: status, Currencies: public.Currencies, DisplayOrder: settings.DisplayOrder, LastCheckedAt: slot.LastCheckedAt, Revision: settings.Revision}
		out = append(out, gatewayView{publicGateway: public, admin: admin})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].admin.DisplayOrder == out[j].admin.DisplayOrder {
			return out[i].ID < out[j].ID
		}
		return out[i].admin.DisplayOrder < out[j].admin.DisplayOrder
	})
	return out, nil
}
func (a *App) listGateways(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "Unsupported method")
		return
	}
	currency := r.URL.Query().Get("currency")
	if currency != "" && !paymentv1.ValidCurrency(currency) {
		fail(w, r, 400, "INVALID_CURRENCY", "Invalid currency")
		return
	}
	views, err := a.gateways(r.Context())
	if err != nil {
		fail(w, r, 503, "PAYMENT_NOT_CONFIGURED", "Payment gateways unavailable")
		return
	}
	out := []publicGateway{}
	for _, v := range views {
		if !v.admin.Enabled || v.admin.Status != "ready" {
			continue
		}
		supported := currency == ""
		for _, c := range v.Currencies {
			supported = supported || c == currency
		}
		if supported {
			out = append(out, v.publicGateway)
		}
	}
	jsonOut(w, 200, out)
}
func (a *App) gatewayAdminHTTP(w http.ResponseWriter, r *http.Request, u User, p string) {
	views, err := a.gateways(r.Context())
	if err != nil {
		a.internal(w, r, err)
		return
	}
	if p == "/admin/integrations/gateways" && r.Method == "GET" {
		out := []gatewayStatus{}
		for _, v := range views {
			out = append(out, v.admin)
		}
		jsonOut(w, 200, out)
		return
	}
	id := strings.TrimPrefix(p, "/admin/integrations/gateways/")
	if r.Method != "PATCH" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "Unsupported method")
		return
	}
	for _, v := range views {
		if v.ID == id {
			var input GatewayPatch
			r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
			if !accountBody(w, r, "adminUpdateGateway", &input) {
				return
			}
			s, err := a.Store.(gatewayStore).UpdateGateway(r.Context(), u, id, input)
			if errors.Is(err, ErrRevisionConflict) {
				fail(w, r, 409, "REVISION_CONFLICT", "Gateway settings changed")
				return
			}
			if errors.Is(err, ErrPaymentInput) {
				fail(w, r, 400, "INVALID_BODY", "Invalid gateway settings")
				return
			}
			if err != nil {
				a.paymentStoreError(w, r, err)
				return
			}
			// Re-read effective host approval after the persistent presentation change.
			updated, err := a.gateways(r.Context())
			if err != nil {
				a.internal(w, r, err)
				return
			}
			for _, next := range updated {
				if next.ID == id {
					next.admin.Revision = s.Revision
					jsonOut(w, 200, next.admin)
					return
				}
			}
			break
		}
	}
	fail(w, r, 404, "NOT_FOUND", "Gateway not found")
}
func (a *App) gatewayRoutes(w http.ResponseWriter, r *http.Request, u User, p string) bool {
	if p == "/admin/integrations/gateways" || strings.HasPrefix(p, "/admin/integrations/gateways/") {
		a.staff("integrations.write", func(w http.ResponseWriter, r *http.Request) { a.gatewayAdminHTTP(w, r, u, p) })(w, r)
		return true
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(parts) < 3 || parts[0] != "invoices" {
		return false
	}
	if len(parts) == 3 && parts[2] == "pay" {
		if r.Method != "POST" {
			fail(w, r, 405, "METHOD_NOT_ALLOWED", "Unsupported method")
			return true
		}
		var input struct {
			Method    string `json:"method"`
			GatewayID string `json:"gateway_id"`
		}
		if !financeBody(w, r, &input) {
			return true
		}
		if input.Method != "gateway" || !pluginapi.ValidGatewayID(input.GatewayID) {
			fail(w, r, 400, "INVALID_BODY", "A supported payment method and gateway are required")
			return true
		}
		key := r.Header.Get("Idempotency-Key")
		if len(key) < 8 || !keyPattern.MatchString(key) {
			fail(w, r, 400, "IDEMPOTENCY_KEY_REQUIRED", "A valid Idempotency-Key is required")
			return true
		}
		invoice, err := a.Store.Invoice(r.Context(), u.ID, parts[1])
		if err != nil {
			a.paymentStoreError(w, r, err)
			return true
		}
		a.checkoutWithGateway(w, r, u, invoice, input.GatewayID, true)
		return true
	}
	if len(parts) == 4 && parts[2] == "payments" {
		if r.Method != "GET" {
			fail(w, r, 405, "METHOD_NOT_ALLOWED", "Unsupported method")
			return true
		}
		a.paymentStatusHTTP(w, r, u, parts[1], parts[3])
		return true
	}
	return false
}
