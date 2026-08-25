package module

import (
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/agent"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

func TestLiveTranscriptPreservesTextAndToolOutputOrder(t *testing.T) {
	transcript := []agent.ChatTranscriptItem{{ID: "user-1", Kind: "user", Text: "go", RunID: "run-1"}}
	apply := func(eventType agentcore.EventType, severity string, payload map[string]any) {
		transcript = applyLiveTranscriptEvent(transcript, "conversation-1", agent.EventEnvelope{
			RunID: "run-1", Type: string(eventType), Severity: severity, CreatedAt: "2026-08-24T00:00:00Z", Payload: payload,
		})
	}
	add := func(id, kind string, ordinal int64, callID string) {
		apply(agentcore.EventTypeOutputPartAdded, string(agentcore.SeverityInfo), map[string]any{
			"output_part_id": id, "output_kind": kind, "output_ordinal": float64(ordinal),
			"parent_message_id": "message-" + id, "tool_call_id": callID, "tool_name": "lookup",
		})
	}

	add("text-1", string(agentcore.OutputPartKindText), 0, "")
	apply(agentcore.EventTypeOutputTextDelta, string(agentcore.SeverityInfo), map[string]any{"output_part_id": "text-1", "delta": "text 1"})
	apply(agentcore.EventTypeOutputPartDone, string(agentcore.SeverityInfo), map[string]any{"output_part_id": "text-1", "content": "text 1"})
	add("tool-2", string(agentcore.OutputPartKindTool), 1, "call-2")
	apply(agentcore.EventTypeToolExecutionStart, string(agentcore.SeverityInfo), map[string]any{"output_part_id": "tool-2"})
	apply(agentcore.EventTypeToolExecutionEnd, string(agentcore.SeverityInfo), map[string]any{"output_part_id": "tool-2"})
	add("text-3", string(agentcore.OutputPartKindText), 2, "")
	apply(agentcore.EventTypeOutputTextDelta, string(agentcore.SeverityInfo), map[string]any{"output_part_id": "text-3", "delta": "text 3"})
	apply(agentcore.EventTypeOutputTextDelta, string(agentcore.SeverityInfo), map[string]any{"output_part_id": "text-3", "delta": "text 4"})
	apply(agentcore.EventTypeOutputPartDone, string(agentcore.SeverityInfo), map[string]any{"output_part_id": "text-3", "content": "text 3text 4"})
	add("tool-5", string(agentcore.OutputPartKindTool), 3, "call-5")

	gotIDs := make([]string, 0, len(transcript))
	for _, item := range transcript {
		gotIDs = append(gotIDs, item.ID)
	}
	if want := []string{"user-1", "text-1", "tool-2", "text-3", "tool-5"}; !reflect.DeepEqual(gotIDs, want) {
		t.Fatalf("transcript IDs = %v, want %v", gotIDs, want)
	}
	if transcript[1].Markdown != "text 1" || transcript[2].Status != "complete" || transcript[3].Markdown != "text 3text 4" {
		t.Fatalf("ordered transcript = %#v", transcript)
	}
}

func TestLiveTranscriptInsertsDeclaredPartsByOrdinalAndDeduplicates(t *testing.T) {
	transcript := []agent.ChatTranscriptItem{{ID: "user-1", Kind: "user", RunID: "run-1"}}
	add := func(id string, ordinal int64) {
		transcript = applyLiveTranscriptEvent(transcript, "conversation-1", agent.EventEnvelope{
			RunID: "run-1", Type: string(agentcore.EventTypeOutputPartAdded), Payload: map[string]any{
				"output_part_id": id, "output_kind": string(agentcore.OutputPartKindText), "output_ordinal": float64(ordinal),
			},
		})
	}

	add("part-2", 2)
	add("part-1", 1)
	add("part-1", 1)

	got := []string{transcript[0].ID, transcript[1].ID, transcript[2].ID}
	if want := []string{"user-1", "part-1", "part-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("transcript IDs = %v, want %v", got, want)
	}
}

func TestLiveTranscriptMakesToolInputAndResultInspectableDuringRun(t *testing.T) {
	transcript := []agent.ChatTranscriptItem{{ID: "user-1", Kind: "user", RunID: "run-1"}}
	apply := func(eventType agentcore.EventType, severity string, payload map[string]any) {
		transcript = applyLiveTranscriptEvent(transcript, "conversation-1", agent.EventEnvelope{
			RunID: "run-1", Type: string(eventType), Severity: severity, Payload: payload,
		})
	}
	part := map[string]any{
		"output_part_id": "part-1", "output_kind": string(agentcore.OutputPartKindTool), "output_ordinal": float64(0),
		"tool_call_id": "call-1", "tool_name": "catalog_list", "tool_arguments": `{"kind":"dashboard","limit":2}`,
	}
	apply(agentcore.EventTypeOutputPartAdded, string(agentcore.SeverityInfo), part)
	if got := transcript[1]; got.Status != "pending" || got.ArgumentsJSON == "" || got.InputJSON == "" {
		t.Fatalf("declared live tool = %#v", got)
	}
	apply(agentcore.EventTypeToolExecutionStart, string(agentcore.SeverityInfo), map[string]any{
		"output_part_id": "part-1", "tool_arguments": `{"kind":"dashboard","limit":2}`,
	})
	if transcript[1].Status != "running" || transcript[1].ResultJSON != "" {
		t.Fatalf("running live tool = %#v", transcript[1])
	}
	apply(agentcore.EventTypeToolExecutionEnd, string(agentcore.SeverityInfo), map[string]any{
		"output_part_id": "part-1", "tool_result": "items[1]{id}:\n  dashboard:sales",
	})
	if got := transcript[1]; got.Status != "complete" || got.ResultJSON == "" || got.ResultFormat != "toon" {
		t.Fatalf("completed live tool = %#v", got)
	}
}
