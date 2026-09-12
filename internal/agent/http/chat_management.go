package http

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	stdhttp "net/http"
	"net/url"
	"strings"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/agent/api"
	"github.com/flidai/leapview/internal/agent/ui"
	"github.com/flidai/leapview/pkg/pagestream"
)

const (
	conversationManagementActionPin        = "pin"
	conversationManagementActionUnpin      = "unpin"
	conversationManagementActionArchive    = "archive"
	conversationManagementActionRestore    = "restore"
	conversationManagementActionDelete     = "delete"
	conversationManagementActionArchiveAll = "archive_all"
	conversationManagementActionDeleteAll  = "delete_all"
)

var conversationManagementActions = map[string]struct{}{
	conversationManagementActionPin:        {},
	conversationManagementActionUnpin:      {},
	conversationManagementActionArchive:    {},
	conversationManagementActionRestore:    {},
	conversationManagementActionDelete:     {},
	conversationManagementActionArchiveAll: {},
	conversationManagementActionDeleteAll:  {},
}

// conversationManagementService is the narrow storage port used by the HTTP
// bridge. The service implementation owns principal scoping, state
// transitions, and the transaction that consumes the audit intent on ctx.
type conversationManagementService interface {
	ManageConversation(context.Context, agent.Scope, string, string) error
	ListArchivedConversations(context.Context, agent.Scope) ([]agent.Conversation, error)
	ListArchivedConversationsPage(context.Context, agent.Scope, agent.Page) ([]agent.Conversation, error)
}

type conversationManagementRequest struct {
	Action         string `json:"action"`
	ConversationID string `json:"conversationId,omitempty"`
}

type conversationManagementResponse struct {
	Action         string `json:"action"`
	ConversationID string `json:"conversationId,omitempty"`
}

type chatManagementSignal = ui.ChatManagementSignal

type chatManagementSignals struct {
	ChatManagement struct {
		Action         string `json:"action"`
		ConversationID string `json:"conversationId"`
		RequestID      string `json:"requestId"`
	} `json:"chatManagement"`
}

// Saved conversation lifecycle operations need authenticated storage, not a
// configured model or a published data serving scope.
func (h *Handler) conversationManagementRequest(w stdhttp.ResponseWriter, r *stdhttp.Request) (*agent.Service, agent.Scope, bool) {
	if h.options.Service == nil {
		writeJSONError(w, agent.ErrDisabled, stdhttp.StatusServiceUnavailable)
		return nil, agent.Scope{}, false
	}
	scope := h.chatScope(r)
	if strings.TrimSpace(scope.PrincipalID) == "" {
		writeJSONError(w, fmt.Errorf("chat management requires an authenticated principal"), stdhttp.StatusUnauthorized)
		return nil, agent.Scope{}, false
	}
	return h.options.Service, scope, true
}

func (h *Handler) ListArchivedAgentConversations(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	service, scope, ok := h.conversationManagementRequest(w, r)
	if !ok {
		return
	}
	manager, ok := any(service).(conversationManagementService)
	if !ok {
		writeJSONError(w, fmt.Errorf("agent conversation management is unavailable"), stdhttp.StatusServiceUnavailable)
		return
	}
	page, limit, ok := agentPageFromRequest(w, r)
	if !ok {
		return
	}
	rows, err := manager.ListArchivedConversationsPage(r.Context(), scope, page)
	if err != nil {
		writeJSONError(w, err, statusForConversationManagementError(err))
		return
	}
	nextCursor := ""
	if len(rows) > limit {
		nextCursor = rows[limit-1].ID
		rows = rows[:limit]
	}
	out := make([]api.AgentConversationResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, agentConversationDTO(row))
	}
	writeJSON(w, stdhttp.StatusOK, pagedResponseWithCursor(out, nextCursor))
}

func (h *Handler) ManageAgentConversations(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	service, scope, ok := h.conversationManagementRequest(w, r)
	if !ok {
		return
	}
	var input conversationManagementRequest
	if err := decodeAgentJSON(r, &input); err != nil {
		h.writeCommandFailure(w, r, manageAgentConversationsOperation, apigenfailure.Wrap("invalid", err))
		return
	}
	action, conversationID, err := validateConversationManagementRequest(input)
	if err != nil {
		h.writeCommandFailure(w, r, manageAgentConversationsOperation, apigenfailure.Wrap("invalid", err))
		return
	}
	targetID := conversationID
	if targetID == "" {
		targetID = scope.PrincipalID
	}
	if withIntent, intentErr := h.withAuditIntent(r, manageAgentConversationsOperation, scope, "conversation", targetID); intentErr != nil {
		h.writeCommandFailure(w, r, manageAgentConversationsOperation, apigenfailure.Wrap("unavailable", intentErr))
		return
	} else {
		r = withIntent
	}
	manager, ok := any(service).(conversationManagementService)
	if !ok {
		h.writeCommandFailure(w, r, manageAgentConversationsOperation, apigenfailure.Wrap("unavailable", fmt.Errorf("agent conversation management is unavailable")))
		return
	}
	if err := manager.ManageConversation(r.Context(), scope, action, conversationID); err != nil {
		h.writeCommandFailure(w, r, manageAgentConversationsOperation, classifyConversationManagementError(err))
		return
	}
	h.recordLegacyCommandAudit(r, manageAgentConversationsOperation, scope, "conversation", targetID)
	writeJSON(w, stdhttp.StatusOK, conversationManagementResponse{Action: action, ConversationID: conversationID})
}

// ChatManagement is the Datastar adapter for durable conversation actions.
// It emits the result signal together with a fresh active list and sidebar so
// every browser surface converges on the storage worker's committed state.
func (h *Handler) ChatManagement(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	service, scope, ok := h.conversationManagementRequest(w, r)
	if !ok {
		return
	}
	var signals chatManagementSignals
	if err := pagestream.ReadSignals(r, &signals); err != nil && !errors.Is(err, io.EOF) {
		h.writeChatManagementFailure(w, r, signals.ChatManagement.RequestID, err)
		return
	}
	input := conversationManagementRequest{
		Action:         strings.TrimSpace(signals.ChatManagement.Action),
		ConversationID: strings.TrimSpace(signals.ChatManagement.ConversationID),
	}
	requestID := strings.TrimSpace(signals.ChatManagement.RequestID)
	action, conversationID, err := validateConversationManagementRequest(input)
	if err != nil {
		h.writeChatManagementFailure(w, r, requestID, err)
		return
	}
	identity := requestID
	if identity == "" {
		identity = uiRequestIdentity(r, action+"\x00"+conversationID)
	}
	targetID := conversationID
	if targetID == "" {
		targetID = scope.PrincipalID
	}
	ctx, err := beginUICommandInvocation(r, agentUIBinding(manageAgentConversationsOperation), nil, conversationID, action, identity)
	if err != nil {
		h.writeChatManagementFailure(w, r, requestID, err)
		return
	}
	if withIntent, intentErr := h.withAuditIntent(r.WithContext(ctx), manageAgentConversationsOperation, scope, "conversation", targetID); intentErr != nil {
		h.writeChatManagementFailure(w, r, requestID, intentErr)
		return
	} else {
		ctx = withIntent.Context()
		r = withIntent
	}
	manager, ok := any(service).(conversationManagementService)
	if !ok {
		h.writeChatManagementFailure(w, r, requestID, fmt.Errorf("agent conversation management is unavailable"))
		return
	}
	if err := manager.ManageConversation(ctx, scope, action, conversationID); err != nil {
		h.writeChatManagementFailure(w, r, requestID, classifyConversationManagementError(err))
		return
	}
	h.recordLegacyCommandAudit(r, manageAgentConversationsOperation, scope, "conversation", targetID)
	archived, err := manager.ListArchivedConversations(ctx, scope)
	if err != nil {
		h.writeChatManagementFailure(w, r, requestID, err)
		return
	}
	h.writeChatManagementState(w, r, scope, chatManagementSignal{
		Action: action, ConversationID: conversationID, ArchivedConversations: chatConversationSummaries(archived), Message: ui.Optional(conversationManagementMessage(action)), RequestID: ui.Optional(requestID), CompletedRequestID: ui.Optional(requestID),
	})
}

// ChatManagementLoad supplies the archived read model for the settings sheet.
// It accepts a Datastar signal envelope so the request identity can be echoed
// and stale responses can be ignored by the browser.
func (h *Handler) ChatManagementLoad(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	service, scope, ok := h.conversationManagementRequest(w, r)
	if !ok {
		return
	}
	var signals chatManagementSignals
	if err := pagestream.ReadSignals(r, &signals); err != nil && !errors.Is(err, io.EOF) {
		h.writeChatManagementFailure(w, r, signals.ChatManagement.RequestID, err)
		return
	}
	requestID := strings.TrimSpace(signals.ChatManagement.RequestID)
	manager, ok := any(service).(conversationManagementService)
	if !ok {
		h.writeChatManagementFailure(w, r, requestID, fmt.Errorf("agent conversation management is unavailable"))
		return
	}
	archived, err := manager.ListArchivedConversations(r.Context(), scope)
	if err != nil {
		h.writeChatManagementFailure(w, r, requestID, err)
		return
	}
	signal := chatManagementSignal{ArchivedConversations: chatConversationSummaries(archived), RequestID: ui.Optional(requestID), CompletedRequestID: ui.Optional(requestID)}
	h.writeChatManagementState(w, r, scope, signal)
}

func (h *Handler) writeChatManagementState(w stdhttp.ResponseWriter, r *stdhttp.Request, scope agent.Scope, management chatManagementSignal) {
	// Keep the active transcript, composer, and status untouched. The manager
	// route only needs to replace the conversation collection and its sidebar
	// projection; replacing the full ChatSignal would blank an open chat.
	active := h.chatSignal(r.Context(), scope, "", "", false)
	activeID := chatManagementActiveConversationID(r)
	if activeID == "" {
		activeID = active.Agent.ActiveConversationID
	}
	patch := ui.ChatConversationsPatch(active.Agent.Conversations, activeID)
	if referrer, err := url.Parse(r.Referer()); err == nil && strings.HasPrefix(referrer.Path, "/admin/") {
		// Settings has its own navigation; do not introduce app chat history.
		delete(patch, "chrome")
	}
	patch["chatManagement"] = management
	_ = pagestream.NewSignalStream(w, r).Patch(patch)
}

// The management endpoint is mounted outside /chats/{conversation}, so the
// current conversation is available only in the browser's same-page referrer.
// This value controls the sidebar highlight exclusively; authorization and
// mutation targeting always come from the authenticated scope and signal.
func chatManagementActiveConversationID(r *stdhttp.Request) string {
	if r == nil {
		return ""
	}
	referer := strings.TrimSpace(r.Header.Get("Referer"))
	if referer == "" {
		return ""
	}
	parsed, err := url.Parse(referer)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "chats" || parts[1] == "" || parts[1] == "new" || parts[1] == "management" {
		return ""
	}
	conversationID, err := url.PathUnescape(parts[1])
	if err != nil {
		return ""
	}
	return strings.TrimSpace(conversationID)
}

func (h *Handler) writeChatManagementFailure(w stdhttp.ResponseWriter, r *stdhttp.Request, requestID string, err error) {
	message := "Unable to manage conversations."
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		message = err.Error()
	}
	requestID = strings.TrimSpace(requestID)
	h.writeChatManagementState(w, r, h.chatScope(r), chatManagementSignal{Error: ui.Optional(message), RequestID: ui.Optional(requestID), CompletedRequestID: ui.Optional(requestID)})
}

func validateConversationManagementRequest(input conversationManagementRequest) (string, string, error) {
	action := strings.TrimSpace(input.Action)
	conversationID := strings.TrimSpace(input.ConversationID)
	if _, ok := conversationManagementActions[action]; !ok {
		return "", "", fmt.Errorf("action must be one of pin, unpin, archive, restore, delete, archive_all, or delete_all")
	}
	individual := action != conversationManagementActionArchiveAll && action != conversationManagementActionDeleteAll
	if individual && conversationID == "" {
		return "", "", fmt.Errorf("conversationId is required for %s", action)
	}
	if !individual && conversationID != "" {
		return "", "", fmt.Errorf("conversationId must be omitted for %s", action)
	}
	return action, conversationID, nil
}

func classifyConversationManagementError(err error) error {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, agent.ErrNotFound) || errors.Is(err, agent.ErrConversationArchived) {
		return apigenfailure.Wrap("not_found", err)
	}
	if _, classified := apigenfailure.KindOf(err); classified {
		return err
	}
	return apigenfailure.Wrap("unavailable", err)
}

func statusForConversationManagementError(err error) int {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, agent.ErrNotFound) || errors.Is(err, agent.ErrConversationArchived) {
		return stdhttp.StatusNotFound
	}
	return stdhttp.StatusInternalServerError
}

func conversationManagementMessage(action string) string {
	switch action {
	case conversationManagementActionPin:
		return "Conversation pinned."
	case conversationManagementActionUnpin:
		return "Conversation unpinned."
	case conversationManagementActionArchive:
		return "Conversation archived."
	case conversationManagementActionRestore:
		return "Conversation restored."
	case conversationManagementActionDelete:
		return "Conversation deleted."
	case conversationManagementActionArchiveAll:
		return "All conversations archived."
	case conversationManagementActionDeleteAll:
		return "All conversations deleted."
	default:
		return "Conversation updated."
	}
}

func chatConversationSummaries(rows []agent.Conversation) []ui.ChatConversationSummary {
	out := make([]ui.ChatConversationSummary, 0, len(rows))
	for _, row := range rows {
		summary := ui.ChatConversationSummary{
			ID: row.ID, PrincipalID: row.PrincipalID, Title: row.Title, Status: row.Status,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, ArchivedAt: ui.Optional(row.ArchivedAt),
		}
		summary.Pinned = ui.Optional(row.Pinned)
		out = append(out, summary)
	}
	return out
}

func conversationPinned(row agent.Conversation) bool {
	return row.Pinned
}
