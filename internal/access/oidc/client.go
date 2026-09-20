package oidc

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	coreosoidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type Config struct {
	ID           string
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
}

type Claims struct {
	Issuer            string
	Subject           string
	Email             string
	Name              string
	PreferredUsername string
	// AuthTime is the authentication event time asserted by the signed ID
	// token. It is required whenever this client asks the provider for
	// max_age and is also used to bound browser-session recent-auth checks.
	AuthTime time.Time
}

type Client struct {
	oauth    *oauth2.Config
	verify   func(context.Context, string) (Claims, string, error)
	exchange func(context.Context, string) (*oauth2.Token, error)
	now      func() time.Time
}

// interactiveMaxAge is deliberately aligned with the platform's recent
// authentication window. Sending max_age makes the identity provider perform
// an interactive reauthentication when its SSO authentication is older than
// this bound; the browser session is then created only after that fresh code
// exchange, so freshness does not depend on a client-supplied timestamp.
const interactiveMaxAge = 15 * time.Minute

func New(ctx context.Context, cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.IssuerURL) == "" {
		return nil, errors.New("oidc issuer URL is required")
	}
	if strings.TrimSpace(cfg.ClientID) == "" {
		return nil, errors.New("oidc client ID is required")
	}
	if strings.TrimSpace(cfg.ClientSecret) == "" {
		return nil, errors.New("oidc client secret is required")
	}
	if strings.TrimSpace(cfg.RedirectURL) == "" {
		return nil, errors.New("oidc redirect URL is required")
	}
	provider, err := coreosoidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, err
	}
	scopes := append([]string{coreosoidc.ScopeOpenID, "profile", "email"}, cfg.Scopes...)
	oauthConfig := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       uniqueScopes(scopes),
	}
	verifier := provider.Verifier(&coreosoidc.Config{ClientID: cfg.ClientID})
	return &Client{
		oauth: oauthConfig,
		verify: func(ctx context.Context, rawIDToken string) (Claims, string, error) {
			idToken, err := verifier.Verify(ctx, rawIDToken)
			if err != nil {
				return Claims{}, "", err
			}
			var raw struct {
				Email             string `json:"email"`
				Name              string `json:"name"`
				PreferredUsername string `json:"preferred_username"`
				AuthTime          *int64 `json:"auth_time"`
			}
			if err := idToken.Claims(&raw); err != nil {
				return Claims{}, "", err
			}
			if raw.AuthTime == nil || *raw.AuthTime <= 0 {
				return Claims{}, "", errors.New("oidc ID token is missing auth_time")
			}
			return Claims{
				Issuer:            idToken.Issuer,
				Subject:           idToken.Subject,
				Email:             raw.Email,
				Name:              raw.Name,
				PreferredUsername: raw.PreferredUsername,
				AuthTime:          time.Unix(*raw.AuthTime, 0).UTC(),
			}, idToken.Nonce, nil
		},
		exchange: func(ctx context.Context, code string) (*oauth2.Token, error) {
			return oauthConfig.Exchange(ctx, code)
		},
		now: time.Now,
	}, nil
}

func AzureIssuerURL(tenant string) string {
	tenant = strings.TrimSpace(tenant)
	if tenant == "" {
		tenant = "common"
	}
	return "https://login.microsoftonline.com/" + tenant + "/v2.0"
}

func AzureProviderConfig(clientID, clientSecret, redirectURL, tenant string) Config {
	return Config{
		ID:           "azureadv2",
		IssuerURL:    AzureIssuerURL(tenant),
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
	}
}

func (c *Client) AuthCodeURL(state, nonce string) string {
	return c.oauth.AuthCodeURL(state, coreosoidc.Nonce(nonce), oauth2.SetAuthURLParam("max_age", strconv.FormatInt(int64(interactiveMaxAge/time.Second), 10)))
}

func (c *Client) Authenticate(ctx context.Context, code, expectedNonce string) (Claims, error) {
	if c == nil {
		return Claims{}, errors.New("oidc client is not configured")
	}
	token, err := c.exchange(ctx, strings.TrimSpace(code))
	if err != nil {
		return Claims{}, err
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || strings.TrimSpace(rawIDToken) == "" {
		return Claims{}, errors.New("oidc token response did not include id_token")
	}
	claims, nonce, err := c.verify(ctx, rawIDToken)
	if err != nil {
		return Claims{}, err
	}
	if err := c.validateAuthTime(claims.AuthTime); err != nil {
		return Claims{}, err
	}
	if expectedNonce == "" || nonce != expectedNonce {
		return Claims{}, fmt.Errorf("oidc nonce mismatch")
	}
	if strings.TrimSpace(claims.Subject) == "" {
		return Claims{}, errors.New("oidc subject is required")
	}
	return claims, nil
}

func (c *Client) validateAuthTime(authTime time.Time) error {
	if authTime.IsZero() {
		return errors.New("oidc auth_time is required")
	}
	now := time.Now
	if c != nil && c.now != nil {
		now = c.now
	}
	nowAt := now().UTC()
	authTime = authTime.UTC()
	if authTime.After(nowAt) {
		return errors.New("oidc auth_time is in the future")
	}
	if nowAt.Sub(authTime) > interactiveMaxAge {
		return errors.New("oidc auth_time is stale")
	}
	return nil
}

func newTestClient(oauthConfig *oauth2.Config, exchange func(context.Context, string) (*oauth2.Token, error), verify func(context.Context, string) (Claims, string, error)) *Client {
	return &Client{oauth: oauthConfig, exchange: exchange, verify: verify, now: time.Now}
}

func uniqueScopes(scopes []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		out = append(out, scope)
	}
	return out
}
