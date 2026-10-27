package billing

import (
	"aster/billing/internal/integrations"
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

type paymentFixture struct {
	store        Store
	owner, other User
	ctx          context.Context
}

func (f paymentFixture) order(t *testing.T) (Order, Invoice) {
	return f.orderCurrency(t, "CNY")
}

func (f paymentFixture) orderCurrency(t *testing.T, currency string) (Order, Invoice) {
	t.Helper()
	o, _, e := f.store.CreateOrder(f.ctx, f.owner, "starter", "pay-"+ID(), ID(), currency)
	if e != nil {
		t.Fatal(e)
	}
	orders, e := f.store.Orders(f.ctx, f.owner.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, stored := range orders {
		if stored.ID == o.ID {
			o = stored
			break
		}
	}
	list, e := f.store.Invoices(f.ctx, f.owner.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, i := range list {
		if i.OrderID == o.ID {
			return o, i
		}
	}
	t.Fatal("missing invoice")
	return Order{}, Invoice{}
}

func (f paymentFixture) reserve(t *testing.T, i Invoice) PaymentAttempt {
	t.Helper()
	a, replay, e := f.store.ReservePayment(f.ctx, f.owner, i.ID, "invoice:"+i.ID, "payment.synthetic", "merchant-synthetic")
	if e != nil || replay {
		t.Fatal(a, replay, e)
	}
	return a
}

func syntheticPayment(a PaymentAttempt) integrations.VerifiedPayment {
	w := a.Expectation
	return integrations.VerifiedPayment{AttemptID: a.ID, ProviderID: w.ProviderID, EventID: "event-" + ID(), TransactionID: "transaction-" + ID(), MerchantAccountID: w.MerchantAccountID, AccountID: w.AccountID, OrderID: w.OrderID, InvoiceID: w.InvoiceID, AmountMinor: w.AmountMinor, Currency: w.Currency, CurrencyDecimalPlaces: w.CurrencyDecimalPlaces, SettledAt: time.Now().UTC().Truncate(time.Microsecond), Status: "settled"}
}

func syntheticCheckout(a PaymentAttempt) integrations.Checkout {
	return integrations.Checkout{ProviderID: a.Expectation.ProviderID, SessionID: "session-" + a.ID, URL: "https://checkout.example.invalid/" + a.ID, ExpiresAt: time.Now().UTC().Add(time.Hour)}
}

func TestPaymentMemoryContract(t *testing.T) {
	m := NewMemory()
	owner, other, _ := cancellationUsers(t, m)
	paymentStoreContract(t, paymentFixture{store: m, owner: owner, other: other, ctx: t.Context()})
}

func paymentStoreContract(t *testing.T, f paymentFixture) {
	t.Run("durable reservation ownership and immutable expectation", func(t *testing.T) {
		o, i := f.order(t)
		if _, _, e := f.store.ReservePayment(f.ctx, f.other, i.ID, "invoice:"+i.ID, "payment.synthetic", "merchant-synthetic"); !errors.Is(e, ErrNotFound) {
			t.Fatal("cross-owner reserve", e)
		}
		a := f.reserve(t, i)
		if a.Expectation != paymentExpectation(o, i, "payment.synthetic", "merchant-synthetic") || a.State != "pending" || a.CreatedAt.IsZero() {
			t.Fatal(a)
		}
		retry, replay, e := f.store.ReservePayment(f.ctx, f.owner, i.ID, a.RequestKey, a.Expectation.ProviderID, a.Expectation.MerchantAccountID)
		if e != nil || !replay || !reflect.DeepEqual(a, retry) {
			t.Fatal("reservation changed on retry", retry, replay, e)
		}
		for _, change := range [][3]string{{"different-key", a.Expectation.ProviderID, a.Expectation.MerchantAccountID}, {a.RequestKey, "payment.other", a.Expectation.MerchantAccountID}, {a.RequestKey, a.Expectation.ProviderID, "another-merchant"}} {
			if _, _, e = f.store.ReservePayment(f.ctx, f.owner, i.ID, change[0], change[1], change[2]); !errors.Is(e, ErrPaymentConflict) {
				t.Fatal("reservation identity replaced", e)
			}
		}
		if _, e = f.store.PaymentAttempt(f.ctx, f.other, a.ID); !errors.Is(e, ErrNotFound) {
			t.Fatal("attempt ownership", e)
		}
		if _, e = f.store.CancelOrder(f.ctx, f.owner, o.ID, "requested"); !errors.Is(e, ErrCancellationState) {
			t.Fatal("pending external operation allowed cancellation", e)
		}
	})
	t.Run("lost checkout response stays uncertain and may settle", func(t *testing.T) {
		o, i := f.order(t)
		a := f.reserve(t, i)
		out, e := f.store.CompletePaymentCheckout(f.ctx, a.ID, integrations.Checkout{}, true)
		if !errors.Is(e, ErrPaymentUncertain) || out.State != "uncertain" {
			t.Fatal(out, e)
		}
		out, e = f.store.PaymentAttempt(f.ctx, f.owner, a.ID)
		if e != nil || out.State != "uncertain" {
			t.Fatal("uncertain reservation not durable", out, e)
		}
		if _, e = f.store.CompletePaymentCheckout(f.ctx, a.ID, syntheticCheckout(a), false); !errors.Is(e, ErrPaymentUncertain) {
			t.Fatal("uncertain reservation silently re-opened", e)
		}
		if _, e = f.store.CancelOrder(f.ctx, f.owner, o.ID, "requested"); !errors.Is(e, ErrCancellationState) {
			t.Fatal("uncertain payment allowed cancellation", e)
		}
		paid, e := f.store.SettlePayment(f.ctx, a.ID, syntheticPayment(a))
		if e != nil || paid.Order.Status != "paid" || paid.Invoice.Status != "paid" {
			t.Fatal(paid, e)
		}
	})
	t.Run("checkout result snapshot and early callback", func(t *testing.T) {
		_, i := f.order(t)
		a := f.reserve(t, i)
		c := syntheticCheckout(a)
		out, e := f.store.CompletePaymentCheckout(f.ctx, a.ID, c, false)
		if e != nil || out.State != "checkout" || out.Checkout == nil || *out.Checkout != c {
			t.Fatal(out, e)
		}
		out.Checkout.URL = "https://mutation.invalid"
		stored, e := f.store.PaymentAttempt(f.ctx, f.owner, a.ID)
		if e != nil || stored.Checkout == nil || *stored.Checkout != c {
			t.Fatal("checkout response aliases storage", stored, e)
		}
		_, i = f.order(t)
		a = f.reserve(t, i)
		if _, e = f.store.SettlePayment(f.ctx, a.ID, syntheticPayment(a)); e != nil {
			t.Fatal(e)
		}
		if out, e = f.store.CompletePaymentCheckout(f.ctx, a.ID, integrations.Checkout{}, true); e != nil || out.State != "settled" {
			t.Fatal("late checkout result overwrote settlement", out, e)
		}
	})
	t.Run("untrusted claims cannot change financial state", func(t *testing.T) {
		o, i := f.order(t)
		a := f.reserve(t, i)
		v := syntheticPayment(a)
		cases := []struct {
			name   string
			change func(*integrations.VerifiedPayment)
		}{
			{"attempt", func(v *integrations.VerifiedPayment) { v.AttemptID = ID() }},
			{"provider", func(v *integrations.VerifiedPayment) { v.ProviderID = "payment.other" }},
			{"merchant", func(v *integrations.VerifiedPayment) { v.MerchantAccountID = "different" }},
			{"owner", func(v *integrations.VerifiedPayment) { v.AccountID = f.other.ID }},
			{"order", func(v *integrations.VerifiedPayment) { v.OrderID = ID() }},
			{"invoice", func(v *integrations.VerifiedPayment) { v.InvoiceID = ID() }},
			{"amount", func(v *integrations.VerifiedPayment) { v.AmountMinor++ }},
			{"currency", func(v *integrations.VerifiedPayment) { v.Currency = "USD" }},
			{"precision", func(v *integrations.VerifiedPayment) { v.CurrencyDecimalPlaces = 3 }},
			{"unsettled", func(v *integrations.VerifiedPayment) { v.Status = "pending" }},
			{"empty event", func(v *integrations.VerifiedPayment) { v.EventID = "" }},
			{"empty transaction", func(v *integrations.VerifiedPayment) { v.TransactionID = "" }},
			{"future time", func(v *integrations.VerifiedPayment) { v.SettledAt = time.Now().Add(time.Hour) }},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				bad := v
				c.change(&bad)
				if _, e := f.store.SettlePayment(f.ctx, a.ID, bad); !errors.Is(e, ErrPaymentConflict) {
					t.Fatal(e)
				}
			})
		}
		stored, e := f.store.Invoice(f.ctx, f.owner.ID, i.ID)
		if e != nil || stored.Status != "unpaid" || stored.PaidAt != nil {
			t.Fatal(stored, e)
		}
		paid, e := f.store.SettlePayment(f.ctx, a.ID, v)
		if e != nil || paid.Replay || paid.Order.Status != "paid" || paid.Invoice.PaidAt == nil {
			t.Fatal(paid, e)
		}
		expectedO, expectedI := o, i
		expectedO.Status = "paid"
		expectedI.Status = "paid"
		expectedI.PaidAt = &v.SettledAt
		if !reflect.DeepEqual(paid.Order, expectedO) || !reflect.DeepEqual(paid.Invoice, expectedI) {
			t.Fatal("payment changed immutable records", paid, expectedO, expectedI)
		}
		replay, e := f.store.SettlePayment(f.ctx, a.ID, v)
		if e != nil || !replay.Replay {
			t.Fatal("callback retry failed", replay, e)
		}
		bad := v
		bad.EventID = "another-event"
		if _, e = f.store.SettlePayment(f.ctx, a.ID, bad); !errors.Is(e, ErrPaymentConflict) {
			t.Fatal("new event overwrote existing receipt", e)
		}
		audits, e := f.store.Audits(f.ctx, f.owner.ID)
		if e != nil {
			t.Fatal(e)
		}
		count := 0
		for _, audit := range audits {
			if audit.Target == o.ID && audit.Action == "payment.settled" {
				count++
			}
		}
		if count != 1 {
			t.Fatal("settlement audit repeated", count)
		}
		if _, e = f.store.CancelOrder(f.ctx, f.owner, o.ID, "requested"); !errors.Is(e, ErrCancellationState) {
			t.Fatal("paid order cancelled", e)
		}
	})
	t.Run("provider transaction and event cannot cross invoices", func(t *testing.T) {
		_, i := f.order(t)
		a := f.reserve(t, i)
		v := syntheticPayment(a)
		if _, e := f.store.SettlePayment(f.ctx, a.ID, v); e != nil {
			t.Fatal(e)
		}
		for _, kind := range []string{"event", "transaction"} {
			_, i := f.order(t)
			b := f.reserve(t, i)
			bad := syntheticPayment(b)
			if kind == "event" {
				bad.EventID = v.EventID
			} else {
				bad.TransactionID = v.TransactionID
			}
			if _, e := f.store.SettlePayment(f.ctx, b.ID, bad); !errors.Is(e, ErrPaymentConflict) {
				t.Fatal(kind, e)
			}
			stored, e := f.store.Invoice(f.ctx, f.owner.ID, i.ID)
			if e != nil || stored.Status != "unpaid" {
				t.Fatal(stored, e)
			}
		}
	})
	t.Run("concurrent callbacks produce one transition", func(t *testing.T) {
		_, i := f.order(t)
		a := f.reserve(t, i)
		v := syntheticPayment(a)
		var wg sync.WaitGroup
		results := make(chan PaymentSettlement, 16)
		failures := make(chan error, 16)
		for n := 0; n < 16; n++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out, e := f.store.SettlePayment(f.ctx, a.ID, v)
				results <- out
				failures <- e
			}()
		}
		wg.Wait()
		close(results)
		close(failures)
		for e := range failures {
			if e != nil {
				t.Fatal(e)
			}
		}
		transitions := 0
		for out := range results {
			if !out.Replay {
				transitions++
			}
		}
		if transitions != 1 {
			t.Fatal("non-idempotent transitions", transitions)
		}
	})
	t.Run("checkout completion races callback without losing settlement", func(t *testing.T) {
		for n := 0; n < 8; n++ {
			_, i := f.order(t)
			a := f.reserve(t, i)
			v := syntheticPayment(a)
			c := syntheticCheckout(a)
			start := make(chan struct{})
			var wg sync.WaitGroup
			var completeErr, settleErr error
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				_, completeErr = f.store.CompletePaymentCheckout(f.ctx, a.ID, c, false)
			}()
			go func() { defer wg.Done(); <-start; _, settleErr = f.store.SettlePayment(f.ctx, a.ID, v) }()
			close(start)
			wg.Wait()
			if completeErr != nil || settleErr != nil {
				t.Fatal(completeErr, settleErr)
			}
			stored, e := f.store.PaymentAttempt(f.ctx, f.owner, a.ID)
			if e != nil || stored.State != "settled" {
				t.Fatal(stored, e)
			}
		}
	})
	t.Run("registered currency precision is preserved without conversion", func(t *testing.T) {
		users, e := f.store.Users(f.ctx)
		if e != nil {
			t.Fatal(e)
		}
		var admin User
		for _, u := range users {
			if u.Role == "admin" {
				admin = u
				break
			}
		}
		if admin.ID == "" {
			t.Fatal("missing admin fixture")
		}
		configureTestCurrency(t, f.store, admin, "KWD", 3, "1.234")
		_, i := f.orderCurrency(t, "KWD")
		a := f.reserve(t, i)
		if a.Expectation.Currency != "KWD" || a.Expectation.AmountMinor != 1234 || a.Expectation.CurrencyDecimalPlaces != 3 {
			t.Fatal("precision changed", a)
		}
		v := syntheticPayment(a)
		v.CurrencyDecimalPlaces = 2
		if _, e = f.store.SettlePayment(f.ctx, a.ID, v); !errors.Is(e, ErrPaymentConflict) {
			t.Fatal("precision mismatch accepted", e)
		}
		v.CurrencyDecimalPlaces = 3
		result, e := f.store.SettlePayment(f.ctx, a.ID, v)
		if e != nil || result.Invoice.AmountMinor != 1234 || result.Invoice.Currency != "KWD" {
			t.Fatal(result, e)
		}
	})
	t.Run("cancellation and reservation are mutually exclusive", func(t *testing.T) {
		for n := 0; n < 12; n++ {
			o, i := f.order(t)
			start := make(chan struct{})
			var wg sync.WaitGroup
			var reserveErr, cancelErr error
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				_, _, reserveErr = f.store.ReservePayment(f.ctx, f.owner, i.ID, "invoice:"+i.ID, "payment.synthetic", "merchant-synthetic")
			}()
			go func() {
				defer wg.Done()
				<-start
				_, cancelErr = f.store.CancelOrder(f.ctx, f.owner, o.ID, "race test")
			}()
			close(start)
			wg.Wait()
			if (reserveErr == nil) == (cancelErr == nil) {
				t.Fatal("expected exactly one transition", reserveErr, cancelErr)
			}
			if reserveErr != nil && !errors.Is(reserveErr, ErrPaymentState) {
				t.Fatal(reserveErr)
			}
			if cancelErr != nil && !errors.Is(cancelErr, ErrCancellationState) {
				t.Fatal(cancelErr)
			}
		}
	})
}

func TestPaymentMemoryRejectsChangedCanonicalInvoice(t *testing.T) {
	m := NewMemory()
	owner, other, _ := cancellationUsers(t, m)
	f := paymentFixture{m, owner, other, t.Context()}
	_, i := f.order(t)
	a := f.reserve(t, i)
	v := syntheticPayment(a)
	m.mu.Lock()
	for n := range m.invoices {
		if m.invoices[n].ID == i.ID {
			m.invoices[n].AmountMinor++
		}
	}
	m.mu.Unlock()
	if _, e := m.SettlePayment(t.Context(), a.ID, v); !errors.Is(e, ErrPaymentState) {
		t.Fatal("changed invoice accepted", e)
	}
	if len(m.paymentReceipts) != 0 {
		t.Fatal("failed validation wrote receipt")
	}
}
