package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	"github.com/flidai/leapview/pkg/jobs"
)

// callerAuthorityRevalidator is the native PostgreSQL live check for caller
// authority. The queue envelope is immutable evidence; this check re-reads
// credential lifecycle and the active project authority immediately before
// admission and dispatch.
type callerAuthorityRevalidator struct {
	tokens         access.APITokenAuthorityEvidenceReader
	sessions       access.SessionAuthorityEvidenceReader
	current        func(context.Context, string, projectgraph.ResourceID, string, access.ResourceRef, access.Capability) (bool, error)
	requirement    access.TypedOperationRequirement
	requirementErr error
}

// executionGrantAuthorityReader is the narrow live authority port required
// by delegated workload mode. In particular, it does not expose issuance,
// credential, or ambient service-principal authority.
type executionGrantAuthorityReader interface {
	CurrentExecutionGrant(context.Context, string, string) (access.ExecutionGrant, error)
}

type delegatedWorkloadRevalidator struct {
	grants      executionGrantAuthorityReader
	current     func(context.Context, string, access.PermissionPair, string) (bool, error)
	instanceID  string
	environment string
}

// newDelegatedWorkloadRevalidator constructs the live execution-grant check.
// The instance and environment bind the transport envelope to this serving
// process; neither is accepted from a queue credential or worker identity.
func newDelegatedWorkloadRevalidator(grants executionGrantAuthorityReader, current func(context.Context, string, access.PermissionPair, string) (bool, error), instanceID, environment string) jobs.AuthorityRevalidator {
	return delegatedWorkloadRevalidator{grants: grants, current: current, instanceID: instanceID, environment: environment}
}

// authorityRevalidator dispatches between the two mutually exclusive product
// authority modes. A caller credential is never consulted for delegated work.
type authorityRevalidator struct {
	caller    callerAuthorityRevalidator
	delegated delegatedWorkloadRevalidator
}

func newAuthorityRevalidator(tokens access.APITokenAuthorityEvidenceReader, sessions access.SessionAuthorityEvidenceReader, grants executionGrantAuthorityReader, current func(context.Context, string, projectgraph.ResourceID, string, access.ResourceRef, access.Capability) (bool, error), delegatedCurrent func(context.Context, string, access.PermissionPair, string) (bool, error), instanceID, environment string) jobs.AuthorityRevalidator {
	requirement, requirementErr := refreshmodule.CreateRefreshRunTypedOperationRequirement()
	return authorityRevalidator{
		caller:    callerAuthorityRevalidator{tokens: tokens, sessions: sessions, current: current, requirement: requirement, requirementErr: requirementErr},
		delegated: delegatedWorkloadRevalidator{grants: grants, current: delegatedCurrent, instanceID: instanceID, environment: environment},
	}
}

func (r authorityRevalidator) Revalidate(ctx context.Context, authority jobs.AuthorityEnvelope) error {
	switch authority.Mode {
	case jobs.CallerAuthorityMode:
		return r.caller.Revalidate(ctx, authority)
	case jobs.DelegatedWorkloadMode:
		return r.delegated.Revalidate(ctx, authority)
	default:
		return fmt.Errorf("%w: unsupported authority mode", jobs.ErrAuthorityInvalid)
	}
}

func newCallerAuthorityRevalidator(tokens access.APITokenAuthorityEvidenceReader, sessions access.SessionAuthorityEvidenceReader, current func(context.Context, string, projectgraph.ResourceID, string, access.ResourceRef, access.Capability) (bool, error)) jobs.AuthorityRevalidator {
	requirement, requirementErr := refreshmodule.CreateRefreshRunTypedOperationRequirement()
	return callerAuthorityRevalidator{tokens: tokens, sessions: sessions, current: current, requirement: requirement, requirementErr: requirementErr}
}

func (r callerAuthorityRevalidator) Revalidate(ctx context.Context, authority jobs.AuthorityEnvelope) error {
	if authority.Mode != jobs.CallerAuthorityMode || authority.Credential == nil {
		return fmt.Errorf("%w: delegated execution authority is not configured", jobs.ErrAuthorityRevalidator)
	}
	if err := authority.Validate(); err != nil {
		return err
	}
	if r.current == nil {
		return jobs.ErrAuthorityRevalidator
	}
	if r.requirementErr != nil {
		return fmt.Errorf("%w: typed refresh operation requirement: %v", jobs.ErrAuthorityInvalid, r.requirementErr)
	}
	permissionPairs := make([]access.PermissionPair, len(authority.Permissions))
	for index, pair := range authority.Permissions {
		converted, err := access.FromContractPermissionPair(pair)
		if err != nil {
			return fmt.Errorf("%w: permission %d is not in the active product catalog: %v", jobs.ErrAuthorityInvalid, index, err)
		}
		permissionPairs[index] = converted
	}
	projectID, err := projectgraph.NewResourceID(authority.Target.ProjectID)
	if err != nil {
		return fmt.Errorf("%w: authority project: %v", jobs.ErrAuthorityInvalid, err)
	}
	targetResourceID, err := projectgraph.NewResourceID(authority.Target.ResourceID)
	if err != nil {
		return fmt.Errorf("%w: authority resource id: %v", jobs.ErrAuthorityInvalid, err)
	}
	targetResource, err := access.NewResourceRef(targetResourceID, projectgraph.Kind(authority.Target.ResourceKind))
	if err != nil {
		return fmt.Errorf("%w: authority target resource: %v", jobs.ErrAuthorityInvalid, err)
	}
	expectedPairs, err := r.requirement.ResolvePairs(projectID, targetResource)
	if err != nil {
		return fmt.Errorf("%w: typed refresh operation requirement: %v", jobs.ErrAuthorityInvalid, err)
	}
	if len(permissionPairs) != len(expectedPairs) {
		return fmt.Errorf("%w: typed refresh operation permission set changed", jobs.ErrAuthorityInvalid)
	}
	expectedSet := make(map[access.PermissionPair]struct{}, len(expectedPairs))
	for _, pair := range expectedPairs {
		expectedSet[pair] = struct{}{}
	}
	for _, pair := range permissionPairs {
		if _, ok := expectedSet[pair]; !ok {
			return fmt.Errorf("%w: typed refresh operation permission set changed", jobs.ErrAuthorityInvalid)
		}
		delete(expectedSet, pair)
	}
	now := time.Now().UTC()
	switch authority.Credential.Class {
	case jobs.CredentialClassSession:
		if r.sessions == nil {
			return jobs.ErrAuthorityRevalidator
		}
		session, err := r.sessions.SessionAuthorityEvidence(ctx, authority.ActorPrincipalID, authority.Credential.ID, authority.Credential.Fingerprint, now)
		if err != nil {
			return fmt.Errorf("%w: session evidence: %v", jobs.ErrAuthorityInvalid, err)
		}
		if session.ID != authority.Credential.ID || session.PrincipalID != authority.ActorPrincipalID || session.TokenFingerprint != authority.Credential.Fingerprint {
			return fmt.Errorf("%w: session evidence changed", jobs.ErrAuthorityInvalid)
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, session.ExpiresAt)
		if err != nil || !expiresAt.Equal(authority.Credential.ExpiresAt.UTC()) || !expiresAt.After(now) {
			return fmt.Errorf("%w: session expiry changed", jobs.ErrAuthorityInvalid)
		}
	case jobs.CredentialClassAPIToken:
		if r.tokens == nil {
			return jobs.ErrAuthorityRevalidator
		}
		token, err := r.tokens.APITokenAuthorityEvidence(ctx, authority.ActorPrincipalID, authority.Credential.ID, now)
		if err != nil {
			return fmt.Errorf("%w: credential evidence: %v", jobs.ErrAuthorityInvalid, err)
		}
		if token.ID != authority.Credential.ID || token.PrincipalID != authority.ActorPrincipalID || token.TokenFingerprint == "" || token.TokenFingerprint != authority.Credential.Fingerprint {
			return fmt.Errorf("%w: credential evidence changed", jobs.ErrAuthorityInvalid)
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, token.ExpiresAt)
		if err != nil || !expiresAt.Equal(authority.Credential.ExpiresAt.UTC()) || !expiresAt.After(now) {
			return fmt.Errorf("%w: credential expiry changed", jobs.ErrAuthorityInvalid)
		}
		if token.PermissionProfile == access.PermissionCatalogProfile {
			if err := access.ValidatePermissionPairs(token.Permissions); err != nil {
				return fmt.Errorf("%w: token permissions: %v", jobs.ErrAuthorityInvalid, err)
			}
			for _, pair := range permissionPairs {
				if !access.PermissionSetAllows(token.Permissions, pair) {
					return fmt.Errorf("%w: token permission ceiling changed", jobs.ErrAuthorityInvalid)
				}
			}
		} else {
			return fmt.Errorf("%w: legacy token permission ceiling changed", jobs.ErrAuthorityInvalid)
		}
	default:
		return fmt.Errorf("%w: unsupported caller credential class", jobs.ErrAuthorityInvalid)
	}
	for _, pair := range permissionPairs {
		if pair.Target.Scope != access.PermissionScopeResource || pair.Target.ProjectID.String() != authority.Target.ProjectID || string(pair.Target.ResourceKind) != authority.Target.ResourceKind || pair.Target.ResourceID.String() != authority.Target.ResourceID || pair.Target.ResourceKind == "" || pair.Target.ResourceID == "" {
			return fmt.Errorf("%w: authority target does not bind an exact project resource", jobs.ErrAuthorityInvalid)
		}
		resource, err := access.NewResourceRef(pair.Target.ResourceID, pair.Target.ResourceKind)
		if err != nil {
			return fmt.Errorf("%w: authority resource: %v", jobs.ErrAuthorityInvalid, err)
		}
		if err := r.requirement.ValidateResource(resource); err != nil {
			return fmt.Errorf("%w: typed refresh operation requirement: %v", jobs.ErrAuthorityInvalid, err)
		}
		allowed, err := r.current(ctx, authority.ActorPrincipalID, pair.Target.ProjectID, authority.Target.Environment, resource, access.CapabilityResourceUse)
		if err != nil {
			return fmt.Errorf("%w: current authority: %v", jobs.ErrAuthorityInvalid, err)
		}
		if !allowed {
			return fmt.Errorf("%w: current exact resource authority is unavailable", jobs.ErrAuthorityInvalid)
		}
	}
	return nil
}

func (r delegatedWorkloadRevalidator) Revalidate(ctx context.Context, authority jobs.AuthorityEnvelope) error {
	if authority.Mode != jobs.DelegatedWorkloadMode {
		return fmt.Errorf("%w: delegated workload authority is not configured", jobs.ErrAuthorityRevalidator)
	}
	if err := authority.Validate(); err != nil {
		return err
	}
	if len(authority.Permissions) == 0 {
		return jobs.ErrAuthorityNoPermissions
	}
	if r.grants == nil || r.current == nil {
		return jobs.ErrAuthorityRevalidator
	}
	if r.instanceID != "" && authority.Target.InstanceID != r.instanceID {
		return fmt.Errorf("%w: delegated target instance changed", jobs.ErrAuthorityInvalid)
	}
	if r.environment != "" && authority.Target.Environment != r.environment {
		return fmt.Errorf("%w: delegated target environment changed", jobs.ErrAuthorityInvalid)
	}
	if authority.Target.InstanceID == "" || authority.Target.Environment == "" || authority.Target.ResourceUID == "" || authority.Target.ResourceKind != string(projectgraph.KindPipeline) {
		return fmt.Errorf("%w: delegated authority requires an exact pipeline target", jobs.ErrAuthorityInvalid)
	}

	evidence := authority.ExecutionGrant
	grant, err := r.grants.CurrentExecutionGrant(ctx, evidence.ID, authority.ExecutionPrincipalID)
	if err != nil {
		return fmt.Errorf("%w: current execution grant: %v", jobs.ErrAuthorityInvalid, err)
	}
	if err := validateDelegatedGrantEvidence(authority, grant); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrAuthorityInvalid, err)
	}
	if err := validateDelegatedPermissionSet(authority, grant); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrAuthorityInvalid, err)
	}
	for index, pair := range grant.Permissions {
		allowed, currentErr := r.current(ctx, authority.ExecutionPrincipalID, pair, authority.Target.Environment)
		if currentErr != nil {
			return fmt.Errorf("%w: current workload permission %d: %v", jobs.ErrAuthorityInvalid, index, currentErr)
		}
		if !allowed {
			return fmt.Errorf("%w: current workload permission %d is unavailable", jobs.ErrAuthorityInvalid, index)
		}
	}
	return nil
}

func validateDelegatedGrantEvidence(authority jobs.AuthorityEnvelope, grant access.ExecutionGrant) error {
	if grant.ID != authority.ExecutionGrant.ID || grant.Fingerprint != authority.ExecutionGrant.Fingerprint {
		return errors.New("execution grant identity changed")
	}
	now := time.Now().UTC()
	if grant.ExpiresAt.IsZero() || !grant.ExpiresAt.Equal(authority.ExecutionGrant.ExpiresAt.UTC()) || !grant.ExpiresAt.After(now) {
		return errors.New("execution grant expiry changed")
	}
	if !grant.RevokedAt.IsZero() {
		return errors.New("execution grant is revoked")
	}
	if grant.Profile != access.DurableGrantProfile {
		return errors.New("execution grant profile changed")
	}
	if err := grant.Target.Validate(); err != nil {
		return fmt.Errorf("execution grant target: %v", err)
	}
	if err := grant.Issuer.Validate(); err != nil {
		return fmt.Errorf("execution grant issuer: %v", err)
	}
	// ActorPrincipalID is the principal that issued the durable grant, while
	// ExecutionPrincipalID is the workload recipient. A schedule/trigger
	// identity is not an ambient authority substitute; trigger evidence is only
	// an immutable closure digest and does not grant access to trigger outputs.
	if grant.Issuer.PrincipalID != authority.ActorPrincipalID {
		return errors.New("execution grant actor principal changed")
	}
	if grant.ExecutionPrincipalID != authority.ExecutionPrincipalID {
		return errors.New("execution grant execution principal changed")
	}
	if grant.Target.InstanceID != authority.Target.InstanceID || grant.Target.ProjectID.String() != authority.Target.ProjectID || grant.Target.ResourceUID != authority.Target.ResourceUID || grant.Target.ResourceID.String() != authority.Target.ResourceID || string(grant.Target.ResourceKind) != authority.Target.ResourceKind {
		return errors.New("execution grant target changed")
	}
	if grant.Target.ResourceKind != projectgraph.KindPipeline {
		return errors.New("execution grant target is not a pipeline")
	}
	if grant.WorkflowID != authority.ExecutionGrant.WorkflowID || grant.WorkflowRevision != authority.ExecutionGrant.WorkflowRevision || grant.ClosureDigest != authority.ExecutionGrant.ClosureDigest || grant.BindingDigest != authority.ExecutionGrant.BindingDigest || grant.DestinationDigest != authority.ExecutionGrant.DestinationDigest || grant.TriggerDigest != authority.ExecutionGrant.TriggerDigest {
		return errors.New("execution grant immutable closure evidence changed")
	}
	return nil
}

func validateDelegatedPermissionSet(authority jobs.AuthorityEnvelope, grant access.ExecutionGrant) error {
	grantSet, err := delegatedPermissionSet(grant.Permissions, grant.Target)
	if err != nil {
		return fmt.Errorf("persisted execution permissions: %v", err)
	}
	authorityPairs := make([]access.PermissionPair, len(authority.Permissions))
	for index, pair := range authority.Permissions {
		converted, err := access.FromContractPermissionPair(pair)
		if err != nil {
			return fmt.Errorf("permission %d is invalid: %v", index, err)
		}
		authorityPairs[index] = converted
	}
	authoritySet, err := delegatedPermissionSet(authorityPairs, grant.Target)
	if err != nil {
		return fmt.Errorf("authority permissions: %v", err)
	}
	if len(grantSet) != len(authoritySet) {
		return errors.New("execution permission set changed")
	}
	for key := range grantSet {
		if _, ok := authoritySet[key]; !ok {
			return errors.New("execution permission set changed")
		}
	}
	return nil
}

func delegatedPermissionSet(pairs []access.PermissionPair, target access.DurableGrantTarget) (map[string]struct{}, error) {
	if err := access.ValidatePermissionPairs(pairs); err != nil {
		return nil, err
	}
	result := make(map[string]struct{}, len(pairs))
	hasRun := false
	for _, pair := range pairs {
		if pair.Target.Scope != access.PermissionScopeResource || pair.Target.ProjectID != target.ProjectID || pair.Target.IncludeFuture || pair.Target.ResourceKind == "" || pair.Target.ResourceID == "" {
			return nil, errors.New("execution permissions must be exact resources in the target project")
		}
		key := pair.Key()
		if _, duplicate := result[key]; duplicate {
			return nil, errors.New("execution permissions contain a duplicate pair")
		}
		result[key] = struct{}{}
		if pair.Action == access.ActionPipelineRun && pair.Target.ResourceKind == projectgraph.KindPipeline && pair.Target.ResourceID == target.ResourceID {
			hasRun = true
		}
	}
	if !hasRun {
		return nil, errors.New("execution permissions do not contain the exact pipeline.run pair")
	}
	return result, nil
}

// Ensure a malformed callback cannot accidentally become a permissive
// revalidator when composed by a caller outside native PostgreSQL startup.
var _ jobs.AuthorityRevalidator = callerAuthorityRevalidator{}
var _ jobs.AuthorityRevalidator = delegatedWorkloadRevalidator{}
var _ jobs.AuthorityRevalidator = authorityRevalidator{}
