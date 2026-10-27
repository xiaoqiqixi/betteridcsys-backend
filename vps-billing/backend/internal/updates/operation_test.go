package updates

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func operationFixture() Operation {
	return Operation{Schema: 1, OperationID: strings.Repeat("a", 32), Action: "install", Status: "executing", Phase: "confirmed", TargetCoreVersion: "0.5.2", TargetSourceSHA: strings.Repeat("b", 40), ServiceAction: "none", ProgramRollback: "none", BusinessValidationRequired: true, StartedAt: "2026-10-15T02:00:00Z", UpdatedAt: "2026-10-15T02:00:01Z", ErrorCode: "none"}
}

func receiptJSON(t *testing.T, operation Operation) []byte {
	t.Helper()
	raw, err := json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestOperationReceiptAcceptsObservedStatesWithoutInferringReadiness(t *testing.T) {
	for _, status := range []string{"checking", "prepared", "executing", "failed", "needs_manual_recovery", "succeeded", "rolled_back"} {
		t.Run(status, func(t *testing.T) {
			operation := operationFixture()
			operation.Status = status
			switch status {
			case "failed", "needs_manual_recovery":
				operation.ErrorCode = "operation_failed"
			case "succeeded":
				operation.Phase = "complete"
				operation.DatabaseMigration = DatabaseMigration{Started: true, Completed: true}
				operation.ActiveReleaseChanged = true
				operation.ServiceAction = "started"
				operation.HTTPSReady = true
			case "rolled_back":
				operation.Action = "upgrade"
				operation.PreviousCoreVersion = "0.5.1"
				operation.PreviousSourceSHA = strings.Repeat("c", 40)
				operation.Maintenance = true
				operation.ServiceAction = "previous_started"
				operation.ProgramRollback = "verified_old_schema"
				operation.ErrorCode = "operation_failed"
			}
			got, err := parseOperation(receiptJSON(t, operation))
			if err != nil || got != operation {
				t.Fatal("valid observed operation rejected", err)
			}
			if status != "succeeded" && got.HTTPSReady {
				t.Fatal("nonterminal operation inferred readiness")
			}
		})
	}
	for _, phase := range []string{"confirmed", "install_preflight", "provision", "configure", "old_schema_preflight", "maintenance", "backup", "migrate", "first_admin", "activate", "readiness", "complete"} {
		operation := operationFixture()
		operation.Phase = phase
		if _, err := parseOperation(receiptJSON(t, operation)); err != nil {
			t.Fatal("contract phase rejected", phase)
		}
	}
}

func TestOperationReceiptRequiresEveryExplicitField(t *testing.T) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(receiptJSON(t, operationFixture()), &fields); err != nil {
		t.Fatal(err)
	}
	for key := range fields {
		t.Run(key, func(t *testing.T) {
			copy := map[string]json.RawMessage{}
			for name, value := range fields {
				if name != key {
					copy[name] = value
				}
			}
			raw, _ := json.Marshal(copy)
			if got, err := parseOperation(raw); !errors.Is(err, errOperationReceipt) || got != (Operation{}) {
				t.Fatal("missing field became a zero-value fact", key)
			}
			// DecodeStrict must reject JSON null before typed decoding can map it
			// to false, zero, or an empty previous-release string.
			copy[key] = json.RawMessage("null")
			raw, _ = json.Marshal(copy)
			if got, err := parseOperation(raw); !errors.Is(err, errOperationReceipt) || got != (Operation{}) {
				t.Fatal("null field became a zero-value fact", key)
			}
		})
	}
	for _, migration := range []string{`{}`, `{"started":false}`, `{"completed":false}`, `{"started":null,"completed":false}`, `{"started":false,"completed":null}`} {
		fields["database_migration"] = json.RawMessage(migration)
		raw, _ := json.Marshal(fields)
		if _, err := parseOperation(raw); !errors.Is(err, errOperationReceipt) {
			t.Fatal("incomplete migration accepted", migration)
		}
	}
}

func TestOperationReceiptRejectsAmbiguousAndSecretBearingJSON(t *testing.T) {
	valid := string(receiptJSON(t, operationFixture()))
	for name, raw := range map[string]string{
		"null": "null", "array": "[]", "trailing": valid + `{}`, "non_utf8": valid + string([]byte{0xff}), "oversized": valid + strings.Repeat(" ", maxOperationReceiptBytes),
		"duplicate":        strings.Replace(valid, `"schema":1`, `"schema":1,"schema":1`, 1),
		"case_alias":       strings.Replace(valid, `"schema":1`, `"Schema":1`, 1),
		"unknown_output":   strings.Replace(valid, `"schema":1`, `"schema":1,"output":"token=SECRET"`, 1),
		"unknown_path":     strings.Replace(valid, `"schema":1`, `"schema":1,"path":"/etc/SECRET"`, 1),
		"null_bool":        strings.Replace(valid, `"maintenance":false`, `"maintenance":null`, 1),
		"null_previous":    strings.Replace(valid, `"previous_core_version":""`, `"previous_core_version":null`, 1),
		"bool_string":      strings.Replace(valid, `"maintenance":false`, `"maintenance":"false"`, 1),
		"nested_unknown":   strings.Replace(valid, `"started":false`, `"started":false,"output":"SECRET"`, 1),
		"nested_duplicate": strings.Replace(valid, `"started":false`, `"started":false,"started":true`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseOperation([]byte(raw))
			if !errors.Is(err, errOperationReceipt) || got != (Operation{}) || strings.Contains(err.Error(), "SECRET") {
				t.Fatal("unsafe receipt accepted or exposed", name)
			}
		})
	}
}

func TestOperationReceiptRejectsInvalidFactsAndContradictorySuccess(t *testing.T) {
	for name, change := range map[string]func(*Operation){
		"schema":                   func(v *Operation) { v.Schema = 2 },
		"id":                       func(v *Operation) { v.OperationID = strings.Repeat("A", 32) },
		"action":                   func(v *Operation) { v.Action = "shell" },
		"status":                   func(v *Operation) { v.Status = "running" },
		"phase":                    func(v *Operation) { v.Phase = "SECRET=/etc/config" },
		"version":                  func(v *Operation) { v.TargetCoreVersion = "v0.5.2" },
		"source":                   func(v *Operation) { v.TargetSourceSHA = strings.Repeat("B", 40) },
		"partial_previous":         func(v *Operation) { v.PreviousCoreVersion = "0.5.1" },
		"install_previous":         func(v *Operation) { v.PreviousCoreVersion, v.PreviousSourceSHA = "0.5.1", strings.Repeat("c", 40) },
		"upgrade_missing_previous": func(v *Operation) { v.Action = "upgrade" },
		"service_command":          func(v *Operation) { v.ServiceAction = "systemctl start SECRET" },
		"rollback_command":         func(v *Operation) { v.ProgramRollback = "rm SECRET" },
		"raw_error":                func(v *Operation) { v.ErrorCode = "token=SECRET" },
		"offset_timestamp":         func(v *Operation) { v.StartedAt = "2026-10-15T02:00:00+00:00" },
		"invalid_timestamp":        func(v *Operation) { v.StartedAt = "not-a-time" },
		"backward_timestamp":       func(v *Operation) { v.UpdatedAt = "2026-10-15T01:59:59Z" },
		"zero_timestamp":           func(v *Operation) { v.StartedAt = "0001-01-01T00:00:00Z" },
		"migration_without_start":  func(v *Operation) { v.DatabaseMigration.Completed = true },
		"optimistic_readiness":     func(v *Operation) { v.HTTPSReady = true },
		"optimistic_success":       func(v *Operation) { v.Status = "succeeded" },
		"optimistic_rollback":      func(v *Operation) { v.Status = "rolled_back" },
		"rollback_without_outcome": func(v *Operation) { v.ProgramRollback = "verified_old_schema" },
	} {
		t.Run(name, func(t *testing.T) {
			operation := operationFixture()
			change(&operation)
			if got, err := parseOperation(receiptJSON(t, operation)); !errors.Is(err, errOperationReceipt) || got != (Operation{}) {
				t.Fatal("invalid operation accepted", name)
			}
		})
	}
}
