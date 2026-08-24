package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	stdhttp "net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Yacobolo/toolbelt/apigen/runtime/agenttool"
	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/agent/api"
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	agentconfig "github.com/flidai/leapview/internal/agent/config"
	"github.com/flidai/leapview/internal/agent/ui"
	httpmodel "github.com/flidai/leapview/internal/platform/http/model"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	agentcore "github.com/flidai/leapview/pkg/agent"
	"github.com/flidai/leapview/pkg/pagestream"
	"github.com/go-chi/chi/v5"
)

type Principal struct {
	ID            string
	DevAuthBypass bool
}

type Settings interface {
	GetSetting(ctx context.Context, key string) (string, error)
	UpsertSetting(ctx context.Context, key, value string) error
}

type Options struct {
	Service *agent.Service
	// ActiveProjectID is retained for statically bound compositions. When
	// ResolveProjectID is configured, it is authoritative and evaluated for
	// each request; it is never read from request paths or signal payloads.
	ActiveProjectID        string
	ResolveProjectID       func(context.Context) (projectgraph.ResourceID, error)
	Settings               Settings
	PlatformAdmin          func(context.Context, string) (bool, error)
	CurrentPrincipal       func(*stdhttp.Request) (Principal, bool)
	CurrentCredential      func(*stdhttp.Request) (access.APICredential, bool)
	Broker                 *pagestream.Broker
	CSRFToken              func(*stdhttp.Request) string
	CurrentRoleLabel       func(*stdhttp.Request) string
	Layout                 func(*stdhttp.Request) webpage.Provider
	ChatSignal             func(context.Context, agent.Scope, string, string, bool) ui.ChatViewState
	ChatSignalWith         func(context.Context, agent.Scope, string, []agent.ChatTranscriptItem, agent.ChatArtifactSignals, string, bool) ui.ChatViewState
	SearchReferences       func(*stdhttp.Request, agent.TurnContext, string, int) ([]ui.AgentReferenceSignal, error)
	ResolveTurnContext     func(*stdhttp.Request, agent.Scope, agent.TurnContext) (agent.TurnContext, error)
	QueueMissingTitle      func(context.Context, agent.Scope, string, string)
	ExecuteStartedChatTurn func(context.Context, *agent.Service, agent.Scope, *agent.StartedPrompt, ChatTurnExecution) (agent.PromptResult, error)
	EnqueueRun             func(context.Context, agent.Scope, *agent.StartedPrompt) error
	EnqueueChatRun         func(context.Context, agent.Scope, *agent.StartedPrompt, string) error
	CancelQueuedRun        func(context.Context, agent.Scope, string, string) (bool, error)
	RecordCommandAudit     func(context.Context, CommandAuditInput) error
	Logger                 *slog.Logger
	APIGenToolContracts    map[string]agenttool.Contract
}

func (h *Handler) DashboardBootstrap(r *stdhttp.Request) ui.ChatViewState {
	scope := h.chatScope(r)
	return h.chatSignal(r.Context(), scope, "", "", false)
}

type Handler struct {
	options Options
}

func NewHandler(options Options) *Handler {
	return &Handler{options: options}
}

func (h *Handler) CreateConversation(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	service, scope, ok := h.agentCommandRequest(w, r, createAgentConversationOperation)
	if !ok {
		return
	}
	var input api.AgentConversationCreateRequest
	if err := decodeAgentJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	conversation, err := service.CreateConversation(r.Context(), scope, input.Title)
	if err != nil {
		if _, classified := apigenfailure.KindOf(err); !classified {
			err = apigenfailure.Wrap("unavailable", err)
		}
		h.writeCommandFailure(w, r, createAgentConversationOperation, err)
		return
	}
	h.recordCommandAudit(r, createAgentConversationOperation, scope, "conversation", conversation.ID)
	response := agentConversationDTO(conversation)
	if etag, revisionErr := agent.ConversationRevision(conversation); revisionErr == nil {
		w.Header().Set("ETag", etag)
	}
	writeJSON(w, stdhttp.StatusCreated, response)
}

func (h *Handler) ListConversations(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	service, scope, ok := h.agentRequest(w, r)
	if !ok {
		return
	}
	page, limit, ok := agentPageFromRequest(w, r)
	if !ok {
		return
	}
	conversations, err := service.ListConversationsPage(r.Context(), scope, page)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	nextCursor := ""
	if len(conversations) > limit {
		nextCursor = conversations[limit-1].ID
		conversations = conversations[:limit]
	}
	out := make([]api.AgentConversationResponse, 0, len(conversations))
	for _, conversation := range conversations {
		out = append(out, agentConversationDTO(conversation))
	}
	writeJSON(w, stdhttp.StatusOK, pagedResponseWithCursor(out, nextCursor))
}

func (h *Handler) GetConversation(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	service, scope, ok := h.agentRequest(w, r)
	if !ok {
		return
	}
	conversation, err := service.GetConversation(r.Context(), scope, chi.URLParam(r, "conversation"))
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	response := agentConversationDTO(conversation)
	if etag, revisionErr := agent.ConversationRevision(conversation); revisionErr == nil {
		w.Header().Set("ETag", etag)
	}
	writeJSON(w, stdhttp.StatusOK, response)
}

func (h *Handler) UpdateConversation(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	service, scope, ok := h.agentCommandRequest(w, r, updateAgentConversationOperation)
	if !ok {
		return
	}
	var input api.AgentConversationUpdateRequest
	if err := decodeAgentJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	executor, err := apigencommand.NewExecutor(agentgen.GetAPIGenCommandRuntimeContract, h.options.Logger)
	if err != nil {
		h.writeCommandFailure(w, r, updateAgentConversationOperation, err)
		return
	}
	conversationID := chi.URLParam(r, "conversation")
	conversation, err := service.UpdateConversationWithRevision(r.Context(), scope, conversationID, input.Title, func(current agent.Conversation) error {
		currentRevision, revisionErr := agent.ConversationRevision(current)
		if revisionErr != nil {
			return revisionErr
		}
		return executor.CheckConcurrency(r.Context(), updateAgentConversationOperation.APIGenOperationID(), r.Header.Get("If-Match"), currentRevision)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, agent.ErrNotFound) {
			err = apigenfailure.Wrap("not_found", err)
		} else if _, classified := apigenfailure.KindOf(err); !classified {
			err = apigenfailure.Wrap("invalid", err)
		}
		h.writeCommandFailure(w, r, updateAgentConversationOperation, err)
		return
	}
	h.recordCommandAudit(r, updateAgentConversationOperation, scope, "conversation", conversation.ID)
	response := agentConversationDTO(conversation)
	if etag, revisionErr := agent.ConversationRevision(conversation); revisionErr == nil {
		w.Header().Set("ETag", etag)
	}
	writeJSON(w, stdhttp.StatusOK, response)
}

func (h *Handler) ArchiveConversation(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	service, scope, ok := h.agentCommandRequest(w, r, archiveAgentConversationOperation)
	if !ok {
		return
	}
	conversation, err := service.ArchiveConversation(r.Context(), scope, chi.URLParam(r, "conversation"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = apigenfailure.Wrap("not_found", err)
		}
		h.writeCommandFailure(w, r, archiveAgentConversationOperation, err)
		return
	}
	h.recordCommandAudit(r, archiveAgentConversationOperation, scope, "conversation", conversation.ID)
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (h *Handler) ListMessages(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	service, scope, ok := h.agentRequest(w, r)
	if !ok {
		return
	}
	page, limit, ok := agentPageFromRequest(w, r)
	if !ok {
		return
	}
	messages, err := service.ListMessagesPage(r.Context(), scope, chi.URLParam(r, "conversation"), page)
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	nextCursor := ""
	if len(messages) > limit {
		nextCursor = messages[limit-1].ID
		messages = messages[:limit]
	}
	out := make([]api.AgentMessageResponse, 0, len(messages))
	for _, message := range messages {
		out = append(out, agentMessageDTO(message))
	}
	writeJSON(w, stdhttp.StatusOK, pagedResponseWithCursor(out, nextCursor))
}

func (h *Handler) ListRuns(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	service, scope, ok := h.agentRequest(w, r)
	if !ok {
		return
	}
	page, limit, ok := agentPageFromRequest(w, r)
	if !ok {
		return
	}
	runs, err := service.ListRunsPage(r.Context(), scope, chi.URLParam(r, "conversation"), page)
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	nextCursor := ""
	if len(runs) > limit {
		nextCursor = runs[limit-1].ID
		runs = runs[:limit]
	}
	out := make([]api.AgentRunResponse, 0, len(runs))
	for _, run := range runs {
		out = append(out, agentRunDTO(run, scope))
	}
	writeJSON(w, stdhttp.StatusOK, pagedResponseWithCursor(out, nextCursor))
}

func (h *Handler) GetRun(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	service, scope, ok := h.agentRequest(w, r)
	if !ok {
		return
	}
	conversationID := chi.URLParam(r, "conversation")
	runID := chi.URLParam(r, "run")
	if conversationID == "" {
		run, err := service.GetRunByID(r.Context(), scope, runID)
		if err != nil {
			writeJSONError(w, err, statusForNotFound(err))
			return
		}
		writeJSON(w, stdhttp.StatusOK, agentRunDTO(run, scope))
		return
	}
	run, err := service.GetRun(r.Context(), scope, conversationID, runID)
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	writeJSON(w, stdhttp.StatusOK, agentRunDTO(run, scope))
}

func (h *Handler) CreateTurn(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	service, scope, ok := h.agentRequest(w, r)
	if !ok {
		return
	}
	var input api.AgentTurnRequest
	if err := decodeAgentJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	if strings.TrimSpace(input.Input) == "" {
		writeJSONError(w, fmt.Errorf("input is required"), stdhttp.StatusBadRequest)
		return
	}
	result, err := service.Prompt(r.Context(), agent.PromptInput{
		Scope:          scope,
		ConversationID: chi.URLParam(r, "conversation"),
		Input:          input.Input,
		CorrelationID:  input.CorrelationID,
	})
	if err != nil {
		status := stdhttp.StatusInternalServerError
		if errors.Is(err, agent.ErrDisabled) {
			status = stdhttp.StatusServiceUnavailable
		} else if agent.IsBusy(err) {
			status = stdhttp.StatusConflict
		} else if errors.Is(err, sql.ErrNoRows) {
			status = stdhttp.StatusNotFound
		}
		writeJSONError(w, err, status)
		return
	}
	writeJSON(w, stdhttp.StatusOK, api.AgentTurnResponse{
		ConversationID: result.ConversationID,
		RunID:          result.RunID,
		StopReason:     string(result.StopReason),
		Content:        result.Content,
	})
}

// CreateRun starts an agent prompt and returns the persisted run before model
// execution begins. The public API is intentionally asynchronous; the private
// browser chat transport may continue to use its richer streaming workflow.
func (h *Handler) CreateRun(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	service, scope, ok := h.agentCommandRequest(w, r, createAgentRunOperation)
	if !ok {
		return
	}
	var input api.AgentTurnRequest
	if err := decodeAgentJSON(r, &input); err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	if strings.TrimSpace(input.Input) == "" {
		h.writeCommandFailure(w, r, createAgentRunOperation, apigenfailure.New("invalid", "agent run input is required"))
		return
	}
	started, err := service.StartDurablePrompt(r.Context(), agent.PromptInput{
		Scope: scope, ConversationID: chi.URLParam(r, "conversation"), Input: input.Input, CorrelationID: input.CorrelationID, RequestID: r.Header.Get("Idempotency-Key"),
	}, agent.PromptDispatch{})
	if err != nil {
		switch {
		case errors.Is(err, agent.ErrDisabled):
			err = apigenfailure.Wrap("unavailable", err)
		case agent.IsBusy(err):
			err = apigenfailure.Wrap("conflict", err)
		case errors.Is(err, agent.ErrRequestConflict):
			err = apigenfailure.Wrap("conflict", err)
		case errors.Is(err, sql.ErrNoRows):
			err = apigenfailure.Wrap("not_found", err)
		case errors.Is(err, agent.ErrConversationArchived):
			err = apigenfailure.Wrap("not_found", err)
		case strings.Contains(err.Error(), "required"):
			err = apigenfailure.Wrap("invalid", err)
		}
		if _, classified := apigenfailure.KindOf(err); !classified {
			err = apigenfailure.Wrap("unavailable", err)
		}
		h.writeCommandFailure(w, r, createAgentRunOperation, err)
		return
	}
	run, err := service.GetRun(r.Context(), scope, started.ConversationID, started.RunID)
	if err != nil {
		_ = started.Abort(context.Background(), err)
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, agent.ErrNotFound) {
			err = apigenfailure.Wrap("not_found", err)
		} else if _, classified := apigenfailure.KindOf(err); !classified {
			err = apigenfailure.Wrap("unavailable", err)
		}
		h.writeCommandFailure(w, r, createAgentRunOperation, err)
		return
	}
	w.Header().Set("Location", "/api/v1/agent/conversations/"+started.ConversationID+"/runs/"+started.RunID)
	if h.options.EnqueueRun == nil {
		_ = started.Abort(context.Background(), fmt.Errorf("durable agent queue is unavailable"))
		h.writeCommandFailure(w, r, createAgentRunOperation, apigenfailure.New("unavailable", "durable agent queue is unavailable"))
		return
	}
	if err := h.options.EnqueueRun(r.Context(), scope, started); err != nil {
		_ = started.Abort(context.Background(), err)
		h.writeCommandFailure(w, r, createAgentRunOperation, apigenfailure.Wrap("unavailable", err))
		return
	}
	h.recordCommandAudit(r, createAgentRunOperation, scope, "conversation", started.ConversationID)
	writeJSON(w, stdhttp.StatusAccepted, agentRunDTO(run, scope))
}

func (h *Handler) CancelRun(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	service, scope, ok := h.agentCommandRequest(w, r, cancelAgentRunOperation)
	if !ok {
		return
	}
	conversationID := chi.URLParam(r, "conversation")
	runID := chi.URLParam(r, "run")
	if h.options.CancelQueuedRun != nil {
		cancelled, err := h.options.CancelQueuedRun(r.Context(), scope, conversationID, runID)
		if err != nil {
			h.writeCommandFailure(w, r, cancelAgentRunOperation, apigenfailure.Wrap("unavailable", err))
			return
		}
		if cancelled {
			run, err := service.GetRun(r.Context(), scope, conversationID, runID)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) || errors.Is(err, agent.ErrNotFound) {
					err = apigenfailure.Wrap("not_found", err)
				}
				h.writeCommandFailure(w, r, cancelAgentRunOperation, err)
				return
			}
			h.recordCommandAudit(r, cancelAgentRunOperation, scope, "conversation", conversationID)
			w.Header().Set("Location", "/api/v1/agent/conversations/"+conversationID+"/runs/"+runID)
			writeJSON(w, stdhttp.StatusAccepted, agentRunDTO(run, scope))
			return
		}
	}
	if err := service.CancelRun(r.Context(), scope, conversationID, runID); err != nil {
		if errors.Is(err, agent.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
			err = apigenfailure.Wrap("not_found", err)
		} else if errors.Is(err, agent.ErrRunNotCancellable) {
			// The generated command contract classifies this sentinel directly.
		} else if _, classified := apigenfailure.KindOf(err); !classified {
			err = apigenfailure.Wrap("unavailable", err)
		}
		h.writeCommandFailure(w, r, cancelAgentRunOperation, err)
		return
	}
	run, err := service.GetRun(r.Context(), scope, conversationID, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, agent.ErrNotFound) {
			err = apigenfailure.Wrap("not_found", err)
		} else if _, classified := apigenfailure.KindOf(err); !classified {
			err = apigenfailure.Wrap("unavailable", err)
		}
		h.writeCommandFailure(w, r, cancelAgentRunOperation, err)
		return
	}
	h.recordCommandAudit(r, cancelAgentRunOperation, scope, "conversation", conversationID)
	w.Header().Set("Location", "/api/v1/agent/conversations/"+conversationID+"/runs/"+runID)
	writeJSON(w, stdhttp.StatusAccepted, agentRunDTO(run, scope))
}

func (h *Handler) ListEvents(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	service, scope, ok := h.agentRequest(w, r)
	if !ok {
		return
	}
	if agentAcceptsEventStream(r.Header.Get("Accept")) {
		h.streamRunEvents(w, r, service, scope, chi.URLParam(r, "conversation"), chi.URLParam(r, "run"))
		return
	}
	var (
		events []agent.Event
		err    error
	)
	page, limit, ok := agentPageFromRequest(w, r)
	if !ok {
		return
	}
	if conversationID := chi.URLParam(r, "conversation"); conversationID != "" {
		events, err = service.ListRunEventsPage(r.Context(), scope, conversationID, chi.URLParam(r, "run"), page)
	} else {
		events, err = service.ListEvents(r.Context(), scope, chi.URLParam(r, "run"))
		if err == nil {
			events = pageAgentEvents(events, page)
		}
	}
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	nextCursor := ""
	if len(events) > limit {
		nextCursor = events[limit-1].ID
		events = events[:limit]
	}
	out := make([]api.AgentEventResponse, 0, len(events))
	for _, event := range events {
		out = append(out, agentEventDTO(event))
	}
	writeJSON(w, stdhttp.StatusOK, pagedResponseWithCursor(out, nextCursor))
}

func (h *Handler) streamRunEvents(w stdhttp.ResponseWriter, r *stdhttp.Request, service *agent.Service, scope agent.Scope, conversationID, runID string) {
	run, err := service.GetRun(r.Context(), scope, conversationID, runID)
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	lastID := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if lastID != "" {
		sequence, parseErr := strconv.ParseInt(lastID, 10, 64)
		if parseErr != nil || sequence < 1 {
			writeJSONError(w, fmt.Errorf("Last-Event-ID does not identify an event in this run"), stdhttp.StatusBadRequest)
			return
		}
		previous := fmt.Sprintf("%020d", sequence-1)
		probe, probeErr := service.ListRunEventsPage(r.Context(), scope, conversationID, runID, agent.Page{Limit: 1, After: previous})
		if probeErr != nil || len(probe) != 1 || probe[0].ID != lastID {
			writeJSONError(w, fmt.Errorf("Last-Event-ID does not identify an event in this run"), stdhttp.StatusBadRequest)
			return
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(stdhttp.StatusOK)
	flusher, _ := w.(stdhttp.Flusher)
	heartbeat := time.NewTicker(15 * time.Second)
	poll := time.NewTicker(time.Second)
	reauthorize := time.NewTimer(5 * time.Minute)
	defer heartbeat.Stop()
	defer poll.Stop()
	defer reauthorize.Stop()

	for {
		for {
			page, pageErr := service.ListRunEventsPage(r.Context(), scope, conversationID, runID, agent.Page{Limit: 100, After: lastID})
			if pageErr != nil {
				return
			}
			for _, event := range page {
				payload, _ := json.Marshal(agentEventDTO(event))
				_, _ = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", event.ID, event.EventType, payload)
				lastID = event.ID
			}
			if len(page) < 100 {
				break
			}
		}
		if flusher != nil {
			flusher.Flush()
		}
		run, err = service.GetRun(r.Context(), scope, conversationID, runID)
		if err != nil {
			return
		}
		if agentRunTerminal(run.Status) {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-reauthorize.C:
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		case <-poll.C:
		}
	}
}

func agentAcceptsEventStream(value string) bool {
	for _, item := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(strings.SplitN(item, ";", 2)[0]), "text/event-stream") {
			return true
		}
	}
	return false
}

func agentRunTerminal(status string) bool {
	return status == agent.RunStatusCompleted || status == agent.RunStatusFailed || status == agent.RunStatusCanceled
}

func (h *Handler) GetAdminConfig(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	details, err := h.AdminDetails(r.Context())
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	w.Header().Set("ETag", agentResourceETag(details))
	writeJSON(w, stdhttp.StatusOK, details)
}

func (h *Handler) GetAgentConfig(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	details, err := h.AdminDetails(r.Context())
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	w.Header().Set("ETag", agentResourceETag(details))
	writeJSON(w, stdhttp.StatusOK, agentgen.GenSchemaAgentConfigResponse{SystemPrompt: details.SystemPrompt})
}

func (h *Handler) UpdateAdminConfig(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var signals adminAgentCommandSignals
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		h.writeCommandFailure(w, r, updateAgentConfigOperation, apigenfailure.Wrap("invalid", err))
		return
	}
	systemPrompt := signals.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = signals.AdminAgentCommand.SystemPrompt
	}
	ctx, err := beginUICommandInvocation(r, agentgen.GenUIActionUpdateAgentConfig(), nil, "", systemPrompt, "")
	if err != nil {
		h.writeCommandFailure(w, r, updateAgentConfigOperation, apigenfailure.Wrap("invalid", err))
		return
	}
	h.updateAgentConfig(w, r.WithContext(ctx), systemPrompt)
}

func (h *Handler) UpdateAgentConfig(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	var input api.AdminAgentConfigPatchRequest
	if err := decodeAgentJSON(r, &input); err != nil {
		h.writeCommandFailure(w, r, updateAgentConfigOperation, apigenfailure.Wrap("invalid", err))
		return
	}
	h.updateAgentConfig(w, r, input.SystemPrompt)
}

func (h *Handler) requirePlatformAdmin(w stdhttp.ResponseWriter, r *stdhttp.Request) bool {
	if h.options.CurrentPrincipal == nil {
		writeJSONError(w, fmt.Errorf("agent configuration requires authentication"), stdhttp.StatusUnauthorized)
		return false
	}
	principal, ok := h.options.CurrentPrincipal(r)
	if !ok {
		writeJSONError(w, fmt.Errorf("agent configuration requires an authenticated principal"), stdhttp.StatusUnauthorized)
		return false
	}
	if h.options.PlatformAdmin == nil {
		writeJSONError(w, fmt.Errorf("platform role checker is unavailable"), stdhttp.StatusInternalServerError)
		return false
	}
	admin, err := h.options.PlatformAdmin(r.Context(), principal.ID)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return false
	}
	if !admin {
		writeJSONError(w, access.ErrForbidden, stdhttp.StatusForbidden)
		return false
	}
	return true
}

func (h *Handler) updateAgentConfig(w stdhttp.ResponseWriter, r *stdhttp.Request, systemPrompt string) {
	current, err := h.AdminDetails(r.Context())
	if err != nil {
		h.writeCommandFailure(w, r, updateAgentConfigOperation, apigenfailure.Wrap("unavailable", err))
		return
	}
	executor, err := apigencommand.NewExecutor(agentgen.GetAPIGenCommandRuntimeContract, h.options.Logger)
	if err != nil {
		h.writeCommandFailure(w, r, updateAgentConfigOperation, err)
		return
	}
	if err := executor.CheckConcurrency(r.Context(), updateAgentConfigOperation.APIGenOperationID(), r.Header.Get("If-Match"), agentResourceETag(current)); err != nil {
		h.writeCommandFailure(w, r, updateAgentConfigOperation, err)
		return
	}
	prompt, err := agentconfig.NormalizeSystemPrompt(systemPrompt)
	if err != nil {
		h.writeCommandFailure(w, r, updateAgentConfigOperation, apigenfailure.Wrap("invalid", err))
		return
	}
	if h.options.Settings == nil {
		h.writeCommandFailure(w, r, updateAgentConfigOperation, apigenfailure.Wrap("unavailable", agent.ErrDisabled))
		return
	}
	if err := h.options.Settings.UpsertSetting(r.Context(), agentconfig.SystemPromptSettingKey, prompt); err != nil {
		h.writeCommandFailure(w, r, updateAgentConfigOperation, apigenfailure.Wrap("unavailable", err))
		return
	}
	details, err := h.AdminDetails(r.Context())
	if err != nil {
		h.writeCommandFailure(w, r, updateAgentConfigOperation, apigenfailure.Wrap("unavailable", err))
		return
	}
	h.recordCommandAudit(r, updateAgentConfigOperation, h.chatScope(r), "agent_config", agentconfig.SystemPromptSettingKey)
	w.Header().Set("ETag", agentResourceETag(details))
	writeJSON(w, stdhttp.StatusOK, agentgen.GenSchemaAgentConfigResponse{SystemPrompt: details.SystemPrompt})
}

func agentResourceETag(value any) string {
	token, err := apigencommand.RevisionToken(value)
	if err != nil {
		return ""
	}
	return token
}

func (h *Handler) AdminDetails(ctx context.Context) (api.AdminAgentResponse, error) {
	prompt, err := h.SystemPrompt(ctx)
	if err != nil {
		return api.AdminAgentResponse{}, err
	}
	out := api.AdminAgentResponse{
		Enabled:      h.options.Service != nil && h.options.Service.Enabled(),
		SystemPrompt: prompt,
	}
	if h.options.Service != nil {
		out.Model = h.options.Service.Model()
		out.Tools = adminAgentToolDTOs(h.options.Service.ToolDefinitions(agent.Scope{PrincipalID: "admin", DevAuthBypass: true}), h.options.APIGenToolContracts)
	}
	return out, nil
}

func (h *Handler) agentRequest(w stdhttp.ResponseWriter, r *stdhttp.Request) (*agent.Service, agent.Scope, bool) {
	if h.options.Service == nil || !h.options.Service.Enabled() {
		writeJSONError(w, agent.ErrDisabled, stdhttp.StatusServiceUnavailable)
		return nil, agent.Scope{}, false
	}
	if h.options.CurrentPrincipal == nil {
		writeJSONError(w, fmt.Errorf("agent API requires authentication"), stdhttp.StatusUnauthorized)
		return nil, agent.Scope{}, false
	}
	principal, ok := h.options.CurrentPrincipal(r)
	if !ok {
		writeJSONError(w, fmt.Errorf("agent API requires an authenticated principal"), stdhttp.StatusUnauthorized)
		return nil, agent.Scope{}, false
	}
	scope := agent.Scope{
		PrincipalID:   principal.ID,
		DevAuthBypass: principal.DevAuthBypass,
	}
	if h.options.CurrentCredential != nil {
		if credential, ok := h.options.CurrentCredential(r); ok {
			scope.Credential = agentCredentialScope(credential)
		}
	}
	return h.options.Service, scope, true
}

// agentCommandRequest keeps capability availability on the generated command
// failure path while leaving authentication and query failures on their
// existing transport-owned path.
func (h *Handler) agentCommandRequest(w stdhttp.ResponseWriter, r *stdhttp.Request, operationID agentgen.GenCommandOperationID) (*agent.Service, agent.Scope, bool) {
	if h.options.Service == nil || !h.options.Service.Enabled() {
		h.writeCommandFailure(w, r, operationID, agent.ErrDisabled)
		return nil, agent.Scope{}, false
	}
	return h.agentRequest(w, r)
}

// writeCommandFailure resolves classified domain failures through the
// compiler-checked generated operation vocabulary.
func (h *Handler) writeCommandFailure(w stdhttp.ResponseWriter, r *stdhttp.Request, operationID agentgen.GenCommandOperationID, err error) {
	if errors.Is(err, apigencommand.ErrPreconditionRequired) || errors.Is(err, apigencommand.ErrPreconditionFailed) {
		apitransport.WriteProblem(w, r, stdhttp.StatusPreconditionFailed, "PRECONDITION_FAILED", "The command revision does not match the current resource.", nil)
		return
	}
	apitransport.WriteAPIGenCommandFailure(r.Context(), w, r, h.options.Logger, operationID, agentgen.GetAPIGenCommandFailureContracts, err)
}

func (h *Handler) SystemPrompt(ctx context.Context) (string, error) {
	if h.options.Settings == nil {
		return agentconfig.DefaultSystemPrompt, nil
	}
	prompt, err := h.options.Settings.GetSetting(ctx, agentconfig.SystemPromptSettingKey)
	if err != nil {
		if err == sql.ErrNoRows {
			return agentconfig.DefaultSystemPrompt, nil
		}
		return "", err
	}
	return agentconfig.NormalizeSystemPrompt(prompt)
}

type adminAgentCommandSignals struct {
	SystemPrompt      string `json:"systemPrompt"`
	AdminAgentCommand struct {
		SystemPrompt string `json:"systemPrompt"`
	} `json:"adminAgentCommand"`
}

func agentCredentialScope(credential access.APICredential) agent.CredentialScope {
	if credential.Authoring != nil {
		capabilities := make([]string, len(credential.Authoring.Scope.Capabilities))
		for index, capability := range credential.Authoring.Scope.Capabilities {
			capabilities[index] = string(capability)
		}
		return agent.CredentialScope{
			ProjectID: credential.Authoring.Scope.ProjectID.String(), Capabilities: capabilities, Restricted: true,
		}
	}
	if credential.Token.ID == "" {
		return agent.CredentialScope{}
	}
	var capabilities []string
	if credential.Token.Capabilities != nil {
		capabilities = make([]string, len(credential.Token.Capabilities))
		for index, capability := range credential.Token.Capabilities {
			capabilities[index] = string(capability)
		}
	}
	return agent.CredentialScope{Capabilities: capabilities, Restricted: true}
}

func agentConversationDTO(row agent.Conversation) api.AgentConversationResponse {
	out := api.AgentConversationResponse{
		ID:          row.ID,
		PrincipalID: row.PrincipalID,
		Title:       row.Title,
		Status:      row.Status,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
	out.ArchivedAt = row.ArchivedAt
	return out
}

func agentRunDTO(row agent.Run, scope agent.Scope) api.AgentRunResponse {
	return api.AgentRunResponse{
		ID:             row.ID,
		ConversationID: row.ConversationID,
		PrincipalID:    scope.PrincipalID,
		Status:         row.Status,
		Model:          row.Model,
		StopReason:     row.StopReason,
		InputTokens:    row.InputTokens,
		OutputTokens:   row.OutputTokens,
		TotalTokens:    row.TotalTokens,
		Error:          row.Error,
		StartedAt:      row.StartedAt,
		CompletedAt:    row.FinishedAt,
		CreatedAt:      row.CreatedAt,
	}
}

func agentMessageDTO(row agent.Message) api.AgentMessageResponse {
	return api.AgentMessageResponse{
		ID:          row.ID,
		RunID:       row.RunID,
		Seq:         row.Seq,
		Role:        row.Role,
		ContentText: row.ContentText,
		Content:     jsonObject(row.ContentJSON),
		ToolCallID:  row.ToolCallID,
		ToolName:    row.ToolName,
		IsError:     row.IsError,
		CreatedAt:   row.CreatedAt,
	}
}

func agentEventDTO(row agent.Event) api.AgentEventResponse {
	return api.AgentEventResponse{
		ID:           row.ID,
		Event:        row.EventType,
		ResourceType: "agent_run",
		ResourceID:   row.RunID,
		Data: map[string]any{
			"sequence": row.Seq,
			"severity": row.Severity,
			"payload":  jsonObject(row.PayloadJSON),
		},
		CreatedAt: row.CreatedAt,
	}
}

func agentPageFromRequest(w stdhttp.ResponseWriter, r *stdhttp.Request) (agent.Page, int, bool) {
	limit, ok := apiLimitForRequest(w, r)
	if !ok {
		return agent.Page{}, 0, false
	}
	query := r.URL.Query()
	pageLimit := limit
	if pageLimit < maxAPILimit {
		pageLimit++
	}
	return agent.Page{Limit: pageLimit, After: firstNonEmpty(query.Get("pageToken"), query.Get("after"))}, limit, true
}

func pageAgentEvents(events []agent.Event, page agent.Page) []agent.Event {
	limit := page.Limit
	if limit <= 0 || limit > maxAPILimit {
		limit = maxAPILimit
	}
	start := 0
	after := strings.TrimSpace(page.After)
	if after != "" {
		start = len(events)
		for i, event := range events {
			if event.ID == after {
				start = i + 1
				break
			}
		}
	}
	if start >= len(events) {
		return []agent.Event{}
	}
	end := start + limit
	if end > len(events) {
		end = len(events)
	}
	return append([]agent.Event(nil), events[start:end]...)
}

func adminAgentToolDTOs(tools []agentcore.ToolDefinition, contracts map[string]agenttool.Contract) []api.AdminAgentToolResponse {
	out := make([]api.AdminAgentToolResponse, 0, len(tools))
	for _, tool := range tools {
		dto := api.AdminAgentToolResponse{
			Name:         tool.Name,
			Description:  tool.Description,
			Effect:       "read",
			Defaults:     map[string]any{},
			InputSchema:  jsonObject(string(tool.InputSchema)),
			OutputSchema: map[string]any{},
		}
		if contract, ok := contracts[tool.Name]; ok {
			dto.Effect = string(contract.Effect)
			dto.OutputSchema = jsonObject(string(contract.OutputSchema))
			for _, binding := range contract.Bindings {
				if binding.Argument != "" && binding.Default != nil {
					dto.Defaults[binding.Argument] = binding.Default
				}
			}
		}
		out = append(out, dto)
	}
	return out
}

type pageResponse struct {
	NextCursor string `json:"nextCursor"`
}

func pagedResponseWithCursor(items any, nextCursor string) map[string]any {
	return map[string]any{"items": items, "page": pageResponse{NextCursor: nextCursor}}
}

const (
	defaultAPILimit = 50
	maxAPILimit     = 200
)

func apiLimitForRequest(w stdhttp.ResponseWriter, r *stdhttp.Request) (int, bool) {
	limit, err := parseAPILimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return 0, false
	}
	return limit, true
}

func parseAPILimit(value string) (int, error) {
	if value == "" {
		return defaultAPILimit, nil
	}
	var limit int
	if _, err := fmt.Sscanf(value, "%d", &limit); err != nil {
		return 0, fmt.Errorf("limit must be an integer")
	}
	if limit < 1 {
		return 0, fmt.Errorf("limit must be at least 1")
	}
	if limit > maxAPILimit {
		return 0, fmt.Errorf("limit must not exceed 200")
	}
	return limit, nil
}

func statusForNotFound(err error) int {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, agent.ErrConversationArchived) {
		return stdhttp.StatusNotFound
	}
	return stdhttp.StatusInternalServerError
}

func statusForBadRequestOrNotFound(err error) int {
	if errors.Is(err, sql.ErrNoRows) {
		return stdhttp.StatusNotFound
	}
	return stdhttp.StatusBadRequest
}

func writeJSON(w stdhttp.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeJSONError(w stdhttp.ResponseWriter, err error, status int) {
	writeJSON(w, status, httpmodel.ErrorResponse{
		Code:      status,
		Message:   err.Error(),
		Details:   map[string]any{},
		RequestID: "",
	})
}

func decodeAgentJSON(r *stdhttp.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("request body must contain exactly one JSON value")
		}
		return err
	}
	return nil
}

func jsonObject(raw string) map[string]any {
	var out map[string]any
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	if out == nil {
		return map[string]any{}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
