package infisical

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
)

const (
	defaultMaxBundleSize = int64(64 << 10)
	defaultSnapshotTTL   = 5 * time.Minute
)

var errAccessTokenRejected = errors.New("Infisical access token rejected")

type AccessToken struct {
	value     string
	expiresAt time.Time
	refreshAt time.Time
}

func (AccessToken) String() string   { return "<infisical-access-token:redacted>" }
func (AccessToken) GoString() string { return "infisical.AccessToken{<redacted>}" }

type Authenticator interface {
	AccessToken(context.Context) (AccessToken, error)
}

type accessTokenInvalidator interface {
	InvalidateAccessToken(AccessToken)
}

type Config struct {
	BaseURL       string
	HTTPClient    *http.Client
	Authenticator Authenticator `json:"-" yaml:"-"`
	Now           func() time.Time
	SnapshotTTL   time.Duration
	MaxBundleSize int64
	AllowedScopes []AllowedScope
}

func (Config) String() string   { return "<infisical-resolver-config:redacted>" }
func (Config) GoString() string { return "infisical.Config{<redacted>}" }

type AllowedScope struct {
	ProjectID        string
	Environment      string
	SecretPathPrefix string
}

type Resolver struct {
	baseURL       *url.URL
	client        *http.Client
	authenticator Authenticator
	now           func() time.Time
	snapshotTTL   time.Duration
	maxBundleSize int64
	allowedScopes []AllowedScope
}

var _ connectionbinding.CredentialResolver = (*Resolver)(nil)
var _ connectionbinding.VersionedCredentialResolver = (*Resolver)(nil)

func NewResolver(config Config) (*Resolver, error) {
	baseURL, err := validateBaseURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	if config.HTTPClient == nil || config.Authenticator == nil || config.Now == nil || len(config.AllowedScopes) == 0 {
		return nil, fmt.Errorf("%w: Infisical HTTP client, authenticator, clock, and allowed scopes are required", connectionbinding.ErrInvalidBinding)
	}
	if config.SnapshotTTL == 0 {
		config.SnapshotTTL = defaultSnapshotTTL
	}
	if config.MaxBundleSize == 0 {
		config.MaxBundleSize = defaultMaxBundleSize
	}
	if config.SnapshotTTL <= 0 || config.MaxBundleSize <= 0 || config.MaxBundleSize > 1<<20 {
		return nil, fmt.Errorf("%w: Infisical snapshot limits are invalid", connectionbinding.ErrInvalidBinding)
	}
	allowedScopes := make([]AllowedScope, len(config.AllowedScopes))
	for index, scope := range config.AllowedScopes {
		scope.ProjectID = strings.TrimSpace(scope.ProjectID)
		scope.Environment = strings.TrimSpace(scope.Environment)
		scope.SecretPathPrefix = strings.TrimSpace(scope.SecretPathPrefix)
		if scope.ProjectID == "" || scope.Environment == "" || !strings.HasPrefix(scope.SecretPathPrefix, "/") {
			return nil, fmt.Errorf("%w: Infisical allowed scope is invalid", connectionbinding.ErrInvalidBinding)
		}
		scope.SecretPathPrefix = strings.TrimSuffix(scope.SecretPathPrefix, "/")
		if scope.SecretPathPrefix == "" {
			scope.SecretPathPrefix = "/"
		}
		allowedScopes[index] = scope
	}
	return &Resolver{
		baseURL: baseURL, client: hardenedClient(config.HTTPClient), authenticator: config.Authenticator,
		now: config.Now, snapshotTTL: config.SnapshotTTL, maxBundleSize: config.MaxBundleSize,
		allowedScopes: allowedScopes,
	}, nil
}

func (resolver *Resolver) Resolve(ctx context.Context, reference connectionbinding.CredentialReference) (connectionbinding.CredentialSnapshot, error) {
	return resolver.resolve(ctx, reference, "", 0)
}

func (resolver *Resolver) ResolveVersion(
	ctx context.Context,
	reference connectionbinding.CredentialReference,
	providerVersion string,
) (connectionbinding.CredentialSnapshot, error) {
	secretID, version, err := parseProviderVersion(providerVersion)
	if err != nil {
		return connectionbinding.CredentialSnapshot{}, err
	}
	return resolver.resolve(ctx, reference, secretID, version)
}

func (resolver *Resolver) resolve(
	ctx context.Context,
	reference connectionbinding.CredentialReference,
	expectedSecretID string,
	expectedVersion int64,
) (connectionbinding.CredentialSnapshot, error) {
	if resolver == nil {
		return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrProviderUnavailable
	}
	if !resolver.authorized(reference) {
		return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrCredentialDenied
	}
	for attempt := 0; attempt < 2; attempt++ {
		token, err := resolver.authenticator.AccessToken(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return connectionbinding.CredentialSnapshot{}, err
			}
			return connectionbinding.CredentialSnapshot{}, providerError(err)
		}
		now := resolver.now().UTC()
		if strings.TrimSpace(token.value) == "" || !token.expiresAt.After(now) {
			return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrProviderUnavailable
		}
		snapshot, err := resolver.resolveWithToken(
			ctx, reference, token, now, expectedSecretID, expectedVersion,
		)
		if !errors.Is(err, errAccessTokenRejected) {
			return snapshot, err
		}
		invalidator, ok := resolver.authenticator.(accessTokenInvalidator)
		if !ok || attempt == 1 {
			return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrCredentialDenied
		}
		invalidator.InvalidateAccessToken(token)
	}
	return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrCredentialDenied
}

func (resolver *Resolver) resolveWithToken(
	ctx context.Context,
	reference connectionbinding.CredentialReference,
	token AccessToken,
	now time.Time,
	expectedSecretID string,
	expectedVersion int64,
) (connectionbinding.CredentialSnapshot, error) {
	endpoint := *resolver.baseURL
	endpoint.Path = "/api/v4/secrets/" + reference.SecretKey
	endpoint.RawPath = "/api/v4/secrets/" + url.PathEscape(reference.SecretKey)
	query := endpoint.Query()
	query.Set("projectId", reference.ProjectID.String())
	query.Set("environment", reference.Environment)
	query.Set("secretPath", reference.SecretPath)
	query.Set("type", "shared")
	query.Set("expandSecretReferences", "true")
	if expectedVersion > 0 {
		query.Set("version", strconv.FormatInt(expectedVersion, 10))
	}
	endpoint.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrProviderUnavailable
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token.value)
	response, err := resolver.client.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return connectionbinding.CredentialSnapshot{}, err
		}
		return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrProviderUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return connectionbinding.CredentialSnapshot{}, errAccessTokenRejected
	}
	if response.StatusCode != http.StatusOK {
		return connectionbinding.CredentialSnapshot{}, statusError(response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, resolver.maxBundleSize+1))
	if err != nil || int64(len(body)) > resolver.maxBundleSize {
		return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrInvalidCredentialBundle
	}
	var envelope struct {
		Secret struct {
			ID      string `json:"id"`
			Value   string `json:"secretValue"`
			Version int64  `json:"version"`
		} `json:"secret"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil ||
		strings.TrimSpace(envelope.Secret.ID) == "" || envelope.Secret.Version <= 0 ||
		strings.TrimSpace(envelope.Secret.Value) == "" {
		return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrInvalidCredentialBundle
	}
	if expectedVersion > 0 && (envelope.Secret.ID != expectedSecretID || envelope.Secret.Version != expectedVersion) {
		return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrInvalidCredentialBundle
	}
	var bundle map[string]string
	if err := json.Unmarshal([]byte(envelope.Secret.Value), &bundle); err != nil {
		return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrInvalidCredentialBundle
	}
	version := envelope.Secret.ID + ":v" + strconv.FormatInt(envelope.Secret.Version, 10)
	snapshot, err := connectionbinding.NewCredentialSnapshot(bundle, version, now, now.Add(resolver.snapshotTTL))
	if err != nil {
		return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrInvalidCredentialBundle
	}
	return snapshot, nil
}

func parseProviderVersion(value string) (string, int64, error) {
	value = strings.TrimSpace(value)
	separator := strings.LastIndex(value, ":v")
	if separator <= 0 || separator+2 >= len(value) {
		return "", 0, connectionbinding.ErrInvalidCredentialBundle
	}
	secretID := value[:separator]
	version, err := strconv.ParseInt(value[separator+2:], 10, 64)
	if strings.TrimSpace(secretID) != secretID || version <= 0 || err != nil {
		return "", 0, connectionbinding.ErrInvalidCredentialBundle
	}
	return secretID, version, nil
}

func (resolver *Resolver) authorized(reference connectionbinding.CredentialReference) bool {
	for _, scope := range resolver.allowedScopes {
		if reference.ProjectID.String() != scope.ProjectID || reference.Environment != scope.Environment {
			continue
		}
		if scope.SecretPathPrefix == "/" || reference.SecretPath == scope.SecretPathPrefix ||
			strings.HasPrefix(reference.SecretPath, scope.SecretPathPrefix+"/") {
			return true
		}
	}
	return false
}

type UniversalAuthConfig struct {
	BaseURL      string
	ClientID     string
	ClientSecret string `json:"-" yaml:"-"`
	HTTPClient   *http.Client
	Now          func() time.Time
}

func (UniversalAuthConfig) String() string   { return "<infisical-universal-auth-config:redacted>" }
func (UniversalAuthConfig) GoString() string { return "infisical.UniversalAuthConfig{<redacted>}" }

type UniversalAuthenticator struct {
	baseURL      *url.URL
	clientID     string
	clientSecret string
	client       *http.Client
	now          func() time.Time

	mu    sync.Mutex
	token AccessToken
}

func NewUniversalAuthenticator(config UniversalAuthConfig) (*UniversalAuthenticator, error) {
	baseURL, err := validateBaseURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	config.ClientID = strings.TrimSpace(config.ClientID)
	if config.ClientID == "" || config.ClientSecret == "" || config.HTTPClient == nil || config.Now == nil {
		return nil, fmt.Errorf("%w: Universal Auth client identity, bootstrap secret, HTTP client, and clock are required", connectionbinding.ErrInvalidBinding)
	}
	return &UniversalAuthenticator{
		baseURL: baseURL, clientID: config.ClientID, clientSecret: config.ClientSecret,
		client: hardenedClient(config.HTTPClient), now: config.Now,
	}, nil
}

func (authenticator *UniversalAuthenticator) AccessToken(ctx context.Context) (AccessToken, error) {
	if authenticator == nil {
		return AccessToken{}, connectionbinding.ErrProviderUnavailable
	}
	authenticator.mu.Lock()
	defer authenticator.mu.Unlock()
	now := authenticator.now().UTC()
	if authenticator.token.value != "" && now.Before(authenticator.token.refreshAt) {
		return authenticator.token, nil
	}
	endpoint := *authenticator.baseURL
	endpoint.Path = "/api/v1/auth/universal-auth/login"
	form := url.Values{"clientId": {authenticator.clientID}, "clientSecret": {authenticator.clientSecret}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return AccessToken{}, connectionbinding.ErrProviderUnavailable
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := authenticator.client.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return AccessToken{}, err
		}
		return AccessToken{}, connectionbinding.ErrProviderUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return AccessToken{}, statusError(response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return AccessToken{}, connectionbinding.ErrProviderUnavailable
	}
	token, err := decodeAccessToken(body, now)
	if err != nil {
		return AccessToken{}, err
	}
	authenticator.token = token
	return authenticator.token, nil
}

func (authenticator *UniversalAuthenticator) InvalidateAccessToken(token AccessToken) {
	if authenticator == nil {
		return
	}
	authenticator.mu.Lock()
	defer authenticator.mu.Unlock()
	if authenticator.token.value == token.value {
		authenticator.token = AccessToken{}
	}
}

func decodeAccessToken(body []byte, now time.Time) (AccessToken, error) {
	var payload struct {
		AccessToken string `json:"accessToken"`
		ExpiresIn   int64  `json:"expiresIn"`
		TokenType   string `json:"tokenType"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || strings.TrimSpace(payload.AccessToken) == "" ||
		payload.ExpiresIn <= 0 || !strings.EqualFold(payload.TokenType, "Bearer") {
		return AccessToken{}, connectionbinding.ErrProviderUnavailable
	}
	ttl := time.Duration(payload.ExpiresIn) * time.Second
	leeway := 30 * time.Second
	if ttl <= leeway {
		leeway = ttl / 10
	}
	return AccessToken{
		value: payload.AccessToken, expiresAt: now.Add(ttl), refreshAt: now.Add(ttl - leeway),
	}, nil
}

func validateBaseURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" {
		return nil, fmt.Errorf("%w: Infisical base URL must be an HTTPS origin", connectionbinding.ErrInvalidBinding)
	}
	parsed.Path = ""
	return parsed, nil
}

func hardenedClient(client *http.Client) *http.Client {
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &copy
}

func statusError(status int) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return connectionbinding.ErrCredentialDenied
	case http.StatusNotFound:
		return connectionbinding.ErrCredentialNotFound
	case http.StatusTooManyRequests:
		return connectionbinding.ErrCredentialRateLimited
	default:
		return connectionbinding.ErrProviderUnavailable
	}
}

func providerError(err error) error {
	for _, candidate := range []error{
		connectionbinding.ErrCredentialDenied, connectionbinding.ErrCredentialNotFound,
		connectionbinding.ErrCredentialRateLimited, connectionbinding.ErrProviderUnavailable,
	} {
		if errors.Is(err, candidate) {
			return candidate
		}
	}
	return connectionbinding.ErrProviderUnavailable
}
