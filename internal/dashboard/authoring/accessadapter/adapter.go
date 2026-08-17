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

type Adapter struct {
	authorize AuthorizeResource
}

func New(authorize AuthorizeResource) (*Adapter, error) {
	if authorize == nil {
		return nil, fmt.Errorf("dashboard authoring canonical resource authorizer is required")
	}
	return &Adapter{authorize: authorize}, nil
}

var _ service.Authorizer = (*Adapter)(nil)

func (a *Adapter) Authorize(ctx context.Context, request service.AuthorizationRequest) error {
	if a == nil || a.authorize == nil {
		return fmt.Errorf("dashboard authoring canonical resource authorizer is required")
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
	resource, err := access.NewResourceRef(request.DashboardID, graph.KindDashboard)
	if err != nil {
		return fmt.Errorf("%w: dashboard resource: %v", ErrInvalid, err)
	}
	allowed, err := a.authorize(ctx, actorID, request.ProjectID, resource, capability)
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
