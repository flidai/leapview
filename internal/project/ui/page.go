package ui

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/flidai/leapview/internal/dashboard"
	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	uiactions "github.com/flidai/leapview/internal/platform/web/actions"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	catalog "github.com/flidai/leapview/internal/project/navigation"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
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
	return catalogPageDocument(catalog, catalogPageSignal(catalog, query), "", false, providers...)
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

// CatalogDashboardItem is the source-neutral dashboard discovery projection.
// The project HTTP boundary supplies already-authorized items from the
// governed dashboard authoring catalog; UI code only shapes them for display.
type CatalogDashboardItem struct {
	ID, DashboardID, Title, Description, SemanticModel, Href string
	Owner, Status, CatalogScope, UpdatedAt                   string
	PageCount                                                int
	Tags                                                     []string
	Appearance                                               dashboardappearance.Value
	RepositoryManaged                                        bool
}

type CatalogListOptions struct {
	Query          string
	ProjectFilter  string
	Metadata       map[string]CatalogDashboardMetadata
	Dashboards     []CatalogDashboardItem
	CanCreateDraft bool
}

func CatalogPageForCatalogsWithOptions(catalogs []catalog.Catalog, options CatalogListOptions, csrfToken string, providers ...webpage.Provider) g.Node {
	if len(catalogs) == 0 {
		return catalogPageDocument(catalog.Catalog{}, catalogPageForCatalogs(catalogs, options), csrfToken, options.CanCreateDraft, providers...)
	}
	// A serving process owns one active project. Keep this helper for callers
	// that still pass a slice, but render only the server-bound catalog and do
	// not expose a project picker in the page signal.
	return catalogPageDocument(catalogs[0], catalogPageForCatalogs(catalogs[:1], options), csrfToken, options.CanCreateDraft, providers...)
}

func catalogPageDocument(catalog catalog.Catalog, page uisignals.CatalogPageSignal, csrfToken string, canCreateDraft bool, providers ...webpage.Provider) g.Node {
	layout := webpage.Resolve(firstProvider(providers), catalogLayoutContext(catalog))
	catalogUpdatesURL := updatesURL(uisignals.RouteKindCatalog, "q", uisignals.ValueOrZero(page.ListQuery))
	title := "Dashboards"
	if productName := strings.TrimSpace(layout.Presentation.ProductName); productName != "" {
		title = productName + " Dashboards"
	}
	content := []g.Node{
		g.Attr("slot", "page"),
		g.Attr("data-on:lv-entity-list-query__debounce.200ms", "$entityListQuery = evt.detail.query; $entityListFilter = evt.detail.filter; "+uiactions.Get("/catalog/search", "entityListQuery", "entityListFilter")),
	}
	if canCreateDraft {
		content = append(content, g.Attr("create-draft-href", "/dashboards/new"))
	}
	return webpage.Render(layout, webpage.Spec{
		Title: title, CSRFToken: csrfToken, Scripts: []string{"/static/catalog-page.js"},
		UpdatesURL: catalogUpdatesURL,
		Content:    g.El("lv-catalog-page", content...),
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
	if options.Dashboards != nil {
		page := catalogPageBase(options.Query)
		dashboards := make([]uisignals.CatalogDashboardSignal, 0, len(options.Dashboards))
		for _, item := range options.Dashboards {
			dashboards = append(dashboards, catalogDashboardItemSignal(item))
		}
		page.Dashboards = filterCatalogDashboards(dashboards, options.Query)
		return page
	}
	if len(catalogs) == 0 {
		page := catalogPageBase(options.Query)
		return page
	}
	// Project selection is a server concern, not a browser control. The first
	// catalog is the active project lease; ignore additional catalogs and any
	// caller-supplied project filter.
	projectCatalog := catalogs[0]
	dashboards := make([]uisignals.CatalogDashboardSignal, 0)
	for _, report := range projectCatalog.Dashboards {
		dashboardID := projectCatalog.Project.ID + "." + report.ID
		dashboards = append(dashboards, catalogDashboardSignal(projectCatalog.Project, report, dashboardID, options.Metadata[dashboardID]))
	}
	page := catalogPageBase(options.Query)
	page.Dashboards = filterCatalogDashboards(dashboards, options.Query)
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
		"page":   page,
		"status": dashboard.Status{},
	})
}

type recordTable = uisignals.RecordTableSignal
type recordTableColumn = uisignals.RecordTableColumnSignal
type recordTableBadge struct {
	Label string  `json:"label"`
	Tone  *string `json:"tone,omitempty"`
}

type recordTableDiff struct {
	Label     string `json:"label"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
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

func projectLayoutContext(catalog catalog.Catalog, active string) webpage.Context {
	if active == "dashboards" {
		active = "dashboard-catalog"
	}
	return webpage.Context{
		Active: active, ScopeID: catalog.Project.ID, ScopeTitle: catalog.Project.Title,
		SectionTitle: "Project", PageTitle: "Published assets",
	}
}

func firstProvider(providers []webpage.Provider) webpage.Provider {
	if len(providers) == 0 {
		return nil
	}
	return providers[0]
}

func catalogPageSignal(projectCatalog catalog.Catalog, query string) uisignals.CatalogPageSignal {
	dashboards := make([]uisignals.CatalogDashboardSignal, 0, len(projectCatalog.Dashboards))
	for _, report := range projectCatalog.Dashboards {
		dashboards = append(dashboards, catalogDashboardSignal(projectCatalog.Project, report, report.ID, CatalogDashboardMetadata{}))
	}
	page := catalogPageBase(query)
	page.Dashboards = filterCatalogDashboards(dashboards, query)
	return page
}

func catalogPageBase(query string) uisignals.CatalogPageSignal {
	return uisignals.CatalogPageSignal{
		Kind:        uisignals.RouteKindDashboard,
		Title:       "Dashboards",
		Description: "Reports backed by semantic models.",
		Dashboards:  []uisignals.CatalogDashboardSignal{},
		ListQuery:   uisignals.Optional(query),
	}
}

func catalogDashboardSignal(_ catalog.Project, report catalog.Dashboard, id string, metadata CatalogDashboardMetadata) uisignals.CatalogDashboardSignal {
	appearance := dashboardappearance.Resolve(report.Appearance)
	return uisignals.CatalogDashboardSignal{
		AppearanceColor:   appearance.Color,
		AppearanceIcon:    appearance.Icon,
		CatalogScope:      "managed",
		DashboardID:       report.ID,
		ID:                id,
		Title:             report.Title,
		Description:       uisignals.Optional(report.Description),
		SemanticModel:     uisignals.Optional(report.SemanticModel),
		PageCount:         int64(report.PageCount),
		Popularity:        uisignals.Optional(metadata.Popularity),
		RepositoryManaged: true,
		LastRefreshedAt:   uisignals.Optional(metadata.LastRefreshedAt),
		Status:            "published",
		Tags:              uisignals.OptionalSlice(report.Tags),
		Href:              "/dashboards/" + url.PathEscape(report.ID),
	}
}

func catalogDashboardItemSignal(item CatalogDashboardItem) uisignals.CatalogDashboardSignal {
	appearance := dashboardappearance.Resolve(item.Appearance)
	return uisignals.CatalogDashboardSignal{
		AppearanceColor: appearance.Color, AppearanceIcon: appearance.Icon,
		CatalogScope: item.CatalogScope, Description: uisignals.Optional(item.Description),
		DashboardID: item.DashboardID, Href: item.Href, ID: item.ID,
		Owner: uisignals.Optional(item.Owner), PageCount: int64(item.PageCount),
		RepositoryManaged: item.RepositoryManaged, SemanticModel: uisignals.Optional(item.SemanticModel),
		Status: item.Status, Tags: uisignals.OptionalSlice(item.Tags), Title: item.Title,
		UpdatedAt: uisignals.Optional(item.UpdatedAt),
	}
}

func filterCatalogDashboards(dashboards []uisignals.CatalogDashboardSignal, query string, filters ...string) []uisignals.CatalogDashboardSignal {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return dashboards
	}
	filtered := make([]uisignals.CatalogDashboardSignal, 0, len(dashboards))
	for _, dashboard := range dashboards {
		haystack := strings.ToLower(strings.Join([]string{
			dashboard.Title,
			uisignals.ValueOrZero(dashboard.Description),
			uisignals.ValueOrZero(dashboard.SemanticModel),
			uisignals.ValueOrZero(dashboard.Owner),
			dashboard.Status,
		}, " "))
		if strings.Contains(haystack, query) {
			filtered = append(filtered, dashboard)
		}
	}
	return filtered
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
