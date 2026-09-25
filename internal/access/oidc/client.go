package oidc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	coreosoidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/flidai/leapview/internal/platform/outbound"
	"golang.org/x/oauth2"
)

type Config struct {
	ID           string
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
	HTTPClient   *http.Client
}

type Claims struct {
	Issuer            string
	Subject           string
	Email             string
	Name              string
	PreferredUsername string
}

type Client struct {
	oauth    *oauth2.Config
	verify   func(context.Context, string) (Claims, string, error)
	exchange func(context.Context, string) (*oauth2.Token, error)
}

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
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = outbound.New(outbound.ExplicitPrivate, outbound.Options{}).HTTPClient(
			&http.Client{Timeout: 15 * time.Second},
			outbound.HTTPConfig{AllowedSchemes: []string{"https"}, MaxRedirects: 5, SameOriginRedirects: true},
		)
	}
	providerContext := coreosoidc.ClientContext(ctx, httpClient)
	provider, err := coreosoidc.NewProvider(providerContext, cfg.IssuerURL)
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
			}
			if err := idToken.Claims(&raw); err != nil {
				return Claims{}, "", err
			}
			return Claims{
				Issuer:            idToken.Issuer,
				Subject:           idToken.Subject,
				Email:             raw.Email,
				Name:              raw.Name,
				PreferredUsername: raw.PreferredUsername,
			}, idToken.Nonce, nil
		},
		exchange: func(ctx context.Context, code string) (*oauth2.Token, error) {
			return oauthConfig.Exchange(coreosoidc.ClientContext(ctx, httpClient), code)
		},
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
	return c.oauth.AuthCodeURL(state, coreosoidc.Nonce(nonce))
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
	if expectedNonce == "" || nonce != expectedNonce {
		return Claims{}, fmt.Errorf("oidc nonce mismatch")
	}
	if strings.TrimSpace(claims.Subject) == "" {
		return Claims{}, errors.New("oidc subject is required")
	}
	return claims, nil
}

func newTestClient(oauthConfig *oauth2.Config, exchange func(context.Context, string) (*oauth2.Token, error), verify func(context.Context, string) (Claims, string, error)) *Client {
	return &Client{oauth: oauthConfig, exchange: exchange, verify: verify}
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
