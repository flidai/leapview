package run

import (
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestRunInputRejectsIdentityAndOperationalAliases(t *testing.T) {
	base := RunInput{
		Identity:        projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "prod", GenerationID: "generation_a"},
		SemanticModelID: "semantic_sales", PipelineID: "pipeline_sales", PrincipalID: "user:test", EstimatedMemoryBytes: 67108864, TargetType: TargetRefreshPipeline,
		TargetID: "pipeline_sales", TriggerType: TriggerManual, JobKind: JobKindRefreshPipeline,
	}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RunInput){
		"identity environment": func(input *RunInput) { input.Identity.Environment = "prod " },
		"target id":            func(input *RunInput) { input.TargetID = " pipeline_sales" },
		"pipeline id":          func(input *RunInput) { input.PipelineID = " pipeline_sales" },
		"parent run":           func(input *RunInput) { input.ParentRunID = " parent" },
		"target revision":      func(input *RunInput) { input.TargetRevision = -1 },
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
		RunID: "run_1", TargetType: TargetRefreshPipeline, TargetID: "pipeline_sales", TriggerType: TriggerManual,
	}
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
