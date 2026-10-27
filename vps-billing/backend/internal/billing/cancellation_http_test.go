package billing

import (
	"aster/billing/internal/integrations"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func cancellationRawRequest(t *testing.T, c *testClient, path, raw, origin, host string) (int, map[string]any) {
	t.Helper()
	r, err := http.NewRequest("POST", c.base+path, bytes.NewBufferString(raw))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", c.csrf)
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if host != "" {
		r.Host = host
	}
	response, err := c.c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var out map[string]any
	if err = json.NewDecoder(response.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, out
}

func TestCancellationHTTPAuthorizationAndPair(t *testing.T) {
	m := NewMemory()
	owner, _, _ := cancellationUsers(t, m)
	o, invoice, _ := cancellationOrder(t, m, owner)
	srv := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()}).Handler())
	defer srv.Close()
	customer := client(t, srv.URL, "demo@aster.local", "DemoCustomer!2026")
	other := client(t, srv.URL, "other@aster.local", "DemoOther!2026")
	admin := client(t, srv.URL, "admin@aster.local", "DemoAdmin!2026")
	anonymous := client(t, srv.URL, "", "")
	path := "/api/v1/orders/" + o.ID + "/cancel"
	body := map[string]string{"reason": "No longer needed"}
	for _, tc := range []struct {
		name, path string
		client     *testClient
		status     int
	}{
		{"anonymous", path, anonymous, 401},
		{"other owner", path, other, 404},
		{"admin customer path retains ownership", path, admin, 404},
		{"customer admin path", "/api/v1/admin/orders/" + o.ID + "/cancel", customer, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if status, out := tc.client.request("POST", tc.path, body, ""); status != tc.status {
				t.Fatal(status, out)
			}
		})
	}
	if status, out := customer.request("GET", path, nil, ""); status != 405 {
		t.Fatal(status, out)
	}
	token := customer.csrf
	customer.csrf = "bad"
	if status, out := customer.request("POST", path, body, ""); status != 403 {
		t.Fatal(status, out)
	}
	customer.csrf = token
	if status, out := cancellationRawRequest(t, customer, path, `{"reason":"No longer needed"}`, "https://evil.example", ""); status != 403 {
		t.Fatal(status, out)
	}
	if status, out := cancellationRawRequest(t, customer, path, `{"reason":"No longer needed"}`, "", "evil.example"); status != 421 {
		t.Fatal(status, out)
	}
	if len(m.audits) != 1 || len(m.orderEvents) != 1 {
		t.Fatal("authorization failures changed history")
	}
	status, result := admin.request("POST", "/api/v1/admin/orders/"+o.ID+"/cancel", body, "")
	if status != 200 {
		t.Fatal(status, result)
	}
	data := result["data"].(map[string]any)
	order := data["order"].(map[string]any)
	inv := data["invoice"].(map[string]any)
	if order["id"] != o.ID || order["status"] != "cancelled" || inv["id"] != invoice.ID || inv["status"] != "void" || inv["order_id"] != o.ID {
		t.Fatal("unpaired response", result)
	}
	for range 2 {
		if status, out := customer.request("POST", path, body, ""); status != 200 {
			t.Fatal("retry failed", status, out)
		}
	}
	if len(m.audits) != 2 || len(m.orderEvents) != 2 {
		t.Fatal("HTTP retries duplicated cancellation")
	}
	if status, out := customer.request("POST", "/api/v1/invoices/"+invoice.ID+"/checkout", nil, ""); status != 409 {
		t.Fatal("void invoice could be checked out", status, out)
	}
	second, _, _ := cancellationOrder(t, m, owner)
	if status, out := customer.request("POST", "/api/v1/orders/"+second.ID+"/cancel", body, ""); status != 200 {
		t.Fatal("owner cancellation", status, out)
	}
}

func TestCancellationHTTPStrictInputAndState(t *testing.T) {
	m := NewMemory()
	owner, _, _ := cancellationUsers(t, m)
	o, invoice, _ := cancellationOrder(t, m, owner)
	srv := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()}).Handler())
	defer srv.Close()
	c := client(t, srv.URL, "demo@aster.local", "DemoCustomer!2026")
	path := "/api/v1/orders/" + o.ID + "/cancel"
	for _, raw := range []string{
		`{}`, `null`, `[]`, `{"reason":null}`, `{"Reason":"No longer needed"}`, `{"reason":"A","reason":"B"}`,
		`{"reason":"A","status":"paid"}`, `{"reason":1}`, `{"reason":true}`, `{"reason":[]}`, `{"reason":"A"} {}`,
		`{"reason":""}`, `{"reason":"   "}`, `{"reason":"line\nbreak"}`, `{"reason":"<b>no</b>"}`,
		`{"reason":"` + strings.Repeat("a", 501) + `"}`, `{"reason":"` + strings.Repeat("中", 167) + `"}`,
		`{"reason":"` + strings.Repeat("a", 5000) + `"}`, string([]byte{'{', '"', 'r', 'e', 'a', 's', 'o', 'n', '"', ':', '"', 0xff, '"', '}'}),
	} {
		if status, out := cancellationRawRequest(t, c, path, raw, "", ""); status != 400 {
			t.Fatalf("invalid body %q: %d %v", raw[:min(len(raw), 80)], status, out)
		}
	}
	if status, out := c.request("POST", "/api/v1/orders/not-an-id/cancel", map[string]string{"reason": "No longer needed"}, ""); status != 400 {
		t.Fatal("invalid order ID", status, out)
	}
	if status, out := c.request("POST", "/api/v1/orders/"+ID()+"/cancel", map[string]string{"reason": "No longer needed"}, ""); status != 404 {
		t.Fatal("missing order", status, out)
	}
	if len(m.audits) != 1 || len(m.orderEvents) != 1 || m.orders[0].Status != "pending_payment" || m.invoices[0].Status != "unpaid" {
		t.Fatal("invalid input changed data")
	}
	m.mu.Lock()
	m.invoices[0].Status = "paid"
	m.mu.Unlock()
	status, out := c.request("POST", path, map[string]string{"reason": "No longer needed"}, "")
	if status != 409 || out["error"].(map[string]any)["code"] != "CANCELLATION_STATE_CONFLICT" || m.invoices[0].ID != invoice.ID || m.orders[0].Status != "pending_payment" {
		t.Fatal("paid pair cancellation", status, out)
	}
}

type cancellationCallStore struct {
	Store
	calls int
}

func (s *cancellationCallStore) CancelOrder(ctx context.Context, actor User, id, reason string) (OrderCancellation, error) {
	s.calls++
	return s.Store.CancelOrder(ctx, actor, id, reason)
}

func TestCancellationHTTPRefusesConnectedPaymentWithoutAdapterCall(t *testing.T) {
	recorder := &checkoutRecorder{}
	registry, err := integrations.New(t.Context(), []integrations.Definition{{Manifest: integrations.Manifest{
		ID: "payment.test", Kind: integrations.Payment, Name: "Test", Description: "Local test only", Capabilities: []string{"checkout"}, SupportedCurrencies: []string{"CNY"}, CurrencyDecimalPlaces: map[string]int{"CNY": 2},
	}, CheckoutHosts: []string{"checkout.example.com"}, PaymentFactory: func(context.Context, integrations.Config) (integrations.PaymentProvider, error) { return recorder, nil }}}, integrations.Options{PaymentID: "payment.test"})
	if err != nil {
		t.Fatal(err)
	}
	m := NewMemory()
	owner, _, _ := cancellationUsers(t, m)
	o, _, _ := cancellationOrder(t, m, owner)
	store := &cancellationCallStore{Store: m}
	srv := httptest.NewServer(NewAppWithIntegrations(store, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()}, registry).Handler())
	defer srv.Close()
	c := client(t, srv.URL, "demo@aster.local", "DemoCustomer!2026")
	status, out := c.request("POST", "/api/v1/orders/"+o.ID+"/cancel", map[string]string{"reason": "No longer needed"}, "")
	if status != 409 || out["error"].(map[string]any)["code"] != "CANCELLATION_UNAVAILABLE" || store.calls != 0 {
		t.Fatal("connected adapter reached cancellation store", status, out, store.calls)
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.requests) != 0 || len(m.audits) != 1 || len(m.orderEvents) != 1 || m.orders[0].Status != "pending_payment" {
		t.Fatal("payment gate called adapter or changed history")
	}
	if cancellationPaymentsDisabled(nil) || !cancellationPaymentsDisabled(integrations.Default()) {
		t.Fatal("disabled payment metadata guard")
	}
}
