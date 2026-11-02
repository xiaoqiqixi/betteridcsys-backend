package runtimeplugins

import (
	"aster/billing/plugins/server.aster-cloud/legacy/pluginapi"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

type fixture struct {
	manager                    *Manager
	base, binary, manifestPath string
	manifest                   pluginapi.Manifest
	policy                     pluginapi.Policy
}

func digestBytes(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func writeJSON(t *testing.T, path string, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return raw
}
func newFixture(t *testing.T) *fixture {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "aster-rpc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	var root syscall.Stat_t
	if err = syscall.Stat("/tmp", &root); err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Geteuid())
	rules := trust{owner: uid, systemRoot: root.Uid, allowSameUID: true}
	dir := filepath.Join(base, "plugins", "email.smtp", "1.1.0")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	binary := make([]byte, 64)
	copy(binary, "\x7fELF")
	binary[4] = 2
	binary[5] = 1
	binary[18] = 62
	f := &fixture{base: base, binary: filepath.Join(dir, "plugin"), manifestPath: filepath.Join(dir, "manifest.json")}
	if err = os.WriteFile(f.binary, binary, 0700); err != nil {
		t.Fatal(err)
	}
	f.manifest = pluginapi.Manifest{Schema: 1, ID: "email.smtp", Version: "1.1.0", Protocol: pluginapi.SMTPProtocol, CoreMin: "0.4.0", CoreMaxExclusive: "0.7.0", Capabilities: []string{pluginapi.SMTPCapability}, Executable: "plugin", SHA256: digestBytes(binary)}
	raw := writeJSON(t, f.manifestPath, f.manifest)
	f.policy = pluginapi.Policy{Schema: 1, Revision: 1, SMTP: &pluginapi.Selection{Enabled: true, ID: "email.smtp", Version: "1.1.0", ManifestSHA256: digestBytes(raw), WorkerUID: uid, ClientUID: uid, Socket: filepath.Join(base, "rpc.sock"), AllowedTargets: []pluginapi.Target{{Host: "mail.example.test", Port: 465}}}}
	f.manager = &Manager{root: filepath.Join(base, "plugins"), policy: filepath.Join(base, "policy.json"), rules: rules}
	writeJSON(t, f.manager.policy, f.policy)
	return f
}
