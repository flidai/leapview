package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// authorizeSemanticModelResourceRead is the semantic metadata boundary used
// by the dashboard semantic API. The development bypass is intentionally
// narrower than the general project-resource authorizer: it applies only to a
// validated semantic-model resource read in the request-local, identity-bound
// development context. Ordinary semantic-model reads use the canonical
// snapshot path. This does not authorize protected semantic data: the API's
// independent consumer-authority check and planner enforcement still apply.
func authorizeSemanticModelResourceRead(
	ctx context.Context,
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	principalID string,
	projectID projectgraph.ResourceID,
	resource access.ResourceRef,
	capability access.Capability,
) (bool, error) {
	if accessModule == nil || runtimeHost == nil {
		return false, fmt.Errorf("authorization modules are required")
	}
	if strings.TrimSpace(principalID) == "" {
		return false, nil
	}
	if err := projectID.Validate(); err != nil {
		return false, err
	}
	if err := resource.Validate(); err != nil {
		return false, err
	}
	if resource.Kind() != projectgraph.KindSemanticModel || capability != access.CapabilityResourceRead {
		return false, nil
	}
	if runtimeHost.ProjectID() != projectID {
		return false, fmt.Errorf("runtime project %q does not match requested project %q", runtimeHost.ProjectID(), projectID)
	}
	if requestLocalDevelopmentAuthorization(ctx, principalID) {
		return true, nil
	}
	return authorizeProjectResources(ctx, accessModule, runtimeHost, principalID, projectID, []access.ResourceRef{resource}, capability)
}
