// Package paymentfixture is a synthetic payment adapter for contract tests and
// developer examples. It never contacts a payment platform or moves money.
// Its signatures prove only possession of a local fixture key.
package paymentfixture

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"
	"time"
)

const (
	ProviderID      = "payment.fixture"
	CheckoutHost    = "checkout.example.test"
	SignatureHeader = "X-Fixture-Signature"
)

// Processor stores a private copy of the key. No credential is accepted in the
// runtime protocol; an independently configured worker owns its fixture key.
type Processor struct {
	secret []byte
	now    func() time.Time
}

func New(secret []byte) (*Processor, error) {
	if len(secret) < 32 || len(secret) > 4096 {
		return nil, paymentv1.ErrInvalid
	}
	return &Processor{secret: append([]byte(nil), secret...), now: time.Now}, nil
}

// Declarations returns fresh metadata so callers cannot mutate worker policy.
func Declarations() paymentv1.Declarations {
	return paymentv1.Declarations{
		Currencies:    []paymentv1.Currency{{Code: "CNY", Precision: 2}, {Code: "USD", Precision: 2}, {Code: "JPY", Precision: 0}},
		CheckoutHosts: []string{CheckoutHost},
	}
}

// Create returns a deterministic synthetic session on the reserved .test domain.
// This URL is deliberately unusable for real checkout and is never fetched.
func (p *Processor) Create(ctx context.Context, cfg paymentv1.Config, intent paymentv1.Intent) (paymentv1.Checkout, error) {
	if err := p.ready(ctx); err != nil {
		return paymentv1.Checkout{}, err
	}
	if paymentv1.ValidateConfig(cfg) != nil || cfg.ProviderID != ProviderID || paymentv1.ValidateIntent(intent, Declarations()) != nil {
		return paymentv1.Checkout{}, paymentv1.ErrInvalid
	}
	// Hash the whole intent so no billing details or return URLs are exposed in
	// the example checkout URL. The same authorized intent yields one session.
	raw, err := json.Marshal(struct {
		Config paymentv1.Config `json:"config"`
		Intent paymentv1.Intent `json:"intent"`
	}{cfg, intent})
	if err != nil {
		return paymentv1.Checkout{}, paymentv1.ErrInvalid
	}
	digest := sha256.Sum256(raw)
	session := "fixture_" + hex.EncodeToString(digest[:])
	query := url.Values{"session": {session}}
	return paymentv1.Checkout{
		ProviderID: ProviderID,
		SessionID:  session,
		URL:        "https://" + CheckoutHost + "/checkout?" + query.Encode(),
		ExpiresAt:  p.now().UTC().Add(15 * time.Minute),
	}, nil
}

// Verify authenticates the raw synthetic JSON body before decoding. The core
// retains responsibility for durable replay deduplication and all accounting.
// Re-delivery of the same valid event remains verifiable within five minutes.
func (p *Processor) Verify(ctx context.Context, cfg paymentv1.Config, callback paymentv1.Callback) (paymentv1.VerifiedEvent, error) {
	if err := p.ready(ctx); err != nil {
		return paymentv1.VerifiedEvent{}, err
	}
	if paymentv1.ValidateConfig(cfg) != nil || cfg.ProviderID != ProviderID || paymentv1.ValidateCallback(callback) != nil ||
		callback.RawQuery != "" || len(callback.Body) > 32<<10 {
		return paymentv1.VerifiedEvent{}, paymentv1.ErrRejected
	}
	var signature string
	for _, header := range callback.Headers {
		if strings.EqualFold(header.Name, SignatureHeader) {
			if signature != "" || len(header.Values) != 1 || len(header.Values[0]) != sha256.Size*2 {
				return paymentv1.VerifiedEvent{}, paymentv1.ErrRejected
			}
			signature = header.Values[0]
		}
	}
	provided, err := hex.DecodeString(signature)
	if err != nil || len(provided) != sha256.Size {
		return paymentv1.VerifiedEvent{}, paymentv1.ErrRejected
	}
	mac := hmac.New(sha256.New, p.secret)
	_, _ = mac.Write(callback.Body)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return paymentv1.VerifiedEvent{}, paymentv1.ErrRejected
	}
	var event paymentv1.VerifiedEvent
	now := p.now().UTC()
	if pluginapi.DecodeStrict(callback.Body, 32<<10, &event) != nil ||
		paymentv1.ValidateEvent(event, ProviderID, cfg.MerchantID, Declarations(), now) != nil ||
		event.SettledAt.Before(now.Add(-5*time.Minute)) || event.SettledAt.After(now.Add(30*time.Second)) {
		return paymentv1.VerifiedEvent{}, paymentv1.ErrRejected
	}
	return event, nil
}

func (p *Processor) ready(ctx context.Context) error {
	if p == nil || len(p.secret) < 32 || len(p.secret) > 4096 || p.now == nil || ctx == nil {
		return paymentv1.ErrUnavailable
	}
	return ctx.Err()
}

// Signature computes the fixture-only header value for local test payloads.
// The body must be signed after its final encoding; whitespace changes matter.
func Signature(secret, body []byte) (string, error) {
	if len(secret) < 32 || len(secret) > 4096 || len(body) == 0 || len(body) > 32<<10 {
		return "", paymentv1.ErrInvalid
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

var _ paymentv1.Processor = (*Processor)(nil)
