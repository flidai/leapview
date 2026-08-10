package module

import (
	"context"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
)

type moduleAdministrationAuditNoop struct{}

func (moduleAdministrationAuditNoop) RecordConnectionAdministration(
	context.Context,
	connectionbinding.AdministrationAuditEvent,
) error {
	return nil
}

type moduleRotationAuditNoop struct{}

func (moduleRotationAuditNoop) RecordCredentialRotation(
	context.Context,
	connectionbinding.RotationAuditEvent,
) error {
	return nil
}
