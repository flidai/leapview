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
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type Module struct {
	handler                      accesshttp.Handler
	auth                         *Auth
	currentPrincipal             func(*http.Request) (Principal, bool)
	repository                   func() (access.Repository, error)
	oauth                        *mcpoauth.Service
	oauthResource                mcpoauth.ResourceServer
	desktopAuth                  *desktopauth.Service
	authoringAuth                *access.AuthoringAuthService
	currentEffectiveCapabilities func(context.Context, string) ([]access.Capability, error)
	currentProjectID             func(context.Context) (projectgraph.ResourceID, error)
	logger                       *slog.Logger
	presentation                 webpage.Presentation
	assets                       staticasset.Resolver
}

type surfaceConfig struct {
	Repository                   func() (access.Repository, error)
	CurrentPrincipal             func(*http.Request) (Principal, bool)
	CurrentCredential            func(*http.Request) (access.APICredential, bool)
	CurrentEffectiveCapabilities func(context.Context, string) ([]access.Capability, error)
	CurrentProjectID             func(context.Context) (projectgraph.ResourceID, error)
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
	module := &Module{auth: config.Auth, currentPrincipal: config.CurrentPrincipal, repository: config.Repository, logger: logger,
		oauth: config.OAuth, oauthResource: config.OAuthResource, authoringAuth: config.AuthoringAuth,
		currentEffectiveCapabilities: config.CurrentEffectiveCapabilities,
		currentProjectID:             config.CurrentProjectID,
		presentation:                 config.Presentation, assets: config.Assets, handler: accesshttp.Handler{
			Repository: config.Repository, CurrentPrincipal: currentPrincipal,
			CurrentCredential: config.CurrentCredential, CurrentSession: currentSession,
			CurrentEffectiveCapabilities: config.CurrentEffectiveCapabilities,
			AuthoringAuth:                config.AuthoringAuth,
			Avatar:                       avatarService, LocalPasswordEnabled: localPasswordEnabled,
		},
	}
	module.handler.RequestEffectiveCapabilities = module.RequestEffectiveCapabilities
	module.handler.PlatformAdmin = module.IsPlatformAdmin
	module.handler.RequestPlatformAdmin = module.RequestPlatformAdmin
	return module, nil
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

// SetCurrentProjectID installs the immutable active project identity used to
// bind request credentials to the serving generation. It is separate from
// the capability projection because that projection is scoped by principal,
// while this identity is scoped by the active runtime lease.
func (m *Module) SetCurrentProjectID(fn func(context.Context) (projectgraph.ResourceID, error)) {
	if m == nil {
		return
	}
	m.currentProjectID = fn
}

// CurrentProjectID returns the active immutable project identity. A missing
// callback fails closed instead of inferring identity from a route or mutable
// access record.
func (m *Module) CurrentProjectID(ctx context.Context) (projectgraph.ResourceID, error) {
	if m == nil || m.currentProjectID == nil {
		return "", fmt.Errorf("active project identity is unavailable")
	}
	return m.currentProjectID(ctx)
}

// IsPlatformAdmin evaluates only the durable, instance-wide platform role.
// Project authorization snapshots are intentionally not consulted here: they
// may be absent while identity administration remains available, and a
// project PROJECT_ADMIN grant is not a platform role.
func (m *Module) IsPlatformAdmin(ctx context.Context, principalID string) (bool, error) {
	principalID = strings.TrimSpace(principalID)
	if principalID == "" {
		return false, nil
	}
	repository := m.repositoryValue()
	if repository == nil {
		return false, fmt.Errorf("access repository is unavailable")
	}
	reader, ok := repository.(access.PlatformAdminReader)
	if !ok {
		return false, fmt.Errorf("access repository does not support durable platform administration")
	}
	return reader.IsPlatformAdmin(ctx, principalID)
}

// AuthorizeBootstrapCredential revalidates the exact durable API token used
// to arm or execute a protected first activation. Request credentials are
// never trusted as role evidence: the repository query binds token ID to the
// actor, checks enabled platform administration, requires an explicit
// RESOURCE_PUBLISH capability at the supplied instant, and verifies that the
// durable expiry still exactly matches the expiry captured when armed.
func (m *Module) AuthorizeBootstrapCredential(ctx context.Context, principalID, credentialID string, expectedExpiresAt, now time.Time) error {
	principalID = strings.TrimSpace(principalID)
	credentialID = strings.TrimSpace(credentialID)
	if principalID == "" || credentialID == "" || expectedExpiresAt.IsZero() {
		return access.ErrForbidden
	}
	repository := m.repositoryValue()
	if repository == nil {
		return fmt.Errorf("access repository is unavailable")
	}
	reader, ok := repository.(access.BootstrapAPITokenEvidenceReader)
	if !ok {
		return fmt.Errorf("access repository does not support bootstrap token evidence")
	}
	token, err := reader.BootstrapAPITokenEvidence(ctx, principalID, credentialID, now)
	if err != nil {
		return err
	}
	if token.ID != credentialID || token.PrincipalID != principalID || token.ExpiresAt == "" {
		return access.ErrForbidden
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, token.ExpiresAt)
	if err != nil || !expiresAt.Equal(expectedExpiresAt.UTC()) {
		return access.ErrForbidden
	}
	return nil
}

// RequestPlatformAdmin evaluates durable platform administration and then
// applies request-credential attenuation. Credentials can reduce authority,
// never grant the durable role: authoring credentials always deny, API tokens
// with nil capabilities inherit, explicit empty capabilities deny, and an
// explicit non-empty list must include PROJECT_ADMIN.
func (m *Module) RequestPlatformAdmin(ctx context.Context, r *http.Request, principalID string) (bool, error) {
	allowed, err := m.IsPlatformAdmin(ctx, principalID)
	if err != nil || !allowed {
		return allowed, err
	}
	credential, ok := m.requestCredential(r)
	if !ok {
		return true, nil
	}
	if credential.Principal.ID != "" && credential.Principal.ID != principalID {
		return false, nil
	}
	if credential.Authoring != nil {
		return false, nil
	}
	if credential.Token.ID == "" || credential.Token.Capabilities == nil {
		return true, nil
	}
	if len(credential.Token.Capabilities) == 0 {
		return false, nil
	}
	return containsCapability(credential.Token.Capabilities, access.CapabilityProjectAdmin), nil
}

func (m *Module) requestCredential(r *http.Request) (access.APICredential, bool) {
	if r == nil {
		return access.APICredential{}, false
	}
	if credential, ok := APICredentialFromContext(r.Context()); ok {
		return credential, true
	}
	if m != nil && m.auth != nil {
		return m.auth.APICredential(r)
	}
	return access.APICredential{}, false
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

func (m *Module) CurrentPrincipal(r *http.Request) (Principal, bool) {
	if m == nil {
		return Principal{}, false
	}
	if m.auth == nil {
		if m.currentPrincipal != nil {
			return m.currentPrincipal(r)
		}
		return LocalDeveloperPrincipal(), true
	}
	return m.auth.Principal(r)
}

// RequestEffectiveCapabilities evaluates the active immutable authorization
// projection and attenuates it with any bearer or authoring credential on the
// request. Stored credentials never add authority: omitted token capabilities
// inherit the active projection, while an explicit empty list denies all.
func (m *Module) RequestEffectiveCapabilities(ctx context.Context, r *http.Request, principalID string) ([]access.Capability, error) {
	capabilities, err := m.CurrentEffectiveCapabilities(ctx, principalID)
	if err != nil {
		return nil, err
	}
	if m == nil || m.auth == nil || r == nil {
		return capabilities, nil
	}
	credential, ok := m.auth.APICredential(r)
	if !ok {
		return capabilities, nil
	}
	if credential.Authoring != nil {
		activeProjectID, err := m.CurrentProjectID(ctx)
		if err != nil {
			return nil, fmt.Errorf("authoring credential active project: %w", err)
		}
		if err := activeProjectID.Validate(); err != nil {
			return nil, fmt.Errorf("authoring credential active project: %w", err)
		}
		if credential.Authoring.Scope.ProjectID != activeProjectID {
			return nil, access.ErrAuthoringScopeDenied
		}
		capabilities = access.IntersectTokenCapabilities(credential.Authoring.Scope.Capabilities, capabilities)
	}
	if credential.Token.ID != "" {
		if credential.Token.Capabilities != nil && len(credential.Token.Capabilities) == 0 {
			return nil, access.ErrForbidden
		}
		capabilities = access.IntersectTokenCapabilities(credential.Token.Capabilities, capabilities)
	}
	return capabilities, nil
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
