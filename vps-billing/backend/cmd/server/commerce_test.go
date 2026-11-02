package main

import "testing"

func TestCommerceProductionDefaultAndLegacySelector(t *testing.T) {
	for _, tc := range []struct {
		demo           bool
		override, want string
	}{{false, "", "0.6"}, {true, "", "0.5"}, {false, "0.5", "0.5"}, {true, "0.6", "0.6"}} {
		got, err := commerceContractVersion(tc.demo, tc.override)
		if err != nil || got != tc.want {
			t.Fatal(tc, got, err)
		}
	}
	if _, err := commerceContractVersion(false, "0.7"); err == nil {
		t.Fatal("unsupported contract accepted")
	}
}
