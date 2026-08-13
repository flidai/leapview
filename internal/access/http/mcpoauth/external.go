package mcpoauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	coreosoidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/flidai/leapview/internal/access"
)

type ExternalConfig struct {
	IssuerURL   string
	ResourceURL string
	HTTPClient  *http.Client
}

type External struct {
	config   ExternalConfig
	repo     access.Repository
	mu       sync.Mutex
	verifier *coreosoidc.IDTokenVerifier
}

func NewExternal(repo access.Repository, config ExternalConfig) (*External, error) {
	if repo == nil {
		return nil, fmt.Errorf("external MCP OAuth requires an access repository")
	}
	config.IssuerURL = strings.TrimSuffix(strings.TrimSpace(config.IssuerURL), "/")
	config.ResourceURL = strings.TrimSuffix(strings.TrimSpace(config.ResourceURL), "/")
	if err := validateCanonicalURL(config.IssuerURL, true); err != nil {
		return nil, fmt.Errorf("invalid external MCP OAuth issuer: %w", err)
	}
	if err := validateCanonicalURL(config.ResourceURL, true); err != nil {
		return nil, fmt.Errorf("invalid external MCP OAuth resource: %w", err)
	}
	return &External{repo: repo, config: config}, nil
}

func (e *External) Authenticate(ctx context.Context, rawToken string) (Credential, error) {
	if strings.TrimSpace(rawToken) == "" {
		return Credential{}, fmt.Errorf("OAuth access token is required")
	}
	verifier, err := e.tokenVerifier(ctx)
	if err != nil {
		return Credential{}, err
	}
	token, err := verifier.Verify(ctx, rawToken)
	if err != nil {
		return Credential{}, fmt.Errorf("verify external OAuth token: %w", err)
	}
	var claims struct {
		Subject string          `json:"sub"`
		Email   string          `json:"email"`
		Name    string          `json:"name"`
		Scope   string          `json:"scope"`
		Scopes  json.RawMessage `json:"scp"`
	}
	if err := token.Claims(&claims); err != nil {
		return Credential{}, fmt.Errorf("decode external OAuth token: %w", err)
	}
	scopes := strings.Fields(claims.Scope)
	if len(claims.Scopes) > 0 {
		var list []string
		if err := json.Unmarshal(claims.Scopes, &list); err == nil {
			scopes = append(scopes, list...)
		} else {
			var raw string
			if err := json.Unmarshal(claims.Scopes, &raw); err == nil {
				scopes = append(scopes, strings.Fields(raw)...)
			}
		}
	}
	if !slicesContains(scopes, ScopeMCPUse) || strings.TrimSpace(claims.Subject) == "" {
		return Credential{}, fmt.Errorf("external OAuth token lacks the required subject or mcp:use scope")
	}
	principal, err := e.repo.ResolveExternalPrincipal(ctx, access.ExternalIdentityInput{
		Provider: "mcp-oauth", TenantID: e.config.IssuerURL, Subject: claims.Subject,
		Email: claims.Email, DisplayName: firstNonEmpty(claims.Name, claims.Email, claims.Subject),
	})
	if err != nil {
		return Credential{}, fmt.Errorf("resolve external OAuth principal: %w", err)
	}
	if principal.AccessDisabled() {
		return Credential{}, fmt.Errorf("external OAuth principal is disabled")
	}
	return Credential{Principal: principal, Resource: e.config.ResourceURL, Scopes: uniqueStrings(scopes)}, nil
}

func (e *External) tokenVerifier(ctx context.Context) (*coreosoidc.IDTokenVerifier, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.verifier != nil {
		return e.verifier, nil
	}
	if e.config.HTTPClient != nil {
		ctx = coreosoidc.ClientContext(ctx, e.config.HTTPClient)
	}
	provider, err := coreosoidc.NewProvider(ctx, e.config.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("discover external OAuth issuer: %w", err)
	}
	e.verifier = provider.Verifier(&coreosoidc.Config{ClientID: e.config.ResourceURL})
	return e.verifier, nil
}

func (e *External) ProtectedResourceMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 e.config.ResourceURL,
		"authorization_servers":    []string{e.config.IssuerURL},
		"scopes_supported":         []string{ScopeMCPUse},
		"bearer_methods_supported": []string{"header"},
	})
}

func (e *External) Challenge(w http.ResponseWriter) {
	metadata := strings.TrimSuffix(e.config.ResourceURL, "/mcp") + "/.well-known/oauth-protected-resource/mcp"
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata=%q, scope=%q`, metadata, ScopeMCPUse))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"invalid_token","error_description":"A valid MCP OAuth access token is required"}`))
}

func slicesContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
