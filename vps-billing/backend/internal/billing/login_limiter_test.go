package billing

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLoginLimiterEvictsOldestInsteadOfLockingEveryone(t *testing.T) {
	a := NewApp(NewMemory(), Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()})
	for n := 0; n < 20000; n++ {
		a.allowLogin(fmt.Sprintf("ip:10.%d.%d.%d", n>>16, n>>8&255, n&255))
		a.allowLogin(fmt.Sprintf("email:attack-%d@example.invalid", n))
	}
	if !a.allowLogin("ip:192.0.2.123") || !a.allowLogin("email:legitimate@example.invalid") {
		t.Fatal("attacker cardinality locked out a fresh customer")
	}
	r := httptest.NewRequest("POST", "http://localhost/api/v1/auth/login", strings.NewReader(`{"email":"demo@aster.local","password":"DemoCustomer!2026"}`))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "192.0.2.124:12345"
	w := httptest.NewRecorder()
	a.login(w, r)
	if w.Code != 200 {
		t.Fatal("fresh legitimate login rejected", w.Code)
	}
	if len(a.attempts) > 10000 {
		t.Fatal("unbounded limiter", len(a.attempts))
	}
}

func TestLoginLimiterIPv6PrefixAndIPv4Identity(t *testing.T) {
	a := &App{attempts: map[string]bucket{}}
	for n := 1; n <= 20; n++ {
		if !a.allowLogin(fmt.Sprintf("ip:2001:db8:1234:5678::%x", n)) {
			t.Fatal(n)
		}
	}
	if a.allowLogin("ip:2001:db8:1234:5678::ffff") {
		t.Fatal("IPv6 rotation bypassed /64 bucket")
	}
	if !a.allowLogin("ip:2001:db8:1234:5679::1") {
		t.Fatal("unrelated prefix blocked")
	}
	for n := 0; n < 20; n++ {
		a.allowLogin("ip:192.0.2.1")
	}
	if a.allowLogin("ip:::ffff:192.0.2.1") {
		t.Fatal("mapped IPv4 bypassed /32 bucket")
	}
	if !a.allowLogin("ip:192.0.2.2") {
		t.Fatal("different IPv4 blocked")
	}
}

func TestLoginLimiterEmailEventuallyReopens(t *testing.T) {
	a := &App{attempts: map[string]bucket{}}
	for n := 0; n < 100; n++ {
		a.allowLogin("email:target@example.invalid")
	}
	b := a.attempts["email:target@example.invalid"]
	b.Start = time.Now().Add(-time.Minute)
	a.attempts["email:target@example.invalid"] = b
	if !a.allowLogin("email:target@example.invalid") {
		t.Fatal("email cannot recover after capped backoff")
	}
}

func TestEmailBackoffIsCappedAndRejectedSprayDoesNotExtendIt(t *testing.T) {
	a := &App{attempts: map[string]bucket{}}
	key := "email:target@example.invalid"
	for range 20 {
		if !a.allowLogin(key) {
			t.Fatal("initial burst denied")
		}
	}
	for range 10 {
		b := a.attempts[key]
		b.Start = time.Now().Add(-31 * time.Second)
		a.attempts[key] = b
		if !a.allowLogin(key) {
			t.Fatal("email backoff exceeded cap")
		}
		deadlineBase := a.attempts[key].Start
		for range 100 {
			if a.allowLogin(key) {
				t.Fatal("backoff bypassed")
			}
		}
		if !a.attempts[key].Start.Equal(deadlineBase) {
			t.Fatal("rejected spray extended account lock")
		}
	}
}
