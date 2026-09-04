package module

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access/http/mcpoauth"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
)

// PostgresBuildConfig supplies the already-open production control handle and
// the purpose-separated fingerprint key. Connection and schema lifecycle stay
// with the application/platform composition roots; the access module owns its
// concrete repository and OAuth adapters.
type PostgresBuildConfig struct {
	Database       accesspostgres.DBTX
	FingerprintKey []byte
}

// BuildPostgres constructs the complete PostgreSQL-backed access capability
// behind the module boundary. This keeps concrete access adapters out of the
// application composition package while preserving the shared pool authority.
func BuildPostgres(ctx context.Context, config Config, postgres PostgresBuildConfig) (*Module, error) {
	if !config.Production {
		return nil, errors.New("PostgreSQL access build requires production mode")
	}
	if config.Database != nil || config.Persistence != nil || config.ExistingAuth != nil {
		return nil, errors.New("PostgreSQL access build rejects preconfigured persistence")
	}
	repository, err := accesspostgres.NewAccess(postgres.Database, accesspostgres.FingerprintConfig{Key: postgres.FingerprintKey})
	if err != nil {
		return nil, fmt.Errorf("build PostgreSQL access repository: %w", err)
	}
	var auth *Auth
	if !config.Auth.Disabled {
		auth = NewAuth(repository, config.Auth)
	}
	var oauth *mcpoauth.Service
	if auth != nil && strings.TrimSpace(config.MCPIssuerURL) == "" {
		publicURL := strings.TrimSuffix(strings.TrimSpace(config.PublicURL), "/")
		if publicURL == "" {
			publicURL = "http://localhost:8080"
		}
		oauth, err = mcpoauth.NewPostgres(postgres.Database, repository, mcpoauth.Config{
			IssuerURL: publicURL, ResourceURL: publicURL + "/mcp", Secret: auth.MCPOAuthSecret(),
		})
		if err != nil {
			return nil, fmt.Errorf("build PostgreSQL MCP OAuth service: %w", err)
		}
	}
	persistence, err := NewPostgresPersistence(repository, oauth)
	if err != nil {
		return nil, fmt.Errorf("build PostgreSQL access persistence: %w", err)
	}
	config.Persistence = &persistence
	config.ExistingAuth = auth
	return Build(ctx, config)
}
