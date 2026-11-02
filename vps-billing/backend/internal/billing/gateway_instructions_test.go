package billing

import (
	"aster/billing/internal/contracttest"
	"net/http/httptest"
	"testing"
)

func TestMySQLGatewayInstructionsManualConfirmation(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	g := &gatewayFlowRuntime{gatewayTestRuntime: &gatewayTestRuntime{slots: map[string]*paymentHTTPRuntime{}}, flow: "instructions"}
	for _, id := range []string{"instructions-a", "instructions-b"} {
		p := newPaymentHTTPRuntime(t)
		p.manifest.Payment.Flow = "instructions"
		p.manifest.Payment.Name = map[string]string{"en": "Fixture instructions"}
		p.manifest.Payment.CheckoutHosts = []string{}
		p.selection.Slot, p.selection.MerchantID = id, "merchant-"+id
		g.slots[id] = p
	}
	srv := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), CommerceContractVersion: "0.6", PaymentRuntime: g}).Handler())
	defer srv.Close()
	admin := client(t, srv.URL, f.admin.Email, "CatalogFixture!2026")
	customer := client(t, srv.URL, f.customer.Email, "CatalogFixture!2026")
	status, out := customer.request("GET", "/api/v1/payment-gateways?currency=CNY", nil, "")
	if status != 200 || len(out["data"].([]any)) != 2 {
		t.Fatal("two instructions slots unavailable", status, out)
	}
	contracttest.Response(t, "GET", "/payment-gateways", out)
	for _, id := range []string{"instructions-a", "instructions-b"} {
		t.Run(id, func(t *testing.T) {
			status, out := admin.request("POST", "/api/v1/admin/invoices", invoice06Input(f.customer.ID, 1000), "create-"+id)
			if status != 200 {
				t.Fatal(status, out)
			}
			invoice := invoice06Data(t, out)
			manual := "/api/v1/admin/invoices/" + invoice.ID + "/payments"
			status, out = admin.request("POST", manual, invoice06Payment(400, "deposit-"+id), "deposit-"+id)
			if status != 200 {
				t.Fatal(status, out)
			}
			status, out = customer.request("POST", "/api/v1/invoices/"+invoice.ID+"/pay", map[string]string{"method": "gateway", "gateway_id": id}, "checkout-"+id)
			if status != 200 {
				t.Fatal(status, out)
			}
			contracttest.Response(t, "POST", "/invoices/{id}/pay", out)
			checkout := out["data"].(map[string]any)
			pid := checkout["payment_id"].(string)
			if checkout["status"] != "instructions" || checkout["instructions"] != "Fixture instructions for "+id+"\nDo not send funds." || checkout["reference"] != pid || checkout["expires_at"] != nil || checkout["amount"].(map[string]any)["amount_minor"] != float64(600) {
				t.Fatal("wrong slot or remaining balance", checkout)
			}
			status, _ = paymentHTTPNotify(t, &paymentHTTPHarness{server: srv}, "POST", "/api/v1/payment-callbacks/"+id, []byte(`{"status":"paid"}`), "forged", "")
			if status != 400 {
				t.Fatal("forged callback accepted", status)
			}
			confirmation := invoice06Payment(600, pid)
			if status, _ := customer.request("POST", manual, confirmation, "confirm-"+id); status != 403 {
				t.Fatal("customer confirmed a manual receipt", status)
			}
			for range 2 {
				status, out = admin.request("POST", manual, confirmation, "confirm-"+id)
				if status != 200 || invoice06Data(t, out).AmountPaid.AmountMinor != 1000 || invoice06Data(t, out).Status != "paid" {
					t.Fatal("manual receipt or replay", status, out)
				}
				contracttest.Response(t, "POST", "/admin/invoices/{id}/payments", out)
			}
			status, out = customer.request("GET", "/api/v1/invoices/"+invoice.ID+"/payments/"+pid, nil, "")
			if status != 200 || out["data"].(map[string]any)["invoice_status"] != "paid" || out["data"].(map[string]any)["status"] != "voided" {
				t.Fatal("manual receipt did not close the outstanding checkout", status, out)
			}
			contracttest.Response(t, "GET", "/invoices/{id}/payments/{payment_id}", out)
			var receipts int
			if err := m.DB.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM payment_settlements WHERE attempt_id=?", pid).Scan(&receipts); err != nil || receipts != 0 {
				t.Fatal("manual receipt invented provider evidence", receipts, err)
			}
		})
	}
}
