package runtimeplugins

import (
	"aster/billing/pkg/pluginapi"
	"strings"
	"testing"
)

func TestPaymentSlotIsolationPolicy(t *testing.T) {
	makeSelection := func(slot string, uid uint32) pluginapi.Selection {
		return pluginapi.Selection{Slot: slot, Enabled: true, ID: "payment.fixture", Version: "1.0.0", ManifestSHA256: strings.Repeat("a", 64), WorkerUID: uid, ClientUID: 1100, Socket: "/run/aster-payment/" + slot + "/plugin.sock", MerchantID: "merchant"}
	}
	a, b := makeSelection("stripe-us", 1101), makeSelection("stripe-eu", 1102)
	p := pluginapi.Policy{Schema: 1, Revision: 1, Payments: []pluginapi.Selection{a, b}}
	if err := validatePolicyKind(p, "payment:stripe-us", trust{}); err != nil {
		t.Fatal(err)
	}
	if selectionFor(p, "payment:stripe-eu").WorkerUID != b.WorkerUID || selectionFor(p, "payment") != nil {
		t.Fatal("slot selection crossed identities")
	}
	for _, mutate := range []func(*pluginapi.Selection){
		func(s *pluginapi.Selection) { s.WorkerUID = a.WorkerUID },
		func(s *pluginapi.Selection) { s.WorkerUID = a.ClientUID },
		func(s *pluginapi.Selection) { s.ClientUID = a.WorkerUID },
		func(s *pluginapi.Selection) { s.Socket = a.Socket },
		func(s *pluginapi.Selection) { s.Slot = a.Slot },
		func(s *pluginapi.Selection) { s.Slot = "../escape" },
	} {
		p.Payments[1] = b
		mutate(&p.Payments[1])
		if validatePolicyKind(p, "payment:stripe-us", trust{}) == nil {
			t.Fatal("shared or unsafe slot accepted")
		}
	}
}
