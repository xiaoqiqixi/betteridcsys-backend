package banktransfer

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"context"
)

const ID = "payment.bank-transfer"
const Version = "1.0.0"

func Declarations() paymentv1.Declarations {
	return paymentv1.Declarations{Name: map[string]string{"en": "Bank transfer", "zh-CN": "银行转账"}, Description: map[string]string{"en": "Send a transfer using the reference shown. Staff confirm receipt."}, Flow: "instructions", Currencies: []paymentv1.Currency{{"USD", 2}, {"EUR", 2}, {"GBP", 2}, {"CNY", 2}, {"JPY", 0}}, CheckoutHosts: []string{}}
}
func Manifest(sha string) pluginapi.Manifest {
	d := Declarations()
	return pluginapi.Manifest{Schema: 1, ID: ID, Version: Version, Protocol: pluginapi.PaymentProtocol, CoreMin: "0.6.0", CoreMaxExclusive: "0.7.0", Capabilities: pluginapi.PaymentCapabilities(), Executable: "plugin", SHA256: sha, Payment: &d}
}

// Instructions are intentionally shown to customers. Keep API tokens and other
// secrets out of this field. Bank account coordinates are supplied by operators.
type Config struct {
	MerchantID   string `json:"merchant_id"`
	Instructions string `json:"instructions"`
}
type Processor struct{ config Config }

func New(c Config) (*Processor, error) {
	if !paymentv1.ValidID(c.MerchantID) || !paymentv1.PlainText(c.Instructions, 7800, true) {
		return nil, paymentv1.ErrInvalid
	}
	return &Processor{c}, nil
}
func (p *Processor) Create(ctx context.Context, c paymentv1.Config, i paymentv1.Intent) (paymentv1.Checkout, error) {
	if ctx.Err() != nil {
		return paymentv1.Checkout{}, paymentv1.ErrUnavailable
	}
	if c.ProviderID != ID || c.MerchantID != p.config.MerchantID || paymentv1.ValidateConfig(c) != nil || paymentv1.ValidateIntent(i, Declarations()) != nil {
		return paymentv1.Checkout{}, paymentv1.ErrInvalid
	}
	return paymentv1.Checkout{ProviderID: ID, GatewayID: c.GatewayID, SessionID: i.AttemptID, Flow: "instructions", Instructions: p.config.Instructions, Reference: i.AttemptID}, nil
}
func (p *Processor) Verify(context.Context, paymentv1.Config, paymentv1.Callback) (paymentv1.VerifiedEvent, error) {
	return paymentv1.VerifiedEvent{}, paymentv1.ErrRejected
}
