package ui

import (
	"strings"

	"github.com/flidai/leapview/internal/dashboard"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	projectview "github.com/flidai/leapview/internal/project"
	catalog "github.com/flidai/leapview/internal/project/navigation"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

const pipelineRunDetailRouteKind uisignals.RouteKind = "pipeline_run_detail"

// PipelineRunDetailDocument renders the MPA shell for one durable pipeline
// run. The current tab and all run evidence arrive through the route's typed
// /updates bootstrap.
func PipelineRunDetailDocument(nav catalog.Catalog, page uisignals.PipelineRunDetailPageSignal, csrfToken string, providers ...webpage.Provider) g.Node {
	layout := webpage.Resolve(firstProvider(providers), webpage.Context{
		Active: "pipelines", ScopeID: nav.Project.ID, ScopeTitle: nav.Project.Title,
		SectionTitle: "Pipelines", PageTitle: "Run details",
	})
	return webpage.Render(layout, webpage.Spec{
		Title: page.Title, CSRFToken: csrfToken,
		Scripts: []string{"/static/project-page.js"},
		Head: []g.Node{
			h.Link(h.Rel("stylesheet"), h.Href(projectStaticAssetURL(providers, "/static/asset-lineage-graph.css"))),
			h.Script(h.Type("module"), h.Src(projectStaticAssetURL(providers, "/static/asset-lineage-graph.js"))),
		},
		UpdatesURL: updatesURL(pipelineRunDetailRouteKind,
			"surface", "pipeline_run_detail", "environment", page.Environment,
			"asset", page.PipelineID, "run", page.RunID, "section", page.ActiveTab),
		Content: g.El("lv-pipeline-run-page", g.Attr("slot", "page")),
	})
}

// PipelineRunDetailBootstrapSignals returns the typed bootstrap envelope used
// by the document and stream route.
func PipelineRunDetailBootstrapSignals(nav catalog.Catalog, page uisignals.PipelineRunDetailPageSignal, providers ...webpage.Provider) map[string]any {
	layout := webpage.Resolve(firstProvider(providers), webpage.Context{
		Active: "pipelines", ScopeID: nav.Project.ID, ScopeTitle: nav.Project.Title,
		SectionTitle: "Pipelines", PageTitle: "Run details",
	})
	return webpage.WithSignal(layout, map[string]any{
		"page":    page,
		"runtime": uisignals.RouteRuntimeSignal{Kind: pipelineRunDetailRouteKind},
		"status":  dashboard.Status{},
	})
}

// PipelineRunDetailPageSignal shapes a run projection into the public route
// contract. Model rows intentionally carry no outcome unless a durable child
// run supplied one.
func PipelineRunDetailPageSignal(state PipelineRunDetailPageState) uisignals.PipelineRunDetailPageSignal {
	activeTab := strings.ToLower(strings.TrimSpace(state.ActiveTab))
	switch activeTab {
	case "events", "details":
	default:
		activeTab = "execution"
	}
	page := uisignals.PipelineRunDetailPageSignal{
		Kind: pipelineRunDetailRouteKind, Title: state.Title, Description: state.Description,
		ActiveTab: activeTab, Environment: state.Environment, PipelineID: state.PipelineID,
		PipelineTitle: state.PipelineTitle, PipelineHref: state.PipelineHref, RunID: state.RunID,
		Status: state.Status, StatusLabel: state.StatusLabel, Execution: state.Execution,
		Events: append([]uisignals.PipelineRunEventSignal{}, state.Events...), Details: state.Details,
		EventsTruncated: state.EventsTruncated,
	}
	if state.EventsUnavailable {
		page.EventsUnavailable = uisignals.Optional(true)
	}
	if strings.TrimSpace(state.RunError) != "" {
		page.RunError = uisignals.Optional(state.RunError)
	}
	return page
}

// PipelineRunHistoricalGraph builds the dependency view from one immutable
// serving-state graph. Its caller supplies the run's recorded generation; this
// helper never resolves the active graph.
func PipelineRunHistoricalGraph(projectID, pipelineID, generationID string, graph servingstate.AssetGraph) (uisignals.AssetLineageGraphSignal, bool, error) {
	assets := make([]projectview.Asset, 0, len(graph.Assets))
	assetIDs := make(map[string]struct{}, len(graph.Assets))
	for _, asset := range graph.Assets {
		if asset.ProjectID.String() != projectID || string(asset.ServingStateID) != generationID {
			continue
		}
		assets = append(assets, projectview.Asset{
			ID: projectview.AssetID(asset.ID.String()), SnapshotID: projectview.AssetSnapshotID(asset.SnapshotID),
			ProjectID: asset.ProjectID, ServingStateID: projectview.ServingStateID(asset.ServingStateID),
			Type: projectview.AssetType(asset.Type), Key: asset.Key, ParentID: projectview.AssetID(asset.ParentID.String()),
			Title: asset.Title, Description: asset.Description, SourceFile: asset.SourceFile,
			PayloadSchema: asset.PayloadSchema, PayloadJSON: asset.PayloadJSON, ContentHash: asset.ContentHash,
		})
		assetIDs[asset.ID.String()] = struct{}{}
	}
	edges := make([]projectview.AssetEdge, 0, len(graph.Edges))
	for _, edge := range graph.Edges {
		if edge.ProjectID.String() != projectID || string(edge.ServingStateID) != generationID {
			continue
		}
		if _, ok := assetIDs[edge.FromAssetID.String()]; !ok {
			continue
		}
		if _, ok := assetIDs[edge.ToAssetID.String()]; !ok {
			continue
		}
		edges = append(edges, projectview.AssetEdge{
			ID: projectview.AssetEdgeID(edge.ID), ProjectID: edge.ProjectID,
			ServingStateID: projectview.ServingStateID(edge.ServingStateID),
			FromAssetID:    projectview.AssetID(edge.FromAssetID.String()),
			ToAssetID:      projectview.AssetID(edge.ToAssetID.String()), Type: projectview.AssetEdgeType(edge.Type),
		})
	}
	catalog, err := projectview.DecodeDevelopCatalog(projectview.DevelopAssetGraph{Assets: assets, Edges: edges})
	if err != nil {
		return uisignals.AssetLineageGraphSignal{}, false, err
	}
	views := make([]projectview.DevelopAssetView, 0, len(catalog.Assets))
	viewEdges := make([]projectview.DevelopEdgeView, 0, len(catalog.Edges))
	var pipeline projectview.DevelopAssetView
	found := false
	for _, record := range catalog.Assets {
		view := projectview.DevelopAssetViewFromCatalogRecord(record)
		views = append(views, view)
		if view.ID == pipelineID && (view.Type == "refresh_pipeline" || view.Type == "pipeline") {
			pipeline = view
			found = true
		}
	}
	if !found {
		return uisignals.AssetLineageGraphSignal{}, false, nil
	}
	for _, edge := range catalog.Edges {
		viewEdges = append(viewEdges, projectview.DevelopEdgeViewFromCatalogRecord(edge))
	}
	return pipelineDetailDependencyGraph(projectID, pipeline, views, viewEdges), true, nil
}

// PipelineRunDetailPageState contains presentation-ready, durable run facts.
// It has no phase model; child statuses are copied only from stored runs.
type PipelineRunDetailPageState struct {
	Title, Description, ActiveTab, Environment string
	PipelineID, PipelineTitle, PipelineHref    string
	RunID, Status, StatusLabel, RunError       string
	EventsTruncated                            bool
	EventsUnavailable                          bool
	Execution                                  uisignals.PipelineRunExecutionSignal
	Events                                     []uisignals.PipelineRunEventSignal
	Details                                    uisignals.PipelineRunDetailsSignal
}
