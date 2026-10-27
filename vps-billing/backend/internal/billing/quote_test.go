package billing

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

func quotedPlan(t *testing.T, s Store, code string) (Plan, PriceQuote) {
	t.Helper()
	plans, err := s.Plans(t.Context(), code)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plans {
		if p.ID == "starter" {
			return p, PriceQuote{p.PriceMinor, p.CurrencyFormat.DecimalPlaces, p.PriceRevision}
		}
	}
	t.Fatal("missing starter plan")
	return Plan{}, PriceQuote{}
}
func TestQuotedOrderRejectsStalePriceAndReplaysOriginal(t *testing.T) {
	m := NewMemory()
	admin, _ := m.UserByEmail(t.Context(), "admin@aster.local")
	user, _ := m.UserByEmail(t.Context(), "demo@aster.local")
	_, quote := quotedPlan(t, m, "CNY")
	prices, _ := m.PlanPrices(t.Context(), "CNY")
	_, err := m.UpdatePlanPrices(t.Context(), admin, PlanPricesUpdate{Revision: prices.Revision, Currency: "CNY", Prices: []PlanPriceInput{{PlanID: "starter", Amount: "99.00", Available: true, PlanAvailable: true}}})
	if err != nil {
		t.Fatal(err)
	}
	beforeAudits := len(m.audits)
	if _, _, err = m.CreateQuotedOrder(t.Context(), user, "starter", "quote", "stale-quote", "CNY", &quote); !errors.Is(err, ErrPriceChanged) {
		t.Fatal(err)
	}
	if len(m.orders) != 0 || len(m.invoices) != 0 || len(m.requests) != 0 || len(m.audits) != beforeAudits {
		t.Fatal("stale quote created records")
	}
	_, fresh := quotedPlan(t, m, "CNY")
	for _, wrong := range []PriceQuote{{PriceMinor: fresh.PriceMinor - 1, DecimalPlaces: 2, PriceRevision: fresh.PriceRevision}, {PriceMinor: fresh.PriceMinor, DecimalPlaces: 3, PriceRevision: fresh.PriceRevision}, {PriceMinor: fresh.PriceMinor, DecimalPlaces: 2, PriceRevision: fresh.PriceRevision - 1}} {
		if _, _, err = m.CreateQuotedOrder(t.Context(), user, "starter", "quote", "mismatch-quote", "CNY", &wrong); !errors.Is(err, ErrPriceChanged) {
			t.Fatal(wrong, err)
		}
	}
	order, replay, err := m.CreateQuotedOrder(t.Context(), user, "starter", "quote", "stale-quote", "CNY", &fresh)
	if err != nil || replay || order.TotalMinor != 9900 {
		t.Fatal(order, replay, err)
	}
	prices, _ = m.PlanPrices(t.Context(), "CNY")
	_, err = m.UpdatePlanPrices(t.Context(), admin, PlanPricesUpdate{Revision: prices.Revision, Currency: "CNY", Prices: []PlanPriceInput{{PlanID: "starter", Amount: "12.00", Available: false, PlanAvailable: false}}})
	if err != nil {
		t.Fatal(err)
	}
	// The first execution's quote is a precondition, never a new idempotency identity.
	replayed, replay, err := m.CreateQuotedOrder(t.Context(), user, "starter", "quote", "stale-quote", "CNY", &quote)
	if err != nil || !replay || replayed.ID != order.ID || replayed.TotalMinor != 9900 {
		t.Fatal(replayed, replay, err)
	}
	if _, replay, err = m.CreateOrder(t.Context(), user, "starter", "quote", "stale-quote", "CNY"); err != nil || !replay {
		t.Fatal("legacy retry fingerprint changed", replay, err)
	}
}
func TestQuotedOrderHTTPRequiresCompleteExactQuote(t *testing.T) {
	m := NewMemory()
	server := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()}).Handler())
	defer server.Close()
	user := client(t, server.URL, "demo@aster.local", "DemoCustomer!2026")
	for _, raw := range []string{`null`, `{}`, `{"price_minor":3900,"decimal_places":null,"price_revision":1}`, `{"price_minor":3900,"decimal_places":2,"price_revision":1,"Price_minor":1}`, `{"price_minor":3900,"decimal_places":2,"price_revision":1,"price_minor":1}`, `{"price_minor":3900,"decimal_places":2,"price_reviſion":1}`, `{"price_minor":"3900","decimal_places":2,"price_revision":1}`} {
		if _, err := decodePriceQuote([]byte(raw)); err == nil {
			t.Fatal("ambiguous quote", raw)
		}
	}
	for _, p := range []map[string]any{{"plan_id": "starter", "hostname": "test", "quote": PriceQuote{3900, 2, 1}}, {"plan_id": "starter", "hostname": "test", "currency": "CNY", "quote": nil}} {
		if status, _ := user.request("POST", "/api/v1/orders", p, "quote-invalid"); status != 400 {
			t.Fatal(status, p)
		}
	}
	if status, out := user.request("POST", "/api/v1/orders", map[string]any{"plan_id": "starter", "hostname": "test", "currency": "CNY", "quote": PriceQuote{3901, 2, 1}}, "quote-stale-http"); status != 409 || out["error"].(map[string]any)["code"] != "PRICE_CHANGED" {
		t.Fatal(status, out)
	}
	if status, out := user.request("POST", "/api/v1/orders", map[string]any{"plan_id": "starter", "hostname": "test", "currency": "CNY", "quote": PriceQuote{3900, 2, 1}}, "quote-fresh-http"); status != 201 {
		t.Fatal(status, out)
	}
	if len(m.orders) != 1 || len(m.invoices) != 1 || len(m.requests) != 1 {
		t.Fatal("failed quotes persisted records")
	}
}

func TestMySQLQuotedOrderRejectsStaleConfirmationAtomically(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	admin := User{ID: ID(), Email: ID() + "@test.invalid", Name: "Admin", Role: "admin", PasswordHash: "fixture"}
	user := User{ID: ID(), Email: ID() + "@test.invalid", Name: "Customer", Role: "customer", PasswordHash: "fixture"}
	for _, u := range []User{admin, user} {
		if err := m.AddUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	prices, _ := m.PlanPrices(ctx, "CNY")
	if _, err := m.UpdatePlanPrices(ctx, admin, PlanPricesUpdate{Revision: prices.Revision, Currency: "CNY", Prices: []PlanPriceInput{{PlanID: "starter", Amount: "39.00", Available: true, PlanAvailable: true}}}); err != nil {
		t.Fatal(err)
	}
	_, oldQuote := quotedPlan(t, m, "CNY")
	prices, _ = m.PlanPrices(ctx, "CNY")
	if _, err := m.UpdatePlanPrices(ctx, admin, PlanPricesUpdate{Revision: prices.Revision, Currency: "CNY", Prices: []PlanPriceInput{{PlanID: "starter", Amount: "99.00", Available: true, PlanAvailable: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.CreateQuotedOrder(ctx, user, "starter", "quote", "sql-stale-quote", "CNY", &oldQuote); !errors.Is(err, ErrPriceChanged) {
		t.Fatal(err)
	}
	for _, table := range []string{"orders", "invoices", "order_requests", "outbox_events"} {
		var count int
		if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatal("stale quote committed", table, count, err)
		}
	}
	var audits int
	if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_events WHERE action='order.created'").Scan(&audits); err != nil || audits != 0 {
		t.Fatal(audits, err)
	}
	_, fresh := quotedPlan(t, m, "CNY")
	order, replay, err := m.CreateQuotedOrder(ctx, user, "starter", "quote", "sql-stale-quote", "CNY", &fresh)
	if err != nil || replay || order.TotalMinor != 9900 {
		t.Fatal(order, replay, err)
	}
	prices, _ = m.PlanPrices(ctx, "CNY")
	if _, err = m.UpdatePlanPrices(ctx, admin, PlanPricesUpdate{Revision: prices.Revision, Currency: "CNY", Prices: []PlanPriceInput{{PlanID: "starter", Amount: "15.00", Available: false, PlanAvailable: false}}}); err != nil {
		t.Fatal(err)
	}
	replayed, replay, err := m.CreateQuotedOrder(ctx, user, "starter", "quote", "sql-stale-quote", "CNY", &oldQuote)
	if err != nil || !replay || replayed.ID != order.ID || replayed.TotalMinor != 9900 || replayed.CurrencyFormat != order.CurrencyFormat {
		t.Fatal(replayed, replay, err)
	}
}

func TestMySQLQuoteWaitsForConcurrentPriceCommit(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	admin := User{ID: ID(), Email: ID() + "@test.invalid", Name: "Admin", Role: "admin", PasswordHash: "fixture"}
	user := User{ID: ID(), Email: ID() + "@test.invalid", Name: "Customer", Role: "customer", PasswordHash: "fixture"}
	for _, u := range []User{admin, user} {
		if err := m.AddUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := m.PlanPrices(ctx, "CNY")
	if _, err := m.UpdatePlanPrices(ctx, admin, PlanPricesUpdate{Revision: p.Revision, Currency: "CNY", Prices: []PlanPriceInput{{PlanID: "starter", Amount: "39.00", Available: true, PlanAvailable: true}}}); err != nil {
		t.Fatal(err)
	}
	_, quote := quotedPlan(t, m, "CNY")
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var revision int64
	if err = tx.QueryRowContext(ctx, "SELECT prices_revision FROM currency_settings WHERE id=1 FOR UPDATE").Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE plan_prices SET amount_minor=9900 WHERE plan_id='starter' AND currency='CNY'"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE currency_settings SET prices_revision=prices_revision+1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		_, _, err := m.CreateQuotedOrder(ctx, user, "starter", "concurrent-quote", "concurrent-quote", "CNY", &quote)
		result <- err
	}()
	<-started
	select {
	case err := <-result:
		t.Fatal("quoted order bypassed active price transaction", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrPriceChanged) {
			t.Fatal("stale quoted order accepted after price commit", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, table := range []string{"orders", "invoices", "order_requests", "outbox_events"} {
		var count int
		if err = m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatal("concurrent stale quote persisted", table, count, err)
		}
	}
}
