package deploymentpostgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	depauth "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// AccessApprovalAuthorizer binds approval authorization to the process-owned
// target and either the active immutable typed-permission projection or, for the
// first reviewer decision only, the exact candidate generation's immutable
// policy. It starts fail-closed until composition installs those resolvers.
type AccessApprovalAuthorizer struct {
	TargetID                         string
	ResolveTarget                    func(context.Context, string) (depauth.DeliveryTarget, error)
	CurrentProject                   func(context.Context) (string, error)
	EffectivePermissions             func(context.Context, string) ([]access.PermissionPair, error)
	CandidatePermissions             func(context.Context, string, string) (string, string, []access.PermissionPair, error)
	bootstrapAuthorization           func(context.Context) (accessmodule.BootstrapAuthorization, bool)
	publicationApprovalAuthorization func(context.Context) (accessmodule.PublicationApprovalBootstrapAuthorization, bool)
}

// SetCandidateResolver installs the immutable candidate-generation permission
// lookup used by the fresh-target reviewer path. The resolver must return the
// project bound to the exact generation as well as that generation's effective
// permissions for the reviewer principal.
func (a *AccessApprovalAuthorizer) SetCandidateResolver(resolve func(context.Context, string, string) (string, string, []access.PermissionPair, error)) {
	if a == nil {
		return
	}
	a.CandidatePermissions = resolve
}

func NewAccessApprovalAuthorizer(targetID string, resolveTarget func(context.Context, string) (depauth.DeliveryTarget, error)) (*AccessApprovalAuthorizer, error) {
	if strings.TrimSpace(targetID) == "" || resolveTarget == nil {
		return nil, errors.New("approval authorizer target and resolver are required")
	}
	return &AccessApprovalAuthorizer{
		TargetID: targetID, ResolveTarget: resolveTarget,
		bootstrapAuthorization:           accessmodule.BootstrapAuthorizationFromContext,
		publicationApprovalAuthorization: accessmodule.PublicationApprovalBootstrapAuthorizationFromContext,
	}, nil
}

// SetResolvers installs active runtime and Access typed-permission lookups. Passing
// nil keeps the adapter fail-closed and is useful during startup/error paths.
func (a *AccessApprovalAuthorizer) SetResolvers(project func(context.Context) (string, error), permissions func(context.Context, string) ([]access.PermissionPair, error)) {
	if a == nil {
		return
	}
	a.CurrentProject = project
	a.EffectivePermissions = permissions
}

func (a *AccessApprovalAuthorizer) AuthorizeApproval(ctx context.Context, input depauth.ApprovalAuthorizationInput) error {
	if a == nil || a.ResolveTarget == nil || a.TargetID == "" || input.Actor.PrincipalID == "" {
		return depauth.ErrApprovalUnauthorized
	}
	if input.Request.TargetID != a.TargetID {
		return depauth.ErrApprovalUnauthorized
	}
	target, err := a.ResolveTarget(ctx, input.Request.TargetID)
	if err != nil {
		return fmt.Errorf("%w: resolve delivery target: %v", depauth.ErrApprovalUnauthorized, err)
	}
	if target.TargetID != a.TargetID || target.ProjectID == "" || target.Environment == "" {
		return depauth.ErrApprovalUnauthorized
	}
	requiredAction, ok := approvalActionPermission(input.Action)
	if !ok {
		return depauth.ErrApprovalUnauthorized
	}
	markerCapability := access.CapabilityProjectAdmin
	if input.Action == depauth.ApprovalActionRequest {
		markerCapability = access.CapabilityResourcePublish
	}
	if a.bootstrapAuthorization != nil {
		if marker, marked := a.bootstrapAuthorization(ctx); marked {
			if input.Action != depauth.ApprovalActionRequest || marker.ProjectID.String() != target.ProjectID || marker.PrincipalID != input.Actor.PrincipalID || marker.Capability != markerCapability {
				return depauth.ErrApprovalUnauthorized
			}
			return nil
		}
	}
	if a.publicationApprovalAuthorization != nil {
		if marker, marked := a.publicationApprovalAuthorization(ctx); marked {
			if input.Action != depauth.ApprovalActionApprove || marker.ProjectID.String() != target.ProjectID || marker.PrincipalID != input.Actor.PrincipalID || marker.Capability != access.CapabilityProjectAdmin || a.CandidatePermissions == nil {
				return depauth.ErrApprovalUnauthorized
			}
			project, environment, permissions, err := a.CandidatePermissions(ctx, input.Request.GenerationID, input.Actor.PrincipalID)
			if err != nil {
				return fmt.Errorf("%w: resolve candidate approval permissions: %v", depauth.ErrApprovalUnauthorized, err)
			}
			if project != target.ProjectID || environment != target.Environment || !approvalPermissionAllowed(permissions, requiredAction, target.ProjectID) {
				return depauth.ErrApprovalUnauthorized
			}
			return nil
		}
	}
	if a.CurrentProject == nil || a.EffectivePermissions == nil {
		return depauth.ErrApprovalUnauthorized
	}
	project, err := a.CurrentProject(ctx)
	if err != nil || strings.TrimSpace(project) == "" || project != target.ProjectID {
		return depauth.ErrApprovalUnauthorized
	}
	permissions, err := a.EffectivePermissions(ctx, input.Actor.PrincipalID)
	if err != nil {
		return fmt.Errorf("%w: resolve effective permissions: %v", depauth.ErrApprovalUnauthorized, err)
	}
	if !approvalPermissionAllowed(permissions, requiredAction, target.ProjectID) {
		return depauth.ErrApprovalUnauthorized
	}
	return nil
}

func approvalActionPermission(action depauth.ApprovalAction) (access.Action, bool) {
	switch action {
	case depauth.ApprovalActionRequest:
		return access.ActionDeliveryPublish, true
	case depauth.ApprovalActionApprove, depauth.ApprovalActionDeny, depauth.ApprovalActionRevoke:
		return access.ActionDeliveryApprove, true
	default:
		return "", false
	}
}

func approvalPermissionAllowed(permissions []access.PermissionPair, action access.Action, projectID string) bool {
	pair, err := access.NewProjectPermissionPair(action, projectgraph.ResourceID(projectID))
	if err != nil {
		return false
	}
	return access.PermissionSetAllows(permissions, pair)
}
