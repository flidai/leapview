package http

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	nethttp "net/http"
	"net/url"
	"strings"
	"time"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/agent"
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	"github.com/flidai/leapview/internal/agent/ui"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	webtransport "github.com/flidai/leapview/internal/platform/web/transport"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	"github.com/flidai/leapview/pkg/pagestream"
	"github.com/go-chi/chi/v5"
)

type chatTurnCommandSignals struct {
	Agent        chatTurnCommandAgentSignal `json:"agent"`
	AgentContext agent.TurnContext          `json:"agentContext"`
}

type chatReferenceSearchSignals struct {
	AgentReferenceSearch ui.AgentReferenceSearchSignal `json:"agentReferenceSearch"`
	AgentContext         agent.TurnContext             `json:"agentContext"`
}

type chatRestoreSignals struct {
	Agent struct {
		ActiveConversationID string `json:"activeConversationId"`
	} `json:"agent"`
}

type chatStopCommandSignals struct {
	Agent struct {
		ActiveConversationID string `json:"activeConversationId"`
		Status               struct {
			RunID string `json:"runId"`
		} `json:"status"`
	} `json:"agent"`
	AgentContext agent.TurnContext `json:"agentContext"`
}

const maxChatReferenceSearchResults = 24

type chatTurnCommandAgentSignal struct {
	ActiveConversationID string                        `json:"activeConversationId"`
	Composer             chatTurnCommandComposerSignal `json:"composer"`
}

type chatTurnCommandComposerSignal struct {
	Value         string `json:"value"`
	EditMessageID string `json:"editMessageId"`
}

type ChatTurnEmitter func(ui.ChatViewState) error

type ChatTurnExecution struct {
	EmitInitialRunning bool
	GenerateTitle      bool
	ClientID           string
	LiveConversations  []ui.ChatConversationSummary
	Emit               ChatTurnEmitter
}

func (h *Handler) Chat(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.renderChat(w, r, "list", ui.ChatViewState{})
}

func (h *Handler) ChatNew(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.renderChat(w, r, "new", ui.ChatViewState{})
}

func (h *Handler) ChatConversation(w nethttp.ResponseWriter, r *nethttp.Request) {
	scope := h.chatScope(r)
	conversationID := strings.TrimSpace(chi.URLParam(r, "conversation"))
	if conversationID == "updates" {
		nethttp.NotFound(w, r)
		return
	}
	if h.options.Service == nil || !h.options.Service.Enabled() {
		h.renderChat(w, r, "conversation", ui.ChatViewState{})
		return
	}
	if scope.PrincipalID == "" {
		nethttp.Error(w, "chat requires an authenticated principal", nethttp.StatusUnauthorized)
		return
	}
	_, err := h.options.Service.GetConversation(r.Context(), scope, conversationID)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	if h.options.QueueMissingTitle != nil {
		h.options.QueueMissingTitle(r.Context(), scope, conversationID, chatClientID(r))
	}
	h.renderChat(w, r, "conversation", ui.ChatViewState{Agent: ui.ChatSignal{ActiveConversationID: conversationID}})
}

// ChatRestore is the Datastar adapter for restoring an embedded chat. The
// browser supplies only a conversation identifier; the service reloads and
// authorizes all transcript state before it is returned.
func (h *Handler) ChatRestore(w nethttp.ResponseWriter, r *nethttp.Request) {
	scope := h.chatScope(r)
	signals := chatRestoreSignals{}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}

	signal := h.chatSignal(r.Context(), scope, "", "", false)
	conversationID := strings.TrimSpace(signals.Agent.ActiveConversationID)
	if conversationID != "" && h.options.Service != nil && h.options.Service.Enabled() && scope.PrincipalID != "" {
		state, err := h.options.Service.ConversationTranscriptState(r.Context(), scope, conversationID)
		switch {
		case err == nil:
			signal = h.chatSignalWith(r.Context(), scope, conversationID, state.Transcript, state.Artifacts, "", h.options.Service.ConversationRunning(conversationID))
		case errors.Is(err, sql.ErrNoRows), errors.Is(err, agent.ErrNotFound):
			// An absent or unauthorized conversation restores to a blank state so
			// callers cannot distinguish those cases.
		default:
			nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
			return
		}
	}

	updates := pagestream.NewSignalStream(w, r)
	_ = updates.Patch(chatSignalPatch(signal, true))
}

func (h *Handler) ChatTurn(w nethttp.ResponseWriter, r *nethttp.Request) {
	service, scope, ok := h.chatService(w, r)
	if !ok {
		return
	}
	clientID, ok := webtransport.RequireClientID(w, r)
	if !ok {
		return
	}
	signals := chatTurnCommandSignals{}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	input := strings.TrimSpace(signals.Agent.Composer.Value)
	if input == "" {
		nethttp.Error(w, "input is required", nethttp.StatusBadRequest)
		return
	}
	turnContext, embedded, err := h.resolveChatTurnContext(r, scope, signals.AgentContext)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	activeConversationID := strings.TrimSpace(signals.Agent.ActiveConversationID)
	if activeConversationID == "" {
		if strings.TrimSpace(signals.Agent.Composer.EditMessageID) != "" {
			nethttp.Error(w, "editing requires an existing conversation", nethttp.StatusBadRequest)
			return
		}
		h.startDraftChatTurn(w, r, service, scope, clientID, input, turnContext, embedded)
		return
	}
	h.runChatTurn(w, r, service, scope, clientID, activeConversationID, input, turnContext, embedded, strings.TrimSpace(signals.Agent.Composer.EditMessageID))
}

// ChatStop cancels the run represented by the browser's current status. The
// run ID is required so a delayed stop request cannot cancel a newer turn in
// the same conversation. In-process runs are cancelled first and allowed to
// persist their partial transcript; queued runs use the transactional queue
// cancellation path instead.
func (h *Handler) ChatStop(w nethttp.ResponseWriter, r *nethttp.Request) {
	service, scope, ok := h.chatService(w, r)
	if !ok {
		return
	}
	clientID, ok := webtransport.RequireClientID(w, r)
	if !ok {
		return
	}
	signals := chatStopCommandSignals{}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	conversationID := strings.TrimSpace(signals.Agent.ActiveConversationID)
	runID := strings.TrimSpace(signals.Agent.Status.RunID)
	if conversationID == "" || runID == "" {
		nethttp.Error(w, "an active conversation and run are required", nethttp.StatusBadRequest)
		return
	}

	run, err := service.GetRun(r.Context(), scope, conversationID, runID)
	if err != nil {
		h.writeChatStopFailure(w, r, err)
		return
	}
	if run.Status != agent.RunStatusRunning && run.Status != agent.RunStatusPreparing {
		h.writeChatStopFailure(w, r, agent.ErrRunNotCancellable)
		return
	}

	identity := uiRequestIdentity(r, conversationID+"\x00"+runID)
	stopCtx, invocationErr := beginUICommandInvocation(r, agentUIBinding(cancelAgentRunOperation), nil, conversationID, runID, identity)
	if invocationErr != nil {
		h.writeChatStopFailure(w, r, invocationErr)
		return
	}
	if withIntent, intentErr := h.withAuditIntent(r.WithContext(stopCtx), cancelAgentRunOperation, scope, "conversation", conversationID); intentErr != nil {
		h.writeChatStopFailure(w, r, intentErr)
		return
	} else {
		r = withIntent
		stopCtx = withIntent.Context()
	}

	active, queued, err := h.cancelChatRun(stopCtx, service, scope, conversationID, runID)
	if err != nil {
		h.writeChatStopFailure(w, r, err)
		return
	}
	if active {
		// StartedPrompt owns the durable terminal transition for an in-process
		// run. Wait briefly for it so the refreshed state includes partial
		// output and Continue cannot race the old worker.
		run, err = waitForChatRunTerminal(stopCtx, service, scope, conversationID, runID)
	} else if queued {
		run, err = service.GetRun(stopCtx, scope, conversationID, runID)
	}
	if err != nil {
		h.writeChatStopFailure(w, r, err)
		return
	}
	if run.Status == agent.RunStatusRunning || run.Status == agent.RunStatusPreparing {
		h.writeChatStopFailure(w, r, fmt.Errorf("agent run cancellation did not settle"))
		return
	}

	state, err := service.ConversationTranscriptState(stopCtx, scope, conversationID)
	if err != nil {
		h.writeChatStopFailure(w, r, err)
		return
	}
	embedded := strings.EqualFold(strings.TrimSpace(signals.AgentContext.Surface), "dashboard") || strings.EqualFold(strings.TrimSpace(signals.AgentContext.Surface), "data")
	signal := h.chatSignalWith(stopCtx, scope, conversationID, state.Transcript, state.Artifacts, "", false)
	// The transport owns the settled stop boundary. Keep the explicit status
	// invariant even when a composition supplies a minimal signal builder.
	signal.Agent.Status.Running = false
	signal.Agent.Status.RunID = nil
	if run.Status == agent.RunStatusCanceled {
		signal.Agent.Status.CanContinue = ui.Pointer(true)
	} else {
		signal.Agent.Status.CanContinue = nil
	}
	patch := chatSignalPatch(signal, embedded)
	if err := pagestream.NewSignalStream(w, r).Patch(patch); err != nil {
		return
	}
	if h.options.Broker != nil {
		h.options.Broker.Publish(chatConversationStreamID(scope, clientID, conversationID), patch)
		h.options.Broker.Publish(chatStreamID(scope, clientID), ui.ChatConversationsPatch(signal.Agent.Conversations, ""))
	}
	h.recordLegacyCommandAudit(r, cancelAgentRunOperation, scope, "conversation", conversationID)
}

func (h *Handler) cancelChatRun(ctx context.Context, service *agent.Service, scope agent.Scope, conversationID, runID string) (active, queued bool, err error) {
	if err := service.CancelRun(ctx, scope, conversationID, runID); err == nil {
		return true, false, nil
	} else if !errors.Is(err, agent.ErrRunNotCancellable) {
		return false, false, err
	}

	var queuedErr error
	if h.options.CancelQueuedRun != nil {
		queued, queuedErr = h.options.CancelQueuedRun(ctx, scope, conversationID, runID)
		if queuedErr == nil && queued {
			return false, true, nil
		}
	}
	// A worker may claim the queue item between the first in-process check and
	// the queued transition. Recheck the service so that race still cancels the
	// worker context and gets the partial-output terminalization path.
	if err := service.CancelRun(ctx, scope, conversationID, runID); err == nil {
		return true, false, nil
	} else if !errors.Is(err, agent.ErrRunNotCancellable) {
		return false, false, err
	}
	if queuedErr != nil {
		return false, false, queuedErr
	}
	return false, false, agent.ErrRunNotCancellable
}

func waitForChatRunTerminal(ctx context.Context, service *agent.Service, scope agent.Scope, conversationID, runID string) (agent.Run, error) {
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for {
		run, err := service.GetRun(waitCtx, scope, conversationID, runID)
		if err != nil {
			return agent.Run{}, err
		}
		if run.Status != agent.RunStatusRunning && run.Status != agent.RunStatusPreparing {
			return run, nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-waitCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return run, waitCtx.Err()
		case <-timer.C:
		}
	}
}

func (h *Handler) writeChatStopFailure(w nethttp.ResponseWriter, r *nethttp.Request, err error) {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, agent.ErrNotFound) {
		err = apigenfailure.Wrap("not_found", err)
	} else if errors.Is(err, agent.ErrRunNotCancellable) {
		// Preserve the generated cancellation failure kind.
	} else if _, classified := apigenfailure.KindOf(err); !classified {
		err = apigenfailure.Wrap("unavailable", err)
	}
	h.writeCommandFailure(w, r, cancelAgentRunOperation, err)
}

func (h *Handler) ChatReferenceSearch(w nethttp.ResponseWriter, r *nethttp.Request) {
	if h.options.SearchReferences == nil {
		nethttp.Error(w, "chat reference search is not configured", nethttp.StatusServiceUnavailable)
		return
	}
	signals := chatReferenceSearchSignals{}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	search := signals.AgentReferenceSearch
	// Reference discovery follows the global agent boundary. SearchReferences
	// applies the request principal or API credential at object and location level,
	// and marks current-page results for deterministic client grouping.
	results, err := h.options.SearchReferences(r, signals.AgentContext, strings.TrimSpace(search.Query), maxChatReferenceSearchResults)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	if len(results) > maxChatReferenceSearchResults {
		results = results[:maxChatReferenceSearchResults]
	}
	updates := pagestream.NewSignalStream(w, r)
	_ = updates.Patch(pagestream.SignalPatch{"agentReferenceSearch": ui.AgentReferenceSearchSignal{
		Query:     strings.TrimSpace(search.Query),
		RequestID: search.RequestID,
		Results:   results,
	}})
}

func (h *Handler) ChatUpdates(w nethttp.ResponseWriter, r *nethttp.Request) {
	scope := h.chatScope(r)
	signal, view := h.chatBootstrapSignal(r, scope)
	projectID := ""
	clientID, ok := webtransport.RequireClientID(w, r)
	if !ok {
		return
	}
	streamID := chatStreamID(scope, clientID)
	if conversationID := strings.TrimSpace(r.URL.Query().Get("conversation")); conversationID != "" {
		streamID = chatConversationStreamID(scope, clientID, conversationID)
	}
	updates := pagestream.NewSignalStream(w, r)
	if err := updates.Patch(ui.ChatBootstrapSignals(projectID, view, signal, h.layout(r))); err != nil {
		return
	}
	if h.options.Service == nil || !h.options.Service.Enabled() || scope.PrincipalID == "" || h.options.Broker == nil {
		updates.Wait(r.Context())
		return
	}
	_ = updates.Forward(r.Context(), h.options.Broker, streamID)
}

func (h *Handler) renderChat(w nethttp.ResponseWriter, r *nethttp.Request, view string, signal ui.ChatViewState) {
	if _, ok := webtransport.RequireClientID(w, r); !ok {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(nethttp.StatusOK)
	projectID := ""
	if err := ui.ChatPage(projectID, h.csrfToken(r), view, signal, h.layout(r)).Render(w); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
	}
}

func (h *Handler) layout(r *nethttp.Request) webpage.Provider {
	if h.options.Layout == nil {
		return nil
	}
	return h.options.Layout(r)
}

func (h *Handler) startDraftChatTurn(w nethttp.ResponseWriter, r *nethttp.Request, service *agent.Service, scope agent.Scope, clientID, input string, turnContext *agent.TurnContext, embedded bool) {
	if !embedded && h.options.EnqueueChatRun == nil {
		nethttp.Error(w, "durable chat turn queue is not configured", nethttp.StatusServiceUnavailable)
		return
	}
	identity := uiRequestIdentity(r, input)
	workflow := []uicommand.Binding{
		agentgen.GenUIActionCreateAgentConversation(),
		agentgen.GenUIActionCreateAgentRun(),
	}
	createCtx, err := beginUICommandInvocation(r, agentUIBinding(createAgentConversationOperation), workflow, "", input, identity)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusForbidden)
		return
	}
	if withIntent, intentErr := h.withAuditIntent(r.WithContext(createCtx), createAgentConversationOperation, scope, "conversation", ""); intentErr != nil {
		nethttp.Error(w, intentErr.Error(), nethttp.StatusServiceUnavailable)
		return
	} else {
		createCtx = withIntent.Context()
	}
	conversation, err := service.CreateConversation(createCtx, scope, "New conversation")
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	h.recordLegacyCommandAudit(r.WithContext(createCtx), createAgentConversationOperation, scope, "conversation", conversation.ID)
	prompt := agent.PromptInput{
		Scope:          scope,
		ConversationID: conversation.ID,
		Input:          input,
		Context:        turnContext,
	}
	var started *agent.StartedPrompt
	runCtx, invocationErr := beginUICommandInvocation(r, agentUIBinding(createAgentRunOperation), workflow, conversation.ID, input, identity)
	if invocationErr != nil {
		nethttp.Error(w, invocationErr.Error(), nethttp.StatusForbidden)
		return
	}
	if withIntent, intentErr := h.withAuditIntent(r.WithContext(runCtx), createAgentRunOperation, scope, "conversation", conversation.ID); intentErr != nil {
		nethttp.Error(w, intentErr.Error(), nethttp.StatusServiceUnavailable)
		return
	} else {
		runCtx = withIntent.Context()
	}
	if embedded {
		started, err = service.StartPrompt(runCtx, prompt)
	} else {
		started, err = service.StartDurablePrompt(runCtx, prompt, agent.PromptDispatch{ChatClientID: clientID})
	}
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	if !embedded {
		if err := h.options.EnqueueChatRun(runCtx, scope, started, clientID); err != nil {
			_ = started.Abort(context.WithoutCancel(r.Context()), err)
			nethttp.Error(w, "durable chat turn queue is unavailable", nethttp.StatusServiceUnavailable)
			return
		}
		h.recordLegacyCommandAudit(r.WithContext(runCtx), createAgentRunOperation, scope, "conversation", conversation.ID)
		// Acknowledge the created conversation through the same signal contract
		// as the chat page. Its draft navigation opens the conversation before
		// the queued provider run finishes, without an injected-script redirect.
		_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{
			"agent": map[string]any{"activeConversationId": conversation.ID},
		})
		return
	}
	h.recordLegacyCommandAudit(r.WithContext(runCtx), createAgentRunOperation, scope, "conversation", conversation.ID)
	if h.options.ExecuteStartedChatTurn == nil {
		nethttp.Error(w, "chat turn executor is not configured", nethttp.StatusServiceUnavailable)
		return
	}
	updates := pagestream.NewSignalStream(w, r)
	_, _ = h.options.ExecuteStartedChatTurn(runCtx, service, scope, started, ChatTurnExecution{
		EmitInitialRunning: true,
		GenerateTitle:      true,
		ClientID:           clientID,
		LiveConversations:  h.chatConversations(r.Context(), scope),
		Emit: func(signal ui.ChatViewState) error {
			return updates.Patch(chatSignalPatch(signal, true))
		},
	})
}

func (h *Handler) runChatTurn(w nethttp.ResponseWriter, r *nethttp.Request, service *agent.Service, scope agent.Scope, clientID, activeConversationID, input string, turnContext *agent.TurnContext, embedded bool, editMessageIDs ...string) {
	editMessageID := ""
	if len(editMessageIDs) > 0 {
		editMessageID = editMessageIDs[0]
	}
	conversationID := strings.TrimSpace(activeConversationID)
	state, err := service.ConversationTranscriptState(r.Context(), scope, conversationID)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	transcript := state.Transcript
	streamArtifacts := state.Artifacts
	updates := pagestream.NewSignalStream(w, r)
	identity := uiRequestIdentity(r, input)
	runCtx, invocationErr := beginUICommandInvocation(r, agentUIBinding(createAgentRunOperation), nil, conversationID, input, identity)
	if invocationErr != nil {
		_ = updates.Patch(chatSignalPatch(h.chatSignalWith(r.Context(), scope, conversationID, transcript, streamArtifacts, chatTurnStatusError(invocationErr), false), embedded))
		return
	}
	if withIntent, intentErr := h.withAuditIntent(r.WithContext(runCtx), createAgentRunOperation, scope, "conversation", conversationID); intentErr != nil {
		_ = updates.Patch(chatSignalPatch(h.chatSignalWith(r.Context(), scope, conversationID, transcript, streamArtifacts, chatTurnStatusError(intentErr), false), embedded))
		return
	} else {
		runCtx = withIntent.Context()
	}
	prompt := agent.PromptInput{
		Scope:          scope,
		ConversationID: conversationID,
		EditMessageID:  editMessageID,
		Input:          input,
		Context:        turnContext,
	}
	// Chat page turns use the durable workflow just like the first turn in a
	// new conversation. Keeping the provider execution off this Datastar
	// command request lets it return immediately; the already-open chat update
	// stream carries the worker's live transcript. Embedded dashboard/data
	// turns intentionally remain inline so their drawer can stream the answer
	// directly to its command response.
	queued := !embedded && h.options.EnqueueChatRun != nil
	if queued {
		// Write the running boundary before the durable workflow becomes
		// visible to a worker. This gives the browser immediate feedback and
		// prevents a very fast worker from racing a late running:true patch.
		_ = updates.Patch(chatSignalPatch(h.chatSignalWith(r.Context(), scope, conversationID, transcript, streamArtifacts, "", true), embedded))
	}
	var started *agent.StartedPrompt
	if queued {
		started, err = service.StartDurablePrompt(runCtx, prompt, agent.PromptDispatch{ChatClientID: clientID})
	} else {
		started, err = service.StartPrompt(runCtx, prompt)
	}
	if err != nil {
		_ = updates.Patch(chatSignalPatch(h.chatSignalWith(r.Context(), scope, conversationID, transcript, streamArtifacts, chatTurnStatusError(err), false), embedded))
		return
	}
	h.recordLegacyCommandAudit(r.WithContext(runCtx), createAgentRunOperation, scope, "conversation", conversationID)
	if queued {
		if err := h.options.EnqueueChatRun(runCtx, scope, started, clientID); err != nil {
			_ = started.Abort(context.WithoutCancel(r.Context()), err)
			_ = updates.Patch(chatSignalPatch(h.chatSignalWith(r.Context(), scope, conversationID, transcript, streamArtifacts, chatTurnStatusError(err), false), embedded))
			return
		}
		return
	}
	if h.options.ExecuteStartedChatTurn == nil {
		nethttp.Error(w, "chat turn executor is not configured", nethttp.StatusServiceUnavailable)
		return
	}
	_, _ = h.options.ExecuteStartedChatTurn(runCtx, service, scope, started, ChatTurnExecution{
		EmitInitialRunning: true,
		LiveConversations:  h.chatConversations(r.Context(), scope),
		Emit: func(signal ui.ChatViewState) error {
			return updates.Patch(chatSignalPatch(signal, embedded))
		},
	})
}

func (h *Handler) chatBootstrapSignal(r *nethttp.Request, scope agent.Scope) (ui.ChatViewState, string) {
	view := strings.TrimSpace(r.URL.Query().Get("view"))
	if view == "" {
		view = "list"
	}
	conversationID := strings.TrimSpace(r.URL.Query().Get("conversation"))
	if conversationID == "" || h.options.Service == nil || !h.options.Service.Enabled() || scope.PrincipalID == "" {
		return h.chatSignal(r.Context(), scope, "", "", false), view
	}
	state, err := h.options.Service.ConversationTranscriptState(r.Context(), scope, conversationID)
	if err != nil {
		return h.chatSignal(r.Context(), scope, "", "", false), view
	}
	return h.chatSignalWith(r.Context(), scope, conversationID, state.Transcript, state.Artifacts, "", h.options.Service.ConversationRunning(conversationID)), view
}

func (h *Handler) chatService(w nethttp.ResponseWriter, r *nethttp.Request) (*agent.Service, agent.Scope, bool) {
	if h.options.Service == nil || !h.options.Service.Enabled() {
		nethttp.Error(w, agent.ErrDisabled.Error(), nethttp.StatusServiceUnavailable)
		return nil, agent.Scope{}, false
	}
	scope := h.chatScope(r)
	if scope.PrincipalID == "" {
		nethttp.Error(w, "chat requires an authenticated principal", nethttp.StatusUnauthorized)
		return nil, agent.Scope{}, false
	}
	bound, err := h.bindRunScope(r.Context(), scope)
	if err != nil {
		status := nethttp.StatusServiceUnavailable
		if kind, classified := apigenfailure.KindOf(err); classified && kind == "forbidden" {
			status = nethttp.StatusForbidden
		}
		nethttp.Error(w, err.Error(), status)
		return nil, agent.Scope{}, false
	}
	return h.options.Service, bound, true
}

func (h *Handler) chatScope(r *nethttp.Request) agent.Scope {
	principalID := ""
	devBypass := false
	if h.options.CurrentPrincipal != nil {
		if principal, ok := h.options.CurrentPrincipal(r); ok {
			principalID = principal.ID
			devBypass = principal.DevAuthBypass
		}
	}
	projectID := strings.TrimSpace(h.options.ActiveProjectID)
	if h.options.ResolveProjectID != nil {
		resolved, err := h.options.ResolveProjectID(r.Context())
		if err != nil || resolved.Validate() != nil {
			projectID = ""
		} else {
			projectID = resolved.String()
		}
	}
	scope := agent.Scope{ProjectID: projectID, PrincipalID: principalID, DevAuthBypass: devBypass}
	if h.options.CurrentCredential != nil {
		if credential, ok := h.options.CurrentCredential(r); ok {
			scope.Credential = agentCredentialScope(credential)
		}
	}
	return scope
}

func (h *Handler) Scope(r *nethttp.Request) agent.Scope {
	if h == nil {
		return agent.Scope{}
	}
	return h.chatScope(r)
}

func (h *Handler) chatSignal(ctx context.Context, scope agent.Scope, activeID, statusErr string, running bool) ui.ChatViewState {
	if h.options.ChatSignal != nil {
		return h.options.ChatSignal(ctx, scope, activeID, statusErr, running)
	}
	return ui.ChatViewState{}
}

func (h *Handler) chatSignalWith(ctx context.Context, scope agent.Scope, activeID string, transcript []agent.ChatTranscriptItem, artifacts agent.ChatArtifactSignals, statusErr string, running bool) ui.ChatViewState {
	if h.options.ChatSignalWith != nil {
		return h.options.ChatSignalWith(ctx, scope, activeID, transcript, artifacts, statusErr, running)
	}
	return ui.ChatViewState{}
}

func (h *Handler) chatConversations(ctx context.Context, scope agent.Scope) []ui.ChatConversationSummary {
	signal := h.chatSignal(ctx, scope, "", "", false)
	return signal.Agent.Conversations
}

func (h *Handler) csrfToken(r *nethttp.Request) string {
	if h.options.CSRFToken == nil {
		return ""
	}
	return h.options.CSRFToken(r)
}

func (h *Handler) currentRoleLabel(r *nethttp.Request) string {
	if h.options.CurrentRoleLabel == nil {
		return ""
	}
	return h.options.CurrentRoleLabel(r)
}

func chatTurnStatusError(err error) string {
	if err == nil {
		return ""
	}
	if agent.IsBusy(err) {
		return "A turn is already running for this conversation."
	}
	return err.Error()
}

func chatSignalPatch(signal ui.ChatViewState, embedded bool) pagestream.SignalPatch {
	patch := ui.ChatSignalPatch(signal)
	if embedded {
		delete(patch, "visuals")
		patch["agentVisuals"] = signal.Visuals
	}
	return patch
}

func (h *Handler) resolveChatTurnContext(r *nethttp.Request, scope agent.Scope, candidate agent.TurnContext) (*agent.TurnContext, bool, error) {
	surface := strings.ToLower(strings.TrimSpace(candidate.Surface))
	embedded := surface == "dashboard" || surface == "data"
	if surface == "" || (surface == "chat" && len(candidate.References) == 0) {
		return nil, false, nil
	}
	if h.options.ResolveTurnContext == nil {
		return nil, embedded, errors.New("turn context resolver is not configured")
	}
	resolved, err := h.options.ResolveTurnContext(r, scope, candidate)
	if err != nil {
		return nil, embedded, err
	}
	return &resolved, embedded, nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func chatRoutePath(parts ...string) string {
	path := "/chats"
	for _, part := range parts {
		part = strings.Trim(part, "/")
		if part == "" {
			continue
		}
		path += "/" + url.PathEscape(part)
	}
	return path
}

func chatClientID(r *nethttp.Request) string {
	return webtransport.ClientIDFromRequest(r, "")
}

func chatStreamID(scope agent.Scope, clientID string) string {
	return "chat:" + clientID + ":" + scope.PrincipalID
}

func chatConversationStreamID(scope agent.Scope, clientID, conversationID string) string {
	return chatStreamID(scope, clientID) + ":conversation:" + strings.TrimSpace(conversationID)
}
