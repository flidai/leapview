// Package accessadapter bridges dashboard authoring to the canonical typed
// action/resource authorization contract. Authoring does not know about access
// grants; the adapter supplies one narrow, graph-identity based decision
// boundary.
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

// AuthorizeTypedResource evaluates an exact action/resource pair and reports
// whether a typed decision was available. A missing typed decision is denied;
// it can never be replaced by legacy capability authority.
type AuthorizeTypedResource func(context.Context, string, graph.ResourceID, access.ResourceRef, access.Action) (typed bool, allowed bool, err error)

// AuthorizeTypedProject evaluates an exact project/action pair.
type AuthorizeTypedProject func(context.Context, string, graph.ResourceID, access.Action) (typed bool, allowed bool, err error)

type Options struct {
	AuthorizeTypedResource AuthorizeTypedResource
	AuthorizeTypedProject  AuthorizeTypedProject
}

type Adapter struct {
	authorizeTypedResource AuthorizeTypedResource
	authorizeTypedProject  AuthorizeTypedProject
}

func New(options Options) (*Adapter, error) {
	if options.AuthorizeTypedResource == nil || options.AuthorizeTypedProject == nil {
		return nil, fmt.Errorf("dashboard authoring typed resource and project authorizers are required")
	}
	return &Adapter{
		authorizeTypedResource: options.AuthorizeTypedResource,
		authorizeTypedProject:  options.AuthorizeTypedProject,
	}, nil
}

var _ service.Authorizer = (*Adapter)(nil)

func (a *Adapter) Authorize(ctx context.Context, request service.AuthorizationRequest) error {
	if a == nil || a.authorizeTypedResource == nil || a.authorizeTypedProject == nil {
		return fmt.Errorf("dashboard authoring typed resource and project authorizers are required")
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
	resource, err := access.NewResourceRef(request.DashboardID, graph.KindDashboard)
	if err != nil {
		return fmt.Errorf("%w: dashboard resource: %v", ErrInvalid, err)
	}
	resourceAction, err := typedActionForAuthorization(request.Action)
	if err != nil {
		return err
	}

	switch request.Target {
	case service.AuthorizationTargetProjectDashboard, service.AuthorizationTargetAuthoredDashboard:
		if allowed, err := a.authorizeResource(ctx, actorID, request.ProjectID, resource, resourceAction); err != nil || !allowed {
			if err != nil {
				return err
			}
			return access.ErrForbidden
		}
		if request.Target == service.AuthorizationTargetAuthoredDashboard && request.DependencyChange {
			if err := a.authorizeSemanticDependency(ctx, actorID, request.ProjectID, request.SemanticModel); err != nil {
				return err
			}
		}
		return nil
	case service.AuthorizationTargetNewDashboard:
		if request.Action != authoring.AuthorizationActionEdit {
			return fmt.Errorf("%w: new-dashboard authorization requires edit action", ErrInvalid)
		}
		if allowed, err := a.authorizeProject(ctx, actorID, request.ProjectID, access.ActionDashboardCreate); err != nil || !allowed {
			if err != nil {
				return err
			}
			return access.ErrForbidden
		}
		return a.authorizeSemanticDependency(ctx, actorID, request.ProjectID, request.SemanticModel)
	default:
		return fmt.Errorf("%w: unsupported authorization target %q", ErrInvalid, request.Target)
	}
}

func (a *Adapter) authorizeSemanticDependency(ctx context.Context, actorID string, projectID graph.ResourceID, semanticModel graph.ResourceID) error {
	if err := semanticModel.Validate(); err != nil {
		return fmt.Errorf("%w: semantic model dependency: %v", ErrInvalid, err)
	}
	resource, err := access.NewResourceRef(semanticModel, graph.KindSemanticModel)
	if err != nil {
		return fmt.Errorf("%w: semantic model dependency resource: %v", ErrInvalid, err)
	}
	allowed, err := a.authorizeResource(ctx, actorID, projectID, resource, access.ActionSemanticRead)
	if err != nil {
		return err
	}
	if !allowed {
		return access.ErrForbidden
	}
	return nil
}

func (a *Adapter) authorizeResource(ctx context.Context, actorID string, projectID graph.ResourceID, resource access.ResourceRef, action access.Action) (bool, error) {
	typed, allowed, err := a.authorizeTypedResource(ctx, actorID, projectID, resource, action)
	if err != nil {
		return false, err
	}
	if !typed {
		return false, access.ErrForbidden
	}
	return allowed, nil
}

func (a *Adapter) authorizeProject(ctx context.Context, actorID string, projectID graph.ResourceID, action access.Action) (bool, error) {
	typed, allowed, err := a.authorizeTypedProject(ctx, actorID, projectID, action)
	if err != nil {
		return false, err
	}
	if !typed {
		return false, access.ErrForbidden
	}
	return allowed, nil
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
