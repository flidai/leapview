package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/stretchr/testify/require"
)

func TestConnectionRotationAuditAdapterPersistsOnlyRedactedBoundedMetadata(t *testing.T) {
	var input access.AuditEventInput
	recorder := connectionRotationAuditRecorder{
		record: func(_ context.Context, current access.AuditEventInput) error {
			input = current
			return nil
		},
	}
	err := recorder.RecordCredentialRotation(context.Background(), analyticsmodule.ConnectionRotationAuditEvent{
		BindingID: "binding_prod_warehouse", TargetID: "lvinst_prod", ConnectionID: "connection_sales", ProjectID: "project:sales",
		ProviderVersion: "secret:v2", Actor: "principal:operator-1",
		Operation: "credential.test.requested", Outcome: "degraded",
		Reason: "POOL_HEALTH_CHECK_FAILED", Timestamp: time.Now(),
	})
	require.NoError(t, err)
	if input.ResourceID != "connection_sales" || input.PrincipalID != "operator-1" ||
		input.Action != "credential.test.requested" ||
		input.ResourceKind != "connection" ||
		input.Capability != access.CapabilityResourceUse || input.Status != "degraded" {
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
	var input access.AuditEventInput
	recorder := connectionAdministrationAuditRecorder{
		record: func(_ context.Context, current access.AuditEventInput) error {
			input = current
			return nil
		},
	}
	err := recorder.RecordConnectionAdministration(context.Background(), analyticsmodule.ConnectionAdministrationAuditEvent{
		ProjectID: "project:sales", BindingID: "binding_prod_warehouse", TargetID: "lvinst_prod", ConnectionID: "connection_sales",
		Actor:  "operator-1",
		Action: "connection.binding.updated", Outcome: "succeeded", Revision: 7,
		Timestamp: time.Now(),
	})
	require.NoError(t, err)
	if input.ResourceID != "connection_sales" || input.PrincipalID != "operator-1" ||
		input.Action != "connection.binding.updated" ||
		input.ResourceKind != "connection" ||
		input.Capability != access.CapabilityResourceManage || input.Status != "succeeded" {
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
		envelope.Payload["targetId"] != "lvinst_prod" || envelope.Payload["logicalConnection"] != "connection_sales" || envelope.Payload["revision"] != float64(7) {
		t.Fatalf("audit metadata = %#v", envelope)
	}
	for _, forbidden := range []string{"source-secret", "connection_string", "password", "secretPath"} {
		if strings.Contains(input.MetadataJSON, forbidden) {
			t.Fatalf("audit metadata disclosed %q: %s", forbidden, input.MetadataJSON)
		}
	}
}
