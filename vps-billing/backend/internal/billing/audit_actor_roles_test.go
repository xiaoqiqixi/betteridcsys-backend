package billing

import (
	"errors"
	"strings"
	"testing"
)

func TestMySQLCommerceAuditActorBatchMatchesAccountCollation(t *testing.T) {
	f := mysqlAccountFixture(t)
	m := f.Store.(*MySQL)
	events := []Audit{}
	for _, email := range []string{f.customer.Email, strings.ToUpper(f.customer.Email), f.customer.Email + " ", f.admin.Email, "former@fixture.invalid", "历史操作员@example.invalid", f.customer.Email} {
		events = append(events, Audit{ActorEmail: email})
	}
	stop := captureFinanceQueries(t, m)
	roles, err := m.auditActorRoles(f.ctx, events)
	queries := stop()
	if err != nil || len(queries) != 1 {
		t.Fatal("actor roles must use one query", len(queries), err)
	}
	for _, event := range events {
		want := "staff"
		user, err := m.UserByEmail(f.ctx, event.ActorEmail)
		if err != nil && !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		if err == nil && user.Role == "customer" {
			want = "client"
		}
		if roles[event.ActorEmail] != want {
			t.Fatalf("actor %q: got %q want %q", event.ActorEmail, roles[event.ActorEmail], want)
		}
	}
	roles, err = m.auditActorRoles(f.ctx, []Audit{{ActorEmail: "system"}, {ActorEmail: "payment:fixture"}, {ActorEmail: ""}})
	if err != nil || len(roles) != 0 {
		t.Fatal("system actors were classified as accounts", roles, err)
	}
}
