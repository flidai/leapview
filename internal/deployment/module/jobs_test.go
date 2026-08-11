package module

import (
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/deployment"
)

func TestActivationWorkflowDistinguishesImmediateAndApprovalGatedStarts(t *testing.T) {
	execution := apigencommand.AsyncExecutionContract{
		JobKind: "deployment.activate", ResourceKind: "deployment",
		InitialEvent: "deployment.queued", InitialState: "queued",
	}
	actor := deployment.ApprovalActor{PrincipalID: "publisher"}
	immediate := activationWorkflow(execution, true, "project", "deployment-1", "release-1", actor, deployment.Approval{}, "key")
	gated := activationWorkflow(execution, false, "project", "deployment-2", "release-1", actor, deployment.Approval{}, "key")

	if immediate.Event.EventType != execution.InitialEvent || immediate.Event.ResourceKind != execution.ResourceKind || immediate.Job.Kind != execution.JobKind {
		t.Fatalf("immediate workflow = %#v", immediate)
	}
	if gated.Event.EventType != execution.InitialEvent || gated.Event.ResourceKind != execution.ResourceKind || gated.Job.ID != "" {
		t.Fatalf("approval-gated workflow = %#v", gated)
	}
}
