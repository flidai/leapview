package ui

import (
	"net/url"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/dashboard"
	uiactions "github.com/flidai/leapview/internal/platform/web/actions"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectview "github.com/flidai/leapview/internal/project"
	catalog "github.com/flidai/leapview/internal/project/navigation"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

func ProjectAssetPage(catalog catalog.Catalog, project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection, roleLabel string, chromeOptions ...webpage.Provider) g.Node {
	return ProjectAssetPageWithRefresh(catalog, project, asset, assets, edges, activeSection, roleLabel, AssetRefreshState{}, chromeOptions...)
}

func ProjectAssetPageWithRefresh(catalog catalog.Catalog, project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection, roleLabel string, refresh AssetRefreshState, chromeOptions ...webpage.Provider) g.Node {
	return ProjectAssetPageWithRefreshAndVersions(catalog, project, asset, assets, edges, activeSection, roleLabel, refresh, AssetVersionsState{}, chromeOptions...)
}

func ProjectAssetPageWithRefreshAndVersions(catalog catalog.Catalog, project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection, roleLabel string, refresh AssetRefreshState, versions AssetVersionsState, chromeOptions ...webpage.Provider) g.Node {
	return ProjectAssetPageWithRefreshAndVersionsForEnvironment(catalog, project, asset, assets, edges, activeSection, "", roleLabel, refresh, versions, chromeOptions...)
}

func ProjectAssetPageWithRefreshAndVersionsForEnvironment(catalog catalog.Catalog, project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection, environment, roleLabel string, refresh AssetRefreshState, versions AssetVersionsState, chromeOptions ...webpage.Provider) g.Node {
	activeSection = normalizeProjectAssetSection(activeSection)
	lineage := assetLineage(project.ID, asset, assets, edges)
	page := projectAssetPageSignalWithRefreshAndVersions(project, asset, assets, edges, activeSection, lineage, refresh, versions)
	page.Environment = uisignals.Optional(environment)
	area := projectAreaForAssetType(asset.Type)
	extras := projectDocumentExtras{}
	attrs := []g.Node{
		g.Attr("slot", "page"),
	}
	if activeSection == "data" && assetDataInspectable(asset.Type) {
		extras.CSRFToken = refresh.CSRFToken
		commandPath := projectAssetDataHref(asset) + "/command"
		attrs = append(attrs, g.Attr("data-on:lv-data-explorer-command", "$dataExplorerCommand = evt.detail; "+uiactions.EventPost(commandPath)))
	}
	if assetRefreshable(asset.Type) {
		refreshPath := "/pipelines/" + url.PathEscape(asset.ID) + "/refresh"
		extras.CSRFToken = refresh.CSRFToken
		attrs = append(attrs,
			g.Attr("data-on:lv-run-refresh-pipeline", uiactions.CommandPost(refresh.RunCommand, refreshPath)),
		)
		if activeSection == "versions" {
			return projectAssetRouteDocument(asset, catalog, area, roleLabel, page, uisignals.RouteKindData, g.El("lv-project-asset-page", attrs...), extras, activeSection, chromeOptions)
		}
		return projectAssetRouteDocument(asset, catalog, area, roleLabel, page, uisignals.RouteKindData, g.El("lv-project-asset-page", attrs...), extras, activeSection, chromeOptions)
	}
	return projectAssetRouteDocument(asset, catalog, area, roleLabel, page, uisignals.RouteKindData, g.El("lv-project-asset-page", attrs...), extras, activeSection, chromeOptions)
}

func ProjectAssetBootstrapSignals(catalog catalog.Catalog, project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection, roleLabel string, refresh AssetRefreshState, versions AssetVersionsState, chromeOptions ...webpage.Provider) map[string]any {
	return ProjectAssetBootstrapSignalsForEnvironment(catalog, project, asset, assets, edges, activeSection, "", roleLabel, refresh, versions, chromeOptions...)
}

func ProjectAssetBootstrapSignalsForEnvironment(catalog catalog.Catalog, project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection, environment, roleLabel string, refresh AssetRefreshState, versions AssetVersionsState, chromeOptions ...webpage.Provider) map[string]any {
	activeSection = normalizeProjectAssetSection(activeSection)
	lineage := assetLineage(project.ID, asset, assets, edges)
	page := projectAssetPageSignalWithRefreshAndVersions(project, asset, assets, edges, activeSection, lineage, refresh, versions)
	page.Environment = uisignals.Optional(environment)
	return projectRouteBootstrapSignals(catalog, projectAreaForAssetType(asset.Type), roleLabel, page, uisignals.RouteKindData, nil, chromeOptions)
}

func ConnectionAssetBootstrapSignals(catalog catalog.Catalog, project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection, roleLabel string, versions AssetVersionsState) map[string]any {
	return ConnectionAssetBootstrapSignalsForEnvironment(catalog, project, asset, assets, edges, activeSection, "", roleLabel, versions)
}

func ConnectionAssetBootstrapSignalsForEnvironment(catalog catalog.Catalog, project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection, environment, roleLabel string, versions AssetVersionsState, chromeOptions ...webpage.Provider) map[string]any {
	return ConnectionAssetBootstrapSignalsWithAdministrationForEnvironment(catalog, project, asset, assets, edges, activeSection, environment, roleLabel, versions, ConnectionAdministrationView{}, chromeOptions...)
}

func ConnectionAssetBootstrapSignalsWithAdministrationForEnvironment(catalog catalog.Catalog, project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection, environment, roleLabel string, versions AssetVersionsState, administration ConnectionAdministrationView, chromeOptions ...webpage.Provider) map[string]any {
	activeSection = normalizeProjectAssetSection(activeSection)
	lineage := assetLineage(project.ID, asset, assets, edges)
	page := connectionAssetPageSignalWithVersions(project, asset, assets, edges, activeSection, lineage, versions, administration)
	page.Environment = uisignals.Optional(environment)
	patch := projectRouteBootstrapSignals(catalog, "connections", roleLabel, page, uisignals.RouteKindConnectionAsset, nil, chromeOptions)
	patch["connectionAdmin"] = emptyConnectionAdministrationSignal(administration.Status)
	return patch
}

func ConnectionAssetPageWithVersions(catalog catalog.Catalog, project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection, roleLabel string, versions AssetVersionsState) g.Node {
	return ConnectionAssetPageWithVersionsForEnvironment(catalog, project, asset, assets, edges, activeSection, "", roleLabel, versions)
}

func ConnectionAssetPageWithVersionsForEnvironment(catalog catalog.Catalog, project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection, environment, roleLabel string, versions AssetVersionsState, chromeOptions ...webpage.Provider) g.Node {
	return ConnectionAssetPageWithAdministrationForEnvironment(catalog, project, asset, assets, edges, activeSection, environment, roleLabel, versions, ConnectionAdministrationView{}, ConnectionCommandBindings{}, "", chromeOptions)
}

func ConnectionAssetPageWithAdministrationForEnvironment(catalog catalog.Catalog, project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection, environment, roleLabel string, versions AssetVersionsState, administration ConnectionAdministrationView, commands ConnectionCommandBindings, csrfToken string, chromeOptions []webpage.Provider) g.Node {
	activeSection = normalizeProjectAssetSection(activeSection)
	lineage := assetLineage(project.ID, asset, assets, edges)
	page := connectionAssetPageSignalWithVersions(project, asset, assets, edges, activeSection, lineage, versions, administration)
	page.Environment = uisignals.Optional(environment)
	rootAttributes := []g.Node{g.Attr("slot", "page")}
	rootAttributes = append(rootAttributes, connectionAdministrationRouteBridge(commands)...)
	return projectAssetRouteDocument(asset, catalog, "connections", roleLabel, page, uisignals.RouteKindConnectionAsset, g.El("lv-project-asset-page",
		rootAttributes...,
	), projectDocumentExtras{CSRFToken: csrfToken, BootstrapSignals: map[string]any{"connectionAdmin": emptyConnectionAdministrationSignal(administration.Status)}}, activeSection, chromeOptions)
}

func projectAssetRouteDocument(asset projectview.DevelopAssetView, catalog catalog.Catalog, active, roleLabel string, page any, routeKind uisignals.RouteKind, routeRoot g.Node, extras projectDocumentExtras, activeSection string, chromeOptions []webpage.Provider, bodyExtras ...g.Node) g.Node {
	extraHead := []g.Node{}
	if activeSection == "lineage" {
		extraHead = append(extraHead,
			h.Link(h.Rel("stylesheet"), h.Href(projectStaticAssetURL(chromeOptions, "/static/asset-lineage-graph.css"))),
			h.Script(h.Type("module"), h.Src(projectStaticAssetURL(chromeOptions, "/static/asset-lineage-graph.js"))),
		)
	}
	if activeSection == "details" && asset.Type == "semantic_model" {
		extraHead = append(extraHead,
			h.Link(h.Rel("stylesheet"), h.Href(projectStaticAssetURL(chromeOptions, "/static/semantic-model-graph.css"))),
			h.Script(h.Type("module"), h.Src(projectStaticAssetURL(chromeOptions, "/static/semantic-model-graph.js"))),
		)
	}
	if activeSection == "data" && assetDataInspectable(asset.Type) {
		extraHead = append(extraHead,
			h.Script(h.Type("module"), h.Src(projectStaticAssetURL(chromeOptions, "/static/data-explorer.js"))),
		)
	}
	return projectRouteDocumentWithBodyExtras(asset.Title, catalog, active, roleLabel, page, routeKind, routeRoot, extras, bodyExtras, chromeOptions, extraHead...)
}

func ConnectionAssetPage(catalog catalog.Catalog, project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection, roleLabel string) g.Node {
	activeSection = normalizeProjectAssetSection(activeSection)
	lineage := assetLineage(project.ID, asset, assets, edges)
	page := connectionAssetPageSignal(project, asset, assets, edges, activeSection, lineage)
	extraHead := []g.Node{}
	if activeSection == "lineage" {
		extraHead = append(extraHead,
			h.Link(h.Rel("stylesheet"), h.Href(projectStaticAssetURL(nil, "/static/asset-lineage-graph.css"))),
			h.Script(h.Type("module"), h.Src(projectStaticAssetURL(nil, "/static/asset-lineage-graph.js"))),
		)
	}
	return projectRouteDocument(asset.Title, catalog, "connections", roleLabel, page, uisignals.RouteKindConnectionAsset,
		g.El("lv-project-asset-page",
			g.Attr("slot", "page"),
		),
		projectDocumentExtras{},
		nil,
		extraHead...,
	)
}

func projectStaticAssetURL(providers []webpage.Provider, path string) string {
	layout := webpage.Resolve(firstProvider(providers), webpage.Context{})
	return layout.Assets.URL(path)
}

func projectRouteDocument(title string, catalog catalog.Catalog, active, roleLabel string, page any, routeKind uisignals.RouteKind, routeRoot g.Node, extras projectDocumentExtras, chromeOptions []webpage.Provider, extraHead ...g.Node) g.Node {
	return projectRouteDocumentWithBodyExtras(title, catalog, active, roleLabel, page, routeKind, routeRoot, extras, nil, chromeOptions, extraHead...)
}

func projectRouteDocumentWithBodyExtras(title string, catalog catalog.Catalog, active, roleLabel string, page any, routeKind uisignals.RouteKind, routeRoot g.Node, extras projectDocumentExtras, bodyExtras []g.Node, chromeOptions []webpage.Provider, extraHead ...g.Node) g.Node {
	layout := webpage.Resolve(firstProvider(chromeOptions), projectLayoutContext(catalog, active))
	return webpage.Render(layout, webpage.Spec{
		Title: title, CSRFToken: extras.CSRFToken, Scripts: []string{"/static/project-page.js"},
		Head:       extraHead,
		UpdatesURL: projectRouteUpdatesURL(routeKind, catalog, page, extras),
		Content:    routeRoot,
		BodyBefore: bodyExtras,
	})
}

func projectRouteBootstrapSignals(catalog catalog.Catalog, active, roleLabel string, page any, routeKind uisignals.RouteKind, bootstrapSignals map[string]any, chromeOptions []webpage.Provider) map[string]any {
	layout := webpage.Resolve(firstProvider(chromeOptions), projectLayoutContext(catalog, active))
	signals := map[string]any{
		"page":    page,
		"runtime": runtimeForPage(routeKind, catalog, page),
		"status":  dashboard.Status{},
	}
	for key, value := range bootstrapSignals {
		signals[key] = value
	}
	return webpage.WithSignal(layout, signals)
}

func runtimeForPage(routeKind uisignals.RouteKind, catalog catalog.Catalog, page any) uisignals.RouteRuntimeSignal {
	return runtimeSignal(routeKind)
}

func projectRouteUpdatesURL(routeKind uisignals.RouteKind, catalog catalog.Catalog, page any, extras projectDocumentExtras) string {
	switch typed := page.(type) {
	case uisignals.ResourcePageSignal:
		assetList := uisignals.ValueOrZero(typed.AssetList)
		return updatesURL(routeKind, "surface", "project", "area", canonicalProjectArea(extras.Area), "environment", uisignals.ValueOrZero(typed.Environment), "type", firstNonEmpty(uisignals.ValueOrZero(assetList.ActiveType), uisignals.ValueOrZero(typed.ListFilter)), "q", firstNonEmpty(uisignals.ValueOrZero(assetList.Query), uisignals.ValueOrZero(typed.ListQuery)))
	case uisignals.ConnectionsPageSignal:
		return updatesURL(routeKind, "surface", "connections", "environment", uisignals.ValueOrZero(typed.Environment), "q", uisignals.ValueOrZero(typed.Query))
	case uisignals.ResourceAssetPageSignal:
		if routeKind == uisignals.RouteKindConnectionAsset {
			return updatesURL(routeKind, "surface", "asset", "environment", uisignals.ValueOrZero(typed.Environment), "asset", typed.AssetID, "section", typed.ActiveSection)
		}
		pairs := []string{"surface", "asset", "environment", uisignals.ValueOrZero(typed.Environment), "asset", typed.AssetID, "section", typed.ActiveSection}
		return updatesURL(routeKind, pairs...)
	case uisignals.PipelinePageSignal:
		return updatesURL(routeKind, "surface", "pipelines", "view", typed.ActiveTab, "environment", typed.Environment)
	default:
		return updatesURL(routeKind, "surface", "project")
	}
}

func projectAssetBaseHref(area string) string {
	return "/" + canonicalProjectArea(area)
}

func projectAssetSearchHref(area string) string {
	return projectAssetBaseHref(area) + "/search"
}

func canonicalProjectArea(area string) string {
	switch strings.TrimSpace(area) {
	case "sources":
		return "sources"
	case "models":
		return "models"
	case "semantic-models":
		return "semantic-models"
	case "dashboards":
		return "dashboards"
	case "pipelines":
		return "pipelines"
	case "connections":
		return "connections"
	default:
		return "sources"
	}
}

func projectAreaForAssetType(assetType string) string {
	switch assetType {
	case string(projectview.AssetTypeDashboard):
		return "dashboards"
	case string(projectview.AssetTypeModelTable):
		return "models"
	case string(projectview.AssetTypeSemanticModel):
		return "semantic-models"
	case string(projectview.AssetTypeRefreshPipeline):
		return "pipelines"
	case string(projectview.AssetTypeConnection):
		return "connections"
	default:
		return "sources"
	}
}

func projectAreaLabel(area string) string {
	switch canonicalProjectArea(area) {
	case "dashboards":
		return "Dashboards"
	case "models":
		return "Models"
	case "semantic-models":
		return "Semantic models"
	case "pipelines":
		return "Pipelines"
	case "connections":
		return "Connections"
	default:
		return "Sources"
	}
}

func projectAssetTypeForArea(area string) string {
	switch canonicalProjectArea(area) {
	case "dashboards":
		return string(projectview.AssetTypeDashboard)
	case "models":
		return string(projectview.AssetTypeModelTable)
	case "semantic-models":
		return string(projectview.AssetTypeSemanticModel)
	default:
		return string(projectview.AssetTypeSource)
	}
}

func ValidProjectAssetSection(assetType, section string) bool {
	section = strings.TrimSpace(section)
	switch section {
	case "details", "definition", "lineage", "versions":
		return true
	case "data":
		return assetDataInspectable(assetType)
	case "refreshes":
		return assetRefreshable(assetType)
	default:
		return false
	}
}

type AssetRefreshState struct {
	CSRFToken        string
	Unavailable      bool
	RunCommand       uicommand.Binding
	CancelCommand    uicommand.Binding
	Runs             []AssetRefreshRun
	Latest           AssetRefreshRun
	LatestSuccessful AssetRefreshRun
	DataVersion      AssetDataVersion
	NextRun          time.Time
}

type AssetDataVersion struct {
	SnapshotID     int64
	ServingStateID string
	RefreshedAt    time.Time
	Source         string
}

type AssetRefreshRun struct {
	ID                   string
	Environment          string
	ModelID              string
	ServingStateID       string
	PrincipalID          string
	PrincipalDisplayName string
	TriggerType          string
	ParentRunID          string
	RetryOf              string
	TargetGeneration     int64
	Status               string
	CreatedAt            string
	UpdatedAt            string
	StartedAt            string
	FinishedAt           string
	Error                string
}

type AssetVersionsState struct {
	CurrentContentHash string
	Versions           []AssetVersionState
}

type AssetVersionState struct {
	ServingStateID string
	Status         string
	Digest         string
	CreatedBy      string
	CreatedAt      string
	ActivatedAt    string
	SourceFile     string
	ContentHash    string
}

func validProjectAssetSectionName(section string) bool {
	switch strings.TrimSpace(section) {
	case "details", "definition", "data", "lineage", "refreshes", "versions":
		return true
	default:
		return false
	}
}
