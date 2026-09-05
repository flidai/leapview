package connectionbinding

import (
	"context"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"log/slog"
	"strings"
	"time"
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
	TargetID     TargetID                   `json:"targetId"`
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
	// Command producers carry a durable Access intent in the context. The
	// source repository consumes it inside its own transaction, so the
	// legacy best-effort recorder must not emit a second event after commit.
	if _, ok := AuditIntentFromContext(ctx); ok {
		return nil
	}
	if service != nil && service.requireAuditIntent {
		return ErrAdministrationAuditUnavailable
	}
	if service == nil || service.audit == nil {
		return ErrAdministrationAuditUnavailable
	}
	event := AdministrationAuditEvent{
		ProjectID: binding.Scope.ProjectID, BindingID: binding.ID,
		TargetID: binding.TargetID, ConnectionID: binding.ConnectionID,
		Actor: strings.TrimSpace(actor), Action: action, Outcome: AdministrationAuditSucceeded,
		Revision: binding.Revision, Timestamp: service.now().UTC(),
	}
	if err := service.audit.RecordConnectionAdministration(context.WithoutCancel(ctx), event); err != nil {
		service.logger.Error("best-effort connection administration audit failed", "error", err,
			slog.String("project_id", binding.Scope.ProjectID.String()),
			slog.String("principal_id", strings.TrimSpace(actor)),
			slog.String("binding_id", binding.ID.String()),
			slog.String("target_id", binding.TargetID.String()),
			slog.String("connection_id", binding.ConnectionID.String()),
			slog.Int64("revision", binding.Revision),
			slog.String("action", string(action)))
	}
	return nil
}
