package app

import (
	"net/http"

	apiprotocol "github.com/flidai/leapview/internal/app/api/protocol"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	uicommand "github.com/flidai/leapview/internal/platform/web/uicommand"
	projectmodule "github.com/flidai/leapview/internal/project/module"
	"github.com/flidai/leapview/pkg/pagestream"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/csrf"
)

func (routes *firstSourcePreparationBrowserRoutes) describe(w http.ResponseWriter, r *http.Request) (projectmodule.FirstSourceCredentialSignal, string, credentialmodule.ValidationResource, bool) {
	var result projectmodule.FirstSourceCredentialSignal
	resource, scoped := r.Context().Value(firstSourcePreparationRouteScopeKey{}).(credentialmodule.ValidationResource)
	actor, authenticated := routes.config.CurrentPrincipal(r)
	describer, capable := routes.config.FirstSourcePreparation.(credentialmodule.FirstSourceBrowserDescriber)
	if !scoped || !authenticated || !capable {
		http.Error(w, "First source credentials are unavailable.", http.StatusForbidden)
		return result, "", resource, false
	}
	description, err := describer.Describe(r.Context(), actor, resource)
	if err != nil {
		http.Error(w, "First source credentials are unavailable.", http.StatusForbidden)
		return result, "", resource, false
	}
	result.ConnectionID, result.Host, result.Database, result.SourceIdentity, result.TargetRevision = resource.ResourceID, description.Host, description.Database, description.SourceIdentity, description.TargetRevision
	result.Drafts = []projectmodule.ConnectionCredentialDraftSignal{}
	return result, actor, resource, true
}

func mountFirstSourceBrowser(r chi.Router, routes *firstSourcePreparationBrowserRoutes, authenticate func(http.Handler) http.Handler, protocol *apiprotocol.Protocol) {
	base := "/connections/{connection}/first-source"
	r.With(authenticate).Get(base, routes.bindScope(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state, _, _, ok := routes.describe(w, r)
		if !ok {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = projectmodule.FirstSourceCredentialsPage(state, csrf.Token(r), nil, credentialmodule.FirstSourceBrowserBindings()).Render(w)
	})).ServeHTTP)
	r.With(authenticate).Get(base+"/state", routes.bindScope(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { routes.browserCommand(w, r, "") })).ServeHTTP)
	for action, binding := range credentialmodule.FirstSourceBrowserBindings() {
		handler, err := protocol.BrowserNonReplayableMutationMiddleware(binding, func(r *http.Request) bool {
			resource, ok := r.Context().Value(firstSourcePreparationRouteScopeKey{}).(credentialmodule.ValidationResource)
			actor, found := routes.config.CurrentPrincipal(r)
			return ok && found && routes.config.FirstSourcePreparation.Authorize(r.Context(), actor, resource) == nil
		}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { routes.browserCommand(w, r, action) }))
		if err != nil {
			continue
		}
		r.With(authenticate).Post(base+"/commands/"+action, routes.bindScope(handler).ServeHTTP)
	}
}

func (routes *firstSourcePreparationBrowserRoutes) browserCommand(w http.ResponseWriter, r *http.Request, action string) {
	state, actor, resource, ok := routes.describe(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var payload projectmodule.FirstSourceCredentialEnvelope
	if err := pagestream.ReadSignals(r, &payload); err != nil {
		http.Error(w, "Invalid credential command.", http.StatusBadRequest)
		return
	}
	input := payload.FirstSourceCredentials.Command
	if action == "" {
		if (input.Action != "list" && input.Action != "status") || input.Password != "" {
			http.Error(w, "Invalid credential query.", http.StatusBadRequest)
			return
		}
	} else if binding, found := credentialmodule.FirstSourceBrowserBindings()[action]; !found || input.Action != action || uicommand.VerifyClaim(uicommand.OperationClaims(r), binding.OperationID()) != nil {
		http.Error(w, "Invalid credential command.", http.StatusBadRequest)
		return
	}
	state.Command = input
	state.Command.Password = ""
	result, err := credentialmodule.RunFirstSourceBrowser(r.Context(), routes.config, actor, resource, credentialmodule.BrowserInput{Action: input.Action, VersionID: input.VersionID, ReceiptID: input.ReceiptID, OperationID: input.OperationID, Password: input.Password, ExpectedRevision: 1, RequestID: r.Header.Get("X-Request-ID"), CorrelationID: r.Header.Get("X-Correlation-ID")}, credentialmodule.FirstSourcePreparationCommandRequest{PreparationID: input.OperationID, VersionID: input.VersionID, ReceiptID: input.ReceiptID, SourceDigest: input.SourceDigest, SourceAttestationDigest: input.SourceAttestationDigest, PlanIdempotencyKey: input.PlanIdempotencyKey, ExpectedTargetRevision: state.TargetRevision})
	input.Password = ""
	if err != nil {
		state.Error = credentialmodule.CredentialBrowserError(err)
	} else {
		if result.VersionID != "" {
			state.Command.VersionID = result.VersionID
		}
		if result.OperationID != "" {
			state.Command.OperationID = result.OperationID
		}
		if result.ReceiptID != "" {
			state.Command.ReceiptID = result.ReceiptID
		}
		state.ReceiptExpiresAt, state.Phase, state.Message = result.ReceiptExpiresAt, result.Phase, result.Message
		for _, draft := range result.Drafts {
			state.Drafts = append(state.Drafts, projectmodule.ConnectionCredentialDraftSignal{VersionID: draft.VersionID, CreatedAt: draft.CreatedAt})
		}
	}
	_ = pagestream.PatchResponse(firstSourceNoStoreWriter{w}, r, pagestream.SignalPatch{"firstSourceCredentials": state})
}

type firstSourceNoStoreWriter struct{ http.ResponseWriter }

func (w firstSourceNoStoreWriter) WriteHeader(status int) {
	w.Header().Set("Cache-Control", "no-store")
	w.ResponseWriter.WriteHeader(status)
}
func (w firstSourceNoStoreWriter) FlushError() error {
	w.Header().Set("Cache-Control", "no-store")
	return http.NewResponseController(w.ResponseWriter).Flush()
}
func (w firstSourceNoStoreWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
