package resourcebridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSystemdCredentialFilesRemainFixedPrivateAndNoLink(t *testing.T) {
	// The production reader does not allow sticky writable ancestors. Use a
	// protected source-workspace fixture, not /tmp, without weakening that rule.
	base, e := os.MkdirTemp(".", "aster-resource-credential-test-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(base)
	base, e = filepath.Abs(base)
	if e != nil {
		t.Fatal(e)
	}
	uid := uint32(os.Geteuid())
	path := filepath.Join(base, "resource-token")
	secret := []byte("test-only-resource-secret")
	write := func() {
		t.Helper()
		if e := os.WriteFile(path, secret, 0600); e != nil {
			t.Fatal(e)
		}
		if e := os.Chmod(path, 0600); e != nil {
			t.Fatal(e)
		}
	}
	write()
	if got, e := readCredential(base, "resource-token", uid); e != nil || string(got) != string(secret) {
		t.Fatal("valid private credential", e)
	}
	if _, e := ReadCredential(base, "resource-token"); e == nil {
		t.Fatal("non-systemd path allowed")
	}
	for _, name := range []string{"../resource-token", "api.env", "mysql.dsn", "resource-payment"} {
		if _, e := readCredential(base, name, uid); e == nil {
			t.Fatal("arbitrary credential path", name)
		}
	}
	for _, mode := range []os.FileMode{0640, 0644, 0666} {
		if e := os.Chmod(path, mode); e != nil {
			t.Fatal(e)
		}
		if _, e := readCredential(base, "resource-token", uid); e == nil {
			t.Fatal("shared readable secret")
		}
	}
	write()
	alias := filepath.Join(base, "alias")
	if e := os.Link(path, alias); e != nil {
		t.Fatal(e)
	}
	if _, e := readCredential(base, "resource-token", uid); e == nil {
		t.Fatal("linked credential")
	}
	os.Remove(alias)
	saved := filepath.Join(base, "saved")
	if e := os.Rename(path, saved); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(saved, path); e != nil {
		t.Fatal(e)
	}
	if _, e := readCredential(base, "resource-token", uid); e == nil {
		t.Fatal("symlink credential")
	}
	os.Remove(path)
	os.Remove(saved)
	write()
	if e := os.Chmod(base, 0770); e != nil {
		t.Fatal(e)
	}
	if _, e := readCredential(base, "resource-token", uid); e == nil {
		t.Fatal("shared writable ancestor")
	}
	os.Chmod(base, 0700)
	if e := os.WriteFile(path, make([]byte, 4097), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := readCredential(base, "resource-token", uid); e == nil {
		t.Fatal("oversized token")
	}
}
