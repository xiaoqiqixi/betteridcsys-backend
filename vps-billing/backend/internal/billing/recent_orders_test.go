package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"
)

func recentOrderTestRecord(number int, owner string, created time.Time, currency string) Order {
	format := DefaultCurrencyFormat(currency)
	amount := int64(3900)
	switch currency {
	case "USD":
		amount = 600
	case "JPY":
		format, amount = CurrencyFormat{"JPY", "Historical yen", "", " JPY", 0}, 1250
	case "KWD":
		format, amount = CurrencyFormat{"KWD", "Historical dinar", "", " KWD", 3}, 1234
	}
	return Order{
		ID: fmt.Sprintf("%032x", number), Number: fmt.Sprintf("RECENT-%d", number), UserID: owner,
		PlanName: "Historical plan", Status: "pending_payment", TotalMinor: amount, Currency: currency, CurrencyFormat: format,
		CreatedAt: created, Hostname: fmt.Sprintf("history-%d.invalid", number),
		Issuer:       &InvoiceIdentity{Name: "Historical issuer", Address: "Recorded address", SupportEmail: "history@fixture.invalid"},
		PlanSnapshot: &PlanSnapshot{PlanID: "starter", Name: "Recorded plan", CPU: 2, MemoryGB: 4, DiskGB: 80, TransferTB: 2, Region: "Recorded region", BillingCycle: "monthly"},
	}
}

func recentOrderTestIDs(orders []Order) []string {
	ids := make([]string, len(orders))
	for n := range orders {
		ids[n] = orders[n].ID
	}
	return ids
}

func TestRecentOrdersMemoryOwnershipOrderingAndSnapshots(t *testing.T) {
	owner, other := fmt.Sprintf("%032x", 1001), fmt.Sprintf("%032x", 1002)
	stamp := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	m := &Memory{}
	if orders, err := m.RecentOrders(t.Context(), owner); err != nil || orders == nil || len(orders) != 0 {
		t.Fatal("empty owner history must be an empty JSON array", orders, err)
	}
	if _, err := m.RecentOrders(t.Context(), ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("empty owner must not select global history", err)
	}
	legacy := recentOrderTestRecord(1, owner, stamp, "USD")
	legacy.Issuer, legacy.PlanSnapshot = nil, nil
	m.orders = []Order{legacy}
	if orders, err := m.RecentOrders(t.Context(), owner); err != nil || !reflect.DeepEqual(orders, []Order{legacy}) {
		t.Fatal("legacy optional snapshots changed", orders, err)
	}
	// Deliberately neither insertion ordered nor timestamp ordered. The three
	// equal timestamps require an explicit ID tiebreaker across both stores.
	records := []Order{
		recentOrderTestRecord(3, owner, stamp.Add(30*time.Second), "JPY"),
		recentOrderTestRecord(7, owner, stamp.Add(50*time.Second), "KWD"),
		recentOrderTestRecord(99, other, stamp.Add(100*time.Second), "CNY"),
		legacy,
		recentOrderTestRecord(6, owner, stamp.Add(40*time.Second), "USD"),
		recentOrderTestRecord(2, owner, stamp.Add(50*time.Second), "CNY"),
		recentOrderTestRecord(5, owner, stamp.Add(20*time.Second), "USD"),
		recentOrderTestRecord(4, owner, stamp.Add(50*time.Second), "CNY"),
	}
	m.orders = records
	expected := []Order{cloneOrder(records[1]), cloneOrder(records[7]), cloneOrder(records[5]), cloneOrder(records[4]), cloneOrder(records[0])}
	for attempt := 0; attempt < 3; attempt++ {
		orders, err := m.RecentOrders(t.Context(), owner)
		if err != nil || !reflect.DeepEqual(orders, expected) {
			t.Fatal("recent history changed order, ownership, currency, or snapshots", recentOrderTestIDs(orders), err)
		}
		orders[0].Issuer.Name, orders[0].PlanSnapshot.Region = "caller mutation", "caller mutation"
		orders[0].CurrencyFormat.Name, orders[0].TotalMinor = "caller mutation", 1
	}
	if orders, err := m.RecentOrders(t.Context(), other); err != nil || len(orders) != 1 || orders[0].ID != records[2].ID {
		t.Fatal("other owner's independent history", orders, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := m.RecentOrders(ctx, owner); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled recent read was not propagated", err)
	}
}

func TestRecentOrdersMemoryBoundsWithoutChangingOrderList(t *testing.T) {
	owner := fmt.Sprintf("%032x", 1001)
	stamp := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, count := range []int{3, 5, 6, 100} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			m := &Memory{}
			for n := 1; n <= count; n++ {
				number, created := n, stamp.Add(time.Duration(n)*time.Second)
				if count == 6 {
					// Put equal timestamps across the fifth/sixth cutoff, in the
					// opposite insertion order from the required ID ordering.
					number, created = count-n+1, stamp
				}
				m.orders = append(m.orders, recentOrderTestRecord(number, owner, created, "CNY"))
			}
			all, err := m.Orders(t.Context(), owner)
			if err != nil || len(all) != count {
				t.Fatal("existing order list was truncated", len(all), err)
			}
			recent, err := m.RecentOrders(t.Context(), owner)
			if err != nil || len(recent) != min(count, 5) {
				t.Fatal("overview read exceeded its fixed bound", len(recent), err)
			}
			for n, order := range recent {
				if order.ID != fmt.Sprintf("%032x", count-n) {
					t.Fatal("incorrect newest order", recentOrderTestIDs(recent))
				}
			}
		})
	}
}

type recentOrdersHTTPStore struct {
	Store
	mu             sync.Mutex
	orders, recent int
	owners         []string
	failure        error
}

func (s *recentOrdersHTTPStore) Orders(ctx context.Context, uid string) ([]Order, error) {
	s.mu.Lock()
	s.orders++
	s.mu.Unlock()
	return s.Store.Orders(ctx, uid)
}

func (s *recentOrdersHTTPStore) RecentOrders(ctx context.Context, uid string) ([]Order, error) {
	s.mu.Lock()
	s.recent++
	s.owners = append(s.owners, uid)
	err := s.failure
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.Store.RecentOrders(ctx, uid)
}

func TestRecentOrdersHTTPOverviewScopeAndFailure(t *testing.T) {
	m := NewMemory()
	owner, other, admin := cancellationUsers(t, m)
	configureTestCurrency(t, m, admin, "USD", 2, "6.00")
	configureTestCurrency(t, m, admin, "JPY", 0, "1250")
	configureTestCurrency(t, m, admin, "KWD", 3, "1.234")
	stamp := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for n := 1; n <= 7; n++ {
		currency := []string{"CNY", "USD", "JPY", "KWD"}[n%4]
		m.orders = append(m.orders, recentOrderTestRecord(n, owner.ID, stamp.Add(time.Duration(n)*time.Second), currency))
	}
	m.orders = append(m.orders, recentOrderTestRecord(99, other.ID, stamp.Add(time.Hour), "CNY"))
	m.invoices = []Invoice{
		{UserID: owner.ID, Status: "unpaid", Currency: "CNY", AmountMinor: 1234},
		{UserID: owner.ID, Status: "unpaid", Currency: "CNY", AmountMinor: 3456},
		{UserID: owner.ID, Status: "paid", Currency: "CNY", AmountMinor: 5000},
		{UserID: owner.ID, Status: "unpaid", Currency: "USD", AmountMinor: 600},
		{UserID: other.ID, Status: "unpaid", Currency: "CNY", AmountMinor: 999999},
	}
	spy := &recentOrdersHTTPStore{Store: m}
	server := httptest.NewServer(NewApp(spy, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()}).Handler())
	defer server.Close()
	tokens := map[string]string{owner.ID: ID() + ID(), admin.ID: ID() + ID()}
	for uid, token := range tokens {
		if err := m.PutSession(t.Context(), Session{Hash: digest(token), UserID: uid, Expires: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	get := func(path, token string) (int, []byte) {
		t.Helper()
		request, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
		if token != "" {
			request.AddCookie(&http.Cookie{Name: "aster_demo_session", Value: token})
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, raw
	}
	var before []Order
	for _, currency := range []string{"CNY", "USD"} {
		code, raw := get("/api/v1/overview?currency="+currency, tokens[owner.ID])
		var body struct {
			Data struct {
				Currency   string  `json:"currency"`
				Scope      string  `json:"recent_orders_scope"`
				CountScope string  `json:"count_scope"`
				Orders     []Order `json:"recent_orders"`
				Pending    int     `json:"pending_invoices"`
				Unpaid     int64   `json:"unpaid_total_minor"`
			} `json:"data"`
		}
		if code != 200 || json.Unmarshal(raw, &body) != nil {
			t.Fatal("overview failed", code)
		}
		data := body.Data
		wantPending, wantUnpaid := 2, int64(4690)
		if currency == "USD" {
			wantPending, wantUnpaid = 1, 600
		}
		if data.Currency != currency || data.Scope != "all" || data.CountScope != "currency" || data.Pending != wantPending || data.Unpaid != wantUnpaid || len(data.Orders) != 5 {
			t.Fatal("overview currency aggregation or recent-order scope changed", data)
		}
		for n, order := range data.Orders {
			// UserID is intentionally private in JSON; exact IDs and all public
			// recorded fields must match the owner's existing immutable records.
			expected := cloneOrder(m.orders[6-n])
			expected.UserID = ""
			if !reflect.DeepEqual(order, expected) {
				t.Fatal("foreign or rewritten recent order", order.ID)
			}
		}
		if before != nil && !reflect.DeepEqual(before, data.Orders) {
			t.Fatal("display currency filtered recent history")
		}
		before = data.Orders
	}
	spy.mu.Lock()
	if spy.orders != 0 || spy.recent != 2 || !reflect.DeepEqual(spy.owners, []string{owner.ID, owner.ID}) {
		t.Fatal("overview called the full order list", spy.orders, spy.recent)
	}
	spy.mu.Unlock()
	if code, _ := get("/api/v1/admin/overview?currency=CNY", tokens[admin.ID]); code != 200 {
		t.Fatal("admin overview failed", code)
	}
	if code, _ := get("/api/v1/overview?currency=CNY", ""); code != 401 {
		t.Fatal("unauthenticated overview", code)
	}
	if code, _ := get("/api/v1/admin/overview?currency=CNY", tokens[owner.ID]); code != 403 {
		t.Fatal("customer admin overview", code)
	}
	spy.mu.Lock()
	if spy.orders != 0 || spy.recent != 2 {
		t.Fatal("admin or denied overview read recent orders")
	}
	spy.failure = errors.New("synthetic recent-order storage failure")
	spy.mu.Unlock()
	code, raw := get("/api/v1/overview?currency=CNY", tokens[owner.ID])
	var failure struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if code != 500 || json.Unmarshal(raw, &failure) != nil || failure.Error.Code != "INTERNAL_ERROR" {
		t.Fatal("recent-order failure was not propagated", code)
	}
	code, raw = get("/api/v1/orders", tokens[owner.ID])
	var list struct {
		Data []Order `json:"data"`
	}
	if code != 200 || json.Unmarshal(raw, &list) != nil || len(list.Data) != 7 {
		t.Fatal("original list changed", code, len(list.Data))
	}
}
