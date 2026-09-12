package module

import (
	"errors"
	"testing"

	agentcore "github.com/flidai/leapview/pkg/agent"
)

func TestChatTurnStatusErrorReportsMaxTurnsWithoutPromptError(t *testing.T) {
	got := chatTurnStatusError(nil, agentcore.StopReasonMaxTurns)
	if got == "" {
		t.Fatal("max-turn completion did not produce a user-facing status")
	}
	if got != "The agent reached its turn limit before producing a final answer. Ask it to continue." {
		t.Fatalf("max-turn status = %q", got)
	}
}

func TestChatTurnStatusErrorPreservesPromptErrors(t *testing.T) {
	want := errors.New("provider failed")
	if got := chatTurnStatusError(want, agentcore.StopReasonMaxTurns); got != want.Error() {
		t.Fatalf("prompt error status = %q, want %q", got, want)
	}
}
