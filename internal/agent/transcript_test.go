package agent

import "testing"

func TestTranscriptFormatsToolInputAndToonResult(t *testing.T) {
	transcript := transcriptFromMessages("conv_1", []Message{
		{
			ID:          "assistant_1",
			Role:        MessageRoleAssistant,
			ContentJSON: `{"tool_calls":[{"id":"call_1","name":"list_workspaces","arguments":{"limit":2}}]}`,
		},
		{
			ID:          "tool_1",
			Role:        MessageRoleTool,
			ToolCallID:  "call_1",
			ToolName:    "list_workspaces",
			ContentText: "items[2]{id,title}:\n  sales,Sales\n  ops,Operations\ncount: 2",
		},
	})

	if len(transcript) != 1 {
		t.Fatalf("transcript len = %d, want merged tool item: %#v", len(transcript), transcript)
	}
	if transcript[0].InputFormat != "json" {
		t.Fatalf("input format = %q, want json", transcript[0].InputFormat)
	}
	if transcript[0].ResultFormat != "toon" {
		t.Fatalf("result format = %q, want toon", transcript[0].ResultFormat)
	}
}

func TestTranscriptFormatsJSONToolAndArtifactResults(t *testing.T) {
	transcript := transcriptFromMessages("conv_1", []Message{
		{
			ID:          "tool_json",
			Role:        MessageRoleTool,
			ToolCallID:  "call_json",
			ToolName:    "list_dashboards",
			ContentText: `{"summary":"Found dashboards"}`,
		},
		{
			ID:          "tool_artifact",
			Role:        MessageRoleTool,
			ToolCallID:  "call_artifact",
			ToolName:    "query_visual",
			ContentText: `{"type":"bar","id":"agent_visual_123","signal":"visuals.agent_visual_123","summary":"Created chart."}`,
		},
	})

	if len(transcript) != 2 {
		t.Fatalf("transcript len = %d, want 2: %#v", len(transcript), transcript)
	}
	for _, item := range transcript {
		if item.ResultFormat != "json" {
			t.Fatalf("%s result format = %q, want json", item.ID, item.ResultFormat)
		}
	}
}

func TestTranscriptProjectsResolvedReferencesOntoUserTurn(t *testing.T) {
	transcript := transcriptFromMessages("conv_1", []Message{{
		ID:          "user_1",
		RunID:       "run_1",
		Role:        MessageRoleUser,
		ContentText: "Why did revenue fall?",
		ContentJSON: `{"turn_context":{"surface":"dashboard","references":[{"reference":{"kind":"dashboard","id":"dashboard_sales"},"name":"Sales dashboard","resource":{"id":"project_demo","name":"Demo"},"hierarchy":["Demo","Sales dashboard"],"href":"/dashboards/dashboard_sales","locations":[],"context":["current_page"]}]}}`,
	}})

	if len(transcript) != 1 || len(transcript[0].References) != 1 {
		t.Fatalf("user turn references = %#v", transcript)
	}
	reference := transcript[0].References[0]
	if reference.Reference.Kind != "dashboard" || reference.Name != "Sales dashboard" {
		t.Fatalf("reference = %#v", reference)
	}
	if reference.VisualID != "" {
		t.Fatalf("transcript reference exposed model-only enrichment: %#v", reference)
	}
	if got := reference.Hierarchy; len(got) != 2 || got[1] != "Sales dashboard" {
		t.Fatalf("hierarchy = %#v", got)
	}
}
