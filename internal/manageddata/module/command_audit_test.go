package module

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	manageddatagen "github.com/flidai/leapview/internal/manageddata/api/gen"
	manageddatahttp "github.com/flidai/leapview/internal/manageddata/http"
)

func TestManagedDataCommandAuditsDerivePolicyFromGeneratedContracts(t *testing.T) {
	var events []CommandAuditEvent
	recorder, err := buildManagedDataCommandAuditRecorder(func(_ context.Context, input CommandAuditEvent) error {
		events = append(events, input)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, operationID := range managedDataCommandOperationIDs {
		if err := recorder(t.Context(), manageddatahttp.CommandAuditInput{
			OperationID: operationID, PrincipalID: "principal-a", ProjectID: "project-a", ConnectionID: "orders",
			TargetType: "managed_data_resource", TargetID: "resource-a",
			RequestID: "request-a", CorrelationID: "correlation-a", Surface: "cli",
		}); err != nil {
			t.Fatalf("record %s: %v", operationID, err)
		}
	}
	if len(events) != len(managedDataCommandOperationIDs) {
		t.Fatalf("events = %d, want %d", len(events), len(managedDataCommandOperationIDs))
	}
	for index, event := range events {
		operationID := managedDataCommandOperationIDs[index]
		generated, _ := manageddatagen.GetAPIGenOperationContract(operationID)
		if event.Action != generated.Command.Audit.SuccessAction || event.Privilege != generated.Command.Privilege ||
			event.PrincipalID != "principal-a" || event.TargetType != "managed_data_resource" || event.TargetID != "resource-a" ||
			event.Status != "success" || event.RequestID != "request-a" || event.CorrelationID != "correlation-a" {
			t.Errorf("%s event = %#v", operationID, event)
		}
		var metadata map[string]string
		if err := json.Unmarshal([]byte(event.MetadataJSON), &metadata); err != nil ||
			metadata["operationId"] != operationID || metadata["owner"] != generated.Command.Owner ||
			metadata["projectId"] != "project-a" || metadata["connectionId"] != "orders" || metadata["surface"] != "cli" {
			t.Errorf("%s metadata = %#v, err = %v", operationID, metadata, err)
		}
	}
}

func TestManagedDataCommandAuditRejectsUnknownOperation(t *testing.T) {
	recorder, err := buildManagedDataCommandAuditRecorder(discardManagedDataAudit)
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder(t.Context(), manageddatahttp.CommandAuditInput{OperationID: "unknown"}); err == nil {
		t.Fatal("unknown operation was audited")
	}
}

func TestManagedDataCommandAuditRejectsMissingSink(t *testing.T) {
	if recorder, err := buildManagedDataCommandAuditRecorder(nil); !errors.Is(err, errManagedDataCommandAuditUnavailable) || recorder != nil {
		t.Fatalf("recorder nil = %t, err = %v", recorder == nil, err)
	}
}

func discardManagedDataAudit(context.Context, CommandAuditEvent) error { return nil }
