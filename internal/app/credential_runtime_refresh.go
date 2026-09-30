package app

import (
	"context"
	"errors"
	"slices"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	"github.com/flidai/leapview/internal/platform/typednil"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	"github.com/flidai/leapview/pkg/jobs"
)

var errRefreshLocalCredentialUnsupported = errors.New("local credential pins are not supported by refresh candidate acquisition")

// refreshRuntimeCredentialReaderFactory binds credential access to immutable
// queued evidence, never an HTTP principal or ambient worker identity. The
// metadata preflight is live; reader consumption remains private until local
// candidate acquisition can preserve the pin through sealing and publication.
type refreshRuntimeCredentialReaderFactory struct {
	authority refreshRuntimeCredentialAuthority
	readers   runtimeCredentialReaderFactory
}

func (factory refreshRuntimeCredentialReaderFactory) bind(job refreshrun.JobRecord) (refreshRuntimeCredentialAuthority, error) {
	authority := factory.authority
	if job.Validate() != nil || job.Kind != refreshrun.JobKindRefreshPipeline || job.TargetType != refreshrun.TargetRefreshPipeline ||
		job.LeaseOwner == "" || job.LeaseRevision <= 0 || job.TargetRevision <= 0 || job.Authority.IsZero() ||
		job.Authority.Target.InstanceID != authority.instanceID || job.Identity.Environment != authority.environment ||
		!canonicalRuntimeCredentialAuthorityValue(authority.instanceID) || !canonicalRuntimeCredentialAuthorityValue(authority.environment) {
		return refreshRuntimeCredentialAuthority{}, credentialmodule.ErrRuntimeInvalid
	}
	if typednil.IsNil(authority.provider) || typednil.IsNil(authority.revalidator) || authority.subjects == nil ||
		typednil.IsNil(authority.evidence.releases) || typednil.IsNil(authority.evidence.commitments) ||
		authority.evidence.targetID != authority.instanceID || authority.evidence.environment != authority.environment ||
		typednil.IsNil(authority.owners) || typednil.IsNil(authority.bindings) {
		return refreshRuntimeCredentialAuthority{}, credentialmodule.ErrRuntimeUnavailable
	}
	authority.identity = job.Identity
	authority.queued = cloneRefreshCredentialAuthority(job.Authority)
	return authority, nil
}

// reader must be invoked by its native consumer only after source-work
// admission. The returned reader owns the exact-base serving lease through
// synchronous consumer cleanup; it does not acquire source admission itself.
func (factory refreshRuntimeCredentialReaderFactory) reader(job refreshrun.JobRecord) (credentialmodule.RuntimeCredentialReader, error) {
	authority, err := factory.bind(job)
	if err != nil {
		return nil, err
	}
	if typednil.IsNil(factory.readers) {
		return nil, credentialmodule.ErrRuntimeUnavailable
	}
	reader, err := factory.readers.RuntimeReader(authority)
	if err != nil {
		return nil, safeRuntimeCredentialAuthorityError(nil, err)
	}
	if typednil.IsNil(reader) {
		return nil, credentialmodule.ErrRuntimeUnavailable
	}
	return runtimeCredentialLeaseReader{provider: authority.provider, identity: job.Identity, reader: reader}, nil
}

// checkBaseCredentials never decrypts. Ordinary candidate acquisition does not
// preserve local pins yet, including pins outside the selected pipeline. Deny
// such a base before planning/building rather than silently using provider
// credentials or discarding a pin when constructing the successor generation.
func (factory refreshRuntimeCredentialReaderFactory) checkBaseCredentials(ctx context.Context, job refreshrun.JobRecord) error {
	configuration := factory.authority
	if ctx == nil || job.Validate() != nil || job.Kind != refreshrun.JobKindRefreshPipeline || job.TargetType != refreshrun.TargetRefreshPipeline ||
		job.Identity.Environment != configuration.environment || !canonicalRuntimeCredentialAuthorityValue(configuration.instanceID) ||
		configuration.evidence.targetID != configuration.instanceID || configuration.evidence.environment != configuration.environment {
		return credentialmodule.ErrRuntimeInvalid
	}
	if typednil.IsNil(configuration.evidence.releases) {
		return credentialmodule.ErrRuntimeUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	evidence, err := configuration.evidence.BindingEvidence(ctx, job.Identity.GenerationID, job.Identity.ProjectID.String())
	if err != nil {
		return safeRuntimeCredentialAuthorityError(ctx, err)
	}
	for _, binding := range evidence {
		if binding.CredentialVersionID == "" {
			continue
		}
		authority, err := factory.bind(job)
		if err != nil {
			return err
		}
		resource := credentialmodule.RuntimeResource{ScopeKind: "connection", TargetID: authority.instanceID,
			ProjectID: job.Identity.ProjectID.String(), Environment: job.Identity.Environment, ResourceID: binding.ConnectionID.String()}
		// Resolve only metadata for a selected connection. An unrelated pin is
		// equally unsupported, without pretending the job may access its secret.
		if authority.capturedConnection(resource) {
			if _, err := authority.ResolveRuntimeCredential(ctx, job.Identity, resource); err != nil {
				return err
			}
		}
		return errRefreshLocalCredentialUnsupported
	}
	return ctx.Err()
}

type refreshRuntimeCredentialAuthority struct {
	instanceID  string
	environment string
	identity    projectgraph.ServingIdentity
	queued      jobs.AuthorityEnvelope
	provider    runtimehostmodule.Provider
	evidence    activeConnectionEvidenceSource
	owners      credentialmodule.CustomerOwnerReader
	bindings    credentialConnectionBindingLookup
	subjects    func(context.Context, string) ([]access.SubjectRef, error)
	revalidator jobs.AuthorityRevalidator
}

func (authority refreshRuntimeCredentialAuthority) capturedConnection(resource credentialmodule.RuntimeResource) bool {
	for _, pair := range authority.queued.Permissions {
		converted, err := access.FromContractPermissionPair(pair)
		if err == nil && converted.Action == access.ActionConnectionUse && converted.Target.Scope == access.PermissionScopeResource &&
			converted.Target.ProjectID.String() == resource.ProjectID && converted.Target.ResourceKind == projectgraph.KindConnection &&
			converted.Target.ResourceID.String() == resource.ResourceID && !converted.Target.IncludeFuture {
			return true
		}
	}
	return false
}

func (authority refreshRuntimeCredentialAuthority) ResolveRuntimeCredential(ctx context.Context, identity projectgraph.ServingIdentity, resource credentialmodule.RuntimeResource) (credentialmodule.RuntimeCredentialReference, error) {
	if ctx == nil || identity.Validate() != nil || identity != authority.identity || resource.Validate() != nil || resource.ScopeKind != "connection" ||
		resource.TargetID != authority.instanceID || resource.ProjectID != identity.ProjectID.String() || resource.Environment != authority.environment || identity.Environment != authority.environment {
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeInvalid
	}
	if authority.queued.Validate() != nil || !authority.capturedConnection(resource) {
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeForbidden
	}
	if typednil.IsNil(authority.provider) || typednil.IsNil(authority.revalidator) || authority.subjects == nil ||
		typednil.IsNil(authority.evidence.releases) || typednil.IsNil(authority.evidence.commitments) ||
		typednil.IsNil(authority.owners) || typednil.IsNil(authority.bindings) {
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeUnavailable
	}
	if err := ctx.Err(); err != nil {
		return credentialmodule.RuntimeCredentialReference{}, err
	}
	lease, err := authority.provider.Acquire(ctx)
	if err != nil {
		if !typednil.IsNil(lease) {
			lease.Release()
		}
		return credentialmodule.RuntimeCredentialReference{}, safeRuntimeCredentialAuthorityError(ctx, err)
	}
	if typednil.IsNil(lease) {
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeUnavailable
	}
	defer lease.Release()
	if _, err := validateRuntimeCredentialLease(lease, identity); err != nil {
		return credentialmodule.RuntimeCredentialReference{}, err
	}
	snapshot := lease.(interface {
		AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
	}).AuthorizationSnapshot()
	check := accessmodule.ConnectionAuthorizerFromSnapshot(func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) { return snapshot, nil }, authority.subjects)
	allowed, err := check(ctx, authority.queued.ExecutionPrincipalID, resource.ProjectID, resource.ResourceID, access.ActionConnectionUse)
	if err != nil {
		return credentialmodule.RuntimeCredentialReference{}, safeRuntimeCredentialAuthorityError(ctx, err)
	}
	if !allowed {
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeForbidden
	}
	if err := authority.revalidator.Revalidate(ctx, cloneRefreshCredentialAuthority(authority.queued)); err != nil {
		if ctx.Err() != nil {
			return credentialmodule.RuntimeCredentialReference{}, ctx.Err()
		}
		if errors.Is(err, jobs.ErrAuthorityInvalid) || errors.Is(err, access.ErrForbidden) {
			return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeForbidden
		}
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeUnavailable
	}
	if err := ctx.Err(); err != nil {
		return credentialmodule.RuntimeCredentialReference{}, err
	}
	return resolveCommittedRuntimeCredential(ctx, identity, resource, authority.evidence, authority.owners, authority.bindings)
}

func cloneRefreshCredentialAuthority(authority jobs.AuthorityEnvelope) jobs.AuthorityEnvelope {
	authority.Permissions = slices.Clone(authority.Permissions)
	if authority.Credential != nil {
		value := *authority.Credential
		authority.Credential = &value
	}
	if authority.ExecutionGrant != nil {
		value := *authority.ExecutionGrant
		authority.ExecutionGrant = &value
	}
	return authority
}
