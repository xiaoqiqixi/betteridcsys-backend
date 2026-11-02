package billing

import (
	"aster/billing/internal/commerce"
	"aster/billing/internal/contracttest"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

func TestMySQLCommerceLegacyAndModernCheckoutShareFinalStock(t *testing.T) {
	f := newCancellationSQLFixture(t)
	m := f.m
	migrationExec(t, f.ctx, m.DB, "UPDATE products SET stock=1,visibility='public' WHERE id='starter'")
	migrationExec(t, f.ctx, m.DB, `INSERT INTO product_prices(product_id,currency,cycle,recurring_minor,setup_minor,decimal_places)
 SELECT plan_id,currency,'monthly',amount_minor,0,2 FROM plan_prices WHERE plan_id='starter' AND currency='CNY'
 ON DUPLICATE KEY UPDATE recurring_minor=VALUES(recurring_minor)`)
	p, err := m.Product06(f.ctx, true, "starter")
	if err != nil {
		t.Fatal(err)
	}
	input := quotedOrder06(t, m, f.owner, p, 1)
	type result struct {
		id  string
		err error
	}
	results := make(chan result, 12)
	var wg sync.WaitGroup
	for n := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if n%2 == 0 {
				o, _, err := m.CreateOrder(f.ctx, f.owner, p.ID, "legacy-"+ID(), ID(), "CNY")
				results <- result{o.ID, err}
			} else {
				o, err := m.CreateOrder06(f.ctx, f.owner, input, ID())
				results <- result{o.Order.ID, err}
			}
		}()
	}
	wg.Wait()
	close(results)
	winner, accepted := "", 0
	for r := range results {
		if r.err == nil {
			winner, accepted = r.id, accepted+1
		} else if !errors.Is(r.err, ErrUnavailable) && !errors.Is(r.err, commerce.ErrQuoteChanged) {
			t.Fatal(r.err)
		}
	}
	if accepted != 1 {
		t.Fatal("mixed API versions oversold the last unit", accepted)
	}
	assertStock := func(want int) {
		t.Helper()
		var stock int
		if err := m.DB.QueryRow("SELECT stock FROM products WHERE id='starter'").Scan(&stock); err != nil || stock != want {
			t.Fatal("stock", stock, "want", want, err)
		}
	}
	assertStock(0)
	order, err := m.Order06(f.ctx, f.owner.ID, winner)
	if err != nil {
		t.Fatal(err)
	}
	contracttest.Data(t, "Order", map[string]any{"data": order.Order})
	if _, err = m.CancelOrder(f.ctx, f.other, winner, "Wrong owner"); !errors.Is(err, ErrNotFound) {
		t.Fatal("wrong owner released stock", err)
	}
	assertStock(0)
	for range 2 {
		if _, err = m.CancelOrder(f.ctx, f.owner, winner, "Release last unit"); err != nil {
			t.Fatal(err)
		}
	}
	assertStock(1)

	// A definite legacy reservation is replayable and releases once on cancel.
	legacy, _, key := f.order(t)
	if replay, duplicate, err := m.CreateOrder(f.ctx, f.owner, "starter", legacy.Hostname, key, "CNY"); err != nil || !duplicate || replay.ID != legacy.ID {
		t.Fatal(replay, duplicate, err)
	}
	var raw []byte
	if err = m.DB.QueryRow("SELECT snapshot FROM order_items WHERE order_id=?", legacy.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var snapshot orderItemSnapshot06
	if err = json.Unmarshal(raw, &snapshot); err != nil || !snapshot.Legacy {
		t.Fatal("legacy stock marker changed fulfillment protocol", err)
	}
	assertStock(0)
	cancelled, err := m.CancelOrder06(f.ctx, f.owner, legacy.ID, "Cancel through the current API")
	if err != nil {
		t.Fatal(err)
	}
	contracttest.Data(t, "Order", map[string]any{"data": cancelled})
	if _, err = m.CancelOrder(f.ctx, f.owner, legacy.ID, "Replay through the deprecated API"); err != nil {
		t.Fatal(err)
	}
	assertStock(1)

	// Payment consumes the reservation; uncertain/pending payment cannot release it.
	legacy, invoice, _ := f.order(t)
	payments := paymentFixture{store: m, owner: f.owner, other: f.other, ctx: f.ctx}
	attempt := payments.reserve(t, invoice)
	if _, err = m.CancelOrder(f.ctx, f.owner, legacy.ID, "Cannot release an active attempt"); !errors.Is(err, ErrCancellationState) {
		t.Fatal(err)
	}
	assertStock(0)
	receipt := syntheticPayment(attempt)
	for range 2 {
		if _, err = m.SettlePayment(f.ctx, attempt.ID, receipt); err != nil {
			t.Fatal(err)
		}
	}
	var reserved int
	if err = m.DB.QueryRow("SELECT stock_reserved FROM order_items WHERE order_id=?", legacy.ID).Scan(&reserved); err != nil || reserved != 0 {
		t.Fatal("payment retained an unconsumed reservation", reserved, err)
	}
	assertStock(0)
	if _, _, err = m.CreateOrder(f.ctx, f.owner, "starter", "another-"+ID(), ID(), "CNY"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("legacy checkout oversold paid stock", err)
	}
}
