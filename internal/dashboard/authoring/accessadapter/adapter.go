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

// AuthorizeTypedResource evaluates an exact action/resource pair and reports
// whether typed authority was present. A false typed result tells the adapter
// it may retain the legacy capability path; a true result makes allowed the
// complete decision and prevents legacy authority from widening or replacing
// the typed assignment.
type AuthorizeTypedResource func(context.Context, string, graph.ResourceID, access.ResourceRef, access.Action) (typed bool, allowed bool, err error)

type AuthorizeTypedProject func(context.Context, string, graph.ResourceID, access.Action) (typed bool, allowed bool, err error)

type Options struct {
	AuthorizeResource          AuthorizeResource
	AuthorizeProjectCapability AuthorizeProjectCapability
	AuthorizeTypedResource     AuthorizeTypedResource
	AuthorizeTypedProject      AuthorizeTypedProject
}

type Adapter struct {
	authorizeResource      AuthorizeResource
	authorizeProject       AuthorizeProjectCapability
	authorizeTypedResource AuthorizeTypedResource
	authorizeTypedProject  AuthorizeTypedProject
}

func New(options Options) (*Adapter, error) {
	if options.AuthorizeResource == nil || options.AuthorizeProjectCapability == nil {
		return nil, fmt.Errorf("dashboard authoring resource and project capability authorizers are required")
	}
	return &Adapter{
		authorizeResource: options.AuthorizeResource, authorizeProject: options.AuthorizeProjectCapability,
		authorizeTypedResource: options.AuthorizeTypedResource, authorizeTypedProject: options.AuthorizeTypedProject,
	}, nil
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
		typed, typedAllowed, typedErr := a.typedResource(ctx, actorID, request.ProjectID, resource, request.Action)
		if typedErr != nil {
			return typedErr
		}
		if typed {
			if !typedAllowed {
				return access.ErrForbidden
			}
			return nil
		}
		allowed, err = a.authorizeResource(ctx, actorID, request.ProjectID, resource, capability)
	case service.AuthorizationTargetNewDashboard:
		if request.Action != authoring.AuthorizationActionEdit {
			return fmt.Errorf("%w: new-dashboard authorization requires edit action", ErrInvalid)
		}
		typedCreate, typedCreateAllowed, typedErr := a.typedProject(ctx, actorID, request.ProjectID, access.ActionDashboardCreate)
		if typedErr != nil {
			return typedErr
		}
		if typedCreate {
			allowed = typedCreateAllowed
		} else {
			allowed, err = a.authorizeProject(ctx, actorID, request.ProjectID, capability)
		}
		if err == nil && allowed && strings.TrimSpace(request.OwnerPrincipalID) != "" && strings.TrimSpace(request.OwnerPrincipalID) != actorID {
			// Typed dashboard.create is already an explicit assignment on the
			// containing project. It must not be downgraded to the legacy
			// project-admin/owner policy before a group assignment can create a
			// dashboard for another owner.
			if !typedCreate {
				allowed, err = a.authorizeProject(ctx, actorID, request.ProjectID, access.CapabilityProjectAdmin)
			}
		}
		if err == nil && allowed {
			if modelErr := request.SemanticModel.Validate(); modelErr != nil {
				return fmt.Errorf("%w: semantic model: %v", ErrInvalid, modelErr)
			}
			semanticResource, resourceErr := access.NewResourceRef(request.SemanticModel, graph.KindSemanticModel)
			if resourceErr != nil {
				return fmt.Errorf("%w: semantic model resource: %v", ErrInvalid, resourceErr)
			}
			typed, typedAllowed, typedErr := a.authorizeTypedResourceAction(ctx, actorID, request.ProjectID, semanticResource, access.ActionSemanticRead)
			if typedErr != nil {
				return typedErr
			}
			if typed {
				allowed = typedAllowed
			} else {
				allowed, err = a.authorizeResource(ctx, actorID, request.ProjectID, semanticResource, access.CapabilityResourceRead)
			}
		}
	case service.AuthorizationTargetAuthoredDashboard:
		resource, resourceErr := access.NewResourceRef(request.DashboardID, graph.KindDashboard)
		if resourceErr != nil {
			return fmt.Errorf("%w: dashboard resource: %v", ErrInvalid, resourceErr)
		}
		typed, typedAllowed, typedErr := a.typedResource(ctx, actorID, request.ProjectID, resource, request.Action)
		if typedErr != nil {
			return typedErr
		}
		if typed {
			if !typedAllowed {
				return access.ErrForbidden
			}
			if request.DependencyChange {
				if modelErr := request.SemanticModel.Validate(); modelErr != nil {
					return fmt.Errorf("%w: semantic model dependency: %v", ErrInvalid, modelErr)
				}
				semanticResource, resourceErr := access.NewResourceRef(request.SemanticModel, graph.KindSemanticModel)
				if resourceErr != nil {
					return fmt.Errorf("%w: semantic model dependency resource: %v", ErrInvalid, resourceErr)
				}
				semanticTyped, semanticAllowed, semanticErr := a.authorizeTypedResourceAction(ctx, actorID, request.ProjectID, semanticResource, access.ActionSemanticRead)
				if semanticErr != nil {
					return semanticErr
				}
				if semanticTyped {
					if !semanticAllowed {
						return access.ErrForbidden
					}
				} else {
					allowed, err = a.authorizeResource(ctx, actorID, request.ProjectID, semanticResource, access.CapabilityResourceRead)
					if err != nil || !allowed {
						break
					}
				}
			}
			return nil
		}
		allowed, err = a.authorizeProject(ctx, actorID, request.ProjectID, capability)
		if err == nil && allowed && strings.TrimSpace(request.OwnerPrincipalID) != actorID {
			if request.Action == authoring.AuthorizationActionView && request.Visibility == authoring.VisibilityOrganization {
				break
			}
			allowed, err = a.authorizeProject(ctx, actorID, request.ProjectID, access.CapabilityProjectAdmin)
		}
		if err == nil && allowed && request.DependencyChange {
			// Selecting a different semantic model is a new dependency edge. The
			// dashboard action and project role do not authorize that edge, so
			// require an independent governed model read before retaining it.
			if modelErr := request.SemanticModel.Validate(); modelErr != nil {
				return fmt.Errorf("%w: semantic model dependency: %v", ErrInvalid, modelErr)
			}
			semanticResource, resourceErr := access.NewResourceRef(request.SemanticModel, graph.KindSemanticModel)
			if resourceErr != nil {
				return fmt.Errorf("%w: semantic model dependency resource: %v", ErrInvalid, resourceErr)
			}
			typed, typedAllowed, typedErr := a.authorizeTypedResourceAction(ctx, actorID, request.ProjectID, semanticResource, access.ActionSemanticRead)
			if typedErr != nil {
				return typedErr
			}
			if typed {
				allowed = typedAllowed
			} else {
				allowed, err = a.authorizeResource(ctx, actorID, request.ProjectID, semanticResource, access.CapabilityResourceRead)
			}
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

func (a *Adapter) typedResource(ctx context.Context, actorID string, projectID graph.ResourceID, resource access.ResourceRef, action authoring.AuthorizationAction) (bool, bool, error) {
	if a == nil || a.authorizeTypedResource == nil {
		return false, false, nil
	}
	typedAction, err := typedActionForAuthorization(action)
	if err != nil {
		return true, false, err
	}
	return a.authorizeTypedResource(ctx, actorID, projectID, resource, typedAction)
}

func (a *Adapter) typedProject(ctx context.Context, actorID string, projectID graph.ResourceID, action access.Action) (bool, bool, error) {
	if a == nil || a.authorizeTypedProject == nil {
		return false, false, nil
	}
	return a.authorizeTypedProject(ctx, actorID, projectID, action)
}

func (a *Adapter) authorizeTypedResourceAction(ctx context.Context, actorID string, projectID graph.ResourceID, resource access.ResourceRef, action access.Action) (bool, bool, error) {
	if a == nil || a.authorizeTypedResource == nil {
		return false, false, nil
	}
	return a.authorizeTypedResource(ctx, actorID, projectID, resource, action)
}

func typedActionForAuthorization(action authoring.AuthorizationAction) (access.Action, error) {
	switch action {
	case authoring.AuthorizationActionView:
		return access.ActionDashboardRead, nil
	case authoring.AuthorizationActionEdit:
		return access.ActionDashboardUpdate, nil
	case authoring.AuthorizationActionPublish:
		return access.ActionDashboardPublish, nil
	case authoring.AuthorizationActionArchive:
		return access.ActionDashboardDelete, nil
	default:
		return "", fmt.Errorf("%w: unsupported authorization action %q", ErrInvalid, action)
	}
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
