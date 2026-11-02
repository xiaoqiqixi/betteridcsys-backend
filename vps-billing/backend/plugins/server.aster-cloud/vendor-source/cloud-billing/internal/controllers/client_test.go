package controllers

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func privateFixture(t *testing.T) string {
	t.Helper()
	base := os.TempDir()
	if _, err := os.Stat("/private/tmp"); err == nil {
		base = "/private/tmp"
	}
	dir, err := os.MkdirTemp(base, "aster-controller-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
func writeFixture(t *testing.T, path string, raw []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, raw, mode); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedHostPolicy(t *testing.T) {
	dir := privateFixture(t)
	token := filepath.Join(dir, "token")
	path := filepath.Join(dir, "policy.json")
	writeFixture(t, token, []byte("a-private-test-token-not-production"), 0600)
	p := Policy{Schema: 1, Controllers: []Profile{{ID: "primary", Enabled: false, Endpoint: "https://controller.invalid/aster/v1/provision", AllowedCIDRs: []string{"192.0.2.0/24"}, TokenFile: token, CAFile: ""}}}
	raw, _ := json.Marshal(p)
	writeFixture(t, path, raw, 0600)
	owner := uint32(os.Geteuid())
	if _, err := loadPolicy(path, owner); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]string{
		"duplicate":             `{"schema":1,"schema":1,"controllers":[]}`,
		"case alias":            `{"Schema":1,"controllers":[]}`,
		"null":                  `{"schema":1,"controllers":null}`,
		"trailing":              string(raw) + `{}`,
		"missing profile field": strings.Replace(string(raw), `,"ca_file":""`, "", 1),
		"unknown":               strings.Replace(string(raw), `"enabled":false`, `"enabled":false,"secret":"no"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			writeFixture(t, path, []byte(bad), 0600)
			if _, err := loadPolicy(path, owner); err != ErrUnavailable {
				t.Fatal("invalid policy", err)
			}
		})
	}
	writeFixture(t, path, raw, 0600)
	for _, mode := range []os.FileMode{0644, 0620, 0666} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := loadPolicy(path, owner); err != ErrUnavailable {
			t.Fatal("shared policy accepted", mode, err)
		}
	}
	_ = os.Chmod(path, 0600)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readHostFile(link, true, 65536, owner); err != ErrUnavailable {
		t.Fatal("symlink accepted")
	}
	hard := filepath.Join(dir, "hard")
	if err := os.Link(path, hard); err != nil {
		t.Fatal(err)
	}
	if _, err := readHostFile(path, true, 65536, owner); err != ErrUnavailable {
		t.Fatal("hardlink accepted")
	}
	_ = os.Remove(hard)
	if _, err := readHostFile(path, true, 1, owner); err != ErrUnavailable {
		t.Fatal("size bound")
	}
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := readHostFile(path, true, 65536, owner); err != ErrUnavailable {
		t.Fatal("shared writable parent")
	}
	_ = os.Chmod(dir, 0700)
}

func TestEndpointPolicy(t *testing.T) {
	p := Profile{ID: "primary", Endpoint: "https://controller.invalid/aster/v1/provision", AllowedCIDRs: []string{"192.0.2.0/24"}}
	for _, endpoint := range []string{"http://controller.invalid/api", "https://user:secret@controller.invalid/api", "https://controller.invalid/", "https://controller.invalid/api?secret=x", "https://controller.invalid/api#fragment", "https://controller.invalid/a/../api", "https://controller.invalid/a//b", "https://controller.invalid/%61pi", "https://controller.invalid:0443/api", "https://controller.invalid:65536/api"} {
		p.Endpoint = endpoint
		if _, _, ok := p.endpoint(); ok {
			t.Fatal("unsafe endpoint", endpoint)
		}
	}
	p.Endpoint = "https://controller.invalid/aster/v1/provision"
	for _, cidr := range []string{"0.0.0.0/0", "::/0", "192.0.2.1/24", "::ffff:192.0.2.0/120", "invalid"} {
		p.AllowedCIDRs = []string{cidr}
		if _, _, ok := p.endpoint(); ok {
			t.Fatal("unsafe CIDR", cidr)
		}
	}
}

func tlsFixture(t *testing.T, handler http.Handler, connections *atomic.Int32) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	if connections != nil {
		server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
			if state == http.StateNew {
				connections.Add(1)
			}
		}
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	dir := privateFixture(t)
	token := filepath.Join(dir, "token")
	ca := filepath.Join(dir, "ca.pem")
	writeFixture(t, token, []byte("a-private-test-token-not-production"), 0600)
	writeFixture(t, ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600)
	p := Policy{Schema: 1, Controllers: []Profile{{ID: "primary", Enabled: true, Endpoint: server.URL + "/aster/v1/provision", AllowedCIDRs: []string{"127.0.0.0/8"}, TokenFile: token, CAFile: ca}}}
	c, err := p.client(context.Background(), "primary", uint32(os.Geteuid()), func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, server
}

func TestTLSTransportAndBound409(t *testing.T) {
	r := fixtureRequest()
	raw, hash, _ := r.BytesAndDigest()
	var calls, connections atomic.Int32
	c, _ := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if req.Header.Get("Authorization") != "Bearer a-private-test-token-not-production" || req.Header.Get("Idempotency-Key") != r.OperationID || req.Header.Get("X-Aster-Request-SHA256") != hash {
			t.Error("missing authentication/binding")
		}
		body, _ := io.ReadAll(req.Body)
		if req.Method == "POST" {
			if req.URL.Path != "/aster/v1/provision" || string(body) != string(raw) {
				t.Error("wrong immutable request")
			}
		} else if req.Method != "GET" || req.URL.Path != "/aster/v1/provision/"+r.OperationID || len(body) != 0 {
			t.Error("lookup must be read-only and operation-bound")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(409)
		_ = json.NewEncoder(w).Encode(fixtureResult(r))
	}), &connections)
	// Even environment proxy settings must not route private credentials elsewhere.
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	if got, err := c.Send(context.Background(), r); err != nil || !got.Matches(r) {
		t.Fatal(got, err)
	}
	if got, err := c.Lookup(context.Background(), r); err != nil || !got.Matches(r) {
		t.Fatal(got, err)
	}
	if calls.Load() != 2 || connections.Load() != 2 {
		t.Fatal("unexpected retry or reused physical connection", calls.Load(), connections.Load())
	}
}

func TestTLSRejectsAmbiguousResponsesWithoutReplay(t *testing.T) {
	r := fixtureRequest()
	good, _ := json.Marshal(fixtureResult(r))
	cases := []struct {
		name, body, content string
		status              int
		drop                bool
	}{
		{"truncated", string(good[:20]), "application/json", 200, false},
		{"duplicate", strings.Replace(string(good), `"status":`, `"status":"accepted","status":`, 1), "application/json", 200, false},
		{"wrong case", strings.Replace(string(good), `"protocol":`, `"Protocol":`, 1), "application/json", 200, false},
		{"wrong binding", strings.Replace(string(good), r.OrderID, strings.Repeat("d", 32), 1), "application/json", 200, false},
		{"null", strings.Replace(string(good), `"vm-1"`, `null`, 1), "application/json", 200, false},
		{"oversize", string(good) + strings.Repeat(" ", 16385), "application/json", 200, false},
		{"HTML", string(good), "text/html", 200, false},
		{"redirect", string(good), "application/json", 307, false},
		{"unbound conflict", `{}`, "application/json", 409, false},
		{"server error", string(good), "application/json", 500, false},
		{"lost response", "", "application/json", 200, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls, connections atomic.Int32
			c, _ := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls.Add(1)
				if tc.drop {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				w.Header().Set("Content-Type", tc.content)
				w.Header().Set("Location", "https://example.invalid/steal")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}), &connections)
			if _, err := c.Send(context.Background(), r); err != ErrUncertain {
				t.Fatal("ambiguous response not fenced", err)
			}
			if calls.Load() != 1 || connections.Load() != 1 {
				t.Fatal("silent retry", calls.Load(), connections.Load())
			}
		})
	}
}

func TestTLSVerificationAndPinnedDNS(t *testing.T) {
	r := fixtureRequest()
	var calls atomic.Int32
	c, _ := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(fixtureResult(r))
	}), nil)
	c.tls.ServerName = "wrong-host.invalid"
	if _, err := c.Send(context.Background(), r); err != ErrUncertain || calls.Load() != 0 {
		t.Fatal("TLS hostname verification bypassed", err, calls.Load())
	}
	dir := privateFixture(t)
	token := filepath.Join(dir, "token")
	writeFixture(t, token, []byte("a-private-test-token-not-production"), 0600)
	p := Policy{Schema: 1, Controllers: []Profile{{ID: "primary", Enabled: true, Endpoint: "https://controller.invalid/api", AllowedCIDRs: []string{"192.0.2.0/24"}, TokenFile: token}}}
	for _, addresses := range [][]netip.Addr{{}, {netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("127.0.0.1")}, {netip.MustParseAddr("169.254.1.1")}} {
		_, err := p.client(context.Background(), "primary", uint32(os.Geteuid()), func(context.Context, string, string) ([]netip.Addr, error) { return addresses, nil })
		if err != ErrUnavailable {
			t.Fatal("unallowlisted DNS accepted", addresses, err)
		}
	}
	p.Controllers[0].Enabled = false
	_, err := p.client(context.Background(), "primary", uint32(os.Geteuid()), func(context.Context, string, string) ([]netip.Addr, error) {
		t.Error("disabled target resolved")
		return nil, fmt.Errorf("never")
	})
	if err != ErrUnavailable {
		t.Fatal(err)
	}
}
