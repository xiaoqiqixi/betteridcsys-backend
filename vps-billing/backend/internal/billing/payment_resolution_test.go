package billing

import (
	"aster/billing/internal/integrations"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestPaymentResolutionMemory(t *testing.T) {
	m := NewMemory()
	owner, other, admin := cancellationUsers(t, m)
	resolutionContract(t, paymentFixture{store: m, owner: owner, other: other, ctx: t.Context()}, admin, func(a PaymentAttempt, expiry time.Time) {
		m.mu.Lock()
		defer m.mu.Unlock()
		a = m.paymentAttempts[a.ID]
		c := *a.Checkout
		c.ExpiresAt = expiry
		a.Checkout = &c
		m.paymentAttempts[a.ID] = a
	})
}
func TestPaymentResolutionMySQL(t *testing.T) {
	f := newCancellationSQLFixture(t)
	resolutionContract(t, paymentFixture{store: f.m, owner: f.owner, other: f.other, ctx: f.ctx}, f.admin, func(a PaymentAttempt, expiry time.Time) {
		c := syntheticCheckout(a)
		c.ExpiresAt = expiry
		raw, _ := json.Marshal(c)
		migrationExec(t, f.ctx, f.m.DB, "UPDATE payment_attempts SET checkout=? WHERE id=?", raw, a.ID)
	})
}
func resolutionContract(t *testing.T, f paymentFixture, admin User, expire func(PaymentAttempt, time.Time)) {
	resolver := f.store.(paymentResolutionStore)
	t.Run("grace period, new attempt, late duplicate and exact replay", func(t *testing.T) {
		o, i := f.order(t)
		a := f.reserve(t, i)
		checkout := syntheticCheckout(a)
		if _, err := f.store.CompletePaymentCheckout(f.ctx, a.ID, checkout, false); err != nil {
			t.Fatal(err)
		}
		stored, err := f.store.PaymentAttempt(f.ctx, f.owner, a.ID)
		if err != nil || stored.Checkout == nil || !stored.Checkout.ExpiresAt.Equal(checkout.ExpiresAt) {
			t.Fatal("provider expiry not persisted", stored, err)
		}
		expire(a, time.Now().Add(-time.Second))
		if _, _, err := f.store.ReservePayment(f.ctx, f.owner, i.ID, "new-attempt", a.Expectation.ProviderID, a.Expectation.MerchantAccountID); !errors.Is(err, ErrPaymentConflict) {
			t.Fatal("grace released reservation", err)
		}
		if _, err := f.store.CancelOrder(f.ctx, f.owner, o.ID, "wait"); !errors.Is(err, ErrCancellationState) {
			t.Fatal("grace allowed cancellation", err)
		}
		expire(a, time.Now().Add(-paymentExpiryGrace-time.Second))
		b, replay, err := f.store.ReservePayment(f.ctx, f.owner, i.ID, "new-attempt", a.Expectation.ProviderID, a.Expectation.MerchantAccountID)
		if err != nil || replay || b.ID == a.ID {
			t.Fatal("expired checkout stuck", b, replay, err)
		}
		old, replay, err := f.store.ReservePayment(f.ctx, f.owner, i.ID, a.RequestKey, a.Expectation.ProviderID, a.Expectation.MerchantAccountID)
		if err != nil || !replay || old.ID != a.ID || old.State != "expired" {
			t.Fatal("old idempotency/history lost", old, replay, err)
		}
		first := syntheticPayment(a)
		paid, err := f.store.SettlePayment(f.ctx, a.ID, first)
		if err != nil || paid.Invoice.Status != "paid" || paid.Disposition != "settled" {
			t.Fatal("late real money dropped", paid, err)
		}
		second := syntheticPayment(b)
		duplicate, err := f.store.SettlePayment(f.ctx, b.ID, second)
		if err != nil || duplicate.Disposition != "duplicate" || !duplicate.Invoice.SettlementReview || !duplicate.Invoice.PaidAt.Equal(*paid.Invoice.PaidAt) {
			t.Fatal("duplicate money not surfaced", duplicate, err)
		}
		again, err := f.store.SettlePayment(f.ctx, b.ID, second)
		if err != nil || !again.Replay || again.Disposition != "duplicate" {
			t.Fatal("duplicate receipt replay", again, err)
		}
		persisted, err := f.store.Invoice(f.ctx, f.owner.ID, i.ID)
		if err != nil || !persisted.SettlementReview {
			t.Fatal("invoice flag not durable", persisted, err)
		}
		audits, err := f.store.Audits(f.ctx, "")
		count := 0
		for _, event := range audits {
			if event.Target == o.ID && event.Action == "payment.duplicate" {
				count++
			}
		}
		if err != nil || count != 1 {
			t.Fatal("duplicate audit count", count, err)
		}
		second.TransactionID = first.TransactionID
		if _, err = f.store.SettlePayment(f.ctx, b.ID, second); !errors.Is(err, ErrPaymentConflict) {
			t.Fatal("provider transaction reuse accepted", err)
		}
	})
	t.Run("uncertain never expires, explicit void audits reason, late settlement", func(t *testing.T) {
		o, i := f.order(t)
		a := f.reserve(t, i)
		if _, err := f.store.CompletePaymentCheckout(f.ctx, a.ID, integrations.Checkout{}, true); !errors.Is(err, ErrPaymentUncertain) {
			t.Fatal(err)
		}
		if _, err := f.store.CancelOrder(f.ctx, f.owner, o.ID, "uncertain"); !errors.Is(err, ErrCancellationState) {
			t.Fatal(err)
		}
		for _, actor := range []User{f.owner, f.other} {
			if _, err := resolver.VoidPayment(f.ctx, actor, a.ID, "reviewed"); !errors.Is(err, ErrNotFound) {
				t.Fatal("customer void", err)
			}
		}
		if _, err := resolver.VoidPayment(f.ctx, admin, a.ID, " "); !errors.Is(err, ErrPaymentInput) {
			t.Fatal("missing reason", err)
		}
		forged := f.owner
		forged.Role = "admin"
		if _, err := resolver.VoidPayment(f.ctx, forged, a.ID, "forged authority"); !errors.Is(err, ErrAdminRequired) {
			t.Fatal("store did not recheck staff authority", err)
		}
		voided, err := resolver.VoidPayment(f.ctx, admin, a.ID, "provider reconciled by staff")
		if err != nil || voided.State != "voided" {
			t.Fatal(voided, err)
		}
		events, _ := f.store.Audits(f.ctx, "")
		found := false
		for _, e := range events {
			if e.Target == a.ID && e.Action == "payment.voided" && e.ActorEmail == admin.Email && e.Detail == "provider reconciled by staff" {
				found = true
			}
		}
		if !found {
			t.Fatal("void not audited")
		}
		v := syntheticPayment(a)
		paid, err := f.store.SettlePayment(f.ctx, a.ID, v)
		if err != nil || paid.Invoice.Status != "paid" {
			t.Fatal("voided receipt lost", paid, err)
		}
	})
	t.Run("cancellation after expiry retains late money for manual reconciliation", func(t *testing.T) {
		o, i := f.order(t)
		a := f.reserve(t, i)
		if _, err := f.store.CompletePaymentCheckout(f.ctx, a.ID, syntheticCheckout(a), false); err != nil {
			t.Fatal(err)
		}
		expire(a, time.Now().Add(-paymentExpiryGrace-time.Second))
		if _, err := f.store.CancelOrder(f.ctx, f.owner, o.ID, "expired checkout"); err != nil {
			t.Fatal(err)
		}
		v := syntheticPayment(a)
		result, err := f.store.SettlePayment(f.ctx, a.ID, v)
		if err != nil || result.Disposition != "cancelled" || !result.Invoice.SettlementReview || result.Order.Status != "cancelled" || result.Invoice.Status != "void" {
			t.Fatal("cancelled late receipt lost or reopened order", result, err)
		}
		if result, err = f.store.SettlePayment(f.ctx, a.ID, v); err != nil || !result.Replay {
			t.Fatal(result, err)
		}
	})
	t.Run("concurrent replacement and settlement", func(t *testing.T) {
		_, i := f.order(t)
		a := f.reserve(t, i)
		if _, err := resolver.VoidPayment(f.ctx, admin, a.ID, "confirmed stuck"); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		attempts := make(chan PaymentAttempt, 8)
		for range 8 {
			wg.Go(func() {
				b, _, err := f.store.ReservePayment(f.ctx, f.owner, i.ID, "concurrent-"+a.ID, a.Expectation.ProviderID, a.Expectation.MerchantAccountID)
				if err != nil {
					t.Error(err)
					return
				}
				attempts <- b
			})
		}
		wg.Wait()
		close(attempts)
		var b PaymentAttempt
		for attempt := range attempts {
			if b.ID != "" && b.ID != attempt.ID {
				t.Fatal("multiple live attempts")
			}
			b = attempt
		}
		if b.ID == "" {
			t.Fatal("missing replacement")
		}
		receipts := []integrations.VerifiedPayment{syntheticPayment(a), syntheticPayment(b)}
		for _, v := range receipts {
			wg.Go(func() {
				if _, err := f.store.SettlePayment(f.ctx, v.AttemptID, v); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		invoice, err := f.store.Invoice(f.ctx, f.owner.ID, i.ID)
		if err != nil || invoice.Status != "paid" || !invoice.SettlementReview {
			t.Fatal(invoice, err)
		}
	})
}

func TestPaymentResolutionHTTP(t *testing.T) {
	h := newPaymentHTTPHarness(t, nil)
	o, i := h.order(t)
	h.checkout(t, i)
	var id string
	h.memory.mu.Lock()
	for key, a := range h.memory.paymentAttempts {
		id = key
		c := *a.Checkout
		c.ExpiresAt = time.Now().Add(-paymentExpiryGrace - time.Second)
		a.Checkout = &c
		h.memory.paymentAttempts[key] = a
	}
	h.memory.mu.Unlock()
	h.checkout(t, i)
	if creates, _ := h.runtime.counts(); creates != 2 {
		t.Fatal("legacy checkout did not issue exactly one successor", creates)
	}
	h.checkout(t, i)
	if creates, _ := h.runtime.counts(); creates != 2 {
		t.Fatal("successor replay called provider", creates)
	}
	path := "/api/v1/admin/payment-attempts/" + id + "/void"
	if status, _ := h.customer.request("POST", path, map[string]string{"reason": "no"}, ""); status != 403 {
		t.Fatal("customer void allowed", status)
	}
	staff := client(t, h.server.URL, "admin@aster.local", "DemoAdmin!2026")
	if status, _ := staff.request("POST", path, map[string]string{"reason": ""}, ""); status != 400 {
		t.Fatal("missing reason", status)
	}
	h.memory.mu.Lock()
	for key, a := range h.memory.paymentAttempts {
		if a.State == "checkout" {
			id = key
		}
	}
	h.memory.mu.Unlock()
	path = "/api/v1/admin/payment-attempts/" + id + "/void"
	csrf := staff.csrf
	staff.csrf = "invalid"
	if status, _ := staff.request("POST", path, map[string]string{"reason": "reviewed"}, ""); status != 403 {
		t.Fatal("CSRF bypass", status)
	}
	staff.csrf = csrf
	if status, out := staff.request("POST", path, map[string]string{"reason": "reviewed"}, ""); status != 200 {
		t.Fatal(status, out)
	}
	if status, out := h.customer.request("POST", "/api/v1/orders/"+o.ID+"/cancel", map[string]string{"reason": "voided"}, ""); status != 200 {
		t.Fatal("resolved attempt blocks cancel with live runtime", status, out)
	}
}

func TestLateVerifiedCallbackAfterReplacementIsRetained(t *testing.T) {
	h := newPaymentHTTPHarness(t, nil)
	_, invoice := h.order(t)
	h.checkout(t, invoice)
	h.memory.mu.Lock()
	for id, a := range h.memory.paymentAttempts {
		c := *a.Checkout
		c.ExpiresAt = time.Now().Add(-paymentExpiryGrace - time.Second)
		a.Checkout = &c
		h.memory.paymentAttempts[id] = a
	}
	h.memory.mu.Unlock()
	h.checkout(t, invoice)
	path := "/api/v1/payment-callbacks/" + h.runtime.selection.ID
	// New checkout settles first; a verified old checkout is real duplicate money.
	for _, n := range []int{1, 0} {
		raw, signature := h.runtime.signed(t, h.runtime.event(t, n))
		if status, out := paymentHTTPNotify(t, h, "POST", path, raw, "invalid", ""); status == 200 {
			t.Fatal("invalid signature accepted", out)
		}
		for range 2 {
			if status, out := paymentHTTPNotify(t, h, "POST", path, raw, signature, ""); status != 200 {
				t.Fatal("late receipt/replay lost", status, out)
			}
		}
	}
	saved, err := h.memory.Invoice(t.Context(), h.owner.ID, invoice.ID)
	if err != nil || !saved.SettlementReview || saved.Status != "paid" {
		t.Fatal(saved, err)
	}
	if len(h.memory.paymentReceipts) != 2 {
		t.Fatal("lost or repeated receipt", len(h.memory.paymentReceipts))
	}
}
