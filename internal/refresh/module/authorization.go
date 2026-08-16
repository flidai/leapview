package module

import (
	"net/http"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func authorizePipeline(r *http.Request, identity projectgraph.ServingIdentity, pipelineID string, capability access.Capability, config AuthorizationConfig) (bool, error) {
	if config.CurrentPrincipal == nil {
		return false, nil
	}
	principal, ok := config.CurrentPrincipal(r)
	if !ok {
		return false, nil
	}
	if principal.DevBypass {
		return true, nil
	}
	if config.ResolvePipelineModel == nil {
		return false, nil
	}
	modelID, found, err := config.ResolvePipelineModel(r.Context(), identity, pipelineID)
	if err != nil || !found {
		return false, err
	}
	if config.AuthorizeObject == nil {
		return true, nil
	}
	resource, err := access.NewResourceRef(projectgraph.ResourceID(modelID), projectgraph.KindModel)
	if err != nil {
		return false, err
	}
	return config.AuthorizeObject(r.Context(), principal.ID, capability, resource)
}
