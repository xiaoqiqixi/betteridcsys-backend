package billing

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type authorizationCountingStore struct {
	*Memory
	sessions, users, policies atomic.Int32
	sessionError              error
}

func (s *authorizationCountingStore) Session(ctx context.Context, hash string) (Session, error) {
	s.sessions.Add(1)
	if s.sessionError != nil {
		return Session{}, s.sessionError
	}
	return s.Memory.Session(ctx, hash)
}
func (s *authorizationCountingStore) UserByID(ctx context.Context, id string) (User, error) {
	s.users.Add(1)
	return s.Memory.UserByID(ctx, id)
}
func (s *authorizationCountingStore) StaffPolicy(ctx context.Context, id string) (staffPolicy, error) {
	s.policies.Add(1)
	return s.Memory.StaffPolicy(ctx, id)
}

func TestCommerceRequestAuthorizationReusesLookupsAndObservesNextRequestRevocation(t *testing.T) {
	f := memoryAccountFixture(t)
	s := &authorizationCountingStore{Memory: f.Store.(*Memory)}
	a := NewApp(s, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	token := ID() + ID()
	putSession := func() {
		t.Helper()
		if err := s.PutSession(t.Context(), Session{Hash: digest(token), UserID: f.admin.ID, Revision: f.admin.Revision, Expires: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	putSession()
	// This route has both the router guard and an inner integrations guard.
	r := httptest.NewRequest("GET", "http://localhost/api/v1/admin/integrations/gateways", nil)
	r.AddCookie(&http.Cookie{Name: a.cookieName(), Value: token})
	call := func(want int) {
		t.Helper()
		w := httptest.NewRecorder()
		// Reusing the request object must not reuse an earlier Handler memo.
		a.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("status=%d want=%d body=%s", w.Code, want, w.Body)
		}
	}
	call(200)
	if s.sessions.Load() != 1 || s.users.Load() != 1 || s.policies.Load() != 1 {
		t.Fatal("repeated lookups in one request", s.sessions.Load(), s.users.Load(), s.policies.Load())
	}
	if err := s.DeleteSession(t.Context(), digest(token)); err != nil {
		t.Fatal(err)
	}
	call(401)
	putSession()
	s.mu.Lock()
	s.staffPolicies[f.admin.ID] = staffPolicy{RoleID: "restricted", Permissions: []string{}}
	s.mu.Unlock()
	call(403)
	s.mu.Lock()
	s.staffPolicies[f.admin.ID] = ownerPolicy()
	s.mu.Unlock()
	call(200)
	if s.sessions.Load() != 4 || s.users.Load() != 3 || s.policies.Load() != 3 {
		t.Fatal("authorization was shared across requests", s.sessions.Load(), s.users.Load(), s.policies.Load())
	}
	s.sessionError = errors.New("temporary session lookup failure")
	call(500)
	s.sessionError = nil
	call(200)
}
