package module

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/flidai/leapview/pkg/permissions"
)

// captureAuthority binds a native refresh invocation to the credential that
// initiated it. The queue transport's identity is not authority: only the
// request credential's durable ID, fingerprint, expiry, and exact pipeline
// permission are persisted. Scheduler/delegated producers remain unsupported
// until an explicit grant verifier is composed.
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
