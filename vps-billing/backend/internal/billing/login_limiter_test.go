package billing

import (
	"fmt"
	"testing"
	"time"
)

func TestLoginLimiterEvictsOldestInsteadOfLockingEveryone(t *testing.T) {
	a := &App{attempts: map[string]bucket{}}
	for n := 0; n < 20000; n++ {
		a.allowLogin(fmt.Sprintf("ip:10.%d.%d.%d", n>>16, n>>8&255, n&255))
		a.allowLogin(fmt.Sprintf("email:attack-%d@example.invalid", n))
	}
	if !a.allowLogin("ip:192.0.2.123") || !a.allowLogin("email:legitimate@example.invalid") {
		t.Fatal("attacker cardinality locked out a fresh customer")
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
