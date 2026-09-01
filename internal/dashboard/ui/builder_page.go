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
	ForkHref       string
	PreviewHref    string
	ExportYAMLHref string
	PageBaseHref   string

	CommandPath    string
	CommandBinding uicommand.Binding
	// FilterCommandPath and FilterOptionPath are draft-preview-only signal
	// endpoints. They intentionally use transient event posts (rather than
	// authoring command bindings) because filter state is ephemeral and never
	// writes the authored document.
	FilterCommandPath string
	FilterOptionPath  string
}

// DashboardBuilderPage renders the document shell for the draft dashboard
// authoring route. Authored pages, visuals, and semantic fields remain in the
// stream bootstrap; this document contains only identity attributes and the
// injected action bridges.
func DashboardBuilderPage(envelope uisignals.DashboardBuilderEnvelope, csrfToken string, actions DashboardBuilderActionBindings, providers ...webpage.Provider) g.Node {
	builder := envelope.Builder
	layout := builderFocusLayout(firstProvider(providers), webpage.Context{
		Active:       "dashboards",
		SectionID:    builder.DashboardID,
		SectionTitle: builder.Title,
		PageTitle:    "Builder",
		Compact:      true,
	})

	updates := dashboardBuilderUpdatesURL(builder)
	attrs := []g.Node{
		g.Attr("slot", "page"),
		g.Attr("dashboard-id", builder.DashboardID),
		g.Attr("draft-id", builder.DraftID),
		builderCommandAction(actions),
		builderFilterCommandAction(actions),
		builderFilterOptionsAction(actions),
	}
	for name, value := range map[string]string{
		"back-href":        actions.BackHref,
		"fork-href":        actions.ForkHref,
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

// DashboardDraftCreatePage and DashboardDraftForkPage are intentionally
// small, server-rendered entry points. They make the existing headless
// authoring service discoverable to browser users without accepting authored
// documents or bypassing the exact revision/authorization boundary.
func DashboardDraftCreatePage(projectID, csrfToken, action string, providers ...webpage.Provider) g.Node {
	return DashboardDraftCreatePageWithKey(projectID, csrfToken, action, "", providers...)
}

// DashboardDraftCreatePageWithKey renders the create form with a stable
// idempotency key so a retry or double submit returns the original result.
func DashboardDraftCreatePageWithKey(projectID, csrfToken, action, idempotencyKey string, providers ...webpage.Provider) g.Node {
	return DashboardDraftCreatePageWithModelsAndKey(projectID, csrfToken, action, idempotencyKey, nil, "", providers...)
}

type DashboardSemanticModelOption struct {
	ID    string
	Title string
}

// DashboardDraftCreatePageWithModelsAndKey renders governed models as a
// bounded choice. The text input fallback keeps focused tests and installations
// without a loaded catalog recoverable rather than presenting an empty select.
func DashboardDraftCreatePageWithModelsAndKey(projectID, csrfToken, action, idempotencyKey string, models []DashboardSemanticModelOption, selectedModel string, providers ...webpage.Provider) g.Node {
	layout := builderFocusLayout(firstProvider(providers), webpage.Context{Active: "dashboards", SectionTitle: "Dashboards", PageTitle: "New dashboard", Compact: true})
	modelField := g.Node(h.Input(h.ID("dashboard-semantic-model"), h.Name("semanticModel"), h.Required(), h.AutoComplete("off")))
	if len(models) > 0 {
		options := make([]g.Node, 0, len(models))
		for _, model := range models {
			id := strings.TrimSpace(model.ID)
			if id == "" {
				continue
			}
			title := strings.TrimSpace(model.Title)
			if title == "" {
				title = id
			}
			options = append(options, h.Option(h.Value(id), g.If(id == strings.TrimSpace(selectedModel), h.Selected()), g.Text(title)))
		}
		if len(options) > 0 {
			modelField = h.Select(g.Group{h.ID("dashboard-semantic-model"), h.Name("semanticModel"), h.Required()}, g.Group(options))
		}
	}
	return webpage.Render(layout, webpage.Spec{
		Title: "New dashboard", CSRFToken: csrfToken,
		UpdatesURL: "/updates?route=dashboard_builder",
		MainAttrs:  []g.Node{h.ID("dashboard-draft-create"), h.Class(webpage.RootClass)},
		Content: draftForm("New dashboard", "Start with a private draft.", action, csrfToken, idempotencyKey,
			g.Group{h.Label(h.For("dashboard-title"), g.Text("Title")), h.Input(h.ID("dashboard-title"), h.Name("title"), h.Required(), h.AutoComplete("off"))},
			g.Group{h.Label(h.For("dashboard-semantic-model"), g.Text("Governed semantic model")), modelField},
			g.Group{h.Label(h.For("dashboard-slug"), g.Text("Slug (optional)")), h.Input(h.ID("dashboard-slug"), h.Name("slug"), h.AutoComplete("off"))},
		),
	})
}

func DashboardDraftForkPage(dashboardID, csrfToken, action string, providers ...webpage.Provider) g.Node {
	return DashboardDraftForkPageWithKey(dashboardID, csrfToken, action, "", providers...)
}

func DashboardDraftForkPageWithKey(dashboardID, csrfToken, action, idempotencyKey string, providers ...webpage.Provider) g.Node {
	layout := builderFocusLayout(firstProvider(providers), webpage.Context{Active: "dashboards", SectionID: dashboardID, SectionTitle: dashboardID, PageTitle: "Fork draft", Compact: true})
	return webpage.Render(layout, webpage.Spec{
		Title: "Fork dashboard draft", CSRFToken: csrfToken,
		UpdatesURL: "/updates?route=dashboard_builder&dashboard=" + url.QueryEscape(dashboardID),
		MainAttrs:  []g.Node{h.ID("dashboard-draft-fork"), h.Class(webpage.RootClass)},
		Content: draftForm("Fork dashboard as draft", "Create a private draft from the governed project source.", action, csrfToken, idempotencyKey,
			g.Group{h.Input(h.Type("hidden"), h.Name("dashboardId"), h.Value(dashboardID)), h.Label(h.For("fork-title"), g.Text("Title (optional)")), h.Input(h.ID("fork-title"), h.Name("title"), h.AutoComplete("off"))},
			g.Group{h.Label(h.For("fork-slug"), g.Text("Slug (optional)")), h.Input(h.ID("fork-slug"), h.Name("slug"), h.AutoComplete("off"))},
		),
	})
}

func draftForm(title, hint, action, csrfToken, idempotencyKey string, fields ...g.Node) g.Node {
	return h.Main(h.Class("lv-draft-form"), h.Section(
		h.H1(g.Text(title)), h.P(g.Text(hint)),
		h.Form(h.Method("post"), h.Action(action), g.Group(append(fields,
			h.Input(h.Type("hidden"), h.Name("gorilla.csrf.Token"), h.Value(csrfToken)),
			h.Input(h.Type("hidden"), h.Name("idempotencyKey"), h.Value(idempotencyKey)),
		)), h.Button(h.Type("submit"), g.Text("Continue"))),
	))
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
		"builder":                    envelope.Builder,
		"builderVisuals":             envelope.BuilderVisuals,
		"runtime":                    envelope.Runtime,
		"status":                     envelope.Status,
		"builderFilterContract":      envelope.BuilderFilterContract,
		"builderFilterState":         envelope.BuilderFilterState,
		"builderFilterOptionPages":   envelope.BuilderFilterOptionPages,
		"builderFilterValidation":    envelope.BuilderFilterValidation,
		"builderFilterCommand":       envelope.BuilderFilterCommand,
		"builderFilterOptionRequest": envelope.BuilderFilterOptionRequest,
	}
}

func dashboardBuilderUpdatesURL(builder uisignals.DashboardBuilderSignal) string {
	values := url.Values{}
	values.Set("route", string(uisignals.RouteKindDashboardBuilder))
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
		// Include runtime identity with durable builder intents so the command
		// response can preserve the same client/stream/page context in its
		// replacement patch.
		value += " " + uiactions.CommandPost(actions.CommandBinding, actions.CommandPath, "builderCommand", "runtime")
	}
	return g.Attr("data-on:lv-builder-command", value)
}

func builderFilterCommandAction(actions DashboardBuilderActionBindings) g.Node {
	value := "$builderFilterCommand = evt.detail;"
	if strings.TrimSpace(actions.FilterCommandPath) != "" {
		value += " " + uiactions.EventPost(actions.FilterCommandPath, "builder", "runtime", "builderFilterCommand")
	}
	return g.Attr("data-on:lv-builder-filter-command", value)
}

func builderFilterOptionsAction(actions DashboardBuilderActionBindings) g.Node {
	value := "$builderFilterOptionRequest = evt.detail;"
	if strings.TrimSpace(actions.FilterOptionPath) != "" {
		value += " " + uiactions.EventPost(actions.FilterOptionPath, "builder", "runtime", "builderFilterOptionRequest")
	}
	return g.Attr("data-on:lv-builder-filter-options-request", value)
}
