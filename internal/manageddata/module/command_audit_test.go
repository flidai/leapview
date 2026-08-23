package module

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flidai/leapview/internal/access"
	manageddatagen "github.com/flidai/leapview/internal/manageddata/api/gen"
	manageddatahttp "github.com/flidai/leapview/internal/manageddata/http"
)

func TestManagedDataAuditIntentsDerivePolicyFromGeneratedContracts(t *testing.T) {
	builder, err := buildManagedDataAuditIntentBuilder()
	if err != nil {
		t.Fatal(err)
	}

	for _, operationID := range managedDataCommandOperationIDs {
		intent, err := builder(t.Context(), manageddatahttp.CommandAuditInput{
			OperationID: operationID, PrincipalID: "principal-a", ProjectID: "project-a", ConnectionID: "orders",
			TargetType: "managed_data_resource", TargetID: "resource-a",
			RequestID: "request-a", CorrelationID: "correlation-a", Surface: "cli",
		})
		if err != nil {
			t.Fatalf("build %s: %v", operationID, err)
		}
		generated, _ := manageddatagen.GetAPIGenOperationContract(operationID)
		if intent.EventID == "" || intent.Source != generated.Command.Owner || intent.Operation != operationID ||
			intent.PrincipalID != "principal-a" || intent.Action != generated.Command.Audit.SuccessAction ||
			intent.ResourceKind != "managed_data_resource" || intent.ResourceID != "resource-a" ||
			intent.Capability != access.CapabilityResourceEdit || intent.RequestID != "request-a" ||
			intent.CorrelationID != "correlation-a" || intent.AggregateKey != "managed_data_resource:resource-a" ||
			intent.AggregateSequence != managedDataAuditSequence(operationID) {
			t.Errorf("%s intent = %#v", operationID, intent)
		}
		var envelope struct {
			SchemaVersion int               `json:"schemaVersion"`
			Retention     string            `json:"retention"`
			PayloadSchema string            `json:"payloadSchema"`
			Payload       map[string]string `json:"payload"`
		}
		if err := json.Unmarshal([]byte(intent.MetadataJSON), &envelope); err != nil ||
			envelope.SchemaVersion != 1 || envelope.Retention != "security" || envelope.PayloadSchema == "" ||
			envelope.Payload["operationId"] != operationID || envelope.Payload["owner"] != generated.Command.Owner ||
			envelope.Payload["projectId"] != "project-a" || envelope.Payload["connectionId"] != "orders" ||
			envelope.Payload["surface"] != "cli" {
			t.Errorf("%s metadata = %#v, err = %v", operationID, envelope, err)
		}
	}
}

func TestManagedDataAuditIntentBuilderRejectsUnknownOperation(t *testing.T) {
	builder, err := buildManagedDataAuditIntentBuilder()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := builder(context.Background(), manageddatahttp.CommandAuditInput{OperationID: "unknown"}); err == nil {
		t.Fatal("unknown operation was accepted")
	}
}
