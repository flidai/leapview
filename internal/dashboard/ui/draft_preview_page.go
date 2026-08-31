package ui

import (
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	webpage "github.com/flidai/leapview/internal/platform/web/page"

	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// DashboardDraftPreviewPage renders an exact draft revision through the same
// report component as a published dashboard. The surface is intentionally
// read-only: authoring remains in the builder and the preview stream never
// exposes dashboard mutation command bridges.
func DashboardDraftPreviewPage(title, dashboardID, pageID, revisionLabel, backHref, updatesURL string, providers ...webpage.Provider) g.Node {
	layout := builderFocusLayout(firstProvider(providers), webpage.Context{
		Active: "dashboards", SectionID: dashboardID, SectionTitle: title,
		PageID: pageID, PageTitle: "Draft preview", Compact: true,
	})
	return webpage.Render(layout, webpage.Spec{
		Title:      title + " preview",
		Scripts:    []string{"/static/dashboard-page.js"},
		MainAttrs:  []g.Node{h.ID("dashboard-draft-preview"), h.Class(webpage.RootClass)},
		UpdatesURL: updatesURL,
		Content: h.Div(h.Class("flex min-h-svh flex-col"),
			h.Header(h.Class("flex min-h-14 items-center justify-between gap-3 border-b border-border-default bg-canvas-default px-4 py-2"),
				h.Div(h.Class("flex min-w-0 items-center gap-3"),
					h.A(h.Class("button"), h.Href(backHref), g.Text("Back to builder")),
					h.Div(h.Class("min-w-0"),
						h.P(h.Class("truncate text-sm font-semibold text-fg-default"), g.Text(title)),
						h.P(h.Class("truncate text-xs text-fg-muted"), g.Text("Draft preview · "+revisionLabel)),
					),
				),
				h.Span(h.Class("rounded-full border border-border-default bg-canvas-subtle px-2 py-1 text-xs font-medium text-fg-muted"), g.Text("Read only")),
			),
			g.El("lv-dashboard-page",
				h.Class("min-h-0 flex-1"),
				g.Attr("dashboard-id", dashboardID),
				g.Attr("page-id", pageID),
				g.Attr("presentation", "public"),
				g.Attr("read-only", ""),
				g.Attr("aria-label", "Draft dashboard preview"),
			),
		),
	})
}

// DashboardDraftPreviewBootstrapSignals projects a successful exact-revision
// preview into the production dashboard signal contract. This keeps the
// builder and preview renderers on one visual implementation.
func DashboardDraftPreviewBootstrapSignals(definition dashboarddefinition.Definition, patch dashboard.Patch, pageID, servingStateID string, pageHrefs map[string]string) (map[string]any, error) {
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
	envelope.Page.Presentation = "public"
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
		"interactionRevision":       envelope.InteractionRevision,
		"spatialSelections":         envelope.SpatialSelections,
		"interactionCommand":        envelope.InteractionCommand,
		"spatialInteractionCommand": envelope.SpatialInteractionCommand,
		"visualWindowCommand":       envelope.VisualWindowCommand,
		"urlParams":                 envelope.URLParams,
		"visuals":                   envelope.Visuals,
		"status":                    envelope.Status,
	}, nil
}
