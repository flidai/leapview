package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	accesscli "github.com/flidai/leapview/internal/access/cli"
)

type originCredentialResolver interface {
	ResolveOrigin(context.Context, string, string) (accesscli.ResolvedCredential, error)
}

type authoringRetryTransport struct {
	base        http.RoundTripper
	credentials originCredentialResolver
	mu          sync.Mutex
	rotated     map[string]string
}

func (transport *authoringRetryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	base := transport.base
	if base == nil {
		base = http.DefaultTransport
	}
	currentToken := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	origin := (&url.URL{Scheme: request.URL.Scheme, Host: request.URL.Host}).String()
	sent := request
	sentToken := currentToken
	if strings.HasPrefix(currentToken, "lv_cli_access_") {
		sentToken = transport.currentToken(origin, currentToken)
		if sentToken != currentToken {
			sent = request.Clone(request.Context())
			sent.Header = request.Header.Clone()
			sent.Header.Set("Authorization", "Bearer "+sentToken)
		}
	}
	response, err := base.RoundTrip(sent)
	if err != nil || response.StatusCode != http.StatusUnauthorized || transport.credentials == nil {
		return response, err
	}
	if !strings.HasPrefix(sentToken, "lv_cli_access_") {
		return response, nil
	}
	refreshedToken, resolveErr := transport.refreshToken(request.Context(), origin, sentToken)
	if resolveErr != nil {
		response.Body.Close()
		return nil, fmt.Errorf("refresh authoring credential after HTTP 401: %w", resolveErr)
	}
	if refreshedToken == "" || refreshedToken == sentToken {
		return response, nil
	}
	retry := request.Clone(request.Context())
	retry.Header = request.Header.Clone()
	retry.Header.Set("Authorization", "Bearer "+refreshedToken)
	if request.Body != nil {
		if request.GetBody == nil {
			return response, nil
		}
		body, bodyErr := request.GetBody()
		if bodyErr != nil {
			response.Body.Close()
			return nil, bodyErr
		}
		retry.Body = body
	}
	response.Body.Close()
	return base.RoundTrip(retry)
}

func (transport *authoringRetryTransport) currentToken(origin, token string) string {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if refreshed := transport.rotated[origin+"\x00"+token]; refreshed != "" {
		return refreshed
	}
	return token
}

func (transport *authoringRetryTransport) refreshToken(ctx context.Context, origin, token string) (string, error) {
	// Serialize refresh with other 401 responses: only the first request may
	// rotate a stored native credential. The rest replay its replacement.
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if refreshed := transport.rotated[origin+"\x00"+token]; refreshed != "" {
		return refreshed, nil
	}
	resolved, err := transport.credentials.ResolveOrigin(ctx, origin, token)
	if err != nil {
		return "", err
	}
	if resolved.AccessToken == "" || resolved.AccessToken == token {
		return resolved.AccessToken, nil
	}
	if transport.rotated == nil {
		transport.rotated = make(map[string]string)
	}
	prefix := origin + "\x00"
	for key, current := range transport.rotated {
		if strings.HasPrefix(key, prefix) && current == token {
			transport.rotated[key] = resolved.AccessToken
		}
	}
	transport.rotated[prefix+token] = resolved.AccessToken
	return resolved.AccessToken, nil
}

type applicationOriginCredentials struct {
	client *http.Client
}

func (resolver applicationOriginCredentials) ResolveOrigin(ctx context.Context, origin, accessToken string) (accesscli.ResolvedCredential, error) {
	authentication, err := defaultAuthoringAuthenticator(resolver.client)
	if err != nil {
		return accesscli.ResolvedCredential{}, err
	}
	return authentication.ResolveOrigin(ctx, origin, accessToken)
}

func authoringRefreshingHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	clone := *client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	plain := *client
	plain.Transport = base
	clone.Transport = &authoringRetryTransport{
		base: base, credentials: applicationOriginCredentials{client: &plain},
	}
	return &clone
}
