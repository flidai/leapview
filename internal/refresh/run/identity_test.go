package run

import (
	"testing"

	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func testPipelinePlan(identity projectgraph.ServingIdentity, pipelineID, semanticModelID string) *deployment.PipelinePlan {
	plan, err := deployment.NewPipelinePlan(deployment.PipelinePlan{
		ID: "pipeline_plan_test", PipelineID: pipelineID, ProjectID: identity.ProjectID.String(), Environment: identity.Environment, SemanticModelID: semanticModelID,
		ServingGenerationID:  identity.GenerationID,
		ArtifactDigest:       "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SelectionDigest:      "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		MaterializationScope: []string{"model_orders"},
	})
	if err != nil {
		panic(err)
	}
	return &plan
}

func TestRunInputRejectsIdentityAndOperationalAliases(t *testing.T) {
	base := RunInput{
		Identity:        projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "prod", GenerationID: "generation_a"},
		SemanticModelID: "semantic_sales", PipelineID: "pipeline_sales", PrincipalID: "user:test", EstimatedMemoryBytes: 67108864, TargetType: TargetRefreshPipeline,
		TargetID: "pipeline_sales", TriggerType: TriggerManual, TriggerID: "manual", Overlap: "forbid", JobKind: JobKindRefreshPipeline,
	}
	base.PipelinePlan = testPipelinePlan(base.Identity, base.PipelineID.String(), base.SemanticModelID.String())
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RunInput){
		"identity environment": func(input *RunInput) { input.Identity.Environment = "prod " },
		"target id":            func(input *RunInput) { input.TargetID = " pipeline_sales" },
		"pipeline id":          func(input *RunInput) { input.PipelineID = " pipeline_sales" },
		"parent run":           func(input *RunInput) { input.ParentRunID = " parent" },
		"target revision":      func(input *RunInput) { input.TargetRevision = -1 },
		"unsorted groups":      func(input *RunInput) { input.GroupIDs = []string{"team-z", "team-a"} },
		"duplicate groups":     func(input *RunInput) { input.GroupIDs = []string{"team-a", "team-a"} },
		"memory estimate":      func(input *RunInput) { input.EstimatedMemoryBytes = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			mutate(&input)
			if err := input.Validate(); err == nil {
				t.Fatal("Validate() = nil, want alias/invalid identity rejection")
			}
		})
	}
}

func TestJobRecordRequiresCanonicalLeaseFence(t *testing.T) {
	job := JobRecord{
		ID: "job_1", Identity: projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "prod", GenerationID: "generation_a"},
		SemanticModelID: "semantic_sales", PipelineID: "pipeline_sales", PrincipalID: "user:test", EstimatedMemoryBytes: 67108864, Kind: JobKindRefreshPipeline,
		RunID: "run_1", TargetType: TargetRefreshPipeline, TargetID: "pipeline_sales", TriggerType: TriggerManual, TriggerID: "manual",
	}
	job.PipelinePlan = testPipelinePlan(job.Identity, job.PipelineID.String(), job.SemanticModelID.String())
	if err := job.Validate(); err != nil {
		t.Fatal(err)
	}
	job.LeaseOwner, job.LeaseRevision = "worker_1", 1
	if err := job.Validate(); err != nil {
		t.Fatal(err)
	}
	job.LeaseOwner = " worker_1"
	if err := job.Validate(); err == nil {
		t.Fatal("Validate() = nil for whitespace lease owner")
	}
	job.LeaseOwner = "worker_1"
	job.AttemptCount = -1
	if err := job.Validate(); err == nil {
		t.Fatal("Validate() = nil for negative attempt count")
	}
}

func TestRunInputKeepsModelTargetDistinctFromPipeline(t *testing.T) {
	input := RunInput{
		Identity:        projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "prod", GenerationID: "generation_a"},
		SemanticModelID: "semantic_sales", PipelineID: "pipeline_sales", PrincipalID: "user:test", EstimatedMemoryBytes: 67108864, TargetType: TargetModelTable,
		TargetID: "model_sales_customers", TriggerType: TriggerDependency, JobKind: JobKindChildRun,
	}
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	input.TargetType = "invented_target"
	if err := input.Validate(); err == nil {
		t.Fatal("Validate() = nil for invented target type")
	}
}

func TestValidateGroupIDsRequiresCanonicalArrayOrder(t *testing.T) {
	for _, groups := range [][]string{{"team-z", "team-a"}, {"team-a", "team-a"}, {" team-a"}} {
		if err := ValidateGroupIDs(groups); err == nil {
			t.Fatalf("ValidateGroupIDs(%q) = nil", groups)
		}
	}
	if err := ValidateGroupIDs([]string{"team-a", "team-z"}); err != nil {
		t.Fatalf("ValidateGroupIDs(sorted) = %v", err)
	}
}
