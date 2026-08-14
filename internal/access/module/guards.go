package module

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/httpauth"
)

func (m *Module) Protect(privilege access.Privilege, handler http.HandlerFunc) http.HandlerFunc {
	return m.ProtectHandler(privilege, handler).ServeHTTP
}

func (m *Module) ProtectWithObjects(privilege access.Privilege, resolver func(*http.Request, string) []access.ObjectRef, handler http.HandlerFunc) http.HandlerFunc {
	return m.ProtectHandlerWithObjects(privilege, resolver, handler).ServeHTTP
}

func (m *Module) ProtectAnyWorkspace(privilege access.Privilege, handler http.HandlerFunc) http.HandlerFunc {
	return m.protectAnyWorkspace(privilege, handler).ServeHTTP
}

func (m *Module) ProtectGlobal(privilege access.Privilege, handler http.HandlerFunc) http.HandlerFunc {
	return m.protectAnyWorkspace(privilege, handler).ServeHTTP
}

func (m *Module) ProtectPlatform(privilege access.Privilege, handler http.HandlerFunc) http.HandlerFunc {
	return m.ProtectWithObjects(privilege, func(*http.Request, string) []access.ObjectRef {
		return []access.ObjectRef{access.PlatformObject()}
	}, handler)
}

func (m *Module) ProtectHandler(privilege access.Privilege, next http.Handler) http.Handler {
	return m.ProtectHandlerWithObjects(privilege, nil, next)
}

func (m *Module) ProtectNamed(privilege string, next http.Handler) http.Handler {
	return m.ProtectHandler(access.Privilege(privilege), next)
}

func (m *Module) ProtectGlobalNamed(privilege string, next http.Handler) http.Handler {
	return m.protectAnyWorkspace(access.Privilege(privilege), next)
}

func (m *Module) ProtectPlatformNamed(privilege string, next http.Handler) http.Handler {
	return m.ProtectHandlerWithObjects(access.Privilege(privilege), func(*http.Request, string) []access.ObjectRef {
		return []access.ObjectRef{access.PlatformObject()}
	}, next)
}

func (m *Module) ProtectAnyWorkspaceNamed(privilege string, next http.Handler) http.Handler {
	return m.protectAnyWorkspace(access.Privilege(privilege), next)
}

func (m *Module) ProtectViewItem(handler http.HandlerFunc) http.HandlerFunc {
	return m.Protect(access.PrivilegeViewItem, handler)
}

func (m *Module) ProtectIngestData(next http.Handler) http.Handler {
	// TUS upload URLs identify an already-authorized upload session rather than
	// a workspace. Authorize the credential across its explicit scopes instead
	// of relying on a process-wide default workspace.
	return m.protectAnyWorkspace(access.PrivilegeIngestData, next)
}

func (m *Module) ProtectHandlerWithObjects(privilege access.Privilege, resolver func(*http.Request, string) []access.ObjectRef, next http.Handler) http.Handler {
	if m == nil || m.auth == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), LocalDeveloperPrincipal())))
		})
	}
	return m.auth.MiddlewareWithObjectResolver(privilege, httpauth.ObjectResolver(resolver), next)
}

func (m *Module) CSRFMiddleware(next http.Handler) http.Handler {
	if m == nil || m.auth == nil {
		return next
	}
	return m.auth.CSRFMiddleware(next)
}

func (m *Module) protectAnyWorkspace(privilege access.Privilege, next http.Handler) http.Handler {
	if m == nil || m.auth == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), LocalDeveloperPrincipal())))
		})
	}
	return m.auth.Middleware("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := m.auth.Principal(r)
		if !ok {
			writeAuthError(w, r, errUnauthorized, http.StatusUnauthorized)
			return
		}
		if principal.DevBypass {
			next.ServeHTTP(w, r)
			return
		}
		var credential *access.APICredential
		if resolved, ok := m.auth.APICredential(r); ok {
			if resolved.Authoring != nil {
				writeAuthError(w, r, errForbidden, http.StatusForbidden)
				return
			}
			credential = &resolved
		}
		allowed, err := m.authorizeAnyWorkspace(r.Context(), principal.ID, credential, privilege)
		if err != nil {
			writeAuthError(w, r, err, http.StatusInternalServerError)
			return
		}
		if !allowed {
			writeAuthError(w, r, errForbidden, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func (m *Module) authorizeAnyWorkspace(ctx context.Context, principalID string, credential *access.APICredential, privilege access.Privilege) (bool, error) {
	if m == nil || m.repository == nil {
		return false, nil
	}
	var workspaceIDs []string
	if m.workspaceIDs != nil {
		var err error
		workspaceIDs, err = m.workspaceIDs(ctx)
		if err != nil {
			return false, err
		}
	}
	repository, err := m.repository()
	if err != nil {
		return false, err
	}
	if repository == nil || strings.TrimSpace(principalID) == "" {
		return false, nil
	}
	objects := authorizationObjects(workspaceIDs, credential, privilege)
	if len(objects) == 0 {
		return false, nil
	}
	decision, err := repository.AuthorizeAny(ctx, principalID, privilege, objects)
	return decision.Allowed, err
}

func authorizationObjects(workspaceIDs []string, credential *access.APICredential, privilege access.Privilege) []access.ObjectRef {
	objects := make([]access.ObjectRef, 0, len(workspaceIDs)+1)
	if credential == nil || apiTokenAllows(credential.Token, "", privilege) {
		objects = append(objects, access.PlatformObject())
	}
	seen := make(map[string]struct{}, len(workspaceIDs))
	for _, workspaceID := range workspaceIDs {
		workspaceID = strings.TrimSpace(workspaceID)
		if workspaceID == "" {
			continue
		}
		if credential != nil && !apiTokenAllows(credential.Token, workspaceID, privilege) {
			continue
		}
		if _, ok := seen[workspaceID]; ok {
			continue
		}
		seen[workspaceID] = struct{}{}
		objects = append(objects, access.WorkspaceObject(workspaceID))
	}
	return objects
}

func (m *Module) AuthorizeAnyWorkspace(ctx context.Context, principalID string, credential *access.APICredential, privilege access.Privilege) (bool, error) {
	return m.authorizeAnyWorkspace(ctx, principalID, credential, privilege)
}

func (m *Module) AuthorizeObject(ctx context.Context, principalID string, privilege access.Privilege, object access.ObjectRef) (bool, error) {
	if m == nil || m.repository == nil {
		return true, nil
	}
	repository, err := m.repository()
	if err != nil {
		return false, err
	}
	if repository == nil {
		return true, nil
	}
	decision, err := repository.Authorize(ctx, principalID, privilege, object)
	return decision.Allowed, err
}

func (m *Module) AuthorizeAnyObject(ctx context.Context, principalID string, privilege access.Privilege, objects []access.ObjectRef) (bool, error) {
	if m == nil || m.repository == nil {
		return true, nil
	}
	repository, err := m.repository()
	if err != nil {
		return false, err
	}
	if repository == nil {
		return true, nil
	}
	decision, err := repository.AuthorizeAny(ctx, principalID, privilege, objects)
	return decision.Allowed, err
}

func (m *Module) AuthorizeCredentialEvidence(
	ctx context.Context,
	evidence access.CredentialEvidence,
	projectID string,
	environment string,
	privilege access.Privilege,
) (bool, error) {
	repository := m.repositoryValue()
	if repository == nil ||
		strings.TrimSpace(evidence.PrincipalID) == "" ||
		strings.TrimSpace(evidence.ID) == "" ||
		!time.Now().UTC().Before(evidence.ExpiresAt.UTC()) {
		return false, nil
	}
	valid := false
	switch evidence.Class {
	case "human", "workload":
		resolver, ok := repository.(interface {
			ListAuthoringSessions(
				context.Context,
				string,
			) ([]access.AuthoringSession, error)
		})
		if !ok || m.authoringAuth == nil {
			return false, nil
		}
		sessions, err := resolver.ListAuthoringSessions(
			ctx,
			evidence.PrincipalID,
		)
		if err != nil {
			return false, err
		}
		for _, session := range sessions {
			if session.ID != evidence.ID ||
				session.PrincipalID != evidence.PrincipalID ||
				!session.RevokedAt.IsZero() ||
				!time.Now().UTC().Before(session.ExpiresAt) ||
				!session.ExpiresAt.UTC().Equal(evidence.ExpiresAt.UTC()) {
				continue
			}
			expectedClass := "human"
			if session.Kind == access.AuthoringSessionWorkload {
				expectedClass = "workload"
			}
			if expectedClass != evidence.Class ||
				session.Scope.Authorize(
					m.authoringAuth.InstanceID(),
					projectID,
					privilege,
				) != nil {
				continue
			}
			valid = true
			break
		}
	case "api_token":
		tokens, err := repository.ListAPITokens(
			ctx,
			evidence.PrincipalID,
		)
		if err != nil {
			return false, err
		}
		for _, token := range tokens {
			expiresAt, parseErr := time.Parse(time.RFC3339Nano, token.ExpiresAt)
			if parseErr == nil &&
				token.ID == evidence.ID &&
				token.PrincipalID == evidence.PrincipalID &&
				token.RevokedAt == "" &&
				expiresAt.UTC().Equal(evidence.ExpiresAt.UTC()) &&
				access.TokenAllows(token, "", privilege) {
				valid = true
				break
			}
		}
	case "session":
		sessions, err := repository.ListSessions(
			ctx,
			evidence.PrincipalID,
		)
		if err != nil {
			return false, err
		}
		for _, session := range sessions {
			expiresAt, parseErr := time.Parse(time.RFC3339Nano, session.ExpiresAt)
			if parseErr == nil &&
				session.ID == evidence.ID &&
				session.PrincipalID == evidence.PrincipalID &&
				session.RevokedAt == "" &&
				expiresAt.UTC().Equal(evidence.ExpiresAt.UTC()) &&
				time.Now().UTC().Before(expiresAt.UTC()) {
				valid = true
				break
			}
		}
	default:
		return false, nil
	}
	if !valid {
		return false, nil
	}
	decision, err := repository.Authorize(
		ctx,
		evidence.PrincipalID,
		privilege,
		access.ProjectEnvironmentObject(projectID, environment),
	)
	return decision.Allowed, err
}

func (m *Module) RecordAudit(ctx context.Context, input access.AuditEventInput) error {
	repository := m.repositoryValue()
	if repository == nil {
		return nil
	}
	return access.PersistAuditEvent(ctx, repository, input)
}
