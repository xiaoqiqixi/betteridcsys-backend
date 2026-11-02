package controllerapi

import (
	"context"
	"errors"
	"testing"
)

func TestPublicTransportPreservesProtectedPolicyAndNilClientGuards(t *testing.T) {
	for _, path := range []string{"", "relative-policy.json", "/tmp/../private/policy.json", "/private/policy\nsecret"} {
		if _, err := LoadPolicy(path); !errors.Is(err, ErrUnavailable) {
			t.Fatal("public wrapper bypassed protected path validation")
		}
		if _, err := New(context.Background(), path, "primary"); !errors.Is(err, ErrUnavailable) {
			t.Fatal("constructor bypassed protected policy validation")
		}
	}
	var client *Client
	if result, err := client.Send(context.Background(), Request{}); !errors.Is(err, ErrUnavailable) || result != (Result{}) {
		t.Fatal("public alias changed nil transport semantics")
	}
	if result, err := client.Lookup(context.Background(), Request{}); !errors.Is(err, ErrUnavailable) || result != (Result{}) {
		t.Fatal("public alias changed nil lookup semantics")
	}
}
