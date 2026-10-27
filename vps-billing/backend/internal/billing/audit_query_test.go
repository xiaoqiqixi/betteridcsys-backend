package billing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type auditQueryFixture struct {
	store      Store
	events     []Audit
	users      []User
	raw        func() []Audit
	changeRole func(string, string)
}

func auditQueryEvents(users []User) []Audit {
	base := time.Date(2025, 1, 2, 3, 4, 5, 123456000, time.UTC)
	events := make([]Audit, 0, 1008)
	for n := 0; n < 1005; n++ {
		owner := users[n%len(users)].ID
		events = append(events, Audit{ID: fmt.Sprintf("%032x", n+1), UserID: owner, Action: []string{"order.cancelled", "order.created", "payment.settled"}[n%3],
			Target: fmt.Sprintf("historical-order-%04d", n), CreatedAt: base.Add(time.Duration(n/7) * time.Microsecond), ActorEmail: []string{"payment:system", "Admin@Example.invalid", "admin@example.invalid", "customer@example.invalid"}[n%4], Detail: fmt.Sprintf("不可改写的历史记录 %d", n)})
	}
	for n, action := range []string{"order.Created", "order.creáted"} {
		actor := "payment:system"
		if n == 0 {
			actor = "Ádmin@Example.invalid"
		}
		events = append(events, Audit{ID: fmt.Sprintf("%032x", 1006+n), UserID: users[0].ID, Action: action, Target: "legacy-action", CreatedAt: base, ActorEmail: actor, Detail: "Historical spelling must stay unchanged"})
	}
	events = append(events, Audit{ID: fmt.Sprintf("%032x", 1008), UserID: users[0].ID, Action: "order.created", Target: "future-event", CreatedAt: time.Now().UTC().Add(24 * time.Hour).Truncate(time.Microsecond), ActorEmail: "payment:system", Detail: "outside snapshot"})
	return events
}
func newAuditQueryFixture(t *testing.T, sql bool) auditQueryFixture {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("AuditFixture!2026"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	users := []User{}
	for n, role := range []string{"customer", "customer", "admin"} {
		users = append(users, User{ID: strings.Repeat(fmt.Sprint(n+1), 32), Email: fmt.Sprintf("audit-%d@example.invalid", n), Name: "Audit fixture", Role: role, PasswordHash: string(hash)})
	}
	fixture := auditQueryFixture{users: users}
	fixture.events = auditQueryEvents(users)
	if !sql {
		m := &Memory{users: map[string]User{}, sessions: map[string]Session{}, audits: append([]Audit{}, fixture.events...)}
		for _, user := range users {
			m.users[user.ID] = user
		}
		fixture.store = m
		fixture.raw = func() []Audit { m.mu.Lock(); defer m.mu.Unlock(); return append([]Audit{}, m.audits...) }
		fixture.changeRole = func(id, role string) {
			m.mu.Lock()
			defer m.mu.Unlock()
			user := m.users[id]
			user.Role = role
			m.users[id] = user
		}
		return fixture
	}
	m, ctx := migrationTestDatabase(t)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, user := range users {
		if err := m.AddUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, "INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) VALUES(?,?,?,?,?,?,?)")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range fixture.events {
		if _, err = statement.ExecContext(ctx, event.ID, event.UserID, event.Action, event.Target, event.CreatedAt, event.ActorEmail, event.Detail); err != nil {
			statement.Close()
			t.Fatal(err)
		}
	}
	if err = statement.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	fixture.store = m
	fixture.raw = func() []Audit {
		rows, err := m.DB.QueryContext(ctx, "SELECT id,user_id,action,target,created_at,actor_email,detail FROM audit_events ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		items := []Audit{}
		for rows.Next() {
			var event Audit
			if err := rows.Scan(&event.ID, &event.UserID, &event.Action, &event.Target, &event.CreatedAt, &event.ActorEmail, &event.Detail); err != nil {
				t.Fatal(err)
			}
			items = append(items, event)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		return items
	}
	fixture.changeRole = func(id, role string) { migrationExec(t, ctx, m.DB, "UPDATE users SET role=? WHERE id=?", role, id) }
	return fixture
}
func expectedAuditRows(events []Audit, uid string, q AuditQuery) []Audit {
	items := []Audit{}
	for _, row := range events {
		if uid != "" && row.UserID != uid || q.OwnerID != "" && row.UserID != q.OwnerID || q.Action != "" && row.Action != q.Action || q.ActorEmail != "" && row.ActorEmail != q.ActorEmail {
			continue
		}
		if !q.From.IsZero() && row.CreatedAt.Before(q.From) || !q.Until.IsZero() && !row.CreatedAt.Before(q.Until) || row.CreatedAt.After(q.Snapshot) {
			continue
		}
		items = append(items, row)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return items
}
func auditQueryContract(t *testing.T, f auditQueryFixture) {
	t.Helper()
	before := f.raw()
	base := f.events[0].CreatedAt
	snapshot := base.Add(time.Hour)
	for _, tc := range []struct {
		name, uid string
		q         AuditQuery
	}{
		{"over_1000", "", AuditQuery{Limit: 37}},
		{"owner_scope", f.users[0].ID, AuditQuery{Limit: 17}},
		{"admin_owner_filter", "", AuditQuery{Limit: 19, OwnerID: f.users[1].ID}},
		{"exact_action", "", AuditQuery{Limit: 23, Action: "order.created"}},
		{"system_actor", "", AuditQuery{Limit: 29, ActorEmail: "payment:system"}},
		{"actor_uppercase", "", AuditQuery{Limit: 31, ActorEmail: "Admin@Example.invalid"}},
		{"actor_lowercase", "", AuditQuery{Limit: 31, ActorEmail: "admin@example.invalid"}},
		{"from_inclusive_until_exclusive", "", AuditQuery{Limit: 7, From: base.Add(2 * time.Microsecond), Until: base.Add(4 * time.Microsecond)}},
		{"combined", f.users[1].ID, AuditQuery{Limit: 11, Action: "order.created", ActorEmail: "Admin@Example.invalid", From: base, Until: base.Add(time.Hour)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := tc.q
			q.Snapshot = snapshot
			want := expectedAuditRows(f.events, tc.uid, q)
			got := []Audit{}
			seen := map[string]bool{}
			for pageNumber := 0; ; pageNumber++ {
				if pageNumber > 100 {
					t.Fatal("pagination did not terminate")
				}
				page, err := f.store.AuditPage(t.Context(), tc.uid, q)
				if err != nil {
					t.Fatal(err)
				}
				if len(page.Items) > q.Limit || page.HasMore != (len(got)+len(page.Items) < len(want)) {
					t.Fatal("page boundary mismatch", len(got), len(page.Items), len(want), page.HasMore)
				}
				for _, event := range page.Items {
					if seen[event.ID] {
						t.Fatal("duplicate cursor row", event.ID)
					}
					seen[event.ID] = true
					got = append(got, event)
				}
				if !page.HasMore {
					break
				}
				if len(page.Items) == 0 {
					t.Fatal("empty page claims more")
				}
				last := page.Items[len(page.Items)-1]
				q.Before, q.BeforeID = last.CreatedAt, last.ID
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ordered/filter rows differ: got %d, want %d", len(got), len(want))
			}
			if tc.name == "over_1000" && len(got) <= 1000 {
				t.Fatal("legacy 1000-row cap retained")
			}
		})
	}
	if after := f.raw(); !reflect.DeepEqual(before, after) {
		t.Fatal("read queries mutated persisted audit history")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.store.AuditPage(ctx, "", AuditQuery{}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled query did not stop", err)
	}
	for _, q := range []AuditQuery{{Limit: -1}, {Limit: 101}, {Action: "order.Created"}, {ActorEmail: "bad\nactor"}, {OwnerID: "not-an-id"}, {From: base, Until: base}, {Snapshot: base.Add(time.Nanosecond)}, {Snapshot: base, Before: base.Add(time.Microsecond), BeforeID: "id"}, {Before: base}} {
		if _, err := f.store.AuditPage(t.Context(), f.users[0].ID, q); !errors.Is(err, ErrAuditQuery) {
			t.Fatal("invalid store query accepted", q, err)
		}
	}
	if _, err := f.store.AuditPage(t.Context(), f.users[0].ID, AuditQuery{OwnerID: f.users[1].ID}); !errors.Is(err, ErrAuditQuery) {
		t.Fatal("cross-owner store scope accepted", err)
	}
}
func TestAuditPageMemoryContract(t *testing.T) { auditQueryContract(t, newAuditQueryFixture(t, false)) }
func TestMySQLAuditPageContract(t *testing.T)  { auditQueryContract(t, newAuditQueryFixture(t, true)) }

type auditPageSpy struct {
	Store
	mu     sync.Mutex
	owners []string
}

func (s *auditPageSpy) AuditPage(ctx context.Context, uid string, q AuditQuery) (AuditPage, error) {
	s.mu.Lock()
	s.owners = append(s.owners, uid)
	s.mu.Unlock()
	return s.Store.AuditPage(ctx, uid, q)
}
func (s *auditPageSpy) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.owners) }
func auditWireData(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	data, ok := response["data"].(map[string]any)
	if !ok {
		t.Fatal("missing page response", response)
	}
	return data
}
func auditHTTPContract(t *testing.T, f auditQueryFixture) {
	t.Helper()
	before := f.raw()
	spy := &auditPageSpy{Store: f.store}
	app := NewApp(spy, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	anonymous := client(t, server.URL, "", "")
	customer := client(t, server.URL, f.users[0].Email, "AuditFixture!2026")
	other := client(t, server.URL, f.users[1].Email, "AuditFixture!2026")
	admin := client(t, server.URL, f.users[2].Email, "AuditFixture!2026")
	reject := func(c *testClient, path string, want int) {
		t.Helper()
		calls := spy.count()
		status, response := c.request("GET", path, nil, "")
		if status != want {
			t.Fatal("unexpected rejection", path, status, response)
		}
		if spy.count() != calls {
			t.Fatal("invalid/unauthorized request reached AuditPage", path)
		}
	}
	reject(anonymous, "/api/v1/admin/audit-events", 401)
	reject(customer, "/api/v1/admin/audit-events", 403)
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		calls := spy.count()
		status, response := admin.request(method, "/api/v1/admin/audit-events", map[string]string{}, "")
		want := 404
		if method == http.MethodPut {
			want = 405
		}
		if status != want || spy.count() != calls {
			t.Fatal("non-GET audit endpoint queried Store", method, status, response)
		}
	}
	for _, query := range []string{
		"owner_id=" + f.users[0].ID, "owner_id=" + f.users[1].ID, "unknown=x", "action=order.created&action=order.cancelled", "limit=0", "limit=101", "limit=01", "limit=", "cursor=invalid", "cursor=x.y", "from=" + strings.Repeat("x", 5000), "from=2025-01-02T03:04:05.0000001Z", "from=2025-01-02T00:00:00Z&until=2025-01-02T00:00:00Z", "actor_email=bad%0Aactor", "action=order.Created", "from=%zz",
	} {
		reject(customer, "/api/v1/audit-events?"+query, 400)
	}
	eventsByID := map[string]Audit{}
	for _, event := range f.events {
		eventsByID[event.ID] = event
	}
	for _, tc := range []struct {
		c           *testClient
		path, owner string
		global      bool
	}{
		{customer, "/api/v1/audit-events?limit=100", f.users[0].ID, false},
		{other, "/api/v1/audit-events?limit=100", f.users[1].ID, false},
		{admin, "/api/v1/audit-events?limit=100", f.users[2].ID, false},
		{admin, "/api/v1/admin/audit-events?limit=100", "", true},
		{admin, "/api/v1/admin/audit-events?limit=100&owner_id=" + f.users[1].ID, f.users[1].ID, true},
	} {
		status, response := tc.c.request("GET", tc.path, nil, "")
		if status != 200 {
			t.Fatal(status, response)
		}
		data := auditWireData(t, response)
		items := data["items"].([]any)
		if len(items) == 0 {
			t.Fatal("fixture returned no events")
		}
		for _, raw := range items {
			event := raw.(map[string]any)
			stored := eventsByID[event["id"].(string)]
			if tc.owner != "" && stored.UserID != tc.owner {
				t.Fatal("owner filter escaped", event)
			}
			if tc.global {
				if event["owner_id"] != stored.UserID {
					t.Fatal("owner_id is not business owner", event, stored)
				}
			} else if _, exists := event["owner_id"]; exists {
				t.Fatal("customer owner metadata leaked", event)
			}
			for _, name := range []string{"user_id", "UserID", "password_hash", "session_hash"} {
				if _, exists := event[name]; exists {
					t.Fatal("private field leaked", name)
				}
			}
		}
		spy.mu.Lock()
		lastOwner := spy.owners[len(spy.owners)-1]
		spy.mu.Unlock()
		wantScope := tc.owner
		if tc.global {
			wantScope = ""
		}
		if lastOwner != wantScope {
			t.Fatal("wrong server-authorized owner scope", lastOwner, wantScope)
		}
	}
	// Follow the actual signed HTTP cursors, not only Store.Before, past 1000.
	wantRows := expectedAuditRows(f.events, "", AuditQuery{Snapshot: time.Now().UTC()})
	gotIDs := []string{}
	seenIDs := map[string]bool{}
	path := "/api/v1/admin/audit-events?limit=100"
	for pageNumber := 0; ; pageNumber++ {
		if pageNumber > 20 {
			t.Fatal("HTTP cursor pagination did not terminate")
		}
		status, response := admin.request("GET", path, nil, "")
		if status != 200 {
			t.Fatal("valid page refused", status, response)
		}
		data := auditWireData(t, response)
		for _, raw := range data["items"].([]any) {
			id := raw.(map[string]any)["id"].(string)
			if seenIDs[id] {
				t.Fatal("HTTP cursor repeated event", id)
			}
			seenIDs[id] = true
			gotIDs = append(gotIDs, id)
		}
		if data["has_more"] != true {
			if cursor, present := data["next_cursor"]; present && cursor != "" {
				t.Fatal("terminal page returned another cursor")
			}
			break
		}
		cursor, ok := data["next_cursor"].(string)
		if !ok || cursor == "" {
			t.Fatal("nonterminal page lost cursor")
		}
		path = "/api/v1/admin/audit-events?limit=100&cursor=" + url.QueryEscape(cursor)
	}
	if len(gotIDs) != len(wantRows) || len(gotIDs) <= 1000 {
		t.Fatal("HTTP pagination retained legacy cap", len(gotIDs), len(wantRows))
	}
	for i, row := range wantRows {
		if gotIDs[i] != row.ID {
			t.Fatal("HTTP pagination changed stable ordering", i)
		}
	}

	// A global cursor is not transferable to an owner path, filters or page size.
	status, response := admin.request("GET", "/api/v1/admin/audit-events?limit=2", nil, "")
	if status != 200 {
		t.Fatal(status, response)
	}
	data := auditWireData(t, response)
	cursor := data["next_cursor"].(string)
	if data["has_more"] != true || cursor == "" {
		t.Fatal("cursor missing")
	}
	suffix := "limit=2&cursor=" + url.QueryEscape(cursor)
	for _, path := range []string{"/api/v1/admin/audit-events?limit=3&cursor=" + url.QueryEscape(cursor), "/api/v1/admin/audit-events?" + suffix + "&action=order.created", "/api/v1/admin/audit-events?" + suffix + "&actor_email=payment%3Asystem", "/api/v1/admin/audit-events?" + suffix + "&owner_id=" + f.users[0].ID, "/api/v1/admin/audit-events?" + suffix + "&from=2025-01-01T00%3A00%3A00Z", "/api/v1/audit-events?" + suffix} {
		reject(admin, path, 400)
	}
	reject(customer, "/api/v1/audit-events?"+suffix, 400)
	replacement := "0"
	if strings.HasSuffix(cursor, replacement) {
		replacement = "1"
	}
	reject(admin, "/api/v1/admin/audit-events?limit=2&cursor="+url.QueryEscape(cursor[:len(cursor)-1]+replacement), 400)
	if status, response = admin.request("GET", "/api/v1/admin/audit-events?"+suffix, nil, ""); status != 200 {
		t.Fatal("valid same-scope cursor refused", status, response)
	}
	_, response = customer.request("GET", "/api/v1/audit-events?limit=2", nil, "")
	ownerCursor := auditWireData(t, response)["next_cursor"].(string)
	reject(other, "/api/v1/audit-events?limit=2&cursor="+url.QueryEscape(ownerCursor), 400)
	// The role is refreshed from persistence on the same still-valid cookie.
	f.changeRole(f.users[2].ID, "customer")
	reject(admin, "/api/v1/admin/audit-events?"+suffix, 403)
	f.changeRole(f.users[2].ID, "admin")
	if after := f.raw(); !reflect.DeepEqual(before, after) {
		t.Fatal("HTTP reads changed audit records")
	}
}
func TestAuditEventsHTTPAuthorizationFiltersAndCursors(t *testing.T) {
	auditHTTPContract(t, newAuditQueryFixture(t, false))
}
func TestMySQLAuditEventsHTTPAuthorizationFiltersAndCursors(t *testing.T) {
	auditHTTPContract(t, newAuditQueryFixture(t, true))
}

func TestAuditPageMemoryPreservesHistoricalTimestamp(t *testing.T) {
	original := Audit{ID: strings.Repeat("a", 32), UserID: strings.Repeat("1", 32), Action: "order.created", CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 123456789, time.FixedZone("legacy", 3600))}
	m := &Memory{audits: []Audit{original}}
	page, err := m.AuditPage(t.Context(), "", AuditQuery{})
	if err != nil || len(page.Items) != 1 || page.Items[0].CreatedAt.Nanosecond() != 123456000 || m.audits[0] != original {
		t.Fatal("read rewrote historical timestamp", page, err)
	}
}
