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
		"createDeployment":                      {deploymentQueuedAuditAction, "RESOURCE_PUBLISH", "required", "best-effort"},
		"cancelDeployment":                      {deploymentCancelledAuditAction, "RESOURCE_PUBLISH", "required", "best-effort"},
		"retryDeployment":                       {deploymentQueuedAuditAction, "RESOURCE_PUBLISH", "required", "best-effort"},
		"rollbackDeployment":                    {deploymentQueuedAuditAction, "PROJECT_ADMIN", "required", "best-effort"},
		"requestDeploymentApproval":             {deploymentApprovalRequestedAuditAction, "RESOURCE_PUBLISH", "required", "best-effort"},
		"approveDeployment":                     {deploymentApprovedAuditAction, "PROJECT_ADMIN", "required", "best-effort"},
		"denyDeploymentApproval":                {deploymentDeniedAuditAction, "PROJECT_ADMIN", "required", "best-effort"},
		"revokeDeploymentApproval":              {deploymentApprovalRevokedAuditAction, "PROJECT_ADMIN", "required", "best-effort"},
		"activateDeployment":                    {deploymentActivationRequestedAuditAction, "PROJECT_ADMIN", "required", "best-effort"},
		"startProjectCandidate":                 {"candidate.started", "RESOURCE_EDIT", "required", "best-effort"},
		"replaceProjectCandidateArtifact":       {"candidate.artifact_replaced", "RESOURCE_EDIT", "required", "best-effort"},
		"retryProjectCandidate":                 {"candidate.retried", "RESOURCE_EDIT", "required", "best-effort"},
		"cancelProjectCandidate":                {"candidate.cancelled", "RESOURCE_EDIT", "required", "best-effort"},
		"cancelProjectCandidateByKey":           {"candidate.cancelled", "RESOURCE_EDIT", "required", "best-effort"},
		"publishProjectCandidate":               {deploymentQueuedAuditAction, "RESOURCE_PUBLISH", "required", "best-effort"},
		"retainProjectCandidateSource":          {"candidate.source_retained", "RESOURCE_EDIT", "required", "best-effort"},
		"uploadProjectCandidateSourceBlob":      {"candidate.source_blob_uploaded", "RESOURCE_EDIT", "", "best-effort"},
		"commitProjectCandidateSynchronization": {"candidate.ready", "RESOURCE_EDIT", "required", "best-effort"},
		"createDeliveryPlan":                    {"delivery.plan.created", "RESOURCE_READ", "required", "transactional"},
		"buildDeliveryPlan":                     {"delivery.build.sealed", "RESOURCE_USE", "required", "transactional"},
		"publishDeliveryCandidate":              {"delivery.publication.requested", "RESOURCE_PUBLISH", "required", "transactional"},
		"rollbackDeliveryGeneration":            {"delivery.rollback.requested", "PROJECT_ADMIN", "required", "transactional"},
		"requestDeliveryPublicationApproval":    {"delivery.publication.approval_requested", "RESOURCE_PUBLISH", "required", "transactional"},
		"approveDeliveryPublicationApproval":    {"delivery.publication.approved", "PROJECT_ADMIN", "required", "transactional"},
		"denyDeliveryPublicationApproval":       {"delivery.publication.denied", "PROJECT_ADMIN", "required", "transactional"},
		"revokeDeliveryPublicationApproval":     {"delivery.publication.approval_revoked", "PROJECT_ADMIN", "required", "transactional"},
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
	queryPrivileges := map[string]string{
		"listDeployments":                     "RESOURCE_READ",
		"getDeployment":                       "RESOURCE_READ",
		"listDeploymentEvents":                "RESOURCE_READ",
		"getProjectCandidate":                 "RESOURCE_EDIT",
		"reviewProjectCandidate":              "RESOURCE_EDIT",
		"planProjectCandidateSynchronization": "RESOURCE_EDIT",
		"getDeliveryPlanPreview":              "RESOURCE_READ",
		"getDeliveryBuildStatus":              "RESOURCE_READ",
		"getDeliverySealStatus":               "RESOURCE_READ",
		"getDeliveryCandidateStatus":          "RESOURCE_READ",
		"getDeliveryGenerationStatus":         "RESOURCE_READ",
		"getDeliveryPublicationEvidence":      "RESOURCE_READ",
		"getDeliveryPublicationApproval":      "RESOURCE_READ",
		"getDeliveryOperatorSnapshot":         "PROJECT_ADMIN",
	}
	for operationID, wantPrivilege := range queryPrivileges {
		contract, ok := contracts[operationID]
		if !ok || contract.Command != nil || generatedOperationKind(contract) != "query" {
			t.Errorf("query contract %q = %#v", operationID, contract)
			continue
		}
		authz, _ := contract.Extensions["x-authz"].(map[string]any)
		privilege, _ := authz["privilege"].(string)
		if contract.AuthzMode != "privilege" || privilege != wantPrivilege {
			t.Errorf("query contract %q authorization = %s/%s, want privilege/%s", operationID, contract.AuthzMode, privilege, wantPrivilege)
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

func TestCandidateSourceRetentionUsesSourceOwnedAuditPayload(t *testing.T) {
	contract, ok := deploymentgen.GetAPIGenOperationContract("retainProjectCandidateSource")
	if !ok || contract.Command == nil || contract.Command.Audit.Payload == nil {
		t.Fatalf("candidate source retention command contract = %#v", contract)
	}
	if contract.Command.Audit.Payload.Schema != "CandidateSourceRetainedAuditPayload" {
		t.Fatalf("candidate source retention payload schema = %q", contract.Command.Audit.Payload.Schema)
	}
	fields := make(map[string]bool, len(contract.Command.Audit.Payload.Fields))
	for _, field := range contract.Command.Audit.Payload.Fields {
		fields[field.Name] = true
	}
	for _, field := range []string{"operationId", "surface", "projectId", "sourceDigest", "sourceAttestationDigest"} {
		if !fields[field] {
			t.Errorf("candidate source retention payload missing field %q", field)
		}
	}
	if fields["candidateId"] || fields["targetId"] {
		t.Errorf("candidate source retention payload contains candidate-owned fields: %#v", fields)
	}
}

func generatedOperationKind(contract deploymentgen.GenOperationContract) string {
	field := reflect.ValueOf(contract).FieldByName("Kind")
	if !field.IsValid() {
		return ""
	}
	return field.String()
}
