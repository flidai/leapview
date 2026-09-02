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
	persistence                  *Persistence
	auth                         *Auth
	currentPrincipal             func(*http.Request) (Principal, bool)
	repository                   func() (access.Repository, error)
	oauth                        *mcpoauth.Service
	oauthResource                mcpoauth.ResourceServer
	desktopAuth                  *desktopauth.Service
	authoringAuth                *access.AuthoringAuthService
	currentEffectiveCapabilities func(context.Context, string) ([]access.Capability, error)
	currentProjectID             func(context.Context) (projectgraph.ResourceID, error)
	// authoringProjectID resolves the durable project binding used by
	// authoring OAuth. It is intentionally separate from the active-runtime
	// resolver: a fresh target has no serving lease yet, but may still accept
	// a validated project before the first claim/plan is created.
	authoringProjectID func(context.Context) (projectgraph.ResourceID, error)
	logger             *slog.Logger
	presentation       webpage.Presentation
	assets             staticasset.Resolver
}

type surfaceConfig struct {
	Persistence                  *Persistence
	Repository                   func() (access.Repository, error)
	CurrentPrincipal             func(*http.Request) (Principal, bool)
	CurrentCredential            func(*http.Request) (access.APICredential, bool)
	CurrentEffectiveCapabilities func(context.Context, string) ([]access.Capability, error)
	CurrentProjectID             func(context.Context) (projectgraph.ResourceID, error)
	AuthoringProjectID           func(context.Context) (projectgraph.ResourceID, error)
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
		cookieName := sessionCookieName
		if config.Auth != nil {
			cookieName = config.Auth.SessionCookieName()
		}
		cookie, err := r.Cookie(cookieName)
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
	module := &Module{auth: config.Auth, persistence: config.Persistence, currentPrincipal: config.CurrentPrincipal, repository: config.Repository, logger: logger,
		oauth: config.OAuth, oauthResource: config.OAuthResource, authoringAuth: config.AuthoringAuth,
		currentEffectiveCapabilities: config.CurrentEffectiveCapabilities,
		currentProjectID:             config.CurrentProjectID,
		authoringProjectID:           config.AuthoringProjectID,
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

// AuthorizeBootstrapRequest applies the narrow request-side checks shared by
// pre-activation project operations and managed-data transport requests. A
// bootstrap request must carry a bearer API token with an explicit capability
// list, belong to the authenticated principal, and be backed by the durable
// platform-admin role. The sole exception is the explicit local-development
// bypass, which production configuration rejects and which still requires its
// configured development bearer token. It never consults an active project
// snapshot.
func (m *Module) AuthorizeBootstrapRequest(ctx context.Context, r *http.Request, required access.Capability) (bool, error) {
	if m == nil || r == nil || bearerToken(r) == "" {
		return false, nil
	}
	principal, ok := m.CurrentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return false, nil
	}
	if principal.DevBypass {
		return m.auth != nil && m.auth.DevBypass() && m.auth.AcceptsPublicBearer(r), nil
	}
	credential, found := m.requestCredential(r)
	if !found || credential.Authoring != nil || credential.Token.ID == "" || credential.Token.Capabilities == nil || len(credential.Token.Capabilities) == 0 || credential.Principal.ID != principal.ID || credential.Token.PrincipalID != principal.ID {
		return false, nil
	}
	isAdmin, err := m.IsPlatformAdmin(ctx, principal.ID)
	if err != nil {
		return false, err
	}
	return isAdmin && bootstrapTokenAllowsCapability(credential.Token.Capabilities, required), nil
}

// AuthorizePublicationApprovalBootstrapRequest validates only the credential
// attenuation for the fresh-target approval ingress. A normal API token must
// explicitly carry PROJECT_ADMIN. A CLI/workload credential must carry the
// exact target, project, and PROJECT_ADMIN authoring scope. Neither path uses
// durable platform administration: the downstream approval authorizer remains
// responsible for loading the requested generation's immutable authorization
// snapshot and proving that the principal is one of its project admins.
func (m *Module) AuthorizePublicationApprovalBootstrapRequest(ctx context.Context, r *http.Request, projectID string) (bool, error) {
	if m == nil || r == nil || bearerToken(r) == "" {
		return false, nil
	}
	principal, ok := m.CurrentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return false, nil
	}
	credential, found := m.requestCredential(r)
	if !found || credential.Principal.ID != principal.ID {
		return false, nil
	}
	if credential.Authoring != nil {
		authoring := *credential.Authoring
		if err := validateAuthoringBootstrapCredential(credential, authoring, principal, projectID); err != nil {
			return false, nil
		}
		if targetID := m.authoringInstanceID(); targetID != "" && authoring.Scope.TargetID != targetID {
			return false, nil
		}
		return containsCapability(authoring.Scope.Capabilities, access.CapabilityProjectAdmin), nil
	}
	if credential.Token.ID == "" || credential.Token.Capabilities == nil || len(credential.Token.Capabilities) == 0 || credential.Token.PrincipalID != principal.ID {
		return false, nil
	}
	return containsCapability(credential.Token.Capabilities, access.CapabilityProjectAdmin), nil
}

// bootstrapTokenAllowsCapability preserves the project-admin hierarchy used
// by the canonical policy. A token explicitly scoped to PROJECT_ADMIN may
// perform project-scoped resource operations during bootstrap; narrower
// tokens must carry the exact operation capability.
func bootstrapTokenAllowsCapability(capabilities []access.Capability, required access.Capability) bool {
	return containsCapability(capabilities, required) ||
		(required != access.CapabilityProjectAdmin && containsCapability(capabilities, access.CapabilityProjectAdmin))
}

// AuthorizeAuthoringBootstrapRequest admits an already-issued human/workload
// authoring credential for the narrow project control-plane operations that
// may race the serving-generation cutover. Unlike API-token bootstrap, this
// path relies on the immutable authoring scope plus durable platform
// administration; it never grants a credential authority it does not already
// carry. The generated API authorizer invokes this method only after its
// bootstrap policy has proved that no active serving generation exists.
//
// A fresh PostgreSQL target has no active authorization snapshot yet. The
// durable authoring-project resolver is therefore consulted first. An empty
// result admits the first plan; a matching claimed project admits the upload,
// retain, and commit steps that follow before activation. Resolver errors,
// malformed state, and project disagreement fail closed.
func (m *Module) AuthorizeAuthoringBootstrapRequest(ctx context.Context, r *http.Request, projectID string, required access.Capability) (bool, error) {
	if m == nil || r == nil || m.auth == nil {
		return false, nil
	}
	principal, ok := m.CurrentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return false, nil
	}
	credential, ok := m.auth.APICredential(r)
	if !ok || credential.Authoring == nil {
		return false, nil
	}
	authoring := credential.Authoring
	if err := validateAuthoringBootstrapCredential(credential, *authoring, principal, projectID); err != nil {
		return false, nil
	}
	if targetID := m.authoringInstanceID(); targetID != "" && authoring.Scope.TargetID != targetID {
		return false, nil
	}
	if m.authoringProjectID == nil {
		return false, fmt.Errorf("authoring bootstrap durable project resolver is unavailable")
	}
	boundProjectID, err := m.authoringProjectID(ctx)
	if err != nil {
		return false, fmt.Errorf("resolve authoring bootstrap project: %w", err)
	}
	if boundProjectID != "" {
		if err := boundProjectID.Validate(); err != nil {
			return false, fmt.Errorf("resolve authoring bootstrap project: %w", err)
		}
		if boundProjectID.String() != strings.TrimSpace(projectID) {
			return false, nil
		}
	}
	if !bootstrapTokenAllowsCapability(authoring.Scope.Capabilities, required) {
		return false, nil
	}
	// Project authorization snapshots do not exist until activation. Durable
	// platform administration is therefore the non-project authority for the
	// complete first-sync sequence, including the claim-only interval between
	// plan and commit.
	return m.IsPlatformAdmin(ctx, principal.ID)
}

func (m *Module) authoringInstanceID() string {
	if m == nil {
		return ""
	}
	if m.authoringAuth != nil {
		return strings.TrimSpace(m.authoringAuth.InstanceID())
	}
	if m.auth != nil && m.auth.authoringAuth != nil {
		return strings.TrimSpace(m.auth.authoringAuth.InstanceID())
	}
	return ""
}

// validateAuthoringBootstrapCredential checks the fields that the auth
// middleware normally obtains from the authoring credential repository. The
// bootstrap path also accepts context-injected credentials in tests and in
// adapters, so it must not assume those fields were validated elsewhere.
func validateAuthoringBootstrapCredential(credential access.APICredential, authoring access.AuthoringSession, principal Principal, projectID string) error {
	requestedProjectID, err := projectgraph.NewResourceID(projectID)
	if err != nil || requestedProjectID.String() != projectID {
		return access.ErrAuthoringScopeDenied
	}
	if strings.TrimSpace(principal.ID) != principal.ID || authoring.PrincipalID == "" || authoring.PrincipalID != principal.ID {
		return access.ErrAuthoringScopeDenied
	}
	if credential.Principal.ID == "" || credential.Principal.ID != principal.ID || credential.Principal.Kind != principal.Kind || credential.Principal.AccessDisabled() {
		return access.ErrAuthoringScopeDenied
	}
	if authoring.ID == "" || strings.TrimSpace(authoring.ID) != authoring.ID || authoring.Scope.TargetID == "" || authoring.ClientID == "" || !authoring.RevokedAt.IsZero() {
		return access.ErrAuthoringScopeDenied
	}
	switch authoring.Kind {
	case access.AuthoringSessionHumanCLI:
		if authoring.ClientID != access.AuthoringCLIClientID || principal.Kind != access.PrincipalKindUser {
			return access.ErrAuthoringScopeDenied
		}
	case access.AuthoringSessionWorkload:
		if authoring.ClientID != principal.ID || principal.Kind != access.PrincipalKindServicePrincipal {
			return access.ErrAuthoringScopeDenied
		}
	default:
		return access.ErrAuthoringScopeDenied
	}
	validatedScope, err := access.NewAuthoringScope(authoring.Scope.TargetID, authoring.Scope.ProjectID, authoring.Scope.Capabilities)
	if err != nil || validatedScope.TargetID != authoring.Scope.TargetID || validatedScope.ProjectID != requestedProjectID {
		return access.ErrAuthoringScopeDenied
	}
	// A REST request may carry an authoring credential resolved by the module's
	// authoring service. When that service is present, bind the opaque scope to
	// this instance as well; a missing service is retained for lightweight
	// adapters, but the durable project resolver is still mandatory above.
	return nil
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
	cookieName := sessionCookieName
	if m.auth != nil {
		cookieName = m.auth.SessionCookieName()
	}
	cookie, err := r.Cookie(cookieName)
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
