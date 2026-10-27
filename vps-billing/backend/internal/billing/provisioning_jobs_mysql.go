package billing

import (
	"aster/billing/internal/controllers"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const provisioningJobColumns = "id,order_id,state,request,request_sha256,claim_token,lease_expires_at,result,attempts,created_at,updated_at"

func readProvisioningJob(row interface{ Scan(...any) error }) (provisioningJobRecord, error) {
	var r provisioningJobRecord
	var raw, result []byte
	var token sql.NullString
	var lease sql.NullTime
	err := row.Scan(&r.Job.ID, &r.Job.OrderID, &r.Job.State, &raw, &r.Job.RequestSHA256, &token, &lease, &result, &r.Attempts, &r.Job.CreatedAt, &r.Job.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if json.Unmarshal(raw, &r.Request) != nil {
		return r, ErrProvisioningState
	}
	r.Job.ConfigurationSHA256 = r.Request.ConfigurationSHA256
	if token.Valid {
		r.ClaimToken = token.String
	}
	if lease.Valid {
		r.LeaseExpiresAt = &lease.Time
	}
	if len(result) > 0 && json.Unmarshal(result, &r.Job.Result) != nil {
		return r, ErrProvisioningState
	}
	if !r.valid() {
		return provisioningJobRecord{}, ErrProvisioningState
	}
	return r, nil
}
func (m *MySQL) CreateProvisioningJob(ctx context.Context, actor User, orderID, reason string) (ProvisioningJob, bool, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ProvisioningJob{}, false, err
	}
	defer tx.Rollback()
	admin, err := lockActiveAdministrator(ctx, tx, actor.ID)
	if err != nil {
		return ProvisioningJob{}, false, err
	}
	reason, ok := catalogReason(reason)
	if !ok || !validCancellationID(orderID) {
		return ProvisioningJob{}, false, ErrProductConfiguration
	}
	_, _, err = readCurrencies(ctx, tx, " FOR SHARE")
	if err != nil {
		return ProvisioningJob{}, false, err
	}
	o, err := readCancellationOrder(tx.QueryRowContext(ctx, "SELECT "+orderColumns+" FROM orders WHERE id=? FOR UPDATE", orderID))
	if errors.Is(err, sql.ErrNoRows) {
		return ProvisioningJob{}, false, ErrNotFound
	}
	if err != nil {
		return ProvisioningJob{}, false, err
	}
	i, err := readCancellationInvoice(ctx, tx, o.ID)
	if err != nil {
		return ProvisioningJob{}, false, err
	}
	if !paidProvisioningPair(admin, o, i) {
		return ProvisioningJob{}, false, ErrProvisioningState
	}
	snapshot, err := readOrderProductConfiguration(ctx, tx, o.ID, " FOR SHARE")
	if err != nil {
		return ProvisioningJob{}, false, err
	}
	r, err := readProvisioningJob(tx.QueryRowContext(ctx, "SELECT "+provisioningJobColumns+" FROM provisioning_jobs WHERE order_id=? FOR UPDATE", o.ID))
	if err == nil {
		if err = tx.Commit(); err != nil {
			return ProvisioningJob{}, false, err
		}
		return cloneProvisioningJob(r.Job), true, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return ProvisioningJob{}, false, err
	}
	r, err = newProvisioningRecord(o, snapshot)
	if err != nil {
		return ProvisioningJob{}, false, err
	}
	raw, _, _ := r.Request.BytesAndDigest()
	_, err = tx.ExecContext(ctx, "INSERT INTO provisioning_jobs(id,order_id,state,request,request_sha256,attempts,created_at,updated_at) VALUES(?,?,?,?,?,0,?,?)", r.Job.ID, o.ID, r.Job.State, raw, r.Job.RequestSHA256, r.Job.CreatedAt, r.Job.UpdatedAt)
	if err != nil {
		return ProvisioningJob{}, false, err
	}
	a := catalogAudit(admin, "provisioning.queued", o.ID, snapshot.Revision, reason)
	a.UserID = o.UserID
	_, err = tx.ExecContext(ctx, "INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) VALUES(?,?,?,?,?,?,?)", a.ID, a.UserID, a.Action, a.Target, a.CreatedAt, a.ActorEmail, a.Detail)
	if err != nil {
		return ProvisioningJob{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return ProvisioningJob{}, false, err
	}
	return cloneProvisioningJob(r.Job), false, nil
}
func (m *MySQL) ProvisioningJob(ctx context.Context, actor User, jobID string) (ProvisioningJob, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ProvisioningJob{}, err
	}
	defer tx.Rollback()
	if _, err = lockActiveAdministrator(ctx, tx, actor.ID); err != nil {
		return ProvisioningJob{}, err
	}
	r, err := readProvisioningJob(tx.QueryRowContext(ctx, "SELECT "+provisioningJobColumns+" FROM provisioning_jobs WHERE id=?", jobID))
	if err != nil {
		return ProvisioningJob{}, err
	}
	if err = tx.Commit(); err != nil {
		return ProvisioningJob{}, err
	}
	return cloneProvisioningJob(r.Job), nil
}
func (m *MySQL) ProvisioningRequest(ctx context.Context, jobID string) (controllers.Request, error) {
	r, err := readProvisioningJob(m.DB.QueryRowContext(ctx, "SELECT "+provisioningJobColumns+" FROM provisioning_jobs WHERE id=?", jobID))
	if err != nil {
		return controllers.Request{}, err
	}
	return cloneProvisioningRequest(r.Request), nil
}
func writeProvisioningJob(ctx context.Context, tx *sql.Tx, r provisioningJobRecord) error {
	var result any
	if r.Job.Result != nil {
		b, _ := json.Marshal(r.Job.Result)
		result = b
	}
	_, err := tx.ExecContext(ctx, "UPDATE provisioning_jobs SET state=?,claim_token=?,lease_expires_at=?,result=?,attempts=?,updated_at=? WHERE id=?", r.Job.State, r.ClaimToken, r.LeaseExpiresAt, result, r.Attempts, r.Job.UpdatedAt, r.Job.ID)
	return err
}
func insertProvisioningStateAudit(ctx context.Context, tx *sql.Tx, r provisioningJobRecord, previous string) error {
	if previous == r.Job.State {
		return nil
	}
	a := provisioningStateAudit(r, previous)
	_, err := tx.ExecContext(ctx, "INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) VALUES(?,?,?,?,?,?,?)", a.ID, a.UserID, a.Action, a.Target, a.CreatedAt, a.ActorEmail, a.Detail)
	return err
}
func lockPaidProvisioningOrder(ctx context.Context, tx *sql.Tx, orderID string) (Order, error) {
	if _, _, err := readCurrencies(ctx, tx, " FOR SHARE"); err != nil {
		return Order{}, err
	}
	o, err := readCancellationOrder(tx.QueryRowContext(ctx, "SELECT "+orderColumns+" FROM orders WHERE id=? FOR UPDATE", orderID))
	if err != nil {
		return Order{}, err
	}
	i, err := readCancellationInvoice(ctx, tx, o.ID)
	if err != nil {
		return Order{}, err
	}
	if !paidProvisioningPair(User{ID: o.UserID, Role: "customer"}, o, i) {
		return Order{}, ErrProvisioningState
	}
	return o, nil
}
func (m *MySQL) ClaimProvisioningJob(ctx context.Context, jobID string) (ProvisioningClaim, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ProvisioningClaim{}, err
	}
	defer tx.Rollback()
	var orderID string
	if err = tx.QueryRowContext(ctx, "SELECT order_id FROM provisioning_jobs WHERE id=?", jobID).Scan(&orderID); errors.Is(err, sql.ErrNoRows) {
		return ProvisioningClaim{}, ErrNotFound
	} else if err != nil {
		return ProvisioningClaim{}, err
	}
	o, err := lockPaidProvisioningOrder(ctx, tx, orderID)
	if err != nil {
		return ProvisioningClaim{}, err
	}
	r, err := readProvisioningJob(tx.QueryRowContext(ctx, "SELECT "+provisioningJobColumns+" FROM provisioning_jobs WHERE id=? FOR UPDATE", jobID))
	if err != nil {
		return ProvisioningClaim{}, err
	}
	if r.Job.OrderID != orderID || o.UserID != r.Request.OwnerRef || o.Hostname != r.Request.Hostname {
		return ProvisioningClaim{}, ErrProvisioningState
	}
	oldState := r.Job.State
	claimErr := claimProvisioning(&r, time.Now().UTC().Truncate(time.Microsecond))
	if claimErr != nil && r.Job.State == oldState {
		return ProvisioningClaim{}, claimErr
	}
	if err = writeProvisioningJob(ctx, tx, r); err != nil {
		return ProvisioningClaim{}, err
	}
	if err = insertProvisioningStateAudit(ctx, tx, r, oldState); err != nil {
		return ProvisioningClaim{}, err
	}
	if err = tx.Commit(); err != nil {
		return ProvisioningClaim{}, err
	}
	if claimErr != nil {
		return ProvisioningClaim{}, claimErr
	}
	return ProvisioningClaim{Job: cloneProvisioningJob(r.Job), Request: cloneProvisioningRequest(r.Request), ClaimToken: r.ClaimToken, LeaseExpiresAt: *r.LeaseExpiresAt}, nil
}
func (m *MySQL) CompleteProvisioningJob(ctx context.Context, jobID, token string, result *controllers.Result) (ProvisioningJob, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ProvisioningJob{}, err
	}
	defer tx.Rollback()
	r, err := readProvisioningJob(tx.QueryRowContext(ctx, "SELECT "+provisioningJobColumns+" FROM provisioning_jobs WHERE id=? FOR UPDATE", jobID))
	if err != nil {
		return ProvisioningJob{}, err
	}
	previous := r.Job.State
	if err = completeProvisioning(&r, token, result, time.Now().UTC().Truncate(time.Microsecond)); err != nil {
		return ProvisioningJob{}, err
	}
	if err = writeProvisioningJob(ctx, tx, r); err != nil {
		return ProvisioningJob{}, err
	}
	if err = insertProvisioningStateAudit(ctx, tx, r, previous); err != nil {
		return ProvisioningJob{}, err
	}
	if err = tx.Commit(); err != nil {
		return ProvisioningJob{}, err
	}
	return cloneProvisioningJob(r.Job), nil
}
func (m *MySQL) ReconcileProvisioningJob(ctx context.Context, jobID string, result controllers.Result) (ProvisioningJob, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ProvisioningJob{}, err
	}
	defer tx.Rollback()
	r, err := readProvisioningJob(tx.QueryRowContext(ctx, "SELECT "+provisioningJobColumns+" FROM provisioning_jobs WHERE id=? FOR UPDATE", jobID))
	if err != nil {
		return ProvisioningJob{}, err
	}
	previous := r.Job.State
	if err = reconcileProvisioning(&r, result, time.Now().UTC().Truncate(time.Microsecond)); err != nil {
		return ProvisioningJob{}, err
	}
	if err = writeProvisioningJob(ctx, tx, r); err != nil {
		return ProvisioningJob{}, err
	}
	if err = insertProvisioningStateAudit(ctx, tx, r, previous); err != nil {
		return ProvisioningJob{}, err
	}
	if err = tx.Commit(); err != nil {
		return ProvisioningJob{}, err
	}
	return cloneProvisioningJob(r.Job), nil
}
