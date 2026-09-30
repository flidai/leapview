package app

import (
	"context"
	"errors"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	"github.com/flidai/leapview/internal/platform/typednil"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
)

// runtimeCredentialReaderFactory binds one authority to the credential
// service's configured repository and keyring for a request or queued job.
type runtimeCredentialReaderFactory interface {
	RuntimeReader(credentialmodule.RuntimeUseAuthority) (credentialmodule.RuntimeCredentialReader, error)
}

var _ runtimeCredentialReaderFactory = (*credentialmodule.Services)(nil)

// foregroundRuntimeCredentialCheck is the synchronous, request-bound
// foreground entry point for checking one serving connection's credential.
// It selects the exact serving identity before analytics work; its reader
// decorator acquires that identity's long-lived lease only after analytics
// source-work admission succeeds.
type foregroundRuntimeCredentialCheck struct {
	authority foregroundRuntimeCredentialAuthority
	readers   runtimeCredentialReaderFactory
	analytics localRuntimeCredentialAnalytics
}

func (check foregroundRuntimeCredentialCheck) check(
	ctx context.Context,
	connectionID projectgraph.ResourceID,
) (connectionbinding.CredentialIdentity, error) {
	if ctx == nil || !connectionID.Valid() ||
		!canonicalRuntimeCredentialAuthorityValue(check.authority.instanceID) ||
		!canonicalRuntimeCredentialAuthorityValue(check.authority.environment) ||
		typednil.IsNil(check.authority.provider) || typednil.IsNil(check.readers) ||
		typednil.IsNil(check.analytics) {
		return connectionbinding.CredentialIdentity{}, credentialmodule.ErrRuntimeInvalid
	}
	if err := ctx.Err(); err != nil {
		return connectionbinding.CredentialIdentity{}, err
	}
	principal, ok := accessmodule.PrincipalFromContext(ctx)
	if !ok || !canonicalRuntimeCredentialAuthorityValue(principal.ID) || principal.DevBypass {
		return connectionbinding.CredentialIdentity{}, credentialmodule.ErrRuntimeForbidden
	}

	selectionLease, err := check.authority.provider.Acquire(ctx)
	if err != nil {
		if !typednil.IsNil(selectionLease) {
			selectionLease.Release()
		}
		return connectionbinding.CredentialIdentity{}, safeRuntimeCredentialAuthorityError(ctx, err)
	}
	if typednil.IsNil(selectionLease) {
		return connectionbinding.CredentialIdentity{}, credentialmodule.ErrRuntimeUnavailable
	}
	localCheck, identity, binding, versionID, setupErr := check.selectCredential(ctx, selectionLease, principal.ID, connectionID)
	if setupErr != nil {
		return connectionbinding.CredentialIdentity{}, setupErr
	}
	return localCheck.check(ctx, identity, binding, versionID)
}

func (check foregroundRuntimeCredentialCheck) selectCredential(
	ctx context.Context,
	selectionLease runtimehostmodule.Lease,
	principalID string,
	connectionID projectgraph.ResourceID,
) (localRuntimeCredentialCheck, projectgraph.ServingIdentity, connectionbinding.TargetBinding, string, error) {
	// This lease only selects the generation and resolves its committed pin.
	// Release it before localCheck.check can wait on source-work admission.
	defer selectionLease.Release()
	identity, err := validateRuntimeCredentialLease(selectionLease, projectgraph.ServingIdentity{})
	if err != nil {
		return localRuntimeCredentialCheck{}, projectgraph.ServingIdentity{}, connectionbinding.TargetBinding{}, "", err
	}
	if identity.Environment != check.authority.environment {
		return localRuntimeCredentialCheck{}, projectgraph.ServingIdentity{}, connectionbinding.TargetBinding{}, "", credentialmodule.ErrRuntimeConflict
	}
	if err := ctx.Err(); err != nil {
		return localRuntimeCredentialCheck{}, projectgraph.ServingIdentity{}, connectionbinding.TargetBinding{}, "", err
	}

	authority := check.authority
	authority.principalID = principalID
	resource := credentialmodule.RuntimeResource{
		ScopeKind: "connection", TargetID: authority.instanceID, ProjectID: identity.ProjectID.String(),
		Environment: identity.Environment, ResourceID: connectionID.String(),
	}
	reference, err := authority.ResolveRuntimeCredential(ctx, identity, resource)
	if err != nil {
		return localRuntimeCredentialCheck{}, projectgraph.ServingIdentity{}, connectionbinding.TargetBinding{}, "", err
	}
	if reference.VersionID == "" || reference.Scope.Resource != resource {
		return localRuntimeCredentialCheck{}, projectgraph.ServingIdentity{}, connectionbinding.TargetBinding{}, "", credentialmodule.ErrRuntimeConflict
	}
	reader, err := check.readers.RuntimeReader(authority)
	if err != nil {
		return localRuntimeCredentialCheck{}, projectgraph.ServingIdentity{}, connectionbinding.TargetBinding{}, "", safeRuntimeCredentialAuthorityError(ctx, err)
	}
	if typednil.IsNil(reader) {
		return localRuntimeCredentialCheck{}, projectgraph.ServingIdentity{}, connectionbinding.TargetBinding{}, "", credentialmodule.ErrRuntimeUnavailable
	}
	binding, err := authority.bindings.Binding(ctx, connectionbinding.BindingScope{
		ProjectID: identity.ProjectID, Environment: identity.Environment,
	}, connectionbinding.TargetID(authority.instanceID), connectionID)
	if err != nil {
		return localRuntimeCredentialCheck{}, projectgraph.ServingIdentity{}, connectionbinding.TargetBinding{}, "", safeRuntimeCredentialAuthorityError(ctx, err)
	}
	localCheck := localRuntimeCredentialCheck{
		targetID: authority.instanceID, environment: identity.Environment,
		reader: runtimeCredentialLeaseReader{
			provider: authority.provider, identity: identity, reader: reader,
		},
		owners: authority.owners, bindings: authority.bindings,
		analytics: check.analytics,
	}
	return localCheck, identity, binding, reference.VersionID, nil
}

// runtimeCredentialLeaseReader adds the serving-generation lease
// that covers the credential consumer and its synchronous native cleanup.
// It runs only when the analytics module invokes read, after source-work
// admission has been acquired.
type runtimeCredentialLeaseReader struct {
	provider runtimehostmodule.Provider
	identity projectgraph.ServingIdentity
	reader   credentialmodule.RuntimeCredentialReader
}

func (reader runtimeCredentialLeaseReader) WithCredential(
	ctx context.Context,
	identity projectgraph.ServingIdentity,
	resource credentialmodule.RuntimeResource,
	consume func(credentialmodule.RuntimeCredentialReference, map[string]string) error,
) error {
	if ctx == nil || identity != reader.identity || identity.Validate() != nil || resource.Validate() != nil || consume == nil ||
		typednil.IsNil(reader.provider) || typednil.IsNil(reader.reader) {
		return credentialmodule.ErrRuntimeInvalid
	}
	lease, err := reader.provider.Acquire(ctx)
	if err != nil {
		if !typednil.IsNil(lease) {
			lease.Release()
		}
		return safeRuntimeCredentialAuthorityError(ctx, err)
	}
	if typednil.IsNil(lease) {
		return credentialmodule.ErrRuntimeUnavailable
	}
	if _, err := validateRuntimeCredentialLease(lease, reader.identity); err != nil {
		lease.Release()
		return err
	}
	if err := ctx.Err(); err != nil {
		lease.Release()
		return err
	}

	// A normal reader error releases this per-read lease. Cleanup errors are
	// observed before RuntimeResolver redacts them; a callback panic escapes as
	// an abnormal return, so either unknown cleanup outcome retains the lease.
	retainLease := false
	normalReturn := false
	defer func() {
		if normalReturn && !retainLease {
			lease.Release()
		}
	}()
	err = reader.reader.WithCredential(ctx, identity, resource, func(reference credentialmodule.RuntimeCredentialReference, fields map[string]string) error {
		callbackErr := consume(reference, fields)
		if errors.Is(callbackErr, analyticsmodule.ErrLocalRuntimeCredentialCleanupFailed) || errors.Is(callbackErr, analyticsruntime.ErrConnectionCleanupFailed) {
			retainLease = true
		}
		return callbackErr
	})
	normalReturn = true
	return err
}

func validateRuntimeCredentialLease(
	lease runtimehostmodule.Lease,
	want projectgraph.ServingIdentity,
) (projectgraph.ServingIdentity, error) {
	identity := lease.Identity()
	if identity.Validate() != nil || (want.Validate() == nil && identity != want) {
		return projectgraph.ServingIdentity{}, credentialmodule.ErrRuntimeConflict
	}
	snapshotLease, ok := lease.(interface {
		AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
	})
	if !ok {
		return projectgraph.ServingIdentity{}, credentialmodule.ErrRuntimeUnavailable
	}
	snapshot := snapshotLease.AuthorizationSnapshot()
	if snapshot.Identity() != identity || snapshot.ValidateBound() != nil {
		return projectgraph.ServingIdentity{}, credentialmodule.ErrRuntimeConflict
	}
	return identity, nil
}
