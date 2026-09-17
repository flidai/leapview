package app

import (
	"context"
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

// Ensure a malformed callback cannot accidentally become a permissive
// revalidator when composed by a caller outside native PostgreSQL startup.
var _ jobs.AuthorityRevalidator = callerAuthorityRevalidator{}
