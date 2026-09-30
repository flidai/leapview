package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent/contracts"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

func TestComposeChatDashboardRequiresOwnedConversationAndExplicitVisualList(t *testing.T) {
	ownerChecked := false
	provider := DashboardComposeProvider{
		AuthorizeConversation: func(context.Context, Scope) error {
			ownerChecked = true
			return nil
		},
	}
	scope := Scope{ProjectID: "sales", PrincipalID: "principal-1", ConversationID: "conversation-1"}
	if definitions := provider.Definitions(Scope{PrincipalID: "principal-1"}); len(definitions) != 0 {
		t.Fatalf("Definitions() without conversation = %#v, want omitted tool", definitions)
	}
	missingVisuals := provider.Run(context.Background(), scope, agentcore.ToolCall{
		ID: "missing-visuals", Arguments: json.RawMessage(`{"title":"Finance","semanticModelId":"commerce"}`),
	})
	if !missingVisuals.IsError || !ownerChecked {
		t.Fatalf("missing visuals result = %#v, owner checked %t", missingVisuals, ownerChecked)
	}

	denied := DashboardComposeProvider{
		AuthorizeConversation: func(context.Context, Scope) error { return errors.New("not owner") },
	}
	deniedResult := denied.Run(context.Background(), scope, agentcore.ToolCall{
		ID: "denied", Arguments: json.RawMessage(`{"title":"Finance","semanticModelId":"commerce","visuals":[]}`),
	})
	if !deniedResult.IsError || deniedResult.DisplayContent != nil {
		t.Fatalf("non-owner compose result = %#v, want error with no preview", deniedResult)
	}
}

func TestComposeChatDashboardAllowsOnlyExplicitEmptySetAfterResolvingModel(t *testing.T) {
	var resolved int
	provider := DashboardComposeProvider{
		AuthorizeConversation: func(context.Context, Scope) error { return nil },
		Visual: VisualProvider{
			Resolve: func(_ context.Context, _ Scope, id projectgraph.ResourceID, kind projectgraph.Kind, capability access.Capability) (projectgraph.ResourceID, error) {
				resolved++
				if kind != projectgraph.KindSemanticModel || capability != access.CapabilityResourceUse {
					t.Fatalf("resolver request = (%q, %q), want semantic model RESOURCE_USE", kind, capability)
				}
				return id, nil
			},
			SemanticModel: func(_, id string) (*semanticmodel.Model, bool) {
				if id != "commerce" {
					t.Fatalf("semantic model ID = %q, want commerce", id)
				}
				return testAgentModel(), true
			},
			QueryDefinition: func(context.Context, string, dashboarddefinition.Definition, string, string, dashboard.Filters) (visualizationir.VisualizationEnvelope, error) {
				t.Fatal("empty draft unexpectedly queried a visual")
				return visualizationir.VisualizationEnvelope{}, nil
			},
		},
	}
	scope := Scope{ProjectID: "sales", PrincipalID: "principal-1", ConversationID: "conversation-1"}
	catalog, err := agentcore.NewToolCatalog(provider.Definitions(scope))
	if err != nil {
		t.Fatalf("compile compose tool schemas: %v", err)
	}
	result, err := catalog.Execute(context.Background(), agentcore.ToolCall{
		Name: ComposeChatDashboardToolName, ID: "empty-draft", Arguments: json.RawMessage(`{"title":"Finance","semanticModelId":"commerce","visuals":[]}`),
	})
	if err != nil {
		t.Fatalf("execute empty composition: %v", err)
	}
	content, ok := result.Content.(contracts.ComposeChatDashboardResult)
	if result.IsError || !ok || content.ID != "empty-draft" || len(content.Visuals) != 0 || resolved != 1 {
		t.Fatalf("empty composition = %#v, resolved %d, want validated empty draft", result, resolved)
	}
	display, ok := result.DisplayContent.(chatDashboardDraftDisplay)
	if !ok || display.Patch == nil || display.Patch["visuals"] == nil || len(display.Patch["visuals"]) != 0 {
		t.Fatalf("empty draft preview = %#v, want explicit empty visuals patch", result.DisplayContent)
	}
}

func TestComposeChatDashboardFailureDiscardsEarlierVisualPreview(t *testing.T) {
	fixture, err := os.ReadFile("../../../api/visualization/conformance/cartesian-inline.json")
	if err != nil {
		t.Fatalf("read visualization conformance fixture: %v", err)
	}
	var template visualizationir.VisualizationEnvelope
	if err := json.Unmarshal(fixture, &template); err != nil {
		t.Fatalf("decode visualization conformance fixture: %v", err)
	}
	queryCalls := 0
	provider := DashboardComposeProvider{
		AuthorizeConversation: func(context.Context, Scope) error { return nil },
		Visual: VisualProvider{
			Resolve: func(_ context.Context, _ Scope, id projectgraph.ResourceID, _ projectgraph.Kind, _ access.Capability) (projectgraph.ResourceID, error) {
				return id, nil
			},
			SemanticModel: func(string, string) (*semanticmodel.Model, bool) { return testAgentModel(), true },
			QueryMetadata: func(context.Context, string, string) VisualQueryMetadata {
				return VisualQueryMetadata{ServingSnapshot: "snapshot-1"}
			},
			QueryDefinition: func(_ context.Context, _ string, _ dashboarddefinition.Definition, _, visualID string, _ dashboard.Filters) (visualizationir.VisualizationEnvelope, error) {
				queryCalls++
				if queryCalls == 2 {
					return visualizationir.VisualizationEnvelope{}, errors.New("second query failed")
				}
				envelope := template
				envelope.VisualID = visualID
				return envelope, nil
			},
		},
	}
	visual := testAgentVisual("bar")
	visual.Title = strPtr("Revenue")
	input := ComposeChatDashboardInput{Title: "Finance", SemanticModelID: "commerce", Visuals: []ChatDashboardVisualInput{
		{ID: "revenue", Visual: visual}, {ID: "comparison", Visual: visual},
	}}
	arguments, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal composition input: %v", err)
	}
	result := provider.Run(context.Background(), Scope{ProjectID: "sales", PrincipalID: "principal-1", ConversationID: "conversation-1"}, agentcore.ToolCall{ID: "compose-all", Arguments: arguments})
	if !result.IsError || result.DisplayContent != nil || queryCalls != 2 {
		t.Fatalf("partial composition = %#v after %d queries, want one error without display content", result, queryCalls)
	}
}

func TestChatDashboardDraftRevisionIncludesSuccessfulToolCallIdentity(t *testing.T) {
	input := ComposeChatDashboardInput{Title: "Finance", SemanticModelID: "commerce", Visuals: []ChatDashboardVisualInput{}}
	one, err := ChatDashboardDraftRevision("compose-one", input)
	if err != nil {
		t.Fatalf("revision one: %v", err)
	}
	two, err := ChatDashboardDraftRevision("compose-two", input)
	if err != nil {
		t.Fatalf("revision two: %v", err)
	}
	if one == two {
		t.Fatalf("revisions for distinct preview invocations are both %q", one)
	}
}

func TestComposeChatDashboardBoundsSourceForFuturePromptContext(t *testing.T) {
	largeTitle := strings.Repeat("x", maxChatDashboardVisualContextBytes)
	visual := testAgentVisual("bar")
	visual.Title = &largeTitle
	input := ComposeChatDashboardInput{
		Title: "Finance", SemanticModelID: "commerce",
		Visuals: []ChatDashboardVisualInput{{ID: "revenue", Visual: visual}},
	}
	if _, err := validateChatDashboardDraftContextSize("compose-large", input); err == nil {
		t.Fatal("visual definition exceeding the future context item budget was accepted")
	}
}

func TestComposeChatDashboardRequiresHumanReadableVisualTitle(t *testing.T) {
	visual := testAgentVisual("bar")
	input := ComposeChatDashboardInput{
		Title: "Finance", SemanticModelID: "commerce",
		Visuals: []ChatDashboardVisualInput{{ID: "revenue", Visual: visual}},
	}
	arguments, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal composition input: %v", err)
	}
	provider := DashboardComposeProvider{
		AuthorizeConversation: func(context.Context, Scope) error { return nil },
		Visual: VisualProvider{
			Resolve: func(_ context.Context, _ Scope, id projectgraph.ResourceID, _ projectgraph.Kind, _ access.Capability) (projectgraph.ResourceID, error) {
				return id, nil
			},
			SemanticModel: func(string, string) (*semanticmodel.Model, bool) { return testAgentModel(), true },
			QueryDefinition: func(context.Context, string, dashboarddefinition.Definition, string, string, dashboard.Filters) (visualizationir.VisualizationEnvelope, error) {
				t.Fatal("untitled visual unexpectedly queried")
				return visualizationir.VisualizationEnvelope{}, nil
			},
		},
	}
	result := provider.Run(context.Background(), Scope{ProjectID: "sales", PrincipalID: "principal-1", ConversationID: "conversation-1"}, agentcore.ToolCall{ID: "compose-title", Arguments: arguments})
	if !result.IsError || !strings.Contains(dashboardComposeToolErrorMessage(result), "human-readable chart title") || result.DisplayContent != nil {
		t.Fatalf("composition without visual title = %#v, want bounded invalid-arguments error", result)
	}
}
