package app

import (
	"context"
	"fmt"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
)

func candidateSourceBlobAuditRecorder(
	accessModule *accessmodule.Module,
	workspaceID string,
) func(context.Context, deploymentmodule.CandidateSourceBlobAuditEvent) error {
	return func(ctx context.Context, event deploymentmodule.CandidateSourceBlobAuditEvent) error {
		if accessModule == nil {
			return fmt.Errorf("candidate source blob access audit module is unavailable")
		}
		privilege, ok := accessmodule.ParsePrivilege(event.Privilege)
		if !ok {
			return fmt.Errorf("candidate source blob audit privilege %q is invalid", event.Privilege)
		}
		return accessModule.RecordAudit(ctx, accessmodule.AuditEventInput{
			WorkspaceID: workspaceID, PrincipalID: event.PrincipalID,
			Action: event.Action, TargetType: "project", TargetID: event.ProjectID,
			Privilege: privilege, Status: event.Status,
			RequestID: event.RequestID, CorrelationID: event.CorrelationID,
			MetadataJSON: event.MetadataJSON,
		})
	}
}
