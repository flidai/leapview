package module

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	manageddatagen "github.com/flidai/leapview/internal/manageddata/api/gen"
	manageddatahttp "github.com/flidai/leapview/internal/manageddata/http"
)

var managedDataCommandOperationIDs = []string{
	manageddatagen.GenOperationCreateManagedDataUploadSession,
	manageddatagen.GenOperationCancelManagedDataUploadSession,
	manageddatagen.GenOperationFinalizeManagedDataUploadSession,
	manageddatagen.GenOperationCreateManagedDataS3MultipartUpload,
	manageddatagen.GenOperationCompleteManagedDataS3MultipartUpload,
	manageddatagen.GenOperationAbortManagedDataS3MultipartUpload,
}

type managedDataCommandAuditContract struct {
	owner     string
	action    string
	privilege string
}

// buildManagedDataAuditIntentBuilder resolves generated command policy into
// an Access-owned durable intent. The returned intent is handed to the
// managed-data source transaction; no post-commit callback is involved.
func buildManagedDataAuditIntentBuilder() (func(context.Context, manageddatahttp.CommandAuditInput) (*access.AuditIntent, error), error) {
	contracts := make(map[string]managedDataCommandAuditContract, len(managedDataCommandOperationIDs))
	for _, operationID := range managedDataCommandOperationIDs {
		generated, ok := manageddatagen.GetAPIGenOperationContract(operationID)
		if !ok || generated.Command == nil {
			return nil, fmt.Errorf("managed-data operation %q is missing its generated command contract", operationID)
		}
		command := generated.Command
		if command.Privilege == "" || command.AuthzMode != "privilege" || generated.AuthzMode != command.AuthzMode ||
			!command.Audit.Required || command.Audit.SuccessAction == "" || command.Target == nil ||
			command.Audit.Guarantee != "transactional" ||
			command.Target.Type != "connection" || command.Target.Parameter != "connection" {
			return nil, fmt.Errorf("managed-data operation %q has an invalid generated command audit contract", operationID)
		}
		contracts[operationID] = managedDataCommandAuditContract{owner: command.Owner, action: command.Audit.SuccessAction, privilege: command.Privilege}
	}
	return func(ctx context.Context, input manageddatahttp.CommandAuditInput) (*access.AuditIntent, error) {
		contract, ok := contracts[input.OperationID]
		if !ok {
			return nil, fmt.Errorf("managed-data operation %q has no command audit contract", input.OperationID)
		}
		metadata, err := encodeManagedDataAuditMetadata(input.OperationID, contract.owner, input.ProjectID, input.ConnectionID, input.Surface)
		if err != nil {
			return nil, err
		}
		capability, err := access.ParseCapability(contract.privilege)
		if err != nil {
			return nil, fmt.Errorf("managed-data operation %q privilege: %w", input.OperationID, err)
		}
		hash := sha256.Sum256([]byte(input.OperationID + "\x00" + input.TargetType + "\x00" + input.TargetID))
		return &access.AuditIntent{
			EventID: "sha256:" + hex.EncodeToString(hash[:]), Source: contract.owner,
			Operation: input.OperationID, PrincipalID: input.PrincipalID, Action: contract.action,
			ResourceKind: input.TargetType, ResourceID: input.TargetID, Capability: capability,
			Outcome: managedDataAuditOutcome(input.OperationID), RequestID: input.RequestID, CorrelationID: input.CorrelationID,
			AggregateKey:      input.TargetType + ":" + input.TargetID,
			AggregateSequence: managedDataAuditSequence(input.OperationID), MetadataJSON: metadata,
		}, nil
	}, nil
}

func managedDataAuditOutcome(operationID string) string {
	switch operationID {
	case string(manageddatagen.GenOperationCreateManagedDataS3MultipartUpload), string(manageddatagen.GenOperationCompleteManagedDataS3MultipartUpload), string(manageddatagen.GenOperationAbortManagedDataS3MultipartUpload):
		// The source transaction durably accepts the provider transition. The
		// provider call and terminal SQLite transition are recoverable but not
		// part of the same transaction, so these are not claimed as successes.
		return "accepted"
	default:
		return "success"
	}
}

func encodeManagedDataAuditMetadata(operationID, owner, projectID, connectionID, surface string) (string, error) {
	if operationID == string(manageddatagen.GenOperationCreateManagedDataS3MultipartUpload) ||
		operationID == string(manageddatagen.GenOperationCompleteManagedDataS3MultipartUpload) ||
		operationID == string(manageddatagen.GenOperationAbortManagedDataS3MultipartUpload) {
		return encodeManagedDataS3MultipartAuditPayload(operationID, manageddatagen.GenSchemaManagedDataS3MultipartAuditPayload{OperationId: operationID, Owner: owner, ProjectId: projectID, ConnectionId: connectionID, Surface: surface})
	}
	return encodeManagedDataCommandAuditPayload(operationID, manageddatagen.GenSchemaManagedDataCommandAuditPayload{OperationId: operationID, Owner: owner, ProjectId: projectID, ConnectionId: connectionID, Surface: surface})
}

func managedDataAuditSequence(operationID string) int64 {
	switch operationID {
	case string(manageddatagen.GenOperationCreateManagedDataUploadSession), string(manageddatagen.GenOperationCreateManagedDataS3MultipartUpload):
		return 1
	case string(manageddatagen.GenOperationFinalizeManagedDataUploadSession), string(manageddatagen.GenOperationCompleteManagedDataS3MultipartUpload):
		return 2
	case string(manageddatagen.GenOperationCancelManagedDataUploadSession), string(manageddatagen.GenOperationAbortManagedDataS3MultipartUpload):
		return 3
	default:
		return 0
	}
}

func encodeManagedDataCommandAuditPayload(operationID string, payload manageddatagen.GenSchemaManagedDataCommandAuditPayload) (string, error) {
	switch operationID {
	case string(manageddatagen.GenOperationCreateManagedDataUploadSession):
		return manageddatagen.EncodeGenCreateManagedDataUploadSessionAuditPayload(payload)
	case string(manageddatagen.GenOperationCancelManagedDataUploadSession):
		return manageddatagen.EncodeGenCancelManagedDataUploadSessionAuditPayload(payload)
	case string(manageddatagen.GenOperationFinalizeManagedDataUploadSession):
		return manageddatagen.EncodeGenFinalizeManagedDataUploadSessionAuditPayload(payload)
	default:
		return "", fmt.Errorf("generated managed-data command audit payload %q is unavailable", operationID)
	}
}

func encodeManagedDataS3MultipartAuditPayload(operationID string, payload manageddatagen.GenSchemaManagedDataS3MultipartAuditPayload) (string, error) {
	switch operationID {
	case string(manageddatagen.GenOperationCreateManagedDataS3MultipartUpload):
		return manageddatagen.EncodeGenCreateManagedDataS3MultipartUploadAuditPayload(payload)
	case string(manageddatagen.GenOperationCompleteManagedDataS3MultipartUpload):
		return manageddatagen.EncodeGenCompleteManagedDataS3MultipartUploadAuditPayload(payload)
	case string(manageddatagen.GenOperationAbortManagedDataS3MultipartUpload):
		return manageddatagen.EncodeGenAbortManagedDataS3MultipartUploadAuditPayload(payload)
	default:
		return "", fmt.Errorf("generated managed-data multipart audit payload %q is unavailable", operationID)
	}
}
