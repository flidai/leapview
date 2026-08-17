package module

import (
	"net/http"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func authorizePipeline(r *http.Request, identity projectgraph.ServingIdentity, pipelineID string, capability access.Capability, config AuthorizationConfig) (bool, error) {
	if err := identity.Validate(); err != nil {
		return false, err
	}
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
	if config.AuthorizeObject == nil {
		return false, nil
	}
	pipelineResourceID, err := projectgraph.NewResourceID(pipelineID)
	if err != nil {
		return false, err
	}
	resource, err := access.NewResourceRef(pipelineResourceID, projectgraph.KindPipeline)
	if err != nil {
		return false, err
	}
	return config.AuthorizeObject(r.Context(), principal.ID, capability, resource)
}
