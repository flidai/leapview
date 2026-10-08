package agent

import (
	agentcore "github.com/flidai/leapview/pkg/agent"
	"testing"
)

func TestIncompletePromptDoesNotReportSuccess(t *testing.T) {
	for _, reason := range []agentcore.StopReason{agentcore.StopReasonTruncated, agentcore.StopReasonMaxTurns, agentcore.StopReasonMaxToolCalls, agentcore.StopReasonContextLimit} {
		if incompletePromptError(reason) == nil {
			t.Fatalf("%s incorrectly reports success", reason)
		}
	}
	if err := incompletePromptError(agentcore.StopReasonCompleted); err != nil {
		t.Fatal(err)
	}
}
