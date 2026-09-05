package http

import (
	stdhttp "net/http"

	"github.com/flidai/leapview/internal/platform/web/transport"
	projectview "github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectui "github.com/flidai/leapview/internal/project/ui"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	"github.com/flidai/leapview/pkg/pagestream"
	"github.com/go-chi/chi/v5"
)

// Explore renders the Data Explorer shell with the saved-exploration state
// that the authenticated browser can reopen and mutate through signals.
func (h *BrowserHandler) Explore(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindSemanticModel}) {
		return
	}
	catalog := h.navigationCatalog(r)
	// The document request only renders the shell. The canonical updates stream
	// owns the first analytical execution so a deep link cannot execute the same
	// exploration once during HTML rendering and again during signal bootstrap.
	page, explorer, ok := h.dataExplorerSignalsForURL(w, r, false)
	if !ok {
		return
	}
	savedState := h.savedExplorationStateForBrowser(r, r.URL.Query().Get("saved"), savedExplorationIncludeArchived(r))
	savedState.Commands = projectui.DataExplorerSavedExplorationCommandBindings{
		Create: h.SavedExplorationCommands.Create, Update: h.SavedExplorationCommands.Update,
		Duplicate: h.SavedExplorationCommands.Duplicate, Archive: h.SavedExplorationCommands.Archive,
	}
	writeDocument(w, projectui.DataExplorerPageWithSavedExplorations(catalog, page, explorer, savedState, h.csrf(r), h.layout(r)))
}

func (h *BrowserHandler) DataExplorerCommand(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindSemanticModel}) {
		return
	}
	var signals struct {
		Command projectsignals.DataExplorerCommand `json:"dataExplorerCommand"`
	}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		stdhttp.Error(w, "data explorer command payload is required", stdhttp.StatusBadRequest)
		return
	}
	page, explorer, ok := h.dataExplorerSignalsForCommand(w, r, signals.Command)
	if !ok {
		return
	}
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{
		"page": page, "dataExplorer": explorer, "dataExplorerCommand": explorer.Command,
	})
}

func (h *BrowserHandler) ModelDataExplorerCommand(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.assetDataExplorerCommand(w, r, string(projectview.AssetTypeModel))
}

func (h *BrowserHandler) SemanticModelDataExplorerCommand(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.assetDataExplorerCommand(w, r, string(projectview.AssetTypeSemanticModel))
}

func (h *BrowserHandler) assetDataExplorerCommand(w stdhttp.ResponseWriter, r *stdhttp.Request, expectedType string) {
	kind, ok := catalogKindForAssetType(expectedType)
	if !ok || !h.authorizeAny(w, r, []projectgraph.Kind{kind}) {
		return
	}
	var signals struct {
		Command projectsignals.DataExplorerCommand `json:"dataExplorerCommand"`
	}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		stdhttp.Error(w, "data explorer command payload is required", stdhttp.StatusBadRequest)
		return
	}
	_, explorer, asset, ok := h.dataExplorerSignalsForAssetCommand(w, r, chi.URLParam(r, "asset"), signals.Command)
	if !ok {
		return
	}
	if asset.Type != expectedType {
		stdhttp.NotFound(w, r)
		return
	}
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{
		"dataExplorer": explorer, "dataExplorerCommand": explorer.Command,
	})
}

// Updates is the shared browser bootstrap router. The explore branch carries
// saved-exploration state so navigation and signal updates use one projection.
func (h *BrowserHandler) Updates(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindProject, projectgraph.KindSource, projectgraph.KindModel, projectgraph.KindSemanticModel, projectgraph.KindPipeline, projectgraph.KindConnection, projectgraph.KindDashboard}) {
		return
	}
	patch := map[string]any{"status": projectsignals.DashboardStatus{}, "runtime": projectsignals.RouteRuntimeSignal{Kind: projectsignals.RouteKindData}}
	switch transport.Route(r) {
	case "catalog":
		patch = projectui.CatalogBootstrapSignals(h.navigationCatalog(r), h.layout(r))
	case "data":
		surface := r.URL.Query().Get("surface")
		if surface == "explore" {
			page, explorer, ok := h.dataExplorerSignalsForURL(w, r, true)
			if !ok {
				return
			}
			savedState := h.savedExplorationStateForBrowser(r, r.URL.Query().Get("saved"), savedExplorationIncludeArchived(r))
			savedState.Commands = projectui.DataExplorerSavedExplorationCommandBindings{
				Create: h.SavedExplorationCommands.Create, Update: h.SavedExplorationCommands.Update,
				Duplicate: h.SavedExplorationCommands.Duplicate, Archive: h.SavedExplorationCommands.Archive,
			}
			patch = projectui.DataExplorerBootstrapSignalsWithSavedExplorations(h.navigationCatalog(r), page, explorer, savedState, h.layout(r))
		} else if surface == "asset" {
			if assetPatch, ok := h.assetBootstrap(w, r); ok {
				patch = assetPatch
			} else {
				return
			}
		} else if projectPatch, ok := h.projectBootstrap(w, r); ok {
			patch = projectPatch
		} else {
			return
		}
	case "connections":
		if connectionPatch, ok := h.connectionsBootstrap(w, r); ok {
			patch = connectionPatch
		} else {
			return
		}
	case "pipelines":
		if pipelinePatch, ok := h.pipelinesBootstrap(w, r); ok {
			patch = pipelinePatch
		} else {
			return
		}
	case "asset", "connection_asset":
		if assetPatch, ok := h.assetBootstrap(w, r); ok {
			patch = assetPatch
		} else {
			return
		}
	}
	transport.PatchAndWait(w, r, pagestream.SignalPatch(patch))
}
