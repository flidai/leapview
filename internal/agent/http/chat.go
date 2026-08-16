package http

import (
	"context"
	"database/sql"
	"errors"
	nethttp "net/http"
	"net/url"
	"strings"

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/agent"
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	"github.com/flidai/leapview/internal/agent/ui"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
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

const maxChatReferenceSearchResults = 24

type chatTurnCommandAgentSignal struct {
	ActiveConversationID string                        `json:"activeConversationId"`
	Composer             chatTurnCommandComposerSignal `json:"composer"`
}

type chatTurnCommandComposerSignal struct {
	Value string `json:"value"`
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
	scope := h.chatScope(r)
	h.renderChat(w, r, "list", h.chatSignal(r.Context(), scope, "", "", false))
}

func (h *Handler) ChatNew(w nethttp.ResponseWriter, r *nethttp.Request) {
	scope := h.chatScope(r)
	h.renderChat(w, r, "new", h.chatSignal(r.Context(), scope, "", "", false))
}

func (h *Handler) ChatConversation(w nethttp.ResponseWriter, r *nethttp.Request) {
	scope := h.chatScope(r)
	conversationID := strings.TrimSpace(chi.URLParam(r, "conversation"))
	if conversationID == "updates" {
		nethttp.NotFound(w, r)
		return
	}
	if h.options.Service == nil || !h.options.Service.Enabled() {
		h.renderChat(w, r, "conversation", h.chatSignal(r.Context(), scope, "", "", false))
		return
	}
	if scope.PrincipalID == "" {
		nethttp.Error(w, "chat requires an authenticated principal", nethttp.StatusUnauthorized)
		return
	}
	state, err := h.options.Service.ConversationTranscriptState(r.Context(), scope, conversationID)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	if h.options.QueueMissingTitle != nil {
		h.options.QueueMissingTitle(r.Context(), scope, conversationID, chatClientID(r))
	}
	h.renderChat(w, r, "conversation", h.chatSignalWith(r.Context(), scope, conversationID, state.Transcript, state.Artifacts, "", h.options.Service.ConversationRunning(conversationID)))
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
	clientID := chatClientID(r)
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
		h.startDraftChatTurn(w, r, service, scope, clientID, input, turnContext, embedded)
		return
	}
	h.runChatTurn(w, r, service, scope, clientID, activeConversationID, input, turnContext, embedded)
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
	streamID := chatStreamID(scope, chatClientID(r))
	var trace *pagestream.TraceStore
	if h.options.Broker != nil {
		trace = h.options.Broker.TraceStore()
	}
	updates := pagestream.NewSignalStream(w, r, pagestream.WithStreamTrace(trace, streamID, "chat.bootstrap"))
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
	_ = pagestream.EnsureClientID(w, r)
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
	conversation, err := service.CreateConversation(createCtx, scope, "New conversation")
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	h.recordCommandAudit(r.WithContext(createCtx), createAgentConversationOperation, scope, "conversation", conversation.ID)
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
		h.recordCommandAudit(r.WithContext(runCtx), createAgentRunOperation, scope, "conversation", conversation.ID)
		_ = pagestream.Redirect(w, r, chatRoutePath(conversation.ID))
		return
	}
	h.recordCommandAudit(r.WithContext(runCtx), createAgentRunOperation, scope, "conversation", conversation.ID)
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

func (h *Handler) runChatTurn(w nethttp.ResponseWriter, r *nethttp.Request, service *agent.Service, scope agent.Scope, clientID, activeConversationID, input string, turnContext *agent.TurnContext, embedded bool) {
	conversationID := strings.TrimSpace(activeConversationID)
	state, err := service.ConversationTranscriptState(r.Context(), scope, conversationID)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	transcript := state.Transcript
	streamArtifacts := state.Artifacts
	streamID := chatStreamID(scope, clientID)
	var trace *pagestream.TraceStore
	if h.options.Broker != nil {
		trace = h.options.Broker.TraceStore()
	}
	updates := pagestream.NewSignalStream(w, r, pagestream.WithStreamTrace(trace, streamID, "chat.turn"))
	identity := uiRequestIdentity(r, input)
	runCtx, invocationErr := beginUICommandInvocation(r, agentUIBinding(createAgentRunOperation), nil, conversationID, input, identity)
	if invocationErr != nil {
		_ = updates.Patch(chatSignalPatch(h.chatSignalWith(r.Context(), scope, conversationID, transcript, streamArtifacts, chatTurnStatusError(invocationErr), false), embedded))
		return
	}
	started, err := service.StartPrompt(runCtx, agent.PromptInput{
		Scope:          scope,
		ConversationID: conversationID,
		Input:          input,
		Context:        turnContext,
	})
	if err != nil {
		_ = updates.Patch(chatSignalPatch(h.chatSignalWith(r.Context(), scope, conversationID, transcript, streamArtifacts, chatTurnStatusError(err), false), embedded))
		return
	}
	h.recordCommandAudit(r.WithContext(runCtx), createAgentRunOperation, scope, "conversation", conversationID)
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
	return h.options.Service, scope, true
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
	scope := agent.Scope{ProjectID: strings.TrimSpace(h.options.ActiveProjectID), PrincipalID: principalID, DevAuthBypass: devBypass}
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
	return pagestream.ClientIDFromRequest(r, "")
}

func chatStreamID(scope agent.Scope, clientID string) string {
	if strings.TrimSpace(clientID) == "" {
		clientID = "default"
	}
	return "chat:" + clientID + ":" + scope.PrincipalID
}
