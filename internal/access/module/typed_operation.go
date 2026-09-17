package module

import (
	"fmt"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// APIGenTypedOperationRequirementService adapts generated APIGen authz
// metadata to the product-owned typed-operation requirement service. An
// operation with no typed metadata remains on the legacy capability path for
// now; a typed credential reaching it is denied by ResolvePairs.
type APIGenTypedOperationRequirementService struct {
	requirements map[string]access.TypedOperationRequirement
}

// NewAPIGenTypedOperationRequirementService validates every typed action and
// resolver emitted by APIGen. Partial metadata is rejected so a malformed
// operation can never silently fall back to a legacy capability.
func NewAPIGenTypedOperationRequirementService(operations map[string]APIGenOperationContract) (*APIGenTypedOperationRequirementService, error) {
	service := &APIGenTypedOperationRequirementService{requirements: make(map[string]access.TypedOperationRequirement)}
	product := access.NewTypedOperationRequirementService()
	for operationID, contract := range operations {
		if contract.Action == "" && contract.Resolver == "" {
			continue
		}
		if contract.OperationID != operationID {
			return nil, fmt.Errorf("typed operation %q has mismatched operation identity %q", operationID, contract.OperationID)
		}
		requirement, err := product.Requirement(access.Action(contract.Action), contract.Resolver)
		if err != nil {
			return nil, fmt.Errorf("APIGen operation %q typed requirement: %w", operationID, err)
		}
		service.requirements[operationID] = requirement
	}
	return service, nil
}

// Requirement returns the validated product requirement for one operation.
func (service *APIGenTypedOperationRequirementService) Requirement(operationID string) (access.TypedOperationRequirement, bool) {
	if service == nil {
		return access.TypedOperationRequirement{}, false
	}
	requirement, ok := service.requirements[operationID]
	return requirement, ok
}

// ResolvePairs converts exact domain-resolved targets into the operation's
// requested permission pairs. Missing metadata, invalid target kinds, and
// malformed project identities all fail closed.
func (service *APIGenTypedOperationRequirementService) ResolvePairs(operationID string, projectID projectgraph.ResourceID, resources ...access.ResourceRef) ([]access.PermissionPair, error) {
	requirement, ok := service.Requirement(operationID)
	if !ok {
		return nil, fmt.Errorf("typed requirement for APIGen operation %q is unavailable", operationID)
	}
	return requirement.ResolvePairs(projectID, resources...)
}
