package billing

import (
	"aster/billing/internal/integrations"
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"net/http"
	"time"
)

func settlementGateway(c paymentv1.Config) string {
	if c.GatewayID != "" {
		return c.GatewayID
	}
	return c.ProviderID
}
func wireCheckout(c integrations.Checkout, cfg paymentv1.Config) paymentv1.Checkout {
	return paymentv1.Checkout{ProviderID: cfg.ProviderID, GatewayID: cfg.GatewayID, SessionID: c.SessionID, Flow: c.Flow, URL: c.URL, QRPayload: c.QRPayload, Instructions: c.Instructions, Reference: c.Reference, ExpiresAt: c.ExpiresAt}
}
func (a *App) checkoutResponse(w http.ResponseWriter, attempt PaymentAttempt, c integrations.Checkout, modern bool) {
	if !modern {
		jsonOut(w, 200, c)
		return
	}
	flow := c.Flow
	if flow == "" {
		flow = "redirect"
	}
	out := map[string]any{"status": flow, "payment_id": attempt.ID, "expires_at": c.ExpiresAt}
	if flow == "redirect" {
		out["checkout_url"] = c.URL
	} else {
		out["amount"] = map[string]any{"currency": attempt.Expectation.Currency, "amount_minor": attempt.Expectation.AmountMinor, "decimal_places": attempt.Expectation.CurrencyDecimalPlaces}
		if flow == "qr" {
			out["qr_payload"] = c.QRPayload
		} else {
			out["instructions"] = c.Instructions
			out["reference"] = c.Reference
			if c.ExpiresAt.IsZero() {
				out["expires_at"] = nil
			}
		}
	}
	jsonOut(w, 200, out)
}
func (a *App) paymentStatusHTTP(w http.ResponseWriter, r *http.Request, actor User, invoiceID, paymentID string) {
	actor.Role = "customer"
	attempt, err := a.Store.PaymentAttempt(r.Context(), actor, paymentID)
	if err != nil {
		a.paymentStoreError(w, r, err)
		return
	}
	if attempt.Expectation.InvoiceID != invoiceID {
		a.paymentStoreError(w, r, ErrNotFound)
		return
	}
	status := "pending"
	switch attempt.State {
	case "settled":
		status = "succeeded"
	case "expired", "voided":
		status = attempt.State
	}
	if runtime, ok := a.Config.PaymentRuntime.(GatewayRuntime); ok && attempt.Checkout != nil && attempt.State != "settled" {
		m, cfg, e := a.paymentSelectionFor(attempt.Expectation.ProviderID)
		if e == nil && cfg.MerchantID == attempt.Expectation.MerchantAccountID && m.HasCapability(pluginapi.PaymentQueryCapability) {
			want := attempt.Expectation
			query := paymentv1.Query{SessionID: attempt.Checkout.SessionID, Intent: paymentv1.Intent{AttemptID: attempt.ID, IdempotencyKey: attempt.ID, InvoiceID: want.InvoiceID, OrderID: want.OrderID, AccountID: want.AccountID, AmountMinor: want.AmountMinor, Currency: want.Currency, CurrencyDecimalPlaces: want.CurrencyDecimalPlaces}}
			out, e := runtime.QueryPayment(r.Context(), cfg, query)
			if e == nil && paymentv1.ValidateQueryResult(out, cfg, query, *m.Payment, time.Now()) == nil {
				if out.Event != nil {
					v := out.Event
					_, e = a.Store.SettlePayment(r.Context(), attempt.ID, integrations.VerifiedPayment{AttemptID: v.AttemptID, ProviderID: settlementGateway(cfg), EventID: v.EventID, TransactionID: v.TransactionID, MerchantAccountID: v.MerchantAccountID, AccountID: v.AccountID, OrderID: v.OrderID, InvoiceID: v.InvoiceID, AmountMinor: v.AmountMinor, Currency: v.Currency, CurrencyDecimalPlaces: v.CurrencyDecimalPlaces, SettledAt: v.SettledAt, Status: v.Status})
					if e != nil {
						a.paymentStoreError(w, r, e)
						return
					}
					status = "succeeded"
				} else if status == "pending" {
					status = out.Status
				}
			}
		}
	}
	invoice, err := a.Store.Invoice(r.Context(), actor.ID, invoiceID)
	if err != nil {
		a.paymentStoreError(w, r, err)
		return
	}
	invoiceStatus := invoice.Status
	if invoiceStatus == "void" {
		invoiceStatus = "cancelled"
	}
	// Offline instructions are confirmed by the existing staff payment action.
	if invoiceStatus == "paid" && attempt.Checkout != nil && attempt.Checkout.Flow == "instructions" {
		status = "succeeded"
	}
	jsonOut(w, 200, map[string]string{"payment_id": attempt.ID, "status": status, "invoice_status": invoiceStatus})
}
