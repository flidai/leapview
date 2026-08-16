package app

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	manageddatamodule "github.com/flidai/leapview/internal/manageddata/module"
)

func managedDataCommandAuditRecorder(
	accessModule *accessmodule.Module,
) func(context.Context, manageddatamodule.CommandAuditEvent) error {
	return func(ctx context.Context, event manageddatamodule.CommandAuditEvent) error {
		if accessModule == nil {
			return fmt.Errorf("managed-data access audit module is unavailable")
		}
		capability, err := access.ParseCapability(event.Privilege)
		if err != nil {
			return fmt.Errorf("managed-data audit privilege %q is invalid", event.Privilege)
		}
		return recordAccessAudit(ctx, accessModule, access.AuditEventInput{
			PrincipalID: event.PrincipalID,
			Action:      event.Action, ResourceKind: event.TargetType, ResourceID: event.TargetID,
			Capability: capability, Status: event.Status,
			RequestID: event.RequestID, CorrelationID: event.CorrelationID,
			MetadataJSON: event.MetadataJSON,
		})
	}
}
