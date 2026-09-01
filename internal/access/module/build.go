package module

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/avatar"
	"github.com/flidai/leapview/internal/access/desktopauth"
	"github.com/flidai/leapview/internal/access/http/mcpoauth"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type Config struct {
	// Persistence is the capability-owned authority bundle.
	Persistence                  *Persistence
	Production                   bool
	Auth                         AuthConfig
	ExistingAuth                 *Auth
	PublicURL                    string
	InstanceID                   string
	MCPIssuerURL                 string
	CurrentEffectiveCapabilities func(context.Context, string) ([]access.Capability, error)
	CurrentProjectID             func(context.Context) (projectgraph.ResourceID, error)
	Presentation                 webpage.Presentation
	Assets                       staticasset.Resolver
	AvatarBlobs                  avatar.BlobStore
}

// NewSQLiteAuditStore constructs the local development/evaluation SQLite
// audit store. It is intentionally named for its backend so PostgreSQL
// composition cannot accidentally select a database/sql audit authority.
// PostgreSQL callers inject a transaction-bound audit recorder from their
// authority.
func NewSQLiteAuditStore(database *sql.DB) access.AuditStore {
	if database == nil {
		return nil
	}
	return accesssqlite.NewRepository(database)
}

func Build(ctx context.Context, config Config) (*Module, error) {
	if config.Production && config.Persistence == nil {
		return nil, errors.New("production access build requires injected PostgreSQL persistence")
	}
	if config.Persistence == nil {
		auth := config.ExistingAuth
		surface := surfaceConfig{
			Persistence: config.Persistence,
			Auth:        auth, CurrentEffectiveCapabilities: config.CurrentEffectiveCapabilities,
			CurrentProjectID: config.CurrentProjectID,
			Presentation:     config.Presentation, Assets: config.Assets,
		}
		if auth != nil {
			surface.CurrentPrincipal = auth.Principal
			surface.CurrentCredential = auth.APICredential
		}
		return newSurface(surface)
	}
	if config.Production && !config.Persistence.isPostgres() {
		return nil, errors.New("production access build requires PostgreSQL persistence")
	}
	if err := config.Persistence.Validate(); err != nil {
		return nil, err
	}
	oauth := config.Persistence.OAuth
	repository := config.Persistence.Repository
	var avatarService *avatar.Service
	if config.AvatarBlobs != nil {
		avatarRepository, ok := repository.(avatar.Repository)
		if !ok {
			return nil, fmt.Errorf("access repository does not support avatar metadata")
		}
		var err error
		avatarService, err = avatar.New(avatarRepository, config.AvatarBlobs)
		if err != nil {
			return nil, err
		}
	}
	publicURL := strings.TrimSuffix(strings.TrimSpace(config.PublicURL), "/")
	if publicURL == "" {
		publicURL = "http://localhost:8080"
	}
	var authoringAuth *access.AuthoringAuthService
	if strings.TrimSpace(config.InstanceID) != "" {
		authoringRepository, ok := repository.(access.AuthoringAuthRepository)
		if !ok {
			return nil, fmt.Errorf("access repository does not support authoring authentication")
		}
		var err error
		authoringAuth, err = access.NewAuthoringAuthService(authoringRepository, access.AuthoringAuthConfig{
			InstanceID: config.InstanceID, CanonicalOrigin: publicURL,
			DeviceTTL: 10 * time.Minute, PollInterval: 5 * time.Second,
			AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 30 * 24 * time.Hour,
			WorkloadMaxTTL: time.Hour,
		})
		if err != nil {
			return nil, err
		}
	}
	auth := config.ExistingAuth
	if auth == nil && !config.Auth.Disabled {
		auth = NewAuth(repository, config.Auth)
	}
	if auth != nil {
		auth.authoringAuth = authoringAuth
	}
	surface := surfaceConfig{
		Persistence:                  config.Persistence,
		Repository:                   func() (access.Repository, error) { return repository, nil },
		Auth:                         auth,
		CurrentEffectiveCapabilities: config.CurrentEffectiveCapabilities,
		CurrentProjectID:             config.CurrentProjectID,
		AuthoringAuth:                authoringAuth,
		Avatar:                       avatarService,
		Presentation:                 config.Presentation,
		Assets:                       config.Assets,
	}
	if auth != nil {
		surface.CurrentPrincipal = func(r *http.Request) (Principal, bool) {
			principal, ok := auth.Principal(r)
			return principal, ok
		}
		surface.CurrentCredential = auth.APICredential
	}
	module, err := newSurface(surface)
	if err != nil {
		return nil, err
	}
	if auth == nil {
		return module, nil
	}
	if oauth != nil {
		module.oauth = oauth
		module.oauthResource = oauth
	} else if issuer := strings.TrimSpace(config.MCPIssuerURL); issuer != "" {
		module.oauthResource, err = mcpoauth.NewExternal(repository, mcpoauth.ExternalConfig{IssuerURL: issuer, ResourceURL: publicURL + "/mcp"})
	} else if database := config.Persistence.legacyDatabase; database != nil {
		module.oauth, err = mcpoauth.New(database, repository, mcpoauth.Config{
			IssuerURL: publicURL, ResourceURL: publicURL + "/mcp", Secret: auth.MCPOAuthSecret(),
		})
		module.oauthResource = module.oauth
	} else {
		return nil, errors.New("MCP OAuth requires injected PostgreSQL-backed service or external resource")
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.InstanceID) != "" {
		desktopStore, ok := repository.(desktopauth.Store)
		if !ok {
			return nil, errors.New("desktop authorization store is unavailable")
		}
		module.desktopAuth, err = desktopauth.New(desktopStore, desktopauth.Config{
			InstanceID: config.InstanceID,
		})
		if err != nil {
			return nil, err
		}
	}
	return module, nil
}

func (m *Module) OAuthResource() mcpoauth.ResourceServer {
	if m == nil {
		return nil
	}
	return m.oauthResource
}

func (m *Module) OAuthService() *mcpoauth.Service {
	if m == nil {
		return nil
	}
	return m.oauth
}

func (m *Module) repositoryValue() access.Repository {
	if m == nil || m.repository == nil {
		return nil
	}
	repository, _ := m.repository()
	return repository
}

// AuthorizationSubjects resolves the complete subject set used by canonical
// resource authorization. The principal is always included; group membership
// is fetched by the repository's indexed principal lookup rather than by
// enumerating all groups. A missing resolver is an authorization failure, not
// an invitation to silently fall back to principal-only access.
func (m *Module) AuthorizationSubjects(ctx context.Context, principalID string) ([]access.SubjectRef, error) {
	repository := m.repositoryValue()
	if repository == nil {
		return nil, fmt.Errorf("access repository is unavailable")
	}
	resolver, ok := repository.(principalGroupResolver)
	if !ok {
		return nil, fmt.Errorf("access repository does not support principal group resolution")
	}
	return authorizationSubjects(ctx, principalID, resolver)
}

type principalGroupResolver interface {
	ListGroupIDsForPrincipal(context.Context, string) ([]string, error)
}

func authorizationSubjects(ctx context.Context, principalID string, resolver principalGroupResolver) ([]access.SubjectRef, error) {
	principal, err := access.NewSubjectRef(access.SubjectKindPrincipal, principalID)
	if err != nil {
		return nil, err
	}
	groupIDs, err := resolver.ListGroupIDsForPrincipal(ctx, principal.ID)
	if err != nil {
		return nil, fmt.Errorf("resolve principal groups: %w", err)
	}
	subjects := make([]access.SubjectRef, 1, 1+len(groupIDs))
	subjects[0] = principal
	for _, groupID := range groupIDs {
		group, err := access.NewSubjectRef(access.SubjectKindGroup, groupID)
		if err != nil {
			return nil, fmt.Errorf("resolve principal group %q: %w", groupID, err)
		}
		subjects = append(subjects, group)
	}
	return subjects, nil
}

func (m *Module) SeedLocalDeveloperPlatformAdmin(ctx context.Context) error {
	if m == nil {
		return nil
	}
	repository := m.repositoryValue()
	if repository == nil {
		return nil
	}
	principal := LocalDeveloperPrincipal()
	_, err := repository.SetPlatformRole(ctx, access.PlatformRoleInput{
		PrincipalID: principal.ID, Email: principal.Email, DisplayName: principal.DisplayName, Role: access.PlatformRoleAdmin,
	})
	return err
}
