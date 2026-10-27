package billing

import (
	"aster/billing/internal/integrations"
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"context"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// PaymentRuntime contains no Store or credential access. The selected independent
// worker can offer checkout or verified evidence; only core storage can settle.
type PaymentRuntime interface {
	PaymentSelection() (pluginapi.Manifest, pluginapi.Selection, error)
	CreatePayment(context.Context, paymentv1.Config, paymentv1.Intent) (paymentv1.Checkout, error)
	VerifyPaymentCallback(context.Context, paymentv1.Config, paymentv1.Callback) (paymentv1.VerifiedEvent, error)
}

func (a *App) paymentSelection() (pluginapi.Manifest, paymentv1.Config, error) {
	if a.Config.PaymentRuntime == nil {
		return pluginapi.Manifest{}, paymentv1.Config{}, paymentv1.ErrUnavailable
	}
	m, s, err := a.Config.PaymentRuntime.PaymentSelection()
	if err != nil || !s.Enabled || m.Payment == nil || m.Validate(pluginapi.CoreVersion) != nil || s.ID != m.ID || s.Version != m.Version || !pluginapi.ValidDigest(s.ManifestSHA256) {
		return pluginapi.Manifest{}, paymentv1.Config{}, paymentv1.ErrUnavailable
	}
	c := paymentv1.Config{MerchantID: s.MerchantID, ProviderID: m.ID, ManifestSHA256: s.ManifestSHA256}
	if paymentv1.ValidateConfig(c) != nil {
		return pluginapi.Manifest{}, paymentv1.Config{}, paymentv1.ErrUnavailable
	}
	return m, c, nil
}

func (a *App) checkout(w http.ResponseWriter, r *http.Request, actor User, invoice Invoice) {
	if invoice.Status != "unpaid" || invoice.PaidAt != nil || !validAmount(invoice.AmountMinor) || !validCurrencyFormat(invoice.CurrencyFormat) || invoice.CurrencyFormat.Code != invoice.Currency {
		fail(w, r, 409, "INVOICE_NOT_PAYABLE", "当前账单不可支付")
		return
	}
	manifest, cfg, err := a.paymentSelection()
	if err != nil {
		fail(w, r, 503, "PAYMENT_NOT_CONFIGURED", "支付渠道当前不可用，已有付款请核对原渠道")
		return
	}
	if !manifest.Payment.Supports(invoice.Currency, invoice.CurrencyFormat.DecimalPlaces) {
		fail(w, r, 409, "PAYMENT_CURRENCY_UNSUPPORTED", "当前支付渠道不支持此账单币种")
		return
	}
	// Reserve re-reads and locks the canonical owner/order/invoice and currency
	// registry. No external operation precedes this durable cancellation barrier.
	// The customer checkout path keeps ownership even for an administrator.
	actor.Role = "customer"
	attempt, replay, err := a.Store.ReservePayment(r.Context(), actor, invoice.ID, "invoice:"+invoice.ID, cfg.ProviderID, cfg.MerchantID)
	if err != nil {
		a.paymentStoreError(w, r, err)
		return
	}
	if replay {
		if attempt.State == "checkout" && attempt.Checkout != nil && attempt.Checkout.ExpiresAt.After(time.Now()) {
			out := paymentv1.Checkout{ProviderID: attempt.Checkout.ProviderID, SessionID: attempt.Checkout.SessionID, URL: attempt.Checkout.URL, ExpiresAt: attempt.Checkout.ExpiresAt}
			if paymentv1.ValidateCheckout(out, cfg.ProviderID, *manifest.Payment, time.Now()) == nil {
				jsonOut(w, 200, attempt.Checkout)
				return
			}
		}
		fail(w, r, 409, "PAYMENT_PENDING", "此账单已有支付尝试，请核对原渠道结果，勿重复创建付款")
		return
	}
	want := attempt.Expectation
	callbackURL := ""
	if !a.Config.Demo {
		callbackURL = a.Config.Origin + "/api/v1/payment-callbacks/" + cfg.ProviderID
	}
	out, callErr := a.Config.PaymentRuntime.CreatePayment(r.Context(), cfg, paymentv1.Intent{
		AttemptID: attempt.ID, IdempotencyKey: attempt.ID, InvoiceID: want.InvoiceID, OrderID: want.OrderID, AccountID: want.AccountID,
		AmountMinor: want.AmountMinor, Currency: want.Currency, CurrencyDecimalPlaces: want.CurrencyDecimalPlaces, CallbackURL: callbackURL,
	})
	if callErr == nil {
		callErr = paymentv1.ValidateCheckout(out, cfg.ProviderID, *manifest.Payment, time.Now())
	}
	checkout := integrations.Checkout{ProviderID: out.ProviderID, SessionID: out.SessionID, URL: out.URL, ExpiresAt: out.ExpiresAt}
	// A lost browser connection must not erase an uncertain external operation.
	// The pending record itself remains a durable barrier even if this write fails.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Second)
	defer cancel()
	saved, saveErr := a.Store.CompletePaymentCheckout(finishCtx, attempt.ID, checkout, callErr != nil)
	if saveErr != nil {
		a.paymentStoreError(w, r, saveErr)
		return
	}
	if saved.State == "settled" {
		fail(w, r, 409, "INVOICE_NOT_PAYABLE", "账单已完成付款，请刷新查看")
		return
	}
	if callErr != nil {
		fail(w, r, 502, "PAYMENT_PROVIDER_ERROR", "支付会话结果未确认，请核对渠道结果，勿重复付款")
		return
	}
	jsonOut(w, 200, checkout)
}

func (a *App) paymentCallbackHTTP(w http.ResponseWriter, r *http.Request, providerID string) {
	if r.Method != "POST" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "不支持此请求方式")
		return
	}
	manifest, cfg, err := a.paymentSelection()
	if err != nil || providerID != cfg.ProviderID || strings.Contains(providerID, "/") {
		fail(w, r, 503, "PAYMENT_NOT_CONFIGURED", "支付回调当前不可用")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, paymentv1.MaxBodyBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		fail(w, r, 413, "PAYMENT_CALLBACK_INVALID", "支付通知无效或超出大小限制")
		return
	}
	headers := make([]paymentv1.Header, 0, len(r.Header))
	for name, values := range r.Header {
		// Browser/session credentials have no role in channel verification.
		if strings.EqualFold(name, "Cookie") || strings.EqualFold(name, "X-CSRF-Token") {
			continue
		}
		headers = append(headers, paymentv1.Header{Name: name, Values: append([]string{}, values...)})
	}
	sort.Slice(headers, func(i, j int) bool { return headers[i].Name < headers[j].Name })
	in := paymentv1.Callback{Method: r.Method, Path: r.URL.EscapedPath(), RawQuery: r.URL.RawQuery, Headers: headers, Body: raw}
	if paymentv1.ValidateCallback(in) != nil {
		fail(w, r, 400, "PAYMENT_CALLBACK_INVALID", "支付通知格式无效")
		return
	}
	verified, err := a.Config.PaymentRuntime.VerifyPaymentCallback(r.Context(), cfg, in)
	if errors.Is(err, paymentv1.ErrUnavailable) {
		fail(w, r, 503, "PAYMENT_PROVIDER_ERROR", "支付渠道暂不可用，请稍后重试通知")
		return
	}
	if err != nil || paymentv1.ValidateEvent(verified, cfg.ProviderID, cfg.MerchantID, *manifest.Payment, time.Now()) != nil {
		fail(w, r, 400, "PAYMENT_CALLBACK_INVALID", "支付通知验证失败")
		return
	}
	_, err = a.Store.SettlePayment(r.Context(), verified.AttemptID, integrations.VerifiedPayment{
		AttemptID: verified.AttemptID, ProviderID: verified.ProviderID, EventID: verified.EventID, TransactionID: verified.TransactionID, MerchantAccountID: verified.MerchantAccountID,
		AccountID: verified.AccountID, OrderID: verified.OrderID, InvoiceID: verified.InvoiceID, AmountMinor: verified.AmountMinor, Currency: verified.Currency, CurrencyDecimalPlaces: verified.CurrencyDecimalPlaces, SettledAt: verified.SettledAt, Status: verified.Status,
	})
	if err != nil {
		a.paymentStoreError(w, r, err)
		return
	}
	jsonOut(w, 200, map[string]bool{"received": true})
}

func (a *App) paymentStoreError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		fail(w, r, 404, "NOT_FOUND", "未找到此支付记录")
	case errors.Is(err, ErrPaymentConflict):
		fail(w, r, 409, "PAYMENT_CONFLICT", "支付通知与已保存的交易记录冲突")
	case errors.Is(err, ErrPaymentState), errors.Is(err, ErrPaymentInput), errors.Is(err, ErrCurrency):
		fail(w, r, 409, "INVOICE_NOT_PAYABLE", "账单或支付记录状态不允许此操作")
	case errors.Is(err, ErrPaymentUncertain):
		fail(w, r, 409, "PAYMENT_PENDING", "支付结果未确认，请核对原渠道记录")
	default:
		observeError(r.Context(), "PAYMENT_STORAGE_UNAVAILABLE", errorKind(err))
		fail(w, r, 503, "PAYMENT_STORAGE_UNAVAILABLE", "支付记录暂不可用，请稍后重试")
	}
}
