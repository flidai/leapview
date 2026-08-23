package module

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
)

const (
	deploymentQueuedAuditAction              = "deployment.queued"
	deploymentCancelledAuditAction           = "deployment.cancelled"
	deploymentApprovalRequestedAuditAction   = "deployment.approval_requested"
	deploymentApprovedAuditAction            = "deployment.approved"
	deploymentDeniedAuditAction              = "deployment.denied"
	deploymentApprovalRevokedAuditAction     = "deployment.approval_revoked"
	deploymentActivationRequestedAuditAction = "deployment.activation_requested"
)

type deploymentAuditCommandInput struct {
	OperationID    string
	ProjectID      string
	DeploymentID   string
	ReleaseID      string
	ApprovalID     string
	ApprovalRev    int64
	IdempotencyKey string
	PrincipalID    string
	RequestID      string
	CorrelationID  string
	Status         string
	Outcome        string
}

// buildDeploymentAuditIntent translates a generated command contract into a
// durable Access outbox intent. The deployment and approval repositories add
// their own aggregate revision while still inside the source transaction.
func buildDeploymentAuditIntent(input deploymentAuditCommandInput) (access.AuditIntent, error) {
	contract, ok := deploymentgen.GetAPIGenOperationContract(input.OperationID)
	if !ok || contract.Command == nil {
		return access.AuditIntent{}, fmt.Errorf("deployment operation %q has no command audit contract", input.OperationID)
	}
	if contract.Command.Audit.Guarantee != "transactional" {
		return access.AuditIntent{}, fmt.Errorf("deployment operation %q does not provide transactional auditing", input.OperationID)
	}
	var metadata string
	var err error
	switch input.OperationID {
	case string(deploymentgen.GenOperationCreateDeployment):
		metadata, err = deploymentgen.EncodeGenCreateDeploymentAuditPayload(deploymentgen.GenSchemaDeploymentQueuedAuditPayload{DeploymentId: input.DeploymentID, ProjectId: input.ProjectID, ReleaseId: input.ReleaseID, Status: input.Status})
	case string(deploymentgen.GenOperationRetryDeployment):
		metadata, err = deploymentgen.EncodeGenRetryDeploymentAuditPayload(deploymentgen.GenSchemaDeploymentQueuedAuditPayload{DeploymentId: input.DeploymentID, ProjectId: input.ProjectID, ReleaseId: input.ReleaseID, Status: input.Status})
	case string(deploymentgen.GenOperationRollbackDeployment):
		metadata, err = deploymentgen.EncodeGenRollbackDeploymentAuditPayload(deploymentgen.GenSchemaDeploymentQueuedAuditPayload{DeploymentId: input.DeploymentID, ProjectId: input.ProjectID, ReleaseId: input.ReleaseID, Status: input.Status})
	case string(deploymentgen.GenOperationRequestDeploymentApproval):
		metadata, err = deploymentgen.EncodeGenRequestDeploymentApprovalAuditPayload(deploymentgen.GenSchemaDeploymentApprovalRequestedAuditPayload{DeploymentId: input.DeploymentID, ApprovalId: input.ApprovalID})
	case string(deploymentgen.GenOperationApproveDeployment):
		metadata, err = deploymentgen.EncodeGenApproveDeploymentAuditPayload(deploymentgen.GenSchemaDeploymentApprovalDecisionAuditPayload{DeploymentId: input.DeploymentID, ApprovalId: input.ApprovalID, ApprovalRevision: input.ApprovalRev})
	case string(deploymentgen.GenOperationDenyDeploymentApproval):
		metadata, err = deploymentgen.EncodeGenDenyDeploymentApprovalAuditPayload(deploymentgen.GenSchemaDeploymentApprovalDecisionAuditPayload{DeploymentId: input.DeploymentID, ApprovalId: input.ApprovalID, ApprovalRevision: input.ApprovalRev})
	case string(deploymentgen.GenOperationRevokeDeploymentApproval):
		metadata, err = deploymentgen.EncodeGenRevokeDeploymentApprovalAuditPayload(deploymentgen.GenSchemaDeploymentApprovalDecisionAuditPayload{DeploymentId: input.DeploymentID, ApprovalId: input.ApprovalID, ApprovalRevision: input.ApprovalRev})
	default:
		return access.AuditIntent{}, fmt.Errorf("deployment operation %q has no transactional audit payload", input.OperationID)
	}
	if err != nil {
		return access.AuditIntent{}, err
	}
	aggregateKey := "deployment:" + strings.TrimSpace(input.DeploymentID)
	if strings.HasPrefix(input.OperationID, "requestDeploymentApproval") || strings.HasPrefix(input.OperationID, "approveDeployment") || strings.HasPrefix(input.OperationID, "denyDeploymentApproval") || strings.HasPrefix(input.OperationID, "revokeDeploymentApproval") {
		aggregateKey += ":approval"
	}
	key := strings.TrimSpace(input.IdempotencyKey)
	if key == "" {
		return access.AuditIntent{}, fmt.Errorf("deployment audit intent requires idempotency key")
	}
	sum := sha256.Sum256([]byte("deployment\x00" + input.OperationID + "\x00" + aggregateKey + "\x00" + key))
	capability := access.CapabilityResourcePublish
	if input.OperationID == string(deploymentgen.GenOperationRollbackDeployment) || input.OperationID == string(deploymentgen.GenOperationApproveDeployment) || input.OperationID == string(deploymentgen.GenOperationDenyDeploymentApproval) || input.OperationID == string(deploymentgen.GenOperationRevokeDeploymentApproval) {
		capability = access.CapabilityProjectAdmin
	}
	outcome := strings.TrimSpace(input.Outcome)
	if outcome == "" {
		outcome = "success"
	}
	sequence := int64(1)
	if input.OperationID == string(deploymentgen.GenOperationActivateDeployment) || input.OperationID == string(deploymentgen.GenOperationCancelDeployment) {
		sequence = 2
	}
	return access.AuditIntent{EventID: "deployment:" + hex.EncodeToString(sum[:16]), Source: "deployment", Operation: input.OperationID, PrincipalID: strings.TrimSpace(input.PrincipalID), Action: contract.Command.Audit.SuccessAction, ResourceKind: "project", ResourceID: strings.TrimSpace(input.ProjectID), Capability: capability, Outcome: outcome, RequestID: strings.TrimSpace(input.RequestID), CorrelationID: strings.TrimSpace(input.CorrelationID), AggregateKey: aggregateKey, AggregateSequence: sequence, MetadataJSON: metadata}.Canonicalize()
}
