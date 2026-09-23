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
	handler                           accesshttp.Handler
	persistence                       *Persistence
	auth                              *Auth
	currentPrincipal                  func(*http.Request) (Principal, bool)
	repository                        func() (access.Repository, error)
	oauth                             mcpOAuthService
	oauthResource                     mcpoauth.ResourceServer
	desktopAuth                       *desktopauth.Service
	authoringAuth                     *access.AuthoringAuthService
	currentEffectiveCapabilities      func(context.Context, string) ([]access.Capability, error)
	currentEffectivePermissionOptions func(context.Context, string) ([]access.PermissionPair, error)
	currentProjectID                  func(context.Context) (projectgraph.ResourceID, error)
	// authoringProjectID resolves the durable project binding used by
	// authoring OAuth. It is intentionally separate from the active-runtime
	// resolver: a fresh target has no serving lease yet, but may still accept
	// a validated project before the first claim/plan is created.
	authoringProjectID func(context.Context) (projectgraph.ResourceID, error)
	logger             *slog.Logger
	presentation       webpage.Presentation
	assets             staticasset.Resolver
}

// mcpOAuthService keeps the module's consent surface independent of the
// transport adapter assembled by Build.
type mcpOAuthService interface {
	AuthorizationServerMetadata(http.ResponseWriter, *http.Request)
	Register(http.ResponseWriter, *http.Request)
	Token(http.ResponseWriter, *http.Request)
	Revoke(http.ResponseWriter, *http.Request)
	Consent(*http.Request) (mcpoauth.Consent, error)
	Authorize(http.ResponseWriter, *http.Request, string, bool)
}

// The resource side needs only this small authentication/metadata contract.
type mcpOAuthResource interface {
	Authenticate(context.Context, string) (mcpoauth.Credential, error)
	ProtectedResourceMetadata(http.ResponseWriter, *http.Request)
	Challenge(http.ResponseWriter)
}

type surfaceConfig struct {
	Persistence                    *Persistence
	Repository                     func() (access.Repository, error)
	AuthorizationPolicyTargetID    string
	AuthorizationPolicyEnvironment string
	InstanceID                     string
	CurrentPrincipal               func(*http.Request) (Principal, bool)
	CurrentCredential              func(*http.Request) (access.APICredential, bool)
	CurrentEffectiveCapabilities   func(context.Context, string) ([]access.Capability, error)
	// CurrentEffectivePermissionOptions is intentionally separate from the
	// legacy capability projection. It may only be populated by a typed
	// principal-grant snapshot; until that authority exists it remains empty.
	CurrentEffectivePermissionOptions func(context.Context, string) ([]access.PermissionPair, error)
	CurrentProjectID                  func(context.Context) (projectgraph.ResourceID, error)
	AuthoringProjectID                func(context.Context) (projectgraph.ResourceID, error)
	Auth                              *Auth
	Logger                            *slog.Logger
	OAuth                             mcpOAuthService
	OAuthResource                     mcpOAuthResource
	AuthoringAuth                     *access.AuthoringAuthService
	Avatar                            *avatar.Service
	Presentation                      webpage.Presentation
	Assets                            staticasset.Resolver
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
		currentEffectiveCapabilities:      config.CurrentEffectiveCapabilities,
		currentEffectivePermissionOptions: config.CurrentEffectivePermissionOptions,
		currentProjectID:                  config.CurrentProjectID,
		authoringProjectID:                config.AuthoringProjectID,
		presentation:                      config.Presentation, assets: config.Assets, handler: accesshttp.Handler{
			Repository: config.Repository, AuthorizationPolicyTargetID: config.AuthorizationPolicyTargetID,
			AuthorizationPolicyEnvironment: config.AuthorizationPolicyEnvironment, CurrentPrincipal: currentPrincipal,
			DurableGrantInstanceID: config.InstanceID,
			CurrentCredential:      config.CurrentCredential, CurrentSession: currentSession,
			CurrentEffectiveCapabilities:      config.CurrentEffectiveCapabilities,
			CurrentEffectivePermissionOptions: config.CurrentEffectivePermissionOptions,
			CurrentProjectID:                  config.CurrentProjectID,
			AuthoringAuth:                     config.AuthoringAuth,
			Avatar:                            avatarService, LocalPasswordEnabled: localPasswordEnabled,
		},
	}
	module.handler.RequestEffectiveCapabilities = module.RequestEffectiveCapabilities
	module.handler.PlatformAdmin = module.IsPlatformAdmin
	module.handler.RequestPlatformAdmin = module.RequestPlatformAdmin
	module.handler.DurableGrantService = module.durableGrantService
	return module, nil
}

// SetCurrentEffectivePermissionOptions installs the active-generation typed
// action-target projection used by personal token creation. During migration,
// the snapshot provider may expose only the explicitly qualified compatibility
// mappings; an absent projection is an explicit empty authority set.
func (m *Module) SetCurrentEffectivePermissionOptions(fn func(context.Context, string) ([]access.PermissionPair, error)) {
	if m == nil {
		return
	}
	m.currentEffectivePermissionOptions = fn
	m.handler.CurrentEffectivePermissionOptions = fn
}

// CurrentEffectivePermissionOptions returns exact typed action-target pairs
// proved by the active authorization snapshot. An unset projection fails
// closed with an explicit empty array rather than inventing pair mappings.
func (m *Module) CurrentEffectivePermissionOptions(ctx context.Context, principalID string) ([]access.PermissionPair, error) {
	if m == nil || m.currentEffectivePermissionOptions == nil {
		return []access.PermissionPair{}, nil
	}
	return m.currentEffectivePermissionOptions(ctx, principalID)
}

func (m *Module) HTTP() accesshttp.Handler { return m.handler }

func (m *Module) RoleBindingAdministration(ctx context.Context) (access.RoleBindingAdministrationState, error) {
	if m == nil {
		return access.RoleBindingAdministrationState{}, fmt.Errorf("access module is unavailable")
	}
	return m.handler.RoleBindingAdministration(ctx)
}

func (m *Module) ApplyRoleBindingAdministration(r *http.Request, command access.RoleBindingAdministrationCommand) (access.RoleBindingAdministrationState, error) {
	if m == nil {
		return access.RoleBindingAdministrationState{}, fmt.Errorf("access module is unavailable")
	}
	return m.handler.ApplyRoleBindingAdministration(r, command)
}

// SetClaimBootstrapBindingAuthorizer installs the application-owned durable
// claim check after deployment and access have both been composed.
func (m *Module) SetClaimBootstrapBindingAuthorizer(fn func(*http.Request, access.AuthorizationPolicyScope, access.RoleBinding, string) (bool, error)) {
	if m != nil {
		m.handler.AuthorizeClaimBootstrapBinding = fn
	}
}

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
	m.handler.CurrentProjectID = fn
}

// SetProjectClaimResolver installs the deployment-owned immutable initial
// project claim used by the credential handoff endpoints. It is deliberately
// separate from the active project resolver because exchange happens before
// the first serving generation exists.
func (m *Module) SetProjectClaimResolver(fn func(context.Context) (projectID, claimedBy string, err error)) {
	if m == nil {
		return
	}
	m.handler.ProjectClaim = fn
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

// RequestPlatformAdmin evaluates durable platform administration and then
// applies request-credential attenuation. Credentials can reduce authority,
// never grant the durable role: authoring credentials always deny, and API
// tokens require an exact, already-validated instance operation marker.
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
	if authorization, found := instanceAuthorizationFromContext(ctx); found && authorization.PrincipalID == principalID {
		return true, nil
	}
	return false, nil
}

// AuthorizeTypedBootstrapRequest checks a pre-activation API credential against
// exact project permission pairs. The durable platform role is the bootstrap
// baseline while the token can only attenuate it; no legacy capability list
// participates in the decision.
func (m *Module) AuthorizeTypedBootstrapRequest(ctx context.Context, r *http.Request, required []access.PermissionPair) (bool, error) {
	if m == nil || r == nil || bearerToken(r) == "" || len(required) == 0 {
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
	if !found || credential.Authoring != nil || credential.Token.ID == "" ||
		credential.Principal.ID != principal.ID || credential.Token.PrincipalID != principal.ID ||
		credential.Token.PermissionProfile != access.PermissionCatalogProfile ||
		!permissionPairsAllowAll(credential.Token.Permissions, required) {
		return false, nil
	}
	return m.IsPlatformAdmin(ctx, principal.ID)
}

// RequestAllowsTypedPermissions applies the credential's attenuation ceiling
// after independent principal/group authorization has succeeded. Browser
// sessions have no token ceiling; API and authoring credentials must carry
// every requested exact action-target pair.
func (m *Module) RequestAllowsTypedPermissions(r *http.Request, projectID projectgraph.ResourceID, required []access.PermissionPair) bool {
	if m == nil || r == nil || len(required) == 0 {
		return false
	}
	principal, ok := m.CurrentPrincipal(r)
	if !ok || principal.ID == "" {
		return false
	}
	credential, found := m.requestCredential(r)
	if !found {
		return true
	}
	if credential.Principal.ID != principal.ID {
		return false
	}
	if credential.Authoring != nil {
		authoring := *credential.Authoring
		if err := validateAuthoringBootstrapCredential(credential, authoring, principal, projectID.String()); err != nil {
			return false
		}
		if targetID := m.authoringInstanceID(); targetID != "" && authoring.Scope.TargetID != targetID {
			return false
		}
		return authoring.Scope.AuthorizePairs(authoring.Scope.TargetID, projectID.String(), required) == nil
	}
	return credential.Token.ID != "" && credential.Token.PrincipalID == principal.ID &&
		credential.Token.PermissionProfile == access.PermissionCatalogProfile &&
		permissionPairsAllowAll(credential.Token.Permissions, required)
}

// AuthorizeTypedPublicationApprovalBootstrapRequest only checks the
// credential's exact reviewer permission and project binding. The downstream
// approval authorizer independently checks the requested generation's
// immutable snapshot and requester/reviewer separation.
func (m *Module) AuthorizeTypedPublicationApprovalBootstrapRequest(r *http.Request, projectID projectgraph.ResourceID, required []access.PermissionPair) (bool, error) {
	if m == nil || r == nil || bearerToken(r) == "" || len(required) == 0 {
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
		if err := validateAuthoringBootstrapCredential(credential, authoring, principal, projectID.String()); err != nil {
			return false, nil
		}
		if targetID := m.authoringInstanceID(); targetID != "" && authoring.Scope.TargetID != targetID {
			return false, nil
		}
		return authoring.Scope.AuthorizePairs(authoring.Scope.TargetID, projectID.String(), required) == nil, nil
	}
	return credential.Token.ID != "" && credential.Token.PrincipalID == principal.ID &&
		credential.Token.PermissionProfile == access.PermissionCatalogProfile &&
		permissionPairsAllowAll(credential.Token.Permissions, required), nil
}

// AuthorizeTypedAuthoringBootstrapRequest applies a typed authoring-session
// ceiling before any active serving generation exists. The session must be
// bound to the requested target and claimed project; durable platform admin
// remains the principal's independent bootstrap authority.
func (m *Module) AuthorizeTypedAuthoringBootstrapRequest(ctx context.Context, r *http.Request, projectID string, required []access.PermissionPair) (bool, error) {
	principal, boundProjectID, allowed, err := m.validateTypedAuthoringScopedRequest(ctx, r, projectID, required)
	if err != nil || !allowed {
		return false, err
	}
	_ = boundProjectID // The first synchronization can precede the durable claim.
	return m.IsPlatformAdmin(ctx, principal.ID)
}

func (m *Module) validateTypedAuthoringScopedRequest(ctx context.Context, r *http.Request, projectID string, required []access.PermissionPair) (Principal, projectgraph.ResourceID, bool, error) {
	if m == nil || r == nil || m.auth == nil || len(required) == 0 {
		return Principal{}, "", false, nil
	}
	principal, ok := m.CurrentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return Principal{}, "", false, nil
	}
	credential, ok := m.auth.APICredential(r)
	if !ok || credential.Authoring == nil {
		return Principal{}, "", false, nil
	}
	authoring := *credential.Authoring
	if err := validateAuthoringBootstrapCredential(credential, authoring, principal, projectID); err != nil {
		return Principal{}, "", false, nil
	}
	if targetID := m.authoringInstanceID(); targetID != "" && authoring.Scope.TargetID != targetID {
		return Principal{}, "", false, nil
	}
	if err := authoring.Scope.AuthorizePairs(authoring.Scope.TargetID, projectID, required); err != nil {
		return Principal{}, "", false, nil
	}
	if m.authoringProjectID == nil {
		return Principal{}, "", false, fmt.Errorf("authoring bootstrap durable project resolver is unavailable")
	}
	boundProjectID, err := m.authoringProjectID(ctx)
	if err != nil {
		return Principal{}, "", false, fmt.Errorf("resolve authoring bootstrap project: %w", err)
	}
	if boundProjectID != "" {
		if err := boundProjectID.Validate(); err != nil {
			return Principal{}, "", false, fmt.Errorf("resolve authoring bootstrap project: %w", err)
		}
		if boundProjectID.String() != strings.TrimSpace(projectID) {
			return Principal{}, "", false, nil
		}
	}
	return principal, boundProjectID, true, nil
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
	validatedScope, err := access.NewAuthoringScope(authoring.Scope.TargetID, authoring.Scope.ProjectID, authoring.Scope.Permissions)
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

// RequestEffectiveCapabilities is a historical read-only projection. It has
// no sound mapping from typed action-target credentials to generic capability
// strings, so API and authoring credentials cannot use it as authority.
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
	if credential.Authoring != nil || credential.Token.ID != "" {
		return nil, access.ErrForbidden
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
	if evidence, found := SessionCredentialEvidenceFromContext(r.Context()); found && evidence.PrincipalID == principal.ID && evidence.ID != "" && evidence.Fingerprint != "" {
		if evidence.Class == "" {
			evidence.Class = "session"
		}
		return evidence, true
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
			if err == nil && credential.Token.ID != "" && credential.Token.TokenFingerprint != "" {
				return access.CredentialEvidence{
					Class: "api_token", ID: credential.Token.ID,
					Fingerprint: credential.Token.TokenFingerprint,
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
		session.RevokedAt != "" || session.TokenFingerprint == "" {
		return access.CredentialEvidence{}, false
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, session.ExpiresAt)
	if err != nil {
		return access.CredentialEvidence{}, false
	}
	return access.CredentialEvidence{
		Class: "session", ID: session.ID,
		Fingerprint: session.TokenFingerprint,
		PrincipalID: principal.ID, ExpiresAt: expiresAt.UTC(),
	}, true
}
