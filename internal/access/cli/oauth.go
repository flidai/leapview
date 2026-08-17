package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const authoringDeviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

type DeviceAuthorizationRequest struct {
	Origin       string
	ProjectID    string
	Capabilities []string
}

type OAuthRefreshRequest struct {
	Origin       string
	RefreshToken string
}

type OAuthRevokeRequest struct {
	Origin      string
	AccessToken string
}

type DeviceAuthorization interface {
	Challenge() DeviceChallenge
	Token(context.Context) (*oauth2.Token, error)
}

type AuthoringOAuthClient interface {
	Begin(context.Context, DeviceAuthorizationRequest) (DeviceAuthorization, error)
	Refresh(context.Context, OAuthRefreshRequest) (*oauth2.Token, error)
	Workload(context.Context, WorkloadIdentityRequest) (*oauth2.Token, error)
	Revoke(context.Context, OAuthRevokeRequest) error
}

type StandardOAuthClient struct {
	HTTPClient *http.Client
}

func (client StandardOAuthClient) Begin(ctx context.Context, request DeviceAuthorizationRequest) (DeviceAuthorization, error) {
	origin := strings.TrimRight(strings.TrimSpace(request.Origin), "/")
	if origin == "" || strings.TrimSpace(request.ProjectID) == "" || len(request.Capabilities) == 0 {
		return nil, fmt.Errorf("device authorization origin, project, and capabilities are required")
	}
	config := oauth2.Config{
		ClientID: access.AuthoringCLIClientID,
		Scopes:   append([]string(nil), request.Capabilities...),
		Endpoint: oauth2.Endpoint{
			DeviceAuthURL: origin + "/oauth/device/code",
			TokenURL:      origin + "/oauth/token",
			AuthStyle:     oauth2.AuthStyleInParams,
		},
	}
	response, err := config.DeviceAuth(
		client.context(ctx),
		oauth2.SetAuthURLParam("project_id", request.ProjectID),
	)
	if err != nil {
		return nil, fmt.Errorf("begin OAuth device authorization: %w", err)
	}
	expiresIn := time.Until(response.Expiry)
	if expiresIn < 0 {
		expiresIn = 0
	}
	return &oauthDeviceAuthorization{
		client:   client,
		config:   config,
		response: response,
		challenge: DeviceChallenge{
			UserCode:                response.UserCode,
			VerificationURI:         response.VerificationURI,
			VerificationURIComplete: response.VerificationURIComplete,
			ExpiresIn:               expiresIn,
		},
	}, nil
}

func (client StandardOAuthClient) Refresh(ctx context.Context, request OAuthRefreshRequest) (*oauth2.Token, error) {
	origin := strings.TrimRight(strings.TrimSpace(request.Origin), "/")
	refreshToken := strings.TrimSpace(request.RefreshToken)
	if origin == "" || refreshToken == "" {
		return nil, fmt.Errorf("OAuth refresh origin and credential are required")
	}
	config := oauth2.Config{
		ClientID: access.AuthoringCLIClientID,
		Endpoint: oauth2.Endpoint{
			TokenURL:  origin + "/oauth/token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
	token, err := config.TokenSource(client.context(ctx), &oauth2.Token{
		RefreshToken: refreshToken,
	}).Token()
	if err != nil {
		return nil, fmt.Errorf("refresh OAuth credential: %w", err)
	}
	return token, nil
}

func (client StandardOAuthClient) Workload(ctx context.Context, request WorkloadIdentityRequest) (*oauth2.Token, error) {
	origin := strings.TrimRight(strings.TrimSpace(request.Origin), "/")
	clientID := strings.TrimSpace(request.ClientID)
	if origin == "" || strings.TrimSpace(request.ProjectID) == "" ||
		clientID == "" || strings.TrimSpace(request.ClientSecret) == "" ||
		len(request.Capabilities) == 0 || request.Lifetime < time.Second {
		return nil, fmt.Errorf("OAuth workload origin, project, client credentials, capabilities, and lifetime are required")
	}
	config := clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: request.ClientSecret,
		TokenURL:     origin + "/oauth/token",
		Scopes:       append([]string(nil), request.Capabilities...),
		AuthStyle:    oauth2.AuthStyleInParams,
		EndpointParams: url.Values{
			"project_id":       {request.ProjectID},
			"lifetime_seconds": {strconv.FormatInt(int64(request.Lifetime/time.Second), 10)},
		},
	}
	token, err := config.Token(client.context(ctx))
	if err != nil {
		return nil, fmt.Errorf("exchange OAuth client credentials: %w", err)
	}
	return token, nil
}

func (client StandardOAuthClient) Revoke(ctx context.Context, request OAuthRevokeRequest) error {
	origin := strings.TrimRight(strings.TrimSpace(request.Origin), "/")
	accessToken := strings.TrimSpace(request.AccessToken)
	if origin == "" || accessToken == "" {
		return fmt.Errorf("OAuth revocation origin and credential are required")
	}
	form := url.Values{
		"client_id":       {access.AuthoringCLIClientID},
		"token":           {accessToken},
		"token_type_hint": {"access_token"},
	}
	httpRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		origin+"/oauth/revoke",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return fmt.Errorf("create OAuth revocation request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.httpClient().Do(httpRequest)
	if err != nil {
		return fmt.Errorf("revoke OAuth credential: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("revoke OAuth credential: token endpoint returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (client StandardOAuthClient) context(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, client.httpClient())
}

func (client StandardOAuthClient) httpClient() *http.Client {
	if client.HTTPClient != nil {
		return client.HTTPClient
	}
	return http.DefaultClient
}

type oauthDeviceAuthorization struct {
	client    StandardOAuthClient
	config    oauth2.Config
	response  *oauth2.DeviceAuthResponse
	challenge DeviceChallenge
}

func (authorization *oauthDeviceAuthorization) Challenge() DeviceChallenge {
	return authorization.challenge
}

func (authorization *oauthDeviceAuthorization) Token(ctx context.Context) (*oauth2.Token, error) {
	token, err := authorization.config.DeviceAccessToken(
		authorization.client.context(ctx),
		authorization.response,
	)
	if err != nil {
		return nil, fmt.Errorf("exchange OAuth device authorization: %w", err)
	}
	return token, nil
}

type authoringOAuthTokenDetails struct {
	SessionID string
	Kind      access.AuthoringSessionKind
	TargetID  string
	ProjectID string
}

func oauthAuthoringTokenDetails(token *oauth2.Token) (authoringOAuthTokenDetails, error) {
	details, err := oauthAuthoringTokenSessionDetails(token)
	if err != nil || strings.TrimSpace(token.RefreshToken) == "" {
		return authoringOAuthTokenDetails{}, fmt.Errorf("OAuth authorization returned incomplete credentials")
	}
	if details.Kind != access.AuthoringSessionHumanCLI {
		return authoringOAuthTokenDetails{}, fmt.Errorf("OAuth authorization returned unexpected session kind")
	}
	return details, nil
}

func oauthWorkloadTokenDetails(token *oauth2.Token) (authoringOAuthTokenDetails, error) {
	details, err := oauthAuthoringTokenSessionDetails(token)
	if err != nil || strings.TrimSpace(token.RefreshToken) != "" {
		return authoringOAuthTokenDetails{}, fmt.Errorf("OAuth workload exchange returned incomplete credentials")
	}
	if details.Kind != access.AuthoringSessionWorkload {
		return authoringOAuthTokenDetails{}, fmt.Errorf("OAuth workload exchange returned unexpected session kind")
	}
	return details, nil
}

func oauthAuthoringTokenSessionDetails(token *oauth2.Token) (authoringOAuthTokenDetails, error) {
	if token == nil || strings.TrimSpace(token.AccessToken) == "" || token.Expiry.IsZero() {
		return authoringOAuthTokenDetails{}, fmt.Errorf("OAuth authorization returned incomplete credentials")
	}
	details := authoringOAuthTokenDetails{
		SessionID: oauthTokenString(token, "session_id"),
		Kind:      access.AuthoringSessionKind(oauthTokenString(token, "session_kind")),
		TargetID:  oauthTokenString(token, "target_id"),
		ProjectID: oauthTokenString(token, "project_id"),
	}
	if details.SessionID == "" || details.Kind == "" || details.TargetID == "" || details.ProjectID == "" {
		return authoringOAuthTokenDetails{}, fmt.Errorf("OAuth authorization returned incomplete session metadata")
	}
	return details, nil
}

func oauthTokenString(token *oauth2.Token, name string) string {
	value, _ := token.Extra(name).(string)
	return strings.TrimSpace(value)
}
