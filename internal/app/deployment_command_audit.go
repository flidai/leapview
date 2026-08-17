package app

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
)

func candidateSourceBlobAuditRecorder(
	accessModule *accessmodule.Module,
) func(context.Context, deploymentmodule.CandidateSourceBlobAuditEvent) error {
	return func(ctx context.Context, event deploymentmodule.CandidateSourceBlobAuditEvent) error {
		if accessModule == nil {
			return fmt.Errorf("candidate source blob access audit module is unavailable")
		}
		return recordAccessAudit(ctx, accessModule, access.AuditEventInput{
			PrincipalID: event.PrincipalID,
			Action:      event.Action, ResourceKind: "project", ResourceID: event.ProjectID.String(),
			Capability: event.Capability, Status: event.Status,
			RequestID: event.RequestID, CorrelationID: event.CorrelationID,
			MetadataJSON: event.MetadataJSON,
		})
	}
}
