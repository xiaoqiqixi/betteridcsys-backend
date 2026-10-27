package billing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type httpPerfProbeTransport func(*http.Request) (*http.Response, error)

func (f httpPerfProbeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestHTTPPerformanceAdminOrdersProbe(t *testing.T) {
	const seed = "aster-http-performance-v1"
	tests := []struct {
		name       string
		users      int
		orders     int
		mutate     func([]Order) []Order
		customer   bool
		wantReject bool
	}{
		{name: "valid_1000_rows", users: 100, orders: 10000},
		{name: "valid_below_limit", users: 10, orders: 37},
		{name: "missing_row", users: 100, orders: 10000, mutate: func(v []Order) []Order { return v[:len(v)-1] }, wantReject: true},
		{name: "customer_100_rows", users: 100, orders: 10000, mutate: func(v []Order) []Order { return v[:100] }, wantReject: true},
		{name: "unknown_id", users: 100, orders: 10000, mutate: func(v []Order) []Order {
			v[0].ID = httpPerfID(seed, "unknown-order")
			return v
		}, wantReject: true},
		{name: "duplicate_id", users: 100, orders: 10000, mutate: func(v []Order) []Order {
			v[1].ID = v[0].ID
			return v
		}, wantReject: true},
		{name: "wrong_id_order", users: 100, orders: 10000, mutate: func(v []Order) []Order {
			v[0].ID, v[1].ID = v[1].ID, v[0].ID
			return v
		}, wantReject: true},
		{name: "ascending_date", users: 100, orders: 10000, mutate: func(v []Order) []Order {
			v[1].CreatedAt = v[0].CreatedAt.Add(time.Second)
			return v
		}, wantReject: true},
		{name: "non_admin_cookie", users: 100, orders: 10000, customer: true, wantReject: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &httpPerfFixture{customer: httpPerfID(seed, "user-0"), adminID: httpPerfID(seed, fmt.Sprintf("user-%d", tc.users)), cookie: "synthetic-customer-cookie", adminCookie: "synthetic-admin-cookie", seed: seed}
			p := httpPerfParameters{Users: tc.users, Orders: tc.orders}
			orders := make([]Order, min(1000, tc.orders))
			stamp := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
			for index := range orders {
				ordinal := tc.orders - 1 - index
				orders[index] = Order{ID: httpPerfID(seed, fmt.Sprintf("order-%d", ordinal)), UserID: httpPerfID(seed, fmt.Sprintf("user-%d", ordinal%tc.users)), CreatedAt: stamp.Add(time.Duration(ordinal) * time.Second)}
			}
			if tc.mutate != nil {
				orders = tc.mutate(orders)
			}
			body, err := json.Marshal(struct {
				Data []Order `json:"data"`
			}{orders})
			if err != nil {
				t.Fatal(err)
			}
			// Use the actual product encoding: owner IDs are intentionally not
			// exposed by Order JSON, even on the administrator's list endpoint.
			if bytes.Contains(body, []byte(`"user_id"`)) || bytes.Contains(body, []byte(f.customer)) {
				t.Fatal("fixture unexpectedly exposed an order owner")
			}
			requests := 0
			client := &http.Client{Transport: httpPerfProbeTransport(func(r *http.Request) (*http.Response, error) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/admin/orders" {
					t.Fatalf("unexpected preflight request: %s %s", r.Method, r.URL.Path)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
			})}
			cookie := f.adminCookie
			if tc.customer {
				cookie = f.cookie
			}
			err = httpPerfProbe(client, "http://127.0.0.1", "/api/v1/admin/orders", cookie, f, p)
			if (err != nil) != tc.wantReject {
				t.Fatalf("preflight rejected=%v, want %v; error=%v", err != nil, tc.wantReject, err)
			}
			if requests != 1 {
				t.Fatalf("preflight issued %d requests, want exactly one", requests)
			}
		})
	}
}
