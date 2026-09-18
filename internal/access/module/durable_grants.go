package module

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
)

// durableGrantService constructs the trusted share service for one
// authenticated request. The request is captured only to obtain server-side
// principal and credential evidence; no authority-bearing value is accepted
// from the request body.
func (m *Module) durableGrantService(r *http.Request) (*access.DurableGrantService, error) {
	if m == nil || r == nil {
		return nil, access.ErrGrantAuthorityUnavailable
	}
	repository := m.repositoryValue()
	writer, ok := repository.(access.DurableGrantWriter)
	if !ok {
		return nil, errors.New("access repository does not support durable grant mutations")
	}
	return access.NewDurableGrantService(writer, access.CurrentAuthorityResolverFunc(func(ctx context.Context, request access.CurrentAuthorityRequest) (access.CurrentAuthoritySnapshot, error) {
		return m.resolveCurrentGrantAuthority(ctx, r, request)
	}))
}

func (m *Module) resolveCurrentGrantAuthority(ctx context.Context, r *http.Request, request access.CurrentAuthorityRequest) (access.CurrentAuthoritySnapshot, error) {
	principal, ok := m.CurrentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return access.CurrentAuthoritySnapshot{}, access.ErrGrantAuthorityUnavailable
	}
	if request.Target.ProjectID != "" {
		if m.currentProjectID == nil {
			return access.CurrentAuthoritySnapshot{}, errors.New("active project identity is unavailable")
		}
		activeProject, err := m.CurrentProjectID(ctx)
		if err != nil || activeProject != request.Target.ProjectID {
			return access.CurrentAuthoritySnapshot{}, fmt.Errorf("durable grant target is outside the active project")
		}
	}
	permissions, err := m.CurrentEffectivePermissionOptions(ctx, principal.ID)
	if err != nil {
		return access.CurrentAuthoritySnapshot{}, fmt.Errorf("resolve current typed authority: %w", err)
	}
	subjects, err := m.AuthorizationSubjects(ctx, principal.ID)
	if err != nil {
		return access.CurrentAuthoritySnapshot{}, fmt.Errorf("resolve current grant subjects: %w", err)
	}
	var groups []access.SubjectRef
	if len(subjects) > 1 {
		groups = append([]access.SubjectRef(nil), subjects[1:]...)
	}
	credential, ok := m.CurrentCredentialEvidence(r)
	if !ok {
		return access.CurrentAuthoritySnapshot{}, access.ErrGrantCredentialInvalid
	}
	var credentialPermissions []access.PermissionPair
	if current, hasCredential := m.requestCredential(r); hasCredential && current.Token.ID != "" {
		if current.Token.Permissions == nil {
			return access.CurrentAuthoritySnapshot{}, access.ErrTokenPermissionAttenuationNeeded
		}
		credentialPermissions = access.ClonePermissionPairs(current.Token.Permissions)
	}
	return access.CurrentAuthoritySnapshot{
		Principal: access.Principal{ID: principal.ID, Kind: principal.Kind, Email: principal.Email, DisplayName: principal.DisplayName, CreatedAt: principal.CreatedAt, UpdatedAt: principal.UpdatedAt},
		Groups:    groups, Permissions: access.ClonePermissionPairs(permissions), Credential: credential,
		CredentialPermissions: credentialPermissions,
	}, nil
}
