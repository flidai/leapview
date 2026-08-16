package app

import (
	"context"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
)

// connectionBindingDependenciesWithoutConsumers is the composition adapter
// for the current serving graph. Target-owned bindings are not yet referenced
// by compiled serving states, so there are no registered dependents to report.
// The adapter keeps that fact explicit at the capability boundary.
type connectionBindingDependenciesWithoutConsumers struct{}

func (connectionBindingDependenciesWithoutConsumers) Dependents(
	context.Context,
	analyticsmodule.ConnectionTargetBinding,
) ([]analyticsmodule.ConnectionBindingDependency, error) {
	return nil, nil
}

type connectionRotationAuditRecorder struct {
	record func(context.Context, accessmodule.AuditEventInput) error
}

func (recorder connectionRotationAuditRecorder) RecordCredentialRotation(
	ctx context.Context,
	event analyticsmodule.ConnectionRotationAuditEvent,
) error {
	metadata, err := connectionRotationAuditMetadata(event)
	if err != nil {
		return err
	}
	principalID := strings.TrimPrefix(event.Actor, "principal:")
	if strings.HasPrefix(event.Actor, "runtime:") {
		principalID = ""
	}
	if recorder.record == nil {
		return nil
	}
	return recorder.record(ctx, accessmodule.AuditEventInput{
		PrincipalID:  principalID,
		Action:       string(event.Operation),
		ResourceKind: "connection_binding",
		ResourceID:   event.BindingID.String(),
		Capability:   access.CapabilityResourceUse,
		Status:       string(event.Outcome),
		MetadataJSON: metadata,
	})
}

type connectionAdministrationAuditRecorder struct {
	record func(context.Context, accessmodule.AuditEventInput) error
}

func (recorder connectionAdministrationAuditRecorder) RecordConnectionAdministration(
	ctx context.Context,
	event analyticsmodule.ConnectionAdministrationAuditEvent,
) error {
	metadata, err := connectionAdministrationAuditMetadata(event)
	if err != nil {
		return err
	}
	if recorder.record == nil {
		return nil
	}
	return recorder.record(ctx, accessmodule.AuditEventInput{
		PrincipalID: event.Actor,
		Action:      string(event.Action), ResourceKind: "connection_binding", ResourceID: event.BindingID.String(),
		Capability: access.CapabilityResourceManage,
		Status:     string(event.Outcome), MetadataJSON: metadata,
	})
}

func connectionRotationAuditMetadata(event analyticsmodule.ConnectionRotationAuditEvent) (string, error) {
	return analyticsmodule.EncodeConnectionRotationAuditMetadata(event)
}

func connectionAdministrationAuditMetadata(event analyticsmodule.ConnectionAdministrationAuditEvent) (string, error) {
	return analyticsmodule.EncodeConnectionAdministrationAuditMetadata(event)
}
