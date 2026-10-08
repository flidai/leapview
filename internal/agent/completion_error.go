package agent

import (
	"errors"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

// Full dashboard source edits need more room than a short chat answer.
// This is a ceiling and context reservation, not a minimum generation length.
func dashboardAgentLimits() agentcore.Limits {
	return agentcore.Limits{MaxTurns: 24, ReserveOutputTokens: 16384, MaxTruncationRetries: 1}
}

func incompletePromptError(reason agentcore.StopReason) error {
	switch reason {
	case agentcore.StopReasonTruncated:
		return errors.New("The response stopped before it was complete. Please ask the agent to continue; any saved draft changes are retained.")
	case agentcore.StopReasonMaxTurns, agentcore.StopReasonMaxToolCalls:
		return errors.New("The agent could not finish within this request. Please narrow the request or ask it to continue; any saved draft changes are retained.")
	case agentcore.StopReasonContextLimit:
		return errors.New("This conversation is too long to finish the request. Start a new chat with the dashboard selected.")
	default:
		return nil
	}
}
