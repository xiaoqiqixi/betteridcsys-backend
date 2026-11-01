package runtimeplugins

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"context"
	"time"
)

func (m *Manager) paymentKind() string {
	if m.paymentSlot != "" {
		return "payment:" + m.paymentSlot
	}
	return "payment"
}
func (m *Manager) gateway(id string) *Manager {
	if m == nil || !pluginapi.ValidGatewayID(id) {
		return nil
	}
	selections, err := m.paymentSelections()
	if err != nil {
		return nil
	}
	found := false
	for _, s := range selections {
		if s.Slot == id {
			found = true
		}
	}
	if !found {
		return nil
	}
	m.paymentMu.Lock()
	defer m.paymentMu.Unlock()
	if m.gateways == nil {
		m.gateways = map[string]*Manager{}
	}
	if child := m.gateways[id]; child != nil {
		return child
	}
	// Bound untrusted lookup cardinality; policy holds at most 64 slots.
	if len(m.gateways) >= 64 {
		for key := range m.gateways {
			present := false
			for _, s := range selections {
				if key == s.Slot {
					present = true
				}
			}
			if !present {
				delete(m.gateways, key)
			}
		}
	}
	child := &Manager{root: m.root, policy: m.policy, rules: m.rules, paymentSlot: id}
	m.gateways[id] = child
	return child
}
func (m *Manager) PaymentGatewaySelection(id string) (pluginapi.Manifest, pluginapi.Selection, error) {
	if selections, err := m.paymentSelections(); err == nil {
		for _, s := range selections {
			if s.Slot == "" && s.ID == id {
				return m.PaymentSelection()
			}
		}
	}
	child := m.gateway(id)
	if child == nil {
		return pluginapi.Manifest{}, pluginapi.Selection{}, ErrDisabled
	}
	return child.PaymentSelection()
}
func (m *Manager) QueryPayment(ctx context.Context, cfg paymentv1.Config, in paymentv1.Query) (paymentv1.QueryResult, error) {
	if cfg.GatewayID != "" && m != nil && m.paymentSlot == "" {
		return m.gateway(cfg.GatewayID).QueryPayment(ctx, cfg, in)
	}
	var result paymentv1.QueryResult
	err := m.paymentOperation(ctx, cfg, pluginapi.PaymentQueryCapability, func(a *Approval, c *connection, ctx context.Context, timeout int64) error {
		if paymentv1.ValidateQuery(in, *a.Manifest.Payment) != nil {
			return paymentv1.ErrInvalid
		}
		var out pluginapi.PaymentQueryResponse
		err := exchange(ctx, c.client, "/v1/payment/query", pluginapi.PaymentQueryRequest{Protocol: pluginapi.PaymentProtocol, Version: a.Manifest.Version, RequestID: c.nonce, TimeoutMS: timeout, Config: cfg, Query: in}, &out)
		if err != nil || out.Protocol != pluginapi.PaymentProtocol || out.RequestID != c.nonce || out.Status != "accepted" || out.Code != "" || out.Result == nil || paymentv1.ValidateQueryResult(*out.Result, cfg, in, *a.Manifest.Payment, time.Now()) != nil {
			return paymentv1.ErrUnavailable
		}
		result = *out.Result
		return nil
	})
	return result, err
}
func (m *Manager) RefundPayment(ctx context.Context, cfg paymentv1.Config, in paymentv1.Refund) (paymentv1.RefundResult, error) {
	if cfg.GatewayID != "" && m != nil && m.paymentSlot == "" {
		return m.gateway(cfg.GatewayID).RefundPayment(ctx, cfg, in)
	}
	var result paymentv1.RefundResult
	err := m.paymentOperation(ctx, cfg, pluginapi.PaymentRefundCapability, func(a *Approval, c *connection, ctx context.Context, timeout int64) error {
		if paymentv1.ValidateRefund(in, *a.Manifest.Payment) != nil {
			return paymentv1.ErrInvalid
		}
		var out pluginapi.PaymentRefundResponse
		err := exchange(ctx, c.client, "/v1/payment/refund", pluginapi.PaymentRefundRequest{Protocol: pluginapi.PaymentProtocol, Version: a.Manifest.Version, RequestID: c.nonce, TimeoutMS: timeout, Config: cfg, Refund: in}, &out)
		if err != nil || out.Protocol != pluginapi.PaymentProtocol || out.RequestID != c.nonce || out.Status != "accepted" || out.Code != "" || out.Result == nil || paymentv1.ValidateRefundResult(*out.Result) != nil {
			return paymentv1.ErrUnavailable
		}
		result = *out.Result
		return nil
	})
	return result, err
}
func (m *Manager) paymentOperation(ctx context.Context, cfg paymentv1.Config, capability string, operation func(*Approval, *connection, context.Context, int64) error) error {
	if ctx == nil || ctx.Err() != nil {
		return paymentv1.ErrUnavailable
	}
	a, done, err := m.paymentStart(cfg)
	if err != nil {
		return err
	}
	defer done()
	if !a.Manifest.HasCapability(capability) {
		return paymentv1.ErrRejected
	}
	caller, key := ctx, paymentSelectionKey(a.Selection)
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	c, _, err := handshake(ctx, a, m.rules)
	if err != nil {
		m.paymentResult(key, caller, false)
		return paymentv1.ErrUnavailable
	}
	defer c.close()
	end, _ := ctx.Deadline()
	_ = c.conn.SetDeadline(end)
	if time.Until(end).Milliseconds() < 1 {
		return paymentv1.ErrUnavailable
	}
	err = operation(a, c, ctx, time.Until(end).Milliseconds())
	m.paymentResult(key, caller, err == nil)
	return err
}
