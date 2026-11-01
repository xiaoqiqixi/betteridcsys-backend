package main

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"aster/billing/pkg/pluginserver"
	bank "aster/billing/plugins/payment.bank-transfer"
	"os"
)

func main() {
	err := pluginserver.RunPayment(pluginserver.PaymentDefinition{ID: bank.ID, Version: bank.Version, Declarations: bank.Declarations(), Capabilities: pluginapi.PaymentCapabilities()}, func(o pluginserver.PaymentOptions) (paymentv1.Processor, error) {
		raw, e := pluginserver.ReadPaymentCredential("payment_config.json")
		if e != nil {
			return nil, e
		}
		defer clear(raw)
		var cfg bank.Config
		if pluginapi.DecodeStrict(raw, 64<<10, &cfg) != nil || cfg.MerchantID != o.MerchantID {
			return nil, paymentv1.ErrInvalid
		}
		return bank.New(cfg)
	})
	if err != nil {
		os.Exit(1)
	}
}
