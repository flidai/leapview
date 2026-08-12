package module

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/avatar"
	"github.com/flidai/leapview/internal/access/desktopauth"
	accesshttp "github.com/flidai/leapview/internal/access/http"
	"github.com/flidai/leapview/internal/access/http/mcpoauth"
	accessoperation "github.com/flidai/leapview/internal/access/operation"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
)

type Module struct {
	handler             accesshttp.Handler
	auth                *Auth
	repository          func() (access.Repository, error)
	workspaceIDs        func(context.Context) ([]string, error)
	workspaceID         string
	oauth               *mcpoauth.Service
	oauthResource       mcpoauth.ResourceServer
	desktopAuth         *desktopauth.Service
	authoringAuth       *access.AuthoringAuthService
	roleBindingCommands access.RoleBindingOperations
	grantCommands       access.GrantOperations
	logger              *slog.Logger
	presentation        webpage.Presentation
	assets              staticasset.Resolver
}

type surfaceConfig struct {
	Repository         func() (access.Repository, error)
	CurrentPrincipal   func(*http.Request) (Principal, bool)
	CurrentCredential  func(*http.Request) (access.APICredential, bool)
	WorkspaceID        func(string) string
	Auth               *Auth
	WorkspaceIDs       func(context.Context) ([]string, error)
	DefaultWorkspaceID string
	Logger             *slog.Logger
	OAuth              *mcpoauth.Service
	OAuthResource      mcpoauth.ResourceServer
	AuthoringAuth      *access.AuthoringAuthService
	Avatar             *avatar.Service
	Presentation       webpage.Presentation
	Assets             staticasset.Resolver
}

func newSurface(config surfaceConfig) (*Module, error) {
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	currentPrincipal := func(r *http.Request) (accesshttp.Principal, bool) {
		if config.CurrentPrincipal == nil {
			principal, ok := PrincipalFromContext(r.Context())
			return accesshttp.Principal{ID: principal.ID, Kind: principal.Kind, Email: principal.Email, DisplayName: principal.DisplayName, CreatedAt: principal.CreatedAt, UpdatedAt: principal.UpdatedAt}, ok
		}
		principal, ok := config.CurrentPrincipal(r)
		return accesshttp.Principal{ID: principal.ID, Kind: principal.Kind, Email: principal.Email, DisplayName: principal.DisplayName, CreatedAt: principal.CreatedAt, UpdatedAt: principal.UpdatedAt}, ok
	}
	localPasswordEnabled := config.Auth != nil && config.Auth.LocalAuthEnabled()
	var avatarService accesshttp.AvatarService
	if config.Avatar != nil {
		avatarService = config.Avatar
	}
	currentSession := func(r *http.Request) (string, bool) {
		if config.Repository == nil {
			return "", false
		}
		cookie, err := r.Cookie("lv_session")
		if err != nil || strings.TrimSpace(cookie.Value) == "" {
			return "", false
		}
		repository, err := config.Repository()
		if err != nil || repository == nil {
			return "", false
		}
		resolver, ok := repository.(interface {
			CredentialForSessionToken(context.Context, string) (access.Session, error)
		})
		if !ok {
			return "", false
		}
		session, err := resolver.CredentialForSessionToken(r.Context(), cookie.Value)
		if err != nil || session.ID == "" {
			return "", false
		}
		return session.ID, true
	}
	roleBindingCommands, err := accessoperation.NewRoleBindingCommands(accessoperation.RepositoryProvider(config.Repository))
	if err != nil {
		return nil, err
	}
	grantCommands, err := accessoperation.NewGrantCommands(accessoperation.RepositoryProvider(config.Repository))
	if err != nil {
		return nil, err
	}
	return &Module{auth: config.Auth, repository: config.Repository, workspaceIDs: config.WorkspaceIDs, workspaceID: config.DefaultWorkspaceID, logger: logger,
		oauth: config.OAuth, oauthResource: config.OAuthResource, authoringAuth: config.AuthoringAuth,
		roleBindingCommands: roleBindingCommands,
		grantCommands:       grantCommands,
		presentation:        config.Presentation, assets: config.Assets, handler: accesshttp.Handler{
			Repository: config.Repository, CurrentPrincipal: currentPrincipal,
			RoleBindingCommands: roleBindingCommands, GrantCommands: grantCommands,
			CurrentCredential: config.CurrentCredential, CurrentSession: currentSession,
			WorkspaceID: config.WorkspaceID, AuthoringAuth: config.AuthoringAuth,
			Avatar: avatarService, LocalPasswordEnabled: localPasswordEnabled,
		}}, nil
}

func (m *Module) RoleBindingCommands() access.RoleBindingOperations {
	if m == nil {
		return nil
	}
	return m.roleBindingCommands
}

func (m *Module) GrantCommands() access.GrantOperations {
	if m == nil {
		return nil
	}
	return m.grantCommands
}

func (m *Module) HTTP() accesshttp.Handler { return m.handler }

func (m *Module) Auth() *Auth {
	if m == nil {
		return nil
	}
	return m.auth
}

func (m *Module) CurrentPrincipal(r *http.Request) (Principal, bool) {
	if m == nil || m.auth == nil {
		return LocalDeveloperPrincipal(), true
	}
	return m.auth.Principal(r)
}

func (m *Module) CurrentCredentialEvidence(
	r *http.Request,
) (access.CredentialEvidence, bool) {
	principal, ok := m.CurrentPrincipal(r)
	if !ok || principal.DevBypass {
		return access.CredentialEvidence{}, false
	}
	if m.auth != nil {
		if credential, found := m.auth.APICredential(r); found {
			if credential.Authoring != nil {
				class := "human"
				if credential.Authoring.Kind == access.AuthoringSessionWorkload {
					class = "workload"
				}
				return access.CredentialEvidence{
					Class: class, ID: credential.Authoring.ID,
					PrincipalID: principal.ID,
					ExpiresAt:   credential.Authoring.ExpiresAt.UTC(),
				}, true
			}
			expiresAt, err := time.Parse(
				time.RFC3339Nano,
				credential.Token.ExpiresAt,
			)
			if err == nil && credential.Token.ID != "" {
				return access.CredentialEvidence{
					Class: "api_token", ID: credential.Token.ID,
					PrincipalID: principal.ID,
					ExpiresAt:   expiresAt.UTC(),
				}, true
			}
		}
	}
	cookie, err := r.Cookie("lv_session")
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return access.CredentialEvidence{}, false
	}
	repository := m.repositoryValue()
	resolver, ok := repository.(interface {
		CredentialForSessionToken(
			context.Context,
			string,
		) (access.Session, error)
	})
	if !ok {
		return access.CredentialEvidence{}, false
	}
	session, err := resolver.CredentialForSessionToken(
		r.Context(),
		cookie.Value,
	)
	if err != nil || session.PrincipalID != principal.ID ||
		session.RevokedAt != "" {
		return access.CredentialEvidence{}, false
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, session.ExpiresAt)
	if err != nil {
		return access.CredentialEvidence{}, false
	}
	return access.CredentialEvidence{
		Class: "session", ID: session.ID,
		PrincipalID: principal.ID, ExpiresAt: expiresAt.UTC(),
	}, true
}
