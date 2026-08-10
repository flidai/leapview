package module

import (
	"fmt"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
)

func generatedRoleBindingCatalog() (access.OperationCatalog, error) {
	return generatedWorkspaceCommandCatalog([]access.OperationID{
		access.OperationCreateRoleBinding,
		access.OperationUpdateRoleBinding,
		access.OperationDeleteRoleBinding,
	})
}

func generatedGrantCatalog() (access.OperationCatalog, error) {
	return generatedWorkspaceCommandCatalog([]access.OperationID{
		access.OperationCreateGrant,
		access.OperationUpdateGrant,
		access.OperationDeleteGrant,
	})
}

func generatedWorkspaceCommandCatalog(operationIDs []access.OperationID) (access.OperationCatalog, error) {
	contracts := accessgen.GetAPIGenOperationContracts()
	descriptors := make([]access.OperationDescriptor, 0, len(operationIDs))
	for _, operationID := range operationIDs {
		contract, ok := contracts[string(operationID)]
		if !ok {
			return access.OperationCatalog{}, fmt.Errorf("generated operation %q is missing", operationID)
		}
		command := contract.Command
		if command == nil {
			return access.OperationCatalog{}, fmt.Errorf("generated operation %q is not a command", operationID)
		}
		if !command.Audit.Required || command.Audit.SuccessAction == "" {
			return access.OperationCatalog{}, fmt.Errorf("generated operation %q must require a success audit", operationID)
		}
		if command.Audit.Guarantee != "transactional" {
			return access.OperationCatalog{}, fmt.Errorf("generated operation %q must require transactional auditing", operationID)
		}
		if command.Target == nil || command.Target.Type != string(access.SecurableWorkspace) {
			return access.OperationCatalog{}, fmt.Errorf("generated operation %q must target a workspace", operationID)
		}
		privilege, ok := access.ParsePrivilege(command.Privilege)
		if !ok || command.AuthzMode != "privilege" || contract.AuthzMode != command.AuthzMode {
			return access.OperationCatalog{}, fmt.Errorf("generated operation %q has an invalid authorization contract", operationID)
		}
		surfaces := []access.OperationSurface{access.OperationSurfaceAPI, access.OperationSurfaceCLI}
		for _, exposure := range command.AdditionalExposures {
			switch exposure {
			case accessgen.GenOperationSurfaceUI:
				surfaces = append(surfaces, access.OperationSurfaceUI)
			default:
				return access.OperationCatalog{}, fmt.Errorf("generated operation %q has unsupported exposure %q", operationID, exposure)
			}
		}
		descriptors = append(descriptors, access.OperationDescriptor{
			ID: operationID, Kind: access.OperationKindCommand, Owner: command.Owner,
			Target:    access.OperationTarget{Type: access.SecurableType(command.Target.Type), Parameter: command.Target.Parameter},
			Privilege: privilege, AuditEvent: command.Audit.SuccessAction,
			HTTPIdempotency: command.Idempotency, HTTPConcurrency: command.Concurrency,
			ExposedSurfaces: surfaces,
		})
	}
	return access.NewOperationCatalog(descriptors)
}
