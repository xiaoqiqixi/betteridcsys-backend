package billing

import (
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
)

func TestMySQLDynamicCurrencyPriceTransactions(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	admin := User{ID: ID(), Email: ID() + "@test.invalid", Name: "Currency administrator", Role: "admin", PasswordHash: "fixture"}
	user := User{ID: ID(), Email: ID() + "@test.invalid", Name: "Currency customer", Role: "customer", PasswordHash: "fixture"}
	for _, u := range []User{admin, user} {
		if err := m.AddUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	configureTestCurrency(t, m, admin, "JPY", 0, "1250")
	configureTestCurrency(t, m, admin, "KWD", 3, "1.234")
	jpy, _, err := m.CreateOrder(ctx, user, "starter", "jpy-test", "jpy-original", "JPY")
	if err != nil || jpy.TotalMinor != 1250 || jpy.CurrencyFormat.DecimalPlaces != 0 || jpy.PlanSnapshot == nil || jpy.PlanSnapshot.BillingCycle != "monthly" {
		t.Fatal(jpy, err)
	}
	kwd, _, err := m.CreateOrder(ctx, user, "starter", "kwd-test", "kwd-original", "KWD")
	if err != nil || kwd.TotalMinor != 1234 || kwd.CurrencyFormat.DecimalPlaces != 3 {
		t.Fatal(kwd, err)
	}
	config, err := m.Currencies(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	input := currencyUpdateFrom(config)
	for n := range input.Currencies {
		if input.Currencies[n].Code == "JPY" {
			input.Currencies[n].Name = "Updated display"
			input.Currencies[n].Prefix = "JPY "
		}
	}
	if _, err = m.UpdateCurrencies(ctx, admin, input); err != nil {
		t.Fatal(err)
	}
	prices, _ := m.PlanPrices(ctx, "JPY")
	changedPrices := PlanPricesUpdate{Revision: prices.Revision, Currency: "JPY", Prices: []PlanPriceInput{{PlanID: "starter", Amount: "1400", Available: true, PlanAvailable: true}}}
	if _, err = m.UpdatePlanPrices(ctx, admin, changedPrices); err != nil {
		t.Fatal(err)
	}
	migrationExec(t, ctx, m.DB, "UPDATE plans SET cpu=8,memory_gb=64,name='Changed plan' WHERE id='starter'")
	got, replay, err := m.CreateOrder(ctx, user, "starter", "jpy-test", "jpy-original", "JPY")
	if err != nil || !replay || got.TotalMinor != 1250 || got.CurrencyFormat != jpy.CurrencyFormat || !reflect.DeepEqual(got.PlanSnapshot, jpy.PlanSnapshot) {
		t.Fatal("historical snapshot changed", got, replay, err)
	}
	invoices, err := m.Invoices(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range invoices {
		if v.OrderID == jpy.ID && (v.AmountMinor != 1250 || v.CurrencyFormat != jpy.CurrencyFormat) {
			t.Fatal(v)
		}
	}
	config, _ = m.Currencies(ctx, true)
	input = currencyUpdateFrom(config)
	for n := range input.Currencies {
		if input.Currencies[n].Code == "KWD" {
			input.Currencies[n].DecimalPlaces = 2
		}
	}
	if _, err = m.UpdateCurrencies(ctx, admin, input); !errors.Is(err, ErrCurrencyPrecision) {
		t.Fatal(err)
	}
	// Failed audit inserts roll back the entire config, default and price changes.
	config, _ = m.Currencies(ctx, true)
	beforeSettings, _ := m.Settings(ctx)
	beforePrices, _ := m.PlanPrices(ctx, "JPY")
	input = currencyUpdateFrom(config)
	input.DefaultCurrency = "JPY"
	migrationExec(t, ctx, m.DB, "CREATE TRIGGER currency_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='fixture audit failure'")
	if _, err = m.UpdateCurrencies(ctx, admin, input); err == nil {
		t.Fatal("registry audit failure did not roll back")
	}
	changedPrices.Revision = beforePrices.Revision
	changedPrices.Prices[0].Amount = "1500"
	if _, err = m.UpdatePlanPrices(ctx, admin, changedPrices); err == nil {
		t.Fatal("price audit failure did not roll back")
	}
	migrationExec(t, ctx, m.DB, "DROP TRIGGER currency_reject_audit")
	afterConfig, _ := m.Currencies(ctx, true)
	afterSettings, _ := m.Settings(ctx)
	afterPrices, _ := m.PlanPrices(ctx, "JPY")
	if !reflect.DeepEqual(config, afterConfig) || beforeSettings != afterSettings || !reflect.DeepEqual(beforePrices, afterPrices) {
		t.Fatal("partial transaction committed")
	}
	invalidBatch := PlanPricesUpdate{Revision: beforePrices.Revision, Currency: "JPY", Prices: []PlanPriceInput{{PlanID: "starter", Amount: "2000", Available: true, PlanAvailable: true}, {PlanID: "missing-plan", Amount: "3000", Available: true, PlanAvailable: true}}}
	if _, err = m.UpdatePlanPrices(ctx, admin, invalidBatch); !errors.Is(err, ErrPlanPrices) {
		t.Fatal("invalid plan batch", err)
	}
	afterPrices, _ = m.PlanPrices(ctx, "JPY")
	if !reflect.DeepEqual(beforePrices, afterPrices) {
		t.Fatal("partial plan batch persisted")
	}
	// An outstanding service blocks disabling even after its invoices are closed.
	snapshot, _ := json.Marshal(kwd.CurrencyFormat)
	migrationExec(t, ctx, m.DB, "INSERT INTO services(id,user_id,name,plan_name,region,status,next_due_at,price_minor,currency,cpu,memory_gb,disk_gb,currency_snapshot) VALUES(?,?,'fixture','snapshot plan','snapshot region','suspended',UTC_TIMESTAMP(6),1234,'KWD',1,2,40,?)", ID(), user.ID, snapshot)
	input = currencyUpdateFrom(config)
	for n := range input.Currencies {
		if input.Currencies[n].Code == "KWD" {
			input.Currencies[n].Enabled = false
		}
	}
	_, err = m.UpdateCurrencies(ctx, admin, input)
	var blocked *CurrencyInUse
	if !errors.As(err, &blocked) || blocked.Usage.PendingOrders != 1 || blocked.Usage.UnpaidInvoices != 1 || blocked.Usage.OngoingServices != 1 {
		t.Fatal(err)
	}
	migrationExec(t, ctx, m.DB, "UPDATE orders SET status='cancelled' WHERE user_id=? AND currency='KWD'", user.ID)
	migrationExec(t, ctx, m.DB, "UPDATE invoices SET status='void' WHERE user_id=? AND currency='KWD'", user.ID)
	_, err = m.UpdateCurrencies(ctx, admin, input)
	if !errors.As(err, &blocked) || blocked.Usage.OngoingServices != 1 || blocked.Usage.PendingOrders != 0 || blocked.Usage.UnpaidInvoices != 0 {
		t.Fatal(err)
	}
	migrationExec(t, ctx, m.DB, "UPDATE services SET status='terminated' WHERE user_id=?", user.ID)
	if _, err = m.UpdateCurrencies(ctx, admin, input); err != nil {
		t.Fatal(err)
	}
	if _, _, err = m.CreateOrder(ctx, user, "starter", "kwd-new", "disabled-new", "KWD"); !errors.Is(err, ErrCurrencyDisabled) {
		t.Fatal(err)
	}
	if got, replay, err = m.CreateOrder(ctx, user, "starter", "kwd-test", "kwd-original", "KWD"); err != nil || !replay || got.CurrencyFormat != kwd.CurrencyFormat || got.TotalMinor != 1234 {
		t.Fatal(got, replay, err)
	}
	if _, err = m.Summary(ctx, user.ID, "KWD"); err != nil {
		t.Fatal(err)
	}
	services, err := m.Services(ctx, user.ID)
	if err != nil || len(services) != 1 || services[0].CurrencyFormat != kwd.CurrencyFormat {
		t.Fatal(services, err)
	}
}

func TestMySQLDynamicCurrencyConcurrentRevisionAndDisable(t *testing.T) {
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
	config, _ := m.Currencies(ctx, true)
	input := currencyUpdateFrom(config)
	input.Currencies[0].Name = "Concurrent rename"
	concurrent := func(fn func() error, want error) {
		t.Helper()
		start := make(chan struct{})
		results := make(chan error, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; results <- fn() }()
		}
		close(start)
		wg.Wait()
		close(results)
		success, conflict := 0, 0
		for err := range results {
			if err == nil {
				success++
			} else if errors.Is(err, want) {
				conflict++
			} else {
				t.Fatal(err)
			}
		}
		if success != 1 || conflict != 1 {
			t.Fatal(success, conflict)
		}
	}
	concurrent(func() error { _, err := m.UpdateCurrencies(ctx, admin, input); return err }, ErrCurrencyConflict)
	prices, _ := m.PlanPrices(ctx, "CNY")
	priceUpdate := PlanPricesUpdate{Revision: prices.Revision, Currency: "CNY", Prices: []PlanPriceInput{{PlanID: "starter", Amount: "39.01", Available: true, PlanAvailable: true}}}
	concurrent(func() error { _, err := m.UpdatePlanPrices(ctx, admin, priceUpdate); return err }, ErrPriceConflict)
	for _, action := range []string{"currencies.updated", "plan.prices.updated"} {
		var count int
		if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_events WHERE action=?", action).Scan(&count); err != nil || count != 1 {
			t.Fatal("duplicate audit", action, count, err)
		}
	}
	configureTestCurrency(t, m, admin, "EUR", 2, "2.50")
	config, _ = m.Currencies(ctx, true)
	input = currencyUpdateFrom(config)
	for n := range input.Currencies {
		if input.Currencies[n].Code == "EUR" {
			input.Currencies[n].Enabled = false
		}
	}
	start := make(chan struct{})
	var orderErr, disableErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, _, orderErr = m.CreateOrder(ctx, user, "starter", "racing-sale", "race-disable", "EUR")
	}()
	go func() { defer wg.Done(); <-start; _, disableErr = m.UpdateCurrencies(ctx, admin, input) }()
	close(start)
	wg.Wait()
	var blocked *CurrencyInUse
	if !((orderErr == nil && errors.As(disableErr, &blocked)) || (disableErr == nil && errors.Is(orderErr, ErrCurrencyDisabled))) {
		t.Fatal("disable/new-sale boundary", orderErr, disableErr)
	}
}
