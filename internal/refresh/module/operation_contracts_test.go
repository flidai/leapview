package module

import (
	"reflect"
	"testing"

	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
)

func TestRefreshRunLifecycleOperationContracts(t *testing.T) {
	contracts := refreshgen.GetAPIGenOperationContracts()
	commands := map[string]string{
		"createRefreshRun": refreshQueuedAuditAction,
		"cancelRefreshRun": refreshCancelledAuditAction,
	}
	for operationID, auditAction := range commands {
		contract, ok := contracts[operationID]
		if !ok || contract.Command == nil {
			t.Fatalf("command contract %q = %#v", operationID, contract)
		}
		if refreshGeneratedOperationKind(contract) != "command" ||
			contract.Command.Owner != "LeapViewAPI.Refresh" ||
			contract.Command.Audit.SuccessAction != auditAction ||
			!contract.Command.Audit.Required ||
			contract.Command.Audit.Guarantee != "best-effort" ||
			(contract.Command.Target == nil || contract.Command.Target.Parameter != "project") ||
			contract.Command.Idempotency != "required" ||
			contract.Command.Privilege != "" {
			t.Errorf("command contract %q = %#v", operationID, contract)
		}
	}
	create := contracts["createRefreshRun"].Command
	if create.UI == nil || create.UI.ActionID != "refresh.run" || len(create.AdditionalExposures) != 1 || string(create.AdditionalExposures[0]) != "ui" {
		t.Errorf("create refresh UI contract = %#v", create)
	}
	cancel := contracts["cancelRefreshRun"].Command
	if cancel.UI == nil || cancel.UI.ActionID != "refresh.cancel" || len(cancel.AdditionalExposures) != 1 || string(cancel.AdditionalExposures[0]) != "ui" {
		t.Errorf("cancel refresh UI contract = %#v", cancel)
	}
	if create.Execution == nil || create.Execution.Guarantee != "transactional" ||
		create.Execution.JobKind != "refresh_pipeline" || create.Execution.ResourceKind != "refresh" ||
		create.Execution.InitialEvent != refreshQueuedAuditAction || create.Execution.InitialState != "queued" ||
		create.Execution.StatusOperation != "getRefreshRun" || create.Execution.EventsOperation != "listRefreshRunEvents" ||
		create.Execution.Cancellation != "supported" {
		t.Errorf("create refresh execution contract = %#v", create.Execution)
	}
	for _, operationID := range []string{"listRefreshRuns", "getRefreshRun", "listRefreshRunEvents"} {
		contract, ok := contracts[operationID]
		if !ok || contract.Command != nil || refreshGeneratedOperationKind(contract) != "query" {
			t.Errorf("query contract %q = %#v", operationID, contract)
		}
	}
}

func refreshGeneratedOperationKind(contract refreshgen.GenOperationContract) string {
	field := reflect.ValueOf(contract).FieldByName("Kind")
	if !field.IsValid() {
		return ""
	}
	return field.String()
}
