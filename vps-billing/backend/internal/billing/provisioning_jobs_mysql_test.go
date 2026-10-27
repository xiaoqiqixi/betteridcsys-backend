package billing

import (
	"reflect"
	"testing"
)

func TestMySQLProvisioningJobAuditFailureRollsBackState(t *testing.T) {
	f, pid, config := productConfigurationFixture(t, mysqlCatalogFixture)
	ctx := t.Context()
	m := f.store.(*MySQL)
	jobs := f.store.(ProvisioningJobStore)
	initial, err := config.ProductConfiguration(ctx, f.admin, pid)
	if err != nil {
		t.Fatal(err)
	}
	migrationExec(t, ctx, m.DB, "CREATE TRIGGER provisioning_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='fixture audit refusal'")
	if _, err = config.UpdateProductConfiguration(ctx, f.admin, pid, ProductConfigurationInput{Revision: 1, Spec: presetProductSpec(), Reason: "manual"}); err == nil {
		t.Fatal("configuration committed without audit")
	}
	migrationExec(t, ctx, m.DB, "DROP TRIGGER provisioning_reject_audit")
	stored, err := config.ProductConfiguration(ctx, f.admin, pid)
	if err != nil || !reflect.DeepEqual(initial, stored) {
		t.Fatal("config/price revision leaked on late audit failure", stored, err)
	}
	if _, err = config.UpdateProductConfiguration(ctx, f.admin, pid, ProductConfigurationInput{Revision: 1, Spec: presetProductSpec(), Reason: "manual"}); err != nil {
		t.Fatal(err)
	}
	o := provisioningOrder(t, f, pid, true)
	migrationExec(t, ctx, m.DB, "CREATE TRIGGER provisioning_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='fixture audit refusal'")
	if _, _, err = jobs.CreateProvisioningJob(ctx, f.admin, o.ID, "manual"); err == nil {
		t.Fatal("queue committed without audit")
	}
	var count int
	if err = m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM provisioning_jobs WHERE order_id=?", o.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("queue rollback leaked", count, err)
	}
	migrationExec(t, ctx, m.DB, "DROP TRIGGER provisioning_reject_audit")
	job, _, err := jobs.CreateProvisioningJob(ctx, f.admin, o.ID, "manual")
	if err != nil {
		t.Fatal(err)
	}
	migrationExec(t, ctx, m.DB, "CREATE TRIGGER provisioning_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='fixture audit refusal'")
	if _, err = jobs.ClaimProvisioningJob(ctx, job.ID); err == nil {
		t.Fatal("claim committed without audit")
	}
	storedJob, err := jobs.ProvisioningJob(ctx, f.admin, job.ID)
	if err != nil || storedJob.State != "queued" {
		t.Fatal("claim rollback leaked", storedJob, err)
	}
	migrationExec(t, ctx, m.DB, "DROP TRIGGER provisioning_reject_audit")
	claim, err := jobs.ClaimProvisioningJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	migrationExec(t, ctx, m.DB, "CREATE TRIGGER provisioning_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='fixture audit refusal'")
	result := provisioningResult(claim.Request, "accepted")
	if _, err = jobs.CompleteProvisioningJob(ctx, job.ID, claim.ClaimToken, &result); err == nil {
		t.Fatal("complete committed without audit")
	}
	storedJob, err = jobs.ProvisioningJob(ctx, f.admin, job.ID)
	if err != nil || storedJob.State != "sending" || storedJob.Result != nil {
		t.Fatal("complete rollback leaked", storedJob, err)
	}
	migrationExec(t, ctx, m.DB, "DROP TRIGGER provisioning_reject_audit")
	if _, err = jobs.CompleteProvisioningJob(ctx, job.ID, claim.ClaimToken, nil); err != nil {
		t.Fatal(err)
	}
	migrationExec(t, ctx, m.DB, "CREATE TRIGGER provisioning_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='fixture audit refusal'")
	if _, err = jobs.ReconcileProvisioningJob(ctx, job.ID, provisioningResult(claim.Request, "succeeded")); err == nil {
		t.Fatal("lookup result committed without audit")
	}
	storedJob, err = jobs.ProvisioningJob(ctx, f.admin, job.ID)
	if err != nil || storedJob.State != "uncertain" || storedJob.Result != nil {
		t.Fatal("reconcile rollback leaked", storedJob, err)
	}
	migrationExec(t, ctx, m.DB, "DROP TRIGGER provisioning_reject_audit")
	if _, err = jobs.ReconcileProvisioningJob(ctx, job.ID, provisioningResult(claim.Request, "succeeded")); err != nil {
		t.Fatal(err)
	}
}
