package main

import (
	"aster/billing/internal/billing"
	"aster/billing/internal/controllers"
	"aster/billing/internal/runtimeplugins"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

type controllerTransport interface {
	Send(context.Context, controllers.Request) (controllers.Result, error)
	Lookup(context.Context, controllers.Request) (controllers.Result, error)
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

// A root-owned one-shot command is the only network dispatch path. Web handlers
// may prepare a paid order's immutable job, but never read host credentials,
// invoke this command, or directly send provisioning requests.
func performProvisioning(ctx context.Context, store billing.ProvisioningJobStore, jobID string, reconcile bool, prepare func(context.Context, string) (controllerTransport, error)) (billing.ProvisioningJob, error) {
	request, err := store.ProvisioningRequest(ctx, jobID)
	if err != nil {
		return billing.ProvisioningJob{}, err
	}
	client, err := prepare(ctx, request.Product.ControllerID)
	if err != nil {
		return billing.ProvisioningJob{}, err // Do not claim an unavailable target.
	}
	if reconcile {
		result, err := client.Lookup(ctx, request)
		if err != nil {
			return billing.ProvisioningJob{}, controllers.ErrUncertain
		}
		return store.ReconcileProvisioningJob(ctx, jobID, result)
	}
	claim, err := store.ClaimProvisioningJob(ctx, jobID)
	if err != nil {
		return billing.ProvisioningJob{}, err
	}
	_, expected, beforeErr := request.BytesAndDigest()
	_, claimed, claimErr := claim.Request.BytesAndDigest()
	var result *controllers.Result
	var sendErr error
	if beforeErr != nil || claimErr != nil || expected != claimed || request.Product.ControllerID != claim.Request.Product.ControllerID {
		sendErr = controllers.ErrUncertain
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
		return billing.ProvisioningJob{}, controllers.ErrUncertain
	}
	if sendErr != nil || job.State == "uncertain" {
		return job, controllers.ErrUncertain
	}
	return job, nil
}

func runHostProvisioning(out io.Writer, jobID string, reconcile bool) int {
	response := struct {
		Status string                   `json:"status"`
		Code   string                   `json:"code"`
		Job    *billing.ProvisioningJob `json:"job,omitempty"`
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
	dsn, err := controllers.ReadHostFile(os.Getenv("PROVISIONING_MYSQL_DSN_FILE"), true, 16<<10)
	if err != nil {
		return emit(2)
	}
	prepare, err := hostProvisioningTransport(os.Getenv("PROVISIONING_TRANSPORT"), os.Getenv("PROVISIONING_POLICY_FILE"), os.Getenv("PROVISIONING_PLUGIN_ROOT"), os.Getenv("PROVISIONING_PLUGIN_POLICY_FILE"))
	if err != nil {
		return emit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	store, err := billing.OpenMySQL(ctx, strings.TrimSpace(string(dsn)), true)
	if err != nil {
		return emit(2)
	}
	defer store.DB.Close()
	if store.Ping(ctx) != nil {
		response.Code = "SCHEMA_NOT_READY"
		return emit(2)
	}
	job, err := performProvisioning(ctx, store, jobID, reconcile, prepare)
	if job.ID != "" {
		response.Job = &job
	}
	if err != nil {
		switch {
		case errors.Is(err, controllers.ErrUncertain):
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
func hostProvisioningTransport(mode, policyPath, pluginRoot, pluginPolicy string) (func(context.Context, string) (controllerTransport, error), error) {
	switch mode {
	case "", "https":
		if pluginRoot != "" || pluginPolicy != "" {
			return nil, controllers.ErrUnavailable
		}
		policy, err := controllers.LoadPolicy(policyPath)
		if err != nil {
			return nil, err
		}
		return func(ctx context.Context, id string) (controllerTransport, error) { return policy.Client(ctx, id) }, nil
	case "resource":
		if policyPath != "" || pluginRoot == "" || pluginPolicy == "" {
			return nil, controllers.ErrUnavailable
		}
		manager := runtimeplugins.New(pluginRoot, pluginPolicy)
		return func(ctx context.Context, id string) (controllerTransport, error) {
			return manager.PrepareResource(ctx, id)
		}, nil
	default:
		return nil, controllers.ErrUnavailable
	}
}
