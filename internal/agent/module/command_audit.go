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
	contract, ok := agentgen.GetAPIGenOperationContract(strings.TrimSpace(input.OperationID))
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
	return m.recordAudit(ctx, access.AuditEventInput{
		WorkspaceID:   strings.TrimSpace(input.Scope.WorkspaceID),
		PrincipalID:   strings.TrimSpace(input.Scope.PrincipalID),
		Action:        command.Audit.SuccessAction,
		TargetType:    targetType,
		TargetID:      strings.TrimSpace(input.TargetID),
		Privilege:     privilege,
		Status:        "success",
		RequestID:     strings.TrimSpace(input.RequestID),
		CorrelationID: strings.TrimSpace(input.CorrelationID),
	})
}
