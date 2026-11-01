package billing

import (
	"aster/billing/internal/contracttest"
	"aster/billing/pkg/pluginapi/paymentv1"
	banktransfer "aster/billing/plugins/payment.bank-transfer"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

type bankSlotRuntime struct {
	*gatewayTestRuntime
	processors map[string]*banktransfer.Processor
}

func (g *bankSlotRuntime) CreatePayment(ctx context.Context, cfg paymentv1.Config, in paymentv1.Intent) (paymentv1.Checkout, error) {
	return g.processors[cfg.GatewayID].Create(ctx, cfg, in)
}
func (g *bankSlotRuntime) VerifyPaymentCallback(ctx context.Context, cfg paymentv1.Config, in paymentv1.Callback) (paymentv1.VerifiedEvent, error) {
	return g.processors[cfg.GatewayID].Verify(ctx, cfg, in)
}

func TestMySQLGatewayBankTransferManualConfirmation(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	g := &bankSlotRuntime{gatewayTestRuntime: &gatewayTestRuntime{slots: map[string]*paymentHTTPRuntime{}}, processors: map[string]*banktransfer.Processor{}}
	for _, id := range []string{"bank-a", "bank-b"} {
		p := newPaymentHTTPRuntime(t)
		p.manifest = banktransfer.Manifest(strings.Repeat("a", 64))
		p.selection.ID, p.selection.Version, p.selection.Slot, p.selection.MerchantID = banktransfer.ID, p.manifest.Version, id, "merchant-"+id
		g.slots[id] = p
		processor, err := banktransfer.New(banktransfer.Config{MerchantID: p.selection.MerchantID, Instructions: "Transfer to receiving account " + id})
		if err != nil {
			t.Fatal(err)
		}
		g.processors[id] = processor
	}
	srv := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), CommerceContractVersion: "0.6", PaymentRuntime: g}).Handler())
	defer srv.Close()
	admin := client(t, srv.URL, f.admin.Email, "CatalogFixture!2026")
	customer := client(t, srv.URL, f.customer.Email, "CatalogFixture!2026")
	status, out := customer.request("GET", "/api/v1/payment-gateways?currency=CNY", nil, "")
	if status != 200 || len(out["data"].([]any)) != 2 {
		t.Fatal("two bank slots unavailable", status, out)
	}
	contracttest.Response(t, "GET", "/payment-gateways", out)
	for _, id := range []string{"bank-a", "bank-b"} {
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
			if checkout["status"] != "instructions" || checkout["instructions"] != "Transfer to receiving account "+id || checkout["reference"] != pid || checkout["amount"].(map[string]any)["amount_minor"] != float64(600) {
				t.Fatal("wrong slot or remaining balance", checkout)
			}
			status, _ = paymentHTTPNotify(t, &paymentHTTPHarness{server: srv}, "POST", "/api/v1/payment-callbacks/"+id, []byte(`{"status":"paid"}`), "forged", "")
			if status != 400 {
				t.Fatal("bank callback accepted", status)
			}
			confirmation := invoice06Payment(600, pid)
			if status, _ := customer.request("POST", manual, confirmation, "confirm-"+id); status != 403 {
				t.Fatal("customer confirmed a bank receipt", status)
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
