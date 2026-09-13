package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	apiprotocol "github.com/flidai/leapview/internal/app/api/protocol"
	"github.com/flidai/leapview/internal/app/brand"
	"github.com/flidai/leapview/internal/platform/http/cursorsigning"
	"github.com/flidai/leapview/internal/platform/http/idempotency"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	releasemodule "github.com/flidai/leapview/internal/release/module"
	"github.com/flidai/leapview/internal/servingstate"
)

type apiProtocolPersistence struct {
	Idempotency               idempotency.Store
	CursorSigning             cursorsigning.Initializer
	BypassDurableIdempotency  map[string]struct{}
	ReclaimExpiredIdempotency map[string]struct{}
	RequireExplicit           bool
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
	// Profile-only/test assemblies may intentionally use process-local
	// authorities. Runnable application composition always injects the native
	// PostgreSQL authorities before entering this seam.
	return idempotency.NewMemoryStore(), cursorsigning.NewEphemeralInitializer(), nil
}

func configureAPIProtocol(routes *capabilityRoutes, runtime *runtimeServices, platform *platformServices, policy *httpPolicy, runtimeConfig runtimeAssemblyInputs, ctx context.Context, persistence apiProtocolPersistence) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Every durable runtime supplies explicit authorities. Profile-only
	// assemblies may use the process-local defaults selected by authorities().
	idempotencyStore, cursorInitializer, err := persistence.authorities()
	if err != nil {
		return err
	}
	idempotencyProjectID := runtime.resolveProjectID
	if runtimeConfig.IdempotencyProjectIDResolver != nil {
		idempotencyProjectID = runtimeConfig.IdempotencyProjectIDResolver
	}
	protocol, err := apiprotocol.Build(ctx, apiprotocol.Config{
		Store:                     idempotencyStore,
		BypassDurableIdempotency:  persistence.BypassDurableIdempotency,
		ReclaimExpiredIdempotency: persistence.ReclaimExpiredIdempotency,
		CursorSigning:             cursorInitializer,
		ProductName:               brand.Name,
		BearerToken:               accessmodule.BearerToken,
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
		AuthoritativeScope: func(r *http.Request) (apiprotocol.AuthoritativeScope, error) {
			return authoritativeAPIIdempotencyScope(r, idempotencyProjectID, runtimeConfig)
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

func authoritativeAPIIdempotencyScope(r *http.Request, resolveProjectID func(context.Context) (projectgraph.ResourceID, error), config runtimeAssemblyInputs) (apiprotocol.AuthoritativeScope, error) {
	scope := apiprotocol.AuthoritativeScope{
		TargetID: strings.TrimSpace(config.InstanceID), Environment: strings.TrimSpace(config.DefaultEnvironment),
	}
	if scope.TargetID == "" || scope.TargetID != config.InstanceID || scope.Environment == "" || scope.Environment != config.DefaultEnvironment {
		return apiprotocol.AuthoritativeScope{}, errors.New("API idempotency target and environment identities are required")
	}
	if !projectScopedIdempotencyRequest(r) {
		return scope, nil
	}
	if resolveProjectID == nil {
		return apiprotocol.AuthoritativeScope{}, errors.New("API idempotency Project identity authority is unavailable")
	}
	projectID, err := resolveProjectID(r.Context())
	if err != nil {
		return apiprotocol.AuthoritativeScope{}, fmt.Errorf("resolve API idempotency Project identity: %w", err)
	}
	if err := projectID.Validate(); err != nil {
		return apiprotocol.AuthoritativeScope{}, fmt.Errorf("validate API idempotency Project identity: %w", err)
	}
	scope.ProjectID = projectID.String()
	if config.ServingSnapshotResolver == nil {
		return scope, nil
	}
	generation, err := config.ServingSnapshotResolver(r.Context())
	if err != nil {
		if errors.Is(err, servingstate.ErrNotFound) {
			return scope, nil
		}
		return apiprotocol.AuthoritativeScope{}, fmt.Errorf("resolve API idempotency generation identity: %w", err)
	}
	scope.GenerationID = strings.TrimSpace(generation)
	if scope.GenerationID == "" || scope.GenerationID != generation {
		return apiprotocol.AuthoritativeScope{}, errors.New("API idempotency generation identity is non-canonical")
	}
	return scope, nil
}

func projectScopedIdempotencyRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
		// Browser mutation middleware is mounted only on Project authoring and
		// publication commands.
		return true
	}
	contract, ok := apiaggregate.GetAPIGenOperationContractForRequest(r.Method, r.URL.Path)
	return ok && strings.Contains(contract.Path, "{project}")
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
