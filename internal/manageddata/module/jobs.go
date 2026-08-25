package module

import (
	"context"
	"encoding/json"
	"fmt"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	manageddataapi "github.com/flidai/leapview/internal/manageddata/api"
	manageddatagen "github.com/flidai/leapview/internal/manageddata/api/gen"
	"github.com/flidai/leapview/internal/manageddata/control"
	"github.com/flidai/leapview/pkg/jobs"
)

type FinalizeUploadJob struct {
	Project       string
	Connection    string
	UploadSession string
}

func (m *Module) JobHandlers(events jobs.EventAppender) []jobs.Handler {
	return []jobs.Handler{jobs.HandlerFunc{JobKind: m.finalizeExecution.JobKind, Run: func(ctx context.Context, job jobs.Job) error {
		var payload FinalizeUploadJob
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return err
		}
		result, err := m.finalizer.CompleteFinalizeUpload(ctx, control.UploadRequest{Project: payload.Project, Connection: payload.Connection, UploadID: payload.UploadSession})
		event := "upload_session.completed"
		if err != nil {
			event = "upload_session.failed"
		}
		data, _ := json.Marshal(map[string]any{"uploadSessionId": payload.UploadSession, "status": result.Upload.Status})
		_, _ = events.AppendEvent(context.WithoutCancel(ctx), m.finalizeExecution.ResourceKind, payload.UploadSession, event, data)
		return err
	}}}
}

func loadFinalizeUploadExecutionContract() (apigencommand.AsyncExecutionContract, error) {
	contract, ok := manageddatagen.GetAPIGenCommandRuntimeContract(string(manageddatagen.GenOperationFinalizeManagedDataUploadSession))
	if !ok {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("finalize managed-data upload command contract is unavailable")
	}
	if err := contract.Validate(); err != nil {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("validate finalize managed-data upload command contract: %w", err)
	}
	if contract.Guarantee != apigencommand.GuaranteeTransactional {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("finalize managed-data upload requires transactional Access auditing, got %q", contract.Guarantee)
	}
	if contract.Execution == nil {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("finalize managed-data upload async execution contract is unavailable")
	}
	execution := *contract.Execution
	if execution.Guarantee != string(apigencommand.GuaranteeTransactional) {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("finalize managed-data upload requires transactional lifecycle persistence, got %q", execution.Guarantee)
	}
	if execution.InitialState != string(manageddataapi.ManagedDataUploadSessionStatusFinalizing) {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("finalize managed-data upload initial state %q does not match public state %q", execution.InitialState, manageddataapi.ManagedDataUploadSessionStatusFinalizing)
	}
	if execution.StatusOperation != string(manageddatagen.GenOperationGetManagedDataUploadSession) {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("finalize managed-data upload status operation %q must be %q", execution.StatusOperation, manageddatagen.GenOperationGetManagedDataUploadSession)
	}
	if execution.EventsOperation != string(manageddatagen.GenOperationListManagedDataUploadSessionEvents) {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("finalize managed-data upload events operation %q must be %q", execution.EventsOperation, manageddatagen.GenOperationListManagedDataUploadSessionEvents)
	}
	if execution.Cancellation != "unsupported" {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("finalize managed-data upload cancellation policy %q is not implemented", execution.Cancellation)
	}
	return execution, nil
}

func validateFinalizeUploadJobHandlers(execution apigencommand.AsyncExecutionContract, handlers []jobs.Handler) error {
	if len(handlers) != 1 {
		return fmt.Errorf("finalize managed-data upload execution requires exactly one job handler, got %d", len(handlers))
	}
	if kind := handlers[0].Kind(); kind != execution.JobKind {
		return fmt.Errorf("finalize managed-data upload job handler kind %q does not match generated kind %q", kind, execution.JobKind)
	}
	return nil
}
