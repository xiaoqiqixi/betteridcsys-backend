package pluginapi

import "aster/billing/pkg/pluginapi/paymentv1"

type PaymentCreateRequest struct {
	Protocol  string           `json:"protocol"`
	Version   string           `json:"version"`
	RequestID string           `json:"request_id"`
	TimeoutMS int64            `json:"timeout_ms"`
	Config    paymentv1.Config `json:"config"`
	Intent    paymentv1.Intent `json:"intent"`
}
type PaymentVerifyRequest struct {
	Protocol  string             `json:"protocol"`
	Version   string             `json:"version"`
	RequestID string             `json:"request_id"`
	TimeoutMS int64              `json:"timeout_ms"`
	Config    paymentv1.Config   `json:"config"`
	Callback  paymentv1.Callback `json:"callback"`
}

// Failed responses never carry partial payment evidence. A create failure may
// be uncertain and must never trigger an automatic core retry.
type PaymentCreateResponse struct {
	Protocol  string              `json:"protocol"`
	RequestID string              `json:"request_id"`
	Status    string              `json:"status"`
	Code      string              `json:"code"`
	Checkout  *paymentv1.Checkout `json:"checkout,omitempty"`
}
type PaymentVerifyResponse struct {
	Protocol  string                   `json:"protocol"`
	RequestID string                   `json:"request_id"`
	Status    string                   `json:"status"`
	Code      string                   `json:"code"`
	Event     *paymentv1.VerifiedEvent `json:"event,omitempty"`
}
