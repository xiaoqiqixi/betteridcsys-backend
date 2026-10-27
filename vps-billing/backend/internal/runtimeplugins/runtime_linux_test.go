package runtimeplugins

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/smtpv1"
	"aster/billing/pkg/pluginserver"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
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
	f.manifest = pluginapi.Manifest{Schema: 1, ID: "email.smtp", Version: "1.1.0", Protocol: pluginapi.SMTPProtocol, CoreMin: "0.4.0", CoreMaxExclusive: "0.6.0", Capabilities: []string{pluginapi.SMTPCapability}, Executable: "plugin", SHA256: digestBytes(binary)}
	raw := writeJSON(t, f.manifestPath, f.manifest)
	f.policy = pluginapi.Policy{Schema: 1, Revision: 1, SMTP: &pluginapi.Selection{Enabled: true, ID: "email.smtp", Version: "1.1.0", ManifestSHA256: digestBytes(raw), WorkerUID: uid, ClientUID: uid, Socket: filepath.Join(base, "rpc.sock"), AllowedTargets: []pluginapi.Target{{Host: "mail.example.test", Port: 465}}}}
	f.manager = &Manager{root: filepath.Join(base, "plugins"), policy: filepath.Join(base, "policy.json"), rules: rules}
	writeJSON(t, f.manager.policy, f.policy)
	return f
}
func (f *fixture) listen(t *testing.T) *net.UnixListener {
	t.Helper()
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: f.policy.SMTP.Socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(f.policy.SMTP.Socket, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}
func config() smtpv1.Config {
	return smtpv1.Config{Host: "mail.example.test", Port: 465, TLSMode: smtpv1.ImplicitTLS, Username: "user", Password: "secret-canary", FromAddress: "sender@example.test", FromName: "Aster"}
}
func message() smtpv1.Message {
	return smtpv1.Message{To: "to@example.test", Subject: "Test", Text: "Synthetic"}
}

type senderFunc func(context.Context, smtpv1.Config, smtpv1.Message) error

func (s senderFunc) Send(c context.Context, cfg smtpv1.Config, m smtpv1.Message) error {
	return s(c, cfg, m)
}
func (f *fixture) serve(t *testing.T, sender senderFunc) {
	t.Helper()
	l := f.listen(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (pluginserver.Server{Manifest: f.manifest, ClientUID: f.policy.SMTP.ClientUID, Targets: f.policy.SMTP.AllowedTargets, Sender: sender}).Serve(ctx, l)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("server failed to stop")
		}
	})
}

func TestApprovalRejectsUnsafeFiles(t *testing.T) {
	tests := map[string]func(*testing.T, *fixture){
		"binary modification":   func(t *testing.T, f *fixture) { os.WriteFile(f.binary, []byte("unapproved"), 0700) },
		"manifest modification": func(t *testing.T, f *fixture) { f.manifest.Version = "1.1.1"; writeJSON(t, f.manifestPath, f.manifest) },
		"writable binary":       func(t *testing.T, f *fixture) { os.Chmod(f.binary, 0777) },
		"writable parent":       func(t *testing.T, f *fixture) { os.Chmod(filepath.Dir(f.binary), 0777) },
		"hard link": func(t *testing.T, f *fixture) {
			if err := os.Link(f.binary, filepath.Join(f.base, "alias")); err != nil {
				t.Fatal(err)
			}
		},
		"symlink executable": func(t *testing.T, f *fixture) {
			os.Rename(f.binary, f.binary+".real")
			os.Symlink(f.binary+".real", f.binary)
		},
		"symlink parent": func(t *testing.T, f *fixture) {
			dir := filepath.Dir(f.binary)
			os.Rename(dir, dir+".real")
			os.Symlink(dir+".real", dir)
		},
		"symlink policy": func(t *testing.T, f *fixture) {
			os.Rename(f.manager.policy, f.manager.policy+".real")
			os.Symlink(f.manager.policy+".real", f.manager.policy)
		},
		"case alias": func(t *testing.T, f *fixture) {
			os.WriteFile(f.manager.policy, []byte(`{"Schema":1,"revision":1}`), 0600)
		},
		"noncanonical root": func(t *testing.T, f *fixture) { f.manager.root += "/../plugins" },
		"incompatible": func(t *testing.T, f *fixture) {
			f.manifest.CoreMin = "0.6.0"
			raw := writeJSON(t, f.manifestPath, f.manifest)
			f.policy.SMTP.ManifestSHA256 = digestBytes(raw)
			writeJSON(t, f.manager.policy, f.policy)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if f.manager.State().Status != "approved" {
				t.Fatal("fixture not approved")
			}
			mutate(t, f)
			if f.manager.State().Status != "unavailable" {
				t.Fatal("unsafe installation accepted")
			}
		})
	}
}

func TestStateCompatibilityRequiresVerifiedApproval(t *testing.T) {
	f := newFixture(t)
	state := f.manager.State()
	if state.SMTPProtocol != pluginapi.SMTPProtocol || state.SMTPCoreMin != "0.4.0" || state.SMTPCoreMaxExclusive != "0.6.0" {
		t.Fatal("approved state omitted verified compatibility", state)
	}
	f.policy.SMTP.Enabled = false
	writeJSON(t, f.manager.policy, f.policy)
	os.Remove(f.binary)
	state = f.manager.State()
	if state.Status != "disabled" || state.Version != "1.1.0" || state.SMTPProtocol != "" || state.SMTPCoreMin != "" || state.SMTPCoreMaxExclusive != "" {
		t.Fatal("disabled policy guessed compatibility or read missing binary", state)
	}
	f.policy.SMTP.Enabled = true
	writeJSON(t, f.manager.policy, f.policy)
	state = f.manager.State()
	if state.Status != "unavailable" || state.SMTPProtocol != "" || state.SMTPCoreMin != "" || state.SMTPCoreMaxExclusive != "" {
		t.Fatal("unavailable state claimed verified compatibility", state)
	}
}
func TestDisabledAndPassiveDiscovery(t *testing.T) {
	f := newFixture(t)
	if got, err := f.manager.Discover(); err != nil || len(got) != 1 || got[0].Status != "discovered" {
		t.Fatal(got, err)
	}
	// The executable is deliberately not executable machine code. Discovery and
	// approval never launch it, and a disabled selection does not even hash it.
	f.policy.SMTP.Enabled = false
	writeJSON(t, f.manager.policy, f.policy)
	os.Remove(f.binary)
	if state := f.manager.State(); state.Status != "disabled" || state.Version != "1.1.0" || state.ID != "email.smtp" || state.PolicyRevision != 1 {
		t.Fatal("disabled plugin not reported")
	}
	if _, err := f.manager.Check(context.Background()); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if err := validatePolicy(pluginapi.Policy{Schema: 1, Revision: 1}, trust{}); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	f.policy.SMTP.Enabled = true
	if validatePolicy(f.policy, trust{}) == nil {
		t.Fatal("production must reject shared UID")
	}
}
func TestRuntimeSendAndErrorRedaction(t *testing.T) {
	for name, returned := range map[string]error{"accepted": nil, "rejected": &smtpv1.Error{Code: smtpv1.CodeRejected}, "unknown": errors.New("secret-canary"), "unknown typed": &smtpv1.Error{Code: "secret-canary"}, "uncertain": &smtpv1.Error{Code: smtpv1.CodeDeliveryUncertain, DeliveryUncertain: true}} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			var sends atomic.Int32
			f.serve(t, func(_ context.Context, cfg smtpv1.Config, _ smtpv1.Message) error {
				if cfg.Password != "secret-canary" {
					t.Error("credential lost")
				}
				sends.Add(1)
				return returned
			})
			id, err := f.manager.Check(context.Background())
			if err != nil || id.Version != "1.1.0" {
				t.Fatal(id, err)
			}
			if sends.Load() != 0 {
				t.Fatal("check sent mail")
			}
			err = f.manager.Send(context.Background(), config(), message())
			if returned == nil {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var safe *smtpv1.Error
				if !errors.As(err, &safe) || strings.Contains(err.Error(), "secret-canary") {
					t.Fatal("unsafe error", err)
				}
				wantUncertain := name != "rejected"
				if safe.DeliveryUncertain != wantUncertain {
					t.Fatal(safe)
				}
			}
			if sends.Load() != 1 {
				t.Fatal("send duplicated", sends.Load())
			}
		})
	}
}
func TestUnapprovedDestinationNeverConnects(t *testing.T) {
	f := newFixture(t)
	var sends atomic.Int32
	f.serve(t, func(context.Context, smtpv1.Config, smtpv1.Message) error { sends.Add(1); return nil })
	cfg := config()
	cfg.Host = "other.example.test"
	if f.manager.Send(context.Background(), cfg, message()) == nil || sends.Load() != 0 {
		t.Fatal("target escaped approval")
	}
}
func TestHandshakeRejectsReplacementBeforeCredentials(t *testing.T) {
	for _, mode := range []string{"wrong identity", "disconnect", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			l := f.listen(t)
			var sends, connections atomic.Int32
			srv := &http.Server{ConnState: func(_ net.Conn, s http.ConnState) {
				if s == http.StateNew {
					connections.Add(1)
				}
			}, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				if strings.Contains(string(raw), "secret-canary") {
					sends.Add(1)
				}
				var hello pluginapi.Hello
				json.Unmarshal(raw, &hello)
				w.Header().Set("Content-Type", "application/json")
				id := pluginapi.Identity{Protocol: f.manifest.Protocol, ID: f.manifest.ID, Version: f.manifest.Version, SHA256: f.manifest.SHA256, Capabilities: f.manifest.Capabilities, Nonce: hello.Nonce}
				if mode == "wrong identity" {
					id.SHA256 = strings.Repeat("0", 64)
				}
				if mode == "oversized" {
					id.Nonce = strings.Repeat("x", pluginapi.MaxResponseBytes+1)
				}
				if mode == "disconnect" {
					w.Header().Set("Connection", "close")
				}
				json.NewEncoder(w).Encode(id)
			})}
			go srv.Serve(l)
			t.Cleanup(func() { srv.Close() })
			err := f.manager.Send(context.Background(), config(), message())
			if err == nil || sends.Load() != 0 || connections.Load() != 1 {
				t.Fatal("credentials crossed failed handshake or connection redialed", err, sends.Load(), connections.Load())
			}
		})
	}
}
func TestCrashAndTimeoutDoNotRetry(t *testing.T) {
	for _, mode := range []string{"panic", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			var sends atomic.Int32
			f.serve(t, func(ctx context.Context, _ smtpv1.Config, _ smtpv1.Message) error {
				sends.Add(1)
				if mode == "panic" {
					panic("secret-canary")
				}
				<-ctx.Done()
				return &smtpv1.Error{Code: smtpv1.CodeDeliveryUncertain, DeliveryUncertain: true}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			start := time.Now()
			err := f.manager.Send(ctx, config(), message())
			var safe *smtpv1.Error
			if !errors.As(err, &safe) || !safe.DeliveryUncertain || sends.Load() != 1 || time.Since(start) > time.Second {
				t.Fatal(err, sends.Load(), time.Since(start))
			}
			if f.manager.State().Status != "approved" {
				t.Fatal("core state damaged")
			}
		})
	}
}
func TestCircuitBreakerAndRecovery(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 3; i++ {
		if f.manager.Send(context.Background(), config(), message()) == nil {
			t.Fatal("missing server accepted")
		}
	}
	if f.manager.blockedUntil.IsZero() {
		t.Fatal("circuit not opened")
	}
	f.serve(t, func(context.Context, smtpv1.Config, smtpv1.Message) error { return nil })
	if f.manager.Send(context.Background(), config(), message()) == nil {
		t.Fatal("cooldown ignored")
	}
	f.manager.mu.Lock()
	f.manager.blockedUntil = time.Now().Add(-time.Second)
	f.manager.mu.Unlock()
	if err := f.manager.Send(context.Background(), config(), message()); err != nil {
		t.Fatal(err)
	}
}

func socketClient(f *fixture) *http.Client {
	return &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", f.policy.SMTP.Socket)
	}}}
}
func TestServerRequiresSameConnectionAndOneSend(t *testing.T) {
	f := newFixture(t)
	var sends atomic.Int32
	f.serve(t, func(context.Context, smtpv1.Config, smtpv1.Message) error { sends.Add(1); return nil })
	nonce := strings.Repeat("a", 64)
	request := pluginapi.SendRequest{Protocol: pluginapi.SMTPProtocol, Version: "1.1.0", RequestID: nonce, TimeoutMS: 1000, Config: config(), Message: message()}
	unhandshaken := socketClient(f)
	defer unhandshaken.CloseIdleConnections()
	var out pluginapi.SendResponse
	if exchange(context.Background(), unhandshaken, "/v1/smtp/send", request, &out) == nil || sends.Load() != 0 {
		t.Fatal("send accepted without handshake")
	}
	client := socketClient(f)
	defer client.CloseIdleConnections()
	var identity pluginapi.Identity
	if err := exchange(context.Background(), client, "/v1/handshake", pluginapi.Hello{Protocol: pluginapi.SMTPProtocol, CoreVersion: pluginapi.CoreVersion, Nonce: nonce}, &identity); err != nil {
		t.Fatal(err)
	}
	if exchange(context.Background(), unhandshaken, "/v1/smtp/send", request, &out) == nil || sends.Load() != 0 {
		t.Fatal("handshake transferred to another connection")
	}
	if err := exchange(context.Background(), client, "/v1/smtp/send", request, &out); err != nil || out.Status != "accepted" {
		t.Fatal(err, out)
	}
	if exchange(context.Background(), client, "/v1/smtp/send", request, &out) == nil || sends.Load() != 1 {
		t.Fatal("replayed send accepted")
	}
}
func TestServerRejectsWrongPeerUID(t *testing.T) {
	f := newFixture(t)
	l := f.listen(t)
	var sends atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- (pluginserver.Server{Manifest: f.manifest, ClientUID: uint32(os.Geteuid()) + 1, Targets: f.policy.SMTP.AllowedTargets, Sender: senderFunc(func(context.Context, smtpv1.Config, smtpv1.Message) error { sends.Add(1); return nil })}).Serve(ctx, l)
	}()
	client := socketClient(f)
	defer client.CloseIdleConnections()
	check, stop := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer stop()
	var identity pluginapi.Identity
	if exchange(check, client, "/v1/handshake", pluginapi.Hello{Protocol: pluginapi.SMTPProtocol, CoreVersion: pluginapi.CoreVersion, Nonce: strings.Repeat("a", 64)}, &identity) == nil || sends.Load() != 0 {
		t.Fatal("wrong UID accepted")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}
func TestMalformedSendResponseConservativelyUncertain(t *testing.T) {
	for _, mutation := range []string{"nonce", "unknown code", "false uncertainty", "invalid accepted"} {
		t.Run(mutation, func(t *testing.T) {
			f := newFixture(t)
			l := f.listen(t)
			var sends atomic.Int32
			srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/handshake" {
					var h pluginapi.Hello
					json.NewDecoder(r.Body).Decode(&h)
					json.NewEncoder(w).Encode(pluginapi.Identity{Protocol: f.manifest.Protocol, ID: f.manifest.ID, Version: f.manifest.Version, SHA256: f.manifest.SHA256, Capabilities: f.manifest.Capabilities, Nonce: h.Nonce})
					return
				}
				sends.Add(1)
				var request pluginapi.SendRequest
				json.NewDecoder(r.Body).Decode(&request)
				out := pluginapi.SendResponse{Protocol: pluginapi.SMTPProtocol, RequestID: request.RequestID, Status: "failed", Code: smtpv1.CodeRejected}
				switch mutation {
				case "nonce":
					out.RequestID = "wrong"
				case "unknown code":
					out.Code = "secret-canary"
				case "false uncertainty":
					out.Code = smtpv1.CodeDeliveryUncertain
				case "invalid accepted":
					out.Status = "accepted"
				}
				json.NewEncoder(w).Encode(out)
			})}
			go srv.Serve(l)
			t.Cleanup(func() { srv.Close() })
			var safe *smtpv1.Error
			err := f.manager.Send(context.Background(), config(), message())
			if !errors.As(err, &safe) || !safe.DeliveryUncertain || sends.Load() != 1 {
				t.Fatal(err, sends.Load())
			}
		})
	}
}

func TestMaximumValidatedTextFitsWireEnvelope(t *testing.T) {
	f := newFixture(t)
	text := strings.Repeat("<", smtpv1.MaxTextBytes)
	var sends atomic.Int32
	f.serve(t, func(_ context.Context, _ smtpv1.Config, m smtpv1.Message) error {
		if m.Text != text {
			t.Error("text changed")
		}
		sends.Add(1)
		return nil
	})
	m := message()
	m.Text = text
	if err := f.manager.Send(context.Background(), config(), m); err != nil || sends.Load() != 1 {
		t.Fatal(err, sends.Load())
	}
}
