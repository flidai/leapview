package module

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
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
	action, ok := deploymentAuditActions[input.OperationID]
	if !ok {
		return access.AuditIntent{}, fmt.Errorf("deployment operation %q has no command audit contract", input.OperationID)
	}
	metadataBytes, err := json.Marshal(map[string]any{
		"operationId": input.OperationID, "deploymentId": input.DeploymentID,
		"projectId": input.ProjectID, "releaseId": input.ReleaseID,
		"approvalId": input.ApprovalID, "approvalRevision": input.ApprovalRev,
		"status": input.Status,
	})
	if err != nil {
		return access.AuditIntent{}, err
	}
	metadata := string(metadataBytes)
	if err != nil {
		return access.AuditIntent{}, err
	}
	aggregateKey := "deployment:" + strings.TrimSpace(input.DeploymentID)
	if isDeploymentApprovalAuditOperation(input.OperationID) {
		aggregateKey += ":approval"
		if approvalID := strings.TrimSpace(input.ApprovalID); approvalID != "" {
			aggregateKey += ":" + approvalID
		}
	}
	key := strings.TrimSpace(input.IdempotencyKey)
	if key == "" {
		return access.AuditIntent{}, fmt.Errorf("deployment audit intent requires idempotency key")
	}
	sum := sha256.Sum256([]byte("deployment\x00" + input.OperationID + "\x00" + aggregateKey + "\x00" + key))
	capability := access.CapabilityResourcePublish
	if input.OperationID == "rollbackDeployment" || input.OperationID == "approveDeployment" || input.OperationID == "denyDeploymentApproval" || input.OperationID == "revokeDeploymentApproval" {
		capability = access.CapabilityProjectAdmin
	}
	outcome := strings.TrimSpace(input.Outcome)
	if outcome == "" {
		outcome = "success"
	}
	sequence := int64(1)
	if input.OperationID == "activateDeployment" || input.OperationID == "cancelDeployment" {
		// Lifecycle events can race (for example, activation and cancellation
		// of the same queued deployment). Let Access allocate the next sequence
		// in the source transaction so both events cannot collide at sequence 2.
		sequence = 0
	}
	return access.AuditIntent{EventID: "deployment:" + hex.EncodeToString(sum[:16]), Source: "deployment", Operation: input.OperationID, PrincipalID: strings.TrimSpace(input.PrincipalID), Action: action, ResourceKind: "project", ResourceID: strings.TrimSpace(input.ProjectID), Capability: capability, Outcome: outcome, RequestID: strings.TrimSpace(input.RequestID), CorrelationID: strings.TrimSpace(input.CorrelationID), AggregateKey: aggregateKey, AggregateSequence: sequence, MetadataJSON: metadata}.Canonicalize()
}

var deploymentAuditActions = map[string]string{
	"createDeployment":          deploymentQueuedAuditAction,
	"cancelDeployment":          deploymentCancelledAuditAction,
	"retryDeployment":           deploymentQueuedAuditAction,
	"rollbackDeployment":        deploymentQueuedAuditAction,
	"requestDeploymentApproval": deploymentApprovalRequestedAuditAction,
	"approveDeployment":         deploymentApprovedAuditAction,
	"denyDeploymentApproval":    deploymentDeniedAuditAction,
	"revokeDeploymentApproval":  deploymentApprovalRevokedAuditAction,
	"activateDeployment":        deploymentActivationRequestedAuditAction,
}

func isDeploymentApprovalAuditOperation(operationID string) bool {
	switch operationID {
	case "requestDeploymentApproval", "approveDeployment", "denyDeploymentApproval", "revokeDeploymentApproval":
		return true
	default:
		return false
	}
}
