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
)

type Config struct {
	Database     *sql.DB
	WorkspaceID  string
	Auth         AuthConfig
	ExistingAuth *Auth
	WorkspaceIDs func(context.Context) ([]string, error)
	PublicURL    string
	InstanceID   string
	MCPIssuerURL string
	Presentation webpage.Presentation
	Assets       staticasset.Resolver
	AvatarBlobs  avatar.BlobStore
}

func newRepository(database *sql.DB) access.Repository { return accesssqlite.NewRepository(database) }

func Build(ctx context.Context, config Config) (*Module, error) {
	if config.Database == nil {
		auth := config.ExistingAuth
		surface := surfaceConfig{
			Auth: auth, WorkspaceIDs: config.WorkspaceIDs, DefaultWorkspaceID: config.WorkspaceID, Presentation: config.Presentation, Assets: config.Assets,
			WorkspaceID: func(value string) string {
				if value != "" {
					return value
				}
				return config.WorkspaceID
			},
		}
		if auth != nil {
			surface.CurrentPrincipal = auth.Principal
			surface.CurrentCredential = auth.APICredential
		}
		return newSurface(surface)
	}
	if err := accesssqlite.Initialize(ctx, config.Database); err != nil {
		return nil, err
	}
	repository := newRepository(config.Database)
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
		auth = NewAuth(repository, config.WorkspaceID, config.Auth)
	}
	if auth != nil {
		auth.authoringAuth = authoringAuth
	}
	surface := surfaceConfig{
		Repository: func() (access.Repository, error) { return repository, nil },
		Auth:       auth, WorkspaceIDs: config.WorkspaceIDs,
		AuthoringAuth:      authoringAuth,
		Avatar:             avatarService,
		Presentation:       config.Presentation,
		Assets:             config.Assets,
		DefaultWorkspaceID: config.WorkspaceID,
		WorkspaceID: func(value string) string {
			if value != "" {
				return value
			}
			return config.WorkspaceID
		},
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
	if issuer := strings.TrimSpace(config.MCPIssuerURL); issuer != "" {
		module.oauthResource, err = mcpoauth.NewExternal(repository, mcpoauth.ExternalConfig{IssuerURL: issuer, ResourceURL: publicURL + "/mcp"})
	} else {
		module.oauth, err = mcpoauth.New(config.Database, repository, mcpoauth.Config{
			IssuerURL: publicURL, ResourceURL: publicURL + "/mcp", Secret: auth.MCPOAuthSecret(),
		})
		module.oauthResource = module.oauth
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
		PrincipalID: principal.ID, Email: principal.Email, DisplayName: principal.DisplayName, Role: access.RolePlatformAdmin,
	})
	return err
}
