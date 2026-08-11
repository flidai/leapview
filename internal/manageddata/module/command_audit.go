package module

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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

var errManagedDataCommandAuditUnavailable = errors.New("managed-data command audit is unavailable")

// CommandAuditEvent is the capability-neutral success audit emitted by a
// managed-data command. The application composition root adapts it to the
// configured audit store; policy values come from the generated contract.
type CommandAuditEvent struct {
	PrincipalID   string
	Action        string
	TargetType    string
	TargetID      string
	Privilege     string
	Status        string
	RequestID     string
	CorrelationID string
	MetadataJSON  string
}

func buildManagedDataCommandAuditRecorder(
	record func(context.Context, CommandAuditEvent) error,
) (func(context.Context, manageddatahttp.CommandAuditInput) error, error) {
	contracts := make(map[string]managedDataCommandAuditContract, len(managedDataCommandOperationIDs))
	for _, operationID := range managedDataCommandOperationIDs {
		generated, ok := manageddatagen.GetAPIGenOperationContract(operationID)
		if !ok || generated.Command == nil {
			return nil, fmt.Errorf("managed-data operation %q is missing its generated command contract", operationID)
		}
		command := generated.Command
		if command.Privilege == "" || command.AuthzMode != "privilege" || generated.AuthzMode != command.AuthzMode ||
			!command.Audit.Required || command.Audit.SuccessAction == "" || command.Target == nil ||
			command.Audit.Guarantee != "best-effort" || command.Target.Type != "project" || command.Target.Parameter != "project" {
			return nil, fmt.Errorf("managed-data operation %q has an invalid generated command audit contract", operationID)
		}
		contracts[operationID] = managedDataCommandAuditContract{
			owner: command.Owner, action: command.Audit.SuccessAction, privilege: command.Privilege,
		}
	}
	if record == nil {
		return nil, errManagedDataCommandAuditUnavailable
	}
	return func(ctx context.Context, input manageddatahttp.CommandAuditInput) error {
		contract, ok := contracts[input.OperationID]
		if !ok {
			return fmt.Errorf("managed-data operation %q has no command audit contract", input.OperationID)
		}
		metadata, err := json.Marshal(map[string]string{
			"operationId":  input.OperationID,
			"owner":        contract.owner,
			"projectId":    input.ProjectID,
			"connectionId": input.ConnectionID,
			"surface":      input.Surface,
		})
		if err != nil {
			return err
		}
		// The managed-data state transition and Access audit live in separate
		// repositories. The HTTP adapter observes failures from this configured
		// recorder without changing the already-successful domain result.
		return record(ctx, CommandAuditEvent{
			PrincipalID: input.PrincipalID,
			Action:      contract.action, TargetType: input.TargetType, TargetID: input.TargetID,
			Privilege: contract.privilege, Status: "success",
			RequestID: input.RequestID, CorrelationID: input.CorrelationID,
			MetadataJSON: string(metadata),
		})
	}, nil
}
