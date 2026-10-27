package billing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func currencyUpdateFrom(c CurrencyConfiguration) CurrencyUpdate {
	out := CurrencyUpdate{Revision: c.Revision, DefaultCurrency: c.DefaultCurrency, Currencies: []CurrencyInput{}}
	for _, v := range c.Currencies {
		out.Currencies = append(out.Currencies, CurrencyInput{v.Code, v.Name, v.Prefix, v.Suffix, v.DecimalPlaces, v.Enabled, v.SortOrder})
	}
	return out
}
func configureTestCurrency(t *testing.T, s Store, actor User, code string, decimals int, amount string) {
	t.Helper()
	c, err := s.Currencies(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.find(code); !ok {
		input := currencyUpdateFrom(c)
		input.Currencies = append(input.Currencies, CurrencyInput{Code: code, Name: code + " fixture", DecimalPlaces: decimals, Enabled: true, SortOrder: 1})
		if _, err = s.UpdateCurrencies(t.Context(), actor, input); err != nil {
			t.Fatal(err)
		}
	}
	if amount != "" {
		p, err := s.PlanPrices(t.Context(), code)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.UpdatePlanPrices(t.Context(), actor, PlanPricesUpdate{Revision: p.Revision, Currency: code, Prices: []PlanPriceInput{{PlanID: "starter", Amount: amount, Available: true, PlanAvailable: true}}}); err != nil {
			t.Fatal(err)
		}
	}
}
func legacyMemoryFixture(t *testing.T, m *Memory) {
	t.Helper()
	admin, err := m.UserByEmail(t.Context(), "admin@aster.local")
	if err != nil {
		t.Fatal(err)
	}
	configureTestCurrency(t, m, admin, "USD", 2, "6.00")
}

func TestParsePriceExactPrecision(t *testing.T) {
	for _, tc := range []struct {
		value string
		scale int
		want  int64
	}{{"1", 0, 1}, {"123", 0, 123}, {"0.01", 2, 1}, {"12.3", 2, 1230}, {"0.001", 3, 1}, {"42.123", 3, 42123}, {"0.000001", 6, 1}, {"9007199254740991", 0, MaxMinor}, {"90071992547409.91", 2, MaxMinor}, {"9007199254740.991", 3, MaxMinor}, {"9007199254.740991", 6, MaxMinor}} {
		if got, err := ParsePrice(tc.value, tc.scale); err != nil || got != tc.want {
			t.Fatalf("%+v got %d %v", tc, got, err)
		}
		if got, err := ParsePrice(priceString(tc.want, tc.scale), tc.scale); err != nil || got != tc.want {
			t.Fatal("roundtrip", tc, got, err)
		}
	}
	for _, tc := range []struct {
		value string
		scale int
	}{{"1.0", 0}, {"1.001", 2}, {"1.0001", 3}, {"1e2", 2}, {"1E2", 2}, {"+1", 2}, {"-1", 2}, {" 1", 2}, {"1 ", 2}, {"01", 2}, {".01", 2}, {"1.", 2}, {"NaN", 2}, {"∞", 2}, {"１", 2}, {"0", 0}, {"0.00", 2}, {"90071992547409.92", 2}, {"9223372036854775808", 0}, {"1", 7}, {"1", -1}} {
		if _, err := ParsePrice(tc.value, tc.scale); !errors.Is(err, ErrPlanPrices) {
			t.Fatal("accepted nonexact", tc, err)
		}
	}
}
func TestCurrencyRegistryFreshAndPrecisionLifecycle(t *testing.T) {
	m := NewMemory()
	admin, _ := m.UserByEmail(t.Context(), "admin@aster.local")
	user, _ := m.UserByEmail(t.Context(), "demo@aster.local")
	config, err := m.Currencies(t.Context(), true)
	if err != nil || len(config.Currencies) != 1 || config.DefaultCurrency != "CNY" || len(m.prices["starter"]) != 1 {
		t.Fatal(config, err)
	}
	if _, _, err = m.CreateOrder(t.Context(), user, "starter", "test", "unknown-usd", "USD"); !errors.Is(err, ErrCurrency) {
		t.Fatal(err)
	}
	configureTestCurrency(t, m, admin, "JPY", 0, "1250")
	configureTestCurrency(t, m, admin, "KWD", 3, "1.234")
	jpy, _, err := m.CreateOrder(t.Context(), user, "starter", "jpy", "dynamic-jpy", "JPY")
	if err != nil || jpy.TotalMinor != 1250 || jpy.CurrencyFormat.DecimalPlaces != 0 || jpy.PlanSnapshot == nil || jpy.PlanSnapshot.BillingCycle != "monthly" {
		t.Fatal(jpy, err)
	}
	kwd, _, err := m.CreateOrder(t.Context(), user, "starter", "kwd", "dynamic-kwd", "KWD")
	if err != nil || kwd.TotalMinor != 1234 || kwd.CurrencyFormat.DecimalPlaces != 3 {
		t.Fatal(kwd, err)
	}
	config, _ = m.Currencies(t.Context(), true)
	input := currencyUpdateFrom(config)
	for n := range input.Currencies {
		if input.Currencies[n].Code == "JPY" {
			input.Currencies[n].Name = "Changed name"
			input.Currencies[n].Prefix = "JP "
		}
	}
	if _, err = m.UpdateCurrencies(t.Context(), admin, input); err != nil {
		t.Fatal(err)
	}
	replayed, replay, err := m.CreateOrder(t.Context(), user, "starter", "jpy", "dynamic-jpy", "JPY")
	if err != nil || !replay || replayed.CurrencyFormat != jpy.CurrencyFormat {
		t.Fatal(replayed, replay, err)
	}
	config, _ = m.Currencies(t.Context(), true)
	input = currencyUpdateFrom(config)
	for n := range input.Currencies {
		if input.Currencies[n].Code == "JPY" {
			input.Currencies[n].DecimalPlaces = 2
		}
	}
	if _, err = m.UpdateCurrencies(t.Context(), admin, input); !errors.Is(err, ErrCurrencyPrecision) {
		t.Fatal(err)
	}
	input = currencyUpdateFrom(config)
	for n := range input.Currencies {
		if input.Currencies[n].Code == "JPY" {
			input.Currencies[n].Enabled = false
		}
	}
	_, err = m.UpdateCurrencies(t.Context(), admin, input)
	var blocked *CurrencyInUse
	if !errors.As(err, &blocked) || blocked.Usage.PendingOrders != 1 || blocked.Usage.UnpaidInvoices != 1 {
		t.Fatal(err)
	}
	m.mu.Lock()
	for n := range m.orders {
		if m.orders[n].Currency == "JPY" {
			m.orders[n].Status = "cancelled"
		}
	}
	for n := range m.invoices {
		if m.invoices[n].Currency == "JPY" {
			m.invoices[n].Status = "void"
		}
	}
	m.services = append(m.services, Service{Currency: "JPY", CurrencyFormat: jpy.CurrencyFormat, Status: "suspended", PriceMinor: 1250, UserID: user.ID})
	m.mu.Unlock()
	_, err = m.UpdateCurrencies(t.Context(), admin, input)
	if !errors.As(err, &blocked) || blocked.Usage.OngoingServices != 1 {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.services[0].Status = "terminated"
	m.mu.Unlock()
	if _, err = m.UpdateCurrencies(t.Context(), admin, input); err != nil {
		t.Fatal(err)
	}
	if _, _, err = m.CreateOrder(t.Context(), user, "starter", "new", "disabled-jpy", "JPY"); !errors.Is(err, ErrCurrencyDisabled) {
		t.Fatal(err)
	}
	if got, replay, err := m.CreateOrder(t.Context(), user, "starter", "jpy", "dynamic-jpy", "JPY"); err != nil || !replay || got.ID != jpy.ID || got.CurrencyFormat != jpy.CurrencyFormat {
		t.Fatal(got, replay, err)
	}
	if plans, err := m.Plans(t.Context(), "JPY"); err != nil || plans[0].Available {
		t.Fatal(plans, err)
	}
	if _, err := m.Summary(t.Context(), user.ID, "JPY"); err != nil {
		t.Fatal("disabled history summary", err)
	}
	invoices, _ := m.Invoices(t.Context(), user.ID)
	found := false
	for _, v := range invoices {
		if v.OrderID == jpy.ID {
			found = true
			if v.CurrencyFormat != jpy.CurrencyFormat {
				t.Fatal(v)
			}
		}
	}
	if !found {
		t.Fatal("missing history")
	}
}
func TestCurrencyRegistryDefaultNoDeletionAndMissingPrice(t *testing.T) {
	m := NewMemory()
	admin, _ := m.UserByEmail(t.Context(), "admin@aster.local")
	user, _ := m.UserByEmail(t.Context(), "demo@aster.local")
	configureTestCurrency(t, m, admin, "EUR", 2, "")
	config, _ := m.Currencies(t.Context(), true)
	input := currencyUpdateFrom(config)
	input.DefaultCurrency = "EUR"
	if _, err := m.UpdateCurrencies(t.Context(), admin, input); err != nil {
		t.Fatal(err)
	}
	settings, _ := m.Settings(t.Context())
	if settings.DefaultCurrency != "EUR" || settings.Revision != 2 {
		t.Fatal(settings)
	}
	if _, _, err := m.CreateOrder(t.Context(), user, "starter", "test", "missing-price", ""); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	config, _ = m.Currencies(t.Context(), true)
	input = currencyUpdateFrom(config)
	for n := range input.Currencies {
		if input.Currencies[n].Code == "EUR" {
			input.Currencies[n].Enabled = false
		}
	}
	if _, err := m.UpdateCurrencies(t.Context(), admin, input); !errors.Is(err, ErrCurrencyConfig) {
		t.Fatal("disabled default", err)
	}
	input = currencyUpdateFrom(config)
	input.Currencies = input.Currencies[:1]
	if _, err := m.UpdateCurrencies(t.Context(), admin, input); !errors.Is(err, ErrCurrencyConfig) {
		t.Fatal("deleted currency", err)
	}
	settings.DefaultCurrency = "CNY"
	if _, err := m.UpdateSettings(t.Context(), admin, settings); err != nil {
		t.Fatal(err)
	}
	config, _ = m.Currencies(t.Context(), true)
	if config.DefaultCurrency != "CNY" {
		t.Fatal(config)
	}
}
func TestCurrencyAndPriceConcurrentRevision(t *testing.T) {
	m := NewMemory()
	admin, _ := m.UserByEmail(t.Context(), "admin@aster.local")
	config, _ := m.Currencies(t.Context(), true)
	input := currencyUpdateFrom(config)
	input.Currencies[0].Name = "Concurrent name"
	run := func(fn func() error, want error) {
		t.Helper()
		var wg sync.WaitGroup
		results := make(chan error, 2)
		for range 2 {
			wg.Add(1)
			go func() { defer wg.Done(); results <- fn() }()
		}
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
	run(func() error { _, err := m.UpdateCurrencies(context.Background(), admin, input); return err }, ErrCurrencyConflict)
	prices, _ := m.PlanPrices(t.Context(), "CNY")
	update := PlanPricesUpdate{Revision: prices.Revision, Currency: "CNY", Prices: []PlanPriceInput{{PlanID: "starter", Amount: "40.01", Available: true, PlanAvailable: true}}}
	run(func() error { _, err := m.UpdatePlanPrices(context.Background(), admin, update); return err }, ErrPriceConflict)
	if len(m.audits) != 2 {
		t.Fatal("duplicate audit", m.audits)
	}
	before, _ := m.PlanPrices(t.Context(), "CNY")
	update.Revision = before.Revision
	update.Prices = append(update.Prices, PlanPriceInput{PlanID: "nonexistent", Amount: "2.00", Available: true, PlanAvailable: true})
	if _, err := m.UpdatePlanPrices(t.Context(), admin, update); !errors.Is(err, ErrPlanPrices) {
		t.Fatal(err)
	}
	after, _ := m.PlanPrices(t.Context(), "CNY")
	left, _ := json.Marshal(before)
	right, _ := json.Marshal(after)
	if string(left) != string(right) || len(m.audits) != 2 {
		t.Fatal("partial batch committed")
	}
}
func TestCurrencyManagementHTTPGuardsAndStrictJSON(t *testing.T) {
	m := NewMemory()
	server := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()}).Handler())
	defer server.Close()
	admin := client(t, server.URL, "admin@aster.local", "DemoAdmin!2026")
	customer := client(t, server.URL, "demo@aster.local", "DemoCustomer!2026")
	anon := client(t, server.URL, "", "")
	for _, path := range []string{"/admin/currencies", "/admin/plan-prices"} {
		for _, tc := range []struct {
			c    *testClient
			want int
		}{{anon, 401}, {customer, 403}, {admin, 200}} {
			if status, _ := tc.c.request("GET", "/api/v1"+path, nil, ""); status != tc.want {
				t.Fatal(path, status)
			}
		}
	}
	status, out := anon.request("GET", "/api/v1/currencies", nil, "")
	if status != 200 || strings.Contains(string(mustJSON(t, out)), "usage") {
		t.Fatal(status, out)
	}
	config, _ := m.Currencies(t.Context(), true)
	input := currencyUpdateFrom(config)
	token := admin.csrf
	admin.csrf = "bad"
	if status, _ := admin.request("POST", "/api/v1/admin/currencies", input, ""); status != 403 {
		t.Fatal(status)
	}
	admin.csrf = token
	good := string(mustJSON(t, input))
	for _, raw := range []string{strings.Replace(good, `"enabled":true`, `"enabled":true,"Enabled":false`, 1), strings.Replace(good, `"suffix":""`, `"suffix":"","ſuffix":"x"`, 1), strings.Replace(good, `"enabled":true`, `"enabled":true,"enabled":false`, 1), strings.Replace(good, `"enabled":true,`, "", 1), strings.Replace(good, `"enabled":true`, `"enabled":null`, 1)} {
		request := httptest.NewRequest("POST", "http://localhost/", strings.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		var decoded CurrencyUpdate
		if financeBody(rec, request, &decoded) || rec.Code != 400 {
			t.Fatal("ambiguous JSON accepted", raw, rec.Code)
		}
	}
	if status, _ := admin.request("POST", "/api/v1/admin/currencies", input, ""); status != 200 {
		t.Fatal(status)
	}
}
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPlanSnapshotDetachedFromCurrentCatalogAndReturnedData(t *testing.T) {
	m := NewMemory()
	user, _ := m.UserByEmail(t.Context(), "demo@aster.local")
	order, _, err := m.CreateOrder(t.Context(), user, "starter", "snapshot-test", "snapshot-key", "")
	if err != nil || order.PlanSnapshot == nil {
		t.Fatal(order, err)
	}
	original := *order.PlanSnapshot
	order.PlanSnapshot.Name = "caller mutation"
	m.mu.Lock()
	m.plans[0].Name = "new catalog"
	m.plans[0].CPU = 9
	m.mu.Unlock()
	replay, _, err := m.CreateOrder(t.Context(), user, "starter", "snapshot-test", "snapshot-key", "")
	if err != nil || replay.PlanSnapshot == nil || *replay.PlanSnapshot != original {
		t.Fatal(replay, err)
	}
	orders, _ := m.Orders(t.Context(), user.ID)
	orders[0].PlanSnapshot.Region = "caller mutation"
	orders, _ = m.Orders(t.Context(), user.ID)
	if *orders[0].PlanSnapshot != original {
		t.Fatal(orders)
	}
}

func TestCurrencyCannotInventPrecisionForUnregisteredStoredData(t *testing.T) {
	old := DefaultCurrencyConfiguration()
	input := currencyUpdateFrom(old)
	input.Currencies = append(input.Currencies, CurrencyInput{Code: "EUR", Name: "Euro", DecimalPlaces: 3, Enabled: true})
	if _, err := applyCurrencyUpdate(old, input, map[string]CurrencyUsage{"EUR": {Invoices: 1}}); !errors.Is(err, ErrCurrencyConfig) {
		t.Fatal("invented precision for unregistered history", err)
	}
}

func TestCurrencySnapshotRequiresExplicitPrecision(t *testing.T) {
	raw := []byte(`{"code":"JPY","name":"Japanese yen","prefix":"","suffix":"","decimal_places":0}`)
	if got, err := decodeCurrencySnapshot(raw, "JPY"); err != nil || got.DecimalPlaces != 0 {
		t.Fatal(got, err)
	}
	for _, bad := range []string{`{"code":"JPY","name":"Japanese yen","prefix":"","suffix":""}`, `{"code":"JPY","name":"Japanese yen","prefix":"","suffix":"","decimal_places":null}`, `{"code":"JPY","name":"Japanese yen","prefix":"","suffix":"","decimal_places":0,"extra":true}`, `{"code":"JPY","name":"Japanese yen","prefix":"","suffix":"","Decimal_places":0}`, `{"code":"JPY","name":"Japanese yen","prefix":"","suffix":"","decimal_places":7}`} {
		if _, err := decodeCurrencySnapshot([]byte(bad), "JPY"); err == nil {
			t.Fatal("malformed historical precision", bad)
		}
	}
	if _, err := decodeCurrencySnapshot(raw, "CNY"); err == nil {
		t.Fatal("mismatched currency snapshot")
	}
}

func TestCurrencyStoredRegistryRequiresExplicitFields(t *testing.T) {
	config := CurrencyConfiguration{Revision: 1, DefaultCurrency: "JPY", Currencies: []Currency{{CurrencyFormat: CurrencyFormat{Code: "JPY", Name: "Japanese yen", DecimalPlaces: 0}, Enabled: true}}}
	raw, err := json.Marshal(cloneCurrencies(config, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := decodeStoredCurrencies(raw); err != nil || len(got.Currencies) != 1 || got.Currencies[0].DecimalPlaces != 0 || got.Currencies[0].PrecisionLocked {
		t.Fatal("explicit zero precision and false lock must round trip", got, err)
	}
	assertInvalid := func(t *testing.T, mutate func(map[string]any, map[string]any)) {
		t.Helper()
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		currency := fields["currencies"].([]any)[0].(map[string]any)
		mutate(fields, currency)
		changed, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = decodeStoredCurrencies(changed); err == nil {
			t.Fatal("accepted incomplete or ambiguous stored registry", string(changed))
		}
	}
	for _, key := range []string{"revision", "default_currency", "currencies"} {
		t.Run("missing_top_"+key, func(t *testing.T) {
			assertInvalid(t, func(fields, _ map[string]any) { delete(fields, key) })
		})
		t.Run("null_top_"+key, func(t *testing.T) {
			assertInvalid(t, func(fields, _ map[string]any) { fields[key] = nil })
		})
	}
	for _, key := range []string{"code", "name", "prefix", "suffix", "decimal_places", "enabled", "sort_order", "precision_locked"} {
		t.Run("missing_currency_"+key, func(t *testing.T) {
			assertInvalid(t, func(_, currency map[string]any) { delete(currency, key) })
		})
		t.Run("null_currency_"+key, func(t *testing.T) {
			assertInvalid(t, func(_, currency map[string]any) { currency[key] = nil })
		})
	}
	for name, mutate := range map[string]func(map[string]any, map[string]any){
		"unknown_top":       func(fields, _ map[string]any) { fields["extra"] = true },
		"unknown_currency":  func(_, currency map[string]any) { currency["extra"] = true },
		"stored_usage":      func(_, currency map[string]any) { currency["usage"] = map[string]any{} },
		"null_stored_usage": func(_, currency map[string]any) { currency["usage"] = nil },
		"case_alias":        func(_, currency map[string]any) { delete(currency, "decimal_places"); currency["Decimal_places"] = 0 },
		"unicode_alias":     func(_, currency map[string]any) { delete(currency, "decimal_places"); currency["decimal_placeſ"] = 0 },
		"wrong_type":        func(_, currency map[string]any) { currency["decimal_places"] = "0" },
		"invalid_precision": func(_, currency map[string]any) { currency["decimal_places"] = 7 },
	} {
		t.Run(name, func(t *testing.T) { assertInvalid(t, mutate) })
	}
}
