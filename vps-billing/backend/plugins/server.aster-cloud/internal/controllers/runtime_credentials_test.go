package controllers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeCredentialsSeparateHostAuthorityAndPrivateSecrets(t *testing.T) {
	dir := privateFixture(t)
	owner := uint32(os.Geteuid())
	path := filepath.Join(dir, "runtime.json")
	token := filepath.Join(dir, "controller-token")
	writeFixture(t, token, []byte("runtime-private-test-token-only"), 0400)
	policy := Policy{Schema: 1, Controllers: []Profile{{ID: "controller", Enabled: true, Endpoint: "https://controller.example.test/billing/provision", AllowedCIDRs: []string{"192.0.2.0/24"}, TokenFile: token, CAFile: ""}}}
	raw, _ := json.Marshal(policy)
	writeFixture(t, path, raw, 0644)
	loaded, err := loadRuntimeCredentials(path, owner, owner)
	if err != nil || len(loaded) != 1 || loaded[0].Profile.TokenFile != "" || loaded[0].Profile.CAFile != "" || string(loaded[0].Token) != "runtime-private-test-token-only" {
		t.Fatal("runtime credential load", err)
	}
	if _, err = loadPolicy(path, owner); err != ErrUnavailable {
		t.Fatal("runtime policy relaxed root CLI policy")
	}
	if err = os.Chmod(token, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = loadRuntimeCredentials(path, owner, owner); err != ErrUnavailable {
		t.Fatal("shared-readable credential accepted")
	}
	if err = os.Chmod(token, 0400); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err = loadRuntimeCredentials(path, owner, owner); err != ErrUnavailable {
		t.Fatal("writable policy accepted")
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked-token")
	if err = os.Symlink(token, link); err != nil {
		t.Fatal(err)
	}
	policy.Controllers[0].TokenFile = link
	raw, _ = json.Marshal(policy)
	writeFixture(t, path, raw, 0644)
	if _, err = loadRuntimeCredentials(path, owner, owner); err != ErrUnavailable {
		t.Fatal("linked credential accepted")
	}
}
