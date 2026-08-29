package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	apiprotocol "github.com/flidai/leapview/internal/app/api/protocol"
	"github.com/flidai/leapview/internal/app/brand"
	"github.com/flidai/leapview/internal/platform/http/cursorsigning"
	cursorsigningsqlite "github.com/flidai/leapview/internal/platform/http/cursorsigning/sqlite"
	"github.com/flidai/leapview/internal/platform/http/idempotency"
	idempotencysqlite "github.com/flidai/leapview/internal/platform/http/idempotency/sqlite"
	releasemodule "github.com/flidai/leapview/internal/release/module"
)

type apiProtocolPersistence struct {
	Database        *sql.DB
	Idempotency     idempotency.Store
	CursorSigning   cursorsigning.Initializer
	RequireExplicit bool
}

func (p apiProtocolPersistence) authorities() (idempotency.Store, cursorsigning.Initializer, error) {
	if (p.Idempotency == nil) != (p.CursorSigning == nil) {
		return nil, nil, errors.New("API protocol requires both idempotency and cursor-signing authorities")
	}
	if p.Idempotency != nil {
		return p.Idempotency, p.CursorSigning, nil
	}
	if p.RequireExplicit {
		return nil, nil, errors.New("production API protocol requires explicit durable authorities")
	}
	if p.Database != nil {
		return idempotencysqlite.NewStore(p.Database), cursorsigningsqlite.NewInitializer(p.Database), nil
	}
	return idempotency.NewMemoryStore(), cursorsigning.NewEphemeralInitializer(), nil
}

func configureAPIProtocol(routes *capabilityRoutes, runtime *runtimeServices, platform *platformServices, policy *httpPolicy, ctx context.Context, persistence apiProtocolPersistence) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Profile-only assemblies use explicit process-local capabilities. A fully
	// configured runtime supplies the transitional SQLite fixture below;
	// PostgreSQL callers inject native adapters at this composition boundary.
	idempotencyStore, cursorInitializer, err := persistence.authorities()
	if err != nil {
		return err
	}
	protocol, err := apiprotocol.Build(ctx, apiprotocol.Config{
		Store:         idempotencyStore,
		CursorSigning: cursorInitializer,
		ProductName:   brand.Name,
		BearerToken:   accessmodule.BearerToken,
		AcceptsBearer: func(r *http.Request) bool {
			return platform.auth == nil || platform.auth.AcceptsPublicBearer(r)
		},
		PrincipalID: func(r *http.Request) (string, bool) {
			if platform.auth == nil {
				return "", false
			}
			principal, _, ok := platform.auth.Authenticate(r)
			return principal.ID, ok
		},
		ReplayAuthorize: func(r *http.Request) bool {
			if platform.auth == nil {
				return true
			}
			_, _, ok := platform.auth.Authenticate(r)
			return ok
		},
		PublicRequest: isPublicAPIGenRequest,
		CursorSnapshot: func(r *http.Request) string {
			return cursorSnapshot(routes.releaseModule, r)
		},
	})
	if err != nil {
		return err
	}
	platform.apiProtocol = protocol
	return nil
}

func isPublicAPIGenRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	for _, contract := range apiaggregate.GetAPIGenOperationContracts() {
		if contract.AuthzMode == "none" && contract.Method == r.Method && contract.Path == r.URL.Path {
			return true
		}
	}
	return false
}

func publicProtocolMiddleware(protocol *apiprotocol.Protocol, next http.Handler) http.Handler {
	return protocol.Middleware(next)
}

func openAPIDescription(protocol *apiprotocol.Protocol, w http.ResponseWriter, r *http.Request) {
	protocol.OpenAPIDescription(w, r)
}

func publicDocs(protocol *apiprotocol.Protocol, w http.ResponseWriter, r *http.Request) {
	protocol.PublicDocs(w, r)
}

func cursorSnapshot(releases *releasemodule.Module, r *http.Request) string {
	segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	for index, segment := range segments {
		if index+1 >= len(segments) {
			continue
		}
		switch segment {
		case "projects":
			if releases != nil {
				if snapshot := releases.ProjectCursorSnapshot(r, segments[index+1]); snapshot != "" {
					return snapshot
				}
			}
		}
	}
	return ""
}
