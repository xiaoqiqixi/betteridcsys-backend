package main

import (
	"aster/billing/internal/billing"
	"aster/billing/internal/controllers"
	"context"
	"errors"
	"flag"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestHostProvisioningExplicitEmptyFlagRemainsCommand(t *testing.T) {
	for _, args := range [][]string{{}, {"-dispatch-provisioning="}, {"-reconcile-provisioning", ""}, {"-dispatch-provisioning=", "-reconcile-provisioning="}} {
		f := flag.NewFlagSet("host", flag.ContinueOnError)
		f.SetOutput(io.Discard)
		f.String("dispatch-provisioning", "", "")
		f.String("reconcile-provisioning", "", "")
		if err := f.Parse(args); err != nil {
			t.Fatal(err)
		}
		d, r := provisioningFlagsSelected(f)
		if d != (len(args) > 0 && args[0] == "-dispatch-provisioning=") || r != (len(args) > 1 && args[1] == "-reconcile-provisioning=" || len(args) > 0 && args[0] == "-reconcile-provisioning") {
			t.Fatal(args, d, r)
		}
	}
}

type hostJobFixture struct {
	billing.ProvisioningJobStore
	request          controllers.Request
	state            string
	events           []string
	completeErr      error
	completeCanceled bool
}

func (s *hostJobFixture) ProvisioningRequest(context.Context, string) (controllers.Request, error) {
	s.events = append(s.events, "read")
	return s.request, nil
}
func (s *hostJobFixture) ClaimProvisioningJob(context.Context, string) (billing.ProvisioningClaim, error) {
	s.events = append(s.events, "claim")
	if s.state != "queued" {
		return billing.ProvisioningClaim{}, billing.ErrProvisioningState
	}
	s.state = "sending"
	return billing.ProvisioningClaim{Request: s.request, ClaimToken: "private-claim"}, nil
}
func (s *hostJobFixture) CompleteProvisioningJob(ctx context.Context, _ string, token string, result *controllers.Result) (billing.ProvisioningJob, error) {
	s.events = append(s.events, "complete")
	s.completeCanceled = ctx.Err() != nil
	if token != "private-claim" {
		return billing.ProvisioningJob{}, billing.ErrProvisioningState
	}
	if s.completeErr != nil {
		return billing.ProvisioningJob{}, s.completeErr
	}
	s.state = "uncertain"
	if result != nil && result.Matches(s.request) {
		s.state = result.Status
	}
	return billing.ProvisioningJob{ID: s.request.OperationID, State: s.state}, nil
}
func (s *hostJobFixture) ReconcileProvisioningJob(_ context.Context, _ string, result controllers.Result) (billing.ProvisioningJob, error) {
	s.events = append(s.events, "reconcile")
	if !result.Matches(s.request) {
		return billing.ProvisioningJob{}, billing.ErrProvisioningState
	}
	s.state = result.Status
	return billing.ProvisioningJob{ID: s.request.OperationID, State: s.state}, nil
}

type hostTransportFixture struct {
	store              *hostJobFixture
	sendErr, lookupErr error
	cancel             context.CancelFunc
	sends, lookups     int
}

func (c *hostTransportFixture) result() controllers.Result {
	r := c.store.request
	_, h, _ := r.BytesAndDigest()
	return controllers.Result{Protocol: controllers.Protocol, OperationID: r.OperationID, OrderID: r.OrderID, RequestSHA256: h, Status: "accepted"}
}
func (c *hostTransportFixture) Send(context.Context, controllers.Request) (controllers.Result, error) {
	c.sends++
	c.store.events = append(c.store.events, "send")
	if c.cancel != nil {
		c.cancel()
	}
	return c.result(), c.sendErr
}
func (c *hostTransportFixture) Lookup(context.Context, controllers.Request) (controllers.Result, error) {
	c.lookups++
	c.store.events = append(c.store.events, "lookup")
	return c.result(), c.lookupErr
}
func hostFixture() (*hostJobFixture, *hostTransportFixture) {
	p := controllers.ProductSpec{Mode: "preset", ControllerID: "primary", RemoteProductID: "sku-1"}
	h, _ := p.Digest()
	s := &hostJobFixture{state: "queued", request: controllers.Request{Protocol: controllers.Protocol, OperationID: strings.Repeat("a", 32), OrderID: strings.Repeat("b", 32), OwnerRef: strings.Repeat("c", 32), Hostname: "web-1", ConfigurationRevision: 2, ConfigurationSHA256: h, Product: p}}
	return s, &hostTransportFixture{store: s}
}
func TestHostProvisioningDispatchOnce(t *testing.T) {
	for _, lost := range []bool{false, true} {
		name := "accepted"
		if lost {
			name = "lost_response"
		}
		t.Run(name, func(t *testing.T) {
			s, c := hostFixture()
			if lost {
				c.sendErr = controllers.ErrUncertain
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if lost {
				c.cancel = cancel
			}
			prepare := func(context.Context, string) (controllerTransport, error) {
				s.events = append(s.events, "prepare")
				return c, nil
			}
			job, err := performProvisioning(ctx, s, s.request.OperationID, false, prepare)
			want := "accepted"
			if lost {
				want = "uncertain"
				if !errors.Is(err, controllers.ErrUncertain) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if job.State != want || s.completeCanceled || !reflect.DeepEqual(s.events, []string{"read", "prepare", "claim", "send", "complete"}) {
				t.Fatal(job, s.events, s.completeCanceled)
			}
			_, err = performProvisioning(t.Context(), s, s.request.OperationID, false, prepare)
			if !errors.Is(err, billing.ErrProvisioningState) || c.sends != 1 {
				t.Fatal("repeated host command resent operation", err, c.sends)
			}
		})
	}
}
func TestHostProvisioningPreparesBeforeClaim(t *testing.T) {
	s, c := hostFixture()
	_, err := performProvisioning(t.Context(), s, s.request.OperationID, false, func(context.Context, string) (controllerTransport, error) { return nil, controllers.ErrUnavailable })
	if !errors.Is(err, controllers.ErrUnavailable) || s.state != "queued" || c.sends != 0 || !reflect.DeepEqual(s.events, []string{"read"}) {
		t.Fatal(s.events, err)
	}
}
func TestHostProvisioningCompletionFailureNeverRetries(t *testing.T) {
	s, c := hostFixture()
	s.completeErr = errors.New("private database information")
	_, err := performProvisioning(t.Context(), s, s.request.OperationID, false, func(context.Context, string) (controllerTransport, error) { return c, nil })
	if err != controllers.ErrUncertain || c.sends != 1 || s.state != "sending" {
		t.Fatal(err, c.sends, s.state)
	}
}
func TestHostProvisioningReconcileOnlyReadsRemote(t *testing.T) {
	for _, lost := range []bool{false, true} {
		s, c := hostFixture()
		s.state = "uncertain"
		if lost {
			c.lookupErr = controllers.ErrUncertain
		}
		job, err := performProvisioning(t.Context(), s, s.request.OperationID, true, func(context.Context, string) (controllerTransport, error) { return c, nil })
		if c.sends != 0 || c.lookups != 1 {
			t.Fatal("reconcile sent a mutation")
		}
		if lost {
			if err != controllers.ErrUncertain || s.state != "uncertain" {
				t.Fatal(err, s.state)
			}
		} else if err != nil || job.State != "accepted" || !reflect.DeepEqual(s.events, []string{"read", "lookup", "reconcile"}) {
			t.Fatal(err, s.events)
		}
	}
}

func TestHostResourceRouteHasNoFallbackAndDoesNotClaimUnavailablePlugin(t *testing.T) {
	for _, cfg := range [][4]string{{"unknown", "", "", ""}, {"https", "/missing", "/plugins", "/policy"}, {"resource", "/https-policy", "/plugins", "/policy"}, {"resource", "", "", "/policy"}, {"resource", "", "/plugins", ""}} {
		if prepare, e := hostProvisioningTransport(cfg[0], cfg[1], cfg[2], cfg[3]); e == nil || prepare != nil {
			t.Fatal("ambiguous route allowed", cfg)
		}
	}
	prepare, e := hostProvisioningTransport("resource", "", "/not-installed-aster-plugins", "/not-installed-aster-policy")
	if e != nil {
		t.Fatal(e)
	}
	store, transport := hostFixture()
	_, e = performProvisioning(t.Context(), store, store.request.OperationID, false, prepare)
	if e == nil || store.state != "queued" || transport.sends != 0 || !reflect.DeepEqual(store.events, []string{"read"}) {
		t.Fatal("unavailable resource plugin claimed/fell back", e, store.events)
	}
}
