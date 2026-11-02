// Package conformance tests module behaviour with an author's isolated provider fixture.
package conformance

import (
	"aster/billing/pkg/pluginapi/serverv1"
	"context"
	"fmt"
	"testing"
	"time"
)

type Suite struct {
	Processor serverv1.Processor
	Config    serverv1.Fields
	Input     map[string]serverv1.Fields
	Actions   map[string]serverv1.Fields
	Secrets   map[string]serverv1.Fields
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
	if call(serverv1.Request{Method: "test_connection"}).State != "done" {
		t.Fatal("connection fixture unavailable")
	}
	normalized := call(serverv1.Request{Method: "validate_product", Config: s.Config})
	if !serverv1.ValidDigest(normalized.ConfigDigest) {
		t.Fatal("missing normalized config digest")
	}
	create := serverv1.Request{Protocol: serverv1.Protocol, Method: "create", OperationID: "11111111111111111111111111111111", ServiceID: "22222222222222222222222222222222", OwnerRef: "33333333333333333333333333333333", Config: normalized.Config, Input: s.Input["create"]}
	if e := create.Seal(); e != nil {
		t.Fatal(e)
	}
	settle := func(req serverv1.Request, out serverv1.Response) serverv1.Response {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for out.State == "accepted" && time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(10 * time.Millisecond):
			}
			out = call(serverv1.Request{Method: "operation_status", OperationID: req.OperationID, Digest: req.Digest})
		}
		if out.State != "done" {
			t.Fatal("operation did not finish", req.Method, out.State)
		}
		return out
	}
	out := settle(create, call(create))
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
	for i, method := range []string{"suspend", "unsuspend", "change_package", "renew", "power"} {
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
		if e := req.Seal(); e != nil {
			t.Fatal(e)
		}
		settle(req, call(req))
	}
	for i, action := range described.Description.CustomActions {
		req := create
		req.Method, req.ActionID, req.ResourceID = "action", action.ID, out.ResourceID
		req.OperationID = fmt.Sprintf("%032x", i+200)
		req.Input, req.Secrets = s.Actions[action.ID], s.Secrets[action.ID]
		if e := req.Seal(); e != nil {
			t.Fatal(e)
		}
		result := settle(req, call(req))
		replay := settle(req, call(req))
		if replay.ResourceID != result.ResourceID {
			t.Fatal("custom action replay changed resource")
		}
	}
	if described.Description.Has("terminate") {
		req := create
		req.Method, req.OperationID, req.ResourceID = "terminate", fmt.Sprintf("%032x", 500), out.ResourceID
		req.Input = s.Input["terminate"]
		if e := req.Seal(); e != nil {
			t.Fatal(e)
		}
		settle(req, call(req))
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := s.Processor.Call(cancelled, create); e == nil {
		t.Fatal("cancelled request accepted")
	}
}
