package cli

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func validStageGrantRequest() StageAccessGrantRequest {
	return StageAccessGrantRequest{ProjectID: "project:test", GrantID: "run", PrincipalID: "principal", ResourceID: "pipeline:test", ResourceKind: "pipeline", Actions: []string{"pipeline.run"}, ExpectedRevision: 3, OperationID: "stage-1"}
}

func TestStageGrantRequiresExactDelegableResourceIntent(t *testing.T) {
	request := validStageGrantRequest()
	grant, err := request.Grant()
	if err != nil {
		t.Fatal(err)
	}
	if len(grant.Permissions) != 1 || grant.Permissions[0].Target.IncludeFuture || grant.Permissions[0].Target.ResourceID != "pipeline:test" || grant.Permissions[0].Target.ProjectID != "project:test" || grant.Permissions[0].Action != access.ActionPipelineRun {
		t.Fatalf("not an exact run grant: %+v", grant)
	}
	for name, mutate := range map[string]func(*StageAccessGrantRequest){
		"missing revision":       func(r *StageAccessGrantRequest) { r.ExpectedRevision = 0 },
		"missing operation":      func(r *StageAccessGrantRequest) { r.OperationID = "" },
		"missing actions":        func(r *StageAccessGrantRequest) { r.Actions = nil },
		"wrong kind":             func(r *StageAccessGrantRequest) { r.ResourceKind = "dashboard" },
		"project authority":      func(r *StageAccessGrantRequest) { r.Actions = []string{"project.access.manage"} },
		"nondelegable authority": func(r *StageAccessGrantRequest) { r.Actions = []string{"workload.delegate"} },
		"unknown action":         func(r *StageAccessGrantRequest) { r.Actions = []string{"pipeline.everything"} },
		"wildcard resource":      func(r *StageAccessGrantRequest) { r.ResourceID = "*" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := validStageGrantRequest()
			mutate(&invalid)
			if _, err := invalid.Grant(); err == nil {
				t.Fatal("accepted invalid grant intent")
			}
		})
	}
}

type stageGrantOperations struct {
	fakeOperations
	request StageAccessGrantRequest
}

func (o *stageGrantOperations) StageAccessGrant(_ context.Context, request StageAccessGrantRequest, _ io.Writer) error {
	o.request = request
	return nil
}

func TestStageGrantCommandDefaultsToPreview(t *testing.T) {
	for _, apply := range []bool{false, true} {
		operations := &stageGrantOperations{}
		command := Command(t.Context(), operations)
		command.SetOut(&bytes.Buffer{})
		args := []string{"access", "stage-grant", "--project", "project:test", "--id", "run", "--principal", "principal", "--resource", "pipeline:test", "--kind", "pipeline", "--action", "pipeline.run", "--expected-revision", "3", "--operation-id", "stage-1"}
		if apply {
			args = append(args, "--apply")
		}
		command.SetArgs(args)
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		if operations.request.Apply != apply || operations.request.ExpectedRevision != 3 {
			t.Fatalf("wrong intent: %+v", operations.request)
		}
	}
}
