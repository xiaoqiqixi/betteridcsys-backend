// Package paymenttemplate is an offline example. Copy it and replace the hooks
// with your provider's official API and authenticated callback implementation.
package paymenttemplate

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"context"
)

const ID = "payment.template"

func Declarations() paymentv1.Declarations {
	return paymentv1.Declarations{Name: map[string]string{"en": "Example payment"}, Flow: "instructions", Currencies: []paymentv1.Currency{{Code: "USD", Precision: 2}}, CheckoutHosts: []string{}}
}
func Manifest(sha string) pluginapi.Manifest {
	d := Declarations()
	return pluginapi.Manifest{Schema: 1, ID: ID, Version: "1.0.0", Protocol: pluginapi.PaymentProtocol, CoreMin: "0.6.0", CoreMaxExclusive: "0.7.0", Capabilities: pluginapi.PaymentCapabilities(), Executable: "plugin", SHA256: sha, Payment: &d}
}

type Processor struct{}

// Create uses the core attempt as the provider idempotency key. Never invent a
// new key after a timeout. This template produces instructions without charging.
func (Processor) Create(ctx context.Context, c paymentv1.Config, i paymentv1.Intent) (paymentv1.Checkout, error) {
	if ctx.Err() != nil {
		return paymentv1.Checkout{}, paymentv1.ErrUnavailable
	}
	if c.ProviderID != ID || paymentv1.ValidateConfig(c) != nil || paymentv1.ValidateIntent(i, Declarations()) != nil {
		return paymentv1.Checkout{}, paymentv1.ErrInvalid
	}
	return paymentv1.Checkout{ProviderID: ID, GatewayID: c.GatewayID, SessionID: i.AttemptID, Flow: "instructions", Instructions: "Example only. Do not send funds.", Reference: i.AttemptID}, nil
}

// Verify rejects every callback until the author implements provider signatures,
// merchant checks, replay windows and exact amount/currency evidence.
// VerifiedEvent.Acknowledgement optionally supplies the provider's exact success
// response; Core sends it only after durable settlement, including exact retries.
func (Processor) Verify(context.Context, paymentv1.Config, paymentv1.Callback) (paymentv1.VerifiedEvent, error) {
	return paymentv1.VerifiedEvent{}, paymentv1.ErrRejected
}

// Optional paymentv1.Querier.Query and paymentv1.Refunder.Refund hooks must be
// declared in capabilities only after implementation. Query success returns a
// complete VerifiedEvent; Refund uses its own durable core idempotency key.
