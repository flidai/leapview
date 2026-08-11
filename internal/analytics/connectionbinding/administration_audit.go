package connectionbinding

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
)

type AdministrationAuditAction string

const (
	AuditBindingCreated  AdministrationAuditAction = "connection.binding.created"
	AuditBindingUpdated  AdministrationAuditAction = "connection.binding.updated"
	AuditBindingEnabled  AdministrationAuditAction = "connection.binding.enabled"
	AuditBindingDisabled AdministrationAuditAction = "connection.binding.disabled"
)

type AdministrationAuditOutcome string

const AdministrationAuditSucceeded AdministrationAuditOutcome = "succeeded"

type AdministrationAuditEvent struct {
	WorkspaceID         string                     `json:"workspaceId"`
	BindingID           string                     `json:"bindingId"`
	TargetID            string                     `json:"targetId"`
	LogicalConnectionID LogicalConnectionID        `json:"logicalConnection"`
	Actor               string                     `json:"actor"`
	Action              AdministrationAuditAction  `json:"action"`
	Outcome             AdministrationAuditOutcome `json:"outcome"`
	Revision            int64                      `json:"revision"`
	Timestamp           time.Time                  `json:"timestamp"`
}

type AdministrationAuditRecorder interface {
	RecordConnectionAdministration(context.Context, AdministrationAuditEvent) error
}

func (service *Administration) recordMutation(
	ctx context.Context,
	actor string,
	action AdministrationAuditAction,
	binding TargetBinding,
) error {
	if service == nil || service.audit == nil {
		return ErrAdministrationAuditUnavailable
	}
	event := AdministrationAuditEvent{
		WorkspaceID: binding.Scope.WorkspaceID, BindingID: binding.ID,
		TargetID: binding.TargetID, LogicalConnectionID: binding.LogicalConnectionID,
		Actor: strings.TrimSpace(actor), Action: action, Outcome: AdministrationAuditSucceeded,
		Revision: binding.Revision, Timestamp: service.now().UTC(),
	}
	operationID, ok := administrationOperationID(action)
	if !ok {
		return fmt.Errorf("connection administration audit action %q has no generated operation", action)
	}
	executor, err := apigencommand.NewExecutor(analyticsgen.GetAPIGenCommandRuntimeContract, service.logger)
	if err != nil {
		return err
	}
	return executor.Execute(ctx, operationID, apigencommand.Execution{
		BestEffortAudit: func(context.Context, apigencommand.Contract) error {
			return service.audit.RecordConnectionAdministration(context.WithoutCancel(ctx), event)
		},
		LogMessage: "best-effort connection administration audit failed",
		LogAttributes: []slog.Attr{
			slog.String("workspace_id", binding.Scope.WorkspaceID),
			slog.String("principal_id", strings.TrimSpace(actor)),
			slog.String("binding_id", binding.ID),
			slog.String("target_id", binding.TargetID),
			slog.String("logical_connection", string(binding.LogicalConnectionID)),
			slog.Int64("revision", binding.Revision),
		},
	})
}

func administrationOperationID(action AdministrationAuditAction) (string, bool) {
	switch action {
	case AuditBindingCreated:
		return string(analyticsgen.GenOperationCreateTargetConnectionBinding), true
	case AuditBindingUpdated:
		return string(analyticsgen.GenOperationUpdateTargetConnectionBinding), true
	case AuditBindingEnabled:
		return string(analyticsgen.GenOperationEnableTargetConnectionBinding), true
	case AuditBindingDisabled:
		return string(analyticsgen.GenOperationDisableTargetConnectionBinding), true
	default:
		return "", false
	}
}
