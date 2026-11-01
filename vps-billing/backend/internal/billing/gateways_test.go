package billing

import (
	"aster/billing/internal/contracttest"
	"aster/billing/internal/runtimeplugins"
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"context"
	"net/http/httptest"
	"testing"
	"time"
)

type gatewayTestRuntime struct {
	slots map[string]*paymentHTTPRuntime
}

func (g *gatewayTestRuntime) PaymentSelection() (pluginapi.Manifest, pluginapi.Selection, error) {
	return pluginapi.Manifest{}, pluginapi.Selection{}, paymentv1.ErrUnavailable
}
func (g *gatewayTestRuntime) PaymentGatewaySelection(id string) (pluginapi.Manifest, pluginapi.Selection, error) {
	p := g.slots[id]
	if p == nil {
		return g.PaymentSelection()
	}
	return p.PaymentSelection()
}
func (g *gatewayTestRuntime) PaymentGateways(context.Context) ([]runtimeplugins.Gateway, error) {
	out := []runtimeplugins.Gateway{}
	for _, p := range g.slots {
		m, s, _ := p.PaymentSelection()
		now := time.Now()
		out = append(out, runtimeplugins.Gateway{Manifest: m, Selection: s, Status: "ready", LastCheckedAt: &now})
	}
	return out, nil
}
func (g *gatewayTestRuntime) CreatePayment(ctx context.Context, c paymentv1.Config, i paymentv1.Intent) (paymentv1.Checkout, error) {
	out, e := g.slots[c.GatewayID].CreatePayment(ctx, c, i)
	out.GatewayID = c.GatewayID
	return out, e
}
func (g *gatewayTestRuntime) VerifyPaymentCallback(ctx context.Context, c paymentv1.Config, i paymentv1.Callback) (paymentv1.VerifiedEvent, error) {
	return g.slots[c.GatewayID].VerifyPaymentCallback(ctx, c, i)
}
func (g *gatewayTestRuntime) QueryPayment(context.Context, paymentv1.Config, paymentv1.Query) (paymentv1.QueryResult, error) {
	return paymentv1.QueryResult{}, paymentv1.ErrRejected
}

func TestMySQLGatewayHandlersContractIsolationAndRevision(t *testing.T) {
	gatewayHTTPContract(t, mysqlCatalogFixture(t), "CatalogFixture!2026", "CatalogFixture!2026")
}
func TestMemoryGatewayHandlersContractIsolationAndRevision(t *testing.T) {
	gatewayHTTPContract(t, memoryCatalogFixture(t), "DemoAdmin!2026", "DemoCustomer!2026")
}
func gatewayHTTPContract(t *testing.T, f catalogFixture, adminPassword, customerPassword string) {
	if db, ok := f.store.(*MySQL); ok {
		migrationExec(t, t.Context(), db.DB, "UPDATE plans SET available=TRUE WHERE id='starter'")
		migrationExec(t, t.Context(), db.DB, "UPDATE plan_prices SET available=TRUE WHERE plan_id='starter' AND currency='CNY'")
	}
	g := &gatewayTestRuntime{slots: map[string]*paymentHTTPRuntime{}}
	for _, id := range []string{"gateway-a", "gateway-b"} {
		p := newPaymentHTTPRuntime(t)
		p.selection.Slot = id
		g.slots[id] = p
	}
	srv := httptest.NewServer(NewApp(f.store, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), PaymentRuntime: g}).Handler())
	defer srv.Close()
	customer := client(t, srv.URL, f.customer.Email, customerPassword)
	admin := client(t, srv.URL, f.admin.Email, adminPassword)
	guest := client(t, srv.URL, "", "")
	for _, c := range []struct {
		client         *testClient
		path, template string
	}{{guest, "/payment-gateways?currency=CNY", "/payment-gateways"}, {admin, "/admin/integrations/gateways", "/admin/integrations/gateways"}} {
		status, out := c.client.request("GET", "/api/v1"+c.path, nil, "")
		if status != 200 {
			t.Fatal(status, out)
		}
		contracttest.Response(t, "GET", c.template, out)
	}
	if status, _ := customer.request("PATCH", "/api/v1/admin/integrations/gateways/gateway-a", map[string]any{"revision": 1, "enabled": false}, ""); status != 403 {
		t.Fatal("customer changed gateway", status)
	}
	status, out := admin.request("PATCH", "/api/v1/admin/integrations/gateways/gateway-a", map[string]any{"revision": 1, "name": map[string]string{"en": "Card A"}, "display_order": -1}, "")
	if status != 200 {
		t.Fatal(status, out)
	}
	contracttest.Response(t, "PATCH", "/admin/integrations/gateways/{id}", out)
	if status, _ := admin.request("PATCH", "/api/v1/admin/integrations/gateways/gateway-a", map[string]any{"revision": 1, "enabled": false}, ""); status != 409 {
		t.Fatal("stale revision", status)
	}
	if status, _ := admin.request("PATCH", "/api/v1/admin/integrations/gateways/gateway-a", map[string]any{"revision": 2, "credentials": "forbidden"}, ""); status != 400 {
		t.Fatal("credentials accepted", status)
	}
	p := paymentFixture{store: f.store, owner: f.customer, ctx: t.Context()}
	for _, id := range []string{"gateway-a", "gateway-b"} {
		_, invoice := p.order(t)
		status, out := customer.request("POST", "/api/v1/invoices/"+invoice.ID+"/pay", map[string]string{"method": "gateway", "gateway_id": id}, "pay-"+invoice.ID)
		if status != 200 {
			t.Fatal(status, out)
		}
		contracttest.Response(t, "POST", "/invoices/{id}/pay", out)
		pid := out["data"].(map[string]any)["payment_id"].(string)
		status, out = customer.request("GET", "/api/v1/invoices/"+invoice.ID+"/payments/"+pid, nil, "")
		if status != 200 {
			t.Fatal(status, out)
		}
		contracttest.Response(t, "GET", "/invoices/{id}/payments/{payment_id}", out)
		e := g.slots[id].event(t, 0)
		e.GatewayID = id
		e.EventID = "shared-event"
		e.TransactionID = "shared-transaction"
		raw, sig := g.slots[id].signed(t, e)
		h := &paymentHTTPHarness{server: srv}
		wrong := "gateway-b"
		if id == wrong {
			wrong = "gateway-a"
		}
		status, _ = paymentHTTPNotify(t, h, "POST", "/api/v1/payment-callbacks/"+wrong, raw, sig, "")
		if status != 400 {
			t.Fatal("callback crossed slot", status)
		}
		for range 2 {
			status, out = paymentHTTPNotify(t, h, "POST", "/api/v1/payment-callbacks/"+id, raw, sig, "")
			if status != 200 {
				t.Fatal("per-gateway dedupe or replay failed", status, out)
			}
		}
		status, out = customer.request("GET", "/api/v1/invoices/"+invoice.ID+"/payments/"+pid, nil, "")
		if status != 200 || out["data"].(map[string]any)["status"] != "succeeded" {
			t.Fatal(status, out)
		}
	}
}
