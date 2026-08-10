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
			contract.Command.Target == nil ||
			contract.Command.Target.Parameter != "workspace" ||
			contract.Command.Target.Type != "workspace" ||
			contract.Command.Privilege != "USE_WORKSPACE" ||
			contract.Command.Idempotency != "required" ||
			len(contract.Command.AdditionalExposures) != 0 {
			t.Errorf("command contract %q = %#v", operationID, contract)
		}
	}
	create := contracts["createRefreshRun"].Command
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
