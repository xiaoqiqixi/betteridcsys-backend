package serviceapi

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	s := httptest.NewTLSServer(h)
	t.Cleanup(s.Close)
	c, e := NewCredentialTransport(context.Background(), Profile{ID: "primary", Enabled: true, Endpoint: s.URL + "/v1/provisioning", AllowedCIDRs: []string{"127.0.0.0/8"}}, []byte(strings.Repeat("x", 32)), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}))
	if e != nil {
		t.Fatal(e)
	}
	return c, s
}
func TestEveryClientMessageAndReplay(t *testing.T) {
	at := time.Now().UTC()
	var replay bool
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("x", 32) || r.Header.Get("X-Aster-Owner-Ref") != testOwner {
			t.Error("binding missing")
		}
		w.Header().Set("Content-Type", "application/json")
		var v any
		switch r.URL.Path {
		case "/v1/images":
			v = []Image{{"ubuntu", "linux", "Ubuntu", "24.04", 8, strings.Repeat("a", 64)}}
		case "/v1/services/" + testID:
			v = Service{Protocol: Protocol, ResourceID: testID, OwnerRef: testOwner, Power: "stopped", IPv4: []string{}, IPv6: []string{}}
		case "/v1/services/" + testID + "/actions":
			raw, _ := io.ReadAll(r.Body)
			var req ActionRequest
			if Decode(raw, &req) != nil {
				t.Error("invalid request")
			}
			b, h, _ := req.BytesAndDigest()
			if string(b) != string(raw) || r.Header.Get("Idempotency-Key") != testID || r.Header.Get("X-Aster-Request-SHA256") != h || r.Method != "POST" {
				t.Error("binding")
			}
			if replay {
				w.WriteHeader(409)
			} else {
				w.WriteHeader(202)
			}
			v = ActionResult{testID, "accepted", "ACCEPTED"}
		case "/v1/services/" + testID + "/actions/" + testID:
			v = Operation{testID, "start", "succeeded", "SUCCEEDED", &at}
		case "/v1/services/" + testID + "/console":
			if r.Method != "POST" {
				t.Error("method")
			}
			v = Console{"https://cloud.example/console/#ticket", at}
		case "/v1/services/" + testID + "/metrics":
			if r.URL.RawQuery != "range=1h" {
				t.Error("range")
			}
			v = Metrics{300, []MetricPoint{{At: at}}}
		case "/v1/services/" + testID + "/snapshots":
			v = Snapshots{[]Snapshot{}, 3, true}
		default:
			t.Error("unexpected route")
		}
		_ = json.NewEncoder(w).Encode(v)
	}))
	ctx := context.Background()
	if _, e := c.Images(ctx, testOwner); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Service(ctx, testID, testOwner); e != nil {
		t.Fatal(e)
	}
	req := ActionRequest{Protocol, testID, testOwner, "start", Params{}}
	if _, again, e := c.Action(ctx, testID, req); e != nil || again {
		t.Fatal(e)
	}
	replay = true
	if _, again, e := c.Action(ctx, testID, req); e != nil || !again {
		t.Fatal(e)
	}
	if _, e := c.Operation(ctx, testID, testOwner, testID); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Console(ctx, testID, testOwner); e != nil {
		t.Fatal(e)
	}
	if v, e := c.Metrics(ctx, testID, testOwner, "1h"); e != nil || v.Points[0].CPUPercent != nil {
		t.Fatal(e)
	}
	if _, e := c.Snapshots(ctx, testID, testOwner); e != nil {
		t.Fatal(e)
	}
}
func TestServiceTransportDoesNotRedirectReplayOrTrustWrongOwner(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "/v1/images")
		w.WriteHeader(307)
	}))
	if _, e := c.Images(context.Background(), testOwner); !errors.Is(e, ErrUncertain) || calls.Load() != 1 {
		t.Fatal("redirect followed")
	}
	c, _ = newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close()
	}))
	calls.Store(0)
	if _, _, e := c.Action(context.Background(), testID, ActionRequest{Protocol, testID, testOwner, "start", Params{}}); !errors.Is(e, ErrUncertain) || calls.Load() != 1 {
		t.Fatal("mutation replayed")
	}
	c, _ = newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Service{Protocol: Protocol, ResourceID: testID, OwnerRef: testID, Power: "stopped", IPv4: []string{}, IPv6: []string{}})
	}))
	if _, e := c.Service(context.Background(), testID, testOwner); !errors.Is(e, ErrUncertain) {
		t.Fatal("wrong owner accepted")
	}
	if _, e := c.Service(context.Background(), "../images", testOwner); !errors.Is(e, ErrConfiguration) {
		t.Fatal("unsafe path")
	}
	if _, e := c.Metrics(context.Background(), testID, testOwner, "1h&x=y"); !errors.Is(e, ErrConfiguration) {
		t.Fatal("unsafe query")
	}
	var missing *Client
	if _, e := missing.Images(context.Background(), testOwner); !errors.Is(e, ErrUnavailable) {
		t.Fatal("nil client")
	}
}
