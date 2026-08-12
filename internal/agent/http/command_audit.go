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
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

var (
	createAgentConversationOperation  = agentgen.GenCommandOperationCreateAgentConversation()
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

func (h *Handler) recordCommandAudit(
	r *stdhttp.Request,
	operationID agentgen.GenCommandOperationID,
	scope agent.Scope,
	targetType string,
	targetID string,
) {
	operationIDValue := operationID.APIGenOperationID()
	if h == nil || h.options.RecordCommandAudit == nil {
		return
	}
	requestID := firstNonEmptyHeader(r, "X-Request-Id", "X-Request-ID")
	correlationID := firstNonEmptyHeader(r, "X-Correlation-Id", "X-Correlation-ID")
	if correlationID == "" {
		correlationID = requestID
	}
	surface := "api"
	switch strings.ToLower(firstNonEmptyHeader(r, "X-LeapView-Invocation-Surface", "X-LeapView-Client")) {
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
	err = executor.Execute(r.Context(), operationIDValue, apigencommand.Execution{
		BestEffortAudit: func(ctx context.Context, _ apigencommand.Contract) error {
			return h.options.RecordCommandAudit(ctx, CommandAuditInput{
				OperationID: operationIDValue, Scope: scope,
				TargetType: strings.TrimSpace(targetType), TargetID: strings.TrimSpace(targetID),
				RequestID: requestID, CorrelationID: correlationID, Surface: surface,
			})
		},
		LogMessage: "best-effort agent command audit failed",
		LogAttributes: []slog.Attr{
			slog.String("principal_id", strings.TrimSpace(scope.PrincipalID)),
			slog.String("target_type", strings.TrimSpace(targetType)),
			slog.String("target_id", strings.TrimSpace(targetID)),
			slog.String("request_id", requestID),
		},
	})
	if err != nil {
		logger.ErrorContext(r.Context(), "agent command contract execution failed", "operation_id", operationIDValue, "error", err)
	}
}

// uiRequestIdentity returns a deterministic identity for a browser command
// retry. Datastar requests normally carry the stable client cookie; an
// explicit request ID takes precedence when the browser or a test supplies
// one. The operation-specific idempotency key is derived from this identity
// by beginUICommandInvocation.
func uiRequestIdentity(r *stdhttp.Request, input string) string {
	if value := firstNonEmptyHeader(r, "X-Request-Id", "X-Request-ID"); value != "" {
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
	contract, ok := agentgen.GetAPIGenCommandRuntimeContract(operationID)
	if !ok {
		return r.Context(), apigencommand.ErrContractNotFound
	}
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
	idempotencyKey := firstNonEmptyHeader(r, "Idempotency-Key")
	if idempotencyKey == "" {
		idempotencyKey = "ui:" + operationID + ":" + identity
	}
	invocation := apigencommand.Invocation{
		TargetValues: map[string]string{
			"conversation": strings.TrimSpace(target),
		},
		IdempotencyKey: idempotencyKey,
		RequestID:      identity,
		CorrelationID:  firstNonEmptyHeader(r, "X-Correlation-Id", "X-Correlation-ID"),
	}
	var ctx context.Context
	var err error
	if len(workflow) > 0 {
		ctx, _, err = uicommand.BeginWorkflowInvocation(r, workflow, binding, contract, invocation)
	} else {
		ctx, _, err = uicommand.BeginInvocation(r, binding, contract, invocation)
	}
	return ctx, err
}

func agentUIBinding(operationID agentgen.GenCommandOperationID) uicommand.Binding {
	switch operationID.APIGenOperationID() {
	case createAgentConversationOperation.APIGenOperationID():
		return agentgen.GenUIActionCreateAgentConversation()
	case createAgentRunOperation.APIGenOperationID():
		return agentgen.GenUIActionCreateAgentRun()
	default:
		return uicommand.Binding{}
	}
}

func firstNonEmptyHeader(r *stdhttp.Request, names ...string) string {
	if r == nil {
		return ""
	}
	for _, name := range names {
		if value := strings.TrimSpace(r.Header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}
