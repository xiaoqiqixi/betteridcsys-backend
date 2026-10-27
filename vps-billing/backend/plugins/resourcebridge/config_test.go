package resourcebridge

import (
	"aster/billing/controllerapi"
	"aster/billing/pkg/pluginapi"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestResourceWorkerConfigRejectsUnapprovedDestinations(t *testing.T) {
	valid := `{"schema":1,"controller_id":"primary","endpoint":"https://cloud.example.test/aster/v1/provision","allowed_cidrs":["192.0.2.0/24"],"custom_ca":false}`
	targets := []pluginapi.Target{{Host: "cloud.example.test", Port: 443}}
	if _, e := DecodeConfig([]byte(valid), "primary", targets); e != nil {
		t.Fatal(e)
	}
	for _, raw := range []string{strings.Replace(valid, `"primary"`, `"other"`, 1), strings.Replace(valid, `https:`, `http:`, 1), strings.Replace(valid, `cloud.example.test`, `attacker.example.test`, 1), strings.Replace(valid, `,"custom_ca":false`, "", 1), strings.Replace(valid, `"schema":1`, `"schema":1,"token":"secret-canary"`, 1), strings.Replace(valid, `"schema":1`, `"schema":1,"schema":1`, 1)} {
		if _, e := DecodeConfig([]byte(raw), "primary", targets); e == nil || strings.Contains(e.Error(), "canary") {
			t.Fatal("unsafe config result", e)
		}
	}
}
func workerRequest() controllerapi.Request {
	p := controllerapi.ProductSpec{Mode: "preset", ControllerID: "primary", RemoteProductID: "sku-1"}
	h, _ := p.Digest()
	return controllerapi.Request{Protocol: controllerapi.Protocol, OperationID: strings.Repeat("a", 32), OrderID: strings.Repeat("b", 32), OwnerRef: strings.Repeat("c", 32), Hostname: "web-1", ConfigurationRevision: 1, ConfigurationSHA256: h, Product: p}
}
func TestFixedCredentialWorkerRealVerifiedTLS(t *testing.T) {
	in := workerRequest()
	original, digest, _ := in.BytesAndDigest()
	var sends, lookups atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-resource-test-token" || r.Header.Get("X-Aster-Request-SHA256") != digest {
			t.Error("fixed credential or digest lost")
		}
		body, _ := io.ReadAll(r.Body)
		if r.Method == "POST" {
			sends.Add(1)
			if string(body) != string(original) {
				t.Error("snapshot mutated")
			}
		} else if r.Method == "GET" {
			lookups.Add(1)
			if len(body) != 0 || !strings.HasSuffix(r.URL.Path, in.OperationID) {
				t.Error("lookup is not bound/read-only")
			}
		} else {
			t.Error("unexpected method")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(controllerapi.Result{Protocol: controllerapi.Protocol, OperationID: in.OperationID, OrderID: in.OrderID, RequestSHA256: digest, Status: "accepted"})
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(u.Port())
	cfg := Config{Schema: 1, ControllerID: "primary", Endpoint: server.URL + "/aster/v1/provision", AllowedCIDRs: []string{"127.0.0.0/8"}, CustomCA: true}
	raw, _ := json.Marshal(cfg)
	cfg, e := DecodeConfig(raw, "primary", []pluginapi.Target{{Host: u.Hostname(), Port: port}})
	if e != nil {
		t.Fatal(e)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	client, e := New(context.Background(), cfg, []byte("synthetic-resource-test-token"), ca)
	if e != nil {
		t.Fatal(e)
	}
	if out, e := client.Send(t.Context(), in); e != nil || !out.Matches(in) {
		t.Fatal(out, e)
	}
	if out, e := client.Lookup(t.Context(), in); e != nil || !out.Matches(in) {
		t.Fatal(out, e)
	}
	if sends.Load() != 1 || lookups.Load() != 1 {
		t.Fatal("automatic retry", sends.Load(), lookups.Load())
	}
	cfg.AllowedCIDRs = []string{"192.0.2.0/24"}
	if _, e := New(t.Context(), cfg, []byte("synthetic-resource-test-token"), ca); e == nil {
		t.Fatal("CIDR escape")
	}
	cfg.AllowedCIDRs = []string{"127.0.0.0/8"}
	cfg.CustomCA = false
	if _, e := New(t.Context(), cfg, []byte("synthetic-resource-test-token"), ca); e == nil {
		t.Fatal("implicit CA change")
	}
	if _, e := controllerapi.NewCredentialTransport(t.Context(), controllerapi.Profile{ID: "primary", Enabled: true, Endpoint: cfg.Endpoint, AllowedCIDRs: cfg.AllowedCIDRs, TokenFile: "/secret"}, []byte("synthetic-resource-test-token"), nil); e == nil {
		t.Fatal("credential transport accepted file path")
	}
}
