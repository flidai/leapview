package connectionbinding

import "context"

type noOpAdministrationAudit struct{}

func (noOpAdministrationAudit) RecordConnectionAdministration(
	context.Context,
	AdministrationAuditEvent,
) error {
	return nil
}

type noOpRotationAudit struct{}

func (noOpRotationAudit) RecordCredentialRotation(
	context.Context,
	RotationAuditEvent,
) error {
	return nil
}
