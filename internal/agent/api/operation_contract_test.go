package api_test

import (
	"testing"

	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
)

func TestGeneratedAgentOperationClassifications(t *testing.T) {
	contracts := agentgen.GetAPIGenOperationContracts()
	commands := map[string]struct {
		audit       string
		target      string
		idempotency string
		concurrency string
		guarantee   string
		ui          bool
	}{
		"createAgentConversation":  {audit: "agent.conversation.created", idempotency: "required", guarantee: "best-effort", ui: true},
		"archiveAgentConversation": {audit: "agent.conversation.archived", target: "conversation", guarantee: "best-effort"},
		"updateAgentConversation":  {audit: "agent.conversation.updated", target: "conversation", concurrency: "if-match", guarantee: "best-effort"},
		"createAgentRun":           {audit: "agent.run.created", target: "conversation", idempotency: "required", guarantee: "best-effort", ui: true},
		"cancelAgentRun":           {audit: "agent.run.cancelled", target: "conversation", idempotency: "required", guarantee: "best-effort"},
	}
	for operationID, want := range commands {
		contract, ok := contracts[operationID]
		if !ok || contract.Command == nil {
			t.Fatalf("%s command contract = %#v", operationID, contract.Command)
		}
		command := contract.Command
		if contract.Namespace != "LeapViewAPI.Agent" || command.Owner != contract.Namespace || command.AuthzMode != "privilege" || command.Privilege != "USE_AGENT" {
			t.Errorf("%s ownership/authz = %#v", operationID, command)
		}
		if !command.Audit.Required || command.Audit.SuccessAction != want.audit || command.Audit.Guarantee != want.guarantee {
			t.Errorf("%s audit = %#v", operationID, command.Audit)
		}
		if command.Idempotency != want.idempotency || command.Concurrency != want.concurrency {
			t.Errorf("%s transport policy = %#v", operationID, command)
		}
		if want.target == "" {
			if command.Target != nil {
				t.Errorf("%s target = %#v, want none", operationID, command.Target)
			}
		} else if command.Target == nil || command.Target.Parameter != want.target || command.Target.Type != want.target {
			t.Errorf("%s target = %#v, want %q", operationID, command.Target, want.target)
		}
		if got := len(command.AdditionalExposures) == 1 && command.AdditionalExposures[0] == agentgen.GenOperationSurfaceUI; got != want.ui {
			t.Errorf("%s UI exposure = %v, want %t", operationID, command.AdditionalExposures, want.ui)
		}
		if operationID == "createAgentRun" {
			if command.Execution == nil {
				t.Fatalf("%s execution contract is missing", operationID)
			}
			execution := command.Execution
			if execution.Mode != "async" || execution.Guarantee != "transactional" || execution.JobKind != "agent.run" || execution.ResourceKind != "agent_run" || execution.InitialEvent != "agent_run.queued" || execution.InitialState != "running" || execution.StatusOperation != "getAgentRun" || execution.EventsOperation != "listAgentEvents" || execution.Cancellation != "supported" {
				t.Errorf("%s execution = %#v", operationID, execution)
			}
		} else if command.Execution != nil {
			t.Errorf("synchronous command %s has execution contract %#v", operationID, command.Execution)
		}
	}

	for _, operationID := range []string{
		"listAgentConversations", "getAgentConversation", "listAgentMessages",
		"listAgentRuns", "getAgentRun", "listAgentEvents",
	} {
		if contract := contracts[operationID]; contract.Command != nil {
			t.Errorf("query %s has command contract %#v", operationID, contract.Command)
		}
	}
}
