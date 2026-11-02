package main

import (
	"aster/billing/internal/billing"
	api "aster/billing/internal/financeapi"
	"aster/billing/internal/modulejobs"
	"aster/billing/internal/runtimeplugins"
	"aster/billing/pkg/pluginapi/serverv1"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"github.com/go-sql-driver/mysql"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

type moduleTransport interface {
	Send(context.Context, modulejobs.Request) (modulejobs.Result, error)
	Lookup(context.Context, modulejobs.Request) (modulejobs.Result, error)
}

// An explicitly provided empty job ID is an invalid command, not a request to
// start a web server. It also participates in command mutual exclusion.
func provisioningFlagsSelected(flags *flag.FlagSet) (dispatch, reconcile bool) {
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "dispatch-provisioning":
			dispatch = true
		case "reconcile-provisioning":
			reconcile = true
		}
	})
	return
}

// This root-only path retains the legacy explicit host dispatch contract.
// Commerce tasks also have an independently configured in-process dispatcher;
// it never invokes this command or borrows these private host credentials.
func performProvisioning(ctx context.Context, store billing.ProvisioningJobStore, jobID string, reconcile bool, prepare func(context.Context, string) (moduleTransport, error)) (billing.ProvisioningJob, error) {
	request, err := store.ProvisioningRequest(ctx, jobID)
	if err != nil {
		return billing.ProvisioningJob{}, err
	}
	client, err := prepare(ctx, request.Product.ConnectionID)
	if err != nil {
		return billing.ProvisioningJob{}, err // Do not claim an unavailable target.
	}
	if reconcile {
		result, err := client.Lookup(ctx, request)
		if err != nil {
			return billing.ProvisioningJob{}, modulejobs.ErrUncertain
		}
		return store.ReconcileProvisioningJob(ctx, jobID, result)
	}
	claim, err := store.ClaimProvisioningJob(ctx, jobID)
	if err != nil {
		return billing.ProvisioningJob{}, err
	}
	_, expected, beforeErr := request.BytesAndDigest()
	_, claimed, claimErr := claim.Request.BytesAndDigest()
	var result *modulejobs.Result
	var sendErr error
	if beforeErr != nil || claimErr != nil || expected != claimed || request.Product.ConnectionID != claim.Request.Product.ConnectionID {
		sendErr = modulejobs.ErrUncertain
	} else {
		value, err := client.Send(ctx, claim.Request)
		sendErr = err
		if err == nil {
			result = &value
		}
	}
	// Cancellation/HTTP timeout must not prevent persisting the unknown outcome.
	// Never replay the network operation if the completion write is uncertain.
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	job, err := store.CompleteProvisioningJob(finish, jobID, claim.ClaimToken, result)
	if err != nil {
		return billing.ProvisioningJob{}, modulejobs.ErrUncertain
	}
	if sendErr != nil || job.State == "uncertain" {
		return job, modulejobs.ErrUncertain
	}
	return job, nil
}

func runHostProvisioning(out io.Writer, jobID string, reconcile bool) int {
	response := struct {
		Status string                   `json:"status"`
		Code   string                   `json:"code"`
		Job    *billing.ProvisioningJob `json:"job,omitempty"`
		Task   *api.ServiceTask         `json:"task,omitempty"`
	}{Status: "unavailable", Code: "PROVISIONING_UNAVAILABLE"}
	emit := func(code int) int { _ = json.NewEncoder(out).Encode(response); return code }
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(jobID) {
		response.Code = "INVALID_JOB_ID"
		return emit(2)
	}
	if os.Geteuid() != 0 {
		response.Code = "HOST_OPERATOR_REQUIRED"
		return emit(2)
	}
	// These dedicated host files are not accepted from web requests and are not
	// borrowed from API runtime environment, session secrets or plugin settings.
	dsn, err := modulejobs.ReadHostFile(os.Getenv("PROVISIONING_MYSQL_DSN_FILE"), true, 16<<10)
	if err != nil {
		return emit(2)
	}
	prepare, err := hostProvisioningTransport(os.Getenv("PROVISIONING_TRANSPORT"), os.Getenv("PROVISIONING_POLICY_FILE"), os.Getenv("PROVISIONING_PLUGIN_ROOT"), os.Getenv("PROVISIONING_PLUGIN_POLICY_FILE"))
	if err != nil {
		return emit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	store, err := openProvisioningMySQL(ctx, strings.TrimSpace(string(dsn)))
	if err != nil {
		response.Code = provisioningMySQLErrorCode(err)
		return emit(2)
	}
	defer store.DB.Close()
	if store.Ping(ctx) != nil {
		response.Code = "SCHEMA_NOT_READY"
		return emit(2)
	}
	var serviceID string
	serviceErr := store.DB.QueryRowContext(ctx, "SELECT service_id FROM service_tasks WHERE id=?", jobID).Scan(&serviceID)
	if serviceErr == nil {
		runtime := billing.NewServerModules06(runtimeplugins.New(os.Getenv("PROVISIONING_PLUGIN_ROOT"), os.Getenv("PROVISIONING_PLUGIN_POLICY_FILE")))
		app := billing.NewApp(store, billing.Config{Services: runtime})
		task, taskErr := app.DispatchServiceTask06(ctx, jobID, reconcile, true)
		if task.ID != "" {
			response.Task = &task
		}
		if taskErr != nil {
			response.Code = "SERVICE_TASK_UNAVAILABLE"
			return emit(1)
		}
		response.Status, response.Code = task.Status, "none"
		if task.Status == "failed" || task.Status == "uncertain" {
			return emit(1)
		}
		return emit(0)
	}
	if !errors.Is(serviceErr, sql.ErrNoRows) {
		return emit(1)
	}
	job, err := performProvisioning(ctx, store, jobID, reconcile, prepare)
	if job.ID != "" {
		response.Job = &job
	}
	if err != nil {
		switch {
		case errors.Is(err, modulejobs.ErrUncertain):
			response.Status, response.Code = "uncertain", "PROVISIONING_UNCERTAIN"
		case errors.Is(err, billing.ErrProvisioningState):
			response.Status, response.Code = "blocked", "PROVISIONING_STATE_CONFLICT"
		case errors.Is(err, billing.ErrNotFound):
			response.Status, response.Code = "not_found", "NOT_FOUND"
		}
		return emit(1)
	}
	response.Status, response.Code = job.State, "none"
	if job.State == "rejected" {
		return emit(1)
	}
	return emit(0)
}

// Selection exists only in the explicit root host command, never the web
// settings/API. Choosing resource disables direct HTTPS fallback completely.
func hostProvisioningTransport(mode, obsoletePolicy, pluginRoot, pluginPolicy string) (func(context.Context, string) (moduleTransport, error), error) {
	if mode != "" && mode != "server" || obsoletePolicy != "" || pluginRoot == "" || pluginPolicy == "" {
		return nil, modulejobs.ErrUnavailable
	}
	manager := runtimeplugins.New(pluginRoot, pluginPolicy)
	return func(ctx context.Context, id string) (moduleTransport, error) {
		p, err := manager.PrepareServer(ctx, id)
		if err != nil {
			return nil, err
		}
		return moduleJobTransport{p}, nil
	}, nil
}

type moduleJobTransport struct{ serverv1.Processor }

func (c moduleJobTransport) Send(ctx context.Context, r modulejobs.Request) (modulejobs.Result, error) {
	return c.call(ctx, r, false)
}
func (c moduleJobTransport) Lookup(ctx context.Context, r modulejobs.Request) (modulejobs.Result, error) {
	return c.call(ctx, r, true)
}
func (c moduleJobTransport) call(ctx context.Context, r modulejobs.Request, lookup bool) (modulejobs.Result, error) {
	if !r.Valid() {
		return modulejobs.Result{}, modulejobs.ErrConfiguration
	}
	described, err := c.Call(ctx, serverv1.Request{Protocol: serverv1.Protocol, Method: "describe"})
	if err != nil || described.Description == nil || described.Description.ID != r.Product.ModuleID {
		return modulejobs.Result{}, modulejobs.ErrUnavailable
	}
	request := serverv1.Request{Protocol: serverv1.Protocol, Method: "create", OperationID: r.OperationID, ServiceID: r.OrderID, OwnerRef: r.OwnerRef, Config: r.Product.Config, Input: serverv1.Fields{"hostname": r.Hostname}}
	if err = request.Seal(); err != nil {
		return modulejobs.Result{}, err
	}
	if lookup {
		request = serverv1.Request{Protocol: serverv1.Protocol, Method: "operation_status", OperationID: r.OperationID, ServiceID: r.OrderID, OwnerRef: r.OwnerRef, Digest: request.Digest}
	}
	response, err := c.Call(ctx, request)
	if err != nil || !response.Matches(request) {
		return modulejobs.Result{}, modulejobs.ErrUncertain
	}
	_, hash, _ := r.BytesAndDigest()
	out := modulejobs.Result{Protocol: modulejobs.Protocol, OperationID: r.OperationID, OrderID: r.OrderID, RequestSHA256: hash, ResourceID: response.ResourceID}
	switch response.State {
	case "done":
		out.Status = "succeeded"
	case "accepted":
		out.Status = "accepted"
	case "failed":
		out.Status = "rejected"
		out.ResourceID = ""
	default:
		return out, modulejobs.ErrUncertain
	}
	return out, nil
}

// A host command opts into the local socket exception only for a Unix DSN.
// Remote connections use exactly the API server's certificate verification.
func openProvisioningMySQL(ctx context.Context, dsn string) (*billing.MySQL, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, billing.ErrMySQLConfiguration
	}
	return billing.OpenMySQL(ctx, dsn, cfg.Net == "unix")
}
func provisioningMySQLErrorCode(err error) string {
	if errors.Is(err, billing.ErrMySQLConfiguration) {
		return "PROVISIONING_MYSQL_CONFIGURATION_INVALID"
	}
	return "PROVISIONING_MYSQL_CONNECTION_FAILED"
}
