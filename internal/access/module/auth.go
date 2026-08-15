package module

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/httpauth"
	oidcauth "github.com/flidai/leapview/internal/access/oidc"
	api "github.com/flidai/leapview/internal/platform/http/model"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/csrf"
)

type principalContextKey struct{}
type apiCredentialContextKey struct{}

const csrfCookieName = "lv_csrf"
const oidcStateCookieName = "lv_oidc_state"
const authReturnCookieName = "lv_auth_return"
const oidcStateMaxAge = 10 * time.Minute
const oidcStateClockSkew = time.Minute
const maxAuthReturnTargetBytes = 2500

var (
	errUnauthorized = errors.New("unauthorized")
	errForbidden    = access.ErrForbidden
)

var (
	ErrUnauthorized = errUnauthorized
	ErrForbidden    = access.ErrForbidden
)

type Principal struct {
	ID          string               `json:"id"`
	Kind        access.PrincipalKind `json:"kind"`
	Email       string               `json:"email"`
	DisplayName string               `json:"displayName"`
	CreatedAt   string               `json:"createdAt"`
	UpdatedAt   string               `json:"updatedAt"`
	DevBypass   bool                 `json:"-"`
}

// IsHuman reports whether the principal represents an interactive person whose
// product usage may be counted. The local development bypass behaves as a
// human user; service principals and workload identities do not.
func (principal Principal) IsHuman() bool {
	return principal.DevBypass || principal.Kind == access.PrincipalKindUser
}

type oidcClient interface {
	AuthCodeURL(state, nonce string) string
	Authenticate(ctx context.Context, code, expectedNonce string) (oidcauth.Claims, error)
}

var authRandomReader io.Reader = rand.Reader
var authNow = time.Now

type sessionManager interface {
	CreateSession(ctx context.Context, principalID string, ttl time.Duration) (string, error)
	PrincipalForToken(ctx context.Context, token string) (access.Principal, error)
	DeleteSession(ctx context.Context, token string) error
}

type localCredentialManager interface {
	VerifyLocalPassword(ctx context.Context, email, password string) (access.Principal, access.LocalCredential, error)
	ChangeLocalPassword(ctx context.Context, principalID, currentPassword, newPassword string) (access.LocalCredential, error)
	LocalCredential(ctx context.Context, principalID string) (access.LocalCredential, error)
}

type disabledCredentialResolver interface {
	DisabledPrincipalForAPIToken(ctx context.Context, token string) (principalID, tokenID string, err error)
	DisabledPrincipalForSessionToken(ctx context.Context, token string) (principalID, sessionID string, err error)
}

type Auth struct {
	repo          access.Repository
	sessions      sessionManager
	devBypass     bool
	devAPIToken   string
	apiTokenOnly  bool
	localAuth     bool
	enabled       bool
	configured    bool
	azureTenant   string
	cookieSecure  bool
	csrf          func(http.Handler) http.Handler
	oidcRegistry  *oidcauth.Registry
	oidcOverride  map[string]oidcClient
	stateKey      []byte
	authoringAuth *access.AuthoringAuthService
}

type AuthConfig struct {
	Disabled        bool
	DevBypass       bool
	DevAPIToken     string
	APITokenOnly    bool
	LocalAuth       bool
	AzureClientID   string
	AzureSecret     string
	AzureCallback   string
	AzureTenant     string
	CSRFKey         string
	CookieSecure    bool
	BootstrapTenant string
	OIDCProviders   []OIDCProviderConfig
}

type OIDCProviderConfig struct {
	ID           string
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
}

func NewAuth(repo access.Repository, cfg AuthConfig) *Auth {
	auth := &Auth{
		repo:         repo,
		sessions:     repo,
		devBypass:    cfg.DevBypass,
		devAPIToken:  strings.TrimSpace(cfg.DevAPIToken),
		apiTokenOnly: cfg.APITokenOnly,
		localAuth:    cfg.LocalAuth,
		azureTenant:  cfg.AzureTenant,
		cookieSecure: cfg.CookieSecure,
	}
	providers := make([]oidcauth.Config, 0, len(cfg.OIDCProviders))
	for _, provider := range cfg.OIDCProviders {
		providers = append(providers, oidcauth.Config{
			ID: provider.ID, IssuerURL: provider.IssuerURL,
			ClientID: provider.ClientID, ClientSecret: provider.ClientSecret,
			RedirectURL: provider.RedirectURL, Scopes: append([]string(nil), provider.Scopes...),
		})
	}
	if cfg.AzureClientID != "" && cfg.AzureSecret != "" && cfg.AzureCallback != "" && !hasOIDCProvider(providers, "azureadv2") {
		providers = append(providers, oidcauth.AzureProviderConfig(cfg.AzureClientID, cfg.AzureSecret, cfg.AzureCallback, cfg.AzureTenant))
	}
	if registry, err := oidcauth.NewRegistry(providers); err == nil {
		auth.oidcRegistry = registry
		auth.configured = registry.Configured()
	}
	auth.csrf = csrf.Protect(
		csrfKey(cfg.CSRFKey),
		csrf.CookieName(csrfCookieName),
		csrf.Path("/"),
		csrf.Secure(cfg.CookieSecure),
		csrf.SameSite(csrf.SameSiteLaxMode),
		csrf.ErrorHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if wantsJSON(r) {
				writeJSONError(w, csrf.FailureReason(r), http.StatusForbidden)
				return
			}
			http.Error(w, csrf.FailureReason(r).Error(), http.StatusForbidden)
		})),
	)
	auth.stateKey = derivedSecret(cfg.CSRFKey, "oidc-state")
	auth.enabled = true
	return auth
}

func (a *Auth) acceptsPublicBearer(r *http.Request) bool {
	if a == nil || !a.devBypass {
		return true
	}
	want := a.devAPIToken
	if want == "" {
		want = "dev"
	}
	got := bearerToken(r)
	return got != "" && hmac.Equal([]byte(got), []byte(want))
}

func (a *Auth) AcceptsPublicBearer(r *http.Request) bool { return a.acceptsPublicBearer(r) }

func (a *Auth) DevBypass() bool { return a != nil && a.devBypass }

func hasOIDCProvider(providers []oidcauth.Config, id string) bool {
	id = oidcauth.ProviderID(id)
	for _, provider := range providers {
		if oidcauth.ProviderID(provider.ID) == id {
			return true
		}
	}
	return false
}

func (a *Auth) Enabled() bool {
	return a != nil && a.enabled
}

func (a *Auth) oidcClient(ctx context.Context, provider string) (oidcClient, oidcauth.Config, error) {
	provider = oidcauth.ProviderID(provider)
	if a.oidcOverride != nil {
		if client := a.oidcOverride[provider]; client != nil {
			var cfg oidcauth.Config
			if a.oidcRegistry != nil {
				cfg, _ = a.oidcRegistry.Config(provider)
			}
			cfg.ID = provider
			return client, cfg, nil
		}
	}
	if a.oidcRegistry == nil {
		return nil, oidcauth.Config{}, errors.New("oidc registry is not configured")
	}
	client, cfg, err := a.oidcRegistry.Client(ctx, provider)
	return client, cfg, err
}

func (a *Auth) Begin(w http.ResponseWriter, r *http.Request) {
	if !a.configured && !a.devBypass {
		http.Error(w, "OIDC auth is not configured", http.StatusServiceUnavailable)
		return
	}
	provider := chi.URLParam(r, "provider")
	if provider == "" {
		provider = "azureadv2"
	}
	client, _, err := a.oidcClient(r.Context(), provider)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	state, err := randomAuthValue()
	if err != nil {
		http.Error(w, "secure randomness unavailable", http.StatusInternalServerError)
		return
	}
	nonce, err := randomAuthValue()
	if err != nil {
		http.Error(w, "secure randomness unavailable", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, a.oidcStateCookie(state, nonce))
	http.Redirect(w, r, client.AuthCodeURL(state, nonce), http.StatusFound)
}

func (a *Auth) Callback(w http.ResponseWriter, r *http.Request) {
	if !a.configured && !a.devBypass {
		http.Error(w, "OIDC auth is not configured", http.StatusServiceUnavailable)
		return
	}
	provider := chi.URLParam(r, "provider")
	if provider == "" {
		provider = "azureadv2"
	}
	state, nonce, err := a.consumeOIDCState(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	if r.URL.Query().Get("state") != state {
		http.Error(w, "invalid oidc state", http.StatusUnauthorized)
		return
	}
	client, providerConfig, err := a.oidcClient(r.Context(), provider)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	claims, err := client.Authenticate(r.Context(), r.URL.Query().Get("code"), nonce)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	email := oidcEmail(claims)
	issuer := firstNonEmpty(claims.Issuer, providerConfig.IssuerURL, provider)
	var principal access.Principal
	var token string
	err = runAuthAuditedMutation(r, a.repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		principal, mutationErr = txRepo.ResolveExternalPrincipal(r.Context(), access.ExternalIdentityInput{
			Provider: "oidc", TenantID: issuer, Subject: stableSubject(claims.Subject, email), Email: email, DisplayName: oidcDisplayName(claims),
		})
		if mutationErr == nil {
			token, mutationErr = txRepo.CreateSession(r.Context(), principal.ID, 8*time.Hour)
		}
		return authAuditInput(r, "session.created", principal.ID, "", "session", "", "", "success", map[string]any{"provider": provider}), mutationErr
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	recordAccessAudit(r, a.repo, "sign_in", principal.ID, "", "principal", principal.ID, "", "success", map[string]any{"provider": provider})
	http.SetCookie(w, a.sessionCookie(token, time.Now().Add(8*time.Hour)))
	http.Redirect(w, r, a.authenticationRedirectTarget(w, r, "/"), http.StatusFound)
}

func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("lv_session"); err == nil {
		principal, _ := a.sessions.PrincipalForToken(r.Context(), cookie.Value)
		if err := runAuthAuditedMutation(r, a.repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
			mutationErr := txRepo.DeleteSession(r.Context(), cookie.Value)
			return authAuditInput(r, "session.revoked", principal.ID, "", "session", "", "", "success", nil), mutationErr
		}); err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		recordAccessAudit(r, a.repo, "sign_out", principal.ID, "", "principal", principal.ID, "", "success", nil)
	}
	http.SetCookie(w, &http.Cookie{Name: "lv_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: a.cookieSecure})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (a *Auth) LocalLogin(w http.ResponseWriter, r *http.Request) {
	if !a.localAuth {
		http.Error(w, "local auth is not enabled", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	email := access.NormalizeEmail(firstNonEmpty(r.Form.Get("email"), r.Form.Get("username")))
	password := r.Form.Get("password")
	local, ok := a.repo.(localCredentialManager)
	if !ok {
		http.Error(w, "local auth repository is not configured", http.StatusServiceUnavailable)
		return
	}
	principal, credential, err := local.VerifyLocalPassword(r.Context(), email, password)
	if err != nil {
		recordAccessAudit(r, a.repo, "sign_in", "", "", "principal", "", "", "denied", map[string]any{"provider": "local", "email": email})
		if wantsJSON(r) {
			writeJSONError(w, errUnauthorized, http.StatusUnauthorized)
			return
		}
		http.Error(w, errUnauthorized.Error(), http.StatusUnauthorized)
		return
	}
	var token string
	err = runAuthAuditedMutation(r, a.repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		var mutationErr error
		token, mutationErr = txRepo.CreateSession(r.Context(), principal.ID, 8*time.Hour)
		return authAuditInput(r, "session.created", principal.ID, "", "session", "", "", "success", map[string]any{"provider": "local"}), mutationErr
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	recordAccessAudit(r, a.repo, "sign_in", principal.ID, "", "principal", principal.ID, "", "success", map[string]any{"provider": "local"})
	http.SetCookie(w, a.sessionCookie(token, time.Now().Add(8*time.Hour)))
	if credential.MustChangePassword {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	http.Redirect(w, r, a.authenticationRedirectTarget(w, r, "/"), http.StatusFound)
}

func (a *Auth) LocalPassword(w http.ResponseWriter, r *http.Request) {
	if !a.localAuth {
		http.Error(w, "local auth is not enabled", http.StatusServiceUnavailable)
		return
	}
	principal, _, ok := a.authenticate(r)
	if !ok {
		if wantsJSON(r) {
			writeJSONError(w, errUnauthorized, http.StatusUnauthorized)
			return
		}
		http.Error(w, errUnauthorized.Error(), http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, ok = a.repo.(localCredentialManager)
	if !ok {
		http.Error(w, "local auth repository is not configured", http.StatusServiceUnavailable)
		return
	}
	err := runAuthAuditedMutation(r, a.repo, func(txRepo access.Repository) (access.AuditEventInput, error) {
		_, mutationErr := txRepo.ChangeLocalPassword(r.Context(), principal.ID, firstNonEmpty(r.Form.Get("currentPassword"), r.Form.Get("current_password")), firstNonEmpty(r.Form.Get("newPassword"), r.Form.Get("new_password")))
		return authAuditInput(r, "password.changed", principal.ID, "", "principal", principal.ID, "", "success", map[string]any{"provider": "local"}), mutationErr
	})
	if err != nil {
		if wantsJSON(r) {
			writeJSONError(w, errUnauthorized, http.StatusUnauthorized)
			return
		}
		http.Error(w, errUnauthorized.Error(), http.StatusUnauthorized)
		return
	}
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "changed"})
		return
	}
	http.Redirect(w, r, a.authenticationRedirectTarget(w, r, "/"), http.StatusFound)
}

func (a *Auth) Principal(r *http.Request) (Principal, bool) {
	if value, ok := r.Context().Value(principalContextKey{}).(Principal); ok {
		return value, true
	}
	return Principal{}, false
}

func (a *Auth) APICredential(r *http.Request) (access.APICredential, bool) {
	if value, ok := r.Context().Value(apiCredentialContextKey{}).(access.APICredential); ok {
		return value, true
	}
	return access.APICredential{}, false
}

func (a *Auth) Middleware(privilege access.Privilege, next http.Handler) http.Handler {
	return a.MiddlewareWithObjectResolver(privilege, nil, next)
}

func (a *Auth) MiddlewareWithObjectResolver(privilege access.Privilege, objectResolver httpauth.ObjectResolver, next http.Handler) http.Handler {
	if !a.Enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(access.WithAuthorizationCache(r.Context()))
		principal, credential, ok := a.authenticate(r)
		if !ok {
			if a.apiTokenOnly {
				writeBearerChallenge(w, r)
				return
			}
			if wantsJSON(r) {
				writeJSONError(w, errUnauthorized, http.StatusUnauthorized)
				return
			}
			if target := authenticationReturnTarget(r); target != "" {
				http.SetCookie(w, a.authReturnCookie(target))
			}
			http.Redirect(w, r, a.defaultLoginRedirect(), http.StatusFound)
			return
		}
		if a.mustChangeLocalPassword(r, principal.ID, credential) {
			writeAuthError(w, r, errForbidden, http.StatusForbidden)
			return
		}
		if privilege != "" {
			workspaceID := a.privilegeWorkspaceID(r)
			objects := httpauth.ObjectsForRequest(privilege, r, workspaceID)
			concealDenied := false
			if objectResolver != nil && privilege != access.PrivilegeManagePlatform {
				if resolved := objectResolver(r, workspaceID); len(resolved) > 0 {
					objects = resolved
					concealDenied = resolved[0].Type != access.SecurablePlatform && resolved[0].Type != access.SecurableWorkspace
				}
			}
			// A non-global privilege must never fall back to a process-wide
			// workspace. If the request did not carry a workspace scope and no
			// explicit resolver supplied a valid object, fail closed.
			if len(objects) == 0 || hasInvalidAuthorizationObject(objects) {
				writeAuthError(w, r, errForbidden, http.StatusForbidden)
				return
			}
			if credential != nil && credential.Authoring != nil {
				projectID := authoringProjectScope(r, credential.Authoring)
				if err := credential.Authoring.Scope.Authorize(a.authoringAuth.InstanceID(), projectID, privilege); err != nil {
					status := http.StatusForbidden
					if concealDenied && strings.HasPrefix(r.URL.Path, "/api/v1/") {
						status = http.StatusNotFound
					}
					recordAuthorizationDenial(r, a.repo, principal.ID, workspaceID, privilege, objects, access.ReasonMissingPrivilege)
					writeAuthError(w, r, errForbidden, status)
					return
				}
			} else if credential != nil && !apiTokenAllows((*credential).Token, workspaceID, privilege) {
				status := http.StatusForbidden
				if concealDenied && strings.HasPrefix(r.URL.Path, "/api/v1/") {
					status = http.StatusNotFound
				}
				recordAuthorizationDenial(r, a.repo, principal.ID, workspaceID, privilege, objects, access.ReasonMissingPrivilege)
				writeAuthError(w, r, errForbidden, status)
				return
			}
			decision, err := a.repo.AuthorizeAny(r.Context(), principal.ID, privilege, objects)
			if err != nil {
				writeAuthError(w, r, err, http.StatusInternalServerError)
				return
			}
			if !decision.Allowed && httpauth.CanDeferDataAuth(privilege) {
				useDecision, err := a.repo.Authorize(r.Context(), principal.ID, access.PrivilegeUseWorkspace, httpauth.ObjectForWorkspace(workspaceID))
				if err != nil {
					writeAuthError(w, r, err, http.StatusInternalServerError)
					return
				}
				if useDecision.Allowed {
					decision.Allowed = true
				}
			}
			if !decision.Allowed && httpauth.CanDeferDataAuth(privilege) {
				viewDecision, err := a.repo.AuthorizeAny(r.Context(), principal.ID, access.PrivilegeViewItem, objects)
				if err != nil {
					writeAuthError(w, r, err, http.StatusInternalServerError)
					return
				}
				if viewDecision.Allowed {
					decision.Allowed = true
				}
			}
			if !decision.Allowed && httpauth.RouteCanDeferGrantManagement(privilege, r) {
				useDecision, err := a.repo.Authorize(r.Context(), principal.ID, access.PrivilegeUseWorkspace, httpauth.ObjectForWorkspace(workspaceID))
				if err != nil {
					writeAuthError(w, r, err, http.StatusInternalServerError)
					return
				}
				if useDecision.Allowed {
					decision.Allowed = true
				}
			}
			if !decision.Allowed {
				status := http.StatusForbidden
				// Public object routes deliberately conceal whether an inaccessible
				// identifier exists. Collection and platform authorization failures
				// remain explicit 403 responses.
				if concealDenied && strings.HasPrefix(r.URL.Path, "/api/v1/") {
					status = http.StatusNotFound
				}
				recordAuthorizationDenial(r, a.repo, principal.ID, workspaceID, privilege, objects, decision.Reason)
				writeAuthError(w, r, errForbidden, status)
				return
			}
			if connectionAuthorizationAuditRequired(privilege) {
				if err := recordAuthorizationAllowed(
					r, a.repo, principal.ID, workspaceID, privilege, objects,
				); err != nil {
					writeAuthError(w, r, err, http.StatusInternalServerError)
					return
				}
			}
		}
		ctx := context.WithValue(r.Context(), principalContextKey{}, principal)
		if credential != nil {
			ctx = context.WithValue(ctx, apiCredentialContextKey{}, *credential)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func hasInvalidAuthorizationObject(objects []access.ObjectRef) bool {
	for _, object := range objects {
		if object.Type == "" {
			return true
		}
		if object.Type == access.SecurableWorkspace && strings.TrimSpace(object.WorkspaceID) == "" {
			return true
		}
	}
	return false
}

func authoringProjectScope(
	request *http.Request,
	session *access.AuthoringSession,
) string {
	projectID := strings.TrimSpace(chi.URLParam(request, "project"))
	if projectID == "" && session != nil {
		projectID = strings.TrimSpace(session.Scope.ProjectID)
	}
	return projectID
}

func (a *Auth) defaultLoginRedirect() string {
	if a.localAuth {
		return "/login"
	}
	return "/auth/azureadv2"
}

func (a *Auth) mustChangeLocalPassword(r *http.Request, principalID string, credential *access.APICredential) bool {
	if !a.localAuth || r.URL.Path == "/auth/local/password" || r.URL.Path == "/auth/logout" {
		return false
	}
	if credential != nil && credential.Token.Name == access.APITokenNameInitialPublisher {
		return false
	}
	local, ok := a.repo.(localCredentialManager)
	if !ok {
		return false
	}
	localCredential, err := local.LocalCredential(r.Context(), principalID)
	return err == nil && localCredential.MustChangePassword
}

func writeBearerChallenge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="leapview"`)
	if strings.HasPrefix(r.URL.Path, "/api/v1/") {
		writeAPIProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "A valid bearer credential is required", nil)
		return
	}
	if wantsJSON(r) {
		writeJSONError(w, errUnauthorized, http.StatusUnauthorized)
		return
	}
	http.Error(w, "API bearer token required", http.StatusUnauthorized)
}

func writeAuthError(w http.ResponseWriter, r *http.Request, err error, status int) {
	if strings.HasPrefix(r.URL.Path, "/api/v1/") {
		code := "AUTHORIZATION_DENIED"
		if status == http.StatusUnauthorized {
			code = "AUTHENTICATION_REQUIRED"
		} else if status >= http.StatusInternalServerError {
			code = "AUTHORIZATION_UNAVAILABLE"
		} else if status == http.StatusNotFound {
			code = "RESOURCE_NOT_FOUND"
		}
		writeAPIProblem(w, r, status, code, http.StatusText(status), nil)
		return
	}
	if wantsJSON(r) {
		writeJSONError(w, err, status)
		return
	}
	http.Error(w, err.Error(), status)
}

func recordAccessAudit(r *http.Request, repo access.Repository, action, principalID, workspaceID, targetType, targetID string, privilege access.Privilege, status string, metadata map[string]any) {
	if repo == nil {
		return
	}
	_ = access.PersistAuditEvent(r.Context(), repo, authAuditInput(r, action, principalID, workspaceID, targetType, targetID, privilege, status, metadata))
}

func recordAuthorizationDenial(r *http.Request, repo access.Repository, principalID, workspaceID string, privilege access.Privilege, objects []access.ObjectRef, reason access.AuthorizationReason) {
	if repo == nil {
		return
	}
	_ = access.PersistAuditEvent(r.Context(), repo, authorizationDenialAuditInput(r, principalID, workspaceID, privilege, objects, reason))
}

func recordAuthorizationAllowed(
	r *http.Request,
	repo access.Repository,
	principalID string,
	workspaceID string,
	privilege access.Privilege,
	objects []access.ObjectRef,
) error {
	if repo == nil {
		return nil
	}
	return access.PersistAuditEvent(
		r.Context(),
		repo,
		authorizationAllowedAuditInput(r, principalID, workspaceID, privilege, objects),
	)
}

func authorizationAllowedAuditInput(
	r *http.Request,
	principalID string,
	workspaceID string,
	privilege access.Privilege,
	objects []access.ObjectRef,
) access.AuditEventInput {
	object := access.WorkspaceObject(workspaceID)
	if len(objects) > 0 {
		object = objects[0]
	}
	return authAuditInput(
		r,
		"authorization.allowed",
		principalID,
		workspaceID,
		string(object.Type),
		object.CanonicalID(),
		privilege,
		"allowed",
		map[string]any{"reason": "granted"},
	)
}

func connectionAuthorizationAuditRequired(privilege access.Privilege) bool {
	return privilege == access.PrivilegeManageConnectionMetadata ||
		privilege == access.PrivilegeTestConnection ||
		privilege == access.PrivilegeViewConnectionHealth
}

func authorizationDenialAuditInput(r *http.Request, principalID, workspaceID string, privilege access.Privilege, objects []access.ObjectRef, reason access.AuthorizationReason) access.AuditEventInput {
	object := access.WorkspaceObject(workspaceID)
	if len(objects) > 0 {
		object = objects[0]
	}
	if reason == "" {
		reason = access.ReasonNoGrant
	}
	return authAuditInput(
		r,
		"authorization.denied",
		principalID,
		workspaceID,
		string(object.Type),
		object.CanonicalID(),
		privilege,
		"denied",
		map[string]any{"reason": string(reason)},
	)
}

func authAuditInput(r *http.Request, action, principalID, workspaceID, targetType, targetID string, privilege access.Privilege, status string, metadata map[string]any) access.AuditEventInput {
	if metadata == nil {
		metadata = map[string]any{}
	}
	bytes, _ := json.Marshal(metadata)
	return access.AuditEventInput{
		WorkspaceID:   workspaceID,
		PrincipalID:   principalID,
		Action:        action,
		TargetType:    targetType,
		TargetID:      targetID,
		Privilege:     privilege,
		Status:        status,
		RequestID:     firstNonEmpty(r.Header.Get("X-Request-Id"), r.Header.Get("X-Request-ID")),
		CorrelationID: firstNonEmpty(r.Header.Get("X-Correlation-Id"), r.Header.Get("X-Correlation-ID"), r.Header.Get("X-Request-Id"), r.Header.Get("X-Request-ID")),
		MetadataJSON:  string(bytes),
	}
}

func runAuthAuditedMutation(r *http.Request, repo access.Repository, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	if transactional, ok := repo.(access.AuditedMutationRepository); ok {
		return transactional.RunAuditedMutation(r.Context(), mutation)
	}
	input, err := mutation(repo)
	if err != nil {
		return err
	}
	return access.PersistAuditEvent(r.Context(), repo, input)
}

func (a *Auth) privilegeWorkspaceID(r *http.Request) string {
	if workspaceID := strings.TrimSpace(chi.URLParam(r, "workspace")); workspaceID != "" {
		return workspaceID
	}
	if workspaceID := strings.TrimSpace(r.URL.Query().Get("workspace")); workspaceID != "" {
		if workspaceID == "platform" && r.URL.Path == "/updates" {
			return ""
		}
		return workspaceID
	}
	// Connection-asset streams carry the owning workspace under this explicit
	// name because the visible route itself is project-global.
	if workspaceID := strings.TrimSpace(r.URL.Query().Get("assetWorkspace")); workspaceID != "" {
		return workspaceID
	}
	return ""
}

func (a *Auth) CSRFMiddleware(next http.Handler) http.Handler {
	if a == nil || a.csrf == nil {
		return next
	}
	protected := a.csrf(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bearerToken(r) != "" && !hasSessionCookie(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !a.cookieSecure && r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" {
			r = csrf.PlaintextHTTPRequest(r)
		}
		protected.ServeHTTP(w, r)
	})
}

func (a *Auth) authenticate(r *http.Request) (Principal, *access.APICredential, bool) {
	if a.devBypass {
		return localDeveloperPrincipal(), nil, true
	}
	if token := bearerToken(r); token != "" {
		return a.authenticateBearer(r)
	}
	if a.apiTokenOnly {
		return Principal{}, nil, false
	}
	cookie, err := r.Cookie("lv_session")
	if err != nil || cookie.Value == "" {
		return Principal{}, nil, false
	}
	principal, err := a.sessions.PrincipalForToken(r.Context(), cookie.Value)
	if err != nil {
		a.auditDisabledCredentialFailure(r, "session", cookie.Value)
		return Principal{}, nil, false
	}
	return Principal{ID: principal.ID, Kind: principal.Kind, Email: principal.Email, DisplayName: principal.DisplayName, CreatedAt: principal.CreatedAt, UpdatedAt: principal.UpdatedAt}, nil, true
}

// authenticateBearer resolves LeapView REST API tokens. MCP OAuth tokens use
// the resource-server verifier and never enter the REST credential path.
func (a *Auth) authenticateBearer(r *http.Request) (Principal, *access.APICredential, bool) {
	if a == nil || bearerToken(r) == "" {
		return Principal{}, nil, false
	}
	if a.devBypass {
		if !a.acceptsPublicBearer(r) {
			return Principal{}, nil, false
		}
		return localDeveloperPrincipal(), nil, true
	}
	token := bearerToken(r)
	credential, err := a.repo.CredentialForAPIToken(r.Context(), token)
	if err == nil {
		principal := credential.Principal
		return Principal{ID: principal.ID, Kind: principal.Kind, Email: principal.Email, DisplayName: principal.DisplayName, CreatedAt: principal.CreatedAt, UpdatedAt: principal.UpdatedAt}, &credential, true
	}
	if a.authoringAuth != nil {
		authoringCredential, authoringErr := a.authoringAuth.Resolve(r.Context(), token)
		if authoringErr == nil {
			principal := authoringCredential.Principal
			credential = access.APICredential{
				Principal: principal,
				Token: access.APIToken{
					ID: authoringCredential.ID, PrincipalID: principal.ID,
					Name:       "authoring:" + string(authoringCredential.Session.Kind),
					Privileges: append([]access.Privilege(nil), authoringCredential.Session.Scope.Privileges...),
					ExpiresAt:  authoringCredential.AccessExpiresAt.UTC().Format(time.RFC3339),
				},
				Authoring: &authoringCredential.Session,
			}
			return Principal{ID: principal.ID, Kind: principal.Kind, Email: principal.Email, DisplayName: principal.DisplayName, CreatedAt: principal.CreatedAt, UpdatedAt: principal.UpdatedAt}, &credential, true
		}
	}
	a.auditDisabledCredentialFailure(r, "api_token", token)
	return Principal{}, nil, false
}

func hasSessionCookie(r *http.Request) bool {
	cookie, err := r.Cookie("lv_session")
	return err == nil && strings.TrimSpace(cookie.Value) != ""
}

func (a *Auth) auditDisabledCredentialFailure(r *http.Request, credentialType, secret string) {
	resolver, ok := a.repo.(disabledCredentialResolver)
	if !ok || strings.TrimSpace(secret) == "" {
		return
	}
	var principalID, targetID string
	var err error
	switch credentialType {
	case "api_token":
		principalID, targetID, err = resolver.DisabledPrincipalForAPIToken(r.Context(), secret)
	case "session":
		principalID, targetID, err = resolver.DisabledPrincipalForSessionToken(r.Context(), secret)
	default:
		return
	}
	if err != nil || principalID == "" {
		return
	}
	recordAccessAudit(r, a.repo, "credential.denied", principalID, "", credentialType, targetID, "", "denied", map[string]any{"reason": "principal_disabled"})
}

func apiTokenAllows(token access.APIToken, workspaceID string, privilege access.Privilege) bool {
	return access.TokenAllows(token, workspaceID, privilege)
}

func (a *Auth) sessionCookie(token string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     "lv_session",
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.cookieSecure,
	}
}

func wantsJSON(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/") || strings.Contains(r.Header.Get("Accept"), "application/json")
}

func oidcDisplayName(claims oidcauth.Claims) string {
	if claims.Name != "" {
		return claims.Name
	}
	if claims.PreferredUsername != "" {
		return claims.PreferredUsername
	}
	return claims.Email
}

func oidcEmail(claims oidcauth.Claims) string {
	if claims.Email != "" {
		return claims.Email
	}
	return claims.PreferredUsername
}

func stableSubject(subject, fallback string) string {
	if subject != "" {
		return subject
	}
	return fallback
}

func bearerToken(r *http.Request) string {
	fields := strings.Fields(r.Header.Get("Authorization"))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return ""
	}
	return fields[1]
}

func csrfKey(value string) []byte {
	if len(value) >= 32 {
		return []byte(value)[:32]
	}
	seed := access.PrincipalIDForEmail("csrf:" + value)
	key := make([]byte, 32)
	copy(key, []byte(seed))
	return key
}

func (a *Auth) oidcStateCookie(state, nonce string) *http.Cookie {
	return &http.Cookie{
		Name:     oidcStateCookieName,
		Value:    a.encodeOIDCState(state, nonce),
		Path:     "/",
		MaxAge:   int(oidcStateMaxAge / time.Second),
		HttpOnly: true,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

func authenticationReturnTarget(r *http.Request) string {
	if r == nil || r.Method != http.MethodGet || r.URL == nil || !isAuthenticationReturnPath(r.URL.Path) {
		return ""
	}
	target := r.URL.RequestURI()
	if len(target) == 0 || len(target) > maxAuthReturnTargetBytes {
		return ""
	}
	return target
}

func (a *Auth) authReturnCookie(target string) *http.Cookie {
	return &http.Cookie{
		Name:     authReturnCookieName,
		Value:    a.encodeAuthReturn(target, authNow()),
		Path:     "/",
		MaxAge:   int(oidcStateMaxAge / time.Second),
		HttpOnly: true,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

func (a *Auth) authenticationRedirectTarget(w http.ResponseWriter, r *http.Request, fallback string) string {
	cookie, err := r.Cookie(authReturnCookieName)
	if err != nil {
		return fallback
	}
	http.SetCookie(w, &http.Cookie{
		Name: authReturnCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: a.cookieSecure, SameSite: http.SameSiteLaxMode,
	})
	target, err := a.decodeAuthReturn(cookie.Value, authNow())
	if err != nil {
		return fallback
	}
	return target
}

func (a *Auth) encodeAuthReturn(target string, issuedAt time.Time) string {
	encoded := base64.RawURLEncoding.EncodeToString([]byte(target))
	issuedUnix := strconv.FormatInt(issuedAt.UTC().Unix(), 10)
	message := encoded + "|" + issuedUnix
	mac := hmac.New(sha256.New, a.stateKey)
	mac.Write([]byte(message))
	return encoded + "." + issuedUnix + "." + hex.EncodeToString(mac.Sum(nil))
}

func (a *Auth) decodeAuthReturn(value string, now time.Time) (string, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return "", errors.New("invalid authentication return cookie")
	}
	issuedUnix, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", errors.New("invalid authentication return cookie timestamp")
	}
	issuedAt := time.Unix(issuedUnix, 0).UTC()
	now = now.UTC()
	if issuedAt.After(now.Add(oidcStateClockSkew)) || !issuedAt.Add(oidcStateMaxAge).After(now) {
		return "", errors.New("authentication return cookie expired")
	}
	message := parts[0] + "|" + parts[1]
	mac := hmac.New(sha256.New, a.stateKey)
	mac.Write([]byte(message))
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[2]), []byte(want)) {
		return "", errors.New("invalid authentication return cookie signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", errors.New("invalid authentication return cookie encoding")
	}
	target := string(raw)
	parsed, err := url.Parse(target)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !isAuthenticationReturnPath(parsed.Path) || len(target) > maxAuthReturnTargetBytes {
		return "", errors.New("invalid authentication return target")
	}
	return target, nil
}

func isAuthenticationReturnPath(path string) bool {
	return path == "/oauth/authorize" || path == DesktopAuthorizePath
}

func (a *Auth) consumeOIDCState(w http.ResponseWriter, r *http.Request) (string, string, error) {
	cookie, err := r.Cookie(oidcStateCookieName)
	if err != nil {
		return "", "", err
	}
	http.SetCookie(w, &http.Cookie{Name: oidcStateCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: a.cookieSecure})
	return a.decodeOIDCState(cookie.Value)
}

func (a *Auth) encodeOIDCState(state, nonce string) string {
	return a.encodeOIDCStateAt(state, nonce, authNow())
}

func (a *Auth) encodeOIDCStateAt(state, nonce string, issuedAt time.Time) string {
	issuedUnix := strconv.FormatInt(issuedAt.UTC().Unix(), 10)
	message := state + "|" + nonce + "|" + issuedUnix
	mac := hmac.New(sha256.New, a.stateKey)
	mac.Write([]byte(message))
	return state + "." + nonce + "." + issuedUnix + "." + hex.EncodeToString(mac.Sum(nil))
}

func (a *Auth) decodeOIDCState(value string) (string, string, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 4 {
		return "", "", errors.New("invalid oidc state cookie")
	}
	issuedUnix, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return "", "", errors.New("invalid oidc state cookie timestamp")
	}
	issuedAt := time.Unix(issuedUnix, 0).UTC()
	now := authNow().UTC()
	if issuedAt.After(now.Add(oidcStateClockSkew)) {
		return "", "", errors.New("oidc state cookie is not yet valid")
	}
	if !issuedAt.Add(oidcStateMaxAge).After(now) {
		return "", "", errors.New("oidc state cookie expired")
	}
	expected := a.encodeOIDCStateAt(parts[0], parts[1], issuedAt)
	if !hmac.Equal([]byte(value), []byte(expected)) {
		return "", "", errors.New("invalid oidc state cookie signature")
	}
	return parts[0], parts[1], nil
}

func derivedSecret(secret, purpose string) []byte {
	base := csrfKey(secret)
	sum := sha256.Sum256(append([]byte("leapview:"+purpose+":"), base...))
	return sum[:]
}

func (a *Auth) mcpOAuthSecret() []byte {
	if a == nil {
		return nil
	}
	sum := sha256.Sum256(append([]byte("leapview:mcp-oauth:"), a.stateKey...))
	return sum[:]
}

func (a *Auth) MCPOAuthSecret() []byte { return a.mcpOAuthSecret() }

func randomAuthValue() (string, error) {
	var b [32]byte
	if _, err := io.ReadFull(authRandomReader, b[:]); err != nil {
		return "", fmt.Errorf("read secure random bytes: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func setAuthRandomReaderForTest(reader io.Reader) func() {
	previous := authRandomReader
	authRandomReader = reader
	return func() {
		authRandomReader = previous
	}
}

func setAuthNowForTest(now time.Time) func() {
	previous := authNow
	authNow = func() time.Time { return now }
	return func() {
		authNow = previous
	}
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}

func APICredentialFromContext(ctx context.Context) (access.APICredential, bool) {
	credential, ok := ctx.Value(apiCredentialContextKey{}).(access.APICredential)
	return credential, ok
}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

func WithAPICredential(ctx context.Context, credential access.APICredential) context.Context {
	return context.WithValue(ctx, apiCredentialContextKey{}, credential)
}

func LocalDeveloperPrincipal() Principal {
	return Principal{ID: "dev", Email: "dev@localhost", DisplayName: "Local Developer", DevBypass: true}
}

func TokenAllows(token access.APIToken, workspaceID string, privilege access.Privilege) bool {
	return apiTokenAllows(token, workspaceID, privilege)
}

func BearerToken(r *http.Request) string { return bearerToken(r) }

func WriteAuthError(w http.ResponseWriter, r *http.Request, err error, status int) {
	writeAuthError(w, r, err, status)
}

func WriteBearerChallenge(w http.ResponseWriter, r *http.Request) { writeBearerChallenge(w, r) }

func writeJSONError(w http.ResponseWriter, err error, status int) {
	writeJSON(w, status, api.ErrorResponse{Code: status, Message: err.Error(), Details: map[string]any{}, RequestID: ""})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAPIProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string, violations []api.ProblemFieldError) {
	if violations == nil {
		violations = []api.ProblemFieldError{}
	}
	requestID := r.Header.Get("X-Request-ID")
	if requestID == "" {
		requestID = w.Header().Get("X-Request-ID")
	}
	if requestID == "" {
		var value [16]byte
		if _, err := rand.Read(value[:]); err != nil {
			requestID = "req_unavailable"
		} else {
			requestID = "req_" + hex.EncodeToString(value[:])
		}
		r.Header.Set("X-Request-ID", requestID)
	}
	w.Header().Set("X-Request-ID", requestID)
	w.Header().Set("Content-Type", "application/problem+json")
	writeJSON(w, status, api.ProblemDetails{
		Type: "https://leapview.dev/problems/" + strings.ToLower(code), Title: http.StatusText(status), Status: int32(status),
		Detail: detail, Instance: r.URL.Path, Code: code, RequestID: requestID, Errors: violations,
	})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func localDeveloperPrincipal() Principal { return LocalDeveloperPrincipal() }

func (a *Auth) Authenticate(r *http.Request) (Principal, *access.APICredential, bool) {
	return a.authenticate(r)
}

func (a *Auth) MustChangeLocalPassword(r *http.Request, principalID string) bool {
	return a.mustChangeLocalPassword(r, principalID, nil)
}

func (a *Auth) SessionCookie(token string, expires time.Time) *http.Cookie {
	return a.sessionCookie(token, expires)
}

func (a *Auth) OIDCStateCookie(state, nonce string) *http.Cookie {
	return a.oidcStateCookie(state, nonce)
}

func (a *Auth) DecodeOIDCState(value string) (string, string, error) {
	return a.decodeOIDCState(value)
}

func (a *Auth) AuthReturnCookie(target string) *http.Cookie { return a.authReturnCookie(target) }

func (a *Auth) AuthenticationRedirectTarget(w http.ResponseWriter, r *http.Request, fallback string) string {
	return a.authenticationRedirectTarget(w, r, fallback)
}

func (a *Auth) ConfigureOIDCTestClients(clients map[string]OIDCClient) {
	a.configured = true
	a.oidcOverride = clients
}

type OIDCClient = oidcClient

const AuthReturnCookieName = authReturnCookieName
const CSRFCookieName = csrfCookieName
const OIDCStateCookieName = oidcStateCookieName

func (a *Auth) LocalAuthEnabled() bool { return a != nil && a.localAuth }

func (a *Auth) SSOConfigured() bool { return a != nil && a.configured }

func SetAuthRandomReaderForTest(reader io.Reader) func() { return setAuthRandomReaderForTest(reader) }

func SetAuthNowForTest(now time.Time) func() { return setAuthNowForTest(now) }
