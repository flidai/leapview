// Package accessadapter bridges dashboard authoring authorization requests to
// the access-owned authorization boundary. The authoring domain remains
// independent from access privileges and securable object representations.
package accessadapter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/service"
)

// ErrInvalid identifies an authorization request that cannot be mapped to an
// access decision. It is intentionally separate from access denial and from
// errors returned by the injected access boundary.
var ErrInvalid = errors.New("invalid dashboard authoring authorization request")

// AuthorizeObject is the narrow access capability required by this adapter.
// It matches access/module.Module.AuthorizeObject without coupling the
// authoring service to the access module or repository implementation.
type AuthorizeObject func(context.Context, string, access.Privilege, access.ObjectRef) (bool, error)

// Adapter implements service.Authorizer using access-owned privileges and
// object references.
type Adapter struct {
	authorize AuthorizeObject
}

// New validates and returns an authoring access adapter. The callback is the
// only dependency; this adapter never creates principals, registers objects,
// or mutates grants.
func New(authorize AuthorizeObject) (*Adapter, error) {
	if authorize == nil {
		return nil, fmt.Errorf("dashboard authoring access authorizer is required")
	}
	return &Adapter{authorize: authorize}, nil
}

var _ service.Authorizer = (*Adapter)(nil)

// Authorize maps a domain action to an access privilege on the exact
// workspace/dashboard securable object. Owner and semantic-model identities
// remain request evidence and are deliberately not used to grant access.
func (a *Adapter) Authorize(ctx context.Context, request service.AuthorizationRequest) error {
	if a == nil || a.authorize == nil {
		return fmt.Errorf("dashboard authoring access authorizer is required")
	}
	actorID := strings.TrimSpace(request.ActorID)
	workspaceID := strings.TrimSpace(request.WorkspaceID)
	if actorID == "" {
		return fmt.Errorf("%w: actor id is required", ErrInvalid)
	}
	if workspaceID == "" {
		return fmt.Errorf("%w: workspace id is required", ErrInvalid)
	}
	if err := request.DashboardID.Validate(); err != nil {
		return err
	}
	privilege, err := privilegeForAction(request.Action)
	if err != nil {
		return err
	}
	object := access.ItemObject(access.SecurableDashboard, workspaceID, request.DashboardID.String())
	allowed, err := a.authorize(ctx, actorID, privilege, object)
	if err != nil {
		return err
	}
	if !allowed {
		return access.ErrForbidden
	}
	return nil
}

func privilegeForAction(action authoring.AuthorizationAction) (access.Privilege, error) {
	switch action {
	case authoring.AuthorizationActionView:
		return access.PrivilegeViewItem, nil
	case authoring.AuthorizationActionEdit:
		return access.PrivilegeEditItem, nil
	case authoring.AuthorizationActionPublish, authoring.AuthorizationActionArchive:
		return access.PrivilegeManageItem, nil
	default:
		return "", fmt.Errorf("%w: unsupported authorization action %q", ErrInvalid, action)
	}
}
