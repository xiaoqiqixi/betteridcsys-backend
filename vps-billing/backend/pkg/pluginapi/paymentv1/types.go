// Package paymentv1 defines the independent payment adapter contract. Adapters
// cannot mutate billing state; a verified event is evidence for core validation.
package paymentv1

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxBodyBytes = 64 << 10
const MaxMinor int64 = 9007199254740991

var ErrInvalid = errors.New("invalid payment data")
var ErrUnavailable = errors.New("payment adapter unavailable")
var ErrRejected = errors.New("payment adapter rejected request")

type Currency struct {
	Code      string `json:"code"`
	Precision int    `json:"precision"`
}
type Declarations struct {
	Currencies    []Currency `json:"currencies"`
	CheckoutHosts []string   `json:"checkout_hosts"`
}

// Config carries only the explicitly approved merchant identity. Credentials
// stay in the worker's separately protected configuration, outside core.
type Config struct {
	MerchantID     string `json:"merchant_id"`
	ProviderID     string `json:"provider_id"`
	ManifestSHA256 string `json:"manifest_sha256"`
}
type Intent struct {
	AttemptID             string `json:"attempt_id"`
	IdempotencyKey        string `json:"idempotency_key"`
	InvoiceID             string `json:"invoice_id"`
	OrderID               string `json:"order_id"`
	AccountID             string `json:"account_id"`
	AmountMinor           int64  `json:"amount_minor"`
	Currency              string `json:"currency"`
	CurrencyDecimalPlaces int    `json:"currency_decimal_places"`
	CallbackURL           string `json:"callback_url"`
	ReturnURL             string `json:"return_url"`
}
type Checkout struct {
	ProviderID string    `json:"provider_id"`
	SessionID  string    `json:"session_id"`
	URL        string    `json:"checkout_url"`
	ExpiresAt  time.Time `json:"expires_at"`
}
type Header struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}
type Callback struct {
	Method   string   `json:"method"`
	Path     string   `json:"path"`
	RawQuery string   `json:"raw_query"`
	Headers  []Header `json:"headers,omitempty"`
	Body     []byte   `json:"body"`
}
type VerifiedEvent struct {
	AttemptID             string    `json:"attempt_id"`
	ProviderID            string    `json:"provider_id"`
	EventID               string    `json:"event_id"`
	TransactionID         string    `json:"transaction_id"`
	MerchantAccountID     string    `json:"merchant_account_id"`
	AccountID             string    `json:"account_id"`
	OrderID               string    `json:"order_id"`
	InvoiceID             string    `json:"invoice_id"`
	AmountMinor           int64     `json:"amount_minor"`
	Currency              string    `json:"currency"`
	CurrencyDecimalPlaces int       `json:"currency_decimal_places"`
	SettledAt             time.Time `json:"settled_at"`
	Status                string    `json:"status"`
}
type Processor interface {
	Create(context.Context, Config, Intent) (Checkout, error)
	Verify(context.Context, Config, Callback) (VerifiedEvent, error)
}

func ValidID(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}
func ValidCurrency(s string) bool {
	return len(s) == 3 && s[0] >= 'A' && s[0] <= 'Z' && s[1] >= 'A' && s[1] <= 'Z' && s[2] >= 'A' && s[2] <= 'Z'
}
func ValidHost(host string) bool {
	if len(host) == 0 || len(host) > 253 || host != strings.ToLower(host) || net.ParseIP(host) != nil {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
func (d Declarations) Validate() error {
	if len(d.Currencies) == 0 || len(d.Currencies) > 32 || len(d.CheckoutHosts) == 0 || len(d.CheckoutHosts) > 8 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, c := range d.Currencies {
		if !ValidCurrency(c.Code) || c.Precision < 0 || c.Precision > 6 || seen[c.Code] {
			return ErrInvalid
		}
		seen[c.Code] = true
	}
	seen = map[string]bool{}
	for _, h := range d.CheckoutHosts {
		if !ValidHost(h) || seen[h] {
			return ErrInvalid
		}
		seen[h] = true
	}
	return nil
}
func (d Declarations) Supports(code string, precision int) bool {
	for _, c := range d.Currencies {
		if c.Code == code && c.Precision == precision {
			return true
		}
	}
	return false
}
func ValidateConfig(c Config) error {
	if !ValidID(c.MerchantID) || !ValidID(c.ProviderID) || len(c.ManifestSHA256) != 64 {
		return ErrInvalid
	}
	for _, c := range c.ManifestSHA256 {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return ErrInvalid
		}
	}
	return nil
}
func httpsURL(raw string, optional bool) bool {
	if raw == "" {
		return optional
	}
	if len(raw) > 2048 || strings.ContainsAny(raw, "\r\n\x00") {
		return false
	}
	u, e := url.Parse(raw)
	return e == nil && u.Scheme == "https" && u.Opaque == "" && ValidHost(u.Hostname()) && u.Port() == "" && u.User == nil && u.Fragment == ""
}

// Core-generated callback/return URLs preserve the configured HTTPS origin,
// including an IP host or nonstandard port. Checkout destinations retain the
// stricter separately approved DNS-host boundary above.
func coreURL(raw string) bool {
	if raw == "" {
		return true
	}
	if len(raw) > 2048 || !utf8.ValidString(raw) || strings.ContainsAny(raw, "\r\n\x00") {
		return false
	}
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Opaque != "" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	for _, c := range u.Hostname() {
		if c < 33 || c > 126 {
			return false
		}
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}
func ValidateIntent(in Intent, d Declarations) error {
	for _, s := range []string{in.AttemptID, in.IdempotencyKey, in.InvoiceID, in.OrderID, in.AccountID} {
		if !ValidID(s) {
			return ErrInvalid
		}
	}
	if in.AttemptID != in.IdempotencyKey || in.AmountMinor <= 0 || in.AmountMinor > MaxMinor || !d.Supports(in.Currency, in.CurrencyDecimalPlaces) || !coreURL(in.CallbackURL) || !coreURL(in.ReturnURL) {
		return ErrInvalid
	}
	return nil
}
func ValidateCheckout(out Checkout, provider string, d Declarations, now time.Time) error {
	if out.ProviderID != provider || !ValidID(out.SessionID) || !httpsURL(out.URL, false) || !out.ExpiresAt.After(now) || out.ExpiresAt.After(now.Add(24*time.Hour)) {
		return ErrInvalid
	}
	u, _ := url.Parse(out.URL)
	for _, h := range d.CheckoutHosts {
		if u.Hostname() == h {
			return nil
		}
	}
	return ErrInvalid
}
func ValidateCallback(in Callback) error {
	if in.Method != "POST" || len(in.Path) == 0 || len(in.Path) > 1024 || in.Path[0] != '/' || len(in.RawQuery) > 4096 || len(in.Body) == 0 || len(in.Body) > MaxBodyBytes || len(in.Headers) > 32 || !utf8.ValidString(in.Path) || !utf8.ValidString(in.RawQuery) || strings.ContainsAny(in.Path+in.RawQuery, "\r\n\x00") {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, h := range in.Headers {
		name := strings.ToLower(h.Name)
		if len(name) == 0 || len(name) > 64 || seen[name] || len(h.Values) == 0 || len(h.Values) > 8 {
			return ErrInvalid
		}
		for _, c := range name {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return ErrInvalid
			}
		}
		seen[name] = true
		for _, v := range h.Values {
			if len(v) > 4096 || !utf8.ValidString(v) || strings.ContainsAny(v, "\r\n\x00") {
				return ErrInvalid
			}
		}
	}
	return nil
}
func ValidateEvent(out VerifiedEvent, provider, merchant string, d Declarations, now time.Time) error {
	for _, s := range []string{out.AttemptID, out.EventID, out.TransactionID, out.AccountID, out.OrderID, out.InvoiceID} {
		if !ValidID(s) {
			return ErrInvalid
		}
	}
	if out.ProviderID != provider || out.MerchantAccountID != merchant || out.AmountMinor <= 0 || out.AmountMinor > MaxMinor || !d.Supports(out.Currency, out.CurrencyDecimalPlaces) || out.Status != "settled" || out.SettledAt.IsZero() || out.SettledAt.After(now.Add(time.Minute)) {
		return ErrInvalid
	}
	return nil
}
