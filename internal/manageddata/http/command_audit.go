package http

import (
	"context"
	"log/slog"
	stdhttp "net/http"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	manageddatagen "github.com/flidai/leapview/internal/manageddata/api/gen"
)

var errCommandAuditUnavailable = apigenfailure.New("unavailable", "managed-data command audit is unavailable")

func (h *Handler) recordCommandAudit(
	r *stdhttp.Request,
	operationID string,
	principalID string,
	projectID string,
	connectionID string,
	targetType string,
	targetID string,
) error {
	if h == nil || h.options.RecordCommandAudit == nil {
		return errCommandAuditUnavailable
	}
	requestID := firstCommandHeader(r, "X-Request-Id", "X-Request-ID")
	correlationID := firstCommandHeader(r, "X-Correlation-Id", "X-Correlation-ID")
	if correlationID == "" {
		correlationID = requestID
	}
	surface := "api"
	if strings.EqualFold(firstCommandHeader(r, "X-LeapView-Invocation-Surface", "X-LeapView-Client"), "cli") {
		surface = "cli"
	}
	logger := h.options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	executor, err := apigencommand.NewExecutor(manageddatagen.GetAPIGenCommandRuntimeContract, logger)
	if err != nil {
		return err
	}
	err = executor.Execute(r.Context(), operationID, apigencommand.Execution{
		BestEffortAudit: func(ctx context.Context, _ apigencommand.Contract) error {
			return h.options.RecordCommandAudit(ctx, CommandAuditInput{
				OperationID: operationID, PrincipalID: strings.TrimSpace(principalID),
				ProjectID: strings.TrimSpace(projectID), ConnectionID: strings.TrimSpace(connectionID),
				TargetType: strings.TrimSpace(targetType), TargetID: strings.TrimSpace(targetID),
				RequestID: requestID, CorrelationID: correlationID, Surface: surface,
			})
		},
		LogMessage: "best-effort managed-data command audit failed",
		LogAttributes: []slog.Attr{
			slog.String("principal_id", strings.TrimSpace(principalID)),
			slog.String("project_id", strings.TrimSpace(projectID)),
			slog.String("connection_id", strings.TrimSpace(connectionID)),
			slog.String("target_type", strings.TrimSpace(targetType)),
			slog.String("target_id", strings.TrimSpace(targetID)),
			slog.String("request_id", requestID),
		},
	})
	if err != nil {
		return err
	}
	return nil
}

func (h *Handler) commandAuditActor(w stdhttp.ResponseWriter, r *stdhttp.Request) (string, bool) {
	if h == nil || h.options.RecordCommandAudit == nil {
		if h != nil {
			h.writeUnavailable(w, r)
		}
		return "", false
	}
	return h.actor(w, r)
}

func (h *Handler) commandAuditActorForOperation(w stdhttp.ResponseWriter, r *stdhttp.Request, operationID string) (string, bool) {
	if h == nil || h.options.RecordCommandAudit == nil {
		if h != nil {
			h.writeCommandUnavailable(w, r, operationID)
		}
		return "", false
	}
	return h.actor(w, r)
}

func firstCommandHeader(r *stdhttp.Request, names ...string) string {
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
