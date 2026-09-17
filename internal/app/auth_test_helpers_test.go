package app

import (
	"context"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
)

func testAuth(store *testControlStore, cfg accessmodule.AuthConfig) *accessmodule.Auth {
	if cfg.CSRFKey == "" {
		cfg.CSRFKey = "0123456789abcdef0123456789abcdef"
	}
	repo := store.fixture.Graph.Access
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
