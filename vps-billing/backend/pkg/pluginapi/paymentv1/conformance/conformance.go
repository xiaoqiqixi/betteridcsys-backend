// Package conformance runs an offline Processor contract against an author's
// fixtures. Supply a fake provider transport; these tests never need live keys.
package conformance

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"context"
	"testing"
	"time"
)

type Suite struct {
	Manifest  pluginapi.Manifest
	Config    paymentv1.Config
	Processor paymentv1.Processor
	Intent    paymentv1.Intent
	// Offline methods omit Callback. Online methods provide a signed success
	// fixture; a one-byte mutation must always be rejected by Verify.
	Callback *paymentv1.Callback
	Refund   *paymentv1.Refund
}

func Test(t *testing.T, s Suite) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if s.Manifest.Protocol != pluginapi.PaymentProtocol || s.Manifest.Payment == nil || s.Processor == nil || s.Manifest.Validate(pluginapi.CoreVersion) != nil || paymentv1.ValidateConfig(s.Config) != nil || paymentv1.ValidateIntent(s.Intent, *s.Manifest.Payment) != nil {
		t.Fatal("invalid conformance fixtures")
	}
	out, err := s.Processor.Create(ctx, s.Config, s.Intent)
	if err != nil || out.GatewayID != s.Config.GatewayID || paymentv1.ValidateCheckout(out, s.Config.ProviderID, *s.Manifest.Payment, time.Now()) != nil {
		t.Fatal("checkout contract", err)
	}
	again, err := s.Processor.Create(ctx, s.Config, s.Intent)
	if err != nil || again.SessionID != out.SessionID {
		t.Fatal("checkout idempotency", err)
	}
	if s.Callback != nil {
		e, err := s.Processor.Verify(ctx, s.Config, *s.Callback)
		query := paymentv1.Query{Intent: s.Intent, SessionID: out.SessionID}
		if err != nil || paymentv1.ValidateQueryResult(paymentv1.QueryResult{Status: "succeeded", Event: &e}, s.Config, query, *s.Manifest.Payment, time.Now()) != nil {
			t.Fatal("verified event contract", err)
		}
		bad := *s.Callback
		bad.Body = append(append([]byte{}, bad.Body...), byte(' '))
		if _, err = s.Processor.Verify(ctx, s.Config, bad); err == nil {
			t.Fatal("mutated raw body verified")
		}
	} else if _, err := s.Processor.Verify(ctx, s.Config, paymentv1.Callback{Method: "POST", Path: "/callback", Body: []byte(`{}`)}); err == nil {
		t.Fatal("unsigned callback verified")
	}
	if s.Manifest.HasCapability(pluginapi.PaymentQueryCapability) {
		p, ok := s.Processor.(paymentv1.Querier)
		if !ok {
			t.Fatal("query capability lacks hook")
		}
		q := paymentv1.Query{Intent: s.Intent, SessionID: out.SessionID}
		result, err := p.Query(ctx, s.Config, q)
		if err != nil || paymentv1.ValidateQueryResult(result, s.Config, q, *s.Manifest.Payment, time.Now()) != nil {
			t.Fatal("query contract", err)
		}
	}
	if s.Manifest.HasCapability(pluginapi.PaymentRefundCapability) {
		p, ok := s.Processor.(paymentv1.Refunder)
		if !ok || s.Refund == nil || paymentv1.ValidateRefund(*s.Refund, *s.Manifest.Payment) != nil {
			t.Fatal("refund capability lacks hook/fixture")
		}
		result, err := p.Refund(ctx, s.Config, *s.Refund)
		if err != nil || paymentv1.ValidateRefundResult(result) != nil {
			t.Fatal("refund contract", err)
		}
		again, err := p.Refund(ctx, s.Config, *s.Refund)
		if err != nil || paymentv1.ValidateRefundResult(again) != nil || again.RefundID != result.RefundID {
			t.Fatal("refund idempotency", err)
		}
	}
}
