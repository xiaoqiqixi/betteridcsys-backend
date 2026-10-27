//go:build linux

package runtimeplugins

import (
	"aster/billing/pkg/pluginapi/paymentv1"
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These maintenance regressions state the desired circuit-breaker behavior.
// They exercise only private synthetic Unix sockets, never a payment channel.
func TestPaymentMaintenancePreCanceledCallsDoNotTripCircuit(t *testing.T) {
	for _, operation := range []string{"checkout", "verify"} {
		t.Run(operation, func(t *testing.T) {
			f := newPaymentFixture(t)
			var dispatched atomic.Int32
			f.paymentServe(t, paymentProcessor{
				create: func(context.Context, paymentv1.Config, paymentv1.Intent) (paymentv1.Checkout, error) {
					dispatched.Add(1)
					return paymentCheckout(), nil
				},
				verify: func(context.Context, paymentv1.Config, paymentv1.Callback) (paymentv1.VerifiedEvent, error) {
					dispatched.Add(1)
					return paymentEvent(), nil
				},
			})
			cfg := f.paymentConfig()
			call := func(ctx context.Context) error {
				if operation == "checkout" {
					_, err := f.manager.CreatePayment(ctx, cfg, paymentIntent())
					return err
				}
				_, err := f.manager.VerifyPaymentCallback(ctx, cfg, paymentCallback())
				return err
			}
			for n := 0; n < 3; n++ {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if err := call(ctx); err == nil {
					t.Fatal("pre-canceled operation unexpectedly succeeded")
				}
			}
			if dispatched.Load() != 0 {
				t.Fatal("pre-canceled calls reached the worker", dispatched.Load())
			}
			err := call(context.Background())
			f.manager.paymentMu.Lock()
			failures, blockedUntil := f.manager.paymentFailures, f.manager.paymentBlockedUntil
			f.manager.paymentMu.Unlock()
			if err != nil || dispatched.Load() != 1 {
				t.Fatalf("healthy worker blocked after caller-only cancellation: err=%v dispatched=%d failures=%d circuit_open=%t", err, dispatched.Load(), failures, time.Now().Before(blockedUntil))
			}
		})
	}
}

func TestPaymentMaintenanceMidFlightCallerCancellationDoesNotTripCircuit(t *testing.T) {
	for _, operation := range []string{"checkout", "verify"} {
		t.Run(operation, func(t *testing.T) {
			f := newPaymentFixture(t)
			started := make(chan struct{}, 3)
			finished := make(chan struct{}, 3)
			var dispatched atomic.Int32
			waitForCancellation := func(ctx context.Context) bool {
				if dispatched.Add(1) > 3 {
					return false
				}
				started <- struct{}{}
				<-ctx.Done()
				finished <- struct{}{}
				return true
			}
			f.paymentServe(t, paymentProcessor{
				create: func(ctx context.Context, _ paymentv1.Config, _ paymentv1.Intent) (paymentv1.Checkout, error) {
					if waitForCancellation(ctx) {
						return paymentv1.Checkout{}, paymentv1.ErrUnavailable
					}
					return paymentCheckout(), nil
				},
				verify: func(ctx context.Context, _ paymentv1.Config, _ paymentv1.Callback) (paymentv1.VerifiedEvent, error) {
					if waitForCancellation(ctx) {
						return paymentv1.VerifiedEvent{}, paymentv1.ErrUnavailable
					}
					return paymentEvent(), nil
				},
			})
			cfg := f.paymentConfig()
			call := func(ctx context.Context) error {
				if operation == "checkout" {
					_, err := f.manager.CreatePayment(ctx, cfg, paymentIntent())
					return err
				}
				_, err := f.manager.VerifyPaymentCallback(ctx, cfg, paymentCallback())
				return err
			}
			for n := 0; n < 3; n++ {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				result := make(chan error, 1)
				go func() { result <- call(ctx) }()
				select {
				case <-started:
				case <-time.After(3 * time.Second):
					t.Fatal("mid-flight request did not reach the worker")
				}
				cancel()
				select {
				case err := <-result:
					if err == nil {
						t.Fatal("canceled in-flight request unexpectedly succeeded")
					}
				case <-time.After(3 * time.Second):
					t.Fatal("caller cancellation failed to release the core")
				}
				select {
				case <-finished:
				case <-time.After(3 * time.Second):
					t.Fatal("disconnect did not cancel the worker context")
				}
			}
			f.manager.paymentMu.Lock()
			failures, active := f.manager.paymentFailures, len(f.manager.paymentSlots)
			f.manager.paymentMu.Unlock()
			if failures != 0 || active != 0 {
				t.Fatalf("caller cancellations must release slots without channel faults: failures=%d slots=%d", failures, active)
			}
			if err := call(context.Background()); err != nil || dispatched.Load() != 4 {
				t.Fatalf("healthy fourth call blocked after three in-flight caller cancellations: err=%v dispatched=%d", err, dispatched.Load())
			}
		})
	}
}

func TestPaymentMaintenanceOldApprovalFailuresDoNotTripNewCircuit(t *testing.T) {
	f := newPaymentFixture(t)
	started := make(chan struct{}, 3)
	finishOld := make(chan struct{})
	var finishOnce sync.Once
	releaseOld := func() { finishOnce.Do(func() { close(finishOld) }) }
	t.Cleanup(releaseOld)
	f.paymentServe(t, paymentProcessor{create: func(ctx context.Context, _ paymentv1.Config, _ paymentv1.Intent) (paymentv1.Checkout, error) {
		started <- struct{}{}
		select {
		case <-finishOld:
		case <-ctx.Done():
		}
		return paymentv1.Checkout{}, paymentv1.ErrUnavailable
	}})
	oldConfig := f.paymentConfig()
	results := make(chan error, 3)
	for n := 0; n < 3; n++ {
		go func() {
			_, err := f.manager.CreatePayment(context.Background(), oldConfig, paymentIntent())
			results <- err
		}()
	}
	for n := 0; n < 3; n++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("old approval calls did not reach their worker")
		}
	}
	// The old requests have pinned their approval and reached its worker. A
	// fresh approved socket and merchant now select a different healthy worker.
	f.policy.Revision++
	f.policy.Payment.Socket = filepath.Join(f.base, "replacement.sock")
	f.policy.Payment.MerchantID = "replacement-merchant"
	writeJSON(t, f.manager.policy, f.policy)
	var newCalls atomic.Int32
	f.paymentServe(t, paymentProcessor{create: func(context.Context, paymentv1.Config, paymentv1.Intent) (paymentv1.Checkout, error) {
		newCalls.Add(1)
		return paymentCheckout(), nil
	}})
	newConfig := f.paymentConfig()
	if _, err := f.manager.CreatePayment(context.Background(), newConfig, paymentIntent()); err != nil || newCalls.Load() != 1 {
		t.Fatal("replacement worker was not initially healthy", err, newCalls.Load())
	}
	releaseOld()
	for n := 0; n < 3; n++ {
		select {
		case err := <-results:
			if err == nil {
				t.Fatal("old worker failure unexpectedly succeeded")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("old worker calls did not finish")
		}
	}
	_, err := f.manager.CreatePayment(context.Background(), newConfig, paymentIntent())
	f.manager.paymentMu.Lock()
	failures, blockedUntil := f.manager.paymentFailures, f.manager.paymentBlockedUntil
	f.manager.paymentMu.Unlock()
	if err != nil || newCalls.Load() != 2 {
		t.Fatalf("old approval failures blocked the healthy replacement: err=%v new_worker_calls=%d failures=%d circuit_open=%t", err, newCalls.Load(), failures, time.Now().Before(blockedUntil))
	}
}

func TestPaymentMaintenanceOldApprovalSuccessDoesNotClearNewCircuit(t *testing.T) {
	f := newPaymentFixture(t)
	started := make(chan struct{}, 1)
	finishOld := make(chan struct{})
	var finishOnce sync.Once
	releaseOld := func() { finishOnce.Do(func() { close(finishOld) }) }
	defer releaseOld()
	f.paymentServe(t, paymentProcessor{create: func(ctx context.Context, _ paymentv1.Config, _ paymentv1.Intent) (paymentv1.Checkout, error) {
		started <- struct{}{}
		select {
		case <-finishOld:
		case <-ctx.Done():
		}
		return paymentCheckout(), nil
	}})
	oldConfig := f.paymentConfig()
	oldResult := make(chan error, 1)
	go func() {
		_, err := f.manager.CreatePayment(context.Background(), oldConfig, paymentIntent())
		oldResult <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("old approval call did not start")
	}
	f.policy.Revision++
	f.policy.Payment.Socket = filepath.Join(f.base, "replacement.sock")
	f.policy.Payment.MerchantID = "replacement-merchant"
	writeJSON(t, f.manager.policy, f.policy)
	var newCalls atomic.Int32
	f.paymentServe(t, paymentProcessor{create: func(context.Context, paymentv1.Config, paymentv1.Intent) (paymentv1.Checkout, error) {
		if newCalls.Add(1) <= 3 {
			return paymentv1.Checkout{}, paymentv1.ErrUnavailable
		}
		return paymentCheckout(), nil
	}})
	newConfig := f.paymentConfig()
	for n := 0; n < 3; n++ {
		if _, err := f.manager.CreatePayment(context.Background(), newConfig, paymentIntent()); err == nil {
			t.Fatal("replacement's real failure unexpectedly succeeded")
		}
	}
	f.manager.paymentMu.Lock()
	opened := time.Now().Before(f.manager.paymentBlockedUntil)
	f.manager.paymentMu.Unlock()
	if newCalls.Load() != 3 || !opened {
		t.Fatal("replacement's real failures did not open its circuit", newCalls.Load(), opened)
	}
	releaseOld()
	select {
	case err := <-oldResult:
		if err != nil {
			t.Fatal("old approval success did not finish", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("old approval success did not return")
	}
	_, err := f.manager.CreatePayment(context.Background(), newConfig, paymentIntent())
	f.manager.paymentMu.Lock()
	failures, blockedUntil := f.manager.paymentFailures, f.manager.paymentBlockedUntil
	f.manager.paymentMu.Unlock()
	if err == nil || newCalls.Load() != 3 || !time.Now().Before(blockedUntil) {
		t.Fatalf("old approval success cleared the replacement's circuit: err=%v new_worker_calls=%d failures=%d circuit_open=%t", err, newCalls.Load(), failures, time.Now().Before(blockedUntil))
	}
}

type maintenanceCallResult struct {
	operation string
	elapsed   time.Duration
	err       error
}

func TestPaymentMaintenanceDefaultRPCTimeoutReleasesCapacity(t *testing.T) {
	f := newPaymentFixture(t)
	started := make(chan string, 2)
	release := make(chan struct{})
	defer close(release)
	// Deliberately ignore context cancellation. The client must still time out
	// and free its capacity while worker operations remain bounded and active.
	f.paymentServe(t, paymentProcessor{
		create: func(context.Context, paymentv1.Config, paymentv1.Intent) (paymentv1.Checkout, error) {
			started <- "checkout"
			<-release
			return paymentCheckout(), nil
		},
		verify: func(context.Context, paymentv1.Config, paymentv1.Callback) (paymentv1.VerifiedEvent, error) {
			started <- "verify"
			<-release
			return paymentEvent(), nil
		},
	})
	cfg := f.paymentConfig()
	results := make(chan maintenanceCallResult, 2)
	for _, operation := range []string{"checkout", "verify"} {
		go func() {
			start := time.Now()
			var err error
			if operation == "checkout" {
				_, err = f.manager.CreatePayment(context.Background(), cfg, paymentIntent())
			} else {
				_, err = f.manager.VerifyPaymentCallback(context.Background(), cfg, paymentCallback())
			}
			results <- maintenanceCallResult{operation, time.Since(start), err}
		}()
	}
	for n := 0; n < 2; n++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("default-deadline RPC did not reach the worker")
		}
	}
	deadline := time.After(16 * time.Second)
	for n := 0; n < 2; n++ {
		select {
		case result := <-results:
			if !errors.Is(result.err, paymentv1.ErrUnavailable) || result.elapsed < 11*time.Second || result.elapsed > 15*time.Second {
				t.Fatalf("default 12-second %s RPC boundary changed: elapsed=%s err=%v", result.operation, result.elapsed, result.err)
			}
			t.Logf("%s ignored-context RPC returned after %s", result.operation, result.elapsed)
		case <-deadline:
			t.Fatal("default 12-second RPC deadline failed to release the caller")
		}
	}
	f.manager.paymentMu.Lock()
	active, failures := len(f.manager.paymentSlots), f.manager.paymentFailures
	f.manager.paymentMu.Unlock()
	if active != 0 || failures != 2 {
		t.Fatalf("real RPC deadlines must release client slots and remain faults: slots=%d failures=%d", active, failures)
	}
	if _, err := f.manager.PaymentCheck(context.Background()); err != nil {
		t.Fatal("client capacity did not recover while two worker operations were still blocked", err)
	}
}

func TestPaymentMaintenanceDefaultHandshakeTimeouts(t *testing.T) {
	f := newPaymentFixture(t)
	listener := f.paymentListen(t)
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	defer close(release)
	var dispatched atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/handshake" {
			dispatched.Add(1)
			http.Error(w, "unexpected dispatch", http.StatusBadRequest)
			return
		}
		started <- struct{}{}
		<-release // Do not write even headers or cooperate with cancellation.
	})}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close() })
	cfg := f.paymentConfig()
	results := make(chan maintenanceCallResult, 3)
	for _, operation := range []string{"checkout", "verify", "check"} {
		go func() {
			start := time.Now()
			var err error
			switch operation {
			case "checkout":
				_, err = f.manager.CreatePayment(context.Background(), cfg, paymentIntent())
			case "verify":
				_, err = f.manager.VerifyPaymentCallback(context.Background(), cfg, paymentCallback())
			case "check":
				_, err = f.manager.PaymentCheck(context.Background())
			}
			results <- maintenanceCallResult{operation, time.Since(start), err}
		}()
	}
	for n := 0; n < 3; n++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("handshake did not reach the synthetic endpoint")
		}
	}
	deadline := time.After(5 * time.Second)
	for n := 0; n < 3; n++ {
		select {
		case result := <-results:
			if result.err == nil || result.elapsed < 1500*time.Millisecond || result.elapsed > 4*time.Second {
				t.Fatalf("default two-second %s handshake boundary changed: elapsed=%s err=%v", result.operation, result.elapsed, result.err)
			}
			t.Logf("%s silent handshake returned after %s", result.operation, result.elapsed)
		case <-deadline:
			t.Fatal("silent worker exceeded its handshake deadline")
		}
	}
	f.manager.paymentMu.Lock()
	active, failures := len(f.manager.paymentSlots), f.manager.paymentFailures
	f.manager.paymentMu.Unlock()
	if active != 0 || failures != 2 || dispatched.Load() != 0 {
		t.Fatalf("handshake faults must release slots without dispatching payment: slots=%d failures=%d operations=%d", active, failures, dispatched.Load())
	}
}
