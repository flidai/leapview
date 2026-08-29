package mcpoauth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
)

const (
	ScopeMCPUse        = "mcp:use"
	ScopeOfflineAccess = "offline_access"
)

type Config struct {
	IssuerURL                string
	ResourceURL              string
	Secret                   []byte
	AccessTokenTTL           time.Duration
	RefreshTokenTTL          time.Duration
	AuthorizationCodeTTL     time.Duration
	ClientMetadataHTTPClient *http.Client
}

type Service struct {
	config         Config
	provider       fosite.OAuth2Provider
	store          StoreBackend
	postgresBacked bool
	repo           access.Repository
	metadataClient *http.Client
}

type ResourceServer interface {
	Authenticate(context.Context, string) (Credential, error)
	ProtectedResourceMetadata(http.ResponseWriter, *http.Request)
	Challenge(http.ResponseWriter)
}

type Consent struct {
	ClientID   string
	ClientName string
	Scopes     []string
	Resource   string
}

type Credential struct {
	Principal access.Principal
	Resource  string
	Scopes    []string
}

func (c Credential) HasScope(scope string) bool { return slices.Contains(c.Scopes, scope) }

type RegistrationRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

type RegistrationResponse struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// StoreBackend is the durable MCP OAuth state contract. Implementations may
// use PostgreSQL, while the legacy Store uses SQLite only through New.
type StoreBackend interface {
	fosite.Storage
	SetClientResolver(func(context.Context, string) (StoredClient, error))
	ClientName(context.Context, string) (string, error)
	CreateClient(context.Context, StoredClient) error
	EnsureServiceClient(context.Context, string, string, string) error
	SetSessionRetention(time.Duration)
}

func New(db *sql.DB, repo access.Repository, config Config) (*Service, error) {
	if db == nil {
		return nil, fmt.Errorf("MCP OAuth SQLite storage is required")
	}
	return newWithStore(NewStore(db), repo, config, false)
}

// NewPostgres constructs MCP OAuth over the access control PostgreSQL
// connection. The caller is responsible for applying the access baseline
// before invoking this constructor.
func NewPostgres(db accesspostgres.DBTX, repo access.Repository, config Config) (*Service, error) {
	store, err := NewPostgresStore(db)
	if err != nil {
		return nil, err
	}
	return newWithStore(store, repo, config, true)
}

// NewWithStore constructs MCP OAuth over an explicitly supplied durable store.
// It is intentionally non-PostgreSQL-marked; production should use
// NewPostgres, which owns the marker after constructing PostgresStore.
func NewWithStore(store StoreBackend, repo access.Repository, config Config) (*Service, error) {
	return newWithStore(store, repo, config, false)
}

// newWithStore is constructor-owned so an arbitrary StoreBackend cannot claim
// PostgreSQL backing through a user-defined marker method. Only NewPostgres
// sets postgresBacked=true after constructing the concrete PG adapter.
func newWithStore(store StoreBackend, repo access.Repository, config Config, postgresBacked bool) (*Service, error) {
	if store == nil || repo == nil {
		return nil, fmt.Errorf("MCP OAuth requires storage and access repository")
	}
	config.IssuerURL = strings.TrimSuffix(strings.TrimSpace(config.IssuerURL), "/")
	config.ResourceURL = strings.TrimSuffix(strings.TrimSpace(config.ResourceURL), "/")
	if err := validateCanonicalOrigin(config.IssuerURL, true); err != nil {
		return nil, fmt.Errorf("invalid MCP OAuth issuer: %w", err)
	}
	if err := validateCanonicalURL(config.ResourceURL, true); err != nil {
		return nil, fmt.Errorf("invalid MCP OAuth resource: %w", err)
	}
	if len(config.Secret) < 32 {
		return nil, fmt.Errorf("MCP OAuth signing secret must contain at least 32 bytes")
	}
	if config.AccessTokenTTL <= 0 {
		config.AccessTokenTTL = 15 * time.Minute
	}
	if config.RefreshTokenTTL <= 0 {
		config.RefreshTokenTTL = 30 * 24 * time.Hour
	}
	if config.AuthorizationCodeTTL <= 0 {
		config.AuthorizationCodeTTL = 5 * time.Minute
	}
	store.SetSessionRetention(max(config.AccessTokenTTL, config.RefreshTokenTTL, config.AuthorizationCodeTTL) + 24*time.Hour)
	metadataClient := config.ClientMetadataHTTPClient
	if metadataClient == nil {
		metadataClient = secureClientMetadataHTTPClient()
	}
	fositeConfig := &fosite.Config{
		GlobalSecret:                   config.Secret,
		AccessTokenLifespan:            config.AccessTokenTTL,
		RefreshTokenLifespan:           config.RefreshTokenTTL,
		AuthorizeCodeLifespan:          config.AuthorizationCodeTTL,
		ScopeStrategy:                  fosite.ExactScopeStrategy,
		AudienceMatchingStrategy:       fosite.ExactAudienceMatchingStrategy,
		EnforcePKCE:                    true,
		EnablePKCEPlainChallengeMethod: false,
		RefreshTokenScopes:             []string{ScopeOfflineAccess},
		TokenURL:                       config.IssuerURL + "/oauth/token",
		SendDebugMessagesToClients:     false,
	}
	strategy := compose.NewOAuth2HMACStrategy(fositeConfig)
	provider := compose.Compose(
		fositeConfig,
		store,
		strategy,
		compose.OAuth2AuthorizeExplicitFactory,
		compose.OAuth2ClientCredentialsGrantFactory,
		compose.OAuth2RefreshTokenGrantFactory,
		compose.OAuth2TokenIntrospectionFactory,
		compose.OAuth2TokenRevocationFactory,
		compose.OAuth2PKCEFactory,
	)
	service := &Service{config: config, provider: provider, store: store, postgresBacked: postgresBacked, repo: repo, metadataClient: metadataClient}
	store.SetClientResolver(service.resolveClientMetadata)
	return service, nil
}

// IsPostgresBacked reports whether the service's state store is a PostgreSQL
// implementation. A nil or legacy SQLite service returns false.
func (s *Service) IsPostgresBacked() bool { return s != nil && s.postgresBacked }

func (s *Service) Consent(r *http.Request) (Consent, error) {
	request, err := s.normalizeResourceRequest(r, true)
	if err != nil {
		return Consent{}, err
	}
	authorizeRequest, err := s.provider.NewAuthorizeRequest(request.Context(), request)
	if err != nil {
		return Consent{}, err
	}
	if !authorizeRequest.GetRequestedScopes().Has(ScopeMCPUse) {
		return Consent{}, fosite.ErrInvalidScope.WithHint("The mcp:use scope is required.")
	}
	name, err := s.store.ClientName(request.Context(), authorizeRequest.GetClient().GetID())
	if err != nil {
		return Consent{}, err
	}
	return Consent{
		ClientID: authorizeRequest.GetClient().GetID(), ClientName: name,
		Scopes:   append([]string(nil), authorizeRequest.GetRequestedScopes()...),
		Resource: s.config.ResourceURL,
	}, nil
}

func (s *Service) Authorize(w http.ResponseWriter, r *http.Request, principalID string, approved bool) {
	request, err := s.normalizeResourceRequest(r, true)
	if err != nil {
		writeOAuthJSONError(w, http.StatusBadRequest, "invalid_target", err.Error())
		return
	}
	authorizeRequest, err := s.provider.NewAuthorizeRequest(request.Context(), request)
	if err != nil {
		s.provider.WriteAuthorizeError(request.Context(), w, authorizeRequest, err)
		return
	}
	if !approved {
		s.provider.WriteAuthorizeError(request.Context(), w, authorizeRequest, fosite.ErrAccessDenied)
		return
	}
	principal, err := s.repo.PrincipalByID(request.Context(), principalID)
	if err != nil || principal.AccessDisabled() {
		s.provider.WriteAuthorizeError(request.Context(), w, authorizeRequest, fosite.ErrAccessDenied)
		return
	}
	for _, scope := range authorizeRequest.GetRequestedScopes() {
		if scope == ScopeMCPUse || scope == ScopeOfflineAccess {
			authorizeRequest.GrantScope(scope)
		}
	}
	for _, audience := range authorizeRequest.GetRequestedAudience() {
		if audience == s.config.ResourceURL {
			authorizeRequest.GrantAudience(audience)
		}
	}
	session := &fosite.DefaultSession{Subject: principal.ID, Username: principal.Email}
	response, err := s.provider.NewAuthorizeResponse(request.Context(), authorizeRequest, session)
	if err != nil {
		s.provider.WriteAuthorizeError(request.Context(), w, authorizeRequest, err)
		return
	}
	s.provider.WriteAuthorizeResponse(request.Context(), w, authorizeRequest, response)
}

func (s *Service) Token(w http.ResponseWriter, r *http.Request) {
	request, err := s.normalizeResourceRequest(r, true)
	if err != nil {
		writeOAuthJSONError(w, http.StatusBadRequest, "invalid_target", err.Error())
		return
	}
	if request.Form.Get("grant_type") == "client_credentials" {
		s.clientCredentialsToken(w, request)
		return
	}
	accessRequest, err := s.provider.NewAccessRequest(request.Context(), request, &fosite.DefaultSession{})
	if err != nil {
		s.provider.WriteAccessError(request.Context(), w, accessRequest, err)
		return
	}
	for _, scope := range accessRequest.GetRequestedScopes() {
		if scope == ScopeMCPUse || scope == ScopeOfflineAccess {
			accessRequest.GrantScope(scope)
		}
	}
	for _, audience := range accessRequest.GetRequestedAudience() {
		if audience == s.config.ResourceURL {
			accessRequest.GrantAudience(audience)
		}
	}
	response, err := s.provider.NewAccessResponse(request.Context(), accessRequest)
	if err != nil {
		s.provider.WriteAccessError(request.Context(), w, accessRequest, err)
		return
	}
	s.provider.WriteAccessResponse(request.Context(), w, accessRequest, response)
}

func (s *Service) clientCredentialsToken(w http.ResponseWriter, r *http.Request) {
	clientID, clientSecret := r.Form.Get("client_id"), r.Form.Get("client_secret")
	if basicID, basicSecret, ok := r.BasicAuth(); ok {
		clientID, clientSecret = basicID, basicSecret
	}
	principal, err := s.repo.PrincipalForServicePrincipalSecret(r.Context(), clientID, clientSecret)
	if err != nil || principal.AccessDisabled() {
		w.Header().Set("WWW-Authenticate", `Basic realm="leapview-oauth"`)
		writeOAuthJSONError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return
	}
	requestedScopes := strings.Fields(r.Form.Get("scope"))
	if len(requestedScopes) == 0 {
		requestedScopes = []string{ScopeMCPUse}
	}
	if !allowedValues(requestedScopes, ScopeMCPUse) || !slices.Contains(requestedScopes, ScopeMCPUse) {
		writeOAuthJSONError(w, http.StatusBadRequest, "invalid_scope", "only mcp:use is supported")
		return
	}
	if err := s.store.EnsureServiceClient(r.Context(), principal.ID, principal.DisplayName, s.config.ResourceURL); err != nil {
		writeOAuthJSONError(w, http.StatusInternalServerError, "server_error", "could not initialize service client")
		return
	}
	client, err := s.store.GetClient(r.Context(), principal.ID)
	if err != nil {
		writeOAuthJSONError(w, http.StatusInternalServerError, "server_error", "could not initialize service client")
		return
	}
	session := &fosite.DefaultSession{Subject: principal.ID, Username: principal.Email}
	accessRequest := fosite.NewAccessRequest(session)
	accessRequest.Client = client
	accessRequest.GrantTypes = fosite.Arguments{"client_credentials"}
	accessRequest.RequestedScope = fosite.Arguments(requestedScopes)
	accessRequest.GrantedScope = fosite.Arguments(requestedScopes)
	accessRequest.RequestedAudience = fosite.Arguments{s.config.ResourceURL}
	accessRequest.GrantedAudience = fosite.Arguments{s.config.ResourceURL}
	accessRequest.Form = r.Form
	response, err := s.provider.NewAccessResponse(r.Context(), accessRequest)
	if err != nil {
		s.provider.WriteAccessError(r.Context(), w, accessRequest, err)
		return
	}
	s.provider.WriteAccessResponse(r.Context(), w, accessRequest, response)
}

func (s *Service) Revoke(w http.ResponseWriter, r *http.Request) {
	err := s.provider.NewRevocationRequest(r.Context(), r)
	s.provider.WriteRevocationResponse(r.Context(), w, err)
}

func (s *Service) Authenticate(ctx context.Context, token string) (Credential, error) {
	if strings.TrimSpace(token) == "" {
		return Credential{}, fosite.ErrRequestUnauthorized
	}
	_, request, err := s.provider.IntrospectToken(ctx, token, fosite.AccessToken, &fosite.DefaultSession{}, ScopeMCPUse)
	if err != nil {
		return Credential{}, err
	}
	if !request.GetGrantedScopes().Has(ScopeMCPUse) || !request.GetGrantedAudience().Has(s.config.ResourceURL) {
		return Credential{}, fosite.ErrRequestUnauthorized
	}
	principal, err := s.repo.PrincipalByID(ctx, request.GetSession().GetSubject())
	if err != nil || principal.AccessDisabled() {
		return Credential{}, fosite.ErrRequestUnauthorized
	}
	return Credential{
		Principal: principal, Resource: s.config.ResourceURL,
		Scopes: append([]string(nil), request.GetGrantedScopes()...),
	}, nil
}

func (s *Service) Register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeOAuthJSONError(w, http.StatusMethodNotAllowed, "invalid_request", "POST is required")
		return
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	var input RegistrationRequest
	if err := decoder.Decode(&input); err != nil {
		writeOAuthJSONError(w, http.StatusBadRequest, "invalid_client_metadata", "invalid registration document")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeOAuthJSONError(w, http.StatusBadRequest, "invalid_client_metadata", "invalid registration document")
		return
	}
	input.ClientName = strings.TrimSpace(input.ClientName)
	if input.ClientName == "" || len(input.ClientName) > 200 || len(input.RedirectURIs) == 0 || len(input.RedirectURIs) > 10 {
		writeOAuthJSONError(w, http.StatusBadRequest, "invalid_client_metadata", "client_name and redirect_uris are required")
		return
	}
	for _, redirect := range input.RedirectURIs {
		if err := validateCanonicalURL(redirect, true); err != nil {
			writeOAuthJSONError(w, http.StatusBadRequest, "invalid_redirect_uri", err.Error())
			return
		}
	}
	if len(input.GrantTypes) == 0 {
		input.GrantTypes = []string{"authorization_code"}
	}
	if len(input.ResponseTypes) == 0 {
		input.ResponseTypes = []string{"code"}
	}
	if input.TokenEndpointAuthMethod == "" {
		input.TokenEndpointAuthMethod = "none"
	}
	if !allowedValues(input.GrantTypes, "authorization_code", "refresh_token") ||
		!slices.Contains(input.GrantTypes, "authorization_code") ||
		!allowedValues(input.ResponseTypes, "code") || !slices.Contains(input.ResponseTypes, "code") ||
		input.TokenEndpointAuthMethod != "none" {
		writeOAuthJSONError(w, http.StatusBadRequest, "invalid_client_metadata", "only public authorization-code clients with PKCE are supported")
		return
	}
	clientID, err := randomValue("mcp_client_", 24)
	if err != nil {
		writeOAuthJSONError(w, http.StatusInternalServerError, "server_error", "could not register client")
		return
	}
	client := storedClient{
		ID: clientID, Name: input.ClientName, RedirectURIs: append([]string(nil), input.RedirectURIs...),
		GrantTypes: append([]string(nil), input.GrantTypes...), ResponseTypes: append([]string(nil), input.ResponseTypes...),
		Scopes: []string{ScopeMCPUse, ScopeOfflineAccess}, Audience: []string{s.config.ResourceURL},
		Public: true, TokenEndpointAuthMethod: "none",
	}
	if err := s.store.CreateClient(r.Context(), client); err != nil {
		writeOAuthJSONError(w, http.StatusInternalServerError, "server_error", "could not register client")
		return
	}
	writeJSON(w, http.StatusCreated, RegistrationResponse{
		ClientID: clientID, ClientName: client.Name, RedirectURIs: client.RedirectURIs,
		GrantTypes: client.GrantTypes, ResponseTypes: client.ResponseTypes,
		TokenEndpointAuthMethod: client.TokenEndpointAuthMethod,
	})
}

func (s *Service) ProtectedResourceMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 s.config.ResourceURL,
		"authorization_servers":    []string{s.config.IssuerURL},
		"scopes_supported":         []string{ScopeMCPUse},
		"bearer_methods_supported": []string{"header"},
	})
}

func (s *Service) AuthorizationServerMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                s.config.IssuerURL,
		"authorization_endpoint":                s.config.IssuerURL + "/oauth/authorize",
		"token_endpoint":                        s.config.IssuerURL + "/oauth/token",
		"registration_endpoint":                 s.config.IssuerURL + "/oauth/register",
		"revocation_endpoint":                   s.config.IssuerURL + "/oauth/revoke",
		"scopes_supported":                      []string{ScopeMCPUse, ScopeOfflineAccess},
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token", "client_credentials"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"},
		"code_challenge_methods_supported":      []string{"S256"},
		"client_id_metadata_document_supported": true,
	})
}

func (s *Service) Challenge(w http.ResponseWriter) {
	metadata := s.config.IssuerURL + "/.well-known/oauth-protected-resource/mcp"
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata=%q, scope=%q`, metadata, ScopeMCPUse))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"invalid_token","error_description":"A valid MCP OAuth access token is required"}`))
}

func (s *Service) normalizeResourceRequest(r *http.Request, required bool) (*http.Request, error) {
	if err := r.ParseForm(); err != nil {
		return r, fmt.Errorf("invalid OAuth form: %w", err)
	}
	resource := strings.TrimSuffix(strings.TrimSpace(r.Form.Get("resource")), "/")
	if resource == "" && required {
		return r, fmt.Errorf("resource is required")
	}
	if resource != "" && resource != s.config.ResourceURL {
		return r, fmt.Errorf("resource must be %s", s.config.ResourceURL)
	}
	if resource != "" {
		r.Form.Set("audience", resource)
		if r.URL != nil {
			query := r.URL.Query()
			query.Set("audience", resource)
			r.URL.RawQuery = query.Encode()
		}
	}
	return r, nil
}

func validateCanonicalURL(raw string, allowLoopbackHTTP bool) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("URL must be an absolute URL without credentials or fragment")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	host := parsed.Hostname()
	if allowLoopbackHTTP && parsed.Scheme == "http" && (host == "localhost" || net.ParseIP(host).IsLoopback()) {
		return nil
	}
	return fmt.Errorf("URL must use HTTPS%s", map[bool]string{true: " or loopback HTTP", false: ""}[allowLoopbackHTTP])
}

func validateCanonicalOrigin(raw string, allowLoopbackHTTP bool) error {
	if err := validateCanonicalURL(raw, allowLoopbackHTTP); err != nil {
		return err
	}
	parsed, _ := url.Parse(strings.TrimSpace(raw))
	if parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("URL must be an origin without credentials, path, query, or fragment")
	}
	return nil
}

func allowedValues(values []string, allowed ...string) bool {
	for _, value := range values {
		if !slices.Contains(allowed, value) {
			return false
		}
	}
	return true
}

func randomValue(prefix string, bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func writeOAuthJSONError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
