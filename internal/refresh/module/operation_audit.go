package module

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
)

const (
	refreshQueuedAuditAction    = "refresh.queued"
	refreshCancelledAuditAction = "refresh.cancelled"
)

func buildRefreshAuditIntent(_ context.Context, operationID, principalID, projectID, requestID, correlationID string) (*access.AuditIntent, error) {
	contract, ok := refreshgen.GetAPIGenOperationContract(operationID)
	if !ok || contract.Command == nil || !contract.Command.Audit.Required || contract.Command.Audit.SuccessAction == "" {
		return nil, fmt.Errorf("generated refresh command contract %q is unavailable", operationID)
	}
	targetType := "project"
	if contract.Command.Target != nil {
		targetType = strings.TrimSpace(contract.Command.Target.Type)
	}
	var metadata string
	var err error
	switch operationID {
	case string(refreshgen.GenOperationCreateRefreshRun):
		metadata, err = refreshgen.EncodeGenCreateRefreshRunAuditPayload(refreshgen.GenSchemaRefreshQueuedAuditPayload{Id: "", PipelineId: projectID, SemanticModel: "", InvocationSource: "manual", Status: "queued"})
	case string(refreshgen.GenOperationCancelRefreshRun):
		metadata, err = refreshgen.EncodeGenCancelRefreshRunAuditPayload(refreshgen.GenSchemaRefreshCancelledAuditPayload{Id: "", PipelineId: projectID, Status: "cancelled", InvocationSource: "manual"})
	default:
		return nil, fmt.Errorf("refresh command %q has no audit payload", operationID)
	}
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256([]byte(operationID + "\x00" + principalID + "\x00" + targetType + "\x00" + projectID + "\x00" + requestID))
	outcome := "success"
	if operationID == string(refreshgen.GenOperationCreateRefreshRun) {
		outcome = "accepted"
	}
	return &access.AuditIntent{
		EventID: "sha256:" + hex.EncodeToString(hash[:]), Source: contract.Command.Owner, Operation: operationID,
		PrincipalID: strings.TrimSpace(principalID), Action: contract.Command.Audit.SuccessAction, ResourceKind: targetType, ResourceID: strings.TrimSpace(projectID),
		Capability: access.CapabilityResourceUse, Outcome: outcome, RequestID: strings.TrimSpace(requestID), CorrelationID: strings.TrimSpace(correlationID),
		AggregateKey: targetType + ":" + strings.TrimSpace(projectID), MetadataJSON: metadata,
	}, nil
}
