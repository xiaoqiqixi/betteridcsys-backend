package billing

import (
	"aster/billing/pkg/pluginapi/paymentv1"
	"aster/billing/plugins/paymentfixture"
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPaymentHTTPProviderAcknowledgementRequiresSettlement(t *testing.T) {
	for _, ack := range []paymentv1.CallbackAcknowledgement{
		{StatusCode: 200, ContentType: "text/plain", Body: "success"},
		{StatusCode: 200, ContentType: "application/json", Body: `{"code":"SUCCESS"}`},
		{StatusCode: 200, ContentType: "application/xml", Body: `<xml><return_code><![CDATA[SUCCESS]]></return_code></xml>`},
		{StatusCode: 204},
	} {
		t.Run(ack.ContentType, func(t *testing.T) {
			h := newPaymentHTTPHarness(t, nil)
			order, invoice := h.order(t)
			h.checkout(t, invoice)
			event := h.runtime.event(t, 0)
			event.Acknowledgement = &ack
			path := "/api/v1/payment-callbacks/" + paymentfixture.ProviderID
			wrong := event
			wrong.AmountMinor++
			raw, signature := h.runtime.signed(t, wrong)
			if status, out := paymentHTTPNotify(t, h, "POST", path, raw, signature, ""); status != 409 {
				t.Fatal("mismatched evidence emitted provider acknowledgement", status, out)
			}
			h.pair(t, order, invoice, "unpaid")
			raw, signature = h.runtime.signed(t, event)
			for range 2 {
				req, err := http.NewRequest("POST", h.server.URL+path, bytes.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set(paymentfixture.SignatureHeader, signature)
				response, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || response.StatusCode != ack.StatusCode || string(body) != ack.Body {
					t.Fatal("provider response changed", response.StatusCode, string(body), err)
				}
				if ack.ContentType != "" && response.Header.Get("Content-Type") != ack.ContentType+"; charset=utf-8" {
					t.Fatal("incorrect acknowledgement type", response.Header)
				}
				if !strings.Contains(response.Header.Get("Content-Security-Policy"), "sandbox") || response.Header.Get("X-Content-Type-Options") != "nosniff" || response.Header.Get("Content-Disposition") != "attachment" {
					t.Fatal("provider content lacks browser isolation", response.Header)
				}
				h.pair(t, order, invoice, "paid")
			}
			if len(h.memory.paymentReceipts) != 1 {
				t.Fatal("acknowledgement replay duplicated settlement")
			}
		})
	}
}
