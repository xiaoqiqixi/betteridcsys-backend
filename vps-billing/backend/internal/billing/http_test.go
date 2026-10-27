package billing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"sync"
	"testing"
)

type testClient struct {
	t    *testing.T
	base string
	c    *http.Client
	csrf string
}

func (c *testClient) request(method, path string, v any, key string) (int, map[string]any) {
	c.t.Helper()
	var b bytes.Buffer
	if v != nil {
		_ = json.NewEncoder(&b).Encode(v)
	}
	r, _ := http.NewRequest(method, c.base+path, &b)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", c.csrf)
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	res, e := c.c.Do(r)
	if e != nil {
		c.t.Fatal(e)
	}
	defer res.Body.Close()
	var out map[string]any
	if e = json.NewDecoder(res.Body).Decode(&out); e != nil {
		c.t.Fatal(e)
	}
	return res.StatusCode, out
}
func client(t *testing.T, base, email, password string) *testClient {
	jar, _ := cookiejar.New(nil)
	c := &testClient{t: t, base: base, c: &http.Client{Jar: jar}}
	_, v := c.request("GET", "/api/v1/session", nil, "")
	c.csrf = v["data"].(map[string]any)["csrf_token"].(string)
	if email != "" {
		s, v := c.request("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": password}, "")
		if s != 200 {
			t.Fatalf("login %d: %v", s, v)
		}
		c.csrf = v["data"].(map[string]any)["csrf_token"].(string)
	}
	return c
}
func TestSecurityAndOrderLifecycle(t *testing.T) {
	m := NewMemory()
	srv := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()}).Handler())
	defer srv.Close()
	c := client(t, srv.URL, "demo@aster.local", "DemoCustomer!2026")
	other := client(t, srv.URL, "other@aster.local", "DemoOther!2026")
	admin := client(t, srv.URL, "admin@aster.local", "DemoAdmin!2026")
	if s, _ := c.request("GET", "/api/v1/admin/customers", nil, ""); s != 403 {
		t.Fatal(s)
	}
	old := c.csrf
	c.csrf = "bad"
	if s, _ := c.request("POST", "/api/v1/orders", map[string]string{"plan_id": "starter", "hostname": "web-1"}, "first-key"); s != 403 {
		t.Fatal(s)
	}
	c.csrf = old
	if s, _ := c.request("POST", "/api/v1/orders", map[string]any{"plan_id": "starter", "hostname": "web-1", "total_minor": 1}, "first-key"); s != 400 {
		t.Fatal("price tampering", s)
	}
	s, v := c.request("POST", "/api/v1/orders", map[string]string{"plan_id": "starter", "hostname": "web-1"}, "first-key")
	if s != 201 {
		t.Fatalf("order %d %v", s, v)
	}
	id := v["data"].(map[string]any)["id"]
	if v["data"].(map[string]any)["total_minor"] != float64(3900) {
		t.Fatal(v)
	}
	s, v = c.request("POST", "/api/v1/orders", map[string]string{"plan_id": "starter", "hostname": "web-1"}, "first-key")
	if s != 200 || v["data"].(map[string]any)["id"] != id {
		t.Fatal(s, v)
	}
	if s, _ = c.request("POST", "/api/v1/orders", map[string]string{"plan_id": "standard", "hostname": "web-1"}, "first-key"); s != 409 {
		t.Fatal(s)
	}
	_, v = c.request("GET", "/api/v1/invoices", nil, "")
	invoices := v["data"].([]any)
	if len(invoices) != 1 {
		t.Fatal(v)
	}
	iid := invoices[0].(map[string]any)["id"].(string)
	if status, _ := admin.request("GET", "/api/v1/admin/invoices/"+iid, nil, ""); status != 200 {
		t.Fatal("admin invoice detail", status)
	}
	if status, _ := c.request("GET", "/api/v1/admin/invoices/"+iid, nil, ""); status != 403 {
		t.Fatal("customer admin detail", status)
	}
	if status, _ := admin.request("POST", "/api/v1/admin/invoices/"+iid+"/checkout", nil, ""); status != 405 {
		t.Fatal("admin checkout must remain unsupported", status)
	}
	for _, path := range []string{"/api/v1/invoices/" + iid, "/api/v1/invoices/" + iid + "/checkout"} {
		method := "GET"
		if path[len(path)-8:] == "checkout" {
			method = "POST"
		}
		if s, _ := other.request(method, path, nil, ""); s != 404 {
			t.Fatal("ownership", s)
		}
	}
	if s, _ := c.request("POST", "/api/v1/invoices/"+iid+"/checkout", nil, ""); s != 503 {
		t.Fatal(s)
	}
	if s, _ := admin.request("GET", "/api/v1/admin/orders", nil, ""); s != 200 {
		t.Fatal(s)
	}
	if s, _ := c.request("POST", "/api/v1/auth/logout", nil, ""); s != 200 {
		t.Fatal(s)
	}
	if s, _ := c.request("GET", "/api/v1/orders", nil, ""); s != 401 {
		t.Fatal("logout", s)
	}
}
func TestConcurrentIdempotency(t *testing.T) {
	m := NewMemory()
	u, _ := m.UserByEmail(t.Context(), "demo@aster.local")
	var wg sync.WaitGroup
	ids := make(chan string, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, _, e := m.CreateOrder(t.Context(), u, "starter", "concurrent", "same-key", "CNY")
			if e != nil {
				t.Error(e)
			}
			ids <- o.ID
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("duplicate order")
		}
	}
	os, _ := m.Orders(t.Context(), u.ID)
	is, _ := m.Invoices(t.Context(), u.ID)
	if len(os) != 1 || len(is) != 1 {
		t.Fatal(fmt.Sprintf("%d orders %d invoices", len(os), len(is)))
	}
}
func TestOriginRejected(t *testing.T) {
	s := httptest.NewServer(NewApp(NewMemory(), Config{Demo: true, Origin: "https://safe.example", Secret: ID() + ID()}).Handler())
	defer s.Close()
	c := client(t, s.URL, "", "")
	r, _ := http.NewRequest("POST", s.URL+"/api/v1/auth/logout", nil)
	r.Header.Set("X-CSRF-Token", c.csrf)
	r.Header.Set("Origin", "https://evil.example")
	v, e := c.c.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer v.Body.Close()
	if v.StatusCode != 403 {
		t.Fatal(v.StatusCode)
	}
}

func TestProductionCookieAndHost(t *testing.T) {
	app := NewApp(NewMemory(), Config{Origin: "https://billing.example", Secret: ID() + ID(), SecureCookie: true})
	r := httptest.NewRequest("GET", "https://billing.example/api/v1/session", nil)
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "__Host-aster_session" || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Domain != "" || cookies[0].Path != "/" {
		t.Fatal(cookies)
	}
	r = httptest.NewRequest("GET", "https://attacker.example/api/v1/session", nil)
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	if w.Code != 421 {
		t.Fatal(w.Code)
	}
}
func TestDemoRejectsUnknownHost(t *testing.T) {
	app := NewApp(NewMemory(), Config{Demo: true, Origin: "http://localhost:5173", Secret: ID() + ID()})
	r := httptest.NewRequest("GET", "http://attacker.example/api/v1/session", nil)
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	if w.Code != 421 {
		t.Fatal(w.Code)
	}
}
