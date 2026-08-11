package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/stretchr/testify/require"
)

func TestConnectionRotationAuditAdapterPersistsOnlyRedactedBoundedMetadata(t *testing.T) {
	var input accessmodule.AuditEventInput
	recorder := connectionRotationAuditRecorder{
		record: func(_ context.Context, current accessmodule.AuditEventInput) error {
			input = current
			return nil
		},
	}
	err := recorder.RecordCredentialRotation(context.Background(), analyticsmodule.ConnectionRotationAuditEvent{
		BindingID: "binding_prod_warehouse", TargetID: "lvinst_prod", WorkspaceID: "sales",
		ProviderVersion: "secret:v2", Actor: "principal:operator-1",
		Operation: "credential.test.requested", Outcome: "degraded",
		Reason: "POOL_HEALTH_CHECK_FAILED", Timestamp: time.Now(),
	})
	require.NoError(t, err)
	if input.WorkspaceID != "sales" || input.PrincipalID != "operator-1" ||
		input.Action != "credential.test.requested" ||
		input.TargetType != "connection_binding" || input.TargetID != "binding_prod_warehouse" ||
		input.Privilege != accessmodule.PrivilegeTestConnection || input.Status != "degraded" {
		t.Fatalf("audit input = %#v", input)
	}
	var envelope struct {
		SchemaVersion int            `json:"schemaVersion"`
		Retention     string         `json:"retention"`
		PayloadSchema string         `json:"payloadSchema"`
		Payload       map[string]any `json:"payload"`
	}
	if err := json.Unmarshal([]byte(input.MetadataJSON), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.SchemaVersion != 1 || envelope.Retention != "security" || envelope.PayloadSchema != "TargetConnectionRotationAuditPayload" ||
		envelope.Payload["diagnosticCode"] != "POOL_HEALTH_CHECK_FAILED" ||
		envelope.Payload["providerVersion"] != "secret:v2" ||
		envelope.Payload["targetId"] != "lvinst_prod" {
		t.Fatalf("audit metadata = %#v", envelope)
	}
	for _, forbidden := range []string{"source-secret", "connection_string", "password", "/leapview/sales"} {
		if strings.Contains(input.MetadataJSON, forbidden) {
			t.Fatalf("audit metadata disclosed %q: %s", forbidden, input.MetadataJSON)
		}
	}
	logMetadata, err := analyticsgen.EncodeGenTestTargetConnectionBindingAuditPayloadForLog(analyticsgen.GenSchemaTargetConnectionRotationAuditPayload{
		Operation: "credential.test.requested", Outcome: "degraded", ProviderVersion: "secret:v2",
		DiagnosticCode: "POOL_HEALTH_CHECK_FAILED", TargetId: "lvinst_prod",
	})
	require.NoError(t, err)
	if strings.Contains(logMetadata, "secret:v2") || strings.Contains(logMetadata, "lvinst_prod") ||
		strings.Contains(logMetadata, "POOL_HEALTH_CHECK_FAILED") {
		t.Fatalf("log metadata leaked internal values: %s", logMetadata)
	}
	if !strings.Contains(logMetadata, `"operation":"credential.test.requested"`) || !strings.Contains(logMetadata, `"outcome":"degraded"`) {
		t.Fatalf("log metadata omitted public values: %s", logMetadata)
	}
}

func TestConnectionAdministrationAuditAdapterPersistsOnlyBindingIdentity(t *testing.T) {
	var input accessmodule.AuditEventInput
	recorder := connectionAdministrationAuditRecorder{
		record: func(_ context.Context, current accessmodule.AuditEventInput) error {
			input = current
			return nil
		},
	}
	err := recorder.RecordConnectionAdministration(context.Background(), analyticsmodule.ConnectionAdministrationAuditEvent{
		WorkspaceID: "sales", BindingID: "binding_prod_warehouse", TargetID: "lvinst_prod",
		LogicalConnectionID: "warehouse", Actor: "operator-1",
		Action: "connection.binding.updated", Outcome: "succeeded", Revision: 7,
		Timestamp: time.Now(),
	})
	require.NoError(t, err)
	if input.WorkspaceID != "sales" || input.PrincipalID != "operator-1" ||
		input.Action != "connection.binding.updated" ||
		input.TargetType != "connection_binding" || input.TargetID != "binding_prod_warehouse" ||
		input.Privilege != accessmodule.PrivilegeManageConnectionMetadata || input.Status != "succeeded" {
		t.Fatalf("audit input = %#v", input)
	}
	var envelope struct {
		SchemaVersion int            `json:"schemaVersion"`
		Retention     string         `json:"retention"`
		PayloadSchema string         `json:"payloadSchema"`
		Payload       map[string]any `json:"payload"`
	}
	if err := json.Unmarshal([]byte(input.MetadataJSON), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.SchemaVersion != 1 || envelope.Retention != "security" || envelope.PayloadSchema != "TargetConnectionAdministrationAuditPayload" ||
		envelope.Payload["targetId"] != "lvinst_prod" || envelope.Payload["logicalConnection"] != "warehouse" || envelope.Payload["revision"] != float64(7) {
		t.Fatalf("audit metadata = %#v", envelope)
	}
	for _, forbidden := range []string{"source-secret", "connection_string", "password", "secretPath"} {
		if strings.Contains(input.MetadataJSON, forbidden) {
			t.Fatalf("audit metadata disclosed %q: %s", forbidden, input.MetadataJSON)
		}
	}
}
