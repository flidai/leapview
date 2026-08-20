package ui

import (
	"sort"
	"strings"

	uiactions "github.com/flidai/leapview/internal/platform/web/actions"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectview "github.com/flidai/leapview/internal/project"
	"github.com/flidai/leapview/internal/project/assetnav"
	catalog "github.com/flidai/leapview/internal/project/navigation"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
	g "maragu.dev/gomponents"
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
