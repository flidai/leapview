package ui

import (
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"strings"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	uiactions "github.com/flidai/leapview/internal/platform/web/actions"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
	"github.com/flidai/leapview/internal/platform/web/uicommand"

	"github.com/flidai/leapview/internal/dashboard"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

func updatesURL(workspaceID, dashboardID, pageID string) string {
	values := url.Values{}
	values.Set("route", string(uisignals.RouteDashboard))
	values.Set("workspace", workspaceID)
	values.Set("dashboard", dashboardID)
	values.Set("page", pageID)
	return "/updates?" + values.Encode()
}

func updatesURLWithParams(workspaceID, dashboardID, pageID string, params map[string]any) string {
	values := url.Values{}
	values.Set("route", string(uisignals.RouteDashboard))
	values.Set("workspace", workspaceID)
	values.Set("dashboard", dashboardID)
	values.Set("page", pageID)
	for key, raw := range params {
		switch typed := raw.(type) {
		case []string:
			for _, value := range typed {
				if strings.TrimSpace(value) != "" {
					values.Add(key, value)
				}
			}
		case string:
			if strings.TrimSpace(typed) != "" {
				values.Set(key, typed)
			}
		}
	}
	return "/updates?" + values.Encode()
}

const (
	PresentationApp    = "app"
	PresentationPublic = "public"
	PresentationEmbed  = "embed"
)

type PublicPageOptions struct {
	PublicID     string
	ClientID     string
	Presentation string
	Assets       staticasset.Resolver
}

type Presentation = webpage.Presentation

// RouteScope moves every interactive dashboard endpoint beneath one private
// route prefix. An empty scope preserves the canonical authenticated routes.
type RouteScope struct {
	BasePath string
}

// AgentCommandBindings is supplied by the composition root so dashboard UI
// can render the agent-owned chat workflow without depending on agent adapters.
type AgentCommandBindings struct {
	CreateConversation uicommand.Binding
	CreateRun          uicommand.Binding
}

func (commands AgentCommandBindings) Workflow() []uicommand.Binding {
	return []uicommand.Binding{commands.CreateConversation, commands.CreateRun}
}

func Page(clientID, csrfToken string, catalog dashboard.Catalog, report dashboarddefinition.Definition, model *semanticmodel.Model, pages []dashboard.Page, activePage dashboard.Page, initialFilters dashboard.Filters, providers ...webpage.Provider) g.Node {
	return PageWithPresentation(Presentation{ProductName: "Application", FaviconPath: "/static/favicon.svg"}, clientID, csrfToken, catalog, report, model, pages, activePage, initialFilters, providers...)
}

func PageWithPresentation(presentation Presentation, clientID, csrfToken string, catalog dashboard.Catalog, report dashboarddefinition.Definition, model *semanticmodel.Model, pages []dashboard.Page, activePage dashboard.Page, initialFilters dashboard.Filters, providers ...webpage.Provider) g.Node {
	return pageWithRouteScope(presentation, RouteScope{}, clientID, csrfToken, catalog, report, model, pages, activePage, initialFilters, AgentCommandBindings{}, providers...)
}

func PageWithRouteScope(presentation Presentation, routes RouteScope, clientID, csrfToken string, catalog dashboard.Catalog, report dashboarddefinition.Definition, model *semanticmodel.Model, pages []dashboard.Page, activePage dashboard.Page, initialFilters dashboard.Filters, providers ...webpage.Provider) g.Node {
	return pageWithRouteScope(presentation, routes, clientID, csrfToken, catalog, report, model, pages, activePage, initialFilters, AgentCommandBindings{}, providers...)
}

func PageWithRouteScopeAndAgentCommands(presentation Presentation, routes RouteScope, clientID, csrfToken string, catalog dashboard.Catalog, report dashboarddefinition.Definition, model *semanticmodel.Model, pages []dashboard.Page, activePage dashboard.Page, initialFilters dashboard.Filters, commands AgentCommandBindings, providers ...webpage.Provider) g.Node {
	return pageWithRouteScope(presentation, routes, clientID, csrfToken, catalog, report, model, pages, activePage, initialFilters, commands, providers...)
}

func pageWithRouteScope(presentation Presentation, routes RouteScope, clientID, csrfToken string, catalog dashboard.Catalog, report dashboarddefinition.Definition, model *semanticmodel.Model, pages []dashboard.Page, activePage dashboard.Page, initialFilters dashboard.Filters, commands AgentCommandBindings, providers ...webpage.Provider) g.Node {
	if activePage.ID == "" {
		activePage = defaultPage()
	}
	routes.BasePath = strings.TrimSuffix(strings.TrimSpace(routes.BasePath), "/")
	initialFilters = report.NormalizeFiltersForPage(activePage.ID, initialFilters)
	initialURLParams := report.URLParamsFromFiltersForPage(activePage.ID, initialFilters)
	initialURLParams["streamInstance"] = newStreamInstanceID()
	dashboardUpdatesURL := updatesURLWithParams(catalog.Workspace.ID, report.ID, activePage.ID, initialURLParams)
	commandBase := "/workspaces/" + catalog.Workspace.ID + "/commands/"
	if routes.BasePath != "" {
		values := url.Values{}
		values.Set("route", string(uisignals.RouteDashboard))
		values.Set("workspace", catalog.Workspace.ID)
		values.Set("dashboard", report.ID)
		values.Set("page", activePage.ID)
		for key, raw := range initialURLParams {
			switch typed := raw.(type) {
			case []string:
				for _, value := range typed {
					if strings.TrimSpace(value) != "" {
						values.Add(key, value)
					}
				}
			case string:
				if strings.TrimSpace(typed) != "" {
					values.Set(key, typed)
				}
			}
		}
		dashboardUpdatesURL = routes.BasePath + "/updates?" + values.Encode()
		commandBase = routes.BasePath + "/commands/"
	}
	provider := firstProvider(providers)
	layout := webpage.Resolve(provider, dashboardLayoutContext(catalog, report, model, activePage))
	if provider == nil {
		layout.Presentation = presentation
	}
	componentAttrs := []g.Node{
		g.Attr("slot", "page"),
		g.Attr("workspace-id", catalog.Workspace.ID),
		g.Attr("dashboard-id", report.ID),
		g.Attr("page-id", activePage.ID),
		g.Attr("data-indicator", "agentTurnPending"),
		g.Attr("data-on:lv-filter-command", "$filterCommand = evt.detail; "+uiactions.EventPost(commandBase+"filter", "runtime", "filterCommand")),
		g.Attr("data-on:lv-filter-options-request", "$filterOptionRequest = evt.detail; "+uiactions.EventPost(commandBase+"filter-options", "runtime", "filterOptionRequest")),
		g.Attr("data-on:lv-page-navigate", "$navigationCommand = evt.detail; "+uiactions.EventPost(commandBase+"navigate", "runtime", "navigationCommand")),
		g.Attr("data-on:lv-selection-clear", "$interactionSelections = []; "+uiactions.EventPost(commandBase+"clear-selection", "runtime")),
		g.Attr("data-on:lv-interaction-select", "$interactionCommand = evt.detail; "+uiactions.EventPost(commandBase+"select", "runtime", "interactionCommand")),
		g.Attr("data-on:lv-interaction-spatial-select", "$spatialInteractionCommand = evt.detail; "+uiactions.EventPost(commandBase+"spatial-select", "runtime", "spatialInteractionCommand")),
		g.Attr("data-on:lv-visualization-window-request", "$visualWindowCommand = evt.detail; "+uiactions.EventPost(commandBase+"visual-window", "runtime", "visualWindowCommand")),
	}
	if routes.BasePath == "" {
		agentTurn := "$agent.composer.value = evt.detail.input; $agentContext.references = evt.detail.references; $agentContext.filters = $filterState; $agentContext.generation = $status.generation; " + uiactions.CommandPostConditional("$agent.activeConversationId", []uicommand.Binding{commands.CreateRun}, commands.Workflow(), "/chats/turns", "agent", "agentContext")
		agentRestore := "$agent.activeConversationId = evt.detail.conversationId; " + uiactions.Get("/chats/restore", "agent")
		componentAttrs = append(componentAttrs,
			g.Attr("data-on:lv-chat-submit", agentTurn),
			g.Attr("data-on:lv-chat-restore", agentRestore),
			g.Attr("data-on:lv-chat-new", "$agent.activeConversationId = ''; $agent.transcript = []; $agent.composer.value = ''; $agentVisuals = {}"),
		)
	}
	contentAttrs := []g.Node{}
	if routes.BasePath == "" {
		contentAttrs = append(contentAttrs,
			g.Attr("data-on:lv-chat-reference-search__debounce.200ms", "$agentReferenceSearch.query = evt.detail.query; $agentReferenceSearch.requestId = evt.detail.requestId; "+uiactions.Get("/chats/references/search", "agentReferenceSearch", "agentContext")),
		)
	}
	return webpage.Render(layout, webpage.Spec{
		Title: layout.Presentation.ProductName, CSRFToken: csrfToken,
		Scripts: []string{"/static/dashboard-page.js", "/static/url-sync.js"},
		MainAttrs: []g.Node{
			h.ID("dashboard"),
			h.Class(webpage.RootClass),
		},
		UpdatesURL:   dashboardUpdatesURL,
		ContentAttrs: contentAttrs,
		Content:      g.El("lv-dashboard-page", componentAttrs...),
	})
}

// PublicPage renders the report component without authenticated application
// chrome or cookies. Every command is scoped beneath the opaque publication
// route and carries the document-generated client and stream identities.
func PublicPage(options PublicPageOptions, catalog dashboard.Catalog, report dashboarddefinition.Definition, model *semanticmodel.Model, pages []dashboard.Page, activePage dashboard.Page, initialFilters dashboard.Filters) g.Node {
	presentation := options.Presentation
	if presentation != PresentationEmbed {
		presentation = PresentationPublic
	}
	if options.ClientID == "" {
		options.ClientID = newStreamInstanceID()
	}
	initialFilters = report.NormalizeFiltersForPage(activePage.ID, initialFilters)
	params := report.URLParamsFromFiltersForPage(activePage.ID, initialFilters)
	params["streamInstance"] = newStreamInstanceID()
	params["clientId"] = options.ClientID
	values := url.Values{}
	values.Set("page", activePage.ID)
	values.Set("presentation", presentation)
	for key, raw := range params {
		switch typed := raw.(type) {
		case []string:
			for _, value := range typed {
				values.Add(key, value)
			}
		case string:
			values.Set(key, typed)
		}
	}
	base := "/public/dashboards/" + options.PublicID
	commandBase := base + "/commands/"
	return webpage.Render(webpage.Layout{Assets: options.Assets}, webpage.Spec{
		Title: report.Title, Scripts: []string{"/static/dashboard-page.js", "/static/url-sync.js"},
		MainAttrs: []g.Node{
			h.ID("dashboard"), h.Class(webpage.RootClass),
		},
		UpdatesURL: base + "/updates?" + values.Encode(),
		Content: g.El("lv-dashboard-page",
			g.Attr("workspace-id", catalog.Workspace.ID), g.Attr("dashboard-id", report.ID), g.Attr("page-id", activePage.ID), g.Attr("presentation", presentation),
			g.Attr("data-on:lv-filter-command", "$filterCommand = evt.detail; "+uiactions.EventPost(commandBase+"filter", "runtime", "filterCommand")),
			g.Attr("data-on:lv-filter-options-request", "$filterOptionRequest = evt.detail; "+uiactions.EventPost(commandBase+"filter-options", "runtime", "filterOptionRequest")),
			g.Attr("data-on:lv-page-navigate", "$navigationCommand = evt.detail; "+uiactions.EventPost(commandBase+"navigate", "runtime", "navigationCommand")),
			g.Attr("data-on:lv-selection-clear", "$interactionSelections = []; "+uiactions.EventPost(commandBase+"clear-selection", "runtime")),
			g.Attr("data-on:lv-interaction-select", "$interactionCommand = evt.detail; "+uiactions.EventPost(commandBase+"select", "runtime", "interactionCommand")),
			g.Attr("data-on:lv-interaction-spatial-select", "$spatialInteractionCommand = evt.detail; "+uiactions.EventPost(commandBase+"spatial-select", "runtime", "spatialInteractionCommand")),
			g.Attr("data-on:lv-visualization-window-request", "$visualWindowCommand = evt.detail; "+uiactions.EventPost(commandBase+"visual-window", "runtime", "visualWindowCommand")),
		),
	})
}

func defaultPage() dashboard.Page {
	return dashboard.Page{
		ID:     "overview",
		Title:  "Overview",
		Canvas: dashboard.PageCanvas{Width: 1366, Height: 940},
		Grid:   dashboard.PageGrid{Columns: 12, RowHeight: 48, Gap: 16, Padding: 16},
	}
}

func BootstrapSignals(clientID, streamInstanceID string, catalog dashboard.Catalog, report dashboarddefinition.Definition, model *semanticmodel.Model, definitions map[string]visualizationdefinition.Definition, pages []dashboard.Page, activePage dashboard.Page, initialFilters dashboard.Filters, providers ...webpage.Provider) map[string]any {
	return BootstrapSignalsWithRouteScope(RouteScope{}, clientID, streamInstanceID, catalog, report, model, definitions, pages, activePage, initialFilters, providers...)
}

func BootstrapSignalsWithRouteScope(routes RouteScope, clientID, streamInstanceID string, catalog dashboard.Catalog, report dashboarddefinition.Definition, model *semanticmodel.Model, definitions map[string]visualizationdefinition.Definition, pages []dashboard.Page, activePage dashboard.Page, initialFilters dashboard.Filters, providers ...webpage.Provider) map[string]any {
	envelope := uisignals.DashboardInitialEnvelope(clientID, streamInstanceID, catalog, report, model, definitions, pages, activePage, initialFilters)
	if base := strings.TrimSuffix(strings.TrimSpace(routes.BasePath), "/"); base != "" {
		for index := range envelope.Page.Pages {
			envelope.Page.Pages[index].Href = base + "/dashboards/" + report.ID + "/pages/" + envelope.Page.Pages[index].ID
		}
	}
	envelope.Runtime.WorkspaceID = uisignals.Optional(catalog.Workspace.ID)
	signals := map[string]any{
		"agent":                     envelope.Agent,
		"agentContext":              envelope.AgentContext,
		"agentReferenceSearch":      envelope.AgentReferenceSearch,
		"agentVisuals":              envelope.AgentVisuals,
		"page":                      envelope.Page,
		"runtime":                   envelope.Runtime,
		"filterContract":            envelope.FilterContract,
		"filterState":               envelope.FilterState,
		"filterOptionPages":         envelope.FilterOptionPages,
		"filterCommand":             envelope.FilterCommand,
		"filterOptionRequest":       envelope.FilterOptionRequest,
		"filterValidation":          envelope.FilterValidation,
		"navigationCommand":         envelope.NavigationCommand,
		"interactionSelections":     envelope.InteractionSelections,
		"spatialSelections":         envelope.SpatialSelections,
		"urlParams":                 envelope.URLParams,
		"interactionCommand":        envelope.InteractionCommand,
		"spatialInteractionCommand": envelope.SpatialInteractionCommand,
		"visualWindowCommand":       envelope.VisualWindowCommand,
		"visuals":                   envelope.Visuals,
		"status":                    envelope.Status,
	}
	layout := webpage.Resolve(firstProvider(providers), dashboardLayoutContext(catalog, report, model, activePage))
	return webpage.WithSignal(layout, signals)
}

func PublicBootstrapSignals(clientID, streamInstanceID, publicID, presentation string, catalog dashboard.Catalog, report dashboarddefinition.Definition, model *semanticmodel.Model, definitions map[string]visualizationdefinition.Definition, pages []dashboard.Page, activePage dashboard.Page, initialFilters dashboard.Filters) map[string]any {
	envelope := uisignals.DashboardInitialEnvelope(clientID, streamInstanceID, catalog, report, model, definitions, pages, activePage, initialFilters)
	family := "public"
	if presentation == PresentationEmbed {
		family = "embed"
	}
	for index := range envelope.Page.Pages {
		envelope.Page.Pages[index].Href = "/" + family + "/dashboards/" + publicID + "/pages/" + envelope.Page.Pages[index].ID
	}
	envelope.Page.Presentation = presentation
	return map[string]any{
		"page":                      envelope.Page,
		"runtime":                   envelope.Runtime,
		"filterContract":            envelope.FilterContract,
		"filterState":               envelope.FilterState,
		"filterOptionPages":         envelope.FilterOptionPages,
		"filterCommand":             envelope.FilterCommand,
		"filterOptionRequest":       envelope.FilterOptionRequest,
		"filterValidation":          envelope.FilterValidation,
		"navigationCommand":         envelope.NavigationCommand,
		"interactionSelections":     envelope.InteractionSelections,
		"spatialSelections":         envelope.SpatialSelections,
		"urlParams":                 envelope.URLParams,
		"interactionCommand":        envelope.InteractionCommand,
		"spatialInteractionCommand": envelope.SpatialInteractionCommand,
		"visualWindowCommand":       envelope.VisualWindowCommand,
		"visuals":                   envelope.Visuals,
		"status":                    envelope.Status,
	}
}

func dashboardLayoutContext(catalog dashboard.Catalog, report dashboarddefinition.Definition, model *semanticmodel.Model, activePage dashboard.Page) webpage.Context {
	context := webpage.Context{
		Active: "dashboards", ScopeID: catalog.Workspace.ID, ScopeTitle: catalog.Workspace.Title,
		SectionID: report.ID, SectionTitle: report.Title,
		PageID: activePage.ID, PageTitle: activePage.Title, Compact: true,
	}
	if model != nil {
		context.RelatedID = model.Name
		context.RelatedTitle = model.Title
	}
	return context
}

func firstProvider(providers []webpage.Provider) webpage.Provider {
	if len(providers) == 0 {
		return nil
	}
	return providers[0]
}

func newStreamInstanceID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return hex.EncodeToString(bytes[:])
	}
	return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
}
