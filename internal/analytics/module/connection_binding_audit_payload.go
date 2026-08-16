package module

import (
	"encoding/json"
	"fmt"

	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
)

// EncodeConnectionRotationAuditMetadata applies the generated payload contract
// for command-owned rotation events while preserving legacy background events.
func EncodeConnectionRotationAuditMetadata(event ConnectionRotationAuditEvent) (string, error) {
	payload := analyticsgen.GenSchemaTargetConnectionRotationAuditPayload{
		Operation:       string(event.Operation),
		Outcome:         string(event.Outcome),
		ProviderVersion: event.ProviderVersion,
		DiagnosticCode:  event.Reason,
		TargetId:        event.TargetID.String(),
	}
	switch event.Operation {
	case connectionbinding.RefreshRequested:
		encoded, err := analyticsgen.EncodeGenRefreshTargetConnectionBindingAuditPayload(payload)
		return addBindingMetadata(encoded, event.BindingID.String(), err)
	case connectionbinding.RefreshTest:
		encoded, err := analyticsgen.EncodeGenTestTargetConnectionBindingAuditPayload(payload)
		return addBindingMetadata(encoded, event.BindingID.String(), err)
	default:
		legacy, err := json.Marshal(map[string]any{
			"operation": event.Operation, "outcome": event.Outcome,
			"providerVersion": event.ProviderVersion, "diagnosticCode": event.Reason,
			"targetId": event.TargetID,
		})
		if err != nil {
			return "", fmt.Errorf("encode connection rotation audit metadata: %w", err)
		}
		return addBindingMetadata(string(legacy), event.BindingID.String(), nil)
	}
}

// EncodeConnectionAdministrationAuditMetadata applies the generated payload
// contract selected by the domain-owned administration action.
func EncodeConnectionAdministrationAuditMetadata(event ConnectionAdministrationAuditEvent) (string, error) {
	payload := analyticsgen.GenSchemaTargetConnectionAdministrationAuditPayload{
		TargetId:          event.TargetID.String(),
		LogicalConnection: event.ConnectionID.String(),
		Revision:          event.Revision,
	}
	switch event.Action {
	case connectionbinding.AuditBindingCreated:
		encoded, err := analyticsgen.EncodeGenCreateTargetConnectionBindingAuditPayload(payload)
		return addBindingMetadata(encoded, event.BindingID.String(), err)
	case connectionbinding.AuditBindingUpdated:
		encoded, err := analyticsgen.EncodeGenUpdateTargetConnectionBindingAuditPayload(payload)
		return addBindingMetadata(encoded, event.BindingID.String(), err)
	case connectionbinding.AuditBindingEnabled:
		encoded, err := analyticsgen.EncodeGenEnableTargetConnectionBindingAuditPayload(payload)
		return addBindingMetadata(encoded, event.BindingID.String(), err)
	case connectionbinding.AuditBindingDisabled:
		encoded, err := analyticsgen.EncodeGenDisableTargetConnectionBindingAuditPayload(payload)
		return addBindingMetadata(encoded, event.BindingID.String(), err)
	default:
		return "", fmt.Errorf("connection administration action %q has no generated audit payload encoder", event.Action)
	}
}

// addBindingMetadata retains the concrete binding identity for operators while
// keeping the graph connection as the durable audit resource. The generated
// payload encoder remains the source of truth for required fields; this
// bounded internal field is appended only after that validation succeeds.
func addBindingMetadata(encoded, bindingID string, encodeErr error) (string, error) {
	if encodeErr != nil {
		return "", encodeErr
	}
	if bindingID == "" {
		return encoded, nil
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(encoded), &envelope); err != nil {
		return "", fmt.Errorf("decode connection audit metadata: %w", err)
	}
	if payload, ok := envelope["payload"].(map[string]any); ok {
		payload["bindingId"] = bindingID
	} else {
		envelope["bindingId"] = bindingID
	}
	updated, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("encode connection audit metadata: %w", err)
	}
	return string(updated), nil
}
