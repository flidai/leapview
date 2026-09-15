package app

import (
	"context"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform"
)

func testAuth(store *platform.Store, cfg accessmodule.AuthConfig) *accessmodule.Auth {
	if cfg.CSRFKey == "" {
		cfg.CSRFKey = "0123456789abcdef0123456789abcdef"
	}
	repo := accesssqlite.NewRepository(store.SQLDB())
	if cfg.DevBypass {
		_, _ = repo.SetPlatformRole(context.Background(), access.PlatformRoleInput{
			PrincipalID: accessmodule.DevelopmentPrincipalID,
			Email:       "dev@localhost",
			DisplayName: "Local Developer",
			Role:        access.PlatformRoleAdmin,
		})
	}
	auth, err := accessmodule.NewAuth(repo, cfg)
	if err != nil {
		panic(err)
	}
	return auth
}
