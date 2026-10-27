package runtimeplugins

import (
	"aster/billing/controllerapi"
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/resourcev1"
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
	"sync/atomic"
	"testing"
	"time"
)

func resourceRequest() controllerapi.Request {
	p := controllerapi.ProductSpec{Mode: "preset", ControllerID: "primary", RemoteProductID: "sku-1"}
	h, _ := p.Digest()
	return controllerapi.Request{Protocol: controllerapi.Protocol, OperationID: strings.Repeat("a", 32), OrderID: strings.Repeat("b", 32), OwnerRef: strings.Repeat("c", 32), Hostname: "web-1", ConfigurationRevision: 2, ConfigurationSHA256: h, Product: p}
}
func resourceResult(in controllerapi.Request) controllerapi.Result {
	_, h, _ := in.BytesAndDigest()
	return controllerapi.Result{Protocol: controllerapi.Protocol, OperationID: in.OperationID, OrderID: in.OrderID, RequestSHA256: h, Status: "accepted"}
}

type resourceProcessor struct {
	send, lookup func(context.Context, controllerapi.Request) (controllerapi.Result, error)
}

func (p resourceProcessor) Send(c context.Context, r controllerapi.Request) (controllerapi.Result, error) {
	if p.send != nil {
		return p.send(c, r)
	}
	return resourceResult(r), nil
}
func (p resourceProcessor) Lookup(c context.Context, r controllerapi.Request) (controllerapi.Result, error) {
	if p.lookup != nil {
		return p.lookup(c, r)
	}
	return resourceResult(r), nil
}
func newResourceFixture(t *testing.T) *fixture {
	f := newFixture(t)
	old := filepath.Dir(filepath.Dir(f.binary))
	next := filepath.Join(filepath.Dir(old), "resource.aster-cloud")
	if e := os.Rename(old, next); e != nil {
		t.Fatal(e)
	}
	f.binary = filepath.Join(next, "1.1.0", "plugin")
	f.manifestPath = filepath.Join(next, "1.1.0", "manifest.json")
	f.manifest.ID = "resource.aster-cloud"
	f.manifest.CoreMin = pluginapi.ResourceMinCoreVersion
	f.manifest.Protocol = pluginapi.ResourceProtocol
	f.manifest.Capabilities = pluginapi.ResourceCapabilities()
	raw := writeJSON(t, f.manifestPath, f.manifest)
	s := *f.policy.SMTP
	s.ID = f.manifest.ID
	s.ControllerID = "primary"
	s.ManifestSHA256 = digestBytes(raw)
	s.AllowedTargets = []pluginapi.Target{{Host: "cloud.example.test", Port: 443}}
	f.policy.SMTP = nil
	f.policy.Resource = &s
	writeJSON(t, f.manager.policy, f.policy)
	return f
}
func (f *fixture) resourceServe(t *testing.T, p resourcev1.Processor) {
	l, e := net.ListenUnix("unix", &net.UnixAddr{Name: f.policy.Resource.Socket, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(f.policy.Resource.Socket, 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (pluginserver.ResourceServer{Manifest: f.manifest, ClientUID: f.policy.Resource.ClientUID, ControllerID: f.policy.Resource.ControllerID, Processor: p}).Serve(ctx, l)
	}()
	t.Cleanup(func() {
		cancel()
		l.Close()
		select {
		case e := <-done:
			if e != nil && !errors.Is(e, net.ErrClosed) {
				t.Error(e)
			}
		case <-time.After(3 * time.Second):
			t.Error("resource worker did not stop")
		}
	})
}
func TestResourceRuntimeExactContractAndIndependentSlot(t *testing.T) {
	f := newResourceFixture(t)
	var sends, lookups atomic.Int32
	f.resourceServe(t, resourceProcessor{send: func(_ context.Context, in controllerapi.Request) (controllerapi.Result, error) {
		sends.Add(1)
		got, _, _ := in.BytesAndDigest()
		want, _, _ := resourceRequest().BytesAndDigest()
		if !bytes.Equal(got, want) {
			t.Error("immutable request changed")
		}
		return resourceResult(in), nil
	}, lookup: func(_ context.Context, in controllerapi.Request) (controllerapi.Result, error) {
		lookups.Add(1)
		return resourceResult(in), nil
	}})
	if s := f.manager.ResourceState(); s.Status != "approved" || s.Protocol != pluginapi.ResourceProtocol {
		t.Fatal(s)
	}
	c, e := f.manager.PrepareResource(t.Context(), "primary")
	if e != nil {
		t.Fatal(e)
	}
	if sends.Load() != 0 || lookups.Load() != 0 {
		t.Fatal("preflight dispatched")
	}
	if out, e := c.Send(t.Context(), resourceRequest()); e != nil || !out.Matches(resourceRequest()) {
		t.Fatal(out, e)
	}
	if out, e := c.Lookup(t.Context(), resourceRequest()); e != nil || !out.Matches(resourceRequest()) {
		t.Fatal(out, e)
	}
	if sends.Load() != 1 || lookups.Load() != 1 {
		t.Fatal("wrong/duplicate processor call", sends.Load(), lookups.Load())
	}
	if _, e := f.manager.PrepareResource(t.Context(), "other"); e == nil {
		t.Fatal("cross-controller admitted")
	}
	if discovered, e := f.manager.Discover(); e != nil || len(discovered) != 1 || discovered[0].Protocol != pluginapi.ResourceProtocol {
		t.Fatal(discovered, e)
	}
	f.policy.Resource.Enabled = false
	writeJSON(t, f.manager.policy, f.policy)
	if _, e := c.Send(t.Context(), resourceRequest()); e != controllerapi.ErrUncertain {
		t.Fatal("disabled slot dispatched", e)
	}
	if f.manager.ResourceState().Status != "disabled" || sends.Load() != 1 {
		t.Fatal("disabled metadata or handler")
	}
}
func TestResourceUnknownOutcomeNeverResendsOrLeaks(t *testing.T) {
	for _, mode := range []string{"error", "panic", "mismatch", "missing_resource"} {
		t.Run(mode, func(t *testing.T) {
			f := newResourceFixture(t)
			var calls atomic.Int32
			f.resourceServe(t, resourceProcessor{send: func(_ context.Context, in controllerapi.Request) (controllerapi.Result, error) {
				calls.Add(1)
				out := resourceResult(in)
				switch mode {
				case "error":
					return out, errors.New("private-resource-token-canary")
				case "panic":
					panic("private-resource-token-canary")
				case "mismatch":
					out.OrderID = strings.Repeat("e", 32)
				case "missing_resource":
					out.Status = "succeeded"
				}
				return out, nil
			}})
			c, e := f.manager.PrepareResource(t.Context(), "primary")
			if e != nil {
				t.Fatal(e)
			}
			out, e := c.Send(t.Context(), resourceRequest())
			if e != controllerapi.ErrUncertain || out != (controllerapi.Result{}) || strings.Contains(e.Error(), "canary") || calls.Load() != 1 {
				t.Fatal("unsafe outcome", out, e, calls.Load())
			}
		})
	}
}
func TestResourceSelectionRotationAndWrongRequestFailClosed(t *testing.T) {
	for _, mode := range []string{"manifest", "worker", "controller", "socket", "targets", "request"} {
		t.Run(mode, func(t *testing.T) {
			f := newResourceFixture(t)
			var calls atomic.Int32
			f.resourceServe(t, resourceProcessor{send: func(_ context.Context, in controllerapi.Request) (controllerapi.Result, error) {
				calls.Add(1)
				return resourceResult(in), nil
			}})
			c, e := f.manager.PrepareResource(t.Context(), "primary")
			if e != nil {
				t.Fatal(e)
			}
			in := resourceRequest()
			switch mode {
			case "manifest":
				f.policy.Resource.ManifestSHA256 = strings.Repeat("0", 64)
			case "worker":
				f.policy.Resource.WorkerUID++
			case "controller":
				f.policy.Resource.ControllerID = "other"
			case "socket":
				f.policy.Resource.Socket += "other"
			case "targets":
				f.policy.Resource.AllowedTargets[0].Host = "other.example.test"
			case "request":
				in.Product.ControllerID = "other"
				in.ConfigurationSHA256, _ = in.Product.Digest()
			}
			writeJSON(t, f.manager.policy, f.policy)
			if _, e := c.Send(t.Context(), in); e == nil || calls.Load() != 0 {
				t.Fatal("rotated pin dispatched", e, calls.Load())
			}
		})
	}
}
func TestResourcePolicyRootHostAndIsolatedWorkerRequired(t *testing.T) {
	f := newResourceFixture(t)
	p := f.policy
	p.Resource = &pluginapi.Selection{Enabled: true, ID: f.manifest.ID, Version: "1.1.0", ManifestSHA256: strings.Repeat("a", 64), WorkerUID: 2100, ClientUID: 0, Socket: "/run/aster-resource/plugin.sock", ControllerID: "primary", AllowedTargets: []pluginapi.Target{{Host: "cloud.example.test", Port: 443}}}
	if e := validatePolicyKind(p, "resource", trust{}); e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"api_client", "root_worker", "merchant", "controller", "collision"} {
		t.Run(mode, func(t *testing.T) {
			copy := p
			sel := *p.Resource
			copy.Resource = &sel
			switch mode {
			case "api_client":
				sel.ClientUID = 2101
			case "root_worker":
				sel.WorkerUID = 0
			case "merchant":
				sel.MerchantID = "payment-secret"
			case "controller":
				sel.ControllerID = "../bad"
			case "collision":
				other := sel
				other.ID = "email.smtp"
				other.ControllerID = ""
				other.ClientUID = 2200
				copy.SMTP = &other
			}
			if e := validatePolicyKind(copy, "resource", trust{}); e == nil {
				t.Fatal("unsafe slot policy")
			}
		})
	}
}
func TestResourcePhysicalConnectionHandshakeAndAllReplayPairs(t *testing.T) {
	for _, pair := range [][2]string{{"send", "send"}, {"send", "lookup"}, {"lookup", "send"}, {"lookup", "lookup"}} {
		t.Run(pair[0]+"_then_"+pair[1], func(t *testing.T) {
			f := newResourceFixture(t)
			var sends, lookups atomic.Int32
			f.resourceServe(t, resourceProcessor{send: func(_ context.Context, in controllerapi.Request) (controllerapi.Result, error) {
				sends.Add(1)
				return resourceResult(in), nil
			}, lookup: func(_ context.Context, in controllerapi.Request) (controllerapi.Result, error) {
				lookups.Add(1)
				return resourceResult(in), nil
			}})
			client := func() *http.Client {
				conn, e := net.DialTimeout("unix", f.policy.Resource.Socket, time.Second)
				if e != nil {
					t.Fatal(e)
				}
				var dials atomic.Int32
				tr := &http.Transport{Proxy: nil, MaxConnsPerHost: 1, MaxIdleConns: 1, MaxIdleConnsPerHost: 1, DialContext: func(context.Context, string, string) (net.Conn, error) {
					if dials.Add(1) != 1 {
						return nil, errors.New("forbidden redial")
					}
					return conn, nil
				}}
				c := &http.Client{Transport: tr, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
				t.Cleanup(func() {
					c.CloseIdleConnections()
					conn.Close()
					if dials.Load() != 1 {
						t.Error("physical connection rebound", dials.Load())
					}
				})
				return c
			}
			post := func(c *http.Client, path string, in any, status int) {
				t.Helper()
				raw, _ := json.Marshal(in)
				req, _ := http.NewRequest("POST", "http://plugin.local"+path, io.NopCloser(bytes.NewReader(raw)))
				req.ContentLength = int64(len(raw))
				req.Header.Set("Content-Type", "application/json")
				r, e := c.Do(req)
				if e != nil {
					t.Fatal("network failure is not rejection", e)
				}
				body, e := io.ReadAll(io.LimitReader(r.Body, pluginapi.MaxResponseBytes+1))
				ce := r.Body.Close()
				if e != nil || ce != nil || len(body) > pluginapi.MaxResponseBytes || r.StatusCode != status {
					t.Fatal("incomplete HTTP response", r.StatusCode, e, ce, string(body))
				}
				if status == 409 && string(body) != "handshake required\n" {
					t.Fatal("incorrect denial", string(body))
				}
			}
			a, b := client(), client()
			nonce := strings.Repeat("a", 64)
			payload, _, _ := resourceRequest().BytesAndDigest()
			envelope := pluginapi.ResourceRequest{Protocol: pluginapi.ResourceProtocol, Version: f.manifest.Version, RequestID: nonce, TimeoutMS: 1000, ControllerID: "primary", Payload: payload}
			post(b, "/v1/resource/"+pair[0], envelope, 409)
			post(a, "/v1/handshake", pluginapi.Hello{Protocol: pluginapi.ResourceProtocol, CoreVersion: pluginapi.CoreVersion, Nonce: nonce}, 200)
			post(b, "/v1/resource/"+pair[0], envelope, 409)
			post(a, "/v1/resource/"+pair[0], envelope, 200)
			post(a, "/v1/resource/"+pair[1], envelope, 409)
			if sends.Load()+lookups.Load() != 1 || pair[0] == "send" && sends.Load() != 1 || pair[0] == "lookup" && lookups.Load() != 1 {
				t.Fatal("replay executed", sends.Load(), lookups.Load())
			}
		})
	}
}

func TestResourceRuntimeRejectsWrongPeerUID(t *testing.T) {
	f := newResourceFixture(t)
	var calls atomic.Int32
	// Socket filesystem metadata is valid but server only accepts another UID.
	f.policy.Resource.ClientUID++
	f.resourceServe(t, resourceProcessor{send: func(_ context.Context, in controllerapi.Request) (controllerapi.Result, error) {
		calls.Add(1)
		return resourceResult(in), nil
	}})
	conn, e := net.DialTimeout("unix", f.policy.Resource.Socket, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	_, _ = conn.Write([]byte("POST /v1/handshake HTTP/1.1\r\nHost: plugin.local\r\nContent-Length: 0\r\n\r\n"))
	var one [1]byte
	n, e := conn.Read(one[:])
	if n != 0 || e == nil || calls.Load() != 0 {
		t.Fatal("unauthorized peer admitted", n, e, calls.Load())
	}
}
