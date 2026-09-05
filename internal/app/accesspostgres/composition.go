// Package accesspostgres composes the access module's native PostgreSQL
// persistence at the application boundary. The access module receives only
// its capability bundle; concrete PostgreSQL and MCP OAuth adapters stay
// owned by this package.
package accesspostgres

import (
	"errors"
	"fmt"

	accesshttpoauth "github.com/flidai/leapview/internal/access/http/mcpoauth"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesspg "github.com/flidai/leapview/internal/access/postgres"
)

// InternalOAuthConfig contains the values required to run LeapView's own MCP
// OAuth authorization server. A nil config explicitly selects the external
// issuer path; access/module.Build uses the external issuer configured by the
// caller and receives a persistence bundle with nil OAuth state.
type InternalOAuthConfig struct {
	IssuerURL   string
	ResourceURL string
	Secret      []byte
}

// NewPersistence constructs the access module's native PostgreSQL authority
// bundle. The repository owns the sole database authority; MCP OAuth, when
// enabled, uses repository.DB() so OAuth state and access state cannot be
// accidentally split across pools.
//
// Passing nil for internalOAuth is intentional and leaves Persistence.OAuth
// nil for callers that selected an external MCP OAuth issuer. A non-nil config
// constructs a PostgreSQL-backed MCP OAuth service and validates its issuer,
// resource URL, and signing secret through the service constructor.
func NewPersistence(repository *accesspg.Repository, internalOAuth *InternalOAuthConfig) (accessmodule.Persistence, error) {
	if repository == nil {
		return accessmodule.Persistence{}, errors.New("PostgreSQL access repository is required")
	}

	var oauth *accesshttpoauth.Service
	if internalOAuth != nil {
		var err error
		oauth, err = accesshttpoauth.NewPostgres(repository.DB(), repository, accesshttpoauth.Config{
			IssuerURL:   internalOAuth.IssuerURL,
			ResourceURL: internalOAuth.ResourceURL,
			Secret:      internalOAuth.Secret,
		})
		if err != nil {
			return accessmodule.Persistence{}, fmt.Errorf("construct PostgreSQL MCP OAuth service: %w", err)
		}
	}

	persistence, err := accessmodule.NewPostgresPersistence(repository, oauth)
	if err != nil {
		return accessmodule.Persistence{}, fmt.Errorf("construct PostgreSQL access persistence: %w", err)
	}
	return persistence, nil
}
