package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	stdhttp "net/http"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/agent"
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	platformhttp "github.com/flidai/leapview/internal/platform/http"
	httpmiddleware "github.com/flidai/leapview/internal/platform/http/middleware"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

var (
	updateAgentConfigOperation        = agentgen.GenCommandOperationUpdateAgentConfig()
	createAgentConversationOperation  = agentgen.GenCommandOperationCreateAgentConversation()
	manageAgentConversationsOperation = agentgen.GenCommandOperationManageAgentConversations()
	archiveAgentConversationOperation = agentgen.GenCommandOperationArchiveAgentConversation()
	updateAgentConversationOperation  = agentgen.GenCommandOperationUpdateAgentConversation()
	createAgentRunOperation           = agentgen.GenCommandOperationCreateAgentRun()
	cancelAgentRunOperation           = agentgen.GenCommandOperationCancelAgentRun()
)

// CommandAuditInput identifies one successfully completed agent command.
type CommandAuditInput struct {
	OperationID   string
	Scope         agent.Scope
	TargetType    string
	TargetID      string
	RequestID     string
	CorrelationID string
	Surface       string
}

// withAuditIntent resolves a generated command intent before a mutation and
// carries it into the source-owned repository transaction. When no durable
// builder is configured, the legacy best-effort producer remains available.
func (h *Handler) withAuditIntent(r *stdhttp.Request, operationID agentgen.GenCommandOperationID, scope agent.Scope, targetType, targetID string) (*stdhttp.Request, error) {
	if h == nil || h.options.BuildAuditIntent == nil {
		return r, nil
	}
	requestID := platformhttp.FirstNonEmptyHeader(r, "Idempotency-Key", "X-Request-Id", "X-Request-ID")
	correlationID := platformhttp.FirstNonEmptyHeader(r, "X-Correlation-Id", "X-Correlation-ID")
	if correlationID == "" {
		correlationID = requestID
	}
	surface := "api"
	switch strings.ToLower(platformhttp.FirstNonEmptyHeader(r, "X-LeapView-Invocation-Surface", "X-LeapView-Client")) {
	case "cli":
		surface = "cli"
	case "ui":
		surface = "ui"
	}
	intent, err := h.options.BuildAuditIntent(r.Context(), CommandAuditInput{
		OperationID: operationID.APIGenOperationID(), Scope: scope, TargetType: strings.TrimSpace(targetType), TargetID: strings.TrimSpace(targetID),
		RequestID: requestID, CorrelationID: correlationID, Surface: surface,
	})
	if err != nil {
		return r, err
	}
	if intent == nil {
		return r, nil
	}
	return r.WithContext(agent.WithAuditIntent(r.Context(), *intent)), nil
}

func (h *Handler) recordCommandAudit(
	r *stdhttp.Request,
	operationID agentgen.GenCommandOperationID,
	scope agent.Scope,
	targetType string,
	targetID string,
) {
	operationIDValue := operationID.APIGenOperationID()
	if h == nil {
		return
	}
	requestID := platformhttp.FirstNonEmptyHeader(r, "X-Request-Id", "X-Request-ID")
	correlationID := platformhttp.FirstNonEmptyHeader(r, "X-Correlation-Id", "X-Correlation-ID")
	if correlationID == "" {
		correlationID = requestID
	}
	surface := "api"
	switch strings.ToLower(platformhttp.FirstNonEmptyHeader(r, "X-LeapView-Invocation-Surface", "X-LeapView-Client")) {
	case "cli":
		surface = "cli"
	case "ui":
		surface = "ui"
	}
	logger := h.options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	executor, err := apigencommand.NewExecutor(agentgen.GetAPIGenCommandRuntimeContract, logger)
	if err != nil {
		logger.ErrorContext(r.Context(), "agent command contract executor is unavailable", "operation_id", operationIDValue, "error", err)
		return
	}
	execution := apigencommand.Execution{
		Transactional: func(context.Context, apigencommand.Contract) error {
			// The source repository already committed the mutation and audit
			// intent atomically. This callback marks the generated invocation as
			// complete so the outer command guard can release the response.
			return nil
		},
		BestEffortAudit: func(ctx context.Context, _ apigencommand.Contract) error {
			if h.options.RecordCommandAudit == nil {
				return apigencommand.ErrExecutionUnavailable
			}
			return h.options.RecordCommandAudit(ctx, CommandAuditInput{
				OperationID: operationIDValue, Scope: scope,
				TargetType: strings.TrimSpace(targetType), TargetID: strings.TrimSpace(targetID),
				RequestID: requestID, CorrelationID: correlationID, Surface: surface,
			})
		},
		LogMessage: "best-effort agent command audit failed",
	}
	err = executor.Execute(r.Context(), operationIDValue, execution)
	if err != nil {
		logger.ErrorContext(r.Context(), "agent command contract execution failed", "operation_id", operationIDValue, "error", err)
	}
}

func (h *Handler) recordLegacyCommandAudit(r *stdhttp.Request, operationID agentgen.GenCommandOperationID, scope agent.Scope, targetType, targetID string) {
	h.recordCommandAudit(r, operationID, scope, targetType, targetID)
}

// uiRequestIdentity returns a deterministic identity for a browser command
// retry. Datastar requests normally carry the stable client cookie; an
// explicit request ID takes precedence when the browser or a test supplies
// one. The operation-specific idempotency key is derived from this identity
// by beginUICommandInvocation.
func uiRequestIdentity(r *stdhttp.Request, input string) string {
	if value := platformhttp.FirstNonEmptyHeader(r, "X-Request-Id", "X-Request-ID"); value != "" && !httpmiddleware.RequestIDWasGenerated(r) {
		return value
	}
	clientID := "default"
	if r != nil {
		clientID = chatClientID(r)
	}
	path := ""
	if r != nil && r.URL != nil {
		path = r.URL.Path
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{"ui", clientID, path, input}, "\x00")))
	return "ui_" + hex.EncodeToString(sum[:16])
}

// beginUICommandInvocation applies the generated policy to direct browser
// commands. The returned context must be used for the mutation and generated
// audit execution so the Guard can prove command completion.
func beginUICommandInvocation(r *stdhttp.Request, binding uicommand.Binding, workflow []uicommand.Binding, target, input, identity string) (context.Context, error) {
	operationID := binding.OperationID()
	identity = strings.TrimSpace(identity)
	if identity == "" {
		identity = uiRequestIdentity(r, input)
	}
	if r != nil {
		if r.Header.Get("X-Request-ID") == "" {
			r.Header.Set("X-Request-ID", identity)
		}
		r.Header.Set("X-LeapView-Invocation-Surface", string(apigencommand.SurfaceUI))
	}
	idempotencyKey := platformhttp.FirstNonEmptyHeader(r, "Idempotency-Key")
	if idempotencyKey == "" {
		idempotencyKey = "ui:" + operationID + ":" + identity
	}
	if len(workflow) > 0 {
		if err := uicommand.VerifyWorkflowClaims(uicommand.OperationClaims(r), workflow); err != nil {
			return r.Context(), err
		}
	} else {
		if err := uicommand.VerifyClaim(uicommand.OperationClaims(r), operationID); err != nil {
			return r.Context(), err
		}
	}
	correlationID := platformhttp.FirstNonEmptyHeader(r, "X-Correlation-Id", "X-Correlation-ID")
	switch operationID {
	case updateAgentConfigOperation.APIGenOperationID():
		ctx, _, err := agentgen.BeginGenUpdateAgentConfigCommand(r.Context(), agentgen.GenUpdateAgentConfigCommandInvocation{
			Surface: apigencommand.SurfaceUI, ConcurrencyToken: platformhttp.FirstNonEmptyHeader(r, "If-Match"),
			RequestID: identity, CorrelationID: correlationID,
		})
		return ctx, err
	case createAgentConversationOperation.APIGenOperationID():
		ctx, _, err := agentgen.BeginGenCreateAgentConversationCommand(r.Context(), agentgen.GenCreateAgentConversationCommandInvocation{
			Surface: apigencommand.SurfaceUI, IdempotencyKey: idempotencyKey,
			RequestID: identity, CorrelationID: correlationID,
		})
		return ctx, err
	case manageAgentConversationsOperation.APIGenOperationID():
		ctx, _, err := agentgen.BeginGenManageAgentConversationsCommand(r.Context(), agentgen.GenManageAgentConversationsCommandInvocation{
			Surface: apigencommand.SurfaceUI, IdempotencyKey: idempotencyKey,
			RequestID: identity, CorrelationID: correlationID,
		})
		return ctx, err
	case createAgentRunOperation.APIGenOperationID():
		ctx, _, err := agentgen.BeginGenCreateAgentRunCommand(r.Context(), agentgen.GenCreateAgentRunCommandInvocation{
			Surface: apigencommand.SurfaceUI, Conversation: strings.TrimSpace(target), IdempotencyKey: idempotencyKey,
			RequestID: identity, CorrelationID: correlationID,
		})
		return ctx, err
	case cancelAgentRunOperation.APIGenOperationID():
		ctx, _, err := agentgen.BeginGenCancelAgentRunCommand(r.Context(), agentgen.GenCancelAgentRunCommandInvocation{
			Surface: apigencommand.SurfaceUI, Conversation: strings.TrimSpace(target), IdempotencyKey: idempotencyKey,
			RequestID: identity, CorrelationID: correlationID,
		})
		return ctx, err
	default:
		return r.Context(), apigencommand.ErrContractNotFound
	}
}

func agentUIBinding(operationID agentgen.GenCommandOperationID) uicommand.Binding {
	switch operationID.APIGenOperationID() {
	case updateAgentConfigOperation.APIGenOperationID():
		return agentgen.GenUIActionUpdateAgentConfig()
	case createAgentConversationOperation.APIGenOperationID():
		return agentgen.GenUIActionCreateAgentConversation()
	case manageAgentConversationsOperation.APIGenOperationID():
		return agentgen.GenUIActionManageAgentConversations()
	case createAgentRunOperation.APIGenOperationID():
		return agentgen.GenUIActionCreateAgentRun()
	case cancelAgentRunOperation.APIGenOperationID():
		return agentgen.GenUIActionCancelAgentRun()
	default:
		return uicommand.Binding{}
	}
}
