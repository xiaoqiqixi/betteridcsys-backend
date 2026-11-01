package main

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"aster/billing/pkg/pluginserver"
	paymenttemplate "aster/billing/plugins/payment-template"
	"os"
)

func main() {
	if pluginserver.RunPayment(pluginserver.PaymentDefinition{ID: paymenttemplate.ID, Version: "1.0.0", Declarations: paymenttemplate.Declarations(), Capabilities: pluginapi.PaymentCapabilities()}, func(pluginserver.PaymentOptions) (paymentv1.Processor, error) {
		return paymenttemplate.Processor{}, nil
	}) != nil {
		os.Exit(1)
	}
}
