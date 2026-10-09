package app

import (
	"context"
	"net/http"

	apiprotocol "github.com/flidai/leapview/internal/app/api/protocol"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/internal/platform/typednil"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

type firstSourcePreparationBrowserRoutes struct {
	config   credentialmodule.CredentialDraftAPIGenConfig
	targetID string
	project  func(context.Context) (projectgraph.ResourceID, error)
}

func newFirstSourceCredentialPreparationRoutes(config credentialmodule.CredentialDraftAPIGenConfig, target string, project func(context.Context) (projectgraph.ResourceID, error)) *firstSourcePreparationBrowserRoutes {
	if typednil.IsNil(config.FirstSourcePreparation) || config.CurrentPrincipal == nil || target == "" || config.Environment == "" || project == nil {
		return nil
	}
	return &firstSourcePreparationBrowserRoutes{config: config, targetID: target, project: project}
}

type firstSourcePreparationRouteScopeKey struct{}

// Mount inside the existing private-response/CSRF browser route group. Public
// API authentication is unchanged; these routes use real session evidence and
// generated replay-forbidden UI commands before reading any request body.
func mountFirstSourceCredentialPreparationRoutes(r chi.Router, routes *firstSourcePreparationBrowserRoutes, authenticate func(http.Handler) http.Handler, protocol *apiprotocol.Protocol) {
	if routes == nil || authenticate == nil || protocol == nil {
		return
	}
	for _, endpoint := range []struct{ operation, path, identity string }{
		{"prepareFirstSourceCredential", "/connections/{connection}/credential-drafts/{version}/prepare-first-source", "version"},
		{"renewFirstSourceCredentialPreparation", "/connections/{connection}/first-source-preparations/{preparation}/renew", "preparation"},
	} {
		binding, ok := credentialmodule.FirstSourcePreparationBrowserBinding(endpoint.operation)
		if !ok {
			continue
		}
		handler, err := protocol.BrowserNonReplayableMutationMiddleware(binding, func(request *http.Request) bool {
			resource, ok := request.Context().Value(firstSourcePreparationRouteScopeKey{}).(credentialmodule.ValidationResource)
			actor, found := routes.config.CurrentPrincipal(request)
			return ok && found && routes.config.FirstSourcePreparation.Authorize(request.Context(), actor, resource) == nil
		}, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			resource, ok := request.Context().Value(firstSourcePreparationRouteScopeKey{}).(credentialmodule.ValidationResource)
			if !ok {
				apitransport.WriteProblem(w, request, http.StatusForbidden, "FIRST_SOURCE_PREPARATION_FORBIDDEN", "First-source preparation is forbidden.", nil)
				return
			}
			credentialmodule.DispatchFirstSourcePreparationBrowser(routes.config, endpoint.operation, w, request, resource.ProjectID, resource.TargetID, resource.ResourceID, chi.URLParam(request, endpoint.identity))
		}))
		if err != nil {
			continue // Missing canonical command proof leaves the route closed.
		}
		r.With(authenticate).Post(endpoint.path, routes.bindScope(handler).ServeHTTP)
	}
}

func (routes *firstSourcePreparationBrowserRoutes) bindScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		project, err := routes.project(request.Context())
		if err != nil || project.Validate() != nil {
			apitransport.WriteProblem(w, request, http.StatusServiceUnavailable, "FIRST_SOURCE_PREPARATION_UNAVAILABLE", "First-source preparation is unavailable.", nil)
			return
		}
		resource := credentialmodule.ValidationResource{ScopeKind: "connection", ProjectID: project.String(), TargetID: routes.targetID, Environment: routes.config.Environment, ResourceID: chi.URLParam(request, "connection")}
		if resource.Validate() != nil {
			apitransport.WriteProblem(w, request, http.StatusBadRequest, "INVALID_FIRST_SOURCE_PREPARATION", "First-source preparation request is invalid.", nil)
			return
		}
		next.ServeHTTP(w, request.WithContext(context.WithValue(request.Context(), firstSourcePreparationRouteScopeKey{}, resource)))
	})
}
