package api_test

import (
	"testing"

	managedgen "github.com/flidai/leapview/internal/manageddata/api/gen"
)

func TestGeneratedManagedDataOperationClassifications(t *testing.T) {
	contracts := managedgen.GetAPIGenOperationContracts()
	commands := map[string]string{
		"createManagedDataUploadSession":       "managed_data.upload_session.created",
		"cancelManagedDataUploadSession":       "managed_data.upload_session.cancelled",
		"finalizeManagedDataUploadSession":     "managed_data.upload_session.finalization_requested",
		"createManagedDataS3MultipartUpload":   "managed_data.s3_multipart_upload.created",
		"completeManagedDataS3MultipartUpload": "managed_data.s3_multipart_upload.completed",
		"abortManagedDataS3MultipartUpload":    "managed_data.s3_multipart_upload.aborted",
	}
	for operationID, auditAction := range commands {
		contract, ok := contracts[operationID]
		if !ok || contract.Command == nil {
			t.Fatalf("%s command contract = %#v", operationID, contract.Command)
		}
		command := contract.Command
		if contract.Namespace != "LeapViewAPI.ManagedData" || command.Owner != contract.Namespace || command.AuthzMode != "privilege" || command.Privilege != "INGEST_DATA" {
			t.Errorf("%s ownership/authz = %#v", operationID, command)
		}
		if !command.Audit.Required || command.Audit.SuccessAction != auditAction || command.Audit.Guarantee != "best-effort" {
			t.Errorf("%s audit = %#v", operationID, command.Audit)
		}
		if command.Target == nil || command.Target.Parameter != "project" || command.Target.Type != "project" {
			t.Errorf("%s target = %#v", operationID, command.Target)
		}
		if command.Idempotency != "required" || command.Concurrency != "" || len(command.AdditionalExposures) != 0 {
			t.Errorf("%s policies/exposures = %#v", operationID, command)
		}
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
