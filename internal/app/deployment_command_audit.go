package app

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
)

func candidateSourceAuditRecorder(
	accessModule *accessmodule.Module,
) func(context.Context, deploymentmodule.CandidateSourceAuditEvent) error {
	return func(ctx context.Context, event deploymentmodule.CandidateSourceAuditEvent) error {
		if accessModule == nil {
			return fmt.Errorf("candidate source access audit module is unavailable")
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
