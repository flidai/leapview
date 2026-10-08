package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	previewservice "github.com/flidai/leapview/internal/dashboard/authoring/preview"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

type previewOutcomeAuthoring struct {
	projectAuthoringFake
	preview previewservice.Preview
}

func (f *previewOutcomeAuthoring) Preview(context.Context, previewservice.PreviewRequest) (previewservice.Preview, error) {
	return f.preview, nil
}

func TestDashboardPreviewRetainsRuntimeFailuresInToolTranscript(t *testing.T) {
	for _, format := range []agentcore.ToolOutputFormat{agentcore.ToolOutputTOON, agentcore.ToolOutputJSON} {
		t.Run(string(format), func(t *testing.T) {
			message := "Chart query failed"
			definition, err := compileAgentVisual(agentVisualInput{Visual: testAgentVisual("bar"), Model: "commerce"}, testAgentModel(), "chart")
			if err != nil {
				t.Fatal(err)
			}
			envelope, err := visualizationruntime.ErrorEnvelopeFromDefinition(definition.Visualizations["chart"], errors.New(message), 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			app := &previewOutcomeAuthoring{preview: previewservice.Preview{
				Revision:   createResultForTest().Revision,
				Definition: definition,
				PagePatch: dashboard.Patch{Status: dashboard.Status{Error: "Page query failed"}, Visuals: map[string]visualizationir.VisualizationEnvelope{
					"chart": envelope,
				}},
			}}
			provider := DashboardAuthoringProvider{Application: app, ProjectID: projectIDForTest(), Resolve: (&projectResolverFake{}).Resolve}
			tool := definitionByName(provider.Definitions(Scope{PrincipalID: "principal"}), PreviewDashboardDraftToolName)
			calls := 0
			model := agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
				calls++
				if calls == 1 {
					return agentcore.ModelResponse{FinishReason: agentcore.FinishReasonToolCalls, ToolCalls: []agentcore.ToolCall{{ID: "preview", Name: tool.Name, Arguments: json.RawMessage(`{"dashboardId":"dashboard_sales","draftId":"0198f2c0-7c7a-7f00-8a11-000000000001","expectedRevision":{"revisionId":"revision_1","number":1,"contentHash":"hash"},"page":"overview"}`)}}}, nil
				}
				return agentcore.ModelResponse{Content: "Preview checked.", FinishReason: agentcore.FinishReasonStop}, nil
			})
			a, err := agentcore.New(agentcore.Definition{Name: "preview-test", SystemPrompt: "Test preview retention.", Model: model, Tools: []agentcore.ToolDefinition{tool}, ToolOutput: agentcore.ToolOutputConfig{Format: format}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.Prompt(t.Context(), agentcore.PromptRequest{Input: "Preview dashboard"}); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, item := range a.Transcript() {
				if item.Role != agentcore.RoleTool {
					continue
				}
				found = true
				if item.IsError || !strings.Contains(item.Content, "visualErrors") || !strings.Contains(item.Content, message) || !strings.Contains(item.Content, "Page query failed") {
					t.Fatalf("retained preview lost runtime errors: %s", item.Content)
				}
				if strings.Contains(item.Content, "pagePatch") {
					t.Fatal("retained preview unexpectedly includes the render payload")
				}
			}
			if !found {
				t.Fatal("preview tool result was not retained")
			}
		})
	}
}
