package http

import (
	"context"
	"errors"
	stdhttp "net/http"
	"strings"

	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func (h *BrowserHandler) dataExplorerSignalsForCommandWithOptions(w stdhttp.ResponseWriter, r *stdhttp.Request, command projectsignals.DataExplorerCommand, executeQuery, strictURLState, legacyURLState bool) (projectsignals.DataExplorerPageSignal, projectsignals.DataExplorerSignal, bool) {
	command = normalizeDataExplorerCommand(command)
	catalog := h.navigationCatalog(r)
	project := catalog.Project
	if projectID := h.dataExplorerProjectID(r, catalog); projectID != "" {
		project.ID = projectID.String()
		if strings.TrimSpace(project.Title) == "" {
			project.Title = project.ID
		}
	}
	page := projectsignals.DataExplorerPageSignal{Kind: projectsignals.RouteKindData, Title: "Data Explorer", Description: projectsignals.Optional("Explore governed semantic data."), Tabs: []projectsignals.ResourceTabSignal{}, Context: projectsignals.DataExplorerContextSignal{Active: true, Environment: h.Environment, ProjectID: project.ID, ProjectTitle: projectsignals.Optional(project.Title)}}
	exploreCommand := projectsignals.DataExploreCommand{Spec: defaultExplorationSpec()}
	if command.Explore != nil {
		exploreCommand = *command.Explore
	}
	if value := strings.TrimSpace(r.URL.Query().Get("model")); value != "" && strings.TrimSpace(exploreCommand.Spec.ModelID) == "" {
		exploreCommand.Spec.ModelID = value
	}
	// The canonical URL uses semanticModel terminology, while the v1/v2
	// exploration spec retains ModelID for its semantic-model resource. Accept
	// both at this boundary so restored SavedExploration state remains portable
	// across the terminology migration.
	if value := strings.TrimSpace(r.URL.Query().Get("semanticModel")); value != "" && strings.TrimSpace(exploreCommand.Spec.ModelID) == "" {
		exploreCommand.Spec.ModelID = value
	}
	if value := strings.TrimSpace(r.URL.Query().Get("dataset")); value != "" && exploreCommand.Spec.DatasetID == nil {
		exploreCommand.Spec.DatasetID = projectsignals.Optional(value)
	}
	command.Explore = &exploreCommand
	explorer := projectsignals.DataExplorerSignal{Command: command, Explore: projectsignals.DataExploreSignal{Command: exploreCommand, Views: map[string]visualizationir.VisualizationEnvelope{}, RecommendedView: "table", DefaultView: "table", SemanticModels: []projectsignals.DataExploreSemanticModelSignal{}, Datasets: []projectsignals.DataExploreDatasetSignal{}, Fields: []projectsignals.DataExploreFieldSignal{}, Result: projectsignals.DataExploreResultSignal{Columns: []projectsignals.DataPreviewColumnSignal{}, Rows: []map[string]any{}, Warnings: []string{}}, Status: projectsignals.DataExploreStatusSignal{RequestSeq: exploreCommand.RequestSeq, State: "idle"}}, Objects: []projectsignals.DataExplorerObjectSignal{}, Preview: projectsignals.DataPreviewSignal{Blocks: emptyDataExplorerBlocks(command), Columns: []projectsignals.DataPreviewColumnSignal{}, ChunkSize: command.Count, RowHeight: dataExplorerRowHeight, Stale: false}}
	_, assets, _, ok := h.assets(w, r)
	if !ok {
		return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, false
	}
	clientKey := h.dataExplorerClientKey(r, projectgraph.ResourceID(project.ID), command)
	action := dataExplorerAction(command)
	requestSeq := dataExplorerRequestSeq(command)
	stopAccepted := false
	if action == "stop" && clientKey != "" {
		stopAccepted = h.dataExplorerLifecycle.stop(clientKey, projectsignals.ValueOrZero(command.RunID), requestSeq)
		if !stopAccepted {
			explorer.Explore.Status = dataExploreStatus(exploreCommand, explorer.Explore.Result, "stop", false)
			explorer.Explore.Status.Message = projectsignals.Pointer("no exploration is running")
		}
	}
	if h == nil || h.ProjectDefinitionReader == nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, false
	}
	// Value suggestions are an independent lane. A typing request must not
	// cancel a semantic Run that is still producing the current result.
	suggestionOnly := exploreCommand.FilterSuggestions != nil && action == "configure"
	accepted := true
	acceptedSuggestions := true
	if !suggestionOnly && clientKey != "" {
		accepted = h.dataExplorerLifecycle.acceptSemantic(clientKey, requestSeq)
	}
	if exploreCommand.FilterSuggestions != nil {
		if clientKey != "" {
			var suggestionSeq int64
			acceptedSuggestions, suggestionSeq = h.dataExplorerLifecycle.acceptSuggestions(clientKey, dataExplorerSuggestionRequestSeq(command))
			if acceptedSuggestions {
				exploreCommand.FilterSuggestions.SuggestionRequestSeq = suggestionSeq
			}
			// A rejected lane token must be carried as zero in the returned
			// command. The final response lease treats that as invalid, so a
			// duplicate/unsafe suggestion cannot emit a full signal replacement.
			if !acceptedSuggestions {
				exploreCommand.FilterSuggestions.SuggestionRequestSeq = 0
			}
			command.Explore = &exploreCommand
		}
	}
	if !accepted && action != "stop" {
		// Still project the authorized catalog and current fields, but do not
		// execute a response that would regress a newer browser command.
		action = "configure"
	}
	definition, compiledModels, err := h.ProjectDefinitionReader.ProjectDefinitionSnapshot(r.Context())
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, false
	}
	consumers := dataExplorerSemanticConsumers(r.Context(), h.QueryExecutor, definition)
	projection := BuildDataExplorerProjection(assets, definition, exploreCommand, compiledModels, consumers)
	if strictURLState && legacyURLState && projectsignals.ValueOrZero(command.Mode) == "explore" {
		if err := adaptLegacyExplorationFilterValues(&exploreCommand.Spec, projection.Fields); err != nil {
			stdhttp.Error(w, "invalid legacy exploration URL state: "+err.Error(), stdhttp.StatusBadRequest)
			return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, false
		}
		// Filter literal kinds do not affect field projection, but the adapted
		// spec must be carried through the command that is subsequently restored
		// and executed.
		projection = BuildDataExplorerProjection(assets, definition, exploreCommand, compiledModels, consumers)
	}
	if strictURLState && projectsignals.ValueOrZero(command.Mode) == "explore" {
		modelID := strings.TrimSpace(projection.Command.Spec.ModelID)
		if err := validateRestoredDataExploreState(exploreCommand, projection, definition.SemanticModels[modelID], compiledModels); err != nil {
			stdhttp.Error(w, "invalid exploration URL state: "+err.Error(), stdhttp.StatusBadRequest)
			return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, false
		}
	}
	explorer.Objects = projection.Objects
	explorer.Explore.SemanticModels = projection.SemanticModels
	explorer.Explore.SelectedSemanticModel = projection.SelectedSemanticModel
	explorer.Explore.Datasets = projection.Datasets
	explorer.Explore.SelectedDataset = projection.SelectedDataset
	explorer.Explore.Fields = projection.Fields
	exploreCommand = projection.Command
	explorer.Explore.Command = exploreCommand
	explorer.Command.Explore = &exploreCommand
	// Configure and Stop return the hydrated catalog/field projection with an
	// explicit lifecycle status. Results are never replayed from process-local
	// memory because that cache cannot carry the effective RLS/masking policy.
	explorer.Explore.Status = dataExploreStatus(exploreCommand, explorer.Explore.Result, action, false)
	if action == "stop" && clientKey != "" && !stopAccepted {
		explorer.Explore.Status.Message = projectsignals.Pointer("no exploration is running")
	}
	shouldExecuteSemantic := executeQuery && projectsignals.ValueOrZero(explorer.Command.Mode) == "explore" && action != "configure" && action != "stop"
	shouldExecuteSuggestions := executeQuery && acceptedSuggestions && exploreCommand.FilterSuggestions != nil && action != "stop"
	if shouldExecuteSemantic {
		explorer.Explore.Status = dataExploreLoadingStatus(exploreCommand)
	}
	if shouldExecuteSemantic {
		projectID, err := h.boundProject(r.Context())
		if err != nil {
			stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
			return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, false
		}
		semanticModel := definition.SemanticModels[exploreCommand.Spec.ModelID]
		compiledModel := compiledModels[exploreCommand.Spec.ModelID]
		var executionContext context.Context
		var finish func()
		var effectiveRunID string
		if clientKey != "" {
			executionContext, finish, effectiveRunID = h.dataExplorerLifecycle.beginRun(clientKey, dataExplorerRunID(command), requestSeq, r.Context())
			defer finish()
		} else {
			executionContext = r.Context()
		}
		if strings.TrimSpace(projectsignals.ValueOrZero(command.RunID)) == "" {
			command.RunID = projectsignals.Optional(effectiveRunID)
			explorer.Command = command
		}
		exploreCommand, explorer.Explore.Result = dataExplorerSemanticResult(executionContext, h.QueryExecutor, projectID, exploreCommand, explorer.Explore.Fields, semanticModel, compiledModel)
		explorer.Explore.Result.Warnings = append(explorer.Explore.Result.Warnings, projection.Warnings...)
		// The browser command does not carry a server-owned serving snapshot.
		// Keep freshness explicit but unknown rather than treating a
		// client-provided header as provenance for governed data.
		explorer.Explore.Result.Freshness = &projectsignals.DataExploreFreshnessSignal{Source: projectsignals.Pointer("unknown"), Status: "unknown"}
		viewProjection := ProjectDataExplorerViews(exploreCommand.Spec, explorer.Explore.Result, explorer.Explore.Fields)
		explorer.Explore.Views = viewProjection.Views
		explorer.Explore.RecommendedView = viewProjection.RecommendedView
		explorer.Explore.DefaultView = viewProjection.DefaultView
		explorer.Explore.Result.Warnings = append(explorer.Explore.Result.Warnings, viewProjection.Warnings...)
		if errors.Is(executionContext.Err(), context.Canceled) {
			explorer.Explore.Status = projectsignals.DataExploreStatusSignal{RequestSeq: exploreCommand.RequestSeq, State: "cancelled", Message: projectsignals.Pointer("exploration stopped")}
		} else if clientKey != "" && !h.dataExplorerLifecycle.currentRun(clientKey, requestSeq, effectiveRunID) {
			explorer.Explore.Status = projectsignals.DataExploreStatusSignal{RequestSeq: exploreCommand.RequestSeq, State: "stale", Stale: true, Message: projectsignals.Pointer("a newer exploration request is active")}
		} else {
			explorer.Explore.Status = dataExploreStatus(exploreCommand, explorer.Explore.Result, action, true)
		}
		explorer.Explore.Command = exploreCommand
		explorer.Command.Explore = &exploreCommand
	}
	if shouldExecuteSuggestions {
		projectID, err := h.boundProject(r.Context())
		if err != nil {
			stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
			return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, false
		}
		var executionContext context.Context
		var finish func()
		var effectiveRunID string
		if clientKey != "" {
			executionContext, finish, effectiveRunID = h.dataExplorerLifecycle.beginSuggestions(clientKey, dataExplorerRunID(command), dataExplorerSuggestionRequestSeq(command), r.Context())
		} else {
			executionContext = r.Context()
		}
		suggestions := dataExplorerFilterSuggestions(executionContext, h.QueryExecutor, projectID, exploreCommand, explorer.Explore.Fields, compiledModels[exploreCommand.Spec.ModelID])
		if clientKey != "" && !h.dataExplorerLifecycle.currentSuggestions(clientKey, dataExplorerSuggestionRequestSeq(command), effectiveRunID) {
			suggestions.Stale = true
		}
		if finish != nil {
			finish()
		}
		explorer.Explore.FilterSuggestions = &suggestions
	}
	page.Context.ObjectCount = int64(len(explorer.Objects))

	requestedObject := strings.TrimSpace(projectsignals.ValueOrZero(command.ObjectKey))
	if projectsignals.ValueOrZero(explorer.Command.Mode) == "explore" {
		requestedObject = ""
		modelID := strings.TrimSpace(exploreCommand.Spec.ModelID)
		datasetID := strings.TrimSpace(projectsignals.ValueOrZero(exploreCommand.Spec.DatasetID))
		for _, object := range explorer.Objects {
			if object.Layer == "model" && projectsignals.ValueOrZero(object.SemanticModelID) == modelID && projectsignals.ValueOrZero(object.DatasetID) == datasetID {
				requestedObject = object.Key
				break
			}
		}
	}
	if requestedObject != "" {
		for index := range explorer.Objects {
			object := explorer.Objects[index]
			if object.Key != requestedObject && object.ResourceID != requestedObject && projectsignals.ValueOrZero(object.AssetID) != requestedObject {
				continue
			}
			explorer.Command.ObjectKey = projectsignals.Optional(object.Key)
			explorer.SelectedKey = projectsignals.Optional(object.Key)
			explorer.SelectedObject = &object
			page.SelectedObject = projectsignals.Optional(object.Key)
			if executeQuery && projectsignals.ValueOrZero(explorer.Command.Mode) != "explore" && action != "configure" && accepted {
				projectID, err := h.boundProject(r.Context())
				if err != nil {
					stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
					return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, false
				}
				var executionContext context.Context
				var finish func()
				var effectiveRunID string
				if clientKey != "" {
					executionContext, finish, effectiveRunID = h.dataExplorerLifecycle.beginRun(clientKey, dataExplorerRunID(command), requestSeq, r.Context())
				} else {
					executionContext = r.Context()
				}
				if strings.TrimSpace(projectsignals.ValueOrZero(command.RunID)) == "" && effectiveRunID != "" {
					command.RunID = projectsignals.Optional(effectiveRunID)
					explorer.Command = command
				}
				explorer.Preview = dataExplorerPreview(executionContext, h.QueryExecutor, projectID, object, explorer.Command)
				if finish != nil {
					finish()
				}
				if clientKey != "" && !h.dataExplorerLifecycle.currentRun(clientKey, requestSeq, effectiveRunID) {
					explorer.Preview.Stale = true
				}
			} else if action == "configure" || !accepted {
				explorer.Preview.Stale = true
			}
			break
		}
	}
	return page, explorer, true
}
