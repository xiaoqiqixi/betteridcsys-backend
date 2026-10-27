// Package integrations defines compiled, typed provider boundaries. Adapters have
// no Store or SQL access; financial state transitions remain the core's concern.
package integrations

import (
	"context"
	"errors"
	"time"
)

var ErrUnavailable = errors.New("integration unavailable")
var ErrInvalidResult = errors.New("invalid integration result")
var ErrUnsupportedCurrency = errors.New("currency not supported by provider")

const maxMinor int64 = 9007199254740991

func supportedCurrency(c string) bool {
	return len(c) == 3 && c[0] >= 'A' && c[0] <= 'Z' && c[1] >= 'A' && c[1] <= 'Z' && c[2] >= 'A' && c[2] <= 'Z'
}

func validCurrencyPrecision(n int) bool { return n >= 0 && n <= 6 }

type Kind string

const (
	Payment Kind = "payment"
	Email   Kind = "email"
)

type ConfigurationField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	Secret   bool   `json:"secret"`
}
type Manifest struct {
	// Explicit integer-unit precision for every supported code. Providers must
	// match the core-owned invoice snapshot; they never infer or convert it.
	CurrencyDecimalPlaces map[string]int       `json:"currency_decimal_places"`
	SupportedCurrencies   []string             `json:"supported_currencies"`
	ID                    string               `json:"id"`
	Kind                  Kind                 `json:"kind"`
	Name                  string               `json:"name"`
	Description           string               `json:"description"`
	Capabilities          []string             `json:"capabilities"`
	ConfigurationFields   []ConfigurationField `json:"configuration_fields"`
	Limitations           []string             `json:"limitations"`
}
type Summary struct {
	Manifest
	Enabled           bool   `json:"enabled"`
	Configured        bool   `json:"configured"`
	Status            string `json:"status"`
	ConfigurationMode string `json:"configuration_mode"`
}

// Config contains ordinary values or secret references, never literal secrets.
// Secret references use env:NAME or file:/absolute/path. Factories resolve them
// with ResolveSecret; neither Config nor resolved values belong in API output.
type Config map[string]string
type CheckoutRequest struct {
	AttemptID, MerchantAccountID  string
	CurrencyDecimalPlaces         int
	InvoiceID, OrderID, AccountID string
	AmountMinor                   int64
	Currency                      string
	IdempotencyKey                string
}
type Checkout struct {
	ProviderID string    `json:"provider_id"`
	SessionID  string    `json:"session_id"`
	URL        string    `json:"checkout_url"`
	ExpiresAt  time.Time `json:"expires_at"`
}
type CallbackRequest struct {
	Headers map[string][]string
	Body    []byte
}

// VerifiedPayment is returned ONLY after an adapter verifies the signature,
// timestamp/replay window and merchant identity from the raw callback body.
// It is not permission to mark paid. Core must independently validate all fields
// and atomically deduplicate ProviderID+EventID AND ProviderID+TransactionID.
type VerifiedPayment struct {
	AttemptID                                             string
	CurrencyDecimalPlaces                                 int
	ProviderID, EventID, TransactionID, MerchantAccountID string
	AccountID, OrderID, InvoiceID                         string
	AmountMinor                                           int64
	Currency                                              string
	SettledAt                                             time.Time
	Status                                                string // only "settled" can ever qualify for settlement
}
type PaymentProvider interface {
	CreateCheckout(context.Context, CheckoutRequest) (Checkout, error)
	VerifyCallback(context.Context, CallbackRequest) (VerifiedPayment, error)
}
type EmailMessage struct {
	IdempotencyKey string
	To, Template   string
	Variables      map[string]string
}
type EmailReceipt struct{ ProviderMessageID string }
type EmailProvider interface {
	Send(context.Context, EmailMessage) (EmailReceipt, error)
}

// Definition is registered by trusted application code, not an HTTP payload.
// Exactly one factory matching Kind is required. Disabled built-ins are reserved.
type Definition struct {
	Manifest       Manifest
	PaymentFactory func(context.Context, Config) (PaymentProvider, error)
	EmailFactory   func(context.Context, Config) (EmailProvider, error)
	CheckoutHosts  []string // exact HTTPS DNS hosts; no wildcards or user input
}
type Options struct {
	PaymentID, EmailID string
	Configs            map[string]Config
	Timeout            time.Duration
}
