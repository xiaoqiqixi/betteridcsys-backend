package serviceapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReinstallAcceptsOnlyOneValidatedCredential(t *testing.T) {
	hash := "$2a$12$" + strings.Repeat("a", 53)
	decode := func(extra string) Params {
		t.Helper()
		var p Params
		raw := `{"image_id":"ubuntu","password_hash":"` + hash + `"` + extra + `}`
		if err := Decode([]byte(raw), &p); err != nil {
			t.Fatal("durable password hash rejected", err)
		}
		return p
	}
	p := decode("")
	if !p.Valid("reinstall") {
		t.Fatal("valid prehashed credential rejected")
	}
	if raw, _ := json.Marshal(p.Redacted()); !strings.Contains(string(raw), hash) {
		t.Fatal("one-way hash lost before asynchronous provisioning")
	}
	// A plaintext password alongside the hash is two forms of one secret: rejected.
	if decode(`,"password":"plaintext"`).Valid("reinstall") {
		t.Fatal("password plus hash accepted")
	}
	// A hash combined with SSH keys is a valid union (set password and keys).
	if !decode(`,"ssh_public_key":"ssh-ed25519 AAAA"`).Valid("reinstall") {
		t.Fatal("hash plus single key rejected")
	}
	if !decode(`,"ssh_public_keys":["ssh-ed25519 AAAA","ssh-rsa BBBB"]`).Valid("reinstall") {
		t.Fatal("hash plus multiple keys rejected")
	}
	for _, invalid := range []string{"plaintext", "$2a$04$" + strings.Repeat("a", 53), hash + "\n", strings.Replace(hash, "a", ":", 1)} {
		var p Params
		raw, _ := json.Marshal(map[string]string{"image_id": "ubuntu", "password_hash": invalid})
		_ = json.Unmarshal(raw, &p)
		if p.Valid("reinstall") {
			t.Fatal("unapproved password hash accepted")
		}
	}
	if p.Valid("reset_password") || p.Valid("start") {
		t.Fatal("hash accepted for unrelated action")
	}
}
