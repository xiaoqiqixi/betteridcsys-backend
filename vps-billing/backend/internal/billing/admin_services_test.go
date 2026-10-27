package billing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type adminServicesStoreSpy struct {
	Store
	mu     sync.Mutex
	owners []string
	err    error
}

func (s *adminServicesStoreSpy) Services(ctx context.Context, owner string) ([]Service, error) {
	s.mu.Lock()
	s.owners = append(s.owners, owner)
	s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return s.Store.Services(ctx, owner)
}

func (s *adminServicesStoreSpy) assertOwners(t *testing.T, want ...string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.owners) != len(want) || (len(want) > 0 && !reflect.DeepEqual(s.owners, want)) {
		t.Fatalf("Services owner filters = %q, want %q", s.owners, want)
	}
}

func serviceWireFields(t *testing.T, service Service) map[string]any {
	t.Helper()
	raw, err := json.Marshal(service)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

func TestAdminServicesAuthorizationOwnershipAndProjection(t *testing.T) {
	m := NewMemory()
	owner, _ := m.UserByEmail(t.Context(), "demo@aster.local")
	other, _ := m.UserByEmail(t.Context(), "other@aster.local")
	admin, _ := m.UserByEmail(t.Context(), "admin@aster.local")
	fixture := []Service{}
	for index, userID := range []string{owner.ID, other.ID, admin.ID, ID()} {
		format := DefaultCurrencyFormat("CNY")
		format.Name, format.Prefix = "历史币种名称", "旧¥"
		fixture = append(fixture, Service{ID: ID(), UserID: userID, Name: []string{"alpha", "beta", "admin-own", "unknown-owner"}[index],
			PlanName: "Recorded plan", Region: "Recorded region", IPv4: "192.0.2.1", Status: "active",
			NextDueAt: time.Date(2026, 11, index+1, 0, 0, 0, 0, time.UTC), PriceMinor: MaxMinor,
			Currency: "CNY", CurrencyFormat: format, CPU: index + 1, MemoryGB: 4, DiskGB: 80})
	}
	m.services = append([]Service{}, fixture...)
	store := &adminServicesStoreSpy{Store: m}
	srv := httptest.NewServer(NewApp(store, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()}).Handler())
	defer srv.Close()
	anonymous := client(t, srv.URL, "", "")
	customer := client(t, srv.URL, owner.Email, "DemoCustomer!2026")
	administrator := client(t, srv.URL, admin.Email, "DemoAdmin!2026")
	for _, check := range []struct {
		client *testClient
		path   string
		status int
	}{
		{anonymous, "/api/v1/admin/services", http.StatusUnauthorized},
		{customer, "/api/v1/admin/services", http.StatusForbidden},
		{customer, "/api/v1/admin/services?role=admin&customer_id=" + other.ID, http.StatusForbidden},
	} {
		if status, body := check.client.request("GET", check.path, nil, ""); status != check.status {
			t.Fatal(status, body)
		}
	}
	store.assertOwners(t)
	status, body := administrator.request("GET", "/api/v1/admin/services", nil, "")
	if status != http.StatusOK {
		t.Fatal(status, body)
	}
	rows := body["data"].([]any)
	if len(rows) != len(fixture) {
		t.Fatalf("services = %d, want %d", len(rows), len(fixture))
	}
	wantByID := map[string]Service{}
	for _, service := range fixture {
		wantByID[service.ID] = service
	}
	for _, value := range rows {
		row := value.(map[string]any)
		service, exists := wantByID[row["id"].(string)]
		if !exists {
			t.Fatal("unexpected or duplicate service", row)
		}
		want := serviceWireFields(t, service)
		want["customer_id"] = service.UserID
		if !reflect.DeepEqual(row, want) {
			t.Fatal("administrator projection changed service fields or ownership", row, want)
		}
		delete(wantByID, service.ID)
	}
	store.assertOwners(t, "")
	for _, check := range []struct {
		client  *testClient
		service Service
	}{
		{customer, fixture[0]},
		{administrator, fixture[2]},
	} {
		path := "/api/v1/services?role=admin&customer_id=" + other.ID
		status, body := check.client.request("GET", path, nil, "")
		if status != http.StatusOK {
			t.Fatal(status, body)
		}
		rows := body["data"].([]any)
		if len(rows) != 1 || !reflect.DeepEqual(rows[0], serviceWireFields(t, check.service)) {
			t.Fatal("customer endpoint changed ownership filtering or exposed administrator fields", body)
		}
		raw, _ := json.Marshal(body)
		for _, secret := range []string{owner.ID, other.ID, admin.ID, owner.PasswordHash, admin.PasswordHash, "customer_id", "user_id", "UserID", "password_hash"} {
			if strings.Contains(string(raw), secret) {
				t.Fatal("customer endpoint exposed ownership or credential data", secret)
			}
		}
	}
	store.assertOwners(t, "", owner.ID, admin.ID)
	// Keep the dispatcher's existing method contract: authenticated POST to a
	// GET-only list is 404; methods other than GET/POST are rejected with 405.
	for method, want := range map[string]int{"POST": http.StatusNotFound, "PUT": http.StatusMethodNotAllowed, "DELETE": http.StatusMethodNotAllowed} {
		if status, body := administrator.request(method, "/api/v1/admin/services", map[string]string{"status": "terminated"}, ""); status != want {
			t.Fatal(method, status, body)
		}
	}
	store.assertOwners(t, "", owner.ID, admin.ID)
	if !reflect.DeepEqual(m.services, fixture) {
		t.Fatal("read or rejected requests mutated service records")
	}
}

func TestAdminServicesReadFailureDoesNotExposeData(t *testing.T) {
	m := NewMemory()
	store := &adminServicesStoreSpy{Store: m, err: errors.New("private-store-error-canary")}
	srv := httptest.NewServer(NewApp(store, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()}).Handler())
	defer srv.Close()
	admin := client(t, srv.URL, "admin@aster.local", "DemoAdmin!2026")
	status, body := admin.request("GET", "/api/v1/admin/services", nil, "")
	raw, _ := json.Marshal(body)
	if status != http.StatusInternalServerError || body["data"] != nil || strings.Contains(string(raw), "private-store-error-canary") {
		t.Fatal(status, body)
	}
	store.assertOwners(t, "")
}

func TestMySQLAdminServicesHTTPAuthorizationOwnershipAndSnapshots(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const password = "AdminServicesSQL!2026"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	users := []User{
		{ID: ID(), Email: ID() + "@services.invalid", Name: "First customer", Role: "customer", PasswordHash: string(hash)},
		{ID: ID(), Email: ID() + "@services.invalid", Name: "Second customer", Role: "customer", PasswordHash: string(hash)},
		{ID: ID(), Email: ID() + "@services.invalid", Name: "Service administrator", Role: "admin", PasswordHash: string(hash)},
	}
	for _, user := range users {
		if err := m.AddUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	fixture := []Service{}
	for index, user := range users[:2] {
		format := DefaultCurrencyFormat("CNY")
		format.Name, format.Prefix, format.Suffix = "Archived currency", "old ¥", " archived"
		service := Service{ID: ID(), UserID: user.ID, Name: []string{"sql-alpha", "sql-beta"}[index],
			PlanName: "Historical plan", Region: "Historical region", IPv4: "192.0.2.1", Status: []string{"active", "suspended"}[index],
			NextDueAt: time.Date(2026, 11, index+1, 0, 0, 0, 0, time.UTC), PriceMinor: MaxMinor - int64(index),
			Currency: "CNY", CurrencyFormat: format, CPU: index + 1, MemoryGB: 4, DiskGB: 80}
		snapshot, err := json.Marshal(format)
		if err != nil {
			t.Fatal(err)
		}
		migrationExec(t, ctx, m.DB, "INSERT INTO services(id,user_id,name,plan_name,region,ipv4,status,next_due_at,price_minor,currency,cpu,memory_gb,disk_gb,currency_snapshot) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
			service.ID, service.UserID, service.Name, service.PlanName, service.Region, service.IPv4, service.Status, service.NextDueAt, service.PriceMinor, service.Currency, service.CPU, service.MemoryGB, service.DiskGB, snapshot)
		fixture = append(fixture, service)
	}
	store := &adminServicesStoreSpy{Store: m}
	srv := httptest.NewServer(NewApp(store, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()}).Handler())
	defer srv.Close()
	anonymous := client(t, srv.URL, "", "")
	first := client(t, srv.URL, users[0].Email, password)
	second := client(t, srv.URL, users[1].Email, password)
	admin := client(t, srv.URL, users[2].Email, password)
	for _, check := range []struct {
		client *testClient
		status int
	}{{anonymous, http.StatusUnauthorized}, {first, http.StatusForbidden}, {second, http.StatusForbidden}} {
		if status, body := check.client.request("GET", "/api/v1/admin/services", nil, ""); status != check.status {
			t.Fatal(status, body)
		}
	}
	store.assertOwners(t)
	status, body := admin.request("GET", "/api/v1/admin/services", nil, "")
	if status != http.StatusOK {
		t.Fatal(status, body)
	}
	rows := body["data"].([]any)
	if len(rows) != len(fixture) {
		t.Fatal("real MySQL administrator list lost customer services", body)
	}
	for index, service := range fixture {
		want := serviceWireFields(t, service)
		want["customer_id"] = service.UserID
		if !reflect.DeepEqual(rows[index], want) {
			t.Fatal("real MySQL administrator projection changed owner or historical fields", rows[index], want)
		}
	}
	for index, customer := range []*testClient{first, second} {
		status, body := customer.request("GET", "/api/v1/services", nil, "")
		if status != http.StatusOK {
			t.Fatal(status, body)
		}
		rows := body["data"].([]any)
		if len(rows) != 1 || !reflect.DeepEqual(rows[0], serviceWireFields(t, fixture[index])) {
			t.Fatal("real MySQL customer ownership or response privacy changed", body)
		}
		raw, _ := json.Marshal(body)
		for _, secret := range []string{users[0].ID, users[1].ID, users[2].ID, string(hash), "customer_id", "user_id", "UserID", "password_hash"} {
			if strings.Contains(string(raw), secret) {
				t.Fatal("real MySQL customer service response leaked private data", secret)
			}
		}
	}
	if status, body := admin.request("GET", "/api/v1/services", nil, ""); status != http.StatusOK || len(body["data"].([]any)) != 0 {
		t.Fatal("real MySQL administrator customer path crossed ownership", status, body)
	}
	store.assertOwners(t, "", users[0].ID, users[1].ID, users[2].ID)
	// The existing session must observe the persisted role on its next request,
	// not retain administrator access from a cookie or a prior response.
	result, err := m.DB.ExecContext(ctx, "UPDATE users SET role='customer' WHERE id=? AND role='admin'", users[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		t.Fatal("fixture administrator role was not changed", count, err)
	}
	if status, body := admin.request("GET", "/api/v1/admin/services", nil, ""); status != http.StatusForbidden {
		t.Fatal("existing cookie retained revoked administrator permissions", status, body)
	}
	store.assertOwners(t, "", users[0].ID, users[1].ID, users[2].ID)
}
