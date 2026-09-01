package module

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/graph"
)

const authoringDeviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

func (m *Module) AuthoringDeviceAuthorization(w http.ResponseWriter, r *http.Request) {
	service, ok := m.authoringOAuthService(w)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeAuthoringOAuthError(w, http.StatusBadRequest, "invalid_request", "invalid device authorization request")
		return
	}
	if r.Form.Get("client_id") != access.AuthoringCLIClientID {
		writeAuthoringOAuthError(w, http.StatusUnauthorized, "invalid_client", "unknown device client")
		return
	}
	capabilities, err := authoringOAuthCapabilities(r.Form.Get("scope"))
	if err != nil {
		writeAuthoringOAuthError(w, http.StatusBadRequest, "invalid_scope", err.Error())
		return
	}
	scope, err := m.authoringOAuthScope(r.Context(), service.InstanceID(), r.Form.Get("project_id"), capabilities)
	if err != nil {
		writeAuthoringOAuthScopeError(w, err)
		return
	}
	response, err := service.BeginDeviceAuthorization(r.Context(), scope)
	if err != nil {
		writeAuthoringOAuthServiceError(w, err)
		return
	}
	setAuthoringOAuthNoStore(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               response.DeviceCode,
		"user_code":                 response.UserCode,
		"verification_uri":          response.VerificationURI,
		"verification_uri_complete": response.VerificationURIComplete,
		"expires_in":                response.ExpiresIn,
		"interval":                  response.Interval,
	})
}

func (m *Module) AuthoringOAuthToken(w http.ResponseWriter, r *http.Request) {
	service, ok := m.authoringOAuthService(w)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeAuthoringOAuthError(w, http.StatusBadRequest, "invalid_request", "invalid token request")
		return
	}
	var (
		tokens access.AuthoringTokenSet
		err    error
	)
	switch r.Form.Get("grant_type") {
	case authoringDeviceGrantType:
		if !requireAuthoringCLIClient(w, r) {
			return
		}
		tokens, err = service.ExchangeDeviceCode(r.Context(), r.Form.Get("device_code"))
	case "refresh_token":
		if !requireAuthoringCLIClient(w, r) {
			return
		}
		tokens, err = service.Refresh(r.Context(), r.Form.Get("refresh_token"))
	case "client_credentials":
		tokens, err = m.exchangeAuthoringClientCredentials(r, service)
	default:
		writeAuthoringOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "unsupported authoring grant type")
		return
	}
	if err != nil {
		writeAuthoringOAuthServiceError(w, err)
		return
	}
	writeAuthoringOAuthToken(w, tokens)
}

func (m *Module) AuthoringOAuthRevoke(w http.ResponseWriter, r *http.Request) {
	service, ok := m.authoringOAuthService(w)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeAuthoringOAuthError(w, http.StatusBadRequest, "invalid_request", "invalid revocation request")
		return
	}
	if !requireAuthoringCLIClient(w, r) {
		return
	}
	if hint := r.Form.Get("token_type_hint"); hint != "" && hint != "access_token" {
		writeAuthoringOAuthError(w, http.StatusBadRequest, "unsupported_token_type", "unsupported authoring token type")
		return
	}
	err := service.RevokeAccessToken(r.Context(), r.Form.Get("token"))
	if err != nil && !errors.Is(err, access.ErrInvalidAuthoringCredential) {
		writeAuthoringOAuthError(w, http.StatusInternalServerError, "server_error", "authoring token revocation failed")
		return
	}
	setAuthoringOAuthNoStore(w)
	w.WriteHeader(http.StatusOK)
}

func requireAuthoringCLIClient(w http.ResponseWriter, r *http.Request) bool {
	if r.Form.Get("client_id") == access.AuthoringCLIClientID {
		return true
	}
	writeAuthoringOAuthError(w, http.StatusUnauthorized, "invalid_client", "unknown authoring client")
	return false
}

func (m *Module) exchangeAuthoringClientCredentials(r *http.Request, service authoringOAuthAuthentication) (access.AuthoringTokenSet, error) {
	capabilities, err := authoringOAuthCapabilities(r.Form.Get("scope"))
	if err != nil {
		return access.AuthoringTokenSet{}, fmt.Errorf("%w: %v", access.ErrAuthoringScopeDenied, err)
	}
	scope, err := m.authoringOAuthScope(r.Context(), service.InstanceID(), r.Form.Get("project_id"), capabilities)
	if err != nil {
		return access.AuthoringTokenSet{}, err
	}
	lifetimeSeconds, err := strconv.ParseInt(r.Form.Get("lifetime_seconds"), 10, 64)
	if err != nil || lifetimeSeconds <= 0 || lifetimeSeconds > math.MaxInt64/int64(time.Second) {
		return access.AuthoringTokenSet{}, access.ErrInvalidWorkloadLifetime
	}
	return service.ExchangeWorkloadIdentity(r.Context(), access.WorkloadIdentityInput{
		ClientID:     r.Form.Get("client_id"),
		ClientSecret: r.Form.Get("client_secret"),
		Scope:        scope,
		Lifetime:     time.Duration(lifetimeSeconds) * time.Second,
	})
}

func writeAuthoringOAuthToken(w http.ResponseWriter, tokens access.AuthoringTokenSet) {
	capabilities := make([]string, len(tokens.Session.Scope.Capabilities))
	for index, capability := range tokens.Session.Scope.Capabilities {
		capabilities[index] = string(capability)
	}
	setAuthoringOAuthNoStore(w)
	response := map[string]any{
		"access_token": tokens.AccessToken,
		"token_type":   tokens.TokenType,
		"expires_in":   tokens.ExpiresIn,
		"session_id":   tokens.Session.ID,
		"session_kind": tokens.Session.Kind,
		"target_id":    tokens.Session.Scope.TargetID,
		"project_id":   tokens.Session.Scope.ProjectID.String(),
		"scope":        strings.Join(capabilities, " "),
	}
	if tokens.RefreshToken != "" {
		response["refresh_token"] = tokens.RefreshToken
	}
	writeJSON(w, http.StatusOK, response)
}

type authoringOAuthAuthentication interface {
	InstanceID() string
	BeginDeviceAuthorization(context.Context, access.AuthoringScope) (access.DeviceAuthorizationResponse, error)
	ExchangeDeviceCode(context.Context, string) (access.AuthoringTokenSet, error)
	Refresh(context.Context, string) (access.AuthoringTokenSet, error)
	ExchangeWorkloadIdentity(context.Context, access.WorkloadIdentityInput) (access.AuthoringTokenSet, error)
	RevokeAccessToken(context.Context, string) error
}

// authoringOAuthScope binds a requested scope to the immutable project served
// by this instance. The project ID in an OAuth request is untrusted input: a
// syntactically valid foreign ID must never reach an issuance service, since
// those services persist the resulting scope.
func (m *Module) authoringOAuthScope(ctx context.Context, targetID, requestedProjectID string, capabilities []access.Capability) (access.AuthoringScope, error) {
	requested, err := graph.NewResourceID(requestedProjectID)
	if err != nil {
		return access.AuthoringScope{}, access.ErrAuthoringScopeDenied
	}
	resolveProject := m.authoringProjectID
	durableResolver := resolveProject != nil
	if resolveProject == nil {
		// SQLite/evaluation callers historically supplied only the active
		// serving resolver. Production composition injects the durable
		// authoring resolver above, so errors from CurrentProjectID are never
		// interpreted as a fresh target there.
		resolveProject = m.CurrentProjectID
	}
	if resolveProject == nil {
		return access.AuthoringScope{}, fmt.Errorf("resolve active authoring project: resolver is unavailable")
	}
	var boundProjectID graph.ResourceID
	boundProjectID, err = resolveProject(ctx)
	if err != nil {
		return access.AuthoringScope{}, fmt.Errorf("resolve active authoring project: %w", err)
	}
	if boundProjectID == "" && !durableResolver {
		return access.AuthoringScope{}, fmt.Errorf("resolve active authoring project: active project is unavailable")
	}
	if boundProjectID != "" {
		if err := boundProjectID.Validate(); err != nil {
			return access.AuthoringScope{}, fmt.Errorf("active authoring project is invalid: %w", err)
		}
		if requested != boundProjectID {
			return access.AuthoringScope{}, access.ErrAuthoringScopeDenied
		}
	}
	scope, err := access.NewAuthoringScope(targetID, requested, capabilities)
	if err != nil {
		return access.AuthoringScope{}, fmt.Errorf("%w: %v", access.ErrAuthoringScopeDenied, err)
	}
	return scope, nil
}

func writeAuthoringOAuthScopeError(w http.ResponseWriter, err error) {
	if errors.Is(err, access.ErrAuthoringScopeDenied) {
		writeAuthoringOAuthError(w, http.StatusBadRequest, "invalid_scope", "authoring scope was denied")
		return
	}
	writeAuthoringOAuthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "active authoring project is unavailable")
}

func (m *Module) authoringOAuthService(w http.ResponseWriter) (authoringOAuthAuthentication, bool) {
	if m == nil || m.handler.AuthoringAuth == nil {
		writeAuthoringOAuthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "authoring authentication is unavailable")
		return nil, false
	}
	return m.handler.AuthoringAuth, true
}

func authoringOAuthCapabilities(scope string) ([]access.Capability, error) {
	values := strings.Fields(scope)
	if len(values) == 0 {
		return nil, fmt.Errorf("at least one authoring capability is required")
	}
	capabilities := make([]access.Capability, 0, len(values))
	for _, value := range values {
		capability, err := access.ParseCapability(value)
		if err != nil {
			return nil, fmt.Errorf("unsupported authoring capability %q", value)
		}
		capabilities = append(capabilities, capability)
	}
	return capabilities, nil
}

func writeAuthoringOAuthServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, access.ErrDeviceAuthorizationPending):
		writeAuthoringOAuthError(w, http.StatusBadRequest, "authorization_pending", "device authorization is pending")
	case errors.Is(err, access.ErrDeviceAuthorizationSlowDown):
		writeAuthoringOAuthError(w, http.StatusBadRequest, "slow_down", "device authorization polling is too frequent")
	case errors.Is(err, access.ErrDeviceAuthorizationDenied):
		writeAuthoringOAuthError(w, http.StatusBadRequest, "access_denied", "device authorization was denied")
	case errors.Is(err, access.ErrDeviceAuthorizationExpired):
		writeAuthoringOAuthError(w, http.StatusBadRequest, "expired_token", "device authorization expired")
	case errors.Is(err, access.ErrAuthoringScopeDenied):
		writeAuthoringOAuthError(w, http.StatusBadRequest, "invalid_scope", "authoring scope was denied")
	case errors.Is(err, access.ErrInvalidAuthoringPrincipal):
		writeAuthoringOAuthError(w, http.StatusUnauthorized, "invalid_client", "service principal credentials are invalid")
	case errors.Is(err, access.ErrInvalidWorkloadLifetime):
		writeAuthoringOAuthError(w, http.StatusBadRequest, "invalid_request", "workload credential lifetime is invalid")
	case errors.Is(err, access.ErrInvalidAuthoringCredential):
		writeAuthoringOAuthError(w, http.StatusBadRequest, "invalid_grant", "device credential is invalid")
	case errors.Is(err, access.ErrAuthoringRefreshReplay):
		writeAuthoringOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh credential replay was detected")
	case errors.Is(err, access.ErrAuthoringCredentialExpired):
		writeAuthoringOAuthError(w, http.StatusBadRequest, "invalid_grant", "authoring credential expired")
	default:
		writeAuthoringOAuthError(w, http.StatusInternalServerError, "server_error", "authoring token exchange failed")
	}
}

func writeAuthoringOAuthError(w http.ResponseWriter, status int, code, description string) {
	setAuthoringOAuthNoStore(w)
	writeJSON(w, status, map[string]string{
		"error":             code,
		"error_description": description,
	})
}

func setAuthoringOAuthNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}
