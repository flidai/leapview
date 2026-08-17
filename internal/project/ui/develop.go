package ui

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/dashboard"
	uiactions "github.com/flidai/leapview/internal/platform/web/actions"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectview "github.com/flidai/leapview/internal/project"
	"github.com/flidai/leapview/internal/project/assetnav"
	catalog "github.com/flidai/leapview/internal/project/navigation"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

func ProjectPage(catalog catalog.Catalog, project projectview.DevelopView, assets []projectview.DevelopAssetView, activeType, query, roleLabel, csrfToken string, chromeOptions ...webpage.Provider) g.Node {
	return ProjectPageForEnvironment(catalog, project, assets, activeType, query, "", roleLabel, csrfToken, chromeOptions...)
}

func ProjectPageForEnvironment(catalog catalog.Catalog, project projectview.DevelopView, assets []projectview.DevelopAssetView, activeType, query, environment, roleLabel, csrfToken string, chromeOptions ...webpage.Provider) g.Node {
	page := projectPageSignal(project, assets, nil, "sources", activeType, query, environment)
	attrs := []g.Node{
		g.Attr("slot", "page"),
	}
	// Access/group/role-binding administration is owned by the access/admin
	// surfaces. The Develop catalog only renders the active project's assets.
	extras := projectDocumentExtras{CSRFToken: csrfToken, Area: "sources"}
	attrs = append(attrs, projectAssetFilterRouteBridge("sources")...)
	return projectRouteDocument(project.Title, catalog, "sources", roleLabel, page, uisignals.RouteKindData,
		g.El("lv-project-page", attrs...),
		extras,
		chromeOptions,
	)
}

// ProjectAreaPage renders the shared project asset catalog under one of the
// canonical Develop resource areas. The project identity remains server-bound
// in the page signal; area only controls navigation and filtering.
func ProjectAreaPage(catalog catalog.Catalog, project projectview.DevelopView, assets []projectview.DevelopAssetView, area, activeType, query, environment, roleLabel, csrfToken string, chromeOptions ...webpage.Provider) g.Node {
	area = canonicalProjectArea(area)
	page := projectPageSignal(project, assets, nil, area, activeType, query, environment)
	attrs := []g.Node{g.Attr("slot", "page")}
	attrs = append(attrs, projectAssetFilterRouteBridge(area)...)
	return projectRouteDocument(project.Title, catalog, area, roleLabel, page, uisignals.RouteKindData, g.El("lv-project-page", attrs...), projectDocumentExtras{CSRFToken: csrfToken, Area: area}, chromeOptions)
}

func ProjectBootstrapSignals(catalog catalog.Catalog, project projectview.DevelopView, assets []projectview.DevelopAssetView, activeType, query, roleLabel string, chromeOptions ...webpage.Provider) map[string]any {
	return ProjectBootstrapSignalsForEnvironment(catalog, project, assets, activeType, query, "", roleLabel, chromeOptions...)
}

func ProjectBootstrapSignalsForEnvironment(catalog catalog.Catalog, project projectview.DevelopView, assets []projectview.DevelopAssetView, activeType, query, environment, roleLabel string, chromeOptions ...webpage.Provider) map[string]any {
	return ProjectBootstrapSignalsForArea(catalog, project, assets, "sources", activeType, query, environment, roleLabel, chromeOptions...)
}

func ProjectBootstrapSignalsForArea(catalog catalog.Catalog, project projectview.DevelopView, assets []projectview.DevelopAssetView, area, activeType, query, environment, roleLabel string, chromeOptions ...webpage.Provider) map[string]any {
	area = canonicalProjectArea(area)
	page := projectPageSignal(project, assets, nil, area, activeType, query, environment)
	return projectRouteBootstrapSignals(catalog, area, roleLabel, page, uisignals.RouteKindData, nil, chromeOptions)
}

func ProjectAssetListResultsPatch(projectID string, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) map[string]any {
	list := projectAssetListSignal(projectID, assets, edges, "", "", nil, "", "")
	return map[string]any{"page": map[string]any{
		"assetList": map[string]any{"assets": list.Assets},
	}}
}

func ConnectionsPage(catalog catalog.Catalog, projectID string, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, query, roleLabel string, chromeOptions ...webpage.Provider) g.Node {
	return ConnectionsPageForEnvironment(catalog, projectID, assets, edges, query, "", roleLabel, "", chromeOptions...)
}

func ConnectionsPageForEnvironment(catalog catalog.Catalog, projectID string, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, query, environment, roleLabel, csrfToken string, chromeOptions ...webpage.Provider) g.Node {
	return ConnectionsPageWithAdministrationForEnvironment(catalog, projectID, assets, edges, query, environment, roleLabel, csrfToken, ConnectionAdministrationView{}, ConnectionCommandBindings{}, chromeOptions...)
}

func ConnectionsPageWithAdministrationForEnvironment(catalog catalog.Catalog, projectID string, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, query, environment, roleLabel, csrfToken string, administration ConnectionAdministrationView, commands ConnectionCommandBindings, chromeOptions ...webpage.Provider) g.Node {
	page := connectionsPageSignal(projectID, assets, edges, query, environment, administration)
	if strings.TrimSpace(projectID) == "" {
		catalog = catalogWithoutProjectContext(catalog)
	}
	rootAttributes := []g.Node{
		g.Attr("slot", "page"),
		g.Attr("data-on:lv-entity-list-query__debounce.200ms", "$entityListQuery = evt.detail.query; "+uiactions.QueryPost("/connections/search", "entityListQuery")),
	}
	rootAttributes = append(rootAttributes, connectionAdministrationRouteBridge(commands)...)
	return projectRouteDocument("Connections", catalog, "connections", roleLabel, page, uisignals.RouteKindConnections,
		g.El("lv-connections-page", rootAttributes...),
		projectDocumentExtras{CSRFToken: csrfToken, BootstrapSignals: map[string]any{"connectionAdmin": emptyConnectionAdministrationSignal(administration.Status)}},
		chromeOptions,
	)
}

func ConnectionsBootstrapSignals(catalog catalog.Catalog, projectID string, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, query, roleLabel string, chromeOptions ...webpage.Provider) map[string]any {
	return ConnectionsBootstrapSignalsForEnvironment(catalog, projectID, assets, edges, query, "", roleLabel, chromeOptions...)
}

func ConnectionsBootstrapSignalsForEnvironment(catalog catalog.Catalog, projectID string, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, query, environment, roleLabel string, chromeOptions ...webpage.Provider) map[string]any {
	return ConnectionsBootstrapSignalsWithAdministrationForEnvironment(catalog, projectID, assets, edges, query, environment, roleLabel, ConnectionAdministrationView{}, chromeOptions...)
}

func ConnectionsBootstrapSignalsWithAdministrationForEnvironment(catalog catalog.Catalog, projectID string, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, query, environment, roleLabel string, administration ConnectionAdministrationView, chromeOptions ...webpage.Provider) map[string]any {
	page := connectionsPageSignal(projectID, assets, edges, query, environment, administration)
	if strings.TrimSpace(projectID) == "" {
		catalog = catalogWithoutProjectContext(catalog)
	}
	patch := projectRouteBootstrapSignals(catalog, "connections", roleLabel, page, uisignals.RouteKindConnections, nil, chromeOptions)
	patch["connectionAdmin"] = emptyConnectionAdministrationSignal(administration.Status)
	return patch
}

func ConnectionsListResultsPatch(assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) map[string]any {
	return ConnectionsListResultsPatchWithAdministration(assets, edges, ConnectionAdministrationView{})
}

func ConnectionsListResultsPatchWithAdministration(assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, administration ConnectionAdministrationView) map[string]any {
	return map[string]any{"page": map[string]any{
		"connections": connectionSummarySignals(assets, edges, administration),
	}}
}

func catalogWithoutProjectContext(value catalog.Catalog) catalog.Catalog {
	value.Project = catalog.Project{}
	return value
}

type projectDocumentExtras struct {
	CSRFToken        string
	Area             string
	BootstrapSignals map[string]any
}

func connectionAdministrationRouteBridge(commands ConnectionCommandBindings) []g.Node {
	if commands.Create.OperationID() == "" || commands.Update.OperationID() == "" ||
		commands.Test.OperationID() == "" || commands.Refresh.OperationID() == "" ||
		commands.Enable.OperationID() == "" || commands.Disable.OperationID() == "" {
		return nil
	}
	configuration := "$connectionAdmin.command = evt.detail; $connectionAdmin.status = {loading: true, error: '', message: ''}; " +
		uiactions.CommandPostSwitch("$connectionAdmin.command.action", map[string]uicommand.Binding{
			"create": commands.Create,
			"update": commands.Update,
		}, "/connections/administration/configuration", "connectionAdmin")
	lifecycle := "$connectionAdmin.command = evt.detail; $connectionAdmin.status = {loading: true, error: '', message: ''}; " +
		uiactions.CommandPostSwitch("$connectionAdmin.command.action", map[string]uicommand.Binding{
			"test":    commands.Test,
			"refresh": commands.Refresh,
			"enable":  commands.Enable,
			"disable": commands.Disable,
		}, "/connections/administration/lifecycle", "connectionAdmin")
	return []g.Node{
		g.Attr("data-on:lv-connection-administration-save", configuration),
		g.Attr("data-on:lv-connection-administration-action", lifecycle),
	}
}

func projectAssetFilterRouteBridge(area string) []g.Node {
	endpoint := projectAssetSearchHref(area)
	filter := "$projectAssetType = evt.detail.type; $projectAssetQuery = evt.detail.query; " + uiactions.QueryPost(endpoint, "projectAssetType", "projectAssetQuery")
	return []g.Node{g.Attr("data-on:lv-project-asset-filter__debounce.200ms", filter)}
}

func projectPageSignal(project projectview.DevelopView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, area, activeType, query, environment string) uisignals.ResourcePageSignal {
	area = canonicalProjectArea(area)
	assetType := projectAssetTypeForArea(area)
	// Each route owns one resource type. Keep the signal scoped even when a
	// stale query or client event supplies a different type.
	activeType = assetType
	return uisignals.ResourcePageSignal{
		Kind:        uisignals.RouteKindData,
		Title:       projectAreaLabel(area),
		Environment: uisignals.Optional(environment),
		AssetList: uisignals.Pointer(projectAssetListSignal(
			project.ID,
			assets,
			edges,
			activeType,
			query,
			nil,
			"No assets match this view.",
			projectAssetBaseHref(area),
		)),
	}
}

func connectionsPageSignal(projectID string, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, query, environment string, administration ConnectionAdministrationView) uisignals.ConnectionsPageSignal {
	return uisignals.ConnectionsPageSignal{
		Kind:        uisignals.RouteKindConnections,
		Title:       "Connections",
		Description: uisignals.Pointer("Data connections used by published semantic models."),
		Environment: uisignals.Optional(environment),
		Query:       uisignals.Optional(query),
		Connections: connectionSummarySignals(assets, edges, administration),
	}
}

func connectionSummarySignals(assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, administration ConnectionAdministrationView) []uisignals.ConnectionSummarySignal {
	connections := make([]projectview.DevelopAssetView, 0, len(assets))
	for _, asset := range assets {
		if asset.Type == "connection" {
			connections = append(connections, asset)
		}
	}
	sort.SliceStable(connections, func(i, j int) bool {
		left, right := strings.ToLower(assetTitle(connections[i])), strings.ToLower(assetTitle(connections[j]))
		if left != right {
			return left < right
		}
		return connections[i].ID < connections[j].ID
	})
	out := make([]uisignals.ConnectionSummarySignal, 0, len(connections))
	for _, connection := range connections {
		lifecycle := connectionLifecycleSignal(connection, assets, edges, administration)
		out = append(out, uisignals.ConnectionSummarySignal{
			ID:               connection.ID,
			Title:            assetTitle(connection),
			Description:      uisignals.Optional(connection.Description),
			DetailHref:       assetnav.ConnectionAssetSectionHref(connection.ID, "details"),
			Kind:             emptyDash(firstNonEmpty(metaString(connection.Payload, "Kind", "kind"), metaString(connection.Payload, "Provider", "provider"))),
			Lifecycle:        lifecycle,
			Scope:            emptyDash(metaString(connection.Payload, "Scope", "scope")),
			SourceCount:      connectionSourceCount(connection.ID, edges),
			CredentialStatus: lifecycle.StatusLabel,
		})
	}
	return out
}

func connectionSourceCount(connectionID string, edges []projectview.DevelopEdgeView) int64 {
	seen := map[string]struct{}{}
	for _, edge := range edges {
		if edge.Type == "uses_connection" && edge.ToAssetID == connectionID {
			seen[edge.FromAssetID] = struct{}{}
		}
	}
	return int64(len(seen))
}

func projectAssetListSignal(projectID string, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeType, query string, tabs []uisignals.ResourceTabSignal, empty, searchHref string) uisignals.ResourceAssetListSignal {
	items := make([]uisignals.ResourceAssetSummarySignal, 0, len(assets))
	sortedAssets := sortedProjectAssetList(assets)
	assetIndex := assetsByID(sortedAssets)
	for _, asset := range sortedAssets {
		items = append(items, projectAssetSummarySignal(projectID, asset, assetIndex, edges))
	}
	return uisignals.ResourceAssetListSignal{
		Query:      uisignals.Optional(query),
		ActiveType: uisignals.Optional(activeType),
		SearchHref: searchHref,
		Tabs:       tabs,
		Assets:     items,
		Empty:      empty,
	}
}

func sortedProjectAssetList(assets []projectview.DevelopAssetView) []projectview.DevelopAssetView {
	out := append([]projectview.DevelopAssetView(nil), assets...)
	sort.SliceStable(out, func(i, j int) bool {
		leftPriority := projectAssetTypePriority(out[i].Type)
		rightPriority := projectAssetTypePriority(out[j].Type)
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		leftTitle := strings.ToLower(assetTitle(out[i]))
		rightTitle := strings.ToLower(assetTitle(out[j]))
		if leftTitle != rightTitle {
			return leftTitle < rightTitle
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func projectAssetTypePriority(typ string) int {
	switch typ {
	case "dashboard":
		return 0
	case "model_table":
		return 1
	case "semantic_model":
		return 2
	case "connection":
		return 3
	case "source":
		return 4
	default:
		return 10
	}
}

func projectAssetSummarySignal(projectID string, asset projectview.DevelopAssetView, assetIndex map[string]projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) uisignals.ResourceAssetSummarySignal {
	detailHref := assetnav.CanonicalAssetSectionHref(asset, "details")
	openHref := detailHref
	if asset.Href != "" {
		openHref = asset.Href
	}
	parentTitle := emptyDash("")
	parentHref := ""
	if asset.Type == "source" {
		if connection, ok := assetIndex[assetnav.SourceConnectionID(asset.ID, edges)]; ok && connection.Type == "connection" {
			parentTitle = assetTitle(connection)
			parentHref = assetnav.ConnectionAssetSectionHref(connection.ID, "details")
		}
	} else if parent, ok := assetIndex[asset.ParentID]; ok {
		parentTitle = assetTitle(parent)
		parentHref = assetnav.CanonicalAssetSectionHref(parent, "details")
	}
	return uisignals.ResourceAssetSummarySignal{
		ID:          asset.ID,
		Title:       assetTitle(asset),
		Description: uisignals.Optional(asset.Description),
		Type:        asset.Type,
		TypeLabel:   assetTypeLabel(asset.Type),
		Key:         asset.Key,
		ParentTitle: uisignals.Optional(parentTitle),
		ParentHref:  uisignals.Optional(parentHref),
		DetailHref:  detailHref,
		OpenHref:    openHref,
	}
}

func projectAssetPageSignal(project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection string, lineage assetLineageModel) uisignals.ResourceAssetPageSignal {
	return projectAssetPageSignalWithRefresh(project, asset, assets, edges, activeSection, lineage, AssetRefreshState{})
}

func projectAssetPageSignalWithRefresh(project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection string, lineage assetLineageModel, refresh AssetRefreshState) uisignals.ResourceAssetPageSignal {
	return projectAssetPageSignalWithRefreshAndVersions(project, asset, assets, edges, activeSection, lineage, refresh, AssetVersionsState{})
}

func projectAssetPageSignalWithRefreshAndVersions(project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection string, lineage assetLineageModel, refresh AssetRefreshState, versions AssetVersionsState) uisignals.ResourceAssetPageSignal {
	page := baseProjectAssetPageSignalWithRefreshAndVersions(project, asset, assets, edges, activeSection, lineage, refresh, versions)
	area := projectAreaForAssetType(asset.Type)
	areaHref := projectAssetBaseHref(area)
	areaLabel := projectAreaLabel(area)
	page.Kind = uisignals.RouteKindData
	page.Breadcrumbs = []uisignals.ResourceBreadcrumbSignal{
		{Label: areaLabel, Href: uisignals.Pointer(areaHref)},
		{Label: assetTitle(asset), Current: uisignals.Pointer(true)},
	}
	actions := []uisignals.ResourceActionSignal{{Label: "Back to " + strings.ToLower(areaLabel), Href: uisignals.Pointer(areaHref), Icon: uisignals.Pointer("back")}}
	if assetRefreshable(asset.Type) {
		actions = append([]uisignals.ResourceActionSignal{{
			Label:    "Run now",
			Icon:     uisignals.Pointer("refresh"),
			Command:  uisignals.Pointer("run-refresh-pipeline"),
			Disabled: uisignals.Optional(assetRefreshSignal(refresh).Running),
		}}, actions...)
	}
	if asset.Href != "" {
		actions = append(actions, uisignals.ResourceActionSignal{Label: "Open asset", Href: uisignals.Pointer(asset.Href), Icon: uisignals.Pointer("open")})
	}
	page.Actions = uisignals.OptionalSlice(actions)
	page.Tabs = []uisignals.ResourceTabSignal{
		{ID: "details", Label: "Details", Href: assetnav.CanonicalAssetSectionHref(asset, "details"), Active: activeSection == "details"},
	}
	if assetDataInspectable(asset.Type) {
		page.Tabs = append(page.Tabs, uisignals.ResourceTabSignal{ID: "data", Label: "Data", Href: projectAssetDataHref(asset), Active: activeSection == "data"})
	}
	if assetRefreshable(asset.Type) {
		page.Tabs = append(page.Tabs, uisignals.ResourceTabSignal{ID: "refreshes", Label: "Refreshes", Href: assetnav.CanonicalAssetSectionHref(asset, "refreshes"), Active: activeSection == "refreshes"})
	}
	if assetHasVersions(versions) {
		page.Tabs = append(page.Tabs, uisignals.ResourceTabSignal{ID: "versions", Label: "Versions", Href: assetnav.CanonicalAssetSectionHref(asset, "versions"), Active: activeSection == "versions", Count: uisignals.Pointer(int64(len(versions.Versions)))})
	}
	page.Tabs = append(page.Tabs, uisignals.ResourceTabSignal{ID: "lineage", Label: "Lineage", Href: assetnav.CanonicalAssetSectionHref(asset, "lineage"), Active: activeSection == "lineage", Count: uisignals.Pointer(int64(lineage.Count))})
	return page
}

func assetHasVersions(versions AssetVersionsState) bool {
	return strings.TrimSpace(versions.CurrentContentHash) != "" || len(versions.Versions) > 0
}

func connectionAssetPageSignal(project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection string, lineage assetLineageModel) uisignals.ResourceAssetPageSignal {
	return connectionAssetPageSignalWithVersions(project, asset, assets, edges, activeSection, lineage, AssetVersionsState{})
}

func connectionAssetPageSignalWithVersions(project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection string, lineage assetLineageModel, versions AssetVersionsState, administration ...ConnectionAdministrationView) uisignals.ResourceAssetPageSignal {
	page := baseProjectAssetPageSignalWithRefreshAndVersions(project, asset, assets, edges, activeSection, lineage, AssetRefreshState{}, versions)
	admin := ConnectionAdministrationView{}
	if len(administration) > 0 {
		admin = administration[0]
	}
	page.Kind = uisignals.RouteKindConnectionAsset
	lifecycle := connectionLifecycleSignal(asset, assets, edges, admin)
	page.ConnectionLifecycle = &lifecycle
	page.Breadcrumbs = []uisignals.ResourceBreadcrumbSignal{
		{Label: "Connections", Href: uisignals.Pointer("/connections")},
		{Label: assetTitle(asset), Current: uisignals.Pointer(true)},
	}
	page.Actions = uisignals.Pointer([]uisignals.ResourceActionSignal{{Label: "Back to connections", Href: uisignals.Pointer("/connections"), Icon: uisignals.Pointer("back")}})
	page.Tabs = []uisignals.ResourceTabSignal{
		{ID: "details", Label: "Details", Href: assetnav.ConnectionAssetSectionHref(asset.ID, "details"), Active: activeSection == "details"},
		{ID: "lineage", Label: "Lineage", Href: assetnav.ConnectionAssetSectionHref(asset.ID, "lineage"), Active: activeSection == "lineage", Count: uisignals.Pointer(int64(lineage.Count))},
	}
	return page
}

func baseProjectAssetPageSignal(project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection string, lineage assetLineageModel) uisignals.ResourceAssetPageSignal {
	return baseProjectAssetPageSignalWithRefresh(project, asset, assets, edges, activeSection, lineage, AssetRefreshState{})
}

func baseProjectAssetPageSignalWithRefresh(project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection string, lineage assetLineageModel, refresh AssetRefreshState) uisignals.ResourceAssetPageSignal {
	return baseProjectAssetPageSignalWithRefreshAndVersions(project, asset, assets, edges, activeSection, lineage, refresh, AssetVersionsState{})
}

func baseProjectAssetPageSignalWithRefreshAndVersions(project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection string, lineage assetLineageModel, refresh AssetRefreshState, versions AssetVersionsState) uisignals.ResourceAssetPageSignal {
	activeSection = normalizeProjectAssetSection(activeSection)
	page := uisignals.ResourceAssetPageSignal{
		Title:         assetTitle(asset),
		AssetID:       asset.ID,
		ActiveSection: activeSection,
		Asset:         projectAssetSummarySignal(project.ID, asset, assetsByID(assets), edges),
	}
	if assetRefreshable(asset.Type) {
		page.Refresh = uisignals.Pointer(assetRefreshSignal(refresh))
	}
	if activeSection == "details" {
		page.Details = uisignals.Pointer(projectAssetDetailsSignalWithRefresh(project, asset, assets, edges, refresh))
	}
	if activeSection == "lineage" {
		page.Lineage = uisignals.Pointer(uisignals.ResourceAssetLineageSignal{
			Count:       int64(lineage.Count),
			Graph:       lineage.Graph,
			UsesTable:   lineage.Uses,
			UsedByTable: lineage.UsedBy,
		})
	}
	if activeSection == "refreshes" && assetRefreshable(asset.Type) {
		runsTable := assetRefreshesTable(refresh)
		page.Refresh.RunsTable = &runsTable
	}
	if activeSection == "versions" {
		versionSignal := assetVersionsSignal(versions)
		page.Versions = &versionSignal
	}
	return page
}

func projectAssetDetailsSignal(project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) uisignals.ResourceAssetDetailsSignal {
	return projectAssetDetailsSignalWithRefresh(project, asset, assets, edges, AssetRefreshState{})
}

func projectAssetDetailsSignalWithRefresh(project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, refresh AssetRefreshState) uisignals.ResourceAssetDetailsSignal {
	model := assetDetailModelForAssetWithRefresh(project, asset, assets, edges, refresh)
	sections := make([]uisignals.ResourceDetailSectionSignal, 0, len(model.Sections))
	for _, section := range model.Sections {
		sections = append(sections, uisignals.ResourceDetailSectionSignal{
			Title: section.Title,
			Facts: uisignals.OptionalSlice(definitionFactSignals(section.Facts)),
			Table: uisignals.Pointer(section.Table),
			Code:  uisignals.Optional(section.Code),
			Lang:  uisignals.Optional(section.Lang),
		})
	}
	return uisignals.ResourceAssetDetailsSignal{
		Overview:           definitionFactSignals(model.Overview),
		Sections:           sections,
		SemanticModelGraph: model.SemanticModelGraph,
	}
}

func definitionFactSignals(facts []definitionFact) []uisignals.DefinitionFactSignal {
	out := make([]uisignals.DefinitionFactSignal, 0, len(facts))
	for _, fact := range facts {
		if strings.TrimSpace(fact.Value) == "" {
			continue
		}
		out = append(out, uisignals.DefinitionFactSignal{Label: fact.Label, Value: fact.Value, Code: uisignals.Optional(fact.Code), Wide: uisignals.Optional(fact.Wide)})
	}
	return out
}

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

func ConnectionAssetBootstrapSignalsForEnvironment(catalog catalog.Catalog, project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection, environment, roleLabel string, versions AssetVersionsState) map[string]any {
	return ConnectionAssetBootstrapSignalsWithAdministrationForEnvironment(catalog, project, asset, assets, edges, activeSection, environment, roleLabel, versions, ConnectionAdministrationView{})
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

func ConnectionAssetPageWithVersionsForEnvironment(catalog catalog.Catalog, project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, activeSection, environment, roleLabel string, versions AssetVersionsState) g.Node {
	return ConnectionAssetPageWithAdministrationForEnvironment(catalog, project, asset, assets, edges, activeSection, environment, roleLabel, versions, ConnectionAdministrationView{}, ConnectionCommandBindings{}, "", nil)
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
	case "models":
		return string(projectview.AssetTypeModelTable)
	case "semantic-models":
		return string(projectview.AssetTypeSemanticModel)
	default:
		return string(projectview.AssetTypeSource)
	}
}

func ValidProjectAssetSection(section string) bool {
	switch section {
	case "details", "data", "lineage", "refreshes", "versions":
		return true
	default:
		return false
	}
}

type AssetRefreshState struct {
	CSRFToken        string
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

func ProjectAssetRefreshSignals(project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, refresh AssetRefreshState, activeSection string) map[string]any {
	lineage := assetLineage(project.ID, asset, assets, edges)
	return map[string]any{
		"page": projectAssetPageSignalWithRefresh(project, asset, assets, edges, activeSection, lineage, refresh),
	}
}

func assetRefreshSignal(refresh AssetRefreshState) uisignals.ResourceAssetRefreshSignal {
	status := strings.TrimSpace(refresh.Latest.Status)
	if status == "" {
		status = "not refreshed"
	}
	return uisignals.ResourceAssetRefreshSignal{
		Status:         status,
		Running:        status == "queued" || status == "running",
		LastSuccessful: refresh.LatestSuccessful.FinishedAt,
	}
}

func assetVersionsSignal(state AssetVersionsState) uisignals.ResourceAssetVersionsSignal {
	return uisignals.ResourceAssetVersionsSignal{
		CurrentContentHash: state.CurrentContentHash,
		Table:              assetVersionsTable(state),
	}
}

func assetVersionsTable(state AssetVersionsState) recordTable {
	rows := make([]map[string]any, 0, len(state.Versions))
	current := strings.TrimSpace(state.CurrentContentHash)
	for _, version := range state.Versions {
		status := version.Status
		if current != "" && version.ContentHash == current {
			status = "current"
		}
		rows = append(rows, map[string]any{
			"version":      shortHash(version.ContentHash),
			"published":    emptyDash(firstNonEmpty(version.ActivatedAt, version.CreatedAt)),
			"status":       recordTableBadge{Label: status, Tone: uisignals.Pointer(versionStatusTone(status))},
			"config_hash":  shortHash(version.ContentHash),
			"source_file":  emptyDash(version.SourceFile),
			"published_by": emptyDash(version.CreatedBy),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "version", Header: "Version", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("150px")},
			{ID: "published", Header: "Published", Width: uisignals.Pointer("180px")},
			{ID: "status", Header: "Status", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("120px")},
			{ID: "config_hash", Header: "Config hash", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("130px")},
			{ID: "source_file", Header: "Source file", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("220px")},
			{ID: "published_by", Header: "Published by", Width: uisignals.Pointer("150px")},
		},
		Rows:     rows,
		Empty:    "No config versions recorded for this asset yet.",
		MinWidth: uisignals.Pointer("850px"),
	}
}

func versionStatusTone(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "current":
		return "success"
	case "active", "validated":
		return "accent"
	case "inactive":
		return "muted"
	default:
		return "muted"
	}
}

func shortVersionID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 18 {
		return id
	}
	return id[:18]
}

func shortHash(hash string) string {
	hash = strings.TrimSpace(hash)
	if len(hash) == 0 {
		return "-"
	}
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

func assetRefreshesTable(refresh AssetRefreshState) recordTable {
	rows := make([]map[string]any, 0, len(refresh.Runs))
	for _, run := range refresh.Runs {
		rows = append(rows, map[string]any{
			"status":       refreshStatusGridValue(run.Status),
			"started":      emptyDash(run.StartedAt),
			"duration":     emptyDash(refreshRunDuration(run)),
			"triggered_by": emptyDash(run.PrincipalDisplayName),
			"trigger":      refreshTriggerLabel(run.TriggerType),
			"run":          emptyDash(shortRefreshRunID(run.ID)),
			"error":        emptyDash(run.Error),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "status", Header: "Status", Kind: uisignals.Pointer("status"), Width: uisignals.Pointer("140px")},
			{ID: "started", Header: "Started", Width: uisignals.Pointer("180px")},
			{ID: "duration", Header: "Duration", Width: uisignals.Pointer("110px")},
			{ID: "triggered_by", Header: "Triggered by", Width: uisignals.Pointer("130px")},
			{ID: "trigger", Header: "Trigger", Width: uisignals.Pointer("130px")},
			{ID: "run", Header: "Run ID", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("160px")},
			{ID: "error", Header: "Error"},
		},
		Rows:     rows,
		Empty:    "No refresh runs have been recorded for this asset.",
		MinWidth: uisignals.Pointer("1040px"),
	}
}

func refreshTriggerLabel(trigger string) string {
	switch strings.TrimSpace(trigger) {
	case "manual":
		return "Manual"
	case "schedule":
		return "Schedule"
	case "retry":
		return "Retry"
	default:
		return "-"
	}
}

func refreshStatusGridValue(status string) any {
	status = strings.TrimSpace(status)
	if status == "" {
		status = "not refreshed"
	}
	return recordTableBadge{Label: status, Tone: uisignals.Pointer(refreshStatusBadgeTone(status))}
}

func refreshStatusBadgeTone(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeeded":
		return "success"
	case "running", "queued":
		return "accent"
	case "failed":
		return "danger"
	default:
		return "muted"
	}
}

func shortRefreshRunID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 18 {
		return id
	}
	return id[:18]
}

func refreshRunDuration(run AssetRefreshRun) string {
	started, ok := parseRefreshTime(run.StartedAt)
	if !ok {
		return ""
	}
	finished, ok := parseRefreshTime(run.FinishedAt)
	if !ok || finished.Before(started) {
		return ""
	}
	return finished.Sub(started).Round(time.Second).String()
}

func parseRefreshTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func assetRefreshable(assetType string) bool {
	return assetType == "refresh_pipeline"
}

func assetDataInspectable(assetType string) bool {
	return assetType == "semantic_model" || assetType == "model_table"
}

func projectAssetDataHref(asset projectview.DevelopAssetView) string {
	return assetnav.CanonicalAssetSectionHref(asset, "data")
}

func normalizeProjectAssetSection(section string) string {
	if ValidProjectAssetSection(section) {
		return section
	}
	return "details"
}

type assetLineageModel struct {
	Count  int
	Graph  assetLineageGraph
	Uses   recordTable
	UsedBy recordTable
}

type assetLineageGraph = uisignals.AssetLineageGraphSignal
type assetLineageNode = uisignals.AssetLineageNodeSignal
type assetLineageEdge = uisignals.AssetLineageEdgeSignal

func assetLineage(projectID string, selected projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) assetLineageModel {
	byID := assetsByID(assets)
	outgoing := edgesByFromAsset(edges)
	incoming := edgesByToAsset(edges)
	graph := assetLineageGraph{
		Nodes: []assetLineageNode{lineageNode(projectID, selected, 0, true, edges)},
	}
	nodeIndex := map[string]int{selected.ID: 0}
	seenEdges := map[string]struct{}{}

	addNode := func(asset projectview.DevelopAssetView, rank int, selected bool) {
		if asset.ID == "" {
			return
		}
		if !selected && isLineageHiddenContextAsset(asset) {
			return
		}
		if existing, ok := nodeIndex[asset.ID]; ok {
			node := graph.Nodes[existing]
			if !uisignals.ValueOrZero(node.Selected) && absInt(rank) < absInt(int(node.Rank)) {
				node.Rank = int64(rank)
				node.Side = lineageSideForRank(rank)
				graph.Nodes[existing] = node
			}
			return
		}
		nodeIndex[asset.ID] = len(graph.Nodes)
		graph.Nodes = append(graph.Nodes, lineageNode(projectID, asset, rank, selected, edges))
	}
	addEdge := func(edge projectview.DevelopEdgeView) {
		if edge.FromAssetID == "" || edge.ToAssetID == "" {
			return
		}
		if _, ok := nodeIndex[edge.FromAssetID]; !ok {
			return
		}
		if _, ok := nodeIndex[edge.ToAssetID]; !ok {
			return
		}
		key := lineageEdgeKey(edge)
		if _, ok := seenEdges[key]; ok {
			return
		}
		graph.Edges = append(graph.Edges, assetLineageEdge{
			ID:     key,
			Source: edge.FromAssetID,
			Target: edge.ToAssetID,
			Label:  uisignals.Optional(labelFromKey(edge.Type)),
			Kind:   edge.Type,
		})
		seenEdges[key] = struct{}{}
	}

	type lineageWalkConfig struct {
		edges  func(string) []projectview.DevelopEdgeView
		peerID func(projectview.DevelopEdgeView) string
		rank   func(int) int
	}
	var walkDependencyEdges func(assetID string, depth int, visiting map[string]struct{}, config lineageWalkConfig)
	walkDependencyEdges = func(assetID string, depth int, visiting map[string]struct{}, config lineageWalkConfig) {
		if _, ok := visiting[assetID]; ok {
			return
		}
		visiting[assetID] = struct{}{}
		defer delete(visiting, assetID)
		for _, edge := range sortedLineageEdges(config.edges(assetID), byID) {
			if !isLineageDependencyEdge(edge) {
				continue
			}
			asset, ok := byID[config.peerID(edge)]
			if !ok {
				continue
			}
			addNode(asset, config.rank(depth), false)
			addEdge(edge)
			walkDependencyEdges(asset.ID, depth+1, visiting, config)
		}
	}

	upstreamWalk := lineageWalkConfig{
		edges: func(assetID string) []projectview.DevelopEdgeView {
			return outgoing[assetID]
		},
		peerID: func(edge projectview.DevelopEdgeView) string {
			return edge.ToAssetID
		},
		rank: func(depth int) int {
			return -depth
		},
	}
	downstreamWalk := lineageWalkConfig{
		edges: func(assetID string) []projectview.DevelopEdgeView {
			return incoming[assetID]
		},
		peerID: func(edge projectview.DevelopEdgeView) string {
			return edge.FromAssetID
		},
		rank: func(depth int) int {
			return depth
		},
	}

	for _, rootID := range lineageDependencyRootIDs(selected, outgoing, byID) {
		if rootID != selected.ID {
			addNode(byID[rootID], 1, false)
		}
		walkDependencyEdges(rootID, 1, map[string]struct{}{}, upstreamWalk)
		if rootID != selected.ID {
			walkDependencyEdges(rootID, 1, map[string]struct{}{}, downstreamWalk)
		}
	}
	walkDependencyEdges(selected.ID, 1, map[string]struct{}{}, downstreamWalk)
	addContainsContext(selected.ID, &graph, nodeIndex, byID, edges, addNode, addEdge)

	sortLineageNodes(graph.Nodes)
	sortLineageGraphEdges(graph.Edges)
	collapsedGraph := collapsedAssetLineageGraph(projectID, selected, graph, byID, edges)
	enrichAssetLineageGraph(collapsedGraph, byID, edges)
	usesRows, usedByRows := lineageTablesFromGraph(projectID, selected, collapsedGraph, byID, edges)
	return assetLineageModel{
		Count:  len(usesRows) + len(usedByRows),
		Graph:  collapsedGraph,
		Uses:   lineageTable(usesRows, "This asset does not reference other assets."),
		UsedBy: lineageTable(usedByRows, "No assets reference this asset."),
	}
}

func enrichAssetLineageGraph(graph assetLineageGraph, assets map[string]projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) {
	nodeIndex := map[string]int{}
	for index, node := range graph.Nodes {
		nodeIndex[node.ID] = index
	}
	for _, edge := range graph.Edges {
		if sourceIndex, ok := nodeIndex[edge.Source]; ok {
			graph.Nodes[sourceIndex].VisibleDownstreamCount = incrementOptionalInt64(graph.Nodes[sourceIndex].VisibleDownstreamCount)
		}
		if targetIndex, ok := nodeIndex[edge.Target]; ok {
			graph.Nodes[targetIndex].VisibleUpstreamCount = incrementOptionalInt64(graph.Nodes[targetIndex].VisibleUpstreamCount)
		}
	}

	containsByParent := map[string]map[string]int{}
	for _, edge := range edges {
		if isLineageDependencyEdge(edge) {
			if index, ok := nodeIndex[edge.FromAssetID]; ok {
				graph.Nodes[index].UsesCount = incrementOptionalInt64(graph.Nodes[index].UsesCount)
			}
			if index, ok := nodeIndex[edge.ToAssetID]; ok {
				graph.Nodes[index].UsedByCount = incrementOptionalInt64(graph.Nodes[index].UsedByCount)
			}
			continue
		}
		if !isContainsEdge(edge) {
			continue
		}
		child, ok := assets[edge.ToAssetID]
		if !ok {
			continue
		}
		if _, ok := containsByParent[edge.FromAssetID]; !ok {
			containsByParent[edge.FromAssetID] = map[string]int{}
		}
		containsByParent[edge.FromAssetID][child.Type]++
	}
	for index, node := range graph.Nodes {
		contains := containsByParent[node.ID]
		if len(contains) == 0 {
			continue
		}
		count := 0
		for _, value := range contains {
			count += value
		}
		graph.Nodes[index].ContainedCount = uisignals.Pointer(int64(count))
		graph.Nodes[index].ContainedSummary = uisignals.Optional(lineageContainedSummary(contains))
	}
}

func lineageContainedSummary(counts map[string]int) string {
	types := make([]string, 0, len(counts))
	for typ := range counts {
		types = append(types, typ)
	}
	sort.Slice(types, func(i, j int) bool {
		return assetTypeLabel(types[i]) < assetTypeLabel(types[j])
	})
	parts := make([]string, 0, len(types))
	for _, typ := range types {
		parts = append(parts, fmt.Sprintf("%d %s", counts[typ], pluralAssetTypeLabel(typ, counts[typ])))
	}
	return strings.Join(parts, ", ")
}

func incrementOptionalInt64(value *int64) *int64 {
	next := uisignals.ValueOrZero(value) + 1
	return &next
}

func pluralAssetTypeLabel(typ string, count int) string {
	label := strings.ToLower(assetTypeLabel(typ))
	if count == 1 {
		return label
	}
	if strings.HasSuffix(label, "y") && len(label) > 1 {
		return strings.TrimSuffix(label, "y") + "ies"
	}
	return label + "s"
}

func collapsedAssetLineageGraph(projectID string, selected projectview.DevelopAssetView, graph assetLineageGraph, assets map[string]projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) assetLineageGraph {
	if selected.Type == "catalog" {
		return graph
	}
	selectedAnchor, selectedAnchorOK := lineageVisibleAnchor(selected, assets)
	out := assetLineageGraph{}
	nodeIndex := map[string]int{}
	addNode := func(asset projectview.DevelopAssetView) {
		if asset.ID == "" {
			return
		}
		if _, ok := nodeIndex[asset.ID]; ok {
			return
		}
		selectedNode := selectedAnchorOK && asset.ID == selectedAnchor.ID
		nodeIndex[asset.ID] = len(out.Nodes)
		out.Nodes = append(out.Nodes, lineageNode(projectID, asset, lineageVisualLayer(asset.Type), selectedNode, edges))
	}

	type collapsedEdge struct {
		source string
		target string
		kind   string
		label  string
	}
	candidates := []collapsedEdge{}
	for _, node := range graph.Nodes {
		asset, ok := assets[node.ID]
		if !ok {
			continue
		}
		if anchor, ok := lineageVisibleAnchor(asset, assets); ok {
			addNode(anchor)
		}
	}
	for _, edge := range graph.Edges {
		if !isLineageDependencyEdge(projectview.DevelopEdgeView{Type: edge.Kind}) {
			continue
		}
		consumer, consumerOK := assets[edge.Source]
		provider, providerOK := assets[edge.Target]
		if !consumerOK || !providerOK {
			continue
		}
		source, sourceOK := lineageVisibleAnchor(provider, assets)
		target, targetOK := lineageVisibleAnchor(consumer, assets)
		if !sourceOK || !targetOK || source.ID == target.ID {
			continue
		}
		policy := lineageProjectionEdge(source.Type, target.Type, edge.Kind)
		addNode(source)
		addNode(target)
		candidates = append(candidates, collapsedEdge{
			source: source.ID,
			target: target.ID,
			kind:   policy.kind,
			label:  policy.label,
		})
	}

	seenEdges := map[string]struct{}{}
	for _, edge := range candidates {
		source := assets[edge.source]
		target := assets[edge.target]
		if lineageVisualLayer(source.Type) >= lineageVisualLayer(target.Type) {
			continue
		}
		key := edge.source + "|" + edge.target + "|" + edge.kind
		if _, ok := seenEdges[key]; ok {
			continue
		}
		seenEdges[key] = struct{}{}
		out.Edges = append(out.Edges, assetLineageEdge{
			ID:     key,
			Source: edge.source,
			Target: edge.target,
			Label:  uisignals.Optional(edge.label),
			Kind:   edge.kind,
		})
	}
	sortLineageNodes(out.Nodes)
	sortLineageGraphEdges(out.Edges)
	return out
}

func edgesByFromAsset(edges []projectview.DevelopEdgeView) map[string][]projectview.DevelopEdgeView {
	out := map[string][]projectview.DevelopEdgeView{}
	for _, edge := range edges {
		out[edge.FromAssetID] = append(out[edge.FromAssetID], edge)
	}
	return out
}

func edgesByToAsset(edges []projectview.DevelopEdgeView) map[string][]projectview.DevelopEdgeView {
	out := map[string][]projectview.DevelopEdgeView{}
	for _, edge := range edges {
		out[edge.ToAssetID] = append(out[edge.ToAssetID], edge)
	}
	return out
}

func sortLineageRows(rows []map[string]any) {
	sort.Slice(rows, func(i, j int) bool {
		return fmt.Sprint(rows[i]["relation"], rows[i]["type"], rows[i]["asset"]) < fmt.Sprint(rows[j]["relation"], rows[j]["type"], rows[j]["asset"])
	})
}

func lineageTablesFromGraph(projectID string, selected projectview.DevelopAssetView, graph assetLineageGraph, assets map[string]projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) ([]map[string]any, []map[string]any) {
	anchorID := selected.ID
	if selected.Type != "catalog" {
		if anchor, ok := lineageVisibleAnchor(selected, assets); ok {
			anchorID = anchor.ID
		}
	}
	usesRows := []map[string]any{}
	usedByRows := []map[string]any{}
	hasDependencyEdges := false
	for _, edge := range graph.Edges {
		if edge.Kind != "contains" {
			hasDependencyEdges = true
			break
		}
	}
	for _, edge := range graph.Edges {
		if hasDependencyEdges {
			if edge.Kind == "contains" {
				continue
			}
			if edge.Target == anchorID {
				if peer, ok := assets[edge.Source]; ok {
					usesRows = append(usesRows, lineageGraphTableRow(projectID, edge, peer, edges))
				}
			}
			if edge.Source == anchorID {
				if peer, ok := assets[edge.Target]; ok {
					usedByRows = append(usedByRows, lineageGraphTableRow(projectID, edge, peer, edges))
				}
			}
			continue
		}
		if edge.Source == anchorID {
			if peer, ok := assets[edge.Target]; ok {
				usesRows = append(usesRows, lineageGraphTableRow(projectID, edge, peer, edges))
			}
		}
		if edge.Target == anchorID {
			if peer, ok := assets[edge.Source]; ok {
				usedByRows = append(usedByRows, lineageGraphTableRow(projectID, edge, peer, edges))
			}
		}
	}
	sortLineageRows(usesRows)
	sortLineageRows(usedByRows)
	return usesRows, usedByRows
}

func lineageGraphTableRow(projectID string, edge assetLineageEdge, peer projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) map[string]any {
	return map[string]any{
		"relation":  firstNonEmpty(uisignals.ValueOrZero(edge.Label), labelFromKey(edge.Kind)),
		"asset":     assetTitle(peer),
		"assetHref": lineageAssetHref(projectID, peer, edges),
		"type":      assetTypeLabel(peer.Type),
		"key":       peer.Key,
	}
}

func lineageTable(rows []map[string]any, empty string) recordTable {
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "relation", Header: "Relationship", Width: uisignals.Pointer("190px")},
			{ID: "asset", Header: "Asset", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("assetHref"), Width: uisignals.Pointer("260px")},
			{ID: "type", Header: "Type", Width: uisignals.Pointer("150px")},
			{ID: "key", Header: "Key", Kind: uisignals.Pointer("code")},
		},
		Rows:     rows,
		Empty:    empty,
		MinWidth: uisignals.Pointer("760px"),
	}
}

func lineageNode(projectID string, asset projectview.DevelopAssetView, rank int, selected bool, edges []projectview.DevelopEdgeView) assetLineageNode {
	return assetLineageNode{
		ID:       asset.ID,
		Label:    assetTitle(asset),
		Kind:     asset.Type,
		Meta:     uisignals.Optional(asset.Key),
		Href:     uisignals.Optional(lineageAssetHref(projectID, asset, edges)),
		Side:     lineageSideForRank(rank),
		Rank:     int64(rank),
		Selected: uisignals.Optional(selected),
	}
}

func lineageSideForRank(rank int) string {
	switch {
	case rank < 0:
		return "upstream"
	case rank > 0:
		return "downstream"
	default:
		return "selected"
	}
}

func lineageDependencyRootIDs(selected projectview.DevelopAssetView, outgoing map[string][]projectview.DevelopEdgeView, assets map[string]projectview.DevelopAssetView) []string {
	rootIDs := []string{selected.ID}
	if !isRollupLineageAsset(selected.Type) {
		return rootIDs
	}
	seen := map[string]struct{}{selected.ID: {}}
	var walk func(string)
	walk = func(assetID string) {
		for _, edge := range sortedLineageEdges(outgoing[assetID], assets) {
			if !isContainsEdge(edge) {
				continue
			}
			if _, ok := seen[edge.ToAssetID]; ok {
				continue
			}
			if _, ok := assets[edge.ToAssetID]; !ok {
				continue
			}
			seen[edge.ToAssetID] = struct{}{}
			rootIDs = append(rootIDs, edge.ToAssetID)
			walk(edge.ToAssetID)
		}
	}
	walk(selected.ID)
	return rootIDs
}

func isLineageHiddenContextAsset(asset projectview.DevelopAssetView) bool {
	return asset.Type == "catalog"
}

func lineageVisibleAnchor(asset projectview.DevelopAssetView, assets map[string]projectview.DevelopAssetView) (projectview.DevelopAssetView, bool) {
	current := asset
	seen := map[string]struct{}{}
	for current.ID != "" {
		if isLineageVisibleGraphAsset(current.Type) {
			return current, true
		}
		if _, ok := seen[current.ID]; ok {
			return projectview.DevelopAssetView{}, false
		}
		seen[current.ID] = struct{}{}
		parent, ok := assets[current.ParentID]
		if !ok {
			return projectview.DevelopAssetView{}, false
		}
		current = parent
	}
	return projectview.DevelopAssetView{}, false
}

func isLineageVisibleGraphAsset(typ string) bool {
	return lineageVisualLayer(typ) >= 0
}

type lineageProjectionLayerPolicy struct {
	assetType string
	layer     int
}

var lineageProjectionLayers = []lineageProjectionLayerPolicy{
	{assetType: "connection", layer: 0},
	{assetType: "source", layer: 1},
	{assetType: "model_table", layer: 2},
	{assetType: "semantic_model", layer: 3},
	{assetType: "dashboard", layer: 4},
}

func lineageVisualLayer(typ string) int {
	for _, policy := range lineageProjectionLayers {
		if typ == policy.assetType {
			return policy.layer
		}
	}
	return -1
}

type lineageProjectionEdgeKey struct {
	sourceType string
	targetType string
}

type lineageProjectionEdgePolicy struct {
	key   lineageProjectionEdgeKey
	kind  string
	label string
}

var lineageProjectionEdges = []lineageProjectionEdgePolicy{
	{
		key:   lineageProjectionEdgeKey{sourceType: "connection", targetType: "source"},
		kind:  "lineage_connection_source",
		label: "Provides source",
	},
	{
		key:   lineageProjectionEdgeKey{sourceType: "source", targetType: "model_table"},
		kind:  "lineage_source_model_table",
		label: "Feeds model table",
	},
	{
		key:   lineageProjectionEdgeKey{sourceType: "model_table", targetType: "semantic_model"},
		kind:  "lineage_model_table_semantic_model",
		label: "Feeds semantic model",
	},
	{
		key:   lineageProjectionEdgeKey{sourceType: "semantic_model", targetType: "dashboard"},
		kind:  "lineage_semantic_model_dashboard",
		label: "Powers dashboard",
	},
}

func lineageProjectionEdge(sourceType, targetType, fallback string) lineageProjectionEdgePolicy {
	key := lineageProjectionEdgeKey{sourceType: sourceType, targetType: targetType}
	for _, policy := range lineageProjectionEdges {
		if policy.key == key {
			return policy
		}
	}
	return lineageProjectionEdgePolicy{
		key:   key,
		kind:  fallback,
		label: labelFromKey(fallback),
	}
}

func lineageCollapsedEdgeKind(sourceType, targetType, fallback string) string {
	return lineageProjectionEdge(sourceType, targetType, fallback).kind
}

func lineageCollapsedEdgeLabel(sourceType, targetType, fallback string) string {
	return lineageProjectionEdge(sourceType, targetType, fallback).label
}

func isRollupLineageAsset(typ string) bool {
	switch typ {
	case "dashboard", "page", "semantic_model":
		return true
	default:
		return false
	}
}

func addContainsContext(selectedID string, graph *assetLineageGraph, nodeIndex map[string]int, assets map[string]projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, addNode func(projectview.DevelopAssetView, int, bool), addEdge func(projectview.DevelopEdgeView)) {
	containsEdges := make([]projectview.DevelopEdgeView, 0)
	for _, edge := range edges {
		if isContainsEdge(edge) {
			containsEdges = append(containsEdges, edge)
		}
	}
	containsEdges = sortedLineageEdges(containsEdges, assets)
	for _, edge := range containsEdges {
		fromIndex, fromOK := nodeIndex[edge.FromAssetID]
		toIndex, toOK := nodeIndex[edge.ToAssetID]
		if !fromOK && !toOK {
			continue
		}
		if fromOK && toOK {
			addEdge(edge)
			continue
		}
		if fromOK && edge.FromAssetID == selectedID {
			asset, ok := assets[edge.ToAssetID]
			if !ok {
				continue
			}
			addNode(asset, int(graph.Nodes[fromIndex].Rank)+1, false)
			addEdge(edge)
			continue
		}
		if toOK {
			asset, ok := assets[edge.FromAssetID]
			if !ok {
				continue
			}
			addNode(asset, int(graph.Nodes[toIndex].Rank)-1, false)
			addEdge(edge)
		}
	}
}

func isLineageDependencyEdge(edge projectview.DevelopEdgeView) bool {
	return !isContainsEdge(edge)
}

func isContainsEdge(edge projectview.DevelopEdgeView) bool {
	return edge.Type == "contains"
}

func lineageEdgeKey(edge projectview.DevelopEdgeView) string {
	if edge.ID != "" {
		return edge.ID
	}
	return edge.FromAssetID + "|" + edge.ToAssetID + "|" + edge.Type
}

func sortedLineageEdges(edges []projectview.DevelopEdgeView, assets map[string]projectview.DevelopAssetView) []projectview.DevelopEdgeView {
	out := append([]projectview.DevelopEdgeView(nil), edges...)
	sort.SliceStable(out, func(i, j int) bool {
		return lineageEdgeSortKey(out[i], assets) < lineageEdgeSortKey(out[j], assets)
	})
	return out
}

func lineageEdgeSortKey(edge projectview.DevelopEdgeView, assets map[string]projectview.DevelopAssetView) string {
	from := assets[edge.FromAssetID]
	to := assets[edge.ToAssetID]
	return edge.Type + ":" + assetTitle(from) + ":" + assetTitle(to) + ":" + lineageEdgeKey(edge)
}

func sortLineageNodes(nodes []assetLineageNode) {
	sort.SliceStable(nodes, func(i, j int) bool {
		left := nodes[i]
		right := nodes[j]
		if left.Rank != right.Rank {
			return left.Rank < right.Rank
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.Label != right.Label {
			return left.Label < right.Label
		}
		return left.ID < right.ID
	})
}

func sortLineageGraphEdges(edges []assetLineageEdge) {
	sort.SliceStable(edges, func(i, j int) bool {
		left := edges[i]
		right := edges[j]
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.Source != right.Source {
			return left.Source < right.Source
		}
		if left.Target != right.Target {
			return left.Target < right.Target
		}
		return left.ID < right.ID
	})
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func lineageAssetHref(projectID string, asset projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) string {
	return assetnav.CanonicalAssetSectionHref(asset, "details")
}

type assetDetailModel struct {
	Overview           []definitionFact
	Sections           []assetDetailSection
	SemanticModelGraph *uisignals.SemanticModelGraphSignal
}

type assetDetailSection struct {
	Title  string
	Signal string
	Table  recordTable
	Facts  []definitionFact
	Code   string
	Lang   string
}

func assetDetailUsesCodeBlock(asset projectview.DevelopAssetView) bool {
	return asset.Type == "model_table" && modelTableSQL(asset.Payload) != ""
}

func assetDetailModelForAsset(project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) assetDetailModel {
	return assetDetailModelForAssetWithRefresh(project, asset, assets, edges, AssetRefreshState{})
}

func assetDetailModelForAssetWithRefresh(project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, refresh AssetRefreshState) assetDetailModel {
	model := assetDetailModel{
		Overview: commonAssetOverviewFacts(asset, assets, shouldShowParentFact(asset.Type)),
	}
	switch asset.Type {
	case "semantic_model":
		semanticModelDetailModel(&model, project, asset, assets, refresh)
	case "model_table":
		modelTableDetailModel(&model, project, asset, assets, refresh)
	case "dashboard":
		dashboardDetailModel(&model, asset, assets)
	case "refresh_pipeline":
		refreshPipelineDetailModel(&model, asset, refresh)
	case "connection":
		connectionDetailModel(&model, project, asset, assets, edges)
	case "source":
		sourceDetailModel(&model, asset)
	case "metric":
		model.Overview = append(model.Overview, metricLeafFacts(asset)...)
	case "field":
		model.Overview = append(model.Overview, metricLeafFacts(asset)...)
	default:
		model.Overview = append(model.Overview, metaFacts(asset.Payload)...)
	}
	return model
}

func refreshPipelineDetailModel(model *assetDetailModel, asset projectview.DevelopAssetView, refresh AssetRefreshState) {
	semanticModel := metaString(asset.Payload, "SemanticModel", "semanticModel")
	schedules := metaSlice(asset.Payload, "Schedules", "schedules")
	lines := make([]string, 0, len(schedules)*2+1)
	for _, raw := range schedules {
		entry, _ := raw.(map[string]any)
		cron := metaString(entry, "Cron", "cron")
		timezone := metaString(entry, "Timezone", "timezone")
		lines = append(lines, "- cron: "+strconv.Quote(cron), "  timezone: "+timezone)
	}
	scheduleYAML := "Manual only"
	if len(lines) > 0 {
		scheduleYAML = "schedule:\n  " + strings.Join(lines, "\n  ")
	}
	nextLabel := "Manual only"
	if !refresh.NextRun.IsZero() {
		nextLabel = refresh.NextRun.Format(time.RFC3339)
	}
	model.Overview = append(model.Overview,
		definitionFact{Label: "Semantic model", Value: semanticModel, Code: true},
		definitionFact{Label: "Schedule", Value: scheduleYAML, Code: true, Wide: true},
		definitionFact{Label: "Next run", Value: nextLabel},
	)
	if refresh.DataVersion.SnapshotID > 0 {
		model.Overview = append(model.Overview,
			definitionFact{Label: "Current data version", Value: fmt.Sprintf("snapshot %d · %s", refresh.DataVersion.SnapshotID, refresh.DataVersion.Source), Code: true},
			definitionFact{Label: "Serving state", Value: refresh.DataVersion.ServingStateID, Code: true},
		)
	}
	model.Overview = append(model.Overview, refreshOverviewFacts(refresh)...)
}

func commonAssetOverviewFacts(asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, includeParent bool) []definitionFact {
	facts := []definitionFact{
		{Label: "Type", Value: assetTypeLabel(asset.Type)},
		{Label: "Key", Value: asset.Key, Code: true},
	}
	if includeParent {
		facts = append(facts, definitionFact{Label: "Parent", Value: assetParentTitle(asset.ParentID, assets)})
	}
	facts = append(facts, definitionFact{Label: "Description", Value: asset.Description, Wide: true})
	return facts
}

func shouldShowParentFact(typ string) bool {
	switch typ {
	case "catalog", "connection", "dashboard", "model_table", "semantic_model":
		return false
	default:
		return true
	}
}

func semanticModelDetailModel(model *assetDetailModel, project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, refresh AssetRefreshState) {
	meta := asset.Payload
	datasetMeta := metaMap(meta, "Datasets")
	datasets := sortedMapKeys(datasetMeta)
	metrics := sortedMapKeys(metaMap(meta, "Metrics"))
	relationships := metaSlice(meta, "Relationships")
	model.SemanticModelGraph = semanticModelGraphSignal(meta)

	model.Overview = append(model.Overview,
		refreshOverviewFacts(refresh)...,
	)
	model.Sections = append(model.Sections,
		assetDetailSection{Title: fmt.Sprintf("Datasets (%d)", len(datasets)), Signal: "assetDetailsSemanticDatasetsTable", Table: semanticDatasetsTable(project.ID, asset, assets, meta, refresh)},
		assetDetailSection{Title: fmt.Sprintf("Metrics (%d)", len(metrics)), Signal: "assetDetailsSemanticMetricsTable", Table: semanticMetricsTable(project.ID, asset, assets, meta)},
		assetDetailSection{Title: fmt.Sprintf("Relationships (%d)", len(relationships)), Signal: "assetDetailsSemanticRelationshipsTable", Table: semanticRelationshipsTable(project.ID, asset, assets, meta)},
	)
}

func refreshOverviewFacts(refresh AssetRefreshState) []definitionFact {
	status := strings.TrimSpace(refresh.Latest.Status)
	if status == "" {
		status = "not refreshed"
	}
	return []definitionFact{
		{Label: "Refresh status", Value: status},
		{Label: "Last refreshed", Value: emptyDash(refresh.LatestSuccessful.FinishedAt)},
	}
}

func semanticFieldCount(tables map[string]any) int {
	count := 0
	for _, tableValue := range tables {
		table := asMap(tableValue)
		count += len(metaMap(table, "Dimensions"))
	}
	return count
}

func assetParentTitle(parentID string, assets []projectview.DevelopAssetView) string {
	if parentID == "" {
		return ""
	}
	for _, asset := range assets {
		if asset.ID == parentID {
			return assetTitle(asset)
		}
	}
	return parentID
}

func semanticConnectionsGrid(projectID string, parent projectview.DevelopAssetView, assets []projectview.DevelopAssetView, meta map[string]any) recordTable {
	connections := metaMap(meta, "Connections")
	rows := make([]map[string]any, 0, len(connections))
	for _, name := range sortedMapKeys(connections) {
		connection := asMap(connections[name])
		child := semanticAssetByName(parent.Key, "connection", name, assets)
		rows = append(rows, map[string]any{
			"name":        name,
			"nameHref":    childHref(projectID, child),
			"kind":        emptyDash(metaString(connection, "Kind")),
			"credentials": recordTableBadgeValue(boolLabel(metaBool(connection, "credentials_configured")), "success"),
			"defaults":    compactJSON(metaValue(connection, "Defaults", "Options")),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "name", Header: "Name", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("nameHref"), Width: uisignals.Pointer("180px")},
			{ID: "kind", Header: "Kind", Width: uisignals.Pointer("120px")},
			{ID: "credentials", Header: "Credentials", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("120px")},
			{ID: "defaults", Header: "Defaults / options", Kind: uisignals.Pointer("expression")},
		},
		Rows:     rows,
		Empty:    "No connections are defined for this semantic model.",
		MinWidth: uisignals.Pointer("760px"),
	}
}

func semanticSourcesGrid(projectID string, parent projectview.DevelopAssetView, assets []projectview.DevelopAssetView, meta map[string]any) recordTable {
	sources := metaMap(meta, "Sources")
	rows := make([]map[string]any, 0, len(sources))
	for _, name := range sortedMapKeys(sources) {
		source := asMap(sources[name])
		child := semanticAssetByName(parent.Key, "source", name, assets)
		rows = append(rows, map[string]any{
			"name":       name,
			"nameHref":   childHref(projectID, child),
			"connection": emptyDash(metaString(source, "Connection")),
			"format":     recordTableBadgeValue(metaString(source, "Format"), "accent"),
			"path":       emptyDash(firstNonEmpty(metaString(source, "Path"), metaString(source, "Object"))),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "name", Header: "Name", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("nameHref"), Width: uisignals.Pointer("180px")},
			{ID: "connection", Header: "Connection", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("150px")},
			{ID: "format", Header: "Format", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("110px")},
			{ID: "path", Header: "Path / object", Kind: uisignals.Pointer("expression")},
		},
		Rows:     rows,
		Empty:    "No sources are defined for this semantic model.",
		MinWidth: uisignals.Pointer("820px"),
	}
}

func semanticDatasetsTable(projectID string, parent projectview.DevelopAssetView, assets []projectview.DevelopAssetView, meta map[string]any, refresh AssetRefreshState) recordTable {
	datasets := metaMap(meta, "Datasets")
	datasetDetails := metaMap(meta, "DatasetDetails")
	metricCounts := semanticMetricCountsByDataset(metaMap(meta, "Metrics"))
	rows := make([]map[string]any, 0, len(datasets))
	lastRefreshed := emptyDash(refresh.LatestSuccessful.FinishedAt)
	refreshStatus := "not refreshed"
	if strings.TrimSpace(refresh.LatestSuccessful.Status) != "" {
		refreshStatus = refresh.LatestSuccessful.Status
	}
	for _, name := range sortedMapKeys(datasets) {
		dataset := asMap(datasets[name])
		details := asMap(datasetDetails[name])
		child := semanticAssetByName(parent.Key, "model_table", name, assets)
		rows = append(rows, map[string]any{
			"name":           name,
			"nameHref":       childHref(projectID, child),
			"model":          emptyDash(metaString(dataset, "Model")),
			"fields":         len(metaMap(details, "Dimensions")),
			"metrics":        metricCounts[name],
			"last_refreshed": lastRefreshed,
			"refresh_status": refreshStatusGridValue(refreshStatus),
			"description":    emptyDash(metaString(dataset, "Description")),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "name", Header: "Name", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("nameHref"), Width: uisignals.Pointer("180px")},
			{ID: "model", Header: "Model", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("150px")},
			{ID: "fields", Header: "Fields", Width: uisignals.Pointer("100px")},
			{ID: "metrics", Header: "Metrics", Width: uisignals.Pointer("110px")},
			{ID: "last_refreshed", Header: "Last refreshed", Width: uisignals.Pointer("180px")},
			{ID: "refresh_status", Header: "Refresh status", Kind: uisignals.Pointer("status"), Width: uisignals.Pointer("130px")},
			{ID: "description", Header: "Description"},
		},
		Rows:     rows,
		Empty:    "No datasets are defined for this semantic model.",
		MinWidth: uisignals.Pointer("1120px"),
	}
}

func semanticMetricCountsByDataset(metrics map[string]any) map[string]int {
	counts := map[string]int{}
	for _, name := range sortedMapKeys(metrics) {
		metric := asMap(metrics[name])
		dataset := metaString(metric, "Dataset")
		if dataset == "" {
			continue
		}
		counts[dataset]++
	}
	return counts
}

func semanticModelGraphSignal(meta map[string]any) *uisignals.SemanticModelGraphSignal {
	datasets := metaMap(meta, "Datasets")
	datasetDetails := metaMap(meta, "DatasetDetails")
	if len(datasets) == 0 {
		return nil
	}
	metrics := metaMap(meta, "Metrics")
	dimensions := metaMap(meta, "Dimensions")
	metricDatasets := semanticModelMetricDatasets(metrics)
	metricCounts := semanticMetricCountsByDataset(metrics)
	conformedCounts := semanticConformedDimensionCounts(dimensions)
	relationships := semanticModelGraphRelationships(meta, datasets)
	joinFields := semanticModelJoinFields(relationships)
	nodes := make([]uisignals.SemanticModelGraphNodeSignal, 0, len(datasets))
	for _, name := range semanticModelGraphDatasetNames(datasets, metricDatasets) {
		dataset := asMap(datasets[name])
		details := asMap(datasetDetails[name])
		badges := []string{}
		if containsString(metricDatasets, name) {
			badges = append(badges, "dataset")
		}
		if semanticModelTableIsDimension(name, relationships) {
			badges = append(badges, "dimension")
		}
		if metricCounts[name] > 0 {
			badges = append(badges, fmt.Sprintf("%d metrics", metricCounts[name]))
		}
		if conformedCounts[name] > 0 {
			badges = append(badges, fmt.Sprintf("%d conformed dimensions", conformedCounts[name]))
		}
		nodes = append(nodes, uisignals.SemanticModelGraphNodeSignal{
			ID:          name,
			Title:       name,
			Description: uisignals.Optional(metaString(dataset, "Description")),
			Entities:    uisignals.OptionalSlice(semanticModelGraphEntities(details)),
			Badges:      uisignals.OptionalSlice(badges),
			Fields:      semanticModelGraphFields(details, joinFields[name]),
			GrainEntity: uisignals.Optional(metaString(details, "GrainEntity")),
		})
	}
	return &uisignals.SemanticModelGraphSignal{
		Datasets: uisignals.OptionalSlice(metricDatasets),
		Nodes:    nodes,
		Edges:    relationships,
	}
}

func semanticModelGraphRelationships(meta map[string]any, datasets map[string]any) []uisignals.SemanticModelGraphEdgeSignal {
	raw := metaSlice(meta, "Relationships")
	edges := make([]uisignals.SemanticModelGraphEdgeSignal, 0, len(raw))
	for _, item := range raw {
		relationship := asMap(item)
		fromTable, fromFields := semanticCompiledRelationshipEndpointMeta(relationship, "From")
		toTable, toFields := semanticCompiledRelationshipEndpointMeta(relationship, "To")
		fromField, toField := strings.Join(fromFields, ", "), strings.Join(toFields, ", ")
		if fromTable == "" || fromField == "" || toTable == "" || toField == "" {
			continue
		}
		if _, ok := datasets[fromTable]; !ok {
			continue
		}
		if _, ok := datasets[toTable]; !ok {
			continue
		}
		id := metaString(relationship, "ID")
		if id == "" {
			id = fromTable + "_" + fromField + "_" + toTable + "_" + toField
		}
		cardinality := metaString(relationship, "Cardinality", "cardinality")
		edges = append(edges, uisignals.SemanticModelGraphEdgeSignal{
			ID:          id,
			Source:      fromTable,
			Target:      toTable,
			SourceField: fromField,
			TargetField: toField,
			Cardinality: cardinality,
			Label:       semanticModelGraphCardinalityLabel(cardinality),
		})
	}
	sort.SliceStable(edges, func(i, j int) bool {
		if edges[i].Source != edges[j].Source {
			return edges[i].Source < edges[j].Source
		}
		if edges[i].Target != edges[j].Target {
			return edges[i].Target < edges[j].Target
		}
		return edges[i].ID < edges[j].ID
	})
	return edges
}

// semanticCompiledRelationshipEndpointMeta reads physical endpoint tuples from
// compiled relationships. Entity endpoints are resolved by the compiler, so
// FromFields/ToFields always contain the ordered physical field tuple rather
// than an entity name (which cannot address a graph handle).
func semanticCompiledRelationshipEndpointMeta(relationship map[string]any, prefix string) (string, []string) {
	return metaString(relationship, prefix+"Dataset"), metaStringSlice(relationship, prefix+"Fields")
}

func semanticModelJoinFields(edges []uisignals.SemanticModelGraphEdgeSignal) map[string]map[string][]string {
	joinFields := map[string]map[string][]string{}
	add := func(table, field, relationship string) {
		if joinFields[table] == nil {
			joinFields[table] = map[string][]string{}
		}
		joinFields[table][field] = append(joinFields[table][field], relationship)
	}
	for _, edge := range edges {
		addEndpointFields(add, edge.Source, edge.SourceField, edge.ID)
		addEndpointFields(add, edge.Target, edge.TargetField, edge.ID)
	}
	for _, fields := range joinFields {
		for field := range fields {
			sort.Strings(fields[field])
		}
	}
	return joinFields
}

func addEndpointFields(add func(string, string, string), table, fields, relationship string) {
	for _, field := range strings.Split(fields, ",") {
		field = strings.TrimSpace(field)
		if field != "" {
			add(table, field, relationship)
		}
	}
}

func semanticModelGraphDatasetNames(datasets map[string]any, metricDatasets []string) []string {
	names := sortedMapKeys(datasets)
	if len(metricDatasets) == 0 {
		return names
	}
	out := make([]string, 0, len(names))
	for _, dataset := range metricDatasets {
		if _, ok := datasets[dataset]; ok {
			out = append(out, dataset)
		}
	}
	for _, name := range names {
		if !containsString(metricDatasets, name) {
			out = append(out, name)
		}
	}
	return out
}

func semanticModelMetricDatasets(metrics map[string]any) []string {
	seen := map[string]bool{}
	for _, metric := range metrics {
		if dataset := metaString(asMap(metric), "Dataset"); dataset != "" {
			seen[dataset] = true
		}
	}
	datasets := make([]string, 0, len(seen))
	for dataset := range seen {
		datasets = append(datasets, dataset)
	}
	sort.Strings(datasets)
	return datasets
}

func semanticConformedDimensionCounts(dimensions map[string]any) map[string]int {
	counts := map[string]int{}
	for _, dimension := range dimensions {
		for dataset := range metaMap(asMap(dimension), "Bindings") {
			counts[dataset]++
		}
	}
	return counts
}

func semanticModelTableIsDimension(table string, edges []uisignals.SemanticModelGraphEdgeSignal) bool {
	for _, edge := range edges {
		if edge.Target == table {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func semanticModelGraphFields(table map[string]any, joins map[string][]string) []uisignals.SemanticModelGraphFieldSignal {
	fields := metaMap(table, "Dimensions")
	columns := modelTableSchemaColumns(fields, metaMap(table, "Schema"))
	entityNames, grainFields := semanticModelGraphFieldIdentity(table)
	seen := map[string]struct{}{}
	out := make([]uisignals.SemanticModelGraphFieldSignal, 0, len(columns)+len(joins))
	for _, column := range columns {
		name := metaString(column, "Name", "name")
		if name == "" {
			continue
		}
		field := asMap(fields[name])
		out = append(out, semanticModelGraphField(name, field, column, entityNames[name], grainFields[name], joins[name]))
		seen[name] = struct{}{}
	}
	for _, name := range sortedMapKeysString(joins) {
		if _, ok := seen[name]; ok {
			continue
		}
		out = append(out, uisignals.SemanticModelGraphFieldSignal{
			Name:          name,
			Label:         uisignals.Optional(labelFromKey(name)),
			Entities:      uisignals.OptionalSlice(entityNames[name]),
			Grain:         uisignals.Optional(grainFields[name]),
			Join:          uisignals.Pointer(true),
			Relationships: uisignals.OptionalSlice(joins[name]),
		})
	}
	return out
}

func semanticModelGraphField(name string, field, column map[string]any, entities []string, grain bool, relationships []string) uisignals.SemanticModelGraphFieldSignal {
	return uisignals.SemanticModelGraphFieldSignal{
		Name:          name,
		Label:         uisignals.Optional(firstNonEmpty(metaString(field, "Label"), labelFromKey(name))),
		Type:          uisignals.Optional(firstNonEmpty(metaString(column, "PhysicalType", "physicalType"), metaString(column, "Type", "type"))),
		Entities:      uisignals.OptionalSlice(entities),
		Grain:         uisignals.Optional(grain),
		Join:          uisignals.Optional(len(relationships) > 0),
		Relationships: uisignals.OptionalSlice(relationships),
	}
}

func semanticModelGraphEntities(table map[string]any) []uisignals.SemanticModelGraphEntitySignal {
	entities := metaMap(table, "Entities")
	grainEntity := metaString(table, "GrainEntity")
	out := make([]uisignals.SemanticModelGraphEntitySignal, 0, len(entities))
	for _, name := range sortedMapKeys(entities) {
		entity := asMap(entities[name])
		out = append(out, uisignals.SemanticModelGraphEntitySignal{
			Name:   name,
			Type:   metaString(entity, "Type"),
			Fields: metaStringSlice(entity, "Fields"),
			Grain:  uisignals.Optional(name == grainEntity),
		})
	}
	return out
}

func semanticModelGraphFieldIdentity(table map[string]any) (map[string][]string, map[string]bool) {
	entityNames := map[string][]string{}
	grainFields := map[string]bool{}
	grainEntity := metaString(table, "GrainEntity")
	for _, entityName := range sortedMapKeys(metaMap(table, "Entities")) {
		entity := asMap(metaMap(table, "Entities")[entityName])
		for _, field := range metaStringSlice(entity, "Fields") {
			entityNames[field] = append(entityNames[field], entityName)
			if entityName == grainEntity {
				grainFields[field] = true
			}
		}
	}
	return entityNames, grainFields
}

func semanticModelGraphCardinalityLabel(cardinality string) string {
	switch strings.ToLower(strings.TrimSpace(cardinality)) {
	case "many_to_one":
		return "*:1"
	case "one_to_one":
		return "1:1"
	default:
		return cardinality
	}
}

func sortedMapKeysString(values map[string][]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func modelTableDetailModel(model *assetDetailModel, project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, refresh AssetRefreshState) {
	modelKey, tableName := modelTableKeyParts(asset)
	fields := modelTableFields(asset.Payload)
	sources := modelTableSourceNames(asset.Payload)
	mode := "Unspecified"
	if modelTableSQL(asset.Payload) != "" {
		mode = "Transform"
	} else if metaString(asset.Payload, "Source", "source") != "" {
		mode = "Direct source"
	}
	entities := metaMap(asset.Payload, "Entities", "entities")
	grainEntity := metaString(asset.Payload, "GrainEntity", "grainEntity")
	model.Overview = append(model.Overview,
		definitionFact{Label: "Grain entity", Value: grainEntity, Code: true},
		definitionFact{Label: "Entities", Value: fmt.Sprint(len(entities))},
		definitionFact{Label: "Fields", Value: fmt.Sprint(len(fields))},
		definitionFact{Label: "Input sources", Value: fmt.Sprint(len(sources))},
		definitionFact{Label: "Mode", Value: mode},
	)
	model.Overview = append(model.Overview, refreshOverviewFacts(refresh)...)
	model.Sections = append(model.Sections,
		assetDetailSection{Title: fmt.Sprintf("Entities (%d)", len(entities)), Signal: "assetDetailsModelTableEntitiesTable", Table: modelTableEntitiesGrid(asset.Payload)},
		assetDetailSection{Title: fmt.Sprintf("Fields (%d)", len(fields)), Signal: "assetDetailsModelTableFieldsTable", Table: modelTableFieldsGrid(project.ID, modelKey, tableName, asset.Payload, assets)},
	)
	if sql := modelTableSQL(asset.Payload); sql != "" {
		model.Sections = append(model.Sections, assetDetailSection{Title: "SQL", Lang: "sql", Code: sql})
	}
}

func modelTableKeyParts(asset projectview.DevelopAssetView) (string, string) {
	parts := strings.SplitN(asset.Key, ".", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", asset.Key
}

func modelTableFields(meta map[string]any) map[string]any {
	return metaMap(meta, "Dimensions", "dimensions", "Fields", "fields")
}

func sourceDetailModel(model *assetDetailModel, asset projectview.DevelopAssetView) {
	fields := metaMap(asset.Payload, "Fields", "fields")
	schema := metaMap(asset.Payload, "Schema", "schema")
	columns := modelTableSchemaColumns(fields, schema)
	model.Overview = append(model.Overview, sourceFacts(asset)...)
	model.Overview = append(model.Overview, definitionFact{Label: "Fields", Value: fmt.Sprint(len(columns))})
	model.Sections = append(model.Sections,
		assetDetailSection{Title: fmt.Sprintf("Fields (%d)", len(columns)), Signal: "assetDetailsSourceFieldsTable", Table: sourceFieldsGrid(fields, schema)},
	)
}

func modelTableSourceNames(meta map[string]any) []string {
	if source := metaString(meta, "Source", "source"); source != "" {
		return []string{source}
	}
	for _, value := range []any{
		metaValue(meta, "SourceDependencies", "source_dependencies"),
		metaValue(meta, "Sources", "sources"),
	} {
		sources := stringSlice(value)
		if len(sources) > 0 {
			sort.Strings(sources)
			return sources
		}
	}
	return nil
}

func modelTableSQL(meta map[string]any) string {
	return firstNonEmpty(
		metaString(metaMap(meta, "Transform", "transform"), "SQL", "sql"),
		metaString(meta, "SQL", "sql"),
	)
}

func modelTableFieldsGrid(projectID, modelKey, tableName string, table map[string]any, assets []projectview.DevelopAssetView) recordTable {
	fields := modelTableFields(table)
	schema := metaMap(table, "Schema", "schema")
	schemaColumns := modelTableSchemaColumns(fields, schema)
	entityNames, grainFields := semanticModelGraphFieldIdentity(table)
	rows := make([]map[string]any, 0, len(schemaColumns))
	for _, column := range schemaColumns {
		name := metaString(column, "Name", "name")
		field := asMap(fields[name])
		child := assetByTypeKey("field", modelKey+"."+tableName+"."+name, assets)
		rows = append(rows, map[string]any{
			"name":          name,
			"nameHref":      childHref(projectID, child),
			"label":         firstNonEmpty(metaString(field, "Label", "label"), labelFromKey(name)),
			"physical_type": recordTableBadgeValue(metaString(column, "PhysicalType", "physicalType"), "muted"),
			"nullable":      nullableLabel(column, "Nullable", "nullable"),
			"entities":      emptyDash(strings.Join(entityNames[name], ", ")),
			"grain":         boolLabel(grainFields[name]),
			"description":   emptyDash(metaString(field, "Description", "description")),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "name", Header: "Name", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("nameHref"), Width: uisignals.Pointer("170px")},
			{ID: "label", Header: "Label", Width: uisignals.Pointer("180px")},
			{ID: "physical_type", Header: "Physical type", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("140px")},
			{ID: "nullable", Header: "Nullable", Width: uisignals.Pointer("100px")},
			{ID: "entities", Header: "Entities", Width: uisignals.Pointer("180px")},
			{ID: "grain", Header: "Grain", Width: uisignals.Pointer("90px")},
			{ID: "description", Header: "Description"},
		},
		Rows:     rows,
		Empty:    "No schema is available for this model table.",
		MinWidth: uisignals.Pointer("900px"),
	}
}

func modelTableEntitiesGrid(table map[string]any) recordTable {
	entities := semanticModelGraphEntities(table)
	rows := make([]map[string]any, 0, len(entities))
	for _, entity := range entities {
		rows = append(rows, map[string]any{
			"name":   entity.Name,
			"type":   recordTableBadgeValue(entity.Type, "muted"),
			"fields": strings.Join(entity.Fields, ", "),
			"grain":  boolLabel(entity.Grain != nil && *entity.Grain),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "name", Header: "Name", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("180px")},
			{ID: "type", Header: "Type", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("120px")},
			{ID: "fields", Header: "Ordered key fields"},
			{ID: "grain", Header: "Grain", Width: uisignals.Pointer("90px")},
		},
		Rows:     rows,
		Empty:    "No entities are defined for this model.",
		MinWidth: uisignals.Pointer("720px"),
	}
}

func sourceFieldsGrid(fields, schema map[string]any) recordTable {
	schemaColumns := modelTableSchemaColumns(fields, schema)
	rows := make([]map[string]any, 0, len(schemaColumns))
	for _, column := range schemaColumns {
		name := metaString(column, "Name", "name")
		field := asMap(fields[name])
		rows = append(rows, map[string]any{
			"name":          name,
			"description":   emptyDash(metaString(field, "Description", "description")),
			"physical_type": recordTableBadgeValue(metaString(column, "PhysicalType", "physicalType"), "muted"),
			"nullable":      nullableLabel(column, "Nullable", "nullable"),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "name", Header: "Name", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("170px")},
			{ID: "description", Header: "Description"},
			{ID: "physical_type", Header: "Physical type", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("140px")},
			{ID: "nullable", Header: "Nullable", Width: uisignals.Pointer("100px")},
		},
		Rows:     rows,
		Empty:    "No schema is available for this source.",
		MinWidth: uisignals.Pointer("900px"),
	}
}

func modelTableSchemaColumns(fields map[string]any, schema map[string]any) []map[string]any {
	if schema != nil {
		if raw := metaSlice(schema, "Columns", "columns"); len(raw) > 0 {
			columns := make([]map[string]any, 0, len(raw))
			for _, item := range raw {
				columns = append(columns, asMap(item))
			}
			sort.Slice(columns, func(i, j int) bool {
				return metaInt(columns[i], "Ordinal", "ordinal") < metaInt(columns[j], "Ordinal", "ordinal")
			})
			return columns
		}
	}
	columns := make([]map[string]any, 0, len(fields))
	for _, name := range sortedMapKeys(fields) {
		columns = append(columns, map[string]any{"name": name})
	}
	return columns
}

func semanticFieldsGrid(projectID string, parent projectview.DevelopAssetView, assets []projectview.DevelopAssetView, meta map[string]any) recordTable {
	datasets := metaMap(meta, "Datasets")
	datasetDetails := metaMap(meta, "DatasetDetails")
	rows := []map[string]any{}
	for _, datasetName := range sortedMapKeys(datasets) {
		dataset := asMap(datasetDetails[datasetName])
		fields := metaMap(dataset, "Dimensions")
		for _, fieldName := range sortedMapKeys(fields) {
			field := asMap(fields[fieldName])
			key := parent.Key + "." + datasetName + "." + fieldName
			child := assetByTypeKey("field", key, assets)
			rows = append(rows, map[string]any{
				"name":       fieldName,
				"nameHref":   childHref(projectID, child),
				"dataset":    datasetName,
				"expression": datasetName + "." + fieldName,
				"type":       recordTableBadgeValue(metaString(field, "Type"), "muted"),
				"filter":     emptyDash(metaString(field, "Where")),
			})
		}
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "name", Header: "Name", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("nameHref"), Width: uisignals.Pointer("170px")},
			{ID: "dataset", Header: "Dataset", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("150px")},
			{ID: "expression", Header: "Expression", Kind: uisignals.Pointer("expression"), Width: uisignals.Pointer("260px")},
			{ID: "type", Header: "Type", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("110px")},
			{ID: "filter", Header: "Filter", Kind: uisignals.Pointer("expression"), Width: uisignals.Pointer("220px")},
		},
		Rows:     rows,
		Empty:    "No fields are defined for this semantic model.",
		MinWidth: uisignals.Pointer("1100px"),
	}
}

func semanticMetricsTable(projectID string, parent projectview.DevelopAssetView, assets []projectview.DevelopAssetView, meta map[string]any) recordTable {
	metrics := metaMap(meta, "Metrics", "metrics")
	rows := make([]map[string]any, 0, len(metrics))
	for _, name := range sortedMapKeys(metrics) {
		metric := asMap(metrics[name])
		input := metaMap(metric, "Input", "input")
		child := childAssetByName(parent.ID, "metric", name, assets)
		rows = append(rows, map[string]any{
			"name":        name,
			"nameHref":    childHref(projectID, child),
			"dataset":     emptyDash(metaString(metric, "Dataset", "dataset")),
			"aggregation": recordTableBadgeValue(firstNonEmpty(metaString(metric, "Aggregation", "aggregation"), metaString(metric, "Type", "type")), "muted"),
			"input":       firstNonEmpty(metaString(input, "Field", "field"), metaString(metric, "Expression", "expression")),
			"format":      recordTableBadgeValue(metaString(metric, "Format", "format"), "accent"),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "name", Header: "Name", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("nameHref"), Width: uisignals.Pointer("160px")},
			{ID: "dataset", Header: "Dataset", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("140px")},
			{ID: "aggregation", Header: "Aggregation", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("120px")},
			{ID: "input", Header: "Input", Kind: uisignals.Pointer("expression")},
			{ID: "format", Header: "Format", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("100px")},
		},
		Rows:     rows,
		Empty:    "No metrics are defined for this semantic model.",
		MinWidth: uisignals.Pointer("900px"),
	}
}

func semanticRelationshipsTable(projectID string, parent projectview.DevelopAssetView, assets []projectview.DevelopAssetView, meta map[string]any) recordTable {
	relationships := metaSlice(meta, "Relationships")
	rows := make([]map[string]any, 0, len(relationships))
	for _, item := range relationships {
		relationship := asMap(item)
		id := metaString(relationship, "ID", "id")
		child := semanticAssetByName(parent.Key, "relationship", id, assets)
		fromTable, fromFields := semanticCompiledRelationshipEndpointMeta(relationship, "From")
		toTable, toFields := semanticCompiledRelationshipEndpointMeta(relationship, "To")
		cardinality := metaString(relationship, "Cardinality", "cardinality")
		fromField, toField := strings.Join(fromFields, ", "), strings.Join(toFields, ", ")
		rows = append(rows, map[string]any{
			"id":          id,
			"idHref":      childHref(projectID, child),
			"from_table":  emptyDash(fromTable),
			"from_field":  emptyDash(fromField),
			"to_table":    emptyDash(toTable),
			"to_field":    emptyDash(toField),
			"cardinality": recordTableBadgeValue(cardinality, "muted"),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "id", Header: "ID", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("idHref"), Width: uisignals.Pointer("180px")},
			{ID: "from_table", Header: "From dataset", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("140px")},
			{ID: "from_field", Header: "From field", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("160px")},
			{ID: "to_table", Header: "To dataset", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("140px")},
			{ID: "to_field", Header: "To field", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("160px")},
			{ID: "cardinality", Header: "Cardinality", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("140px")},
		},
		Rows:     rows,
		Empty:    "No relationships are defined for this semantic model.",
		MinWidth: uisignals.Pointer("920px"),
	}
}

func splitSemanticFieldRef(ref string) (string, string) {
	parts := strings.SplitN(strings.TrimSpace(ref), ".", 2)
	if len(parts) != 2 {
		return strings.TrimSpace(ref), ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func dashboardDetailModel(model *assetDetailModel, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView) {
	pages := childrenByType(asset.ID, "page", assets)
	filters := childrenByType(asset.ID, "filter", assets)
	visuals := childrenByType(asset.ID, "visual", assets)
	model.Overview = append(model.Overview,
		definitionFact{Label: "Semantic model", Value: metaString(asset.Payload, "SemanticModel", "semantic_model")},
		definitionFact{Label: "Tags", Value: strings.Join(stringSlice(metaValue(asset.Payload, "Tags", "tags")), ", ")},
	)
	model.Sections = append(model.Sections,
		assetDetailSection{Title: fmt.Sprintf("Pages (%d)", len(pages)), Signal: "assetDetailsPagesTable", Table: dashboardPagesTable(asset, pages)},
		assetDetailSection{Title: fmt.Sprintf("Filters (%d)", len(filters)), Signal: "assetDetailsFiltersTable", Table: dashboardFiltersTable(asset, filters)},
		assetDetailSection{Title: fmt.Sprintf("Visuals (%d)", len(visuals)), Signal: "assetDetailsVisualsTable", Table: dashboardVisualsTable(asset, visuals)},
	)
}

func dashboardPagesTable(parent projectview.DevelopAssetView, pages []projectview.DevelopAssetView) recordTable {
	rows := make([]map[string]any, 0, len(pages))
	for _, page := range pages {
		key := assetChildName(parent, page)
		rows = append(rows, map[string]any{
			"page":        assetTitle(page),
			"pageHref":    assetnav.ProjectAssetSectionHref(page.ID, "details"),
			"key":         key,
			"description": emptyDash(page.Description),
			"runtime":     "Open",
			"runtimeHref": "/dashboards/" + parent.Key + "/pages/" + key,
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "page", Header: "Page", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("pageHref"), Width: uisignals.Pointer("220px")},
			{ID: "key", Header: "Key", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("190px")},
			{ID: "description", Header: "Description"},
			{ID: "runtime", Header: "Runtime", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("runtimeHref"), Width: uisignals.Pointer("110px")},
		},
		Rows:     rows,
		Empty:    "No pages are defined for this dashboard.",
		MinWidth: uisignals.Pointer("860px"),
	}
}

func dashboardFiltersTable(parent projectview.DevelopAssetView, filters []projectview.DevelopAssetView) recordTable {
	sortAssetChildren(parent, filters)
	rows := make([]map[string]any, 0, len(filters))
	for _, filter := range filters {
		rows = append(rows, map[string]any{
			"filter":     assetTitle(filter),
			"filterHref": assetnav.ProjectAssetSectionHref(filter.ID, "details"),
			"key":        assetChildName(parent, filter),
			"field":      emptyDash(metaString(filter.Payload, "Dimension", "dimension", "Field", "field")),
			"type":       emptyDash(metaString(filter.Payload, "Type", "type", "Kind", "kind")),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "filter", Header: "Filter", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("filterHref"), Width: uisignals.Pointer("190px")},
			{ID: "key", Header: "Key", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("160px")},
			{ID: "field", Header: "Field", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("220px")},
			{ID: "type", Header: "Type", Width: uisignals.Pointer("120px")},
		},
		Rows:     rows,
		Empty:    "No filters are defined for this dashboard.",
		MinWidth: uisignals.Pointer("820px"),
	}
}

func dashboardVisualsTable(parent projectview.DevelopAssetView, visuals []projectview.DevelopAssetView) recordTable {
	sortAssetChildren(parent, visuals)
	rows := make([]map[string]any, 0, len(visuals))
	for _, visual := range visuals {
		query := metaMap(visual.Payload, "Query", "query")
		rows = append(rows, map[string]any{
			"visual":     assetTitle(visual),
			"visualHref": assetnav.ProjectAssetSectionHref(visual.ID, "details"),
			"key":        assetChildName(parent, visual),
			"type":       emptyDash(firstNonEmpty(metaString(visual.Payload, "Type", "type"), metaString(visual.Payload, "Shape", "shape"))),
			"metrics":    emptyDash(strings.Join(stringSlice(metaValue(query, "Metrics", "metrics")), ", ")),
			"dimensions": emptyDash(strings.Join(stringSlice(metaValue(query, "Dimensions", "dimensions")), ", ")),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "visual", Header: "Visual", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("visualHref"), Width: uisignals.Pointer("230px")},
			{ID: "key", Header: "Key", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("180px")},
			{ID: "type", Header: "Type", Width: uisignals.Pointer("120px")},
			{ID: "metrics", Header: "Metrics", Kind: uisignals.Pointer("expression"), Width: uisignals.Pointer("220px")},
			{ID: "dimensions", Header: "Dimensions", Kind: uisignals.Pointer("expression")},
		},
		Rows:     rows,
		Empty:    "No visuals are defined for this dashboard.",
		MinWidth: uisignals.Pointer("1040px"),
	}
}

func connectionDetailModel(model *assetDetailModel, project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) {
	sources := sourcesUsingConnection(asset.ID, assets, edges)
	model.Overview = append(model.Overview, connectionFacts(asset)...)
	model.Overview = append(model.Overview, definitionFact{Label: "Sources", Value: fmt.Sprint(len(sources))})
	model.Sections = append(model.Sections,
		assetDetailSection{
			Title:  fmt.Sprintf("Sources (%d)", len(sources)),
			Signal: "assetDetailsConnectionSourcesTable",
			Table:  connectionSourcesGrid(project.ID, sources, edges),
		},
	)
}

func connectionSourcesGrid(projectID string, sources []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) recordTable {
	sort.SliceStable(sources, func(i, j int) bool {
		return strings.ToLower(assetTitle(sources[i])) < strings.ToLower(assetTitle(sources[j]))
	})
	rows := make([]map[string]any, 0, len(sources))
	for _, source := range sources {
		fields := metaMap(source.Payload, "Fields", "fields")
		schema := metaMap(source.Payload, "Schema", "schema")
		rows = append(rows, map[string]any{
			"source":     assetTitle(source),
			"sourceHref": assetnav.CanonicalAssetSectionHref(source, "details"),
			"format":     emptyDash(metaString(source.Payload, "Format", "format")),
			"path":       emptyDash(metaString(source.Payload, "Path", "path")),
			"fields":     len(modelTableSchemaColumns(fields, schema)),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "source", Header: "Source", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("sourceHref"), Width: uisignals.Pointer("240px")},
			{ID: "format", Header: "Format", Width: uisignals.Pointer("140px")},
			{ID: "path", Header: "Path", Kind: uisignals.Pointer("code")},
			{ID: "fields", Header: "Fields", Align: uisignals.Pointer("right"), Width: uisignals.Pointer("100px")},
		},
		Rows:     rows,
		Empty:    "No sources use this connection.",
		MinWidth: uisignals.Pointer("760px"),
	}
}

func sourcesUsingConnection(connectionID string, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) []projectview.DevelopAssetView {
	byID := assetsByID(assets)
	sources := []projectview.DevelopAssetView{}
	seen := map[string]struct{}{}
	for _, edge := range edges {
		if edge.Type != "uses_connection" || edge.ToAssetID != connectionID {
			continue
		}
		source, ok := byID[edge.FromAssetID]
		if !ok || source.Type != "source" {
			continue
		}
		if _, ok := seen[source.ID]; ok {
			continue
		}
		seen[source.ID] = struct{}{}
		sources = append(sources, source)
	}
	sort.Slice(sources, func(i, j int) bool {
		return assetTitle(sources[i]) < assetTitle(sources[j])
	})
	return sources
}

func connectionFacts(asset projectview.DevelopAssetView) []definitionFact {
	options := compactJSON(metaValue(asset.Payload, "Options", "options"))
	if options == "" {
		options = compactJSON(metaValue(asset.Payload, "Defaults", "defaults"))
	}
	return []definitionFact{
		{Label: "Kind", Value: metaString(asset.Payload, "Kind", "kind")},
		{Label: "Scope", Value: metaString(asset.Payload, "Scope", "scope")},
		{Label: "Root", Value: metaString(asset.Payload, "Root", "root")},
		{Label: "Path", Value: metaString(asset.Payload, "Path", "path")},
		{Label: "Credentials", Value: boolLabel(metaBool(asset.Payload, "credentials_configured"))},
		{Label: "Options", Value: options},
	}
}

func sourceFacts(asset projectview.DevelopAssetView) []definitionFact {
	return []definitionFact{
		{Label: "Connection", Value: metaString(asset.Payload, "Connection", "connection")},
		{Label: "Format", Value: metaString(asset.Payload, "Format", "format")},
		{Label: "Path", Value: metaString(asset.Payload, "Path", "path")},
		{Label: "Object", Value: metaString(asset.Payload, "Object", "object")},
		{Label: "Options", Value: compactJSON(metaValue(asset.Payload, "Options", "options"))},
	}
}

func metricLeafFacts(asset projectview.DevelopAssetView) []definitionFact {
	facts := []definitionFact{}
	for _, key := range []string{"Expression", "Where", "Unit", "Format"} {
		if value := metaString(asset.Payload, key); strings.TrimSpace(value) != "" {
			facts = append(facts, definitionFact{Label: labelFromKey(key), Value: value, Code: strings.Contains(strings.ToLower(key), "expr") || strings.EqualFold(key, "expression")})
		}
	}
	return facts
}

type definitionFact struct {
	Label string
	Value string
	Code  bool
	Wide  bool
}

func childAssetGrid(projectID string, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, empty string) recordTable {
	sort.Slice(assets, func(i, j int) bool {
		return assetTitle(assets[i]) < assetTitle(assets[j])
	})
	rows := make([]map[string]any, 0, len(assets))
	for _, asset := range assets {
		rows = append(rows, map[string]any{
			"name":        assetTitle(asset),
			"nameHref":    assetnav.CanonicalAssetSectionHref(asset, "details"),
			"key":         asset.Key,
			"type":        assetTypeLabel(asset.Type),
			"description": emptyDash(asset.Description),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "name", Header: "Name", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("nameHref"), Width: uisignals.Pointer("220px")},
			{ID: "key", Header: "Key", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("220px")},
			{ID: "type", Header: "Type", Width: uisignals.Pointer("150px")},
			{ID: "description", Header: "Description"},
		},
		Rows:     rows,
		Empty:    empty,
		MinWidth: uisignals.Pointer("860px"),
	}
}

func childDependencyGrid(projectID, assetID string, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) recordTable {
	byID := assetsByID(assets)
	rows := []map[string]any{}
	for _, edge := range edges {
		if edge.FromAssetID != assetID && edge.ToAssetID != assetID {
			continue
		}
		peerID := edge.ToAssetID
		direction := recordTableBadge{Label: "Outgoing", Tone: uisignals.Pointer("accent")}
		if edge.ToAssetID == assetID {
			peerID = edge.FromAssetID
			direction = recordTableBadge{Label: "Incoming", Tone: uisignals.Pointer("muted")}
		}
		peer, ok := byID[peerID]
		if !ok {
			continue
		}
		rows = append(rows, map[string]any{
			"direction": direction,
			"relation":  labelFromKey(edge.Type),
			"asset":     assetTitle(peer),
			"assetHref": assetnav.CanonicalAssetSectionHref(peer, "details"),
			"type":      assetTypeLabel(peer.Type),
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		return fmt.Sprint(rows[i]["relation"], rows[i]["asset"]) < fmt.Sprint(rows[j]["relation"], rows[j]["asset"])
	})
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "direction", Header: "Direction", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("120px")},
			{ID: "relation", Header: "Relationship", Width: uisignals.Pointer("180px")},
			{ID: "asset", Header: "Asset", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("assetHref"), Width: uisignals.Pointer("240px")},
			{ID: "type", Header: "Type", Width: uisignals.Pointer("140px")},
		},
		Rows:     rows,
		Empty:    "No direct dependencies for this asset.",
		MinWidth: uisignals.Pointer("720px"),
	}
}

func metaFacts(meta map[string]any) []definitionFact {
	keys := make([]string, 0, len(meta))
	for key := range meta {
		if assetDefinitionDetailKey(key) {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	facts := make([]definitionFact, 0, len(keys))
	for _, key := range keys {
		facts = append(facts, definitionFact{Label: labelFromKey(key), Value: assetDefinitionValue(meta[key]), Code: looksLikeCodeKey(key)})
	}
	return facts
}

func assetDefinitionDetailKey(key string) bool {
	switch strings.ToLower(key) {
	case "description", "id", "name", "title", "auth":
		return true
	default:
		return false
	}
}

func assetDefinitionValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		if data, err := json.MarshalIndent(typed, "", "  "); err == nil {
			return string(data)
		}
		return fmt.Sprint(value)
	}
}

func looksLikeCodeKey(key string) bool {
	key = strings.ToLower(key)
	return strings.Contains(key, "expr") || strings.Contains(key, "sql")
}

func asMap(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return map[string]any{}
}

func metaValue(meta map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := meta[key]; ok {
			return value
		}
	}
	return nil
}

func metaMap(meta map[string]any, keys ...string) map[string]any {
	return asMap(metaValue(meta, keys...))
}

func metaSlice(meta map[string]any, keys ...string) []any {
	if typed, ok := metaValue(meta, keys...).([]any); ok {
		return typed
	}
	return nil
}

func metaStringSlice(meta map[string]any, keys ...string) []string {
	value := metaValue(meta, keys...)
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if value, ok := item.(string); ok && strings.TrimSpace(value) != "" {
				out = append(out, strings.TrimSpace(value))
			}
		}
		return out
	default:
		return nil
	}
}

func metaString(meta map[string]any, keys ...string) string {
	value := metaValue(meta, keys...)
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case float64:
		return fmt.Sprintf("%g", typed)
	default:
		return compactJSON(typed)
	}
}

func metaBool(meta map[string]any, keys ...string) bool {
	switch typed := metaValue(meta, keys...).(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(typed, "true") || strings.EqualFold(typed, "yes")
	default:
		return false
	}
}

func metaInt(meta map[string]any, keys ...string) int {
	switch typed := metaValue(meta, keys...).(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func sortedMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func compactJSON(value any) string {
	if value == nil {
		return ""
	}
	bytes, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	text := string(bytes)
	if text == "null" || text == "{}" || text == "[]" {
		return ""
	}
	return text
}

func boolLabel(value bool) string {
	if value {
		return "Yes"
	}
	return "No"
}

func nullableLabel(meta map[string]any, keys ...string) string {
	value := metaValue(meta, keys...)
	if value == nil {
		return "-"
	}
	switch typed := value.(type) {
	case bool:
		return boolLabel(typed)
	case string:
		if strings.EqualFold(typed, "true") || strings.EqualFold(typed, "yes") {
			return "Yes"
		}
		if strings.EqualFold(typed, "false") || strings.EqualFold(typed, "no") {
			return "No"
		}
	}
	return "-"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func childAssetByName(parentID, typ, name string, assets []projectview.DevelopAssetView) projectview.DevelopAssetView {
	for _, asset := range assets {
		if asset.ParentID != parentID || asset.Type != typ {
			continue
		}
		if asset.Title == name || asset.Key == name || strings.HasSuffix(asset.Key, "."+name) {
			return asset
		}
	}
	return projectview.DevelopAssetView{}
}

func semanticAssetByName(modelKey, typ, name string, assets []projectview.DevelopAssetView) projectview.DevelopAssetView {
	key := modelKey + "." + name
	if asset := assetByTypeKey(typ, key, assets); asset.ID != "" {
		return asset
	}
	for _, asset := range assets {
		if asset.Type != typ {
			continue
		}
		if asset.Title == name || asset.Key == name || strings.HasSuffix(asset.Key, "."+name) {
			return asset
		}
	}
	return projectview.DevelopAssetView{}
}

func assetByTypeKey(typ, key string, assets []projectview.DevelopAssetView) projectview.DevelopAssetView {
	for _, asset := range assets {
		if asset.Type == typ && asset.Key == key {
			return asset
		}
	}
	return projectview.DevelopAssetView{}
}

func childrenByType(parentID, typ string, assets []projectview.DevelopAssetView) []projectview.DevelopAssetView {
	out := []projectview.DevelopAssetView{}
	for _, asset := range assets {
		if asset.ParentID == parentID && asset.Type == typ {
			out = append(out, asset)
		}
	}
	return out
}

func metricChildName(parent, child projectview.DevelopAssetView) string {
	return assetChildName(parent, child)
}

func assetChildName(parent, child projectview.DevelopAssetView) string {
	prefix := parent.Key + "."
	if strings.HasPrefix(child.Key, prefix) {
		return strings.TrimPrefix(child.Key, prefix)
	}
	if child.Key != "" {
		return child.Key
	}
	return assetTitle(child)
}

func sortAssetChildren(parent projectview.DevelopAssetView, children []projectview.DevelopAssetView) {
	sort.Slice(children, func(i, j int) bool {
		left := assetChildName(parent, children[i])
		right := assetChildName(parent, children[j])
		if left == right {
			return assetTitle(children[i]) < assetTitle(children[j])
		}
		return left < right
	})
}

func childHref(projectID string, asset projectview.DevelopAssetView) string {
	if asset.ID == "" {
		return ""
	}
	return assetnav.CanonicalAssetSectionHref(asset, "details")
}

func assetsByID(assets []projectview.DevelopAssetView) map[string]projectview.DevelopAssetView {
	byID := map[string]projectview.DevelopAssetView{}
	for _, asset := range assets {
		byID[asset.ID] = asset
	}
	return byID
}

func dependentAssetNames(assetID, edgeType string, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) []string {
	byID := assetsByID(assets)
	names := []string{}
	for _, edge := range edges {
		if edge.FromAssetID != assetID || edge.Type != edgeType {
			continue
		}
		if asset, ok := byID[edge.ToAssetID]; ok {
			names = append(names, assetTitle(asset))
		}
	}
	sort.Strings(names)
	return names
}

func assetTitle(asset projectview.DevelopAssetView) string {
	return displayLabel(asset.Title, asset.Key)
}

func assetTypeLabel(typ string) string {
	switch typ {
	case "semantic_model":
		return "Semantic model"
	case "semantic_table":
		return "Semantic table"
	case "model_table":
		return "Model table"
	case "page_item":
		return "Page item"
	case "refresh_pipeline":
		return "Refresh pipeline"
	default:
		return strings.Title(strings.ReplaceAll(typ, "_", " "))
	}
}

func labelFromKey(key string) string {
	switch key {
	case "reads_source":
		return "Reads source"
	case "uses_connection":
		return "Uses connection"
	case "uses_field":
		return "Uses field"
	case "filters_field":
		return "Filters field"
	case "uses_filter":
		return "Uses filter"
	case "uses_model_table":
		return "Uses model table"
	case "uses_metric":
		return "Uses metric"
	case "uses_semantic_model":
		return "Uses semantic model"
	case "uses_semantic_table":
		return "Uses semantic table"
	case "uses_table":
		return "Uses table"
	case "uses_visual":
		return "Uses visual"
	case "refreshes_semantic_model":
		return "Refreshes semantic model"
	}
	return strings.Title(strings.ReplaceAll(key, "_", " "))
}
