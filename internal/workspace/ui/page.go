package ui

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/flidai/leapview/internal/dashboard"
	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	uiactions "github.com/flidai/leapview/internal/platform/web/actions"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	workspacegen "github.com/flidai/leapview/internal/workspace/api/gen"
	catalog "github.com/flidai/leapview/internal/workspace/navigation"
	uisignals "github.com/flidai/leapview/internal/workspace/ui/signals"
	g "maragu.dev/gomponents"
)

func updatesURL(route uisignals.RouteKind, pairs ...string) string {
	values := url.Values{}
	values.Set("route", string(route))
	for i := 0; i+1 < len(pairs); i += 2 {
		if strings.TrimSpace(pairs[i+1]) == "" {
			continue
		}
		values.Set(pairs[i], pairs[i+1])
	}
	return "/updates?" + values.Encode()
}

func runtimeSignal(kind uisignals.RouteKind) uisignals.RouteRuntimeSignal {
	return uisignals.RouteRuntimeSignal{
		Kind: kind,
	}
}

func CatalogPage(catalog catalog.Catalog, providers ...webpage.Provider) g.Node {
	return CatalogPageForQuery(catalog, "", providers...)
}

func CatalogPageForCatalogs(catalogs []catalog.Catalog, providers ...webpage.Provider) g.Node {
	return CatalogPageForCatalogsQuery(catalogs, "", providers...)
}

func CatalogPageForQuery(catalog catalog.Catalog, query string, providers ...webpage.Provider) g.Node {
	return catalogPageDocument(catalog, catalogPageSignal(catalog, query), "", providers...)
}

func CatalogPageForCatalogsQuery(catalogs []catalog.Catalog, query string, providers ...webpage.Provider) g.Node {
	return CatalogPageForCatalogsQueryWithCSRF(catalogs, query, "", providers...)
}

func CatalogPageForCatalogsQueryWithCSRF(catalogs []catalog.Catalog, query, csrfToken string, providers ...webpage.Provider) g.Node {
	return CatalogPageForCatalogsWithOptions(catalogs, CatalogListOptions{Query: query}, csrfToken, providers...)
}

type CatalogDashboardMetadata struct {
	Popularity      uisignals.PopularityLevel
	LastRefreshedAt string
}

type CatalogListOptions struct {
	Query           string
	WorkspaceFilter string
	Metadata        map[string]CatalogDashboardMetadata
}

func CatalogPageForCatalogsWithOptions(catalogs []catalog.Catalog, options CatalogListOptions, csrfToken string, providers ...webpage.Provider) g.Node {
	if len(catalogs) == 0 {
		return catalogPageDocument(catalog.Catalog{}, catalogPageForCatalogs(catalogs, options), csrfToken, providers...)
	}
	return catalogPageDocument(catalogs[0], catalogPageForCatalogs(catalogs, options), csrfToken, providers...)
}

func catalogPageDocument(catalog catalog.Catalog, page uisignals.CatalogPageSignal, csrfToken string, providers ...webpage.Provider) g.Node {
	layout := webpage.Resolve(firstProvider(providers), catalogLayoutContext(catalog))
	catalogUpdatesURL := updatesURL(uisignals.RouteDashboard, "q", uisignals.ValueOrZero(page.ListQuery))
	title := "Dashboards"
	if productName := strings.TrimSpace(layout.Presentation.ProductName); productName != "" {
		title = productName + " Dashboards"
	}
	return webpage.Render(layout, webpage.Spec{
		Title: title, CSRFToken: csrfToken, Scripts: []string{"/static/catalog-page.js"},
		UpdatesURL: catalogUpdatesURL,
		Content: g.El("lv-catalog-page",
			g.Attr("slot", "page"),
			g.Attr("data-on:lv-entity-list-query__debounce.200ms", "$entityListQuery = evt.detail.query; $entityListFilter = evt.detail.filter; "+uiactions.QueryPost("/catalog/search", "entityListQuery", "entityListFilter")),
			g.Attr("data-on:lv-dashboard-appearance-change", "$dashboardAppearanceCommand = evt.detail; "+uiactions.CommandPostWorkspace(workspacegen.GenUIActionUpdateDashboardAppearance(), "/catalog/appearance", "$dashboardAppearanceCommand.workspaceId", "dashboardAppearanceCommand", "entityListQuery")),
		),
	})
}

func CatalogListPatchForCatalogsQuery(catalogs []catalog.Catalog, query string) map[string]any {
	return CatalogListPatchForCatalogs(catalogs, CatalogListOptions{Query: query})
}

func CatalogListPatchForCatalogs(catalogs []catalog.Catalog, options CatalogListOptions) map[string]any {
	page := catalogPageForCatalogs(catalogs, options)
	return map[string]any{"page": map[string]any{"dashboards": page.Dashboards}}
}

func catalogPageForCatalogsQuery(catalogs []catalog.Catalog, query string) uisignals.CatalogPageSignal {
	return catalogPageForCatalogs(catalogs, CatalogListOptions{Query: query})
}

func catalogPageForCatalogs(catalogs []catalog.Catalog, options CatalogListOptions) uisignals.CatalogPageSignal {
	if len(catalogs) == 0 {
		page := catalogPageBase(options.Query)
		page.ListFilter = uisignals.Optional("all")
		return page
	}
	dashboards := make([]uisignals.CatalogDashboardSignal, 0)
	for _, workspaceCatalog := range catalogs {
		for _, report := range workspaceCatalog.Dashboards {
			dashboardID := workspaceCatalog.Workspace.ID + "." + report.ID
			dashboards = append(dashboards, catalogDashboardSignal(workspaceCatalog.Workspace, report, dashboardID, options.Metadata[dashboardID]))
		}
	}
	filter := normalizeCatalogWorkspaceFilter(catalogs, options.WorkspaceFilter)
	page := catalogPageBase(options.Query)
	page.WorkspaceFilters = catalogWorkspaceFilters(catalogs)
	page.ListFilter = uisignals.Optional(filter)
	page.Dashboards = filterCatalogDashboards(dashboards, options.Query, filter)
	return page
}

func CatalogBootstrapSignals(catalog catalog.Catalog, providers ...webpage.Provider) map[string]any {
	return CatalogBootstrapSignalsForPage(catalog, catalogPageSignal(catalog, ""), providers...)
}

func CatalogBootstrapSignalsForCatalogs(catalogs []catalog.Catalog, providers ...webpage.Provider) map[string]any {
	return CatalogBootstrapSignalsForCatalogsQuery(catalogs, "", providers...)
}

func CatalogBootstrapSignalsForCatalogsQuery(catalogs []catalog.Catalog, query string, providers ...webpage.Provider) map[string]any {
	return CatalogBootstrapSignalsForCatalogsWithOptions(catalogs, CatalogListOptions{Query: query}, providers...)
}

func CatalogBootstrapSignalsForCatalogsWithOptions(catalogs []catalog.Catalog, options CatalogListOptions, providers ...webpage.Provider) map[string]any {
	if len(catalogs) == 0 {
		return CatalogBootstrapSignalsForPage(catalog.Catalog{}, catalogPageForCatalogs(catalogs, options), providers...)
	}
	return CatalogBootstrapSignalsForPage(catalogs[0], catalogPageForCatalogs(catalogs, options), providers...)
}

func CatalogBootstrapSignalsForPage(catalog catalog.Catalog, page uisignals.CatalogPageSignal, providers ...webpage.Provider) map[string]any {
	layout := webpage.Resolve(firstProvider(providers), catalogLayoutContext(catalog))
	return webpage.WithSignal(layout, map[string]any{
		"page":                       page,
		"status":                     dashboard.Status{},
		"dashboardAppearanceCommand": map[string]any{},
	})
}

type recordTable = uisignals.RecordTableSignal
type recordTableColumn = uisignals.RecordTableColumnSignal
type recordTableBadge struct {
	Label string  `json:"label"`
	Tone  *string `json:"tone,omitempty"`
}

func catalogLayoutContext(catalog catalog.Catalog) webpage.Context {
	context := webpage.Context{
		Active:       "dashboards",
		SectionTitle: "Dashboards", PageTitle: "Discovery",
	}
	if len(catalog.Models) > 0 {
		context.RelatedID = catalog.Models[0].ID
		context.RelatedTitle = catalog.Models[0].Title
	}
	return context
}

func workspaceLayoutContext(catalog catalog.Catalog, active string) webpage.Context {
	return webpage.Context{
		Active: active, ScopeID: catalog.Workspace.ID, ScopeTitle: catalog.Workspace.Title,
		SectionTitle: "Workspace", PageTitle: "Published assets",
	}
}

func firstProvider(providers []webpage.Provider) webpage.Provider {
	if len(providers) == 0 {
		return nil
	}
	return providers[0]
}

func catalogPageSignal(workspaceCatalog catalog.Catalog, query string) uisignals.CatalogPageSignal {
	dashboards := make([]uisignals.CatalogDashboardSignal, 0, len(workspaceCatalog.Dashboards))
	for _, report := range workspaceCatalog.Dashboards {
		dashboards = append(dashboards, catalogDashboardSignal(workspaceCatalog.Workspace, report, report.ID, CatalogDashboardMetadata{}))
	}
	page := catalogPageBase(query)
	page.Dashboards = filterCatalogDashboards(dashboards, query)
	page.WorkspaceFilters = catalogWorkspaceFilters([]catalog.Catalog{workspaceCatalog})
	return page
}

func catalogPageBase(query string) uisignals.CatalogPageSignal {
	return uisignals.CatalogPageSignal{
		Kind:             uisignals.RouteDashboard,
		Title:            "Dashboards",
		Description:      "Reports backed by semantic models.",
		Dashboards:       []uisignals.CatalogDashboardSignal{},
		WorkspaceFilters: []uisignals.CatalogWorkspaceFilterSignal{},
		ListQuery:        uisignals.Optional(query),
	}
}

func catalogDashboardSignal(workspace catalog.Workspace, report catalog.Dashboard, id string, metadata CatalogDashboardMetadata) uisignals.CatalogDashboardSignal {
	appearance := dashboardappearance.Resolve(report.Appearance)
	return uisignals.CatalogDashboardSignal{
		AppearanceColor: appearance.Color,
		AppearanceIcon:  appearance.Icon,
		DashboardID:     report.ID,
		ID:              id,
		Title:           report.Title,
		Description:     uisignals.Optional(report.Description),
		SemanticModel:   uisignals.Optional(report.SemanticModel),
		PageCount:       int64(report.PageCount),
		Popularity:      uisignals.Optional(metadata.Popularity),
		LastRefreshedAt: uisignals.Optional(metadata.LastRefreshedAt),
		Tags:            uisignals.OptionalSlice(report.Tags),
		Href:            "/workspaces/" + workspace.ID + "/dashboards/" + report.ID,
		Workspace:       workspaceLabel(workspace),
		WorkspaceID:     workspace.ID,
	}
}

func filterCatalogDashboards(dashboards []uisignals.CatalogDashboardSignal, query string, filters ...string) []uisignals.CatalogDashboardSignal {
	query = strings.ToLower(strings.TrimSpace(query))
	filter := "all"
	if len(filters) > 0 && strings.TrimSpace(filters[0]) != "" {
		filter = strings.TrimSpace(filters[0])
	}
	if query == "" && filter == "all" {
		return dashboards
	}
	filtered := make([]uisignals.CatalogDashboardSignal, 0, len(dashboards))
	for _, dashboard := range dashboards {
		if filter != "all" && dashboard.WorkspaceID != filter {
			continue
		}
		haystack := strings.ToLower(strings.Join([]string{
			dashboard.Title,
			uisignals.ValueOrZero(dashboard.Description),
			uisignals.ValueOrZero(dashboard.SemanticModel),
			dashboard.Workspace,
		}, " "))
		if strings.Contains(haystack, query) {
			filtered = append(filtered, dashboard)
		}
	}
	return filtered
}

func catalogWorkspaceFilters(catalogs []catalog.Catalog) []uisignals.CatalogWorkspaceFilterSignal {
	filters := make([]uisignals.CatalogWorkspaceFilterSignal, 0, len(catalogs))
	seen := make(map[string]bool, len(catalogs))
	for _, workspaceCatalog := range catalogs {
		if workspaceCatalog.Workspace.ID == "" || seen[workspaceCatalog.Workspace.ID] {
			continue
		}
		seen[workspaceCatalog.Workspace.ID] = true
		filters = append(filters, uisignals.CatalogWorkspaceFilterSignal{
			ID: workspaceCatalog.Workspace.ID, Title: workspaceLabel(workspaceCatalog.Workspace),
		})
	}
	return filters
}

func normalizeCatalogWorkspaceFilter(catalogs []catalog.Catalog, filter string) string {
	filter = strings.TrimSpace(filter)
	if filter == "" || filter == "all" {
		return "all"
	}
	for _, workspaceCatalog := range catalogs {
		if workspaceCatalog.Workspace.ID == filter {
			return filter
		}
	}
	return "all"
}

func workspaceLabel(workspace catalog.Workspace) string {
	if strings.TrimSpace(workspace.Title) != "" {
		return workspace.Title
	}
	return workspace.ID
}

func recordTableBadgeValue(value, tone string) any {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return recordTableBadge{Label: value, Tone: uisignals.Optional(tone)}
}

func displayLabel(label, fallback string) string {
	if strings.TrimSpace(label) != "" {
		return label
	}
	return fallback
}

func emptyDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func jsonString(value any) string {
	bytes, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(bytes)
}
