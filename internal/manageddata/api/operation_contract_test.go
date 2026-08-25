package api_test

import (
	"testing"

	managedgen "github.com/flidai/leapview/internal/manageddata/api/gen"
)

func TestGeneratedManagedDataOperationClassifications(t *testing.T) {
	contracts := managedgen.GetAPIGenOperationContracts()
	commands := map[string]struct {
		auditAction string
		guarantee   string
	}{
		"createManagedDataUploadSession":       {"managed_data.upload_session.created", "transactional"},
		"cancelManagedDataUploadSession":       {"managed_data.upload_session.cancelled", "transactional"},
		"finalizeManagedDataUploadSession":     {"managed_data.upload_session.finalization_requested", "transactional"},
		"createManagedDataS3MultipartUpload":   {"managed_data.s3_multipart_upload.created", "transactional"},
		"completeManagedDataS3MultipartUpload": {"managed_data.s3_multipart_upload.completion_requested", "transactional"},
		"abortManagedDataS3MultipartUpload":    {"managed_data.s3_multipart_upload.abort_requested", "transactional"},
	}
	for operationID, expected := range commands {
		contract, ok := contracts[operationID]
		if !ok || contract.Command == nil {
			t.Fatalf("%s command contract = %#v", operationID, contract.Command)
		}
		command := contract.Command
		if contract.Namespace != "LeapViewAPI.ManagedData" || command.Owner != contract.Namespace || command.AuthzMode != "privilege" || command.Privilege != "RESOURCE_EDIT" {
			t.Errorf("%s ownership/authz = %#v", operationID, command)
		}
		if !command.Audit.Required || command.Audit.SuccessAction != expected.auditAction || command.Audit.Guarantee != expected.guarantee {
			t.Errorf("%s audit = %#v", operationID, command.Audit)
		}
		if command.Target == nil || command.Target.Parameter != "connection" || command.Target.Type != "connection" {
			t.Errorf("%s target = %#v", operationID, command.Target)
		}
		if command.Idempotency != "required" || command.Concurrency != "" || len(command.AdditionalExposures) != 0 {
			t.Errorf("%s policies/exposures = %#v", operationID, command)
		}
	}

	finalize := contracts["finalizeManagedDataUploadSession"].Command.Execution
	if finalize == nil ||
		finalize.Mode != "async" ||
		finalize.Guarantee != "transactional" ||
		finalize.JobKind != "upload.finalize" ||
		finalize.ResourceKind != "upload" ||
		finalize.InitialEvent != "upload_session.finalizing" ||
		finalize.InitialState != "finalizing" ||
		finalize.StatusOperation != "getManagedDataUploadSession" ||
		finalize.EventsOperation != "listManagedDataUploadSessionEvents" ||
		finalize.Cancellation != "unsupported" {
		t.Fatalf("finalize managed-data upload execution contract = %#v", finalize)
	}

	for _, operationID := range []string{
		"listManagedConnections", "getManagedConnection", "getActiveManagedDataRevision",
		"listManagedDataRevisions", "getManagedDataRevision", "listManagedDataUploadSessions",
		"getManagedDataUploadSession", "listManagedDataUploadSessionEvents", "signManagedDataS3MultipartPart",
	} {
		if contract := contracts[operationID]; contract.Command != nil {
			t.Errorf("query %s has command contract %#v", operationID, contract.Command)
		}
	}
}
