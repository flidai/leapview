package module

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/avatar"
	"github.com/flidai/leapview/internal/access/desktopauth"
	accesshttp "github.com/flidai/leapview/internal/access/http"
	"github.com/flidai/leapview/internal/access/http/mcpoauth"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
)

type Module struct {
	handler                      accesshttp.Handler
	auth                         *Auth
	repository                   func() (access.Repository, error)
	oauth                        *mcpoauth.Service
	oauthResource                mcpoauth.ResourceServer
	desktopAuth                  *desktopauth.Service
	authoringAuth                *access.AuthoringAuthService
	currentEffectiveCapabilities func(context.Context, string) ([]access.Capability, error)
	logger                       *slog.Logger
	presentation                 webpage.Presentation
	assets                       staticasset.Resolver
}

type surfaceConfig struct {
	Repository                   func() (access.Repository, error)
	CurrentPrincipal             func(*http.Request) (Principal, bool)
	CurrentCredential            func(*http.Request) (access.APICredential, bool)
	CurrentEffectiveCapabilities func(context.Context, string) ([]access.Capability, error)
	Auth                         *Auth
	Logger                       *slog.Logger
	OAuth                        *mcpoauth.Service
	OAuthResource                mcpoauth.ResourceServer
	AuthoringAuth                *access.AuthoringAuthService
	Avatar                       *avatar.Service
	Presentation                 webpage.Presentation
	Assets                       staticasset.Resolver
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
	return &Module{auth: config.Auth, repository: config.Repository, logger: logger,
		oauth: config.OAuth, oauthResource: config.OAuthResource, authoringAuth: config.AuthoringAuth,
		currentEffectiveCapabilities: config.CurrentEffectiveCapabilities,
		presentation:                 config.Presentation, assets: config.Assets, handler: accesshttp.Handler{
			Repository: config.Repository, CurrentPrincipal: currentPrincipal,
			CurrentCredential: config.CurrentCredential, CurrentSession: currentSession,
			CurrentEffectiveCapabilities: config.CurrentEffectiveCapabilities,
			AuthoringAuth:                config.AuthoringAuth,
			Avatar:                       avatarService, LocalPasswordEnabled: localPasswordEnabled,
		}}, nil
}

func (m *Module) HTTP() accesshttp.Handler { return m.handler }

// SetCurrentEffectiveCapabilities installs the active-generation projection
// used by the current-user capability endpoint. It is intentionally an
// explicit setter because the serving snapshot is created after the access
// module during application composition.
func (m *Module) SetCurrentEffectiveCapabilities(fn func(context.Context, string) ([]access.Capability, error)) {
	if m == nil {
		return
	}
	m.currentEffectiveCapabilities = fn
	m.handler.CurrentEffectiveCapabilities = fn
}

// CurrentEffectiveCapabilities returns the active-generation capability
// projection. A missing callback fails closed instead of consulting mutable
// access storage or inventing a role-derived answer.
func (m *Module) CurrentEffectiveCapabilities(ctx context.Context, principalID string) ([]access.Capability, error) {
	if m == nil || m.currentEffectiveCapabilities == nil {
		return nil, fmt.Errorf("active authorization snapshot is unavailable")
	}
	return m.currentEffectiveCapabilities(ctx, principalID)
}

func (m *Module) Auth() *Auth {
	if m == nil {
		return nil
	}
	return m.auth
}

// IsPlatformAdmin exposes the immutable instance-wide administrator check to
// sibling capability handlers that use authenticated transport contracts.
func (m *Module) IsPlatformAdmin(ctx context.Context, principalID string) (bool, error) {
	if m == nil || m.repository == nil {
		return false, fmt.Errorf("access repository is unavailable")
	}
	repository, err := m.repository()
	if err != nil {
		return false, err
	}
	checker, ok := repository.(access.PlatformRoleReader)
	if !ok {
		return false, fmt.Errorf("platform role checker is unavailable")
	}
	return checker.IsPlatformAdmin(ctx, principalID)
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
