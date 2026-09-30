package app

import (
	"context"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	"github.com/flidai/leapview/internal/platform/typednil"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
)

// foregroundRuntimeCredentialAuthority is the request-bound authority adapter
// for a foreground connection.use operation. It resolves only the pin in the
// exact currently leased serving generation. Its short metadata lease does not
// authorize semantic queries, refreshes, or other long-lived runtime work; a
// caller must provide a separate encompassing runtime/admission lease for any
// consumer that outlives ResolveRuntimeCredential.
type foregroundRuntimeCredentialAuthority struct {
	instanceID  string
	environment string
	principalID string

	provider runtimehostmodule.Provider
	evidence activeConnectionEvidenceSource
	owners   credentialmodule.CustomerOwnerReader
	bindings credentialConnectionBindingLookup
	subjects func(context.Context, string) ([]access.SubjectRef, error)
	recheck  credentialmodule.CredentialAuthorityRechecker
}

var _ credentialmodule.RuntimeUseAuthority = foregroundRuntimeCredentialAuthority{}

func (authority foregroundRuntimeCredentialAuthority) ResolveRuntimeCredential(
	ctx context.Context,
	identity projectgraph.ServingIdentity,
	resource credentialmodule.RuntimeResource,
) (credentialmodule.RuntimeCredentialReference, error) {
	if ctx == nil || identity.Validate() != nil || resource.Validate() != nil || resource.ScopeKind != "connection" ||
		!canonicalRuntimeCredentialAuthorityValue(authority.instanceID) ||
		!canonicalRuntimeCredentialAuthorityValue(authority.environment) ||
		!canonicalRuntimeCredentialAuthorityValue(authority.principalID) ||
		resource.TargetID != authority.instanceID || resource.ProjectID != identity.ProjectID.String() ||
		resource.Environment != authority.environment || identity.Environment != authority.environment {
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeInvalid
	}
	if typednil.IsNil(authority.provider) || typednil.IsNil(authority.evidence.releases) ||
		typednil.IsNil(authority.evidence.commitments) ||
		!canonicalRuntimeCredentialAuthorityValue(authority.evidence.targetID) ||
		authority.evidence.targetID != authority.instanceID || authority.evidence.environment != authority.environment ||
		typednil.IsNil(authority.owners) || typednil.IsNil(authority.bindings) ||
		authority.subjects == nil || authority.recheck == nil {
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeUnavailable
	}
	if err := ctx.Err(); err != nil {
		return credentialmodule.RuntimeCredentialReference{}, err
	}
	principal, ok := accessmodule.PrincipalFromContext(ctx)
	if !ok || !canonicalRuntimeCredentialAuthorityValue(principal.ID) || principal.ID != authority.principalID || principal.DevBypass {
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeForbidden
	}
	_, hasAPICredential := accessmodule.APICredentialFromContext(ctx)
	_, hasSessionCredential := accessmodule.SessionCredentialEvidenceFromContext(ctx)
	if !hasAPICredential && !hasSessionCredential || hasAPICredential && hasSessionCredential {
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeForbidden
	}

	lease, acquireErr := authority.provider.Acquire(ctx)
	if acquireErr != nil {
		if !typednil.IsNil(lease) {
			lease.Release()
		}
		return credentialmodule.RuntimeCredentialReference{}, safeRuntimeCredentialAuthorityError(ctx, acquireErr)
	}
	if typednil.IsNil(lease) {
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeUnavailable
	}
	defer lease.Release()
	if lease.Identity() != identity {
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeConflict
	}
	snapshotLease, ok := lease.(interface {
		AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
	})
	if !ok {
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeUnavailable
	}
	leasedSnapshot := snapshotLease.AuthorizationSnapshot()
	if leasedSnapshot.Identity() != identity || leasedSnapshot.ValidateBound() != nil {
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeConflict
	}
	if err := ctx.Err(); err != nil {
		return credentialmodule.RuntimeCredentialReference{}, err
	}

	connectionID, err := projectgraph.NewResourceID(resource.ResourceID)
	if err != nil {
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeInvalid
	}
	connectionRef, err := access.NewResourceRef(connectionID, projectgraph.KindConnection)
	if err != nil {
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeInvalid
	}
	pair, err := access.NewExactPermissionPair(access.ActionConnectionUse, identity.ProjectID, connectionRef)
	if err != nil {
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeInvalid
	}
	checkConnection := accessmodule.ConnectionAuthorizerFromSnapshot(
		authority.instanceID,
		func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) { return leasedSnapshot, nil },
		authority.subjects,
	)
	allowed, err := checkConnection(ctx, principal.ID, identity.ProjectID.String(), connectionID.String(), access.ActionConnectionUse)
	if err != nil {
		return credentialmodule.RuntimeCredentialReference{}, safeRuntimeCredentialAuthorityError(ctx, err)
	}
	if !allowed {
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeForbidden
	}
	if err := authority.recheck(ctx, principal.ID, pair); err != nil {
		return credentialmodule.RuntimeCredentialReference{}, safeRuntimeCredentialAuthorityError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return credentialmodule.RuntimeCredentialReference{}, err
	}

	return resolveCommittedRuntimeCredential(ctx, identity, resource, authority.evidence, authority.owners, authority.bindings)
}
