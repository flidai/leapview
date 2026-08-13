package app

import (
	"context"
	"fmt"

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
		privilege, ok := accessmodule.ParsePrivilege(event.Privilege)
		if !ok {
			return fmt.Errorf("managed-data audit privilege %q is invalid", event.Privilege)
		}
		return accessModule.RecordAudit(ctx, accessmodule.AuditEventInput{
			PrincipalID: event.PrincipalID,
			Action:      event.Action, TargetType: event.TargetType, TargetID: event.TargetID,
			Privilege: privilege, Status: event.Status,
			RequestID: event.RequestID, CorrelationID: event.CorrelationID,
			MetadataJSON: event.MetadataJSON,
		})
	}
}
