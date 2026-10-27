package runtimeplugins

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"aster/billing/pkg/pluginserver"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newPaymentFixture(t *testing.T) *fixture {
	f := newFixture(t)
	old := filepath.Dir(filepath.Dir(f.binary))
	next := filepath.Join(filepath.Dir(old), "payment.fixture")
	if e := os.Rename(old, next); e != nil {
		t.Fatal(e)
	}
	f.binary = filepath.Join(next, "1.1.0", "plugin")
	f.manifestPath = filepath.Join(next, "1.1.0", "manifest.json")
	f.manifest.ID = "payment.fixture"
	f.manifest.CoreMin = pluginapi.PaymentMinCoreVersion
	f.manifest.Protocol = pluginapi.PaymentProtocol
	f.manifest.Capabilities = pluginapi.PaymentCapabilities()
	f.manifest.Payment = &paymentv1.Declarations{Currencies: []paymentv1.Currency{{Code: "CNY", Precision: 2}, {Code: "JPY", Precision: 0}}, CheckoutHosts: []string{"checkout.example.test"}}
	raw := writeJSON(t, f.manifestPath, f.manifest)
	s := *f.policy.SMTP
	s.ID = f.manifest.ID
	s.MerchantID = "synthetic-merchant"
	s.ManifestSHA256 = digestBytes(raw)
	s.AllowedTargets = []pluginapi.Target{{Host: "gateway.example.test", Port: 443}}
	f.policy.SMTP = nil
	f.policy.Payment = &s
	writeJSON(t, f.manager.policy, f.policy)
	return f
}
func (f *fixture) paymentConfig() paymentv1.Config {
	s := f.policy.Payment
	return paymentv1.Config{MerchantID: s.MerchantID, ProviderID: s.ID, ManifestSHA256: s.ManifestSHA256}
}
func paymentIntent() paymentv1.Intent {
	return paymentv1.Intent{AttemptID: "attempt-1", IdempotencyKey: "attempt-1", InvoiceID: "invoice-1", OrderID: "order-1", AccountID: "account-1", AmountMinor: 1200, Currency: "CNY", CurrencyDecimalPlaces: 2}
}
func paymentCallback() paymentv1.Callback {
	return paymentv1.Callback{Method: "POST", Path: "/callback", Body: []byte(`{}`)}
}
func paymentCheckout() paymentv1.Checkout {
	return paymentv1.Checkout{ProviderID: "payment.fixture", SessionID: "session-1", URL: "https://checkout.example.test/session-1", ExpiresAt: time.Now().Add(time.Hour)}
}
func paymentEvent() paymentv1.VerifiedEvent {
	return paymentv1.VerifiedEvent{AttemptID: "attempt-1", ProviderID: "payment.fixture", EventID: "event-1", TransactionID: "tx-1", MerchantAccountID: "synthetic-merchant", AccountID: "account-1", OrderID: "order-1", InvoiceID: "invoice-1", AmountMinor: 1200, Currency: "CNY", CurrencyDecimalPlaces: 2, SettledAt: time.Now().UTC(), Status: "settled"}
}

type paymentProcessor struct {
	create func(context.Context, paymentv1.Config, paymentv1.Intent) (paymentv1.Checkout, error)
	verify func(context.Context, paymentv1.Config, paymentv1.Callback) (paymentv1.VerifiedEvent, error)
}

func (p paymentProcessor) Create(c context.Context, cfg paymentv1.Config, in paymentv1.Intent) (paymentv1.Checkout, error) {
	if p.create == nil {
		return paymentCheckout(), nil
	}
	return p.create(c, cfg, in)
}
func (p paymentProcessor) Verify(c context.Context, cfg paymentv1.Config, in paymentv1.Callback) (paymentv1.VerifiedEvent, error) {
	if p.verify == nil {
		return paymentEvent(), nil
	}
	return p.verify(c, cfg, in)
}
func (f *fixture) paymentListen(t *testing.T) *net.UnixListener {
	l, e := net.ListenUnix("unix", &net.UnixAddr{Name: f.policy.Payment.Socket, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(f.policy.Payment.Socket, 0600); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { l.Close() })
	return l
}
func (f *fixture) paymentServe(t *testing.T, p paymentv1.Processor) {
	l := f.paymentListen(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (pluginserver.PaymentServer{Manifest: f.manifest, ClientUID: f.policy.Payment.ClientUID, MerchantID: f.policy.Payment.MerchantID, Processor: p}).Serve(ctx, l)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(3 * time.Second):
			t.Error("payment server failed to stop")
		}
	})
}
func TestPaymentRuntimeContract(t *testing.T) {
	f := newPaymentFixture(t)
	var creates, verifies atomic.Int32
	f.paymentServe(t, paymentProcessor{create: func(_ context.Context, cfg paymentv1.Config, in paymentv1.Intent) (paymentv1.Checkout, error) {
		creates.Add(1)
		if cfg != f.paymentConfig() || in != paymentIntent() {
			t.Error("request changed")
		}
		return paymentCheckout(), nil
	}, verify: func(_ context.Context, _ paymentv1.Config, in paymentv1.Callback) (paymentv1.VerifiedEvent, error) {
		verifies.Add(1)
		if string(in.Body) != "{}" {
			t.Error("raw callback changed")
		}
		return paymentEvent(), nil
	}})
	if s := f.manager.PaymentState(); s.Status != "approved" || s.Protocol != pluginapi.PaymentProtocol {
		t.Fatal(s)
	}
	if id, e := f.manager.PaymentCheck(context.Background()); e != nil || id.ID != "payment.fixture" {
		t.Fatal(id, e)
	}
	if creates.Load() != 0 || verifies.Load() != 0 {
		t.Fatal("check performed payment")
	}
	if out, e := f.manager.CreatePayment(context.Background(), f.paymentConfig(), paymentIntent()); e != nil || out.SessionID != "session-1" {
		t.Fatal(out, e)
	}
	if out, e := f.manager.VerifyPaymentCallback(context.Background(), f.paymentConfig(), paymentCallback()); e != nil || out.TransactionID != "tx-1" {
		t.Fatal(out, e)
	}
	if creates.Load() != 1 || verifies.Load() != 1 {
		t.Fatal("duplicated operation")
	}
	if got, e := f.manager.Discover(); e != nil || len(got) != 1 || got[0].Protocol != pluginapi.PaymentProtocol {
		t.Fatal(got, e)
	}
	f.policy.Payment.Enabled = false
	writeJSON(t, f.manager.policy, f.policy)
	os.Remove(f.binary)
	if s := f.manager.PaymentState(); s.Status != "disabled" || s.ID != "payment.fixture" {
		t.Fatal(s)
	}
	if _, e := f.manager.PaymentCheck(context.Background()); !errors.Is(e, ErrDisabled) {
		t.Fatal(e)
	}
}
func TestPaymentPinsAndCurrencyBeforeDispatch(t *testing.T) {
	for _, mode := range []string{"merchant", "provider", "manifest", "currency", "precision", "attempt"} {
		t.Run(mode, func(t *testing.T) {
			f := newPaymentFixture(t)
			var calls atomic.Int32
			f.paymentServe(t, paymentProcessor{create: func(context.Context, paymentv1.Config, paymentv1.Intent) (paymentv1.Checkout, error) {
				calls.Add(1)
				return paymentCheckout(), nil
			}})
			cfg, in := f.paymentConfig(), paymentIntent()
			switch mode {
			case "merchant":
				cfg.MerchantID = "other"
			case "provider":
				cfg.ProviderID = "payment.other"
			case "manifest":
				cfg.ManifestSHA256 = strings.Repeat("0", 64)
			case "currency":
				in.Currency = "USD"
			case "precision":
				in.CurrencyDecimalPlaces = 0
			case "attempt":
				in.IdempotencyKey = "other"
			}
			if _, e := f.manager.CreatePayment(context.Background(), cfg, in); e == nil || calls.Load() != 0 {
				t.Fatal("unsafe dispatch", e, calls.Load())
			}
		})
	}
}

func TestPaymentPreservesConfiguredHTTPSCallbackOrigin(t *testing.T) {
	for _, origin := range []string{"https://billing.example.test:8443", "https://127.0.0.1:8443", "https://[::1]:8443"} {
		t.Run(origin, func(t *testing.T) {
			f := newPaymentFixture(t)
			var got atomic.Value
			f.paymentServe(t, paymentProcessor{create: func(_ context.Context, _ paymentv1.Config, in paymentv1.Intent) (paymentv1.Checkout, error) {
				got.Store(in.CallbackURL)
				return paymentCheckout(), nil
			}})
			in := paymentIntent()
			in.CallbackURL = origin + "/api/v1/payment-callbacks/payment.fixture"
			if _, e := f.manager.CreatePayment(context.Background(), f.paymentConfig(), in); e != nil || got.Load() != in.CallbackURL {
				t.Fatal("configured callback origin changed or rejected", e, got.Load())
			}
		})
	}
}
func TestPaymentApprovalRejectsMetadata(t *testing.T) {
	for _, mode := range []string{"currency", "precision", "host", "capability", "protocol", "merchant", "cross-slot"} {
		t.Run(mode, func(t *testing.T) {
			f := newPaymentFixture(t)
			switch mode {
			case "currency":
				f.manifest.Payment.Currencies = append(f.manifest.Payment.Currencies, f.manifest.Payment.Currencies[0])
			case "precision":
				f.manifest.Payment.Currencies[0].Precision = 7
			case "host":
				f.manifest.Payment.CheckoutHosts = []string{"*.example.test"}
			case "capability":
				f.manifest.Capabilities = append(f.manifest.Capabilities, "database.write")
			case "protocol":
				f.manifest.Protocol = "aster.payment/2"
			case "merchant":
				f.policy.Payment.MerchantID = "bad merchant"
			case "cross-slot":
				s := *f.policy.Payment
				s.ID = "email.smtp"
				s.MerchantID = ""
				f.policy.SMTP = &s
			}
			raw := writeJSON(t, f.manifestPath, f.manifest)
			f.policy.Payment.ManifestSHA256 = digestBytes(raw)
			writeJSON(t, f.manager.policy, f.policy)
			if s := f.manager.PaymentState(); s.Status != "unavailable" {
				t.Fatal("unsafe metadata accepted", s)
			}
		})
	}
}
func TestPaymentCrashTimeoutAndCircuit(t *testing.T) {
	for _, mode := range []string{"panic", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			f := newPaymentFixture(t)
			var calls atomic.Int32
			f.paymentServe(t, paymentProcessor{create: func(ctx context.Context, _ paymentv1.Config, _ paymentv1.Intent) (paymentv1.Checkout, error) {
				calls.Add(1)
				if mode == "panic" {
					panic("secret-canary")
				}
				<-ctx.Done()
				return paymentv1.Checkout{}, errors.New("secret-canary")
			}})
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			start := time.Now()
			_, e := f.manager.CreatePayment(ctx, f.paymentConfig(), paymentIntent())
			if e == nil || strings.Contains(e.Error(), "secret-canary") || calls.Load() != 1 || time.Since(start) > time.Second {
				t.Fatal(e, calls.Load(), time.Since(start))
			}
			if f.manager.PaymentState().Status != "approved" {
				t.Fatal("core damaged by plugin failure")
			}
		})
	}
	f := newPaymentFixture(t)
	for i := 0; i < 3; i++ {
		f.manager.CreatePayment(context.Background(), f.paymentConfig(), paymentIntent())
	}
	if f.manager.paymentBlockedUntil.IsZero() {
		t.Fatal("circuit did not open")
	}
	f.paymentServe(t, paymentProcessor{})
	if _, e := f.manager.CreatePayment(context.Background(), f.paymentConfig(), paymentIntent()); e == nil {
		t.Fatal("cooldown ignored")
	}
	f.manager.paymentMu.Lock()
	f.manager.paymentBlockedUntil = time.Now().Add(-time.Second)
	f.manager.paymentMu.Unlock()
	if _, e := f.manager.CreatePayment(context.Background(), f.paymentConfig(), paymentIntent()); e != nil {
		t.Fatal(e)
	}
}
func TestPaymentClientRejectsForgedEvidence(t *testing.T) {
	for _, mode := range []string{"merchant", "provider", "precision", "transaction", "future", "status", "nonce", "host", "missing outcome"} {
		t.Run(mode, func(t *testing.T) {
			f := newPaymentFixture(t)
			l := f.paymentListen(t)
			var calls atomic.Int32
			srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/handshake" {
					var h pluginapi.Hello
					json.NewDecoder(r.Body).Decode(&h)
					json.NewEncoder(w).Encode(pluginapi.Identity{Protocol: f.manifest.Protocol, ID: f.manifest.ID, Version: f.manifest.Version, SHA256: f.manifest.SHA256, Capabilities: f.manifest.Capabilities, Nonce: h.Nonce})
					return
				}
				calls.Add(1)
				if mode == "host" {
					var in pluginapi.PaymentCreateRequest
					json.NewDecoder(r.Body).Decode(&in)
					out := paymentCheckout()
					out.URL = "https://unapproved.example.test"
					json.NewEncoder(w).Encode(pluginapi.PaymentCreateResponse{Protocol: pluginapi.PaymentProtocol, RequestID: in.RequestID, Status: "accepted", Checkout: &out})
					return
				}
				var in pluginapi.PaymentVerifyRequest
				json.NewDecoder(r.Body).Decode(&in)
				event := paymentEvent()
				out := pluginapi.PaymentVerifyResponse{Protocol: pluginapi.PaymentProtocol, RequestID: in.RequestID, Status: "verified", Event: &event}
				switch mode {
				case "merchant":
					event.MerchantAccountID = "other"
				case "provider":
					event.ProviderID = "payment.other"
				case "precision":
					event.CurrencyDecimalPlaces = 0
				case "transaction":
					event.TransactionID = ""
				case "future":
					event.SettledAt = time.Now().Add(time.Hour)
				case "status":
					event.Status = "paid"
				case "nonce":
					out.RequestID = strings.Repeat("0", 64)
				case "missing outcome":
					out.Status = ""
				}
				json.NewEncoder(w).Encode(out)
			})}
			go srv.Serve(l)
			t.Cleanup(func() { srv.Close() })
			var e error
			if mode == "host" {
				_, e = f.manager.CreatePayment(context.Background(), f.paymentConfig(), paymentIntent())
			} else {
				_, e = f.manager.VerifyPaymentCallback(context.Background(), f.paymentConfig(), paymentCallback())
			}
			if e == nil || calls.Load() != 1 {
				t.Fatal("forged evidence accepted", e, calls.Load())
			}
		})
	}
}
func TestPaymentBoundedConcurrency(t *testing.T) {
	f := newPaymentFixture(t)
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	defer close(release)
	f.paymentServe(t, paymentProcessor{create: func(ctx context.Context, _ paymentv1.Config, _ paymentv1.Intent) (paymentv1.Checkout, error) {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return paymentCheckout(), nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < 8; i++ {
		go f.manager.CreatePayment(ctx, f.paymentConfig(), paymentIntent())
	}
	for i := 0; i < 8; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("calls did not start")
		}
	}
	start := time.Now()
	if _, e := f.manager.CreatePayment(context.Background(), f.paymentConfig(), paymentIntent()); e == nil || time.Since(start) > time.Second {
		t.Fatal("busy worker did not reject promptly", e)
	}
}

func TestPaymentApprovalSharesRPCCapacityAndRecovers(t *testing.T) {
	f := newPaymentFixture(t)
	started := make(chan struct{}, 8)
	finish := make(chan struct{})
	var finishOnce sync.Once
	releaseCalls := func() { finishOnce.Do(func() { close(finish) }) }
	defer releaseCalls()
	f.paymentServe(t, paymentProcessor{create: func(ctx context.Context, _ paymentv1.Config, _ paymentv1.Intent) (paymentv1.Checkout, error) {
		started <- struct{}{}
		select {
		case <-finish:
		case <-ctx.Done():
		}
		return paymentCheckout(), nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls sync.WaitGroup
	results := make(chan error, 8)
	for n := 0; n < 8; n++ {
		calls.Add(1)
		go func() {
			defer calls.Done()
			_, e := f.manager.CreatePayment(ctx, f.paymentConfig(), paymentIntent())
			results <- e
		}()
	}
	for n := 0; n < 8; n++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("payment calls did not fill capacity")
		}
	}
	// Every RPC has finished its approval read before signaling started. Empty
	// paths would yield ErrDisabled if either method reached approval loading.
	root, policy := f.manager.root, f.manager.policy
	f.manager.root, f.manager.policy = "", ""
	start := time.Now()
	if _, _, e := f.manager.PaymentSelection(); !errors.Is(e, ErrUnavailable) {
		t.Fatal("busy selection reached disabled approval paths", e)
	}
	if _, e := f.manager.PaymentCheck(context.Background()); !errors.Is(e, ErrUnavailable) {
		t.Fatal("busy check reached disabled approval paths", e)
	}
	if time.Since(start) > time.Second {
		t.Fatal("busy approval did not reject promptly")
	}
	releaseCalls()
	calls.Wait()
	close(results)
	for e := range results {
		if e != nil {
			t.Fatal("held RPC failed", e)
		}
	}
	f.manager.paymentMu.Lock()
	remaining := len(f.manager.paymentSlots)
	f.manager.paymentMu.Unlock()
	if remaining != 0 {
		t.Fatal("completed RPCs retained manager capacity", remaining)
	}
	if _, _, e := f.manager.PaymentSelection(); !errors.Is(e, ErrDisabled) {
		t.Fatal("released selection did not inspect approval paths", e)
	}
	if _, e := f.manager.PaymentCheck(context.Background()); !errors.Is(e, ErrDisabled) {
		t.Fatal("released check did not inspect approval paths", e)
	}
	f.manager.root, f.manager.policy = root, policy
	if m, _, e := f.manager.PaymentSelection(); e != nil || m.ID != "payment.fixture" {
		t.Fatal("selection capacity did not recover", m, e)
	}
	// This test proves the Manager's shared approval/RPC capacity, not the
	// separate server listener pool. A closed client socket does not synchronize
	// the server's EOF/Close; immediately starting a ninth physical connection
	// would race that unrelated pool. TestPaymentRuntimeContract separately
	// requires a complete authenticated PaymentCheck handshake.
}

func TestPaymentServerRequiresOneOperationOnHandshakenConnection(t *testing.T) {
	for _, operations := range []struct{ first, replay string }{
		{"checkout", "checkout"}, {"checkout", "verify"},
		{"verify", "checkout"}, {"verify", "verify"},
	} {
		t.Run(operations.first+"_then_"+operations.replay, func(t *testing.T) {
			f := newPaymentFixture(t)
			var creates, verifies atomic.Int32
			f.paymentServe(t, paymentProcessor{
				create: func(context.Context, paymentv1.Config, paymentv1.Intent) (paymentv1.Checkout, error) {
					creates.Add(1)
					return paymentCheckout(), nil
				},
				verify: func(context.Context, paymentv1.Config, paymentv1.Callback) (paymentv1.VerifiedEvent, error) {
					verifies.Add(1)
					return paymentEvent(), nil
				},
			})
			client := func() *http.Client {
				t.Helper()
				conn, err := net.DialTimeout("unix", f.policy.Payment.Socket, 2*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				var dials atomic.Int32
				transport := &http.Transport{Proxy: nil, MaxConnsPerHost: 1, MaxIdleConns: 1, MaxIdleConnsPerHost: 1,
					DialContext: func(context.Context, string, string) (net.Conn, error) {
						if dials.Add(1) != 1 {
							return nil, errors.New("test client attempted to redial its pinned Unix connection")
						}
						return conn, nil
					}}
				c := &http.Client{Transport: transport, Timeout: 2 * time.Second,
					CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
				t.Cleanup(func() {
					c.CloseIdleConnections()
					conn.Close()
					if n := dials.Load(); n != 1 {
						t.Errorf("pinned connection handed to transport %d times, want 1", n)
					}
				})
				return c
			}
			first, second := client(), client()
			post := func(c *http.Client, path string, input any, status int) []byte {
				t.Helper()
				raw, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				// Like exchange, omit GetBody and idempotency headers to forbid
				// automatic replay; unlike exchange, drain rejected responses too.
				req, err := http.NewRequest("POST", "http://plugin.local"+path, io.NopCloser(bytes.NewReader(raw)))
				if err != nil {
					t.Fatal(err)
				}
				req.ContentLength = int64(len(raw))
				req.Header.Set("Content-Type", "application/json")
				response, err := c.Do(req)
				if err != nil {
					t.Fatalf("%s: transport error is not a protocol rejection: %v", path, err)
				}
				body, readErr := io.ReadAll(io.LimitReader(response.Body, pluginapi.MaxResponseBytes+1))
				closeErr := response.Body.Close()
				if readErr != nil || closeErr != nil || len(body) > pluginapi.MaxResponseBytes {
					t.Fatalf("%s: incomplete or oversized response: read=%v close=%v bytes=%d", path, readErr, closeErr, len(body))
				}
				if response.StatusCode != status {
					t.Fatalf("%s: HTTP %d, want %d; body=%q", path, response.StatusCode, status, body)
				}
				if status == http.StatusConflict && string(body) != "handshake required\n" {
					t.Fatalf("%s: unexpected rejection body %q", path, body)
				}
				if status == http.StatusOK && response.Header.Get("Content-Type") != "application/json" {
					t.Fatalf("%s: unexpected success content type %q", path, response.Header.Get("Content-Type"))
				}
				return body
			}
			nonce := strings.Repeat("a", 64)
			operation := func(c *http.Client, name string, status int) []byte {
				t.Helper()
				var input any = pluginapi.PaymentCreateRequest{Protocol: pluginapi.PaymentProtocol, Version: f.manifest.Version, RequestID: nonce, TimeoutMS: 1000, Config: f.paymentConfig(), Intent: paymentIntent()}
				if name == "verify" {
					input = pluginapi.PaymentVerifyRequest{Protocol: pluginapi.PaymentProtocol, Version: f.manifest.Version, RequestID: nonce, TimeoutMS: 1000, Config: f.paymentConfig(), Callback: paymentCallback()}
				}
				return post(c, "/v1/payment/"+name, input, status)
			}
			assertCalls := func(wantCreates, wantVerifies int32) {
				t.Helper()
				if c, v := creates.Load(), verifies.Load(); c != wantCreates || v != wantVerifies {
					t.Fatalf("processor calls: checkout=%d verify=%d, want %d/%d", c, v, wantCreates, wantVerifies)
				}
			}
			operation(first, operations.first, http.StatusConflict)
			assertCalls(0, 0)
			body := post(first, "/v1/handshake", pluginapi.Hello{Protocol: pluginapi.PaymentProtocol, CoreVersion: pluginapi.CoreVersion, Nonce: nonce}, http.StatusOK)
			var identity pluginapi.Identity
			if err := pluginapi.DecodeStrict(body, pluginapi.MaxResponseBytes, &identity); err != nil || identity.Protocol != pluginapi.PaymentProtocol || identity.ID != f.manifest.ID || identity.Nonce != nonce {
				t.Fatal("invalid handshake identity", err, identity)
			}
			operation(second, operations.first, http.StatusConflict)
			assertCalls(0, 0)
			body = operation(first, operations.first, http.StatusOK)
			var wantCreates, wantVerifies int32
			if operations.first == "checkout" {
				var out pluginapi.PaymentCreateResponse
				if err := pluginapi.DecodeStrict(body, pluginapi.MaxResponseBytes, &out); err != nil || out.Protocol != pluginapi.PaymentProtocol || out.RequestID != nonce || out.Status != "accepted" || out.Checkout == nil {
					t.Fatal("checkout not accepted", err, out)
				}
				wantCreates = 1
			} else {
				var out pluginapi.PaymentVerifyResponse
				if err := pluginapi.DecodeStrict(body, pluginapi.MaxResponseBytes, &out); err != nil || out.Protocol != pluginapi.PaymentProtocol || out.RequestID != nonce || out.Status != "verified" || out.Event == nil {
					t.Fatal("callback not verified", err, out)
				}
				wantVerifies = 1
			}
			assertCalls(wantCreates, wantVerifies)
			operation(first, operations.replay, http.StatusConflict)
			assertCalls(wantCreates, wantVerifies)
		})
	}
}
