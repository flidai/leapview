package module

import (
	"fmt"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

func loadRefreshExecutionContract() (apigencommand.AsyncExecutionContract, error) {
	contract, ok := refreshgen.GetAPIGenCommandRuntimeContract(string(refreshgen.GenOperationCreateRefreshRun))
	if !ok {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("create refresh run command contract is unavailable")
	}
	if err := contract.Validate(); err != nil {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("validate create refresh run command contract: %w", err)
	}
	if contract.Execution == nil {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("create refresh run async execution contract is unavailable")
	}
	execution := *contract.Execution
	if execution.Guarantee != "transactional" {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("create refresh run requires transactional execution, got %q", execution.Guarantee)
	}
	if execution.JobKind != refreshrun.JobKindRefreshPipeline {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("refresh execution job kind %q does not match dispatcher handler %q", execution.JobKind, refreshrun.JobKindRefreshPipeline)
	}
	if execution.ResourceKind != "refresh" || execution.InitialState != refreshrun.RunStatusQueued {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("refresh execution has incompatible initial lifecycle %q/%q", execution.ResourceKind, execution.InitialState)
	}
	if execution.StatusOperation != string(refreshgen.GenOperationGetRefreshRun) || execution.EventsOperation != string(refreshgen.GenOperationListRefreshRunEvents) {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("refresh execution has incompatible lifecycle operations")
	}
	if execution.Cancellation != "supported" {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("refresh execution cancellation policy %q is not implemented", execution.Cancellation)
	}
	return execution, nil
}
