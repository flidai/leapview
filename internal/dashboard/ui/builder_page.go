package ui

import (
	"net/url"
	"strings"

	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
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
	AgentCommands     AgentCommandBindings
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
		g.Attr("data-indicator", "agentTurnPending"),
		builderCommandAction(actions),
		builderFilterCommandAction(actions),
		builderFilterOptionsAction(actions),
	}
	agentEnabled := strings.TrimSpace(actions.AgentCommands.CreateConversation.OperationID()) != "" && strings.TrimSpace(actions.AgentCommands.CreateRun.OperationID()) != ""
	if agentEnabled {
		attrs = append(attrs,
			g.Attr("data-on:lv-chat-submit", "$agent.composer.value = evt.detail.input; $agentContext.references = evt.detail.references; $agentContext.filters = $builderFilterState; $agentContext.generation = $status.generation; "+uiactions.CommandPostConditional("$agent.activeConversationId", []uicommand.Binding{actions.AgentCommands.CreateRun}, actions.AgentCommands.Workflow(), "/chats/turns", "agent", "agentContext")),
			g.Attr("data-on:lv-chat-restore", "$agent.activeConversationId = evt.detail.conversationId; "+uiactions.Get("/chats/restore", "agent")),
			g.Attr("data-on:lv-chat-new", "$agent.activeConversationId = ''; $agent.transcript = []; $agent.composer.value = ''; $agentVisuals = {}"),
		)
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

	contentAttrs := []g.Node{}
	if agentEnabled {
		contentAttrs = append(contentAttrs,
			g.Attr("data-on:lv-chat-reference-search__debounce.200ms", "$agentReferenceSearch.query = evt.detail.query; $agentReferenceSearch.requestId = evt.detail.requestId; "+uiactions.Get("/chats/references/search", "agentReferenceSearch", "agentContext")),
		)
	}
	return webpage.Render(layout, webpage.Spec{
		Title: builder.Title, CSRFToken: csrfToken,
		Scripts:      []string{"/static/dashboard-builder.js"},
		MainAttrs:    []g.Node{h.ID("dashboard-builder"), h.Class(webpage.RootClass)},
		UpdatesURL:   updates,
		ContentAttrs: contentAttrs,
		Content:      g.El("lv-dashboard-builder", attrs...),
	})
}

// DashboardDraftForkPage is a small, server-rendered entry point for the
// existing headless copy operation. Dashboard creation lives in the catalog
// modal so users keep their place while choosing the required data model.
func DashboardDraftForkPage(dashboardID, csrfToken, action string, providers ...webpage.Provider) g.Node {
	return DashboardDraftForkPageWithKey(dashboardID, csrfToken, action, "", providers...)
}

func DashboardDraftForkPageWithKey(dashboardID, csrfToken, action, idempotencyKey string, providers ...webpage.Provider) g.Node {
	layout := builderFocusLayout(firstProvider(providers), webpage.Context{Active: "dashboards", SectionID: dashboardID, SectionTitle: dashboardID, PageTitle: "Make a copy", Compact: true})
	return webpage.Render(layout, webpage.Spec{
		Title:      "Make a dashboard copy",
		CSRFToken:  csrfToken,
		UpdatesURL: "/updates?route=catalog",
		MainAttrs:  []g.Node{h.ID("dashboard-draft-fork"), h.Class(webpage.RootClass)},
		Content: draftForm("Make a copy", "Create an editable copy in My dashboards.", action, csrfToken, idempotencyKey,
			g.Group{h.Input(h.Type("hidden"), h.Name("dashboardId"), h.Value(dashboardID)), h.Label(h.For("fork-title"), g.Text("Title (optional)")), h.Input(h.ID("fork-title"), h.Name("title"), h.AutoComplete("off"))},
			g.Group{h.Label(h.For("fork-slug"), g.Text("Slug (optional)")), h.Input(h.ID("fork-slug"), h.Name("slug"), h.AutoComplete("off"))},
		),
	})
}

func draftForm(title, hint, action, csrfToken, idempotencyKey string, fields ...g.Node) g.Node {
	return h.Div(h.Class("lv-draft-form"), h.Section(
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
	agentContext := DashboardBuilderAgentContext(envelope)
	return map[string]any{
		"agent": uisignals.ChatSignal{
			Conversations: []uisignals.ChatConversationSummary{},
			Transcript:    []uisignals.ChatTranscriptItemSignal{},
			Status: uisignals.ChatStatus{
				Enabled: false,
				Running: false,
				Error:   uisignals.Optional("Agent is not configured"),
			},
			Composer: uisignals.ComposerSignal{Disabled: true, Placeholder: "Agent is not configured"},
		},
		"agentContext":               agentContext,
		"agentReferenceSearch":       uisignals.AgentReferenceSearchSignal{Results: []uisignals.AgentReferenceSignal{}},
		"agentVisuals":               map[string]visualizationir.VisualizationEnvelope{},
		"interactionSelections":      []uisignals.DashboardInteractionSelection{},
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

// DashboardBuilderAgentContext projects the current draft selection into the
// shared dashboard-agent context without making the browser own a second
// representation of the builder state.
func DashboardBuilderAgentContext(envelope uisignals.DashboardBuilderEnvelope) uisignals.AgentContextSignal {
	builder := envelope.Builder
	pageID, pageTitle := "", ""
	if builder.SelectedPageID != nil {
		pageID = strings.TrimSpace(*builder.SelectedPageID)
	}
	for _, page := range builder.Pages {
		if pageID == "" || page.ID == pageID {
			pageID = page.ID
			pageTitle = page.Title
			break
		}
	}
	return uisignals.AgentContextSignal{
		Surface:        "dashboard",
		DashboardID:    builder.DashboardID,
		DashboardTitle: builder.Title,
		PageID:         pageID,
		PageTitle:      pageTitle,
		ModelID:        builder.SemanticModel.ID,
		Filters:        envelope.BuilderFilterState,
		ReferenceLimit: 12,
		References:     []uisignals.AgentReferenceSignal{},
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
