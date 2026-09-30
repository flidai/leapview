package http

import (
	"context"
	"fmt"
	stdhttp "net/http"
	"strings"
	"unicode/utf8"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/document"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

// SaveChatDashboardDraft is the generated headless adapter. The entire source
// comes from a successful, owned conversation artifact, never from browser rows.
func (h *Handler) SaveChatDashboardDraft(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.saveChatDashboardDraft(w, r, false)
}

// SaveChatDashboardDraftUI additionally consumes the generated browser claim.
func (h *Handler) SaveChatDashboardDraftUI(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.saveChatDashboardDraft(w, r, true)
}

func (h *Handler) saveChatDashboardDraft(w stdhttp.ResponseWriter, r *stdhttp.Request, browser bool) {
	service, scope, ok := h.chatService(w, r)
	if !ok {
		return
	}
	var input agentgen.SaveChatDashboardDraftRequest
	if err := decodeAgentJSON(r, &input); err != nil {
		h.writeChatDashboardDraftError(w, r, fmt.Errorf("%w: invalid dashboard save request", authoring.ErrInvalidPayload))
		return
	}
	input.Revision, input.Title = strings.TrimSpace(input.Revision), strings.TrimSpace(input.Title)
	if input.Revision == "" || len(input.Revision) > 256 || input.Title == "" || utf8.RuneCountInString(input.Title) > 255 {
		h.writeChatDashboardDraftError(w, r, fmt.Errorf("%w: a preview revision and dashboard title are required", authoring.ErrInvalidPayload))
		return
	}
	conversationID := chi.URLParam(r, "conversation")
	if browser {
		ctx, err := beginUICommandInvocation(r, agentUIBinding(saveChatDashboardDraftOperation), nil, conversationID, conversationID+"\x00"+input.Revision+"\x00"+input.Title, "")
		if err != nil {
			h.writeChatDashboardDraftError(w, r, err)
			return
		}
		r = r.WithContext(ctx)
	}
	key, err := chatVisualIdempotencyKey(r)
	if err != nil {
		h.writeChatDashboardDraftError(w, r, err)
		return
	}
	draft, err := service.ConversationDashboardDraft(r.Context(), scope, conversationID)
	if err != nil {
		h.writeChatDashboardDraftError(w, r, err)
		return
	}
	if draft.Revision != input.Revision {
		h.writeChatDashboardDraftError(w, r, authoring.ErrStaleRevision)
		return
	}
	if h.options.DashboardAuthoring == nil || h.options.AuthorizeSemanticModel == nil {
		h.writeChatDashboardDraftError(w, r, fmt.Errorf("dashboard authoring is unavailable"))
		return
	}
	projectID, err := projectgraph.NewResourceID(strings.TrimSpace(scope.ProjectID))
	if err != nil {
		h.writeChatDashboardDraftError(w, r, err)
		return
	}
	modelID, err := projectgraph.NewResourceID(draft.SemanticModelID)
	if err != nil {
		h.writeChatDashboardDraftError(w, r, err)
		return
	}
	if err := h.options.AuthorizeSemanticModel(r.Context(), scope, modelID.String()); err != nil {
		h.writeChatDashboardDraftError(w, r, err)
		return
	}
	if err := h.options.DashboardAuthoring.AuthorizeNewDashboard(r.Context(), projectID, scope.PrincipalID, modelID); err != nil {
		h.writeChatDashboardDraftError(w, r, err)
		return
	}
	value, err := chatDashboardDocument(draft, authoring.CommandID(key))
	if err != nil {
		h.writeChatDashboardDraftError(w, r, err)
		return
	}
	ctx, err := chatDashboardSaveAuditContext(r, scope, key)
	if err != nil {
		h.writeChatDashboardDraftError(w, r, err)
		return
	}
	result, err := h.options.DashboardAuthoring.CreateFromDocument(ctx, authoringservice.CreateFromDocumentRequest{
		ProjectID: projectID, ActorID: scope.PrincipalID, Document: value, Title: input.Title,
		Origin: authoring.OriginAgent, ConversationID: conversationID, ToolCallID: draft.ToolCallID,
		IdempotencyKey: key, OperationKind: "create",
	})
	if err != nil {
		h.writeChatDashboardDraftError(w, r, err)
		return
	}
	target := result.Lifecycle.ID.String()
	h.recordLegacyCommandAudit(r, saveChatDashboardDraftOperation, scope, "dashboard", target)
	writeJSON(w, stdhttp.StatusOK, agentgen.AddChatVisualToDashboardResponse{
		DashboardId: target, Title: result.Lifecycle.Title, PageId: "overview", Href: chatDashboardHref(target, "overview"),
	})
}

// Build the complete canonical document before invoking the single create
// transaction, preserving each card's independent governed filter intent.
func chatDashboardDocument(draft agent.ChatDashboardDraft, commandID authoring.CommandID) (document.DashboardDocument, error) {
	modelID, err := projectgraph.NewResourceID(draft.SemanticModelID)
	if err != nil || len(draft.Visuals) == 0 {
		return document.DashboardDocument{}, fmt.Errorf("%w: dashboard needs a semantic model and visuals", authoring.ErrInvalidPayload)
	}
	value := newChatVisualDashboardDocument(modelID)
	for _, card := range draft.Visuals {
		visual, err := agenttools.NormalizeChatVisualDefinition(card.Visual)
		if err != nil {
			return document.DashboardDocument{}, fmt.Errorf("%w: %v", authoring.ErrInvalidPayload, err)
		}
		_, err = application.AddChatVisualToDocument(&value, "overview", application.ChatVisualImport{
			ArtifactID: card.ArtifactID, ToolCallID: draft.ToolCallID, SemanticModelID: modelID,
			Visual: normalizeImportedChatLegend(visual), Filters: card.Filters,
		}, commandID)
		if err != nil {
			return document.DashboardDocument{}, err
		}
	}
	return value, nil
}

func chatDashboardSaveAuditContext(r *stdhttp.Request, scope agent.Scope, key string) (context.Context, error) {
	operationID := saveChatDashboardDraftOperation.APIGenOperationID()
	contract, ok := agentgen.GetAPIGenCommandRuntimeContract(operationID)
	if !ok || contract.Guarantee != apigencommand.GuaranteeTransactional {
		return nil, fmt.Errorf("dashboard save audit contract is unavailable")
	}
	metadata, err := agentgen.EncodeGenSaveChatDashboardDraftAuditPayload(agentgen.GenSchemaChatVisualDashboardCommandAuditPayload{
		OperationId: operationID, ProjectId: scope.ProjectID, DashboardId: "pending-dashboard", DraftId: "pending-draft", Origin: string(authoring.OriginAgent),
	})
	if err != nil {
		return nil, err
	}
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if correlationID == "" {
		correlationID = requestID
	}
	return authoring.WithAuditIntent(r.Context(), access.AuditIntent{
		EventID: key, Source: "dashboard.authoring", Operation: operationID,
		ActorID: scope.PrincipalID, PrincipalID: scope.PrincipalID, Action: contract.AuditAction,
		ResourceKind: "dashboard", ResourceID: "pending-dashboard", Capability: access.CapabilityResourceEdit,
		Outcome: "success", RequestID: requestID, CorrelationID: correlationID, MetadataJSON: metadata,
	}), nil
}

func (h *Handler) writeChatDashboardDraftError(w stdhttp.ResponseWriter, r *stdhttp.Request, err error) {
	kind := "unavailable"
	switch chatVisualErrorStatus(err) {
	case stdhttp.StatusUnprocessableEntity:
		kind = "invalid"
	case stdhttp.StatusForbidden:
		kind = "forbidden"
	case stdhttp.StatusNotFound:
		kind = "not_found"
	case stdhttp.StatusConflict:
		kind = "conflict"
	}
	h.writeCommandFailure(w, r, saveChatDashboardDraftOperation, apigenfailure.Wrap(kind, err))
}
