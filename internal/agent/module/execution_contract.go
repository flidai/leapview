package module

import (
	"fmt"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/agent"
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	"github.com/flidai/leapview/internal/platform/jobs"
)

func loadRunExecutionContract() (apigencommand.AsyncExecutionContract, error) {
	contract, ok := agentgen.GetAPIGenCommandRuntimeContract(string(agentgen.GenOperationCreateAgentRun))
	if !ok {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("create agent run command contract is unavailable")
	}
	if err := contract.Validate(); err != nil {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("validate create agent run command contract: %w", err)
	}
	if contract.Guarantee != apigencommand.GuaranteeBestEffort {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("create agent run requires best-effort Access auditing, got %q", contract.Guarantee)
	}
	if contract.Execution == nil {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("create agent run async execution contract is unavailable")
	}
	execution := *contract.Execution
	if execution.Guarantee != "transactional" {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("create agent run execution guarantee %q does not preserve the run, event, and job atomically", execution.Guarantee)
	}
	if execution.JobKind != "agent.run" {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("create agent run job kind %q is not supported by the durable agent repository", execution.JobKind)
	}
	if execution.ResourceKind != "agent_run" {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("create agent run resource kind %q is not supported by the durable agent repository", execution.ResourceKind)
	}
	if execution.InitialState != agent.RunStatusRunning {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("create agent run initial state %q does not match domain state %q", execution.InitialState, agent.RunStatusRunning)
	}
	if execution.StatusOperation != string(agentgen.GenOperationGetAgentRun) {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("create agent run status operation %q must be %q", execution.StatusOperation, agentgen.GenOperationGetAgentRun)
	}
	if execution.EventsOperation != string(agentgen.GenOperationListAgentEvents) {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("create agent run events operation %q must be %q", execution.EventsOperation, agentgen.GenOperationListAgentEvents)
	}
	if execution.Cancellation != "supported" {
		return apigencommand.AsyncExecutionContract{}, fmt.Errorf("create agent run cancellation policy %q does not match the implemented cancel command", execution.Cancellation)
	}
	return execution, nil
}

func validateRunJobHandlers(execution apigencommand.AsyncExecutionContract, handlers []jobs.Handler) error {
	if len(handlers) != 1 {
		return fmt.Errorf("create agent run execution requires exactly one job handler, got %d", len(handlers))
	}
	if kind := handlers[0].Kind(); kind != execution.JobKind {
		return fmt.Errorf("agent run job handler kind %q does not match generated kind %q", kind, execution.JobKind)
	}
	return nil
}
