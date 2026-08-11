package module

import (
	"fmt"

	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
)

func requireConnectionBindingAuditSinks(
	rotation connectionbinding.RotationAuditRecorder,
	administration connectionbinding.AdministrationAuditRecorder,
) error {
	rotationRequired, administrationRequired := connectionBindingAuditRequirements()
	if administrationRequired && administration == nil {
		return fmt.Errorf(
			"%w: generated connection-binding commands require an administration recorder",
			connectionbinding.ErrAdministrationAuditUnavailable,
		)
	}
	if rotationRequired && rotation == nil {
		return fmt.Errorf(
			"%w: generated connection-binding commands require a rotation recorder",
			connectionbinding.ErrRotationAuditUnavailable,
		)
	}
	return nil
}

func connectionBindingAuditRequirements() (rotation, administration bool) {
	for _, contract := range analyticsgen.GetAPIGenOperationContracts() {
		if contract.Command == nil || !contract.Command.Audit.Required {
			continue
		}
		action := contract.Command.Audit.SuccessAction
		switch connectionbinding.AdministrationAuditAction(action) {
		case connectionbinding.AuditBindingCreated,
			connectionbinding.AuditBindingUpdated,
			connectionbinding.AuditBindingEnabled,
			connectionbinding.AuditBindingDisabled:
			administration = true
		}
		switch connectionbinding.RefreshOperation(action) {
		case connectionbinding.RefreshRequested, connectionbinding.RefreshTest:
			rotation = true
		}
	}
	return rotation, administration
}

func requireConnectionRotationAuditSink(
	rotation connectionbinding.RotationAuditRecorder,
) error {
	rotationRequired, _ := connectionBindingAuditRequirements()
	if rotationRequired && rotation == nil {
		return fmt.Errorf(
			"%w: generated connection-binding commands require a rotation recorder",
			connectionbinding.ErrRotationAuditUnavailable,
		)
	}
	return nil
}
