package module

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	agenthttp "github.com/flidai/leapview/internal/agent/http"
)

// BuildAuditIntent resolves the generated command contract into a durable
// source intent. The repository fills IDs that are not known until insertion.
func BuildAuditIntent(ctx context.Context, input agenthttp.CommandAuditInput) (*access.AuditIntent, error) {
	operationID := strings.TrimSpace(input.OperationID)
	contract, ok := agentgen.GetAPIGenOperationContract(operationID)
	if !ok || contract.Command == nil {
		return nil, fmt.Errorf("generated agent command contract %q is unavailable", operationID)
	}
	command := contract.Command
	if !command.Audit.Required || strings.TrimSpace(command.Audit.SuccessAction) == "" || (command.Audit.Guarantee != "best-effort" && command.Audit.Guarantee != "transactional") {
		return nil, fmt.Errorf("generated agent command contract %q does not define a durable audit", operationID)
	}
	capability, ok := agentCommandCapability(command.Privilege)
	if command.AuthzMode == "authenticated" && strings.TrimSpace(command.Privilege) == "" {
		capability, ok = access.CapabilityResourceUse, true
	}
	if !ok || (command.AuthzMode != "privilege" && command.AuthzMode != "authenticated") || contract.AuthzMode != command.AuthzMode {
		return nil, fmt.Errorf("generated agent command contract %q has invalid authorization", operationID)
	}
	targetType := strings.TrimSpace(input.TargetType)
	if command.Target != nil {
		targetType = strings.TrimSpace(command.Target.Type)
	}
	surface := strings.TrimSpace(input.Surface)
	if surface == "" {
		surface = "api"
	}
	metadata, err := encodeAgentCommandAuditPayload(operationID, agentgen.GenSchemaAgentCommandAuditPayload{
		OperationId: operationID, ResourceKind: targetType, ResourceId: strings.TrimSpace(input.TargetID), Surface: surface,
	})
	if err != nil {
		return nil, err
	}
	resourceID := strings.TrimSpace(input.TargetID)
	hash := sha256.Sum256([]byte(operationID + "\x00" + input.Scope.PrincipalID + "\x00" + targetType + "\x00" + resourceID + "\x00" + input.RequestID))
	return &access.AuditIntent{
		EventID: "sha256:" + hex.EncodeToString(hash[:]), Source: command.Owner, Operation: operationID,
		PrincipalID: strings.TrimSpace(input.Scope.PrincipalID), Action: command.Audit.SuccessAction, ResourceKind: targetType, ResourceID: resourceID,
		Capability: capability, Outcome: agentAuditOutcome(operationID), RequestID: strings.TrimSpace(input.RequestID), CorrelationID: strings.TrimSpace(input.CorrelationID),
		AggregateKey: targetType + ":" + resourceID, MetadataJSON: metadata,
	}, nil
}

func agentAuditOutcome(operationID string) string {
	switch operationID {
	case string(agentgen.GenOperationCreateAgentRun):
		return "accepted"
	default:
		return "success"
	}
}

func (m *Module) recordCommandAudit(ctx context.Context, input agenthttp.CommandAuditInput) error {
	operationID := strings.TrimSpace(input.OperationID)
	contract, ok := agentgen.GetAPIGenOperationContract(operationID)
	if !ok || contract.Command == nil {
		return fmt.Errorf("generated agent command contract %q is unavailable", input.OperationID)
	}
	command := contract.Command
	if !command.Audit.Required || strings.TrimSpace(command.Audit.SuccessAction) == "" || command.Audit.Guarantee != "best-effort" {
		return fmt.Errorf("generated agent command contract %q does not define a best-effort success audit", input.OperationID)
	}
	capability, ok := agentCommandCapability(command.Privilege)
	if command.AuthzMode == "authenticated" && strings.TrimSpace(command.Privilege) == "" {
		capability, ok = access.CapabilityResourceUse, true
	}
	if !ok || (command.AuthzMode != "privilege" && command.AuthzMode != "authenticated") || contract.AuthzMode != command.AuthzMode {
		return fmt.Errorf("generated agent command contract %q has invalid authorization", input.OperationID)
	}
	if m == nil || m.recordAudit == nil {
		return fmt.Errorf("agent audit recorder is unavailable")
	}
	targetType := strings.TrimSpace(input.TargetType)
	if command.Target != nil {
		targetType = strings.TrimSpace(command.Target.Type)
	}
	surface := strings.TrimSpace(input.Surface)
	if surface == "" {
		surface = "api"
	}
	metadata, err := encodeAgentCommandAuditPayload(operationID, agentgen.GenSchemaAgentCommandAuditPayload{
		OperationId:  operationID,
		ResourceKind: targetType,
		ResourceId:   strings.TrimSpace(input.TargetID),
		Surface:      surface,
	})
	if err != nil {
		return err
	}
	return m.recordAudit(ctx, access.AuditEventInput{
		PrincipalID:   strings.TrimSpace(input.Scope.PrincipalID),
		Action:        command.Audit.SuccessAction,
		ResourceKind:  targetType,
		ResourceID:    strings.TrimSpace(input.TargetID),
		Capability:    capability,
		Status:        "success",
		RequestID:     strings.TrimSpace(input.RequestID),
		CorrelationID: strings.TrimSpace(input.CorrelationID),
		MetadataJSON:  metadata,
	})
}

func agentCommandCapability(value string) (access.Capability, bool) {
	capability, err := access.ParseCapability(strings.TrimSpace(value))
	return capability, err == nil
}

func encodeAgentCommandAuditPayload(operationID string, payload agentgen.GenSchemaAgentCommandAuditPayload) (string, error) {
	switch operationID {
	case string(agentgen.GenOperationUpdateAgentConfig):
		return agentgen.EncodeGenUpdateAgentConfigAuditPayload(payload)
	case string(agentgen.GenOperationCreateAgentConversation):
		return agentgen.EncodeGenCreateAgentConversationAuditPayload(payload)
	case string(agentgen.GenOperationArchiveAgentConversation):
		return agentgen.EncodeGenArchiveAgentConversationAuditPayload(payload)
	case string(agentgen.GenOperationUpdateAgentConversation):
		return agentgen.EncodeGenUpdateAgentConversationAuditPayload(payload)
	case string(agentgen.GenOperationCreateAgentRun):
		return agentgen.EncodeGenCreateAgentRunAuditPayload(payload)
	case string(agentgen.GenOperationCancelAgentRun):
		return agentgen.EncodeGenCancelAgentRunAuditPayload(payload)
	default:
		return "", fmt.Errorf("generated agent command audit payload %q is unavailable", operationID)
	}
}
