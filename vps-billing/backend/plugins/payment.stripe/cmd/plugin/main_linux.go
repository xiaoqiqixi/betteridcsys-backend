package main

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"aster/billing/pkg/pluginserver"
	"aster/billing/plugins/payment.stripe"
	"os"
)

func main() {
	err := pluginserver.RunPayment(pluginserver.PaymentDefinition{ID: stripe.ID, Version: stripe.Version, Declarations: stripe.Declarations(), Capabilities: stripe.Capabilities()}, func(o pluginserver.PaymentOptions) (paymentv1.Processor, error) {
		allowed := false
		for _, t := range o.Targets {
			if t.Host == "api.stripe.com" && t.Port == 443 {
				allowed = true
			}
		}
		if !allowed {
			return nil, paymentv1.ErrInvalid
		}
		raw, e := pluginserver.ReadPaymentCredential("payment_config.json")
		if e != nil {
			return nil, e
		}
		defer clear(raw)
		var cfg stripe.Config
		if pluginapi.DecodeStrict(raw, 64<<10, &cfg) != nil || cfg.AccountID != o.MerchantID {
			return nil, paymentv1.ErrInvalid
		}
		return stripe.New(cfg)
	})
	if err != nil {
		os.Exit(1)
	}
}
