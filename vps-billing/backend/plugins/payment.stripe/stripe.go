// Package stripe implements Stripe Checkout with direct-account credentials.
// Provider response bodies and secrets never appear in returned errors.
package stripe

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const ID = "payment.stripe"
const Version = "1.0.0"
const APIVersion = "2026-10-28.endive"

func Declarations() paymentv1.Declarations {
	return paymentv1.Declarations{Name: map[string]string{"en": "Card · Stripe", "zh-CN": "银行卡 · Stripe"}, Flow: "redirect", Currencies: []paymentv1.Currency{{"USD", 2}, {"EUR", 2}, {"GBP", 2}, {"CNY", 2}, {"JPY", 0}, {"AUD", 2}, {"CAD", 2}, {"HKD", 2}, {"SGD", 2}, {"CHF", 2}}, CheckoutHosts: []string{"checkout.stripe.com"}}
}
func Capabilities() []string {
	return append(pluginapi.PaymentCapabilities(), pluginapi.PaymentQueryCapability, pluginapi.PaymentRefundCapability)
}
func Manifest(sha string) pluginapi.Manifest {
	d := Declarations()
	return pluginapi.Manifest{Schema: 1, ID: ID, Version: Version, Protocol: pluginapi.PaymentProtocol, CoreMin: "0.6.0", CoreMaxExclusive: "0.7.0", Capabilities: Capabilities(), Executable: "plugin", SHA256: sha, Payment: &d}
}

type Config struct {
	AccountID     string `json:"account_id"`
	APIKey        string `json:"api_key"`
	WebhookSecret string `json:"webhook_secret"`
	SuccessURL    string `json:"success_url"`
	CancelURL     string `json:"cancel_url"`
	LiveMode      bool   `json:"live_mode"`
}
type Processor struct {
	config           Config
	client           *http.Client
	endpoint         string
	now              func() time.Time
	merchantVerified atomic.Bool
}

var objectID = regexp.MustCompile(`^[a-z][a-z0-9_]{1,15}_[A-Za-z0-9_]{1,100}$`)

func New(c Config) (*Processor, error) {
	if !strings.HasPrefix(c.AccountID, "acct_") || !objectID.MatchString(c.AccountID) || !strings.HasPrefix(c.WebhookSecret, "whsec_") || len(c.WebhookSecret) < 16 || len(c.APIKey) < 12 || len(c.APIKey) > 512 {
		return nil, paymentv1.ErrInvalid
	}
	mode := "test"
	if c.LiveMode {
		mode = "live"
	}
	if !strings.HasPrefix(c.APIKey, "sk_"+mode+"_") && !strings.HasPrefix(c.APIKey, "rk_"+mode+"_") {
		return nil, paymentv1.ErrInvalid
	}
	for _, raw := range []string{c.SuccessURL, c.CancelURL} {
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return nil, paymentv1.ErrInvalid
		}
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, MaxResponseHeaderBytes: 16 << 10, ResponseHeaderTimeout: 8 * time.Second}
	return &Processor{config: c, client: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, endpoint: "https://api.stripe.com", now: time.Now}, nil
}
func (p *Processor) valid(c paymentv1.Config) bool {
	return c.ProviderID == ID && c.MerchantID == p.config.AccountID && paymentv1.ValidateConfig(c) == nil && paymentv1.ValidGatewayID(c.GatewayID)
}

// An account ID in local configuration or payment metadata is not evidence of
// which merchant owns the API key. Bind the immutable worker credentials to the
// authenticated account before performing any payment operation. Cache success
// only; transient failures may be retried and credential changes restart workers.
func (p *Processor) checkMerchant(ctx context.Context) error {
	if p.merchantVerified.Load() {
		return nil
	}
	var account struct {
		ID     string `json:"id"`
		Object string `json:"object"`
	}
	if err := p.call(ctx, "GET", "/v1/account", "", nil, &account); err != nil {
		return err
	}
	if account.Object != "account" || account.ID != p.config.AccountID {
		return paymentv1.ErrRejected
	}
	p.merchantVerified.Store(true)
	return nil
}

func (p *Processor) call(ctx context.Context, method, path, key string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r, e := http.NewRequestWithContext(ctx, method, p.endpoint+path, body)
	if e != nil {
		return paymentv1.ErrInvalid
	}
	r.SetBasicAuth(p.config.APIKey, "")
	r.Header.Set("Stripe-Version", APIVersion)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	response, e := p.client.Do(r)
	if e != nil {
		return paymentv1.ErrUnavailable
	}
	defer response.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 {
		return paymentv1.ErrUnavailable
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return paymentv1.ErrUnavailable
	}
	if json.Unmarshal(raw, out) != nil {
		return paymentv1.ErrUnavailable
	}
	return nil
}
func metadata(c paymentv1.Config, i paymentv1.Intent) map[string]string {
	return map[string]string{"aster_attempt": i.AttemptID, "aster_gateway": c.GatewayID, "aster_merchant": c.MerchantID, "aster_invoice": i.InvoiceID, "aster_order": i.OrderID, "aster_account": i.AccountID, "aster_precision": strconv.Itoa(i.CurrencyDecimalPlaces)}
}

type session struct {
	ID            string            `json:"id"`
	Object        string            `json:"object"`
	Mode          string            `json:"mode"`
	URL           string            `json:"url"`
	ExpiresAt     int64             `json:"expires_at"`
	AmountTotal   int64             `json:"amount_total"`
	Currency      string            `json:"currency"`
	PaymentStatus string            `json:"payment_status"`
	Status        string            `json:"status"`
	LiveMode      bool              `json:"livemode"`
	Metadata      map[string]string `json:"metadata"`
	PaymentIntent json.RawMessage   `json:"payment_intent"`
}
type intent struct {
	Metadata       map[string]string `json:"metadata"`
	ID             string            `json:"id"`
	Status         string            `json:"status"`
	AmountReceived int64             `json:"amount_received"`
	Currency       string            `json:"currency"`
	LiveMode       bool              `json:"livemode"`
	Charge         charge            `json:"latest_charge"`
}
type charge struct {
	ID             string `json:"id"`
	PaymentIntent  string `json:"payment_intent"`
	Paid           bool   `json:"paid"`
	Captured       bool   `json:"captured"`
	Status         string `json:"status"`
	AmountCaptured int64  `json:"amount_captured"`
	Currency       string `json:"currency"`
	Created        int64  `json:"created"`
	LiveMode       bool   `json:"livemode"`
}

func (p *Processor) Create(ctx context.Context, c paymentv1.Config, i paymentv1.Intent) (paymentv1.Checkout, error) {
	if !p.valid(c) || paymentv1.ValidateIntent(i, Declarations()) != nil {
		return paymentv1.Checkout{}, paymentv1.ErrInvalid
	}
	if err := p.checkMerchant(ctx); err != nil {
		return paymentv1.Checkout{}, err
	}
	form := url.Values{"mode": {"payment"}, "success_url": {p.config.SuccessURL}, "cancel_url": {p.config.CancelURL}, "client_reference_id": {i.AttemptID}, "line_items[0][quantity]": {"1"}, "line_items[0][price_data][currency]": {strings.ToLower(i.Currency)}, "line_items[0][price_data][unit_amount]": {strconv.FormatInt(i.AmountMinor, 10)}, "line_items[0][price_data][product_data][name]": {"Invoice " + i.InvoiceID}}
	// Automatic card capture gives charge.created a stable payment timestamp.
	// Delayed bank methods and manual capture require a different settlement
	// timestamp source and are deliberately not advertised by this plugin.
	form.Set("payment_method_types[0]", "card")
	form.Set("payment_intent_data[capture_method]", "automatic")
	form.Set("automatic_tax[enabled]", "false")
	form.Set("adaptive_pricing[enabled]", "false")
	for k, v := range metadata(c, i) {
		form.Set("metadata["+k+"]", v)
		form.Set("payment_intent_data[metadata]["+k+"]", v)
	}
	var s session
	if e := p.call(ctx, "POST", "/v1/checkout/sessions", "checkout:"+c.GatewayID+":"+i.IdempotencyKey, form, &s); e != nil {
		return paymentv1.Checkout{}, e
	}
	if s.Object != "checkout.session" || s.Mode != "payment" || !objectID.MatchString(s.ID) || s.AmountTotal != i.AmountMinor || s.Currency != strings.ToLower(i.Currency) || s.LiveMode != p.config.LiveMode {
		return paymentv1.Checkout{}, paymentv1.ErrUnavailable
	}
	out := paymentv1.Checkout{ProviderID: ID, GatewayID: c.GatewayID, SessionID: s.ID, Flow: "redirect", URL: s.URL, ExpiresAt: time.Unix(s.ExpiresAt, 0).UTC()}
	if paymentv1.ValidateCheckout(out, ID, Declarations(), p.now()) != nil {
		return paymentv1.Checkout{}, paymentv1.ErrUnavailable
	}
	return out, nil
}
func (p *Processor) retrieve(ctx context.Context, id string) (session, error) {
	var s session
	if !strings.HasPrefix(id, "cs_") || !objectID.MatchString(id) {
		return s, paymentv1.ErrInvalid
	}
	if err := p.checkMerchant(ctx); err != nil {
		return s, err
	}
	err := p.call(ctx, "GET", "/v1/checkout/sessions/"+id+"?expand%5B%5D=payment_intent.latest_charge", "", nil, &s)
	if err == nil && (s.ID != id || s.Object != "checkout.session" || s.Mode != "payment" || s.LiveMode != p.config.LiveMode) {
		err = paymentv1.ErrRejected
	}
	return s, err
}
func (p *Processor) evidence(c paymentv1.Config, s session) (paymentv1.VerifiedEvent, error) {
	var pi intent
	var out paymentv1.VerifiedEvent
	if s.PaymentStatus != "paid" || s.Status != "complete" || json.Unmarshal(s.PaymentIntent, &pi) != nil || pi.Status != "succeeded" || !strings.HasPrefix(pi.ID, "pi_") || !objectID.MatchString(pi.ID) || pi.LiveMode != p.config.LiveMode || pi.AmountReceived != s.AmountTotal || pi.Currency != s.Currency {
		return out, paymentv1.ErrRejected
	}
	ch := pi.Charge
	if !ch.Paid || !ch.Captured || ch.Status != "succeeded" || !objectID.MatchString(ch.ID) || ch.PaymentIntent != pi.ID || ch.AmountCaptured != s.AmountTotal || ch.Currency != s.Currency || ch.LiveMode != p.config.LiveMode || ch.Created <= 0 {
		return out, paymentv1.ErrRejected
	}
	for _, key := range []string{"aster_gateway", "aster_attempt", "aster_merchant", "aster_invoice", "aster_order", "aster_account", "aster_precision"} {
		if pi.Metadata[key] != s.Metadata[key] {
			return out, paymentv1.ErrRejected
		}
	}
	precision, e := strconv.Atoi(s.Metadata["aster_precision"])
	if e != nil {
		return out, paymentv1.ErrRejected
	}
	out = paymentv1.VerifiedEvent{ProviderID: ID, GatewayID: s.Metadata["aster_gateway"], AttemptID: s.Metadata["aster_attempt"], MerchantAccountID: s.Metadata["aster_merchant"], InvoiceID: s.Metadata["aster_invoice"], OrderID: s.Metadata["aster_order"], AccountID: s.Metadata["aster_account"], AmountMinor: s.AmountTotal, Currency: strings.ToUpper(s.Currency), CurrencyDecimalPlaces: precision, TransactionID: pi.ID, EventID: "settled:" + pi.ID, SettledAt: time.Unix(ch.Created, 0).UTC(), Status: "settled"}
	if out.GatewayID != c.GatewayID || paymentv1.ValidateEvent(out, ID, c.MerchantID, Declarations(), p.now()) != nil {
		return paymentv1.VerifiedEvent{}, paymentv1.ErrRejected
	}
	return out, nil
}

// VerifySignature authenticates the exact raw bytes, accepts rotated v1
// signatures, and rejects stale/future timestamps before any provider request.
func VerifySignature(raw []byte, header, secret string, now time.Time) error {
	if len(raw) == 0 || len(raw) > paymentv1.MaxBodyBytes || len(header) > 4096 {
		return paymentv1.ErrRejected
	}
	stamp := ""
	signatures := []string{}
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return paymentv1.ErrRejected
		}
		switch k {
		case "t":
			if stamp != "" {
				return paymentv1.ErrRejected
			}
			stamp = v
		case "v1":
			signatures = append(signatures, v)
		}
	}
	n, e := strconv.ParseInt(stamp, 10, 64)
	if e != nil || n <= 0 || strconv.FormatInt(n, 10) != stamp {
		return paymentv1.ErrRejected
	}
	delta := now.Sub(time.Unix(n, 0))
	if delta > 5*time.Minute || delta < -30*time.Second {
		return paymentv1.ErrRejected
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stamp + "."))
	mac.Write(raw)
	expected := mac.Sum(nil)
	for _, signature := range signatures {
		given, e := hex.DecodeString(signature)
		if e == nil && hmac.Equal(given, expected) {
			return nil
		}
	}
	return paymentv1.ErrRejected
}
func (p *Processor) Verify(ctx context.Context, c paymentv1.Config, in paymentv1.Callback) (paymentv1.VerifiedEvent, error) {
	if !p.valid(c) || paymentv1.ValidateCallback(in) != nil {
		return paymentv1.VerifiedEvent{}, paymentv1.ErrRejected
	}
	header := ""
	for _, h := range in.Headers {
		if strings.EqualFold(h.Name, "Stripe-Signature") {
			if len(h.Values) != 1 || header != "" {
				return paymentv1.VerifiedEvent{}, paymentv1.ErrRejected
			}
			header = h.Values[0]
		}
	}
	if VerifySignature(in.Body, header, p.config.WebhookSecret, p.now()) != nil {
		return paymentv1.VerifiedEvent{}, paymentv1.ErrRejected
	}
	var event struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Account  string `json:"account"`
		LiveMode bool   `json:"livemode"`
		Data     struct {
			Object struct {
				ID string `json:"id"`
			} `json:"object"`
		} `json:"data"`
	}
	if json.Unmarshal(in.Body, &event) != nil || !strings.HasPrefix(event.ID, "evt_") || !objectID.MatchString(event.ID) || event.LiveMode != p.config.LiveMode || event.Account != "" && event.Account != c.MerchantID || event.Type != "checkout.session.completed" && event.Type != "checkout.session.async_payment_succeeded" {
		return paymentv1.VerifiedEvent{}, paymentv1.ErrRejected
	}
	s, e := p.retrieve(ctx, event.Data.Object.ID)
	if e != nil {
		return paymentv1.VerifiedEvent{}, e
	}
	return p.evidence(c, s)
}
func (p *Processor) Query(ctx context.Context, c paymentv1.Config, in paymentv1.Query) (paymentv1.QueryResult, error) {
	if !p.valid(c) || paymentv1.ValidateQuery(in, Declarations()) != nil {
		return paymentv1.QueryResult{}, paymentv1.ErrInvalid
	}
	s, e := p.retrieve(ctx, in.SessionID)
	if e != nil {
		return paymentv1.QueryResult{}, e
	}
	for k, v := range metadata(c, in.Intent) {
		if s.Metadata[k] != v {
			return paymentv1.QueryResult{}, paymentv1.ErrRejected
		}
	}
	if s.AmountTotal != in.Intent.AmountMinor || s.Currency != strings.ToLower(in.Intent.Currency) {
		return paymentv1.QueryResult{}, paymentv1.ErrRejected
	}
	if s.PaymentStatus != "paid" {
		status := "pending"
		if s.Status == "expired" {
			status = "expired"
		}
		return paymentv1.QueryResult{Status: status}, nil
	}
	event, e := p.evidence(c, s)
	if e != nil {
		return paymentv1.QueryResult{}, e
	}
	return paymentv1.QueryResult{Status: "succeeded", Event: &event}, nil
}
func (p *Processor) Refund(ctx context.Context, c paymentv1.Config, in paymentv1.Refund) (paymentv1.RefundResult, error) {
	if !p.valid(c) || paymentv1.ValidateRefund(in, Declarations()) != nil || !strings.HasPrefix(in.TransactionID, "pi_") || !objectID.MatchString(in.TransactionID) {
		return paymentv1.RefundResult{}, paymentv1.ErrInvalid
	}
	if err := p.checkMerchant(ctx); err != nil {
		return paymentv1.RefundResult{}, err
	}
	// The default PaymentIntent response contains latest_charge as an ID.
	// Only decode fields needed for refund authorization, without expansion.
	var original struct {
		ID             string            `json:"id"`
		Status         string            `json:"status"`
		Metadata       map[string]string `json:"metadata"`
		Currency       string            `json:"currency"`
		AmountReceived int64             `json:"amount_received"`
		LiveMode       bool              `json:"livemode"`
	}
	if err := p.call(ctx, "GET", "/v1/payment_intents/"+in.TransactionID, "", nil, &original); err != nil {
		return paymentv1.RefundResult{}, err
	}
	if original.ID != in.TransactionID || original.Status != "succeeded" || original.Metadata["aster_gateway"] != c.GatewayID || original.Metadata["aster_merchant"] != c.MerchantID || original.Metadata["aster_precision"] != strconv.Itoa(in.CurrencyDecimalPlaces) || original.Currency != strings.ToLower(in.Currency) || original.AmountReceived < in.AmountMinor || original.LiveMode != p.config.LiveMode {
		return paymentv1.RefundResult{}, paymentv1.ErrRejected
	}
	var out struct {
		ID            string `json:"id"`
		Status        string `json:"status"`
		Currency      string `json:"currency"`
		Amount        int64  `json:"amount"`
		PaymentIntent string `json:"payment_intent"`
	}
	e := p.call(ctx, "POST", "/v1/refunds", "refund:"+c.GatewayID+":"+in.IdempotencyKey, url.Values{"payment_intent": {in.TransactionID}, "amount": {strconv.FormatInt(in.AmountMinor, 10)}}, &out)
	if e != nil {
		return paymentv1.RefundResult{}, e
	}
	if out.PaymentIntent != in.TransactionID || out.Currency != strings.ToLower(in.Currency) || out.Amount != in.AmountMinor {
		return paymentv1.RefundResult{}, paymentv1.ErrUnavailable
	}
	status := out.Status
	if status == "requires_action" {
		status = "pending"
	}
	if status == "canceled" {
		status = "failed"
	}
	result := paymentv1.RefundResult{RefundID: out.ID, Status: status}
	if paymentv1.ValidateRefundResult(result) != nil {
		return paymentv1.RefundResult{}, paymentv1.ErrUnavailable
	}
	return result, nil
}
