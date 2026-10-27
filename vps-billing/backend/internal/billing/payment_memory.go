package billing

import (
	"aster/billing/internal/integrations"
	"context"
	"time"
)

func (m *Memory) paymentRecords(actor User, invoiceID string) (int, int, error) {
	ii, oi := -1, -1
	for n, i := range m.invoices {
		if i.ID == invoiceID && (actor.Role == "admin" || i.UserID == actor.ID) {
			if ii >= 0 {
				return -1, -1, ErrPaymentState
			}
			ii = n
		}
	}
	if ii < 0 {
		return -1, -1, ErrNotFound
	}
	for n, o := range m.orders {
		if o.ID == m.invoices[ii].OrderID {
			if oi >= 0 {
				return -1, -1, ErrPaymentState
			}
			oi = n
		}
	}
	if oi < 0 {
		return -1, -1, ErrPaymentState
	}
	if e := paymentPair(actor, m.orders[oi], m.invoices[ii]); e != nil {
		return -1, -1, e
	}
	if currency, known := m.currencies.find(m.invoices[ii].Currency); !known || currency.DecimalPlaces != m.invoices[ii].CurrencyFormat.DecimalPlaces {
		return -1, -1, ErrPaymentState
	}
	return oi, ii, nil
}

func (m *Memory) ReservePayment(_ context.Context, actor User, invoiceID, key, provider, merchant string) (PaymentAttempt, bool, error) {
	if e := validatePaymentInput(actor, invoiceID, key, provider, merchant); e != nil {
		return PaymentAttempt{}, false, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	oi, ii, e := m.paymentRecords(actor, invoiceID)
	if e != nil {
		return PaymentAttempt{}, false, e
	}
	o, i := m.orders[oi], m.invoices[ii]
	if currency, _ := m.currencies.find(i.Currency); !currency.Enabled {
		return PaymentAttempt{}, false, ErrCurrency
	}
	for _, a := range m.paymentAttempts {
		if (a.Expectation.AccountID == i.UserID && a.RequestKey == key) || a.Expectation.InvoiceID == invoiceID {
			if a.RequestKey != key || a.Expectation != paymentExpectation(o, i, provider, merchant) {
				return PaymentAttempt{}, false, ErrPaymentConflict
			}
			if e = validateAttemptPair(a, o, i); e != nil {
				return PaymentAttempt{}, false, e
			}
			return clonePaymentAttempt(a), true, nil
		}
	}
	if o.Status != "pending_payment" || i.Status != "unpaid" || i.PaidAt != nil {
		return PaymentAttempt{}, false, ErrPaymentState
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := PaymentAttempt{ID: ID(), RequestKey: key, Expectation: paymentExpectation(o, i, provider, merchant), State: "pending", CreatedAt: now, UpdatedAt: now}
	if m.paymentAttempts == nil {
		m.paymentAttempts = map[string]PaymentAttempt{}
	}
	m.paymentAttempts[a.ID] = a
	return clonePaymentAttempt(a), false, nil
}

func (m *Memory) PaymentAttempt(_ context.Context, actor User, id string) (PaymentAttempt, error) {
	if !paymentActor(actor) || !validCancellationID(id) {
		return PaymentAttempt{}, ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.paymentAttempts[id]
	if !ok || (actor.Role != "admin" && a.Expectation.AccountID != actor.ID) {
		return PaymentAttempt{}, ErrNotFound
	}
	oi, ii, e := m.paymentRecords(actor, a.Expectation.InvoiceID)
	if e != nil {
		return PaymentAttempt{}, e
	}
	if e = validateAttemptPair(a, m.orders[oi], m.invoices[ii]); e != nil {
		return PaymentAttempt{}, e
	}
	return clonePaymentAttempt(a), nil
}

func (m *Memory) CompletePaymentCheckout(_ context.Context, id string, c integrations.Checkout, failed bool) (PaymentAttempt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.paymentAttempts[id]
	if !ok {
		return PaymentAttempt{}, ErrNotFound
	}
	if a.State == "settled" {
		return clonePaymentAttempt(a), nil
	}
	if a.State == "uncertain" {
		return clonePaymentAttempt(a), ErrPaymentUncertain
	}
	if a.State == "checkout" {
		if !failed && a.Checkout != nil && *a.Checkout == c {
			return clonePaymentAttempt(a), nil
		}
		return PaymentAttempt{}, ErrPaymentConflict
	}
	if a.State != "pending" {
		return PaymentAttempt{}, ErrPaymentState
	}
	if failed || validateCheckout(a, c) != nil {
		a.State = "uncertain"
		a.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
		m.paymentAttempts[id] = a
		return clonePaymentAttempt(a), ErrPaymentUncertain
	}
	a.State = "checkout"
	a.Checkout = &c
	a.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
	m.paymentAttempts[id] = clonePaymentAttempt(a)
	return clonePaymentAttempt(a), nil
}

func (m *Memory) SettlePayment(_ context.Context, id string, v integrations.VerifiedPayment) (PaymentSettlement, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.paymentAttempts[id]
	if !ok {
		return PaymentSettlement{}, ErrNotFound
	}
	if e := validatePayment(a, v); e != nil {
		return PaymentSettlement{}, e
	}
	actor := User{ID: a.Expectation.AccountID, Role: "customer"}
	oi, ii, e := m.paymentRecords(actor, a.Expectation.InvoiceID)
	if e != nil {
		return PaymentSettlement{}, e
	}
	o, i := m.orders[oi], m.invoices[ii]
	if e = validateAttemptPair(a, o, i); e != nil {
		return PaymentSettlement{}, e
	}
	for _, p := range m.paymentReceipts {
		if p.AttemptID == id || p.ProviderID == v.ProviderID && (p.EventID == v.EventID || p.TransactionID == v.TransactionID) {
			if !samePaymentReceipt(p, a, v) || a.State != "settled" {
				return PaymentSettlement{}, ErrPaymentConflict
			}
			return PaymentSettlement{AttemptID: id, Invoice: cloneInvoice(i), Order: cloneOrder(o), Replay: true}, nil
		}
	}
	if a.State == "settled" {
		return PaymentSettlement{}, ErrPaymentState
	}
	for _, event := range m.orderEvents {
		if event.AggregateID == o.ID && (event.EventType == "payment.settled" || event.EventType == "order.cancelled") {
			return PaymentSettlement{}, ErrPaymentState
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	paid := v.SettledAt.UTC().Truncate(time.Microsecond)
	o.Status = "paid"
	i.Status = "paid"
	i.PaidAt = &paid
	a.State = "settled"
	a.UpdatedAt = now
	m.orders[oi] = o
	m.invoices[ii] = i
	m.paymentAttempts[id] = a
	m.paymentReceipts = append(m.paymentReceipts, paymentReceipt{AttemptID: id, ProviderID: v.ProviderID, EventID: v.EventID, TransactionID: v.TransactionID, SettledAt: paid, ReceivedAt: now})
	m.audits = append(m.audits, paidAudit(o, a, now))
	m.orderEvents = append(m.orderEvents, orderOutboxEvent{ID: ID(), AggregateID: o.ID, EventType: "payment.settled", Payload: paidEvent(o, i, a), CreatedAt: now})
	return PaymentSettlement{AttemptID: id, Invoice: cloneInvoice(i), Order: cloneOrder(o)}, nil
}
