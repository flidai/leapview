package app

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
)

// Test-local aliases keep the aggregate integration fixtures readable while
// production code uses access/module directly.
type Principal = accessmodule.Principal
type AuthConfig = accessmodule.AuthConfig
type oidcClient = accessmodule.OIDCClient

var errUnauthorized = accessmodule.ErrUnauthorized

const authReturnCookieName = accessmodule.AuthReturnCookieName
const csrfCookieName = accessmodule.CSRFCookieName
const oidcStateCookieName = accessmodule.OIDCStateCookieName

func NewAuth(repo access.Repository, config AuthConfig) *accessmodule.Auth {
	return accessmodule.NewAuth(repo, config)
}

func withPrincipal(ctx context.Context, principal Principal) context.Context {
	return accessmodule.WithPrincipal(ctx, principal)
}

func withAPICredential(ctx context.Context, credential access.APICredential) context.Context {
	return accessmodule.WithAPICredential(ctx, credential)
}

func apiTokenAllows(token access.APIToken, workspaceID string, privilege access.Privilege) bool {
	return accessmodule.TokenAllows(token, workspaceID, privilege)
}

func bearerToken(r *http.Request) string { return accessmodule.BearerToken(r) }

func setAuthRandomReaderForTest(reader io.Reader) func() {
	return accessmodule.SetAuthRandomReaderForTest(reader)
}

func setAuthNowForTest(now time.Time) func() { return accessmodule.SetAuthNowForTest(now) }

func SeedLocalDeveloperPlatformAdmin(ctx context.Context, repo access.Repository) error {
	if repo == nil {
		return nil
	}
	principal := accessmodule.LocalDeveloperPrincipal()
	_, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{
		PrincipalID: principal.ID,
		Email:       principal.Email,
		DisplayName: principal.DisplayName,
		Role:        access.RolePlatformAdmin,
	})
	return err
}
