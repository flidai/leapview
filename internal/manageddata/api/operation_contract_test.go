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
		"createManagedDataUploadSession":       {"managed_data.upload_session.created", "best-effort"},
		"cancelManagedDataUploadSession":       {"managed_data.upload_session.cancelled", "best-effort"},
		"finalizeManagedDataUploadSession":     {"managed_data.upload_session.finalization_requested", "best-effort"},
		"createManagedDataS3MultipartUpload":   {"managed_data.s3_multipart_upload.created", "best-effort"},
		"completeManagedDataS3MultipartUpload": {"managed_data.s3_multipart_upload.completed", "best-effort"},
		"abortManagedDataS3MultipartUpload":    {"managed_data.s3_multipart_upload.aborted", "best-effort"},
	}
	for operationID, expected := range commands {
		contract, ok := contracts[operationID]
		if !ok || contract.Command == nil {
			t.Fatalf("%s command contract = %#v", operationID, contract.Command)
		}
		command := contract.Command
		if contract.Namespace != "LeapViewAPI.ManagedData" || command.Owner != contract.Namespace || command.AuthzMode != "privilege" || command.Privilege != "INGEST_DATA" {
			t.Errorf("%s ownership/authz = %#v", operationID, command)
		}
		if !command.Audit.Required || command.Audit.SuccessAction != expected.auditAction || command.Audit.Guarantee != expected.guarantee {
			t.Errorf("%s audit = %#v", operationID, command.Audit)
		}
		if command.Target == nil || command.Target.Parameter != "project" || command.Target.Type != "project" {
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
