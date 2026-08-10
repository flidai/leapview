package module

import (
	"reflect"
	"testing"

	releasegen "github.com/flidai/leapview/internal/release/api/gen"
)

func TestReleaseLifecycleOperationContracts(t *testing.T) {
	contracts := releasegen.GetAPIGenOperationContracts()
	commands := map[string]struct {
		auditAction string
		idempotency string
		guarantee   string
	}{
		"createRelease":         {auditAction: releaseCreatedAuditAction, idempotency: "required", guarantee: "best-effort"},
		"uploadReleaseArtifact": {auditAction: releaseArtifactUploadedAuditAction, guarantee: "best-effort"},
		"finalizeRelease":       {auditAction: releaseValidatingAuditAction, idempotency: "required", guarantee: "transactional"},
	}
	for operationID, expected := range commands {
		contract, ok := contracts[operationID]
		if !ok || contract.Command == nil {
			t.Fatalf("command contract %q = %#v", operationID, contract)
		}
		if releaseGeneratedOperationKind(contract) != "command" ||
			contract.Command.Owner != "LeapViewAPI.Release" ||
			contract.Command.Audit.SuccessAction != expected.auditAction ||
			!contract.Command.Audit.Required ||
			contract.Command.Audit.Guarantee != expected.guarantee ||
			contract.Command.Target == nil ||
			contract.Command.Target.Parameter != "project" ||
			contract.Command.Target.Type != "project" ||
			contract.Command.Privilege != "PUBLISH_RELEASE" ||
			contract.Command.Idempotency != expected.idempotency ||
			len(contract.Command.AdditionalExposures) != 0 {
			t.Errorf("command contract %q = %#v", operationID, contract)
		}
	}
	for _, operationID := range []string{"listReleases", "getRelease", "listReleaseEvents"} {
		contract, ok := contracts[operationID]
		if !ok || contract.Command != nil || releaseGeneratedOperationKind(contract) != "query" {
			t.Errorf("query contract %q = %#v", operationID, contract)
		}
	}
	finalize := contracts["finalizeRelease"].Command.Execution
	if finalize == nil ||
		finalize.Mode != "async" ||
		finalize.JobKind != "release.finalize" ||
		finalize.ResourceKind != "release" ||
		finalize.InitialEvent != releaseValidatingAuditAction ||
		finalize.InitialState != "validating" ||
		finalize.StatusOperation != "getRelease" ||
		finalize.EventsOperation != "listReleaseEvents" ||
		finalize.Cancellation != "unsupported" {
		t.Fatalf("finalize release execution contract = %#v", finalize)
	}
}

func releaseGeneratedOperationKind(contract releasegen.GenOperationContract) string {
	field := reflect.ValueOf(contract).FieldByName("Kind")
	if !field.IsValid() {
		return ""
	}
	return field.String()
}
