package deploymentpostgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	depauth "github.com/flidai/leapview/internal/deployment/postgres"
)

// AccessApprovalAuthorizer binds approval authorization to the active,
// immutable Access capability projection and the process-bound delivery
// target. It intentionally starts fail-closed: composition must install both
// resolvers after the runtime host is ready before a production approval can
// succeed.
type AccessApprovalAuthorizer struct {
	TargetID              string
	ResolveTarget         func(context.Context, string) (depauth.DeliveryTarget, error)
	CurrentProject        func(context.Context) (string, error)
	EffectiveCapabilities func(context.Context, string) ([]access.Capability, error)
}

func NewAccessApprovalAuthorizer(targetID string, resolveTarget func(context.Context, string) (depauth.DeliveryTarget, error)) (*AccessApprovalAuthorizer, error) {
	if strings.TrimSpace(targetID) == "" || resolveTarget == nil {
		return nil, errors.New("approval authorizer target and resolver are required")
	}
	return &AccessApprovalAuthorizer{TargetID: targetID, ResolveTarget: resolveTarget}, nil
}

// SetResolvers installs active runtime and Access capability lookups. Passing
// nil keeps the adapter fail-closed and is useful during startup/error paths.
func (a *AccessApprovalAuthorizer) SetResolvers(project func(context.Context) (string, error), capabilities func(context.Context, string) ([]access.Capability, error)) {
	if a == nil {
		return
	}
	a.CurrentProject = project
	a.EffectiveCapabilities = capabilities
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
	if a.CurrentProject == nil || a.EffectiveCapabilities == nil {
		return depauth.ErrApprovalUnauthorized
	}
	project, err := a.CurrentProject(ctx)
	if err != nil || strings.TrimSpace(project) == "" || project != target.ProjectID {
		return depauth.ErrApprovalUnauthorized
	}
	capabilities, err := a.EffectiveCapabilities(ctx, input.Actor.PrincipalID)
	if err != nil {
		return fmt.Errorf("%w: resolve effective capabilities: %v", depauth.ErrApprovalUnauthorized, err)
	}
	required := access.CapabilityProjectAdmin
	if input.Action == depauth.ApprovalActionRequest {
		required = access.CapabilityResourcePublish
	}
	if !capabilityAllowed(capabilities, required) {
		return depauth.ErrApprovalUnauthorized
	}
	return nil
}

func capabilityAllowed(capabilities []access.Capability, required access.Capability) bool {
	for _, capability := range capabilities {
		if capability == required || (required != access.CapabilityProjectAdmin && capability == access.CapabilityProjectAdmin) {
			return true
		}
	}
	return false
}
