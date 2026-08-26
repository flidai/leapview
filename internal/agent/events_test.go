package agent

import (
	"testing"

	agentcore "github.com/flidai/leapview/pkg/agent"
)

func TestToolLifecyclePayloadPreservesInspectableInputAndResult(t *testing.T) {
	payload := eventPayload(eventPayloadJSON(agentcore.Event{
		Type:          agentcore.EventTypeToolExecutionEnd,
		ToolArguments: `{"kind":"dashboard"}`,
		ToolResult:    "items[1]{id}:\n  dashboard:sales",
		ToolDisplay:   `{"type":"code","language":"yaml","content":"kind: Dashboard\n"}`,
	}))
	if payload["tool_arguments"] != `{"kind":"dashboard"}` {
		t.Fatalf("tool arguments = %#v", payload["tool_arguments"])
	}
	if payload["tool_result"] != "items[1]{id}:\n  dashboard:sales" {
		t.Fatalf("tool result = %#v", payload["tool_result"])
	}
	if payload["tool_display"] != `{"type":"code","language":"yaml","content":"kind: Dashboard\n"}` {
		t.Fatalf("tool display = %#v", payload["tool_display"])
	}
}
