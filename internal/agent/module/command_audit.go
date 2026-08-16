package module

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	agenthttp "github.com/flidai/leapview/internal/agent/http"
)

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
	privilege, ok := access.ParsePrivilege(command.Privilege)
	if !ok || command.AuthzMode != "privilege" || contract.AuthzMode != command.AuthzMode {
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
	projectID := strings.TrimSpace(input.Scope.ProjectID)
	metadata, err := encodeAgentCommandAuditPayload(operationID, agentgen.GenSchemaAgentCommandAuditPayload{
		OperationId: operationID,
		WorkspaceId: projectID,
		TargetType:  targetType,
		TargetId:    strings.TrimSpace(input.TargetID),
		Surface:     surface,
	})
	if err != nil {
		return err
	}
	return m.recordAudit(ctx, access.AuditEventInput{
		WorkspaceID:   projectID,
		PrincipalID:   strings.TrimSpace(input.Scope.PrincipalID),
		Action:        command.Audit.SuccessAction,
		TargetType:    targetType,
		TargetID:      strings.TrimSpace(input.TargetID),
		Privilege:     privilege,
		Status:        "success",
		RequestID:     strings.TrimSpace(input.RequestID),
		CorrelationID: strings.TrimSpace(input.CorrelationID),
		MetadataJSON:  metadata,
	})
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
