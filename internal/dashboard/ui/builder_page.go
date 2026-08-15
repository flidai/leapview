package ui

import (
	"net/url"
	"strings"

	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	uiactions "github.com/flidai/leapview/internal/platform/web/actions"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/uicommand"

	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// DashboardBuilderActionBindings supplies routes and generated operation
// identities to the builder shell. The builder component only emits typed
// domain events; this boundary keeps command transport in the page shell.
//
// BackHref, PreviewHref, and ExportYAMLHref are optional browser navigations.
// PageBaseHref is the canonical edit URL used by page-navigation anchors.
// Mutating actions use one injected command path and APIGen binding so CSRF
// and operation headers are always assembled by the shared action helpers.
type DashboardBuilderActionBindings struct {
	BackHref       string
	PreviewHref    string
	ExportYAMLHref string
	PageBaseHref   string

	CommandPath    string
	CommandBinding uicommand.Binding
}

// DashboardBuilderPage renders the document shell for the draft dashboard
// authoring route. Authored pages, visuals, and semantic fields remain in the
// stream bootstrap; this document contains only identity attributes and the
// injected action bridges.
func DashboardBuilderPage(envelope uisignals.DashboardBuilderEnvelope, csrfToken string, actions DashboardBuilderActionBindings, providers ...webpage.Provider) g.Node {
	builder := envelope.Builder
	layout := builderFocusLayout(firstProvider(providers), webpage.Context{
		Active:       "dashboards",
		ScopeID:      builder.WorkspaceID,
		SectionID:    builder.DashboardID,
		SectionTitle: builder.Title,
		PageTitle:    "Builder",
		Compact:      true,
	})

	updates := dashboardBuilderUpdatesURL(builder)
	attrs := []g.Node{
		g.Attr("slot", "page"),
		g.Attr("workspace-id", builder.WorkspaceID),
		g.Attr("dashboard-id", builder.DashboardID),
		g.Attr("draft-id", builder.DraftID),
		builderCommandAction(actions),
	}
	for name, value := range map[string]string{
		"back-href":        actions.BackHref,
		"preview-href":     actions.PreviewHref,
		"export-yaml-href": actions.ExportYAMLHref,
		"page-base-href":   actions.PageBaseHref,
	} {
		if strings.TrimSpace(value) != "" {
			attrs = append(attrs, g.Attr(name, value))
		}
	}

	return webpage.Render(layout, webpage.Spec{
		Title: builder.Title, CSRFToken: csrfToken,
		Scripts:    []string{"/static/dashboard-builder.js"},
		MainAttrs:  []g.Node{h.ID("dashboard-builder"), h.Class(webpage.RootClass)},
		UpdatesURL: updates,
		Content:    g.El("lv-dashboard-builder", attrs...),
	})
}

// builderFocusLayout keeps product presentation/theme/assets while removing
// the application shell mount and chrome assets. The builder owns its own
// back/title/actions and therefore renders as a full-width route-local focus
// surface; other routes continue using the injected layout unchanged.
func builderFocusLayout(provider webpage.Provider, context webpage.Context) webpage.Layout {
	layout := webpage.Resolve(provider, context)
	layout.Signal = nil
	layout.Scripts = nil
	layout.Mount = nil
	return layout
}

// DashboardBuilderBootstrapSignals exposes the typed stream bootstrap under
// stable signal keys without serializing it into the document shell.
func DashboardBuilderBootstrapSignals(envelope uisignals.DashboardBuilderEnvelope) map[string]any {
	return map[string]any{
		"builder":        envelope.Builder,
		"builderVisuals": envelope.BuilderVisuals,
		"runtime":        envelope.Runtime,
		"status":         envelope.Status,
	}
}

func dashboardBuilderUpdatesURL(builder uisignals.DashboardBuilderSignal) string {
	values := url.Values{}
	values.Set("route", string(uisignals.RouteKindDashboardBuilder))
	if strings.TrimSpace(builder.WorkspaceID) != "" {
		values.Set("workspace", builder.WorkspaceID)
	}
	if strings.TrimSpace(builder.DashboardID) != "" {
		values.Set("dashboard", builder.DashboardID)
	}
	if strings.TrimSpace(builder.DraftID) != "" {
		values.Set("draft", builder.DraftID)
	}
	if builder.SelectedPageID != nil && strings.TrimSpace(*builder.SelectedPageID) != "" {
		values.Set("page", strings.TrimSpace(*builder.SelectedPageID))
	}
	return "/updates?" + values.Encode()
}

func builderCommandAction(actions DashboardBuilderActionBindings) g.Node {
	value := "$builderCommand = evt.detail;"
	if strings.TrimSpace(actions.CommandPath) != "" {
		value += " " + uiactions.CommandPost(actions.CommandBinding, actions.CommandPath, "builderCommand")
	}
	return g.Attr("data-on:lv-builder-command", value)
}
