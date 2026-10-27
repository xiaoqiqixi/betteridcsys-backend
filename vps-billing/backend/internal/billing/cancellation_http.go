package billing

import (
	"aster/billing/internal/integrations"
	"errors"
	"net/http"
	"strings"
)

func cancellationPath(path string, admin bool) (string, bool) {
	prefix := "/orders/"
	if admin {
		prefix = "/admin/orders/"
	}
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	return parts[0], len(parts) == 2 && parts[1] == "cancel"
}

// This is a core policy check over immutable registry metadata. It never invokes
// an adapter: unpaid local flags cannot prove absence of in-flight settlement.
func cancellationPaymentsDisabled(registry *integrations.Registry) bool {
	if registry == nil {
		return false
	}
	disabled := false
	for _, s := range registry.Summaries() {
		if s.Kind != integrations.Payment {
			continue
		}
		if s.Enabled {
			return false
		}
		if s.ID == "payment.disabled" {
			disabled = true
		}
	}
	return disabled
}

func (a *App) cancelOrderHTTP(w http.ResponseWriter, r *http.Request, actor User, orderID string) {
	if r.Method != "POST" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "不支持此请求方式")
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if !financeBody(w, r, &input) {
		return
	}
	if _, err := normalizeCancellationInput(actor, orderID, input.Reason); err != nil {
		a.cancellationError(w, r, err)
		return
	}
	if !cancellationPaymentsDisabled(a.Integrations) {
		a.cancellationError(w, r, ErrCancellationUnavailable)
		return
	}
	out, err := a.Store.CancelOrder(r.Context(), actor, orderID, input.Reason)
	if err != nil {
		a.cancellationError(w, r, err)
		return
	}
	jsonOut(w, 200, out)
}

func (a *App) cancellationError(w http.ResponseWriter, r *http.Request, err error) {
	code, message, status := "", "", 0
	switch {
	case errors.Is(err, ErrNotFound):
		code, message, status = "NOT_FOUND", "未找到此订单", 404
	case errors.Is(err, ErrCancellationInput):
		code, message, status = "INVALID_CANCELLATION", "请填写 1–500 字节的纯文本取消原因", 400
	case errors.Is(err, ErrCancellationState):
		code, message, status = "CANCELLATION_STATE_CONFLICT", "当前订单、账单或处理记录无法安全取消，请重新读取并核对", 409
	case errors.Is(err, ErrCancellationUnavailable):
		code, message, status = "CANCELLATION_UNAVAILABLE", "已接入支付服务，无法直接取消；请先核对付款处理状态", 409
	default:
		a.internal(w, r, err)
		return
	}
	if requestLocale(r.Header.Get("Accept-Language")) == "en" {
		message = map[string]string{
			"NOT_FOUND":                   "Order not found.",
			"INVALID_CANCELLATION":        "Enter a plain-text cancellation reason of 1–500 bytes.",
			"CANCELLATION_STATE_CONFLICT": "The order, invoice or processing history cannot be safely cancelled. Reload and review its state.",
			"CANCELLATION_UNAVAILABLE":    "Payments are connected. Local cancellation is unavailable while payment processing cannot be ruled out.",
		}[code]
	}
	fail(w, r, status, code, message)
}
