package ui

import (
	"bytes"
	"encoding/json"
	"net/url"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/dashboard"
	uiactions "github.com/flidai/leapview/internal/platform/web/actions"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	catalog "github.com/flidai/leapview/internal/project/navigation"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
	g "maragu.dev/gomponents"
)

// DataExplorerAgentBootstrap is agent-owned state projected into the data
// explorer without making the project module depend on agent signal types.
type DataExplorerAgentBootstrap struct {
	Agent   any
	Visuals any
}

type DataExplorerAgentCommandBindings struct {
	CreateConversation uicommand.Binding
	CreateRun          uicommand.Binding
	CancelRun          uicommand.Binding
}

func (commands DataExplorerAgentCommandBindings) Workflow() []uicommand.Binding {
	return []uicommand.Binding{commands.CreateConversation, commands.CreateRun}
}

func DataExplorerPage(catalog catalog.Catalog, page uisignals.DataExplorerPageSignal, explorer uisignals.DataExplorerSignal, csrfToken string, providers ...webpage.Provider) g.Node {
	return DataExplorerPageWithAgent(catalog, page, explorer, DataExplorerAgentBootstrap{}, DataExplorerAgentCommandBindings{}, csrfToken, providers...)
}

func DataExplorerPageWithAgent(_ catalog.Catalog, page uisignals.DataExplorerPageSignal, explorer uisignals.DataExplorerSignal, agent DataExplorerAgentBootstrap, commands DataExplorerAgentCommandBindings, csrfToken string, providers ...webpage.Provider) g.Node {
	layout := webpage.Resolve(firstProvider(providers), webpage.Context{Active: "data-explorer", PageTitle: page.Title})
	explorerUpdatesURL := dataExplorerUpdatesURL(explorer.Command)
	agentTurn := "$agent.composer.value = evt.detail.input; $agent.composer.editMessageId = evt.detail.editMessageId || ''; $agentContext.references = evt.detail.references; " + uiactions.CommandPostConditional("$agent.activeConversationId", []uicommand.Binding{commands.CreateRun}, commands.Workflow(), "/chats/turns", "agent", "agentContext")
	agentRestore := "$agent.activeConversationId = evt.detail.conversationId; " + uiactions.Get("/chats/restore", "agent")
	return webpage.Render(layout, webpage.Spec{
		Title: page.Title, CSRFToken: csrfToken, Scripts: []string{"/static/data-explorer.js"},
		UpdatesURL: explorerUpdatesURL,
		Content: g.El("lv-data-explorer",
			g.Attr("slot", "page"),
			g.Attr("data-indicator", "agentTurnPending"),
			g.Attr("data-on:lv-data-explorer-command", "$dataExplorerCommand = evt.detail; "+uiactions.EventPost("/explore/command")),
			g.Attr("data-on:lv-chat-submit", agentTurn),
			g.Attr("data-on:lv-chat-stop", uiactions.CommandPost(commands.CancelRun, "/chats/stop", "agent", "agentContext")),
			g.Attr("data-on:lv-chat-restore", agentRestore),
			g.Attr("data-on:lv-chat-new", "$agent.activeConversationId = ''; $agent.transcript = []; $agent.composer.value = ''; $agentVisuals = {}"),
		),
		ContentAttrs: []g.Node{
			g.Attr("data-on:lv-chat-reference-search__debounce.200ms", "$agentReferenceSearch.query = evt.detail.query; $agentReferenceSearch.requestId = evt.detail.requestId; "+uiactions.Get("/chats/references/search", "agentReferenceSearch", "agentContext")),
		},
	})
}

func dataExplorerUpdatesURL(command uisignals.DataExplorerCommand) string {
	values := url.Values{"route": {string(uisignals.RouteKindData)}, "surface": {"explore"}}
	if uisignals.ValueOrZero(command.Mode) != "explore" || command.Explore == nil {
		if object := uisignals.ValueOrZero(command.ObjectKey); object != "" {
			values.Set("object", object)
		}
		return "/updates?" + values.Encode()
	}
	explore := command.Explore
	spec := dataExplorerCanonicalSpec(*explore)
	values.Set("mode", "explore")
	values.Set("v", "2")
	encoded, _ := canonicalExplorationJSON(spec)
	values.Set("state", string(encoded))
	return "/updates?" + values.Encode()
}

func canonicalExplorationJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

func dataExplorerCanonicalSpec(command uisignals.DataExploreCommand) exploration.ExplorationSpec {
	spec := command.Spec
	if spec.SchemaVersion != 0 || spec.ModelID != "" {
		return spec
	}
	spec = exploration.ExplorationSpec{SchemaVersion: 1, ModelID: uisignals.ValueOrZero(command.SemanticModelID), DatasetID: command.DatasetID, Limit: int32(command.Limit)}
	if spec.Limit == 0 {
		spec.Limit = int32(dataExplorerDefaultLimit)
	}
	spec.Dimensions = make([]exploration.ExplorationDimensionRef, 0, len(command.Dimensions))
	for _, field := range command.Dimensions {
		spec.Dimensions = append(spec.Dimensions, exploration.ExplorationDimensionRef{Field: field})
	}
	spec.Metrics = make([]exploration.ExplorationMetricRef, 0, len(command.Metrics))
	for _, field := range command.Metrics {
		spec.Metrics = append(spec.Metrics, exploration.ExplorationMetricRef{Field: field})
	}
	spec.Filters = make([]exploration.ExplorationFilter, 0, len(command.Filters))
	for _, filter := range command.Filters {
		values := make([]exploration.ExplorationFilterValue, 0, len(filter.Values))
		for _, value := range filter.Values {
			values = append(values, exploration.ExplorationFilterValue{Value: &exploration.StringExplorationFilterValue{ExplorationFilterValueBase: exploration.ExplorationFilterValueBase{Kind: "string"}, Kind: "string", Value: value}})
		}
		var expression exploration.ExplorationFilterExpressionVariant
		switch filter.Operator {
		case "is_null", "is_not_null":
			expression = &exploration.NullCheckExplorationFilterExpression{ExplorationFilterExpressionBase: exploration.ExplorationFilterExpressionBase{Kind: "null_check"}, Kind: "null_check", Operator: filter.Operator}
		case "in", "not_in":
			expression = &exploration.SetExplorationFilterExpression{ExplorationFilterExpressionBase: exploration.ExplorationFilterExpressionBase{Kind: "set"}, Kind: "set", Operator: filter.Operator, Values: values}
		default:
			if len(values) == 0 {
				continue
			}
			expression = &exploration.ComparisonExplorationFilterExpression{ExplorationFilterExpressionBase: exploration.ExplorationFilterExpressionBase{Kind: "comparison"}, Kind: "comparison", Operator: filter.Operator, Value: values[0]}
		}
		spec.Filters = append(spec.Filters, exploration.ExplorationFilter{Field: filter.Field, DatasetID: filter.DatasetID, Expression: exploration.ExplorationFilterExpression{Value: expression}})
	}
	spec.Sort = make([]exploration.ExplorationSort, 0, len(command.Sort))
	for _, sort := range command.Sort {
		spec.Sort = append(spec.Sort, exploration.ExplorationSort{Field: sort.Field, Direction: exploration.ExplorationSortDirection(sort.Direction)})
	}
	if command.Time != nil {
		spec.Time = &exploration.ExplorationTimeSelection{Field: command.Time.Field, Grain: exploration.ExplorationTimeGrain(command.Time.Grain), Alias: command.Time.Alias}
	}
	return spec
}

const dataExplorerDefaultLimit = int64(100)

func DataExplorerBootstrapSignals(catalog catalog.Catalog, page uisignals.DataExplorerPageSignal, explorer uisignals.DataExplorerSignal, providers ...webpage.Provider) map[string]any {
	return DataExplorerBootstrapSignalsWithAgent(catalog, page, explorer, DataExplorerAgentBootstrap{}, providers...)
}

func DataExplorerBootstrapSignalsWithAgent(_ catalog.Catalog, page uisignals.DataExplorerPageSignal, explorer uisignals.DataExplorerSignal, agent DataExplorerAgentBootstrap, providers ...webpage.Provider) map[string]any {
	layout := webpage.Resolve(firstProvider(providers), webpage.Context{Active: "data-explorer", PageTitle: page.Title})
	context := DataExplorerAgentContext(page, explorer)
	if agent.Agent == nil {
		agent.Agent = uisignals.ChatSignal{
			Conversations: []uisignals.ChatConversationSummary{}, Transcript: []uisignals.ChatTranscriptItemSignal{},
			Status: uisignals.ChatStatus{}, Composer: uisignals.ComposerSignal{Disabled: true, Placeholder: "Agent is not configured."},
		}
	}
	if agent.Visuals == nil {
		agent.Visuals = map[string]any{}
	}
	return webpage.WithSignal(layout, map[string]any{
		"page":                page,
		"dataExplorer":        explorer,
		"dataExplorerCommand": explorer.Command,
		"status":              dashboard.Status{},
		"agent":               agent.Agent,
		"agentContext":        context,
		"agentReferenceSearch": uisignals.AgentReferenceSearchSignal{
			Results: []uisignals.AgentReferenceSignal{},
		},
		"agentVisuals": agent.Visuals,
	})
}

func DataExplorerAgentContext(page uisignals.DataExplorerPageSignal, explorer uisignals.DataExplorerSignal) uisignals.AgentContextSignal {
	command := explorer.Explore.Command
	spec := dataExplorerCanonicalSpec(command)
	modelID := spec.ModelID
	datasetID := uisignals.ValueOrZero(spec.DatasetID)
	return uisignals.AgentContextSignal{
		Surface: "data", ModelID: modelID, DatasetID: &datasetID,
		DashboardID: "", DashboardTitle: "", PageID: "", PageTitle: "",
		Exploration: &spec,
		Filters: uisignals.DashboardFilterState{
			AppliedControls: map[string]uisignals.DashboardAppliedFilterState{},
			DraftControls:   map[string]uisignals.DashboardFilterExpression{}, DirtyBindings: []string{},
		},
		ReferenceLimit: 12, References: []uisignals.AgentReferenceSignal{},
	}
}
