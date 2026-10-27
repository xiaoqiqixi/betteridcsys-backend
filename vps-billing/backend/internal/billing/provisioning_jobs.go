package billing

import (
	"aster/billing/internal/controllers"
	"context"
	"fmt"
	"time"
)

const provisioningLease = 30 * time.Second

type ProvisioningJob struct {
	ID                  string              `json:"id"`
	OrderID             string              `json:"order_id"`
	State               string              `json:"state"`
	RequestSHA256       string              `json:"request_sha256"`
	ConfigurationSHA256 string              `json:"configuration_sha256"`
	CreatedAt           time.Time           `json:"created_at"`
	UpdatedAt           time.Time           `json:"updated_at"`
	Result              *controllers.Result `json:"result,omitempty"`
}
type ProvisioningClaim struct {
	Job            ProvisioningJob
	Request        controllers.Request
	ClaimToken     string
	LeaseExpiresAt time.Time
}
type ProvisioningJobStore interface {
	CreateProvisioningJob(context.Context, User, string, string) (ProvisioningJob, bool, error)
	ProvisioningJob(context.Context, User, string) (ProvisioningJob, error)
	ProvisioningRequest(context.Context, string) (controllers.Request, error)
	ClaimProvisioningJob(context.Context, string) (ProvisioningClaim, error)
	CompleteProvisioningJob(context.Context, string, string, *controllers.Result) (ProvisioningJob, error)
	ReconcileProvisioningJob(context.Context, string, controllers.Result) (ProvisioningJob, error)
}
type provisioningJobRecord struct {
	Job            ProvisioningJob
	Request        controllers.Request
	ClaimToken     string
	LeaseExpiresAt *time.Time
	Attempts       int
}

func cloneProvisioningJob(j ProvisioningJob) ProvisioningJob {
	if j.Result != nil {
		r := *j.Result
		j.Result = &r
	}
	return j
}
func cloneProvisioningRequest(r controllers.Request) controllers.Request {
	r.Product = r.Product.Clone()
	return r
}
func (r provisioningJobRecord) valid() bool {
	_, h, err := r.Request.BytesAndDigest()
	if err != nil || r.Job.ID != r.Request.OperationID || r.Job.OrderID != r.Request.OrderID || r.Job.RequestSHA256 != h || r.Job.ConfigurationSHA256 != r.Request.ConfigurationSHA256 || r.Job.CreatedAt.IsZero() || r.Job.UpdatedAt.Before(r.Job.CreatedAt) {
		return false
	}
	if r.Job.State == "queued" {
		return r.Attempts == 0 && r.ClaimToken == "" && r.LeaseExpiresAt == nil && r.Job.Result == nil
	}
	if r.Attempts != 1 || !validCancellationID(r.ClaimToken) || r.LeaseExpiresAt == nil {
		return false
	}
	switch r.Job.State {
	case "sending", "uncertain":
		return r.Job.Result == nil
	case "accepted", "succeeded", "rejected":
		return r.Job.Result != nil && r.Job.Result.Status == r.Job.State && r.Job.Result.Matches(r.Request)
	}
	return false
}
func newProvisioningRecord(o Order, s orderProductConfiguration) (provisioningJobRecord, error) {
	if !s.valid() || s.Spec.Mode == "disabled" || o.PlanSnapshot == nil || o.PlanSnapshot.PlanID != s.PlanID {
		return provisioningJobRecord{}, ErrProvisioningUnavailable
	}
	id := ID()
	r := controllers.Request{Protocol: controllers.Protocol, OperationID: id, OrderID: o.ID, OwnerRef: o.UserID, Hostname: o.Hostname, ConfigurationRevision: s.Revision, ConfigurationSHA256: s.SHA256, Product: s.Spec.Clone()}
	_, h, err := r.BytesAndDigest()
	if err != nil {
		return provisioningJobRecord{}, ErrProductConfiguration
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	return provisioningJobRecord{Job: ProvisioningJob{ID: id, OrderID: o.ID, State: "queued", RequestSHA256: h, ConfigurationSHA256: s.SHA256, CreatedAt: now, UpdatedAt: now}, Request: r}, nil
}
func paidProvisioningPair(actor User, o Order, i Invoice) bool {
	return paymentPair(actor, o, i) == nil && o.Status == "paid" && i.Status == "paid" && i.PaidAt != nil
}
func claimProvisioning(r *provisioningJobRecord, now time.Time) error {
	if !r.valid() {
		return ErrProvisioningState
	}
	if r.Job.State != "queued" {
		if r.Job.State == "sending" && !now.Before(*r.LeaseExpiresAt) {
			r.Job.State = "uncertain"
			r.Job.UpdatedAt = now
		}
		return ErrProvisioningState
	}
	expires := now.Add(provisioningLease)
	r.Job.State = "sending"
	r.Job.UpdatedAt = now
	r.ClaimToken = ID()
	r.LeaseExpiresAt = &expires
	r.Attempts = 1
	return nil
}
func completeProvisioning(r *provisioningJobRecord, token string, result *controllers.Result, now time.Time) error {
	if !r.valid() || r.Job.State != "sending" || token != r.ClaimToken {
		return ErrProvisioningState
	}
	r.Job.UpdatedAt = now
	if !now.Before(*r.LeaseExpiresAt) || result == nil || !result.Matches(r.Request) {
		r.Job.State = "uncertain"
		r.Job.Result = nil
		return nil
	}
	v := *result
	r.Job.State = v.Status
	r.Job.Result = &v
	return nil
}
func reconcileProvisioning(r *provisioningJobRecord, result controllers.Result, now time.Time) error {
	if !r.valid() || !result.Matches(r.Request) {
		return ErrProvisioningState
	}
	switch r.Job.State {
	case "sending":
		if now.Before(*r.LeaseExpiresAt) {
			return ErrProvisioningState
		}
	case "accepted", "uncertain":
	case "succeeded", "rejected":
		if r.Job.Result != nil && *r.Job.Result == result {
			return nil
		}
		return ErrProvisioningState
	default:
		return ErrProvisioningState
	}
	if r.Job.Result != nil && r.Job.Result.ResourceID != "" && r.Job.Result.ResourceID != result.ResourceID {
		return ErrProvisioningState
	}
	r.Job.State = result.Status
	r.Job.Result = &result
	r.Job.UpdatedAt = now
	return nil
}
func provisioningStateAudit(r provisioningJobRecord, previous string) Audit {
	return Audit{ID: ID(), UserID: r.Request.OwnerRef, Action: "provisioning." + r.Job.State, Target: r.Job.OrderID, CreatedAt: r.Job.UpdatedAt, ActorEmail: "system:controller-worker", Detail: fmt.Sprintf("job %s; state %s -> %s", r.Job.ID, previous, r.Job.State)}
}
func (m *Memory) paidProvisioningOrderLocked(orderID string) (Order, error) {
	var o Order
	var i Invoice
	for _, v := range m.orders {
		if v.ID == orderID {
			o = v
			break
		}
	}
	for _, v := range m.invoices {
		if v.OrderID == orderID {
			i = v
			break
		}
	}
	if o.ID == "" {
		return o, ErrNotFound
	}
	if !paidProvisioningPair(User{ID: o.UserID, Role: "customer"}, o, i) {
		return Order{}, ErrProvisioningState
	}
	return o, nil
}
func (m *Memory) CreateProvisioningJob(ctx context.Context, actor User, orderID, reason string) (ProvisioningJob, bool, error) {
	if err := ctx.Err(); err != nil {
		return ProvisioningJob{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	admin, err := m.activeAdministratorLocked(actor.ID)
	if err != nil {
		return ProvisioningJob{}, false, err
	}
	reason, ok := catalogReason(reason)
	if !ok || !validCancellationID(orderID) {
		return ProvisioningJob{}, false, ErrProductConfiguration
	}
	var o Order
	var i Invoice
	for _, v := range m.orders {
		if v.ID == orderID {
			o = v
			break
		}
	}
	for _, v := range m.invoices {
		if v.OrderID == orderID {
			i = v
			break
		}
	}
	if o.ID == "" {
		return ProvisioningJob{}, false, ErrNotFound
	}
	if !paidProvisioningPair(admin, o, i) {
		return ProvisioningJob{}, false, ErrProvisioningState
	}
	for _, r := range m.provisioningJobs {
		if r.Job.OrderID == orderID {
			if !r.valid() {
				return ProvisioningJob{}, false, ErrProvisioningState
			}
			return cloneProvisioningJob(r.Job), true, nil
		}
	}
	s, ok := m.orderProductConfigurations[orderID]
	if !ok {
		return ProvisioningJob{}, false, ErrProvisioningUnavailable
	}
	r, err := newProvisioningRecord(o, s)
	if err != nil {
		return ProvisioningJob{}, false, err
	}
	if m.provisioningJobs == nil {
		m.provisioningJobs = map[string]provisioningJobRecord{}
	}
	m.provisioningJobs[r.Job.ID] = r
	a := catalogAudit(admin, "provisioning.queued", orderID, s.Revision, reason)
	a.UserID = o.UserID
	m.audits = append(m.audits, a)
	return cloneProvisioningJob(r.Job), false, nil
}
func (m *Memory) ProvisioningJob(ctx context.Context, actor User, jobID string) (ProvisioningJob, error) {
	if err := ctx.Err(); err != nil {
		return ProvisioningJob{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.activeAdministratorLocked(actor.ID); err != nil {
		return ProvisioningJob{}, err
	}
	r, ok := m.provisioningJobs[jobID]
	if !ok {
		return ProvisioningJob{}, ErrNotFound
	}
	if !r.valid() {
		return ProvisioningJob{}, ErrProvisioningState
	}
	return cloneProvisioningJob(r.Job), nil
}
func (m *Memory) ProvisioningRequest(ctx context.Context, jobID string) (controllers.Request, error) {
	if err := ctx.Err(); err != nil {
		return controllers.Request{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.provisioningJobs[jobID]
	if !ok {
		return controllers.Request{}, ErrNotFound
	}
	if !r.valid() {
		return controllers.Request{}, ErrProvisioningState
	}
	return cloneProvisioningRequest(r.Request), nil
}
func (m *Memory) ClaimProvisioningJob(ctx context.Context, jobID string) (ProvisioningClaim, error) {
	if err := ctx.Err(); err != nil {
		return ProvisioningClaim{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.provisioningJobs[jobID]
	if !ok {
		return ProvisioningClaim{}, ErrNotFound
	}
	o, err := m.paidProvisioningOrderLocked(r.Job.OrderID)
	if err != nil {
		return ProvisioningClaim{}, err
	}
	if o.UserID != r.Request.OwnerRef || o.Hostname != r.Request.Hostname {
		return ProvisioningClaim{}, ErrProvisioningState
	}
	previous := r.Job.State
	err = claimProvisioning(&r, time.Now().UTC().Truncate(time.Microsecond))
	if r.valid() {
		m.provisioningJobs[jobID] = r
		if previous != r.Job.State {
			m.audits = append(m.audits, provisioningStateAudit(r, previous))
		}
	}
	if err != nil {
		return ProvisioningClaim{}, err
	}
	return ProvisioningClaim{Job: cloneProvisioningJob(r.Job), Request: cloneProvisioningRequest(r.Request), ClaimToken: r.ClaimToken, LeaseExpiresAt: *r.LeaseExpiresAt}, nil
}
func (m *Memory) CompleteProvisioningJob(ctx context.Context, jobID, token string, result *controllers.Result) (ProvisioningJob, error) {
	if err := ctx.Err(); err != nil {
		return ProvisioningJob{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.provisioningJobs[jobID]
	if !ok {
		return ProvisioningJob{}, ErrNotFound
	}
	previous := r.Job.State
	if err := completeProvisioning(&r, token, result, time.Now().UTC().Truncate(time.Microsecond)); err != nil {
		return ProvisioningJob{}, err
	}
	m.provisioningJobs[jobID] = r
	if previous != r.Job.State {
		m.audits = append(m.audits, provisioningStateAudit(r, previous))
	}
	return cloneProvisioningJob(r.Job), nil
}
func (m *Memory) ReconcileProvisioningJob(ctx context.Context, jobID string, result controllers.Result) (ProvisioningJob, error) {
	if err := ctx.Err(); err != nil {
		return ProvisioningJob{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.provisioningJobs[jobID]
	if !ok {
		return ProvisioningJob{}, ErrNotFound
	}
	previous := r.Job.State
	if err := reconcileProvisioning(&r, result, time.Now().UTC().Truncate(time.Microsecond)); err != nil {
		return ProvisioningJob{}, err
	}
	m.provisioningJobs[jobID] = r
	if previous != r.Job.State {
		m.audits = append(m.audits, provisioningStateAudit(r, previous))
	}
	return cloneProvisioningJob(r.Job), nil
}
