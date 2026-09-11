// Package accessadapter bridges dashboard authoring to the canonical project
// resource capability contract. Authoring does not know about access grants;
// the adapter supplies one narrow, graph-identity based decision boundary.
package accessadapter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/project/graph"
)

var ErrInvalid = errors.New("invalid dashboard authoring authorization request")

// AuthorizeResource is implemented by access-owned policy evaluation. The
// project and resource identities are graph IDs; capability is the canonical
// object-grant action, never a legacy broad-scope privilege.
type AuthorizeResource func(context.Context, string, graph.ResourceID, access.ResourceRef, access.Capability) (bool, error)

type AuthorizeProjectCapability func(context.Context, string, graph.ResourceID, access.Capability) (bool, error)

type Options struct {
	AuthorizeResource          AuthorizeResource
	AuthorizeProjectCapability AuthorizeProjectCapability
}

type Adapter struct {
	authorizeResource AuthorizeResource
	authorizeProject  AuthorizeProjectCapability
}

func New(options Options) (*Adapter, error) {
	if options.AuthorizeResource == nil || options.AuthorizeProjectCapability == nil {
		return nil, fmt.Errorf("dashboard authoring resource and project capability authorizers are required")
	}
	return &Adapter{authorizeResource: options.AuthorizeResource, authorizeProject: options.AuthorizeProjectCapability}, nil
}

var _ service.Authorizer = (*Adapter)(nil)

func (a *Adapter) Authorize(ctx context.Context, request service.AuthorizationRequest) error {
	if a == nil || a.authorizeResource == nil || a.authorizeProject == nil {
		return fmt.Errorf("dashboard authoring resource and project capability authorizers are required")
	}
	actorID := strings.TrimSpace(request.ActorID)
	if actorID == "" {
		return fmt.Errorf("%w: actor id is required", ErrInvalid)
	}
	if err := request.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: project id: %v", ErrInvalid, err)
	}
	if err := authoring.ValidateDashboardID(request.DashboardID); err != nil {
		return fmt.Errorf("%w: dashboard id: %v", ErrInvalid, err)
	}
	capability, err := capabilityForAction(request.Action)
	if err != nil {
		return err
	}
	var allowed bool
	switch request.Target {
	case service.AuthorizationTargetProjectDashboard:
		resource, resourceErr := access.NewResourceRef(request.DashboardID, graph.KindDashboard)
		if resourceErr != nil {
			return fmt.Errorf("%w: dashboard resource: %v", ErrInvalid, resourceErr)
		}
		allowed, err = a.authorizeResource(ctx, actorID, request.ProjectID, resource, capability)
	case service.AuthorizationTargetNewDashboard:
		if request.Action != authoring.AuthorizationActionEdit {
			return fmt.Errorf("%w: new-dashboard authorization requires edit action", ErrInvalid)
		}
		allowed, err = a.authorizeProject(ctx, actorID, request.ProjectID, capability)
		if err == nil && allowed && strings.TrimSpace(request.OwnerPrincipalID) != "" && strings.TrimSpace(request.OwnerPrincipalID) != actorID {
			allowed, err = a.authorizeProject(ctx, actorID, request.ProjectID, access.CapabilityProjectAdmin)
		}
		if err == nil && allowed {
			if modelErr := request.SemanticModel.Validate(); modelErr != nil {
				return fmt.Errorf("%w: semantic model: %v", ErrInvalid, modelErr)
			}
			semanticResource, resourceErr := access.NewResourceRef(request.SemanticModel, graph.KindSemanticModel)
			if resourceErr != nil {
				return fmt.Errorf("%w: semantic model resource: %v", ErrInvalid, resourceErr)
			}
			allowed, err = a.authorizeResource(ctx, actorID, request.ProjectID, semanticResource, access.CapabilityResourceRead)
		}
	case service.AuthorizationTargetAuthoredDashboard:
		allowed, err = a.authorizeProject(ctx, actorID, request.ProjectID, capability)
		if err == nil && allowed && strings.TrimSpace(request.OwnerPrincipalID) != actorID {
			if request.Action == authoring.AuthorizationActionView && request.Visibility == authoring.VisibilityOrganization {
				break
			}
			allowed, err = a.authorizeProject(ctx, actorID, request.ProjectID, access.CapabilityProjectAdmin)
		}
	default:
		return fmt.Errorf("%w: unsupported authorization target %q", ErrInvalid, request.Target)
	}
	if err != nil {
		return err
	}
	if !allowed {
		return access.ErrForbidden
	}
	return nil
}

func capabilityForAction(action authoring.AuthorizationAction) (access.Capability, error) {
	switch action {
	case authoring.AuthorizationActionView:
		return access.CapabilityResourceRead, nil
	case authoring.AuthorizationActionEdit:
		return access.CapabilityResourceEdit, nil
	case authoring.AuthorizationActionPublish:
		return access.CapabilityResourcePublish, nil
	case authoring.AuthorizationActionArchive:
		return access.CapabilityResourceManage, nil
	default:
		return "", fmt.Errorf("%w: unsupported authorization action %q", ErrInvalid, action)
	}
}
