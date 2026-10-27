package billing

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCurrencyPricingAndImmutableReplay(t *testing.T) {
	m := NewMemory()
	legacyMemoryFixture(t, m)
	u, _ := m.UserByEmail(t.Context(), "demo@aster.local")
	admin, _ := m.UserByEmail(t.Context(), "admin@aster.local")
	cny, _, err := m.CreateOrder(t.Context(), u, "starter", "test", "default-key", "")
	if err != nil || cny.Currency != "CNY" || cny.TotalMinor != 3900 {
		t.Fatal(cny, err)
	}
	usd, _, err := m.CreateOrder(t.Context(), u, "starter", "test", "usd-key", "USD")
	if err != nil || usd.Currency != "USD" || usd.TotalMinor != 600 {
		t.Fatal(usd, err)
	}
	if _, _, err = m.CreateOrder(t.Context(), u, "starter", "test", "usd-key", "CNY"); err != ErrConflict {
		t.Fatal("currency replay", err)
	}
	settings, _ := m.Settings(t.Context())
	originalIssuer := settings.InvoiceIssuer
	settings.DefaultCurrency, settings.InvoiceIssuer = "USD", "Changed company"
	if _, err = m.UpdateSettings(t.Context(), admin, settings); err != nil {
		t.Fatal(err)
	}
	m.prices["starter"]["CNY"] = PlanPrice{4500, true}
	cny.Issuer.Name = "attempted returned snapshot mutation"
	replayed, replay, err := m.CreateOrder(t.Context(), u, "starter", "test", "default-key", "")
	if err != nil || !replay || replayed.Currency != "CNY" || replayed.TotalMinor != 3900 || replayed.Issuer.Name != originalIssuer {
		t.Fatal(replayed, replay, err)
	}
	fresh, _, err := m.CreateOrder(t.Context(), u, "starter", "test", "new-default", "")
	if err != nil || fresh.Currency != "USD" || fresh.Issuer.Name != "Changed company" {
		t.Fatal(fresh, err)
	}
	orders, _ := m.Orders(t.Context(), u.ID)
	orders[0].Issuer.Name = "mutated"
	invoices, _ := m.Invoices(t.Context(), u.ID)
	invoices[0].Issuer.Name = "mutated"
	invoices, _ = m.Invoices(t.Context(), u.ID)
	orders, _ = m.Orders(t.Context(), u.ID)
	if orders[0].Issuer.Name != "Changed company" || invoices[0].Issuer.Name != "Changed company" {
		t.Fatal("mutable returned identity")
	}
	delete(m.prices["starter"], "CNY")
	plans, err := m.Plans(t.Context(), "CNY")
	if err != nil || plans[0].Available || plans[0].PriceMinor != 0 || plans[0].Currency != "CNY" {
		t.Fatal("implicit price fallback", plans, err)
	}
	if _, _, err = m.CreateOrder(t.Context(), u, "starter", "test", "missing-price", "CNY"); err != ErrUnavailable {
		t.Fatal(err)
	}
	for _, currency := range []string{"EUR", "usd", "", "JPY"} {
		if _, err = m.Plans(t.Context(), currency); err != ErrCurrency {
			t.Fatal("invalid catalog currency", currency, err)
		}
	}
}

func TestCurrencyScopedSummaryAndExactAmounts(t *testing.T) {
	m := NewMemory()
	legacyMemoryFixture(t, m)
	u, _ := m.UserByEmail(t.Context(), "demo@aster.local")
	m.services = []Service{{UserID: u.ID, Status: "active", PriceMinor: 3900, Currency: "CNY"}, {UserID: u.ID, Status: "active", PriceMinor: 600, Currency: "USD"}}
	m.invoices = []Invoice{{UserID: u.ID, Status: "paid", AmountMinor: 1234, Currency: "CNY"}, {UserID: u.ID, Status: "paid", AmountMinor: 567, Currency: "USD"}, {UserID: u.ID, Status: "unpaid", AmountMinor: 100, Currency: "USD"}}
	for _, tc := range []struct {
		currency         string
		monthly, revenue int64
		pending          int
	}{{"CNY", 3900, 1234, 0}, {"USD", 600, 567, 1}} {
		s, err := m.Summary(t.Context(), u.ID, tc.currency)
		if err != nil || s.TotalServices != 1 || s.ActiveServices != 1 || s.MonthlyTotal != tc.monthly || s.Revenue != tc.revenue || s.PendingInvoices != tc.pending || s.UnpaidTotal != int64(tc.pending)*100 {
			t.Fatal(s, err)
		}
	}
	m.services = append(m.services, Service{UserID: u.ID, Status: "active", PriceMinor: MaxMinor, Currency: "USD"})
	if _, err := m.Summary(t.Context(), u.ID, "USD"); err == nil {
		t.Fatal("unsafe integer aggregate")
	}
	for _, amount := range []int64{-1, 0, MaxMinor + 1} {
		m.prices["starter"]["CNY"] = PlanPrice{amount, true}
		if _, _, err := m.CreateOrder(t.Context(), u, "starter", "test", "bad-amount", "CNY"); err != ErrUnavailable {
			t.Fatal("unsafe price", amount, err)
		}
	}
}

func TestCurrencyHTTPValidationAndLocaleIndependence(t *testing.T) {
	m := NewMemory()
	legacyMemoryFixture(t, m)
	app := NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()})
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	c := client(t, srv.URL, "demo@aster.local", "DemoCustomer!2026")
	for _, path := range []string{"/plans?currency=EUR", "/plans?currency=", "/plans?currency=CNY&currency=USD", "/overview?currency=JPY", "/plans?currency=%ZZ"} {
		if status, _ := c.request("GET", "/api/v1"+path, nil, ""); status != 400 {
			t.Fatal(path, status)
		}
	}
	for _, currency := range []any{"EUR", "usd", "", nil, 123} {
		if status, _ := c.request("POST", "/api/v1/orders", map[string]any{"plan_id": "starter", "hostname": "test", "currency": currency}, "invalid-currency"); status != 400 {
			t.Fatal(currency, status)
		}
	}
	for _, currency := range []string{"CNY", "USD"} {
		if status, data := c.request("POST", "/api/v1/orders", map[string]any{"plan_id": "starter", "hostname": "test", "currency": currency}, "money-"+currency); status != 201 || data["data"].(map[string]any)["currency"] != currency {
			t.Fatal(status, data)
		}
	}
	r := httptest.NewRequest("GET", "http://localhost/api/v1/plans", nil)
	r.Header.Set("Accept-Language", "en")
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	var catalog struct{ Data []Plan }
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || catalog.Data[0].Currency != "CNY" {
		t.Fatal("language changed currency", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("GET", "http://localhost/api/v1/plans?currency=EUR", nil)
	r.Header.Set("Accept-Language", "en")
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	if w.Code != 400 || w.Header().Get("Content-Language") != "en" || !strings.Contains(w.Body.String(), "has not been configured") || !strings.Contains(w.Body.String(), "UNSUPPORTED_CURRENCY") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestLocaleFallback(t *testing.T) {
	for _, tc := range []struct{ header, want string }{{"", "zh-CN"}, {"fr", "zh-CN"}, {"en-US", "en"}, {"en;q=0,zh-CN;q=1", "zh-CN"}, {"zh-CN;q=0.2,en;q=0.8", "en"}, {"en;q=NaN", "zh-CN"}, {"en;q=2", "zh-CN"}, {"en;q=-1", "zh-CN"}} {
		if got := requestLocale(tc.header); got != tc.want {
			t.Fatal(tc, got)
		}
	}
	legacy := digest(`{"plan_id":"starter","hostname":"test"}`)
	if orderFingerprint("starter", "test", "") != legacy {
		t.Fatal("legacy idempotency fingerprint changed")
	}
}
