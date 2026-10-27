package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

var (
	ErrCancellationInput       = errors.New("invalid cancellation request")
	ErrCancellationState       = errors.New("order cannot be safely cancelled")
	ErrCancellationUnavailable = errors.New("local cancellation requires disabled payments")
)

// OrderCancellation closes only an unpaid order and its original invoice. It
// never changes money, creates a refund or authorizes resource operations.
type OrderCancellation struct {
	Order   Order   `json:"order"`
	Invoice Invoice `json:"invoice"`
}

func validCancellationID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func normalizeCancellationInput(actor User, orderID, reason string) (string, error) {
	if actor.ID == "" || (actor.Role != "customer" && actor.Role != "admin") {
		return "", ErrNotFound
	}
	reason = strings.TrimSpace(reason)
	if !validCancellationID(orderID) || reason == "" || !validText(reason, 500, false) {
		return "", ErrCancellationInput
	}
	return reason, nil
}

func validateCancellationPair(actor User, o Order, i Invoice) (bool, error) {
	if actor.Role != "admin" && o.UserID != actor.ID {
		return false, ErrNotFound
	}
	if !validCancellationID(o.ID) || !validCancellationID(i.ID) || o.UserID == "" || i.UserID != o.UserID || i.OrderID != o.ID || o.ServiceID != "" ||
		!validAmount(o.TotalMinor) || i.AmountMinor != o.TotalMinor || o.Currency != i.Currency || o.CurrencyFormat != i.CurrencyFormat ||
		!validCurrencyFormat(o.CurrencyFormat) || o.CurrencyFormat.Code != o.Currency || i.PaidAt != nil ||
		o.CreatedAt.IsZero() || i.IssuedAt.IsZero() || !o.CreatedAt.Equal(i.IssuedAt) || i.DueAt.Before(i.IssuedAt) {
		return false, ErrCancellationState
	}
	if (o.Issuer == nil) != (i.Issuer == nil) || (o.Issuer != nil && *o.Issuer != *i.Issuer) {
		return false, ErrCancellationState
	}
	if o.Status == "cancelled" && i.Status == "void" {
		return true, nil
	}
	if o.Status != "pending_payment" || i.Status != "unpaid" {
		return false, ErrCancellationState
	}
	return false, nil
}

func cancellationAudit(actor User, o Order, reason string) Audit {
	return Audit{ID: ID(), UserID: o.UserID, Action: "order.cancelled", Target: o.ID, CreatedAt: time.Now().UTC(), ActorEmail: actor.Email,
		Detail: "未付款订单已取消，原账单已作废；原因：" + reason}
}

func cancellationEvent(o Order, i Invoice, actor User, reason string) []byte {
	// The reason is retained once in the core audit, not copied into event data.
	raw, _ := json.Marshal(map[string]string{"order_id": o.ID, "invoice_id": i.ID, "status": "cancelled", "invoice_status": "void", "actor_user_id": actor.ID})
	return raw
}

func validCancellationEventPayload(raw []byte, o Order, i Invoice, cancelled bool) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if !financeTokens(d, 0) {
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return false
	}
	var fields map[string]string
	if json.Unmarshal(raw, &fields) != nil || fields["order_id"] != o.ID || fields["invoice_id"] != i.ID {
		return false
	}
	if cancelled {
		return len(fields) == 5 && fields["status"] == "cancelled" && fields["invoice_status"] == "void" && fields["actor_user_id"] != ""
	}
	return len(fields) == 3 && fields["status"] == "pending_payment"
}

// Memory models the same untouched creation-event boundary as MySQL. It has no
// consumer; retaining events also makes retry and ambiguity checks equivalent.
type orderOutboxEvent struct {
	ID, AggregateID, EventType string
	Payload                    []byte
	CreatedAt                  time.Time
	ProcessedAt                *time.Time
	Attempts                   int
}

func orderCreatedEvent(o Order, i Invoice) orderOutboxEvent {
	raw, _ := json.Marshal(map[string]string{"order_id": o.ID, "invoice_id": i.ID, "status": "pending_payment"})
	return orderOutboxEvent{ID: ID(), AggregateID: o.ID, EventType: "order.created", Payload: raw, CreatedAt: o.CreatedAt}
}

func (m *Memory) CancelOrder(_ context.Context, actor User, orderID, reason string) (OrderCancellation, error) {
	reason, err := normalizeCancellationInput(actor, orderID, reason)
	if err != nil {
		return OrderCancellation{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	oi := -1
	for n, o := range m.orders {
		if o.ID == orderID && (actor.Role == "admin" || o.UserID == actor.ID) {
			if oi >= 0 {
				return OrderCancellation{}, ErrCancellationState
			}
			oi = n
		}
	}
	if oi < 0 {
		return OrderCancellation{}, ErrNotFound
	}
	o := m.orders[oi]
	ii := -1
	for n, i := range m.invoices {
		if i.OrderID == orderID {
			if ii >= 0 {
				return OrderCancellation{}, ErrCancellationState
			}
			ii = n
		}
	}
	if ii < 0 {
		return OrderCancellation{}, ErrCancellationState
	}
	i := m.invoices[ii]
	// This uses the same mutex as reservation and settlement. An expired or
	// uncertain checkout still may have moved money outside the core.
	for _, attempt := range m.paymentAttempts {
		if attempt.Expectation.OrderID == orderID {
			return OrderCancellation{}, ErrCancellationState
		}
	}
	replay, err := validateCancellationPair(actor, o, i)
	if err != nil {
		return OrderCancellation{}, err
	}
	if currency, known := m.currencies.find(o.Currency); !known || currency.DecimalPlaces != o.CurrencyFormat.DecimalPlaces {
		return OrderCancellation{}, ErrCancellationState
	}
	for _, s := range m.services {
		if s.UserID == o.UserID && s.Name == o.Hostname {
			return OrderCancellation{}, ErrCancellationState
		}
	}
	created, cancelled := 0, 0
	for _, e := range m.orderEvents {
		if e.AggregateID != o.ID {
			continue
		}
		switch e.EventType {
		case "order.created":
			created++
			if e.Attempts != 0 || e.ProcessedAt != nil || !validCancellationEventPayload(e.Payload, o, i, false) {
				return OrderCancellation{}, ErrCancellationState
			}
		case "order.cancelled":
			cancelled++
			if !replay || !validCancellationEventPayload(e.Payload, o, i, true) {
				return OrderCancellation{}, ErrCancellationState
			}
		default:
			return OrderCancellation{}, ErrCancellationState
		}
	}
	if created != 1 || (replay && cancelled != 1) || (!replay && cancelled != 0) {
		return OrderCancellation{}, ErrCancellationState
	}
	if !replay {
		o.Status, i.Status = "cancelled", "void"
		a := cancellationAudit(actor, o, reason)
		m.orders[oi], m.invoices[ii] = o, i
		m.audits = append(m.audits, a)
		m.orderEvents = append(m.orderEvents, orderOutboxEvent{ID: ID(), AggregateID: o.ID, EventType: "order.cancelled", Payload: cancellationEvent(o, i, actor, reason), CreatedAt: a.CreatedAt})
	}
	return OrderCancellation{Order: cloneOrder(o), Invoice: cloneInvoice(i)}, nil
}
