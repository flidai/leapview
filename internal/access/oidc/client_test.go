package oidc

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestAuthCodeURLIncludesStateNonceScopesAndRedirect(t *testing.T) {
	client := newTestClient(&oauth2.Config{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		RedirectURL:  "https://app.example/auth/azureadv2/callback",
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://issuer.example/authorize",
			TokenURL: "https://issuer.example/token",
		},
		Scopes: []string{"openid", "profile", "email"},
	}, nil, nil)

	authURL := client.AuthCodeURL("state-value", "nonce-value")
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	values := parsed.Query()
	if parsed.Scheme != "https" || parsed.Host != "issuer.example" || parsed.Path != "/authorize" {
		t.Fatalf("auth URL = %s, want issuer authorize endpoint", authURL)
	}
	for key, want := range map[string]string{
		"client_id":     "client-id",
		"redirect_uri":  "https://app.example/auth/azureadv2/callback",
		"response_type": "code",
		"state":         "state-value",
		"nonce":         "nonce-value",
		"max_age":       "900",
	} {
		if got := values.Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	for _, scope := range []string{"openid", "profile", "email"} {
		if !strings.Contains(values.Get("scope"), scope) {
			t.Fatalf("scope %q missing from %q", scope, values.Get("scope"))
		}
	}
}

func TestAuthenticateRejectsMissingIDToken(t *testing.T) {
	client := newTestClient(&oauth2.Config{}, func(context.Context, string) (*oauth2.Token, error) {
		return &oauth2.Token{}, nil
	}, func(context.Context, string) (Claims, string, error) {
		t.Fatal("verify should not be called")
		return Claims{}, "", nil
	})

	if _, err := client.Authenticate(context.Background(), "code", "nonce"); err == nil {
		t.Fatal("Authenticate() error = nil, want missing id_token error")
	}
}

func TestAuthenticateRejectsInvalidNonce(t *testing.T) {
	client := newTestClient(&oauth2.Config{}, func(context.Context, string) (*oauth2.Token, error) {
		return (&oauth2.Token{}).WithExtra(map[string]any{"id_token": "raw-id-token"}), nil
	}, func(context.Context, string) (Claims, string, error) {
		return Claims{Subject: "sub", Email: "user@example.com"}, "actual-nonce", nil
	})

	if _, err := client.Authenticate(context.Background(), "code", "expected-nonce"); err == nil {
		t.Fatal("Authenticate() error = nil, want nonce mismatch")
	}
}

func TestAuthenticateMapsVerifiedClaims(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	client := newTestClient(&oauth2.Config{}, func(_ context.Context, code string) (*oauth2.Token, error) {
		if code != "auth-code" {
			t.Fatalf("code = %q, want auth-code", code)
		}
		return (&oauth2.Token{}).WithExtra(map[string]any{"id_token": "raw-id-token"}), nil
	}, func(_ context.Context, raw string) (Claims, string, error) {
		if raw != "raw-id-token" {
			t.Fatalf("raw token = %q, want raw-id-token", raw)
		}
		return Claims{Subject: "subject-1", Email: "user@example.com", Name: "User Example", PreferredUsername: "user", AuthTime: now.Add(-time.Minute)}, "nonce", nil
	})
	client.now = func() time.Time { return now }

	claims, err := client.Authenticate(context.Background(), "auth-code", "nonce")
	if err != nil {
		t.Fatalf("Authenticate(): %v", err)
	}
	if claims.Subject != "subject-1" || claims.Email != "user@example.com" || claims.Name != "User Example" || claims.PreferredUsername != "user" {
		t.Fatalf("claims = %#v", claims)
	}
}

func TestAuthenticateRejectsMissingOIDCAuthTime(t *testing.T) {
	client := newTestClient(&oauth2.Config{}, func(context.Context, string) (*oauth2.Token, error) {
		return (&oauth2.Token{}).WithExtra(map[string]any{"id_token": "raw-id-token"}), nil
	}, func(context.Context, string) (Claims, string, error) {
		return Claims{Subject: "subject-1"}, "nonce", nil
	})

	if _, err := client.Authenticate(context.Background(), "code", "nonce"); err == nil || !strings.Contains(err.Error(), "auth_time") {
		t.Fatalf("Authenticate() error = %v, want missing auth_time", err)
	}
}

func TestAuthenticateRejectsStaleAndFutureOIDCAuthTime(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name string
		at   time.Time
		want string
	}{
		{name: "stale", at: now.Add(-interactiveMaxAge - time.Second), want: "stale"},
		{name: "future", at: now.Add(time.Second), want: "future"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newTestClient(&oauth2.Config{}, func(context.Context, string) (*oauth2.Token, error) {
				return (&oauth2.Token{}).WithExtra(map[string]any{"id_token": "raw-id-token"}), nil
			}, func(context.Context, string) (Claims, string, error) {
				return Claims{Subject: "subject-1", AuthTime: test.at}, "nonce", nil
			})
			client.now = func() time.Time { return now }
			if _, err := client.Authenticate(context.Background(), "code", "nonce"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Authenticate() error = %v, want %s auth_time rejection", err, test.want)
			}
		})
	}
}

func TestAuthenticatePropagatesExchangeError(t *testing.T) {
	want := errors.New("exchange failed")
	client := newTestClient(&oauth2.Config{}, func(context.Context, string) (*oauth2.Token, error) {
		return nil, want
	}, nil)

	if _, err := client.Authenticate(context.Background(), "code", "nonce"); !errors.Is(err, want) {
		t.Fatalf("Authenticate() error = %v, want %v", err, want)
	}
}
