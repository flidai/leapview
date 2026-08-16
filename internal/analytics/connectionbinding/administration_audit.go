package connectionbinding

import (
	"context"
	"fmt"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
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
	ProjectID    projectgraph.ResourceID    `json:"projectId"`
	BindingID    BindingID                  `json:"bindingId"`
	TargetID     string                     `json:"targetId"`
	ConnectionID projectgraph.ResourceID    `json:"connectionId"`
	Actor        string                     `json:"actor"`
	Action       AdministrationAuditAction  `json:"action"`
	Outcome      AdministrationAuditOutcome `json:"outcome"`
	Revision     int64                      `json:"revision"`
	Timestamp    time.Time                  `json:"timestamp"`
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
		ProjectID: binding.Scope.ProjectID, BindingID: binding.ID,
		TargetID: binding.TargetID, ConnectionID: binding.ConnectionID,
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
			slog.String("project_id", binding.Scope.ProjectID.String()),
			slog.String("principal_id", strings.TrimSpace(actor)),
			slog.String("binding_id", binding.ID),
			slog.String("target_id", binding.TargetID),
			slog.String("connection_id", binding.ConnectionID.String()),
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
