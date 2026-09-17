package module

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/flidai/leapview/pkg/permissions"
)

// ExecutionGrantReader is the live, read-only authority port used by
// scheduled workload capture. Implementations must resolve the current row;
// a historical Get-by-ID lookup is not sufficient for queue admission.
type ExecutionGrantReader interface {
	CurrentExecutionGrant(context.Context, string, string) (access.ExecutionGrant, error)
}

// DelegatedWorkloadAuthorityService constructs queue authority from one
// current execution grant. It deliberately accepts a grant ID, not a grant
// document: all issuer, recipient, target, permission, expiry, and closure
// evidence comes from the live access repository.
type DelegatedWorkloadAuthorityService struct {
	grants   ExecutionGrantReader
	instance string
	now      func() time.Time
}

// NewDelegatedWorkloadAuthorityService requires the native current-grant
// reader and the process-bound instance identity. No scheduler or worker
// identity is used as product authority.
func NewDelegatedWorkloadAuthorityService(grants ExecutionGrantReader, instanceID string) (*DelegatedWorkloadAuthorityService, error) {
	if grants == nil {
		return nil, errors.New("current execution grant reader is required")
	}
	if strings.TrimSpace(instanceID) == "" || strings.TrimSpace(instanceID) != instanceID {
		return nil, errors.New("execution grant instance identity is required")
	}
	return &DelegatedWorkloadAuthorityService{grants: grants, instance: instanceID, now: func() time.Time { return time.Now().UTC() }}, nil
}

// Capture reads a current grant and projects only its exact executable
// authority into the jobs envelope. The execution principal is obtained from
// the current grant row itself; the caller cannot provide a stale recipient.
func (s *DelegatedWorkloadAuthorityService) Capture(ctx context.Context, grantID string, identity projectgraph.ServingIdentity, pipelineID projectgraph.ResourceID) (jobs.AuthorityEnvelope, error) {
	if s == nil || s.grants == nil {
		return jobs.AuthorityEnvelope{}, errors.New("delegated workload authority service is unavailable")
	}
	if strings.TrimSpace(grantID) == "" || strings.TrimSpace(grantID) != grantID {
		return jobs.AuthorityEnvelope{}, errors.New("scheduled execution grant id is required")
	}
	if err := identity.Validate(); err != nil {
		return jobs.AuthorityEnvelope{}, err
	}
	if err := pipelineID.Validate(); err != nil {
		return jobs.AuthorityEnvelope{}, err
	}
	grant, err := s.grants.CurrentExecutionGrant(ctx, grantID, "")
	if err != nil {
		return jobs.AuthorityEnvelope{}, fmt.Errorf("resolve current execution grant: %w", err)
	}
	if grant.ID != grantID || grant.Profile != access.DurableGrantProfile {
		return jobs.AuthorityEnvelope{}, errors.New("current execution grant identity is invalid")
	}
	if grant.Target.InstanceID != s.instance || grant.Target.ProjectID != identity.ProjectID || grant.Target.ResourceID != pipelineID || grant.Target.ResourceKind != projectgraph.KindPipeline {
		return jobs.AuthorityEnvelope{}, errors.New("current execution grant target does not match scheduled pipeline")
	}
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now().UTC()
	}
	if grant.ExpiresAt.IsZero() || !grant.ExpiresAt.After(now) || !grant.RevokedAt.IsZero() {
		return jobs.AuthorityEnvelope{}, errors.New("current execution grant is not active")
	}
	if err := grant.Target.Validate(); err != nil {
		return jobs.AuthorityEnvelope{}, fmt.Errorf("current execution grant target: %w", err)
	}
	if err := grant.Issuer.Validate(); err != nil {
		return jobs.AuthorityEnvelope{}, fmt.Errorf("current execution grant issuer: %w", err)
	}
	if len(grant.Permissions) == 0 {
		return jobs.AuthorityEnvelope{}, jobs.ErrAuthorityNoPermissions
	}
	if err := access.ValidatePermissionPairs(grant.Permissions); err != nil {
		return jobs.AuthorityEnvelope{}, fmt.Errorf("current execution grant permissions: %w", err)
	}
	hasRun := false
	for _, pair := range grant.Permissions {
		if pair.Target.Scope != access.PermissionScopeResource || pair.Target.ProjectID != grant.Target.ProjectID || pair.Target.ResourceKind == "" || pair.Target.ResourceID == "" || pair.Target.IncludeFuture {
			return jobs.AuthorityEnvelope{}, errors.New("current execution grant permission target is not exact")
		}
		if pair.Action == access.ActionPipelineRun && pair.Target.ResourceKind == projectgraph.KindPipeline && pair.Target.ResourceID == pipelineID {
			hasRun = true
		}
	}
	if !hasRun {
		return jobs.AuthorityEnvelope{}, access.ErrGrantMissingExecutionPair
	}
	pairs := make([]permissions.Pair, len(grant.Permissions))
	for index, pair := range grant.Permissions {
		converted, convertErr := access.ToContractPermissionPair(pair)
		if convertErr != nil {
			return jobs.AuthorityEnvelope{}, fmt.Errorf("current execution grant permission %d: %w", index, convertErr)
		}
		pairs[index] = converted
	}
	authority := jobs.AuthorityEnvelope{
		Profile:              jobs.AuthorityEnvelopeProfile,
		Mode:                 jobs.DelegatedWorkloadMode,
		ActorPrincipalID:     grant.Issuer.PrincipalID,
		ExecutionPrincipalID: grant.ExecutionPrincipalID,
		Target: jobs.AuthorityTarget{
			InstanceID: s.instance, ProjectID: identity.ProjectID.String(), Environment: identity.Environment,
			ResourceKind: string(projectgraph.KindPipeline), ResourceID: pipelineID.String(), ResourceUID: grant.Target.ResourceUID,
		},
		Permissions: pairs,
		ExecutionGrant: &jobs.ExecutionGrantEvidence{
			ID: grant.ID, Fingerprint: grant.Fingerprint, ExpiresAt: grant.ExpiresAt.UTC(),
			WorkflowID: grant.WorkflowID, WorkflowRevision: grant.WorkflowRevision, ClosureDigest: grant.ClosureDigest,
			BindingDigest: grant.BindingDigest, DestinationDigest: grant.DestinationDigest, TriggerDigest: grant.TriggerDigest,
		},
	}
	if err := authority.Validate(); err != nil {
		return jobs.AuthorityEnvelope{}, fmt.Errorf("construct delegated workload authority: %w", err)
	}
	return authority, nil
}

// captureAuthority binds a native refresh invocation to the credential that
// initiated it. The queue transport's identity is not authority: only the
// request credential's durable ID, fingerprint, expiry, and exact pipeline
// permission are persisted. Scheduled delegated producers use the explicit
// DelegatedWorkloadAuthorityService below; no scheduler identity is promoted
// into product authority.
func (m *Module) captureAuthority(ctx context.Context, identity projectgraph.ServingIdentity, pipelineID projectgraph.ResourceID, principalID string) (jobs.AuthorityEnvelope, error) {
	if m == nil || !m.service.RequireAuthority {
		return jobs.AuthorityEnvelope{}, nil
	}
	var credential access.APICredential
	apiOK := false
	if m.currentCredential != nil {
		credential, apiOK = m.currentCredential(ctx)
	}
	class := jobs.CredentialClassAPIToken
	credentialID := credential.Token.ID
	credentialFingerprint := credential.Token.TokenFingerprint
	expiresAtValue := credential.Token.ExpiresAt
	principalForCredential := credential.Token.PrincipalID
	if !apiOK {
		if m.currentSessionEvidence == nil {
			return jobs.AuthorityEnvelope{}, access.ErrForbidden
		}
		evidence, sessionOK := m.currentSessionEvidence(ctx)
		if !sessionOK {
			return jobs.AuthorityEnvelope{}, access.ErrForbidden
		}
		class = jobs.CredentialClassSession
		credentialID = evidence.ID
		credentialFingerprint = evidence.Fingerprint
		expiresAtValue = evidence.ExpiresAt.Format(time.RFC3339Nano)
		principalForCredential = evidence.PrincipalID
	} else if credential.Authoring != nil {
		return jobs.AuthorityEnvelope{}, access.ErrForbidden
	}
	if strings.TrimSpace(credentialID) == "" || strings.TrimSpace(credentialFingerprint) == "" {
		return jobs.AuthorityEnvelope{}, access.ErrForbidden
	}
	if principalForCredential != principalID {
		return jobs.AuthorityEnvelope{}, access.ErrForbidden
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expiresAtValue)
	if err != nil || !expiresAt.After(time.Now().UTC()) {
		return jobs.AuthorityEnvelope{}, access.ErrForbidden
	}
	resource, err := access.NewResourceRef(pipelineID, projectgraph.KindPipeline)
	if err != nil {
		return jobs.AuthorityEnvelope{}, err
	}
	requirement, err := refreshRunTypedOperationRequirement()
	if err != nil {
		return jobs.AuthorityEnvelope{}, fmt.Errorf("refresh typed operation requirement: %w", err)
	}
	pair, err := requirement.PermissionPair(identity.ProjectID, resource)
	if err != nil {
		return jobs.AuthorityEnvelope{}, err
	}
	if class == jobs.CredentialClassAPIToken {
		// Only a typed credential may initiate a protected refresh. Legacy
		// capability rows have no action/resource audience and must not be
		// converted into a newly durable pipeline.run authority pair.
		if credential.Token.PermissionProfile != access.PermissionCatalogProfile || !access.PermissionSetAllows(credential.Token.Permissions, pair) {
			return jobs.AuthorityEnvelope{}, access.ErrForbidden
		}
	}
	contractPair, err := access.ToContractPermissionPair(pair)
	if err != nil {
		return jobs.AuthorityEnvelope{}, err
	}
	authority := jobs.AuthorityEnvelope{
		Profile: jobs.AuthorityEnvelopeProfile, Mode: jobs.CallerAuthorityMode,
		ActorPrincipalID: principalID, ExecutionPrincipalID: principalID,
		Credential:  &jobs.CredentialEvidence{Class: class, ID: credentialID, Fingerprint: credentialFingerprint, ExpiresAt: expiresAt.UTC()},
		Target:      jobs.AuthorityTarget{ProjectID: identity.ProjectID.String(), Environment: identity.Environment, ResourceKind: string(projectgraph.KindPipeline), ResourceID: pipelineID.String()},
		Permissions: []permissions.Pair{contractPair},
	}
	if err := authority.Validate(); err != nil {
		return jobs.AuthorityEnvelope{}, fmt.Errorf("capture refresh authority: %w", err)
	}
	return authority, nil
}

// refreshRunTypedOperationRequirement loads the generated operation contract
// used by both the API command and browser manual-refresh capture. Keeping the
// generated action/resolver pair at this boundary prevents the queue path
// from drifting into a handwritten action mapping.
func refreshRunTypedOperationRequirement() (access.TypedOperationRequirement, error) {
	contract, ok := refreshgen.GetAPIGenOperationContracts()[CreateRefreshRunOperationID]
	if !ok || contract.Authz == nil {
		return access.TypedOperationRequirement{}, fmt.Errorf("generated operation %q authz contract is unavailable", CreateRefreshRunOperationID)
	}
	return access.NewTypedOperationRequirementService().Requirement(access.Action(contract.Authz.Action), contract.Authz.Resolver)
}

// CreateRefreshRunTypedOperationRequirement exposes the generated manual
// refresh requirement to the durable dequeue adapter. Both producers and the
// dequeue revalidator therefore consume the same APIGen operation contract.
func CreateRefreshRunTypedOperationRequirement() (access.TypedOperationRequirement, error) {
	return refreshRunTypedOperationRequirement()
}
