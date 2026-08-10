package http

import (
	"context"
	"log/slog"
	stdhttp "net/http"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/agent"
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
)

const (
	createAgentConversationOperation  = "createAgentConversation"
	archiveAgentConversationOperation = "archiveAgentConversation"
	updateAgentConversationOperation  = "updateAgentConversation"
	createAgentRunOperation           = "createAgentRun"
	cancelAgentRunOperation           = "cancelAgentRun"
)

// CommandAuditInput identifies one successfully completed agent command.
type CommandAuditInput struct {
	OperationID   string
	Scope         agent.Scope
	TargetType    string
	TargetID      string
	RequestID     string
	CorrelationID string
}

func (h *Handler) recordCommandAudit(
	r *stdhttp.Request,
	operationID string,
	scope agent.Scope,
	targetType string,
	targetID string,
) {
	if h == nil || h.options.RecordCommandAudit == nil {
		return
	}
	requestID := firstNonEmptyHeader(r, "X-Request-Id", "X-Request-ID")
	correlationID := firstNonEmptyHeader(r, "X-Correlation-Id", "X-Correlation-ID")
	if correlationID == "" {
		correlationID = requestID
	}
	logger := h.options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	executor, err := apigencommand.NewExecutor(agentgen.GetAPIGenCommandRuntimeContract, logger)
	if err != nil {
		logger.ErrorContext(r.Context(), "agent command contract executor is unavailable", "operation_id", operationID, "error", err)
		return
	}
	err = executor.Execute(r.Context(), operationID, apigencommand.Execution{
		BestEffortAudit: func(ctx context.Context, _ apigencommand.Contract) error {
			return h.options.RecordCommandAudit(ctx, CommandAuditInput{
				OperationID: operationID, Scope: scope,
				TargetType: strings.TrimSpace(targetType), TargetID: strings.TrimSpace(targetID),
				RequestID: requestID, CorrelationID: correlationID,
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
		logger.ErrorContext(r.Context(), "agent command contract execution failed", "operation_id", operationID, "error", err)
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
