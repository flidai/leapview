package module

import (
	"context"

	"github.com/flidai/leapview/internal/agent"
	agenthttp "github.com/flidai/leapview/internal/agent/http"
	"github.com/flidai/leapview/internal/agent/ui"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

func (m *Module) executeStartedChatTurn(ctx context.Context, service *agent.Service, scope agent.Scope, started *agent.StartedPrompt, execution agenthttp.ChatTurnExecution) (agent.PromptResult, error) {
	state, err := service.ConversationTranscriptState(ctx, scope, started.ConversationID)
	if err != nil {
		_ = started.Abort(ctx, err)
		return agent.PromptResult{}, err
	}
	transcript := state.Transcript
	streamArtifacts := state.Artifacts
	liveConversations := execution.LiveConversations
	if liveConversations == nil {
		// A streamed answer may emit many token/tool events. Resolve the
		// sidebar snapshot once per turn instead of issuing one conversation
		// query for every event.
		liveConversations = m.chatConversations(ctx, scope)
	}
	emit := func(signal ui.ChatViewState) {
		if execution.Emit != nil {
			_ = execution.Emit(signal)
		}
	}
	liveSignal := func(statusErr string, running bool) ui.ChatViewState {
		return chatSignalWithConversations(liveConversations, started.ConversationID, transcript, streamArtifacts, statusErr, running, true, started.RunID)
	}
	finalSignal := func(statusErr string, running bool) ui.ChatViewState {
		return m.ChatSignalWith(ctx, scope, started.ConversationID, transcript, streamArtifacts, statusErr, running)
	}
	if execution.EmitInitialRunning {
		emit(finalSignal("", true))
	}
	result, err := started.Complete(ctx, func(event agent.EventEnvelope) {
		transcript = applyLiveTranscriptEvent(transcript, started.ConversationID, event)
		emit(liveSignal("", true))
	})
	statusErr := chatTurnStatusError(err, result.StopReason)
	if result.RunID != "" {
		if refreshed, refreshErr := service.ConversationTranscriptState(ctx, scope, started.ConversationID); refreshErr == nil {
			transcript = refreshed.Transcript
			streamArtifacts = refreshed.Artifacts
		}
	}
	shouldGenerateTitle := execution.GenerateTitle && err == nil && result.RunID != ""
	if shouldGenerateTitle {
		m.markChatTitlePending(started.ConversationID)
	}
	emit(finalSignal(statusErr, false))
	if shouldGenerateTitle {
		m.generateConversationTitleAsync(scope, started.ConversationID, execution.ClientID)
	}
	return result, err
}

func chatTurnStatusError(err error, stopReason agentcore.StopReason) string {
	if err == nil {
		switch stopReason {
		case agentcore.StopReasonMaxTurns:
			return "The agent reached its turn limit before producing a final answer. Ask it to continue."
		}
		return ""
	}
	if agent.IsBusy(err) {
		return "A turn is already running for this conversation."
	}
	return err.Error()
}
