// Package conformance tests module behaviour with an author's isolated provider fixture.
package conformance

import (
	"aster/billing/pkg/pluginapi/serverv1"
	"context"
	"fmt"
	"testing"
)

type Suite struct {
	Processor serverv1.Processor
	Config    serverv1.Fields
	Input     map[string]serverv1.Fields
}

func Test(t *testing.T, s Suite) {
	t.Helper()
	ctx := t.Context()
	call := func(in serverv1.Request) serverv1.Response {
		t.Helper()
		in.Protocol = serverv1.Protocol
		if serverv1.IsMutation(in.Method) {
			if e := in.Seal(); e != nil {
				t.Fatal(e)
			}
		}
		out, e := s.Processor.Call(ctx, in)
		if e != nil || !out.Matches(in) {
			t.Fatal("module response", in.Method, e)
		}
		return out
	}
	described := call(serverv1.Request{Method: "describe"})
	if described.Description == nil || described.Description.Validate() != nil {
		t.Fatal("invalid description")
	}
	call(serverv1.Request{Method: "test_connection"})
	normalized := call(serverv1.Request{Method: "validate_product", Config: s.Config})
	if !serverv1.ValidDigest(normalized.ConfigDigest) {
		t.Fatal("missing normalized config digest")
	}
	create := serverv1.Request{Protocol: serverv1.Protocol, Method: "create", OperationID: "11111111111111111111111111111111", ServiceID: "22222222222222222222222222222222", OwnerRef: "33333333333333333333333333333333", Config: normalized.Config, Input: s.Input["create"]}
	if e := create.Seal(); e != nil {
		t.Fatal(e)
	}
	out := call(create)
	if out.State == "accepted" {
		out = call(serverv1.Request{Method: "operation_status", OperationID: create.OperationID, Digest: create.Digest})
	}
	if out.State != "done" || !serverv1.ValidID(out.ResourceID) {
		t.Fatal("create did not produce a resource")
	}
	repeated := call(create)
	if repeated.ResourceID != "" && repeated.ResourceID != out.ResourceID {
		t.Fatal("create replay changed resource")
	}
	bad := create
	bad.ServiceID = "44444444444444444444444444444444"
	_ = bad.Seal()
	if _, e := s.Processor.Call(ctx, bad); e == nil {
		t.Fatal("operation id digest collision accepted")
	}
	call(serverv1.Request{Method: "status", ResourceID: out.ResourceID, OwnerRef: create.OwnerRef})
	for i, method := range []string{"suspend", "unsuspend", "change_package", "renew", "power", "terminate"} {
		if !described.Description.Has(method) {
			continue
		}
		req := create
		req.Method = method
		req.OperationID = fmt.Sprintf("%032x", i+100)
		req.ResourceID = out.ResourceID
		req.Input = s.Input[method]
		if method == "power" && req.Input == nil {
			req.Input = serverv1.Fields{"action": "stop"}
		}
		result := call(req)
		if result.State != "done" && result.State != "accepted" {
			t.Fatal("lifecycle failed", method)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := s.Processor.Call(cancelled, create); e == nil {
		t.Fatal("cancelled request accepted")
	}
}
