package ui

import (
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	webpage "github.com/flidai/leapview/internal/platform/web/page"

	g "maragu.dev/gomponents"
	c "maragu.dev/gomponents/components"
	h "maragu.dev/gomponents/html"
)

// DashboardDraftPreviewPage renders an exact draft revision through the same
// application dashboard shell as a published dashboard. The surface remains
// read-only and exposes one explicit transition into the builder.
func DashboardDraftPreviewPage(title, dashboardID, pageID, editHref, updatesURL, csrfToken string, commands AgentCommandBindings, providers ...webpage.Provider) g.Node {
	layout := webpage.Resolve(firstProvider(providers), webpage.Context{
		Active: "dashboards", SectionID: dashboardID, SectionTitle: title,
		PageID: pageID, PageTitle: title, Compact: true,
	})
	componentAttrs := []g.Node{
		g.Attr("slot", "page"),
		g.Attr("dashboard-id", dashboardID),
		g.Attr("page-id", pageID),
		g.Attr("presentation", PresentationApp),
		g.Attr("read-only", ""),
		g.Attr("authoring-action-label", "Edit dashboard"),
		g.Attr("authoring-action-href", editHref),
	}
	componentAttrs = append(componentAttrs, dashboardAgentComponentAttrs(commands)...)
	return webpage.Render(layout, webpage.Spec{
		Title:        title,
		CSRFToken:    csrfToken,
		Scripts:      []string{"/static/dashboard-page.js"},
		MainAttrs:    []g.Node{h.ID("dashboard"), h.Class(webpage.RootClass)},
		UpdatesURL:   updatesURL,
		ContentAttrs: dashboardAgentContentAttrs(),
		Content:      g.El("lv-dashboard-page", componentAttrs...),
	})
}

// DashboardDraftPreviewRevisionChangedPage renders a static recovery page for
// an exact-revision preview link that no longer points at the current draft.
// It intentionally does not open a stream or retry against the latest
// revision: the caller must return to the builder and choose the new exact
// revision explicitly.
func DashboardDraftPreviewRevisionChangedPage(backHref string, providers ...webpage.Provider) g.Node {
	layout := webpage.Resolve(firstProvider(providers), webpage.Context{
		Active: "dashboards", PageTitle: "Preview unavailable", Compact: true,
	})
	productName := strings.TrimSpace(layout.Presentation.ProductName)
	if productName == "" {
		productName = "LeapView"
	}
	faviconPath := strings.TrimSpace(layout.Presentation.FaviconPath)
	if faviconPath == "" {
		faviconPath = "/static/favicon.svg"
	}
	return c.HTML5(c.HTML5Props{
		Title: "Draft preview unavailable · " + productName, Language: "en",
		Head: g.Group{
			h.Link(h.Rel("icon"), h.Href(layout.Assets.URL(faviconPath)), h.Type("image/svg+xml")),
			h.Link(h.Rel("stylesheet"), h.Href(layout.Assets.URL("/static/app.css"))),
		},
		Body: g.Group{
			h.Main(h.Class("min-h-svh bg-app text-fg-default flex items-center justify-center p-6"),
				h.Section(h.Class("w-full max-w-lg rounded-xl border border-border-default bg-canvas-default p-6 shadow-lg"),
					h.P(h.Class("text-sm font-medium text-fg-muted"), g.Text(productName)),
					h.H1(h.Class("mt-3 text-xl font-semibold"), g.Text("Draft changed")),
					h.P(h.Class("mt-3 text-sm text-fg-muted"), g.Text("This exact preview link points to an older draft revision. The draft changed in the builder, so LeapView did not open a newer revision automatically.")),
					h.P(h.Class("mt-3 text-sm text-fg-muted"), g.Text("Return to builder to continue with the latest draft.")),
					h.Div(h.Class("mt-6"),
						h.A(h.Class("inline-flex items-center rounded-md border border-border-default px-3 py-2 text-sm font-medium hover:bg-canvas-subtle"), h.Href(backHref), g.Text("Back to builder")),
					),
				),
			),
		},
	})
}

// DashboardDraftPreviewBootstrapSignals projects a successful exact-revision
// preview into the production dashboard signal contract. This keeps the
// builder and preview renderers on one visual implementation.
func DashboardDraftPreviewBootstrapSignals(definition dashboarddefinition.Definition, patch dashboard.Patch, pageID, servingStateID string, pageHrefs map[string]string, providers ...webpage.Provider) (map[string]any, error) {
	activePage, ok := definition.PageOrDefault(strings.TrimSpace(pageID))
	if !ok {
		return nil, fmt.Errorf("draft preview page %q is unavailable", pageID)
	}
	filters := patch.Filters.WithDefaults()
	if filters.CompiledState == nil {
		state := definition.DefaultFilterState()
		filters.CompiledState = &state
	}
	filters.ActivePageID = activePage.ID
	if strings.TrimSpace(filters.ServingStateID) == "" {
		filters.ServingStateID = strings.TrimSpace(servingStateID)
	}
	envelope := uisignals.DashboardInitialEnvelope("", "", dashboard.Catalog{}, definition, nil, definition.Visualizations, definition.Pages, activePage, filters)
	envelope.Page.Presentation = PresentationApp
	for index := range envelope.Page.Pages {
		if href := strings.TrimSpace(pageHrefs[envelope.Page.Pages[index].ID]); href != "" {
			envelope.Page.Pages[index].Href = href
		}
	}
	envelope.FilterContract = uisignals.DashboardFilterContractFromDefinition(definition)
	for key, binding := range envelope.FilterContract.Bindings {
		binding.ReaderEditable = false
		envelope.FilterContract.Bindings[key] = binding
	}
	envelope.FilterState = uisignals.DashboardFilterStateFromDomain(*filters.CompiledState)
	envelope.InteractionSelections = uisignals.DashboardInteractionSelectionsFromDashboard(filters.Selections)
	envelope.InteractionRevision = int64(filters.InteractionRevision)
	envelope.SpatialSelections = uisignals.DashboardSpatialSelectionsFromDashboard(filters.SpatialSelections)
	envelope.Status = uisignals.DashboardStatusFromDashboard(patch.Status)
	envelope.Visuals = make(map[string]uisignals.DashboardVisualizationSignal, len(patch.Visuals))
	generation := patch.Status.Generation
	if generation <= 0 {
		generation = 1
	}
	filterRevision := int64(filters.CompiledState.Revision)
	for visualID, visual := range patch.Visuals {
		visualID = strings.TrimSpace(visualID)
		if visualID == "" {
			visualID = strings.TrimSpace(visual.VisualID)
		}
		if visualID == "" {
			continue
		}
		signal := uisignals.DashboardVisualizationSignalFromIR(visual)
		signal.VisualID = visualID
		signal.StreamGeneration = generation
		signal.FilterRevision = filterRevision
		signal.InteractionRevision = int64(filters.InteractionRevision)
		signal.ServingStateID = filters.ServingStateID
		signal.ConsumerIdentity = activePage.ID + "/" + visualID
		envelope.Visuals[visualID] = signal
	}
	if err := uisignals.ValidateDashboardEnvelope(envelope); err != nil {
		return nil, fmt.Errorf("validate draft preview dashboard signals: %w", err)
	}
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
		"interactionRevision":       envelope.InteractionRevision,
		"spatialSelections":         envelope.SpatialSelections,
		"interactionCommand":        envelope.InteractionCommand,
		"spatialInteractionCommand": envelope.SpatialInteractionCommand,
		"visualWindowCommand":       envelope.VisualWindowCommand,
		"urlParams":                 envelope.URLParams,
		"visuals":                   envelope.Visuals,
		"status":                    envelope.Status,
	}
	layout := webpage.Resolve(firstProvider(providers), webpage.Context{
		Active: "dashboards", SectionID: definition.ID, SectionTitle: definition.Title,
		PageID: activePage.ID, PageTitle: activePage.Title, Compact: true,
	})
	return webpage.WithSignal(layout, signals), nil
}
