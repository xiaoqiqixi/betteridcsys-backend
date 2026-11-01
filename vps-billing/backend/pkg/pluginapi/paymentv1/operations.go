package paymentv1

import (
	"context"
	"time"
)

type Query struct {
	Intent    Intent `json:"intent"`
	SessionID string `json:"session_id"`
}
type QueryResult struct {
	Status string         `json:"status"` // pending, succeeded, expired, failed
	Event  *VerifiedEvent `json:"event,omitempty"`
}
type Querier interface {
	Query(context.Context, Config, Query) (QueryResult, error)
}
type Refund struct {
	IdempotencyKey        string `json:"idempotency_key"`
	TransactionID         string `json:"transaction_id"`
	AmountMinor           int64  `json:"amount_minor"`
	Currency              string `json:"currency"`
	CurrencyDecimalPlaces int    `json:"currency_decimal_places"`
}
type RefundResult struct {
	RefundID string `json:"refund_id"`
	Status   string `json:"status"` // pending, succeeded, failed
}
type Refunder interface {
	Refund(context.Context, Config, Refund) (RefundResult, error)
}

func ValidateQuery(in Query, d Declarations) error {
	// Historical attempts may predate new checkout limits.
	d.Min, d.Max = nil, nil
	if ValidateIntent(in.Intent, d) != nil || !ValidID(in.SessionID) {
		return ErrInvalid
	}
	return nil
}
func ValidateQueryResult(out QueryResult, cfg Config, in Query, d Declarations, now time.Time) error {
	if out.Status == "pending" || out.Status == "expired" || out.Status == "failed" {
		if out.Event == nil {
			return nil
		}
		return ErrInvalid
	}
	if out.Status != "succeeded" || out.Event == nil {
		return ErrInvalid
	}
	e, i := out.Event, in.Intent
	if ValidateEvent(*e, cfg.ProviderID, cfg.MerchantID, d, now) != nil || e.GatewayID != cfg.GatewayID || e.AttemptID != i.AttemptID || e.InvoiceID != i.InvoiceID || e.OrderID != i.OrderID || e.AccountID != i.AccountID || e.AmountMinor != i.AmountMinor || e.Currency != i.Currency || e.CurrencyDecimalPlaces != i.CurrencyDecimalPlaces {
		return ErrInvalid
	}
	return nil
}
func ValidateRefund(in Refund, d Declarations) error {
	if !ValidID(in.IdempotencyKey) || !ValidID(in.TransactionID) || in.AmountMinor <= 0 || in.AmountMinor > MaxMinor || !d.Supports(in.Currency, in.CurrencyDecimalPlaces) {
		return ErrInvalid
	}
	return nil
}
func ValidateRefundResult(out RefundResult) error {
	if !ValidID(out.RefundID) || out.Status != "pending" && out.Status != "succeeded" && out.Status != "failed" {
		return ErrInvalid
	}
	return nil
}
