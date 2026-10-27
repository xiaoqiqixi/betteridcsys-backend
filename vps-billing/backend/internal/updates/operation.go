package updates

import (
	"aster/billing/pkg/pluginapi"
	"encoding/json"
	"strings"
	"time"
)

// Operation is the bounded public projection written by the protected native
// administrator tool. It deliberately has no paths, commands, output or secrets.
type Operation struct {
	Schema                     int               `json:"schema"`
	OperationID                string            `json:"operation_id"`
	Action                     string            `json:"action"`
	Status                     string            `json:"status"`
	Phase                      string            `json:"phase"`
	TargetCoreVersion          string            `json:"target_core_version"`
	TargetSourceSHA            string            `json:"target_source_sha"`
	PreviousCoreVersion        string            `json:"previous_core_version"`
	PreviousSourceSHA          string            `json:"previous_source_sha"`
	Maintenance                bool              `json:"maintenance"`
	DatabaseMigration          DatabaseMigration `json:"database_migration"`
	ActiveReleaseChanged       bool              `json:"active_release_changed"`
	ServiceAction              string            `json:"service_action"`
	ProgramRollback            string            `json:"program_rollback"`
	HTTPSReady                 bool              `json:"https_ready"`
	BusinessValidationRequired bool              `json:"business_validation_required"`
	StartedAt                  string            `json:"started_at"`
	UpdatedAt                  string            `json:"updated_at"`
	ErrorCode                  string            `json:"error_code"`
}

type DatabaseMigration struct {
	Started   bool `json:"started"`
	Completed bool `json:"completed"`
}

func parseOperation(raw []byte) (Operation, error) {
	var operation Operation
	if pluginapi.DecodeStrict(raw, maxOperationReceiptBytes, &operation) != nil {
		return Operation{}, errOperationReceipt
	}
	// Explicit false and empty previous-release strings carry meaning. Missing
	// fields must not turn Go zero values into evidence of a completed operation.
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 19 {
		return Operation{}, errOperationReceipt
	}
	for _, key := range []string{"schema", "operation_id", "action", "status", "phase", "target_core_version", "target_source_sha", "previous_core_version", "previous_source_sha", "maintenance", "database_migration", "active_release_changed", "service_action", "program_rollback", "https_ready", "business_validation_required", "started_at", "updated_at", "error_code"} {
		if _, present := fields[key]; !present {
			return Operation{}, errOperationReceipt
		}
	}
	var migration map[string]json.RawMessage
	if json.Unmarshal(fields["database_migration"], &migration) != nil || len(migration) != 2 || migration["started"] == nil || migration["completed"] == nil {
		return Operation{}, errOperationReceipt
	}
	if operation.Schema != 1 || !actorPattern.MatchString(operation.OperationID) || !oneOf(operation.Action, "install", "upgrade", "recover") || !oneOf(operation.Status, "checking", "prepared", "executing", "succeeded", "failed", "needs_manual_recovery", "rolled_back") || !oneOf(operation.Phase, "confirmed", "install_preflight", "provision", "configure", "old_schema_preflight", "maintenance", "backup", "migrate", "first_admin", "activate", "readiness", "complete") || !pluginapi.ValidVersion(operation.TargetCoreVersion) || !commitPattern.MatchString(operation.TargetSourceSHA) || !oneOf(operation.ServiceAction, "none", "stopped", "started", "previous_started") || !oneOf(operation.ProgramRollback, "none", "verified_old_schema", "blocked_schema_or_readiness") || !oneOf(operation.ErrorCode, "none", "operation_failed", "interrupted_requires_recovery") {
		return Operation{}, errOperationReceipt
	}
	previousAbsent := operation.PreviousCoreVersion == "" && operation.PreviousSourceSHA == ""
	if !previousAbsent && (!pluginapi.ValidVersion(operation.PreviousCoreVersion) || !commitPattern.MatchString(operation.PreviousSourceSHA)) || operation.Action == "install" && !previousAbsent || operation.Action == "upgrade" && previousAbsent {
		return Operation{}, errOperationReceipt
	}
	started, startErr := time.Parse(time.RFC3339Nano, operation.StartedAt)
	updated, updateErr := time.Parse(time.RFC3339Nano, operation.UpdatedAt)
	if startErr != nil || updateErr != nil || !strings.HasSuffix(operation.StartedAt, "Z") || !strings.HasSuffix(operation.UpdatedAt, "Z") || started.Year() < 2020 || updated.Before(started) {
		return Operation{}, errOperationReceipt
	}
	if operation.DatabaseMigration.Completed && !operation.DatabaseMigration.Started || operation.HTTPSReady && (operation.Status != "succeeded" || operation.Phase != "complete" || operation.Maintenance) {
		return Operation{}, errOperationReceipt
	}
	if operation.Status == "succeeded" && (operation.Phase != "complete" || !operation.HTTPSReady || operation.Maintenance || !operation.DatabaseMigration.Completed || !operation.ActiveReleaseChanged || operation.ServiceAction != "started" || operation.ProgramRollback != "none" || operation.ErrorCode != "none") {
		return Operation{}, errOperationReceipt
	}
	if operation.Status == "rolled_back" && (operation.ProgramRollback != "verified_old_schema" || previousAbsent || operation.ActiveReleaseChanged || operation.ServiceAction != "previous_started") || operation.ProgramRollback == "verified_old_schema" && operation.Status != "rolled_back" {
		return Operation{}, errOperationReceipt
	}
	return operation, nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
