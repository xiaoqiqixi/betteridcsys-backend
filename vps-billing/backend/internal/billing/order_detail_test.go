package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func assertOrderDetail(t *testing.T, store Store, owner, other, admin User) {
	t.Helper()
	ctx := t.Context()
	f := paymentFixture{store: store, owner: owner, other: other, ctx: ctx}
	o, i := f.order(t)
	attempt := f.reserve(t, i)
	if _, err := store.CompletePaymentCheckout(ctx, attempt.ID, syntheticCheckout(attempt), false); err != nil {
		t.Fatal(err)
	}
	details := store.(orderDetailStore)
	for _, uid := range []string{owner.ID, ""} {
		d, err := details.OrderDetail(ctx, uid, o.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(o, d.Order) || d.Invoice == nil || !reflect.DeepEqual(i, *d.Invoice) || d.Service != nil {
			t.Fatalf("snapshot/link changed: %#v", d)
		}
		if uid == "" {
			if d.Customer == nil || d.Customer.ID != owner.ID || d.Customer.PasswordHash != "" {
				t.Fatal("customer projection", d.Customer)
			}
		} else if d.Customer != nil {
			t.Fatal("customer route exposed customer projection")
		}
		if len(d.PaymentAttempts) != 1 || d.PaymentAttempts[0].ID != attempt.ID || d.PaymentAttempts[0].State != "checkout" {
			t.Fatal(d.PaymentAttempts)
		}
		raw, _ := json.Marshal(d)
		for _, secret := range []string{"checkout.example.invalid", "merchant-synthetic", attempt.RequestKey, "password_hash", "request_key", "merchant_account_id", "\"checkout\":", "user_id"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("detail exposed %q: %s", secret, raw)
			}
		}
		for _, event := range d.Audit {
			if event.UserID != owner.ID {
				t.Fatal("foreign audit", event)
			}
		}
	}
	settled, err := store.SettlePayment(ctx, attempt.ID, syntheticPayment(attempt))
	if err != nil {
		t.Fatal(err)
	}
	paid, err := details.OrderDetail(ctx, owner.ID, o.ID)
	if err != nil || paid.Order.Status != "paid" || paid.Invoice == nil || paid.Invoice.Status != "paid" || paid.Invoice.PaidAt == nil || paid.PaymentAttempts[0].State != "settled" || !reflect.DeepEqual(paid.Order, settled.Order) || !reflect.DeepEqual(*paid.Invoice, settled.Invoice) {
		t.Fatal("detail does not reflect actual atomic settlement", paid, err)
	}
	for _, uid := range []string{other.ID, admin.ID} {
		if _, err := details.OrderDetail(ctx, uid, o.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("owner boundary", uid, err)
		}
	}
	if _, err := details.OrderDetail(ctx, "", ID()); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing order", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := details.OrderDetail(canceled, owner.ID, o.ID); !errors.Is(err, context.Canceled) {
		t.Fatal("context", err)
	}
}

func TestOrderDetailMemoryAndHTTP(t *testing.T) {
	m := NewMemory()
	owner, other, admin := cancellationUsers(t, m)
	assertOrderDetail(t, m, owner, other, admin)
	srv := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()}).Handler())
	defer srv.Close()
	c := client(t, srv.URL, "demo@aster.local", "DemoCustomer!2026")
	stranger := client(t, srv.URL, "other@aster.local", "DemoOther!2026")
	a := client(t, srv.URL, "admin@aster.local", "DemoAdmin!2026")
	_, v := c.request("POST", "/api/v1/orders", map[string]string{"plan_id": "starter", "hostname": "detail-http"}, ID())
	id := v["data"].(map[string]any)["id"].(string)
	for _, tc := range []struct {
		c    *testClient
		path string
		want int
	}{{c, "/orders/" + id, 200}, {a, "/admin/orders/" + id, 200}, {stranger, "/orders/" + id, 404}, {a, "/orders/" + id, 404}, {c, "/admin/orders/" + id, 403}, {c, "/orders/bad", 404}, {c, "/orders/" + ID(), 404}, {client(t, srv.URL, "", ""), "/orders/" + id, 401}} {
		if status, out := tc.c.request("GET", "/api/v1"+tc.path, nil, ""); status != tc.want {
			t.Fatal(tc.path, status, out)
		}
	}
	if status, _ := a.request("POST", "/api/v1/admin/orders/"+id, map[string]string{"status": "paid"}, ""); status != 405 {
		t.Fatal("arbitrary status mutation", status)
	}
}

func TestOrderDetailMemoryBoundsIsolationAndOldRecords(t *testing.T) {
	m := NewMemory()
	owner, other, _ := cancellationUsers(t, m)
	f := paymentFixture{store: m, owner: owner, other: other, ctx: t.Context()}
	o, i := f.order(t)
	m.mu.Lock()
	for j := 0; j < 103; j++ {
		m.audits = append(m.audits, Audit{ID: fmt.Sprintf("%032d", j), UserID: owner.ID, Target: i.ID, Action: "test.order", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	}
	m.audits = append(m.audits, Audit{ID: ID(), UserID: other.ID, Target: o.ID, Action: "foreign"}, Audit{ID: ID(), UserID: owner.ID, Target: ID(), Action: "unrelated"})
	m.mu.Unlock()
	d, err := m.OrderDetail(t.Context(), owner.ID, o.ID)
	if err != nil || len(d.Audit) != 100 || !d.AuditHasMore {
		t.Fatal(d, err)
	}
	d.Order.PlanSnapshot.Name = "mutated"
	d.Invoice.Issuer.Name = "mutated"
	again, err := m.OrderDetail(t.Context(), owner.ID, o.ID)
	if err != nil || again.Order.PlanSnapshot.Name == "mutated" || again.Invoice.Issuer.Name == "mutated" {
		t.Fatal("escaped snapshot", err)
	}
	m.mu.Lock()
	m.invoices = nil
	m.mu.Unlock()
	d, err = m.OrderDetail(t.Context(), owner.ID, o.ID)
	if err != nil || d.Invoice != nil || d.Service != nil {
		t.Fatal("historical missing link guessed", err)
	}
}

func TestMySQLOrderDetail(t *testing.T) {
	f := newCancellationSQLFixture(t)
	assertOrderDetail(t, f.m, f.owner, f.other, f.admin)
	o, i, _ := f.order(t)
	for j := 0; j < 103; j++ {
		migrationExec(t, f.ctx, f.m.DB, "INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) VALUES(?,?,?,?,?,?,?)", fmt.Sprintf("%032d", j), f.owner.ID, "test.order", i.ID, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), f.admin.Email, "bounded detail")
	}
	migrationExec(t, f.ctx, f.m.DB, "INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) VALUES(?,?,?,?,?,?,?)", ID(), f.other.ID, "foreign", o.ID, time.Now().UTC(), f.other.Email, "must not leak")
	before := cancellationSQLRecords(t, f, o.ID)
	d, err := f.m.OrderDetail(f.ctx, "", o.ID)
	if err != nil || len(d.Audit) != 100 || !d.AuditHasMore {
		t.Fatal(d, err)
	}
	for _, a := range d.Audit {
		if a.UserID != f.owner.ID {
			t.Fatal("foreign event")
		}
	}
	if !reflect.DeepEqual(before, cancellationSQLRecords(t, f, o.ID)) {
		t.Fatal("detail mutated financial records")
	}
}

func TestManagementRoutesLogOnlyFixedLabels(t *testing.T) {
	for path, want := range map[string]string{
		"/api/v1/admin/customer-accounts/private@example.invalid/password": "/api/v1/admin/customer-accounts/{account_id}/password",
		"/api/v1/admin/customer-accounts/private/revoke-sessions":          "/api/v1/admin/customer-accounts/{account_id}/revoke-sessions",
		"/api/v1/admin/catalog/private/duplicate":                          "/api/v1/admin/catalog/{plan_id}/duplicate",
		"/api/v1/admin/orders/private":                                     "/api/v1/admin/orders/{order_id}",
		"/api/v1/orders/private/cancel":                                    "/api/v1/orders/{order_id}/cancel",
		"/api/v1/admin/catalog/private/secret":                             "unmatched",
	} {
		if got := loggedRoute(path); got != want {
			t.Fatal(path, got, want)
		}
	}
}

func TestMySQLOrderDetailOlderThanListLimit(t *testing.T) {
	f := newCancellationSQLFixture(t)
	o, i, _ := f.order(t)
	tx, err := f.m.DB.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(f.ctx, "INSERT INTO orders(id,number,user_id,plan_id,plan_name,hostname,status,total_minor,currency,created_at,issuer_snapshot,currency_snapshot,plan_snapshot) SELECT ?,?,user_id,plan_id,plan_name,'newer',status,total_minor,currency,?,issuer_snapshot,currency_snapshot,plan_snapshot FROM orders WHERE id=?")
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	for j := 0; j < 1001; j++ {
		id := ID()
		if _, err = stmt.ExecContext(f.ctx, id, "LIMIT-"+id, o.CreatedAt.Add(time.Second), o.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	list, err := f.m.Orders(f.ctx, f.owner.ID)
	if err != nil || len(list) != 1000 {
		t.Fatal(len(list), err)
	}
	for _, v := range list {
		if v.ID == o.ID {
			t.Fatal("fixture did not exceed list cap")
		}
	}
	detail, err := f.m.OrderDetail(f.ctx, f.owner.ID, o.ID)
	if err != nil || detail.Order.ID != o.ID || detail.Invoice == nil || detail.Invoice.ID != i.ID {
		t.Fatal("detail depends on capped list", detail, err)
	}
}
