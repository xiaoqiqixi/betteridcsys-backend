package runtimeplugins

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"context"
	"encoding/json"
	"errors"
	"time"
)

// PaymentSelection reads the current approved identity and merchant without
// starting or contacting a worker. Callers pin its manifest hash in Config.
func (m *Manager) PaymentSelection() (pluginapi.Manifest, pluginapi.Selection, error) {
	if m == nil {
		return pluginapi.Manifest{}, pluginapi.Selection{}, ErrDisabled
	}
	release, e := m.acquirePaymentSlot()
	if e != nil {
		return pluginapi.Manifest{}, pluginapi.Selection{}, e
	}
	defer release()
	a, e := loadApprovalKind(m.root, m.policy, "payment", m.rules)
	if e != nil {
		return pluginapi.Manifest{}, pluginapi.Selection{}, e
	}
	defer a.Close()
	return a.Manifest, a.Selection, nil
}
func (m *Manager) PaymentManifest() (pluginapi.Manifest, error) {
	a, _, e := m.PaymentSelection()
	return a, e
}
func (m *Manager) PaymentState() State {
	s := State{Status: "unavailable", Protocol: pluginapi.PaymentProtocol}
	if m == nil || m.root == "" && m.policy == "" {
		s.Status = "not_installed"
		return s
	}
	a, e := loadApprovalKind(m.root, m.policy, "payment", m.rules)
	if errors.Is(e, ErrDisabled) {
		s.Status = "disabled"
		if a != nil {
			s.ID, s.Version, s.PolicyRevision = a.Selection.ID, a.Selection.Version, a.PolicyRevision
		}
		return s
	}
	if e != nil {
		return s
	}
	defer a.Close()
	s.Status, s.ID, s.Version, s.PolicyRevision = "approved", a.Manifest.ID, a.Manifest.Version, a.PolicyRevision
	return s
}
func (m *Manager) PaymentCheck(ctx context.Context) (pluginapi.Identity, error) {
	if m == nil || ctx == nil {
		return pluginapi.Identity{}, ErrDisabled
	}
	release, e := m.acquirePaymentSlot()
	if e != nil {
		return pluginapi.Identity{}, e
	}
	defer release()
	a, e := loadApprovalKind(m.root, m.policy, "payment", m.rules)
	if e != nil {
		return pluginapi.Identity{}, e
	}
	defer a.Close()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c, id, e := handshake(ctx, a, m.rules)
	if e != nil {
		return pluginapi.Identity{}, e
	}
	c.close()
	return id, nil
}

// Approval reads and RPC share one bounded capacity pool. Each caller releases
// its own slot before returning, so an HTTP selection never reserves a slot for
// a later RPC. This bounds concurrent hashing; filesystem reads are not canceled
// by the separate RPC deadline.
func (m *Manager) acquirePaymentSlot() (func(), error) {
	m.paymentMu.Lock()
	if m.paymentSlots == nil {
		m.paymentSlots = make(chan struct{}, 8)
	}
	slots := m.paymentSlots
	select {
	case slots <- struct{}{}:
	default:
		m.paymentMu.Unlock()
		return nil, ErrUnavailable
	}
	m.paymentMu.Unlock()
	return func() { <-slots }, nil
}

func (m *Manager) paymentStart(cfg paymentv1.Config) (*Approval, func(), error) {
	if m == nil || paymentv1.ValidateConfig(cfg) != nil {
		return nil, nil, paymentv1.ErrUnavailable
	}
	release, e := m.acquirePaymentSlot()
	if e != nil {
		return nil, nil, paymentv1.ErrUnavailable
	}
	a, e := loadApprovalKind(m.root, m.policy, "payment", m.rules)
	if e != nil {
		release()
		return nil, nil, paymentv1.ErrUnavailable
	}
	if a.Selection.MerchantID != cfg.MerchantID || a.Selection.ID != cfg.ProviderID || a.Selection.ManifestSHA256 != cfg.ManifestSHA256 {
		a.Close()
		release()
		return nil, nil, paymentv1.ErrUnavailable
	}
	m.paymentMu.Lock()
	key := paymentSelectionKey(a.Selection)
	if key != m.paymentApprovalKey {
		m.paymentApprovalKey = key
		m.paymentFailures = 0
		m.paymentBlockedUntil = time.Time{}
	}
	blocked := time.Now().Before(m.paymentBlockedUntil)
	m.paymentMu.Unlock()
	if blocked {
		release()
		a.Close()
		return nil, nil, paymentv1.ErrUnavailable
	}
	return a, func() { a.Close(); release() }, nil
}

// Every result belongs to the complete selection admitted for that call. A
// policy replacement must not inherit failures or successes from an old worker.
func paymentSelectionKey(selection pluginapi.Selection) string {
	raw, _ := json.Marshal(selection)
	return string(raw)
}

func (m *Manager) paymentResult(key string, caller context.Context, ok bool) {
	// An explicit caller cancellation does not establish worker health. Keep
	// deadline failures: the RPC and handshake limits remain fault boundaries.
	if errors.Is(caller.Err(), context.Canceled) {
		return
	}
	m.paymentMu.Lock()
	defer m.paymentMu.Unlock()
	if key != m.paymentApprovalKey {
		return
	}
	if ok {
		m.paymentFailures = 0
		m.paymentBlockedUntil = time.Time{}
		return
	}
	m.paymentFailures++
	if m.paymentFailures >= 3 {
		m.paymentBlockedUntil = time.Now().Add(5 * time.Second)
	}
}

// CreatePayment makes one bounded request. It never automatically retries an
// uncertain checkout creation; the persistent attempt ID belongs to core.
func (m *Manager) CreatePayment(ctx context.Context, cfg paymentv1.Config, in paymentv1.Intent) (paymentv1.Checkout, error) {
	if ctx == nil || ctx.Err() != nil {
		return paymentv1.Checkout{}, paymentv1.ErrUnavailable
	}
	a, done, e := m.paymentStart(cfg)
	if e != nil {
		return paymentv1.Checkout{}, e
	}
	defer done()
	if ctx.Err() != nil {
		return paymentv1.Checkout{}, paymentv1.ErrUnavailable
	}
	if paymentv1.ValidateIntent(in, *a.Manifest.Payment) != nil {
		return paymentv1.Checkout{}, paymentv1.ErrInvalid
	}
	caller, key := ctx, paymentSelectionKey(a.Selection)
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	c, _, e := handshake(ctx, a, m.rules)
	if e != nil {
		m.paymentResult(key, caller, false)
		return paymentv1.Checkout{}, paymentv1.ErrUnavailable
	}
	defer c.close()
	end, _ := ctx.Deadline()
	_ = c.conn.SetDeadline(end)
	request := pluginapi.PaymentCreateRequest{Protocol: pluginapi.PaymentProtocol, Version: a.Manifest.Version, RequestID: c.nonce, TimeoutMS: time.Until(end).Milliseconds(), Config: cfg, Intent: in}
	var out pluginapi.PaymentCreateResponse
	if request.TimeoutMS < 1 || exchange(ctx, c.client, "/v1/payment/checkout", request, &out) != nil || out.Protocol != pluginapi.PaymentProtocol || out.RequestID != c.nonce {
		m.paymentResult(key, caller, false)
		return paymentv1.Checkout{}, paymentv1.ErrUnavailable
	}
	if out.Status == "failed" && out.Checkout == nil && out.Code == "rejected" {
		m.paymentResult(key, caller, true)
		return paymentv1.Checkout{}, paymentv1.ErrRejected
	}
	if out.Status != "accepted" || out.Code != "" || out.Checkout == nil || paymentv1.ValidateCheckout(*out.Checkout, a.Manifest.ID, *a.Manifest.Payment, time.Now()) != nil {
		m.paymentResult(key, caller, false)
		return paymentv1.Checkout{}, paymentv1.ErrUnavailable
	}
	m.paymentResult(key, caller, true)
	return *out.Checkout, nil
}

// VerifyPaymentCallback passes the original callback bytes to the approved
// verifier. Its result is still subject to core-owned accounting checks.
func (m *Manager) VerifyPaymentCallback(ctx context.Context, cfg paymentv1.Config, in paymentv1.Callback) (paymentv1.VerifiedEvent, error) {
	if ctx == nil || paymentv1.ValidateCallback(in) != nil {
		return paymentv1.VerifiedEvent{}, paymentv1.ErrInvalid
	}
	if ctx.Err() != nil {
		return paymentv1.VerifiedEvent{}, paymentv1.ErrUnavailable
	}
	a, done, e := m.paymentStart(cfg)
	if e != nil {
		return paymentv1.VerifiedEvent{}, e
	}
	defer done()
	if ctx.Err() != nil {
		return paymentv1.VerifiedEvent{}, paymentv1.ErrUnavailable
	}
	caller, key := ctx, paymentSelectionKey(a.Selection)
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	c, _, e := handshake(ctx, a, m.rules)
	if e != nil {
		m.paymentResult(key, caller, false)
		return paymentv1.VerifiedEvent{}, paymentv1.ErrUnavailable
	}
	defer c.close()
	end, _ := ctx.Deadline()
	_ = c.conn.SetDeadline(end)
	request := pluginapi.PaymentVerifyRequest{Protocol: pluginapi.PaymentProtocol, Version: a.Manifest.Version, RequestID: c.nonce, TimeoutMS: time.Until(end).Milliseconds(), Config: cfg, Callback: in}
	var out pluginapi.PaymentVerifyResponse
	if request.TimeoutMS < 1 || exchange(ctx, c.client, "/v1/payment/verify", request, &out) != nil || out.Protocol != pluginapi.PaymentProtocol || out.RequestID != c.nonce {
		m.paymentResult(key, caller, false)
		return paymentv1.VerifiedEvent{}, paymentv1.ErrUnavailable
	}
	if out.Status == "failed" && out.Event == nil && out.Code == "rejected" {
		m.paymentResult(key, caller, true)
		return paymentv1.VerifiedEvent{}, paymentv1.ErrRejected
	}
	if out.Status != "verified" || out.Code != "" || out.Event == nil || paymentv1.ValidateEvent(*out.Event, a.Manifest.ID, cfg.MerchantID, *a.Manifest.Payment, time.Now()) != nil {
		m.paymentResult(key, caller, false)
		return paymentv1.VerifiedEvent{}, paymentv1.ErrUnavailable
	}
	m.paymentResult(key, caller, true)
	return *out.Event, nil
}
