package module

import (
	"context"
	"encoding/json"
	"fmt"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/release"
	releasegen "github.com/flidai/leapview/internal/release/api/gen"
)

type FinalizeJob struct {
	Project string
	Release string
}

func (m *Module) JobHandlers() []jobs.Handler {
	return []jobs.Handler{jobs.HandlerFunc{JobKind: m.finalizeExecution.JobKind, Run: func(ctx context.Context, job jobs.Job) error {
		var payload FinalizeJob
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return err
		}
		_, err := m.service.ValidateFinalization(ctx, payload.Project, payload.Release)
		return err
	}}}
}

func loadFinalizeExecutionContract() (apigencommand.AsyncExecutionContract, error) {
	contract, ok := releasegen.GetAPIGenCommandRuntimeContract(string(releasegen.GenOperationFinalizeRelease))
	if !ok {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("finalize release command contract is unavailable")
	}
	if err := contract.Validate(); err != nil {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("validate finalize release command contract: %w", err)
	}
	if contract.Guarantee != apigencommand.GuaranteeTransactional {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("finalize release requires transactional auditing, got %q", contract.Guarantee)
	}
	if contract.Execution == nil {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("finalize release async execution contract is unavailable")
	}
	execution := *contract.Execution
	if execution.InitialState != string(release.StatusValidating) {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("finalize release initial state %q does not match domain state %q", execution.InitialState, release.StatusValidating)
	}
	if execution.StatusOperation != string(releasegen.GenOperationGetRelease) {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("finalize release status operation %q must be %q", execution.StatusOperation, releasegen.GenOperationGetRelease)
	}
	if execution.EventsOperation != string(releasegen.GenOperationListReleaseEvents) {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("finalize release events operation %q must be %q", execution.EventsOperation, releasegen.GenOperationListReleaseEvents)
	}
	if execution.Cancellation != "unsupported" {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("finalize release cancellation policy %q is not implemented", execution.Cancellation)
	}
	return execution, nil
}

func validateFinalizeJobHandlers(execution apigencommand.AsyncExecutionContract, handlers []jobs.Handler) error {
	if len(handlers) != 1 {
		return fmt.Errorf("finalize release execution requires exactly one job handler, got %d", len(handlers))
	}
	if kind := handlers[0].Kind(); kind != execution.JobKind {
		return fmt.Errorf("finalize release job handler kind %q does not match generated kind %q", kind, execution.JobKind)
	}
	return nil
}
