package http

import (
	"context"
	stdhttp "net/http"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	manageddatagen "github.com/flidai/leapview/internal/manageddata/api/gen"
	platformhttp "github.com/flidai/leapview/internal/platform/http"
)

func (h *Handler) completeCommand(r *stdhttp.Request, operationID manageddatagen.GenCommandOperationID) error {
	executor, err := apigencommand.NewExecutor(manageddatagen.GetAPIGenCommandRuntimeContract, h.options.Logger)
	if err != nil {
		return err
	}
	return executor.Execute(r.Context(), operationID.APIGenOperationID(), apigencommand.Execution{
		Transactional: func(context.Context, apigencommand.Contract) error { return nil },
	})
}

func (h *Handler) buildAuditIntent(
	r *stdhttp.Request,
	operationID manageddatagen.GenCommandOperationID,
	principalID, projectID, connectionID, targetType, targetID string,
) (*access.AuditIntent, error) {
	if h == nil || h.options.BuildAuditIntent == nil {
		return nil, nil
	}
	requestID := platformhttp.FirstNonEmptyHeader(r, "X-Request-Id", "X-Request-ID")
	correlationID := platformhttp.FirstNonEmptyHeader(r, "X-Correlation-Id", "X-Correlation-ID")
	if correlationID == "" {
		correlationID = requestID
	}
	surface := "api"
	if strings.EqualFold(platformhttp.FirstNonEmptyHeader(r, "X-LeapView-Invocation-Surface", "X-LeapView-Client"), "cli") {
		surface = "cli"
	}
	return h.options.BuildAuditIntent(r.Context(), CommandAuditInput{
		OperationID: operationID.APIGenOperationID(), PrincipalID: strings.TrimSpace(principalID),
		ProjectID: strings.TrimSpace(projectID), ConnectionID: strings.TrimSpace(connectionID),
		TargetType: strings.TrimSpace(targetType), TargetID: strings.TrimSpace(targetID),
		RequestID: requestID, CorrelationID: correlationID, Surface: surface,
	})
}

func (h *Handler) commandAuditActor(w stdhttp.ResponseWriter, r *stdhttp.Request) (string, bool) {
	if h == nil || h.options.BuildAuditIntent == nil {
		if h != nil {
			h.writeUnavailable(w, r)
		}
		return "", false
	}
	return h.actor(w, r)
}

func (h *Handler) commandAuditActorForOperation(w stdhttp.ResponseWriter, r *stdhttp.Request, operationID manageddatagen.GenCommandOperationID) (string, bool) {
	if h == nil || h.options.BuildAuditIntent == nil {
		if h != nil {
			h.writeCommandUnavailable(w, r, operationID)
		}
		return "", false
	}
	return h.actor(w, r)
}
