package module

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
)

func TestDeploymentLifecycleOperationContracts(t *testing.T) {
	contracts := deploymentgen.GetAPIGenOperationContracts()
	commands := map[string]struct {
		auditAction string
		privilege   string
		idempotency string
		guarantee   string
	}{
		"createDeployment":                      {deploymentQueuedAuditAction, "PROJECT_ADMIN", "required", "best-effort"},
		"cancelDeployment":                      {deploymentCancelledAuditAction, "PROJECT_ADMIN", "required", "best-effort"},
		"retryDeployment":                       {deploymentQueuedAuditAction, "PROJECT_ADMIN", "required", "best-effort"},
		"rollbackDeployment":                    {deploymentQueuedAuditAction, "PROJECT_ADMIN", "required", "best-effort"},
		"requestDeploymentApproval":             {deploymentApprovalRequestedAuditAction, "PROJECT_ADMIN", "required", "best-effort"},
		"approveDeployment":                     {deploymentApprovedAuditAction, "PROJECT_ADMIN", "required", "best-effort"},
		"denyDeploymentApproval":                {deploymentDeniedAuditAction, "PROJECT_ADMIN", "required", "best-effort"},
		"revokeDeploymentApproval":              {deploymentApprovalRevokedAuditAction, "PROJECT_ADMIN", "required", "best-effort"},
		"activateDeployment":                    {deploymentActivationRequestedAuditAction, "PROJECT_ADMIN", "required", "best-effort"},
		"startProjectCandidate":                 {"candidate.started", "PROJECT_ADMIN", "required", "best-effort"},
		"replaceProjectCandidateArtifact":       {"candidate.artifact_replaced", "PROJECT_ADMIN", "required", "best-effort"},
		"retryProjectCandidate":                 {"candidate.retried", "PROJECT_ADMIN", "required", "best-effort"},
		"cancelProjectCandidate":                {"candidate.cancelled", "PROJECT_ADMIN", "required", "best-effort"},
		"cancelProjectCandidateByKey":           {"candidate.cancelled", "PROJECT_ADMIN", "required", "best-effort"},
		"publishProjectCandidate":               {deploymentQueuedAuditAction, "PROJECT_ADMIN", "required", "best-effort"},
		"uploadProjectCandidateSourceBlob":      {"candidate.source_blob_uploaded", "PROJECT_ADMIN", "", "best-effort"},
		"commitProjectCandidateSynchronization": {"candidate.ready", "PROJECT_ADMIN", "required", "best-effort"},
	}
	for operationID, expected := range commands {
		contract, ok := contracts[operationID]
		if !ok || contract.Command == nil {
			t.Fatalf("command contract %q = %#v", operationID, contract)
		}
		if generatedOperationKind(contract) != "command" ||
			contract.Command.Owner != "LeapViewAPI.Deployment" ||
			contract.Command.Audit.SuccessAction != expected.auditAction ||
			!contract.Command.Audit.Required ||
			contract.Command.Audit.Guarantee != expected.guarantee ||
			contract.Command.Target == nil ||
			contract.Command.Target.Parameter != "project" ||
			contract.Command.Target.Type != "project" ||
			contract.Command.Privilege != expected.privilege ||
			contract.Command.Idempotency != expected.idempotency ||
			len(contract.Command.AdditionalExposures) != 0 {
			t.Errorf("command contract %q = %#v", operationID, contract)
		}
	}
	for _, operationID := range []string{"createDeployment", "retryDeployment", "rollbackDeployment", "activateDeployment", "publishProjectCandidate"} {
		contract := contracts[operationID]
		if contract.Command.Execution == nil ||
			contract.Command.Execution.Guarantee != "transactional" ||
			contract.Command.Execution.JobKind != "deployment.activate" ||
			contract.Command.Execution.ResourceKind != "deployment" ||
			contract.Command.Execution.InitialState != "queued" ||
			contract.Command.Execution.StatusOperation != "getDeployment" ||
			contract.Command.Execution.EventsOperation != "listDeploymentEvents" ||
			contract.Command.Execution.Cancellation != "supported" {
			t.Errorf("async command contract %q = %#v", operationID, contract.Command.Execution)
		}
	}
	for _, operationID := range []string{
		"listDeployments",
		"getDeployment",
		"listDeploymentEvents",
		"getProjectCandidate",
		"reviewProjectCandidate",
		"planProjectCandidateSynchronization",
	} {
		contract, ok := contracts[operationID]
		if !ok || contract.Command != nil || generatedOperationKind(contract) != "query" {
			t.Errorf("query contract %q = %#v", operationID, contract)
		}
	}
}

func TestGeneratedCandidateCommandsRequireConfiguredAuditSink(t *testing.T) {
	if err := requireCandidateAuditSink(nil); !errors.Is(err, deployment.ErrCandidateAuditUnavailable) {
		t.Fatalf("requireCandidateAuditSink(nil) error = %v, want ErrCandidateAuditUnavailable", err)
	}
	if err := requireCandidateAuditSink(func(context.Context, deployment.CandidateEvent) error {
		return nil
	}); err != nil {
		t.Fatalf("requireCandidateAuditSink(noop) error = %v", err)
	}
}

func TestCandidateSourceBlobDeclaresBestEffortAuditGuarantee(t *testing.T) {
	contract, ok := deploymentgen.GetAPIGenOperationContract("uploadProjectCandidateSourceBlob")
	if !ok || contract.Command == nil || contract.Command.Audit.Guarantee != "best-effort" {
		t.Fatalf("candidate source blob command contract = %#v", contract)
	}
}

func generatedOperationKind(contract deploymentgen.GenOperationContract) string {
	field := reflect.ValueOf(contract).FieldByName("Kind")
	if !field.IsValid() {
		return ""
	}
	return field.String()
}
