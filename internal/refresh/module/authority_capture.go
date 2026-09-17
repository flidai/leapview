package module

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/jobs"
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
	pair, err := access.NewExactPermissionPair(access.ActionPipelineRun, identity.ProjectID, resource)
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
	authority := jobs.AuthorityEnvelope{
		Profile: jobs.AuthorityEnvelopeProfile, Mode: jobs.CallerAuthorityMode,
		ActorPrincipalID: principalID, ExecutionPrincipalID: principalID,
		Credential:  &jobs.CredentialEvidence{Class: class, ID: credentialID, Fingerprint: credentialFingerprint, ExpiresAt: expiresAt.UTC()},
		Target:      jobs.AuthorityTarget{ProjectID: identity.ProjectID.String(), Environment: identity.Environment, ResourceKind: string(projectgraph.KindPipeline), ResourceID: pipelineID.String()},
		Permissions: []access.PermissionPair{pair},
	}
	if err := authority.Validate(); err != nil {
		return jobs.AuthorityEnvelope{}, fmt.Errorf("capture refresh authority: %w", err)
	}
	return authority, nil
}
