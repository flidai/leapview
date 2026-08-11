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
		TargetId:        event.TargetID,
	}
	switch event.Operation {
	case connectionbinding.RefreshRequested:
		return analyticsgen.EncodeGenRefreshTargetConnectionBindingAuditPayload(payload)
	case connectionbinding.RefreshTest:
		return analyticsgen.EncodeGenTestTargetConnectionBindingAuditPayload(payload)
	default:
		legacy, err := json.Marshal(map[string]any{
			"operation": event.Operation, "outcome": event.Outcome,
			"providerVersion": event.ProviderVersion, "diagnosticCode": event.Reason,
			"targetId": event.TargetID,
		})
		if err != nil {
			return "", fmt.Errorf("encode connection rotation audit metadata: %w", err)
		}
		return string(legacy), nil
	}
}

// EncodeConnectionAdministrationAuditMetadata applies the generated payload
// contract selected by the domain-owned administration action.
func EncodeConnectionAdministrationAuditMetadata(event ConnectionAdministrationAuditEvent) (string, error) {
	payload := analyticsgen.GenSchemaTargetConnectionAdministrationAuditPayload{
		TargetId:          event.TargetID,
		LogicalConnection: string(event.LogicalConnectionID),
		Revision:          event.Revision,
	}
	switch event.Action {
	case connectionbinding.AuditBindingCreated:
		return analyticsgen.EncodeGenCreateTargetConnectionBindingAuditPayload(payload)
	case connectionbinding.AuditBindingUpdated:
		return analyticsgen.EncodeGenUpdateTargetConnectionBindingAuditPayload(payload)
	case connectionbinding.AuditBindingEnabled:
		return analyticsgen.EncodeGenEnableTargetConnectionBindingAuditPayload(payload)
	case connectionbinding.AuditBindingDisabled:
		return analyticsgen.EncodeGenDisableTargetConnectionBindingAuditPayload(payload)
	default:
		return "", fmt.Errorf("connection administration action %q has no generated audit payload encoder", event.Action)
	}
}
