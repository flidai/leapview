package app

import (
	"context"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform"
)

func testAuth(store *platform.Store, projectID string, cfg accessmodule.AuthConfig) *accessmodule.Auth {
	_ = projectID
	repo := accesssqlite.NewRepository(store.SQLDB())
	if cfg.DevBypass {
		_, _ = repo.SetPlatformRole(context.Background(), access.PlatformRoleInput{
			PrincipalID: "dev",
			Email:       "dev@localhost",
			DisplayName: "Local Developer",
			Role:        access.PlatformRoleAdmin,
		})
	}
	return accessmodule.NewAuth(repo, cfg)
}
