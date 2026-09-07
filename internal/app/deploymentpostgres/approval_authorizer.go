package deploymentpostgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	depauth "github.com/flidai/leapview/internal/deployment/postgres"
)

// AccessApprovalAuthorizer binds approval authorization to the process-owned
// target and either the active immutable capability projection or, for the
// first reviewer decision only, the exact candidate generation's immutable
// policy. It starts fail-closed until composition installs those resolvers.
type AccessApprovalAuthorizer struct {
	TargetID                         string
	ResolveTarget                    func(context.Context, string) (depauth.DeliveryTarget, error)
	CurrentProject                   func(context.Context) (string, error)
	EffectiveCapabilities            func(context.Context, string) ([]access.Capability, error)
	CandidateCapabilities            func(context.Context, string, string) (string, string, []access.Capability, error)
	bootstrapAuthorization           func(context.Context) (accessmodule.BootstrapAuthorization, bool)
	publicationApprovalAuthorization func(context.Context) (accessmodule.PublicationApprovalBootstrapAuthorization, bool)
}

// SetCandidateResolver installs the immutable candidate-generation capability
// lookup used by the fresh-target reviewer path. The resolver must return the
// project bound to the exact generation as well as that generation's effective
// capabilities for the reviewer principal.
func (a *AccessApprovalAuthorizer) SetCandidateResolver(resolve func(context.Context, string, string) (string, string, []access.Capability, error)) {
	if a == nil {
		return
	}
	a.CandidateCapabilities = resolve
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
	required := access.CapabilityProjectAdmin
	if input.Action == depauth.ApprovalActionRequest {
		required = access.CapabilityResourcePublish
	}
	if a.bootstrapAuthorization != nil {
		if marker, marked := a.bootstrapAuthorization(ctx); marked {
			if input.Action != depauth.ApprovalActionRequest || marker.ProjectID.String() != target.ProjectID || marker.PrincipalID != input.Actor.PrincipalID || marker.Capability != required {
				return depauth.ErrApprovalUnauthorized
			}
			return nil
		}
	}
	if a.publicationApprovalAuthorization != nil {
		if marker, marked := a.publicationApprovalAuthorization(ctx); marked {
			if input.Action != depauth.ApprovalActionApprove || marker.ProjectID.String() != target.ProjectID || marker.PrincipalID != input.Actor.PrincipalID || marker.Capability != access.CapabilityProjectAdmin || a.CandidateCapabilities == nil {
				return depauth.ErrApprovalUnauthorized
			}
			project, environment, capabilities, err := a.CandidateCapabilities(ctx, input.Request.GenerationID, input.Actor.PrincipalID)
			if err != nil {
				return fmt.Errorf("%w: resolve candidate approval capabilities: %v", depauth.ErrApprovalUnauthorized, err)
			}
			if project != target.ProjectID || environment != target.Environment || !capabilityAllowed(capabilities, access.CapabilityProjectAdmin) {
				return depauth.ErrApprovalUnauthorized
			}
			return nil
		}
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
