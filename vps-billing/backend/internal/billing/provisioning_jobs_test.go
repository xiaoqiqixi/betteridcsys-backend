package billing

import (
	"aster/billing/internal/controllers"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func provisioningOrder(t *testing.T, f catalogFixture, pid string, paid bool) Order {
	t.Helper()
	o, _, err := f.store.CreateOrder(t.Context(), f.customer, pid, "provision-"+ID(), ID(), "CNY")
	if err != nil {
		t.Fatal(err)
	}
	if !paid {
		return o
	}
	invoices, err := f.store.Invoices(t.Context(), f.customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	var i Invoice
	for _, v := range invoices {
		if v.OrderID == o.ID {
			i = v
		}
	}
	a, _, err := f.store.ReservePayment(t.Context(), f.customer, i.ID, "provision:"+i.ID, "payment.synthetic", "merchant-synthetic")
	if err != nil {
		t.Fatal(err)
	}
	settled, err := f.store.SettlePayment(t.Context(), a.ID, syntheticPayment(a))
	if err != nil {
		t.Fatal(err)
	}
	return settled.Order
}
func provisioningResult(r controllers.Request, status string) controllers.Result {
	_, h, _ := r.BytesAndDigest()
	v := controllers.Result{Protocol: controllers.Protocol, OperationID: r.OperationID, OrderID: r.OrderID, RequestSHA256: h, Status: status}
	if status == "succeeded" {
		v.ResourceID = "resource-test-123"
	}
	return v
}
func expireProvisioningLease(t *testing.T, f catalogFixture, id string) {
	t.Helper()
	at := time.Now().UTC().Add(-time.Minute)
	switch m := f.store.(type) {
	case *Memory:
		m.mu.Lock()
		r := m.provisioningJobs[id]
		r.LeaseExpiresAt = &at
		m.provisioningJobs[id] = r
		m.mu.Unlock()
	case *MySQL:
		migrationExec(t, t.Context(), m.DB, "UPDATE provisioning_jobs SET lease_expires_at=? WHERE id=?", at, id)
	}
}
func TestProvisioningJobsMemory(t *testing.T) { runProvisioningJobs(t, memoryCatalogFixture) }
func TestMySQLProvisioningJobs(t *testing.T)  { runProvisioningJobs(t, mysqlCatalogFixture) }
func runProvisioningJobs(t *testing.T, makeFixture func(*testing.T) catalogFixture) {
	t.Run("manual_paid_only_snapshot_unique_and_owner_audit", func(t *testing.T) {
		f, pid, config := productConfigurationFixture(t, makeFixture)
		jobs := f.store.(ProvisioningJobStore)
		ctx := t.Context()
		disabled := provisioningOrder(t, f, pid, true)
		_, err := config.UpdateProductConfiguration(ctx, f.admin, pid, ProductConfigurationInput{Revision: 1, Spec: presetProductSpec(), Reason: "Enable manual jobs"})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = jobs.CreateProvisioningJob(ctx, f.admin, disabled.ID, "manual"); !errors.Is(err, ErrProvisioningUnavailable) {
			t.Fatal("disabled order used current product", err)
		}
		unpaid := provisioningOrder(t, f, pid, false)
		if _, _, err = jobs.CreateProvisioningJob(ctx, f.admin, unpaid.ID, "manual"); !errors.Is(err, ErrProvisioningState) {
			t.Fatal("unpaid order queued", err)
		}
		paid := provisioningOrder(t, f, pid, true)
		orders, invoices, services := catalogHistory(t, f)
		for _, actor := range []User{f.customer, {ID: f.customer.ID, Role: "admin"}, {ID: ID(), Role: "admin"}} {
			if _, _, err = jobs.CreateProvisioningJob(ctx, actor, paid.ID, "manual"); !errors.Is(err, ErrAdminRequired) {
				t.Fatal("unprivileged queue", err)
			}
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		var outputs []ProvisioningJob
		fresh, replays := 0, 0
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out, replay, e := jobs.CreateProvisioningJob(ctx, f.admin, paid.ID, "Operator reviewed paid order")
				mu.Lock()
				defer mu.Unlock()
				if e != nil {
					t.Error(e)
					return
				}
				outputs = append(outputs, out)
				if replay {
					replays++
				} else {
					fresh++
				}
			}()
		}
		wg.Wait()
		if fresh != 1 || replays != 3 || len(outputs) != 4 {
			t.Fatal("duplicate create operation", fresh, replays, len(outputs))
		}
		job := outputs[0]
		for _, out := range outputs {
			if !reflect.DeepEqual(job, out) {
				t.Fatal("job retry changed immutable identity")
			}
		}
		request, err := jobs.ProvisioningRequest(ctx, job.ID)
		if err != nil || request.OwnerRef != f.customer.ID || request.OrderID != paid.ID || request.Product.RemoteProductID != "plan-123" || !request.Valid() {
			t.Fatal(request, err)
		}
		_, err = config.UpdateProductConfiguration(ctx, f.admin, pid, ProductConfigurationInput{Revision: 2, Spec: controllers.DisabledSpec(), Reason: "Future orders disabled"})
		if err != nil {
			t.Fatal(err)
		}
		frozen, err := jobs.ProvisioningRequest(ctx, job.ID)
		if err != nil || !reflect.DeepEqual(request, frozen) {
			t.Fatal("queued request used new config", err)
		}
		raw, _ := json.Marshal(job)
		for _, private := range []string{"owner_ref", "hostname", "controller_id", "claim_token", "request\""} {
			if strings.Contains(string(raw), private) {
				t.Fatal("admin job leaks payload", string(raw))
			}
		}
		audits, err := f.store.Audits(ctx, f.customer.ID)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, a := range audits {
			if a.Action == "provisioning.queued" {
				count++
				if a.UserID != f.customer.ID || a.ActorEmail != f.admin.Email || a.Target != paid.ID {
					t.Fatal("queue audit lost actor/owner", a)
				}
			}
		}
		if count != 1 {
			t.Fatal("queue audit count", count)
		}
		if _, err = jobs.ProvisioningJob(ctx, f.customer, job.ID); !errors.Is(err, ErrAdminRequired) {
			t.Fatal("customer saw module job", err)
		}
		afterOrders, afterInvoices, afterServices := catalogHistory(t, f)
		if !reflect.DeepEqual(orders, afterOrders) || !reflect.DeepEqual(invoices, afterInvoices) || !reflect.DeepEqual(services, afterServices) {
			t.Fatal("jobs changed financial records/services")
		}
		deletePrivateOrderConfiguration(t, f, disabled.ID)
		if _, _, err = jobs.CreateProvisioningJob(ctx, f.admin, disabled.ID, "manual"); !errors.Is(err, ErrProvisioningUnavailable) {
			t.Fatal("old order missing snapshot was reconstructed", err)
		}
	})
	for _, outcome := range []string{"accepted", "succeeded", "rejected", "lost", "mismatch", "expired"} {
		t.Run("claim_once_"+outcome, func(t *testing.T) {
			f, pid, config := productConfigurationFixture(t, makeFixture)
			ctx := t.Context()
			jobs := f.store.(ProvisioningJobStore)
			_, err := config.UpdateProductConfiguration(ctx, f.admin, pid, ProductConfigurationInput{Revision: 1, Spec: customProductSpec(), Reason: "manual provisioning"})
			if err != nil {
				t.Fatal(err)
			}
			paid := provisioningOrder(t, f, pid, true)
			job, _, err := jobs.CreateProvisioningJob(ctx, f.admin, paid.ID, "Operator reviewed")
			if err != nil {
				t.Fatal(err)
			}
			req, err := jobs.ProvisioningRequest(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = jobs.ReconcileProvisioningJob(ctx, job.ID, provisioningResult(req, "succeeded")); !errors.Is(err, ErrProvisioningState) {
				t.Fatal("queued job forced successful", err)
			}
			var wg sync.WaitGroup
			claims := make(chan ProvisioningClaim, 4)
			errs := make(chan error, 4)
			for range 4 {
				wg.Add(1)
				go func() { defer wg.Done(); v, e := jobs.ClaimProvisioningJob(ctx, job.ID); claims <- v; errs <- e }()
			}
			wg.Wait()
			close(claims)
			close(errs)
			good := 0
			for e := range errs {
				if e == nil {
					good++
				} else if !errors.Is(e, ErrProvisioningState) {
					t.Fatal(e)
				}
			}
			if good != 1 {
				t.Fatal("more than one dispatch claim", good)
			}
			var claim ProvisioningClaim
			for v := range claims {
				if v.ClaimToken != "" {
					claim = v
				}
			}
			if claim.Job.State != "sending" || claim.ClaimToken == "" || !reflect.DeepEqual(claim.Request, req) {
				t.Fatal(claim)
			}
			claim.Request.Product.Resources.CPUCores = 88
			unchanged, err := jobs.ProvisioningRequest(ctx, job.ID)
			if err != nil || unchanged.Product.Resources.CPUCores != 2 {
				t.Fatal("claim aliased stored request", err)
			}
			if _, err = jobs.CompleteProvisioningJob(ctx, job.ID, ID(), nil); !errors.Is(err, ErrProvisioningState) {
				t.Fatal("wrong claim token changed job", err)
			}
			result := provisioningResult(req, outcome)
			var completion *controllers.Result = &result
			want := outcome
			switch outcome {
			case "lost":
				completion = nil
				want = "uncertain"
			case "mismatch":
				result = provisioningResult(req, "succeeded")
				result.RequestSHA256 = strings.Repeat("a", 64)
				want = "uncertain"
			case "expired":
				expireProvisioningLease(t, f, job.ID)
				result = provisioningResult(req, "succeeded")
				want = "uncertain"
			}
			final, err := jobs.CompleteProvisioningJob(ctx, job.ID, claim.ClaimToken, completion)
			if err != nil || final.State != want {
				t.Fatal(final, err)
			}
			if _, err = jobs.ClaimProvisioningJob(ctx, job.ID); !errors.Is(err, ErrProvisioningState) {
				t.Fatal("completed/uncertain operation resent", err)
			}
			if want == "uncertain" || want == "accepted" {
				wrong := provisioningResult(req, "succeeded")
				wrong.OrderID = ID()
				if _, err = jobs.ReconcileProvisioningJob(ctx, job.ID, wrong); !errors.Is(err, ErrProvisioningState) {
					t.Fatal("unbound lookup accepted", err)
				}
				final, err = jobs.ReconcileProvisioningJob(ctx, job.ID, provisioningResult(req, "succeeded"))
				if err != nil || final.State != "succeeded" {
					t.Fatal(final, err)
				}
			}
			if final.Result != nil {
				correct := *final.Result
				final.Result.ResourceID = "mutated"
				stored, e := jobs.ProvisioningJob(ctx, f.admin, job.ID)
				if e != nil || stored.Result == nil || *stored.Result != correct {
					t.Fatal("result alias", stored, e)
				}
				replay, e := jobs.ReconcileProvisioningJob(ctx, job.ID, correct)
				if e != nil || replay.State != correct.Status {
					t.Fatal("terminal lookup exact replay failed", replay, e)
				}
				opposite := provisioningResult(req, "rejected")
				if correct.Status == "rejected" {
					opposite = provisioningResult(req, "succeeded")
				}
				if _, e = jobs.ReconcileProvisioningJob(ctx, job.ID, opposite); !errors.Is(e, ErrProvisioningState) {
					t.Fatal("terminal result overwritten", e)
				}
			}
			audits, err := f.store.Audits(ctx, f.customer.ID)
			if err != nil {
				t.Fatal(err)
			}
			transitions := 0
			for _, a := range audits {
				if strings.HasPrefix(a.Action, "provisioning.") && a.Action != "provisioning.queued" {
					transitions++
					if a.ActorEmail != "system:controller-worker" || a.Target != paid.ID || strings.Contains(a.Detail, "local-test") {
						t.Fatal("system audit wrong/leaky", a)
					}
				}
			}
			if transitions < 2 {
				t.Fatal("missing state audits", transitions)
			}
			services, err := f.store.Services(ctx, f.customer.ID)
			if err != nil || len(services) != 0 {
				t.Fatal("controller result manufactured local service", services, err)
			}
		})
	}
	t.Run("expired_claim_requires_lookup_and_paid_recheck", func(t *testing.T) {
		f, pid, config := productConfigurationFixture(t, makeFixture)
		ctx := t.Context()
		jobs := f.store.(ProvisioningJobStore)
		if _, err := config.UpdateProductConfiguration(ctx, f.admin, pid, ProductConfigurationInput{Revision: 1, Spec: presetProductSpec(), Reason: "manual"}); err != nil {
			t.Fatal(err)
		}
		o := provisioningOrder(t, f, pid, true)
		job, _, err := jobs.CreateProvisioningJob(ctx, f.admin, o.ID, "manual")
		if err != nil {
			t.Fatal(err)
		}
		claim, err := jobs.ClaimProvisioningJob(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		expireProvisioningLease(t, f, job.ID)
		if _, err = jobs.ClaimProvisioningJob(ctx, job.ID); !errors.Is(err, ErrProvisioningState) {
			t.Fatal("expired lease reclaimed", err)
		}
		state, err := jobs.ProvisioningJob(ctx, f.admin, job.ID)
		if err != nil || state.State != "uncertain" {
			t.Fatal("expired claim not persisted uncertain", state, err)
		}
		if _, err = jobs.CompleteProvisioningJob(ctx, job.ID, claim.ClaimToken, nil); !errors.Is(err, ErrProvisioningState) {
			t.Fatal("old claim still completes", err)
		}
		if _, err = jobs.ReconcileProvisioningJob(ctx, job.ID, provisioningResult(claim.Request, "accepted")); err != nil {
			t.Fatal(err)
		}
		o2 := provisioningOrder(t, f, pid, true)
		j2, _, err := jobs.CreateProvisioningJob(ctx, f.admin, o2.ID, "manual")
		if err != nil {
			t.Fatal(err)
		}
		switch m := f.store.(type) {
		case *Memory:
			m.mu.Lock()
			for n := range m.invoices {
				if m.invoices[n].OrderID == o2.ID {
					m.invoices[n].Status = "unpaid"
				}
			}
			m.mu.Unlock()
		case *MySQL:
			migrationExec(t, ctx, m.DB, "UPDATE invoices SET status='unpaid' WHERE order_id=?", o2.ID)
		}
		if _, err = jobs.ClaimProvisioningJob(ctx, j2.ID); !errors.Is(err, ErrProvisioningState) {
			t.Fatal("claim trusted stale paid task", err)
		}
	})
}
