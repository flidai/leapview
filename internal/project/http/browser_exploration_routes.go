package http

import (
	stdhttp "net/http"

	"github.com/flidai/leapview/internal/platform/web/transport"
	projectview "github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectnavigation "github.com/flidai/leapview/internal/project/navigation"
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
	writeDocument(w, projectui.DataExplorerPageWithSavedExplorationsAndDashboard(catalog, page, explorer, savedState, h.dashboardBootstrap(r, explorer.Explore.Command.Spec.ModelID), h.csrf(r), h.layout(r)))
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
	if !h.hasDataExplorerClientIdentity(r, signals.Command) {
		stdhttp.Error(w, "data explorer client identity is required", stdhttp.StatusBadRequest)
		return
	}
	page, explorer, ok := h.dataExplorerSignalsForCommand(w, r, signals.Command)
	if !ok {
		return
	}
	unlock, current := h.dataExplorerResponseLease(r, explorer.Command)
	if !current {
		return
	}
	defer unlock()
	if dataExplorerSuggestionCommand(explorer.Command) {
		_ = pagestream.PatchResponse(w, r, dataExplorerSuggestionPatch(explorer.Explore.FilterSuggestions))
		return
	}
	// Keep the agent's explore link coupled to the same canonical, authorized
	// projection as the command response.  This must be emitted only after the
	// response lease check so a late query cannot replace a newer context.
	context := projectui.DataExplorerAgentContext(page, explorer)
	patch := dataExplorerSignalPatch(explorer)
	patch["page"] = page
	patch["agentContext"] = context
	_ = pagestream.PatchResponse(w, r, patch)
}

func (h *BrowserHandler) ModelDataExplorerCommand(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.assetDataExplorerCommand(w, r, string(projectview.AssetTypeModel))
}

func (h *BrowserHandler) SemanticModelDataExplorerCommand(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.assetDataExplorerCommand(w, r, string(projectview.AssetTypeSemanticModel))
}

func (h *BrowserHandler) dataExplorerSignalsForCommand(w stdhttp.ResponseWriter, r *stdhttp.Request, command projectsignals.DataExplorerCommand) (projectsignals.DataExplorerPageSignal, projectsignals.DataExplorerSignal, bool) {
	return h.dataExplorerSignalsForCommandWithOptions(w, r, command, true, false, false)
}

func (h *BrowserHandler) dataExplorerSignalsForRestoredCommand(w stdhttp.ResponseWriter, r *stdhttp.Request, command projectsignals.DataExplorerCommand, executeQuery, legacyURLState bool) (projectsignals.DataExplorerPageSignal, projectsignals.DataExplorerSignal, bool) {
	return h.dataExplorerSignalsForCommandWithOptions(w, r, command, executeQuery, true, legacyURLState)
}

func (h *BrowserHandler) dataExplorerSignalsForAssetCommand(w stdhttp.ResponseWriter, r *stdhttp.Request, assetID string, command projectsignals.DataExplorerCommand) (projectsignals.DataExplorerPageSignal, projectsignals.DataExplorerSignal, projectview.DevelopAssetView, bool) {
	_, assets, _, ok := h.assets(w, r)
	if !ok {
		return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, projectview.DevelopAssetView{}, false
	}
	asset, found := projectview.AssetByID(assets, assetID)
	if !found || (asset.Type != string(projectview.AssetTypeModel) && asset.Type != string(projectview.AssetTypeSemanticModel)) {
		stdhttp.NotFound(w, r)
		return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, projectview.DevelopAssetView{}, false
	}

	if asset.Type == string(projectview.AssetTypeModel) {
		command.Mode = projectsignals.Pointer("browse")
		command.ObjectKey = projectsignals.Pointer(asset.ID)
	} else {
		command.Mode = projectsignals.Pointer("explore")
		explore := projectsignals.DataExploreCommand{Spec: defaultExplorationSpec()}
		if command.Explore != nil {
			explore = *command.Explore
		}
		explore.Spec.ModelID = asset.ID
		command.Explore = &explore
	}

	page, explorer, ok := h.dataExplorerSignalsForCommand(w, r, command)
	if !ok {
		return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, projectview.DevelopAssetView{}, false
	}
	objects := make([]projectsignals.DataExplorerObjectSignal, 0, len(explorer.Objects))
	for _, object := range explorer.Objects {
		include := asset.Type == string(projectview.AssetTypeModel) && explorer.SelectedObject != nil && object.Key == explorer.SelectedObject.Key
		include = include || asset.Type == string(projectview.AssetTypeSemanticModel) && projectsignals.ValueOrZero(object.SemanticModelID) == asset.ID
		if include {
			objects = append(objects, object)
		}
	}
	explorer.Objects = objects
	page.Context.ObjectCount = int64(len(objects))
	if explorer.SelectedObject != nil {
		selected := false
		for _, object := range objects {
			if object.Key == explorer.SelectedObject.Key {
				selected = true
				break
			}
		}
		if !selected {
			explorer.SelectedKey = nil
			explorer.SelectedObject = nil
			page.SelectedObject = nil
		}
	}
	if asset.Type == string(projectview.AssetTypeSemanticModel) {
		semanticModels := make([]projectsignals.DataExploreSemanticModelSignal, 0, 1)
		for _, model := range explorer.Explore.SemanticModels {
			if model.ID == asset.ID {
				semanticModels = append(semanticModels, model)
			}
		}
		explorer.Explore.SemanticModels = semanticModels
	}
	return page, explorer, asset, true
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
	if !h.hasDataExplorerClientIdentity(r, signals.Command) {
		stdhttp.Error(w, "data explorer client identity is required", stdhttp.StatusBadRequest)
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
	unlock, current := h.dataExplorerResponseLease(r, explorer.Command)
	if !current {
		return
	}
	defer unlock()
	if dataExplorerSuggestionCommand(explorer.Command) {
		_ = pagestream.PatchResponse(w, r, dataExplorerSuggestionPatch(explorer.Explore.FilterSuggestions))
		return
	}
	_ = pagestream.PatchResponse(w, r, dataExplorerSignalPatch(explorer))
}

func dataExplorerSuggestionCommand(command projectsignals.DataExplorerCommand) bool {
	return dataExplorerAction(command) == "configure" && command.Explore != nil && command.Explore.FilterSuggestions != nil
}

// Suggestions are a side lane. Patch only that signal so a late response
// cannot replace the current semantic command, result, status, or agent
// context. Null optional values intentionally clear a prior error/type in
// Datastar's recursive signal merge.
func dataExplorerSuggestionPatch(suggestion *projectsignals.DataExploreFilterSuggestionsSignal) pagestream.SignalPatch {
	if suggestion == nil {
		return nil
	}
	values := suggestion.Values
	if values == nil {
		values = []projectsignals.DataExploreFilterValueSuggestionSignal{}
	}
	var errorValue any
	if suggestion.Error != nil {
		errorValue = *suggestion.Error
	}
	var typeValue any
	if suggestion.Type != nil {
		typeValue = *suggestion.Type
	}
	return pagestream.SignalPatch{
		"dataExplorer": map[string]any{
			"explore": map[string]any{
				"filterSuggestions": map[string]any{
					"error": errorValue, "field": suggestion.Field, "loading": suggestion.Loading,
					"requestSeq": suggestion.RequestSeq, "suggestionRequestSeq": suggestion.SuggestionRequestSeq,
					"stale": suggestion.Stale, "truncated": suggestion.Truncated, "type": typeValue, "values": values,
				},
			},
		},
	}
}

// Updates is the shared browser bootstrap router. The explore branch carries
// saved-exploration state so navigation and signal updates use one projection.
func (h *BrowserHandler) Updates(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindProjectNamespace, projectgraph.KindSource, projectgraph.KindModel, projectgraph.KindSemanticModel, projectgraph.KindPipeline, projectgraph.KindConnection, projectgraph.KindDashboard}) {
		return
	}
	patch := map[string]any{"status": projectsignals.DashboardStatus{}, "runtime": projectsignals.RouteRuntimeSignal{Kind: projectsignals.RouteKindData}}
	switch transport.Route(r) {
	case "catalog":
		catalog, options, err := h.dashboardCatalogPage(r, r.URL.Query().Get("q"))
		if err != nil {
			stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
			return
		}
		patch = projectui.CatalogBootstrapSignalsForCatalogsWithOptions([]projectnavigation.Catalog{catalog}, options, h.layout(r))
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
			catalog := h.navigationCatalog(r)
			patch = projectui.DataExplorerBootstrapSignalsWithSavedExplorationsAndDashboard(catalog, page, explorer, savedState, h.dashboardBootstrap(r, explorer.Explore.Command.Spec.ModelID), h.layout(r))
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
