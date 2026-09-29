package module

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/graph"
)

// WorkloadCredentialLease is short-lived evidence derived from LeapView's
// canonical workload-identity exchange. Revoke should be called after the
// bounded offline operation has persisted its approval records.
type WorkloadCredentialLease struct {
	evidence access.CredentialEvidence
	revoke   func(context.Context) error
	once     sync.Once
	err      error
}

func (l *WorkloadCredentialLease) Evidence() access.CredentialEvidence {
	if l == nil {
		return access.CredentialEvidence{}
	}
	return l.evidence
}

func (l *WorkloadCredentialLease) Revoke(ctx context.Context) error {
	if l == nil || l.revoke == nil {
		return nil
	}
	l.once.Do(func() { l.err = l.revoke(ctx) })
	return l.err
}

// AcquireWorkloadCredential performs the same credential exchange and exact
// scope authentication used by authoring clients, but returns only non-secret
// approval evidence. It is intended for offline deployment workflows that
// hold an independently supplied service-principal secret; callers must not
// persist the returned access token.
func (m *Module) AcquireWorkloadCredential(
	ctx context.Context,
	clientID, clientSecret, targetID, projectID string,
	permissions []access.PermissionPair,
	lifetime time.Duration,
) (*WorkloadCredentialLease, error) {
	if m == nil || m.authoringAuth == nil || strings.TrimSpace(clientID) == "" || clientID != strings.TrimSpace(clientID) || clientSecret == "" || strings.TrimSpace(targetID) == "" || strings.TrimSpace(projectID) == "" || len(permissions) == 0 {
		return nil, errors.New("offline workload credential inputs are incomplete")
	}
	project, err := graph.NewResourceID(projectID)
	if err != nil {
		return nil, err
	}
	scope, err := access.NewAuthoringScope(targetID, project, permissions)
	if err != nil {
		return nil, err
	}
	tokens, err := m.authoringAuth.ExchangeWorkloadIdentity(ctx, access.WorkloadIdentityInput{
		ClientID: clientID, ClientSecret: clientSecret, Scope: scope, Lifetime: lifetime,
	})
	if err != nil {
		return nil, err
	}
	revoke := func(revokeCtx context.Context) error {
		return m.authoringAuth.RevokeAccessToken(revokeCtx, tokens.AccessToken)
	}
	credential, err := m.authoringAuth.Authenticate(ctx, tokens.AccessToken, targetID, projectID, permissions)
	if err != nil {
		_ = revoke(context.Background())
		return nil, err
	}
	if credential.Principal.ID != tokens.Session.PrincipalID || credential.Session.ID != tokens.Session.ID || credential.ID == "" || credential.Session.Kind != access.AuthoringSessionWorkload {
		_ = revoke(context.Background())
		return nil, access.ErrInvalidAuthoringCredential
	}
	return &WorkloadCredentialLease{
		evidence: access.CredentialEvidence{
			Class: "workload", ID: credential.ID, PrincipalID: credential.Principal.ID,
			ExpiresAt: credential.AccessExpiresAt.UTC(),
		},
		revoke: revoke,
	}, nil
}
