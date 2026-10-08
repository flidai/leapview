package http

import (
	"encoding/json"
	"testing"

	"github.com/flidai/leapview/internal/agent"
)

func TestHydratePreviewActionIgnoresRenderPayload(t *testing.T) {
	// The encoder emits nested page lists that its TOON decoder cannot read.
	// Navigation needs retained arguments and the preview outcome, not chart data.
	const preview = `definition:
  pages[1]:
    -
      canvas:
        height: 0
      id: overview
pagePatch:
  visuals:
    chart:
      rows[1]:
        -
          values[2]: 1,2
revision:
  number: 4
  revisionId: revision-4
semanticEvidence:
  identity:
    generationId: generation-1
`
	for _, failure := range []string{"", "error: preview failed\n", "visualErrors:\n  chart: query failed\n"} {
		t.Run(failure, func(t *testing.T) {
			item := agent.ChatTranscriptItem{RunID: "run-1", ToolCallID: "call-1", Name: "preview_dashboard_draft"}
			hydrateRetainedTool(&item, []agent.Message{
				{RunID: "other-run", ToolCallID: "call-1", Role: agent.MessageRoleTool, ContentText: `{"error":"wrong run"}`},
				{RunID: "run-1", ContentJSON: `{"tool_calls":[{"id":"call-1","arguments":{"dashboardId":"dashboard-1","page":"overview"}}]}`},
				{RunID: "run-1", ToolCallID: "call-1", Role: agent.MessageRoleTool, ContentText: preview + failure},
			})
			var result map[string]json.RawMessage
			if err := json.Unmarshal([]byte(item.ResultJSON), &result); err != nil {
				t.Fatalf("preview navigation result is not JSON: %v", err)
			}
			if len(result["revision"]) == 0 || item.ArgumentsJSON == "" {
				t.Fatal("missing retained revision or arguments")
			}
			if failure != "" && len(result["error"]) == 0 && len(result["visualErrors"]) == 0 {
				t.Fatal("preview failure was discarded")
			}
		})
	}
}

func TestActionFromCompletedEventsBeforeTranscriptCommit(t *testing.T) {
	events := []agent.Event{
		{RunID: "run-1", EventType: "tool_execution_start", PayloadJSON: `{"tool_call_id":"call-1","tool_name":"read_dashboard_source","tool_arguments":"{\"dashboardId\":\"dashboard-1\"}"}`},
		{RunID: "run-1", EventType: "tool_execution_end", PayloadJSON: `{"tool_call_id":"call-1","tool_name":"read_dashboard_source","tool_result":"dashboardId: dashboard-1\ndraftId: draft-1\n"}`},
	}
	item := actionFromCompletedEvents(events, "run-1", "call-1")
	if item == nil || item.Status != "complete" || item.ArgumentsJSON != `{"dashboardId":"dashboard-1"}` || !json.Valid([]byte(item.ResultJSON)) {
		t.Fatalf("item=%+v", item)
	}
	if actionFromCompletedEvents(events, "other-run", "call-1") != nil {
		t.Fatal("cross-run action accepted")
	}
	if actionFromCompletedEvents(events[:1], "run-1", "call-1") != nil {
		t.Fatal("unfinished tool accepted")
	}
	events[1].PayloadJSON = `{"tool_call_id":"call-1","tool_name":"read_dashboard_source","error":"failed","tool_result":"error: failed"}`
	if actionFromCompletedEvents(events, "run-1", "call-1") != nil {
		t.Fatal("failed tool accepted")
	}
}

func TestDraftActionIgnoresUndecodableDocument(t *testing.T) {
	item := agent.ChatTranscriptItem{RunID: "run-1", ToolCallID: "call-1", Name: "get_dashboard_draft"}
	hydrateRetainedTool(&item, []agent.Message{{RunID: "run-1", ToolCallID: "call-1", Role: agent.MessageRoleTool, ContentText: `lifecycle:
  id: dashboard-1
  draft:
    id: draft-1
revision:
  dashboardId: dashboard-1
  document:
    pages[1]:
      -
        id: overview
  number: 3
error: failed
`}})
	var result map[string]json.RawMessage
	if err := json.Unmarshal([]byte(item.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	if len(result["lifecycle"]) == 0 || len(result["revision"]) == 0 || len(result["error"]) == 0 {
		t.Fatalf("lost receipt: %s", item.ResultJSON)
	}
}

func TestHydrateVisualActionUsesScopedStructuredReceipt(t *testing.T) {
	item := agent.ChatTranscriptItem{
		RunID: "run-1", ToolCallID: "call-1", Name: "query_visual",
		Artifact: &agent.ChatArtifact{ID: "visual-1"},
	}
	const receipt = `{"id":"visual-1","datasetId":"dataset-1","data":[{"values":[1,2]}]}`
	hydrateRetainedTool(&item, []agent.Message{
		{RunID: "run-1", ToolCallID: "call-1", Role: agent.MessageRoleTool, ContentText: "compacted preview", ContentJSON: `{"display_content":{"result":` + receipt + `}}`},
		{RunID: "other-run", ToolCallID: "call-1", Role: agent.MessageRoleTool, ContentText: `{"datasetId":"wrong-run"}`},
		{RunID: "run-1", ToolCallID: "other-call", Role: agent.MessageRoleTool, ContentText: `{"datasetId":"wrong-call"}`},
	})
	if item.ResultJSON != receipt {
		t.Fatalf("retained visual receipt changed: %s", item.ResultJSON)
	}
}

func TestHydrateVisualActionRejectsMismatchedReceiptAndPreservesErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		receiptID  string
		failed     bool
		wantResult string
	}{
		{name: "mismatched artifact", receiptID: "other-visual", wantResult: `{"datasetId":"fallback"}`},
		{name: "failed visual", receiptID: "visual-1", failed: true, wantResult: `{"id":"visual-1","datasetId":"recorded"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := agent.ChatTranscriptItem{
				RunID: "run-1", ToolCallID: "call-1", Name: "query_visual",
				Artifact: &agent.ChatArtifact{ID: "visual-1"},
			}
			hydrateRetainedTool(&item, []agent.Message{{
				RunID: "run-1", ToolCallID: "call-1", Role: agent.MessageRoleTool, IsError: tc.failed,
				ContentText: `{"datasetId":"fallback"}`,
				ContentJSON: `{"display_content":{"result":{"id":"` + tc.receiptID + `","datasetId":"recorded"}}}`,
			}})
			if item.ResultJSON != tc.wantResult || (item.Error != "") != tc.failed {
				t.Fatalf("result=%s error=%q", item.ResultJSON, item.Error)
			}
		})
	}
}
