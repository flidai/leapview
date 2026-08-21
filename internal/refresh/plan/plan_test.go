package plan

import (
	"reflect"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/refresh/artifact"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

const testSelectionDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestForPipelineOrdersDependenciesBeforeDependents(t *testing.T) {
	definition := &artifact.Definition{
		ModelTables: map[string]semanticmodel.Table{
			"orders":    {ModelName: "orders", ModelDependencies: []string{"customers"}},
			"customers": {ModelName: "customers"},
		},
		Models: map[string]*semanticmodel.Model{
			"sales": {
				Tables: map[string]semanticmodel.Table{
					"orders":    {ModelName: "orders", ModelDependencies: []string{"customers"}},
					"customers": {ModelName: "customers"},
				},
				Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}, "customers": {Model: "customers"}},
			},
		},
		Pipelines: map[string]refreshschedule.Definition{
			"daily": {ID: "daily", SemanticModelID: "sales", SelectionDigest: testSelectionDigest},
		},
	}

	got, err := ForPipeline(definition, projectgraph.ResourceID("project_acme"), projectgraph.ResourceID("daily"))
	if err != nil {
		t.Fatalf("plan refresh pipeline: %v", err)
	}
	if got.TargetType != "refresh_pipeline" || got.TargetID != "daily" || got.SemanticModelID != "sales" {
		t.Fatalf("unexpected plan identity: %#v", got)
	}
	if want := []string{"customers", "orders"}; !reflect.DeepEqual(got.Tables, want) {
		t.Fatalf("tables = %#v, want %#v", got.Tables, want)
	}
	if !reflect.DeepEqual(got.DependencyTables, got.Tables) {
		t.Fatalf("dependency tables = %#v, want %#v", got.DependencyTables, got.Tables)
	}
}

func TestForPipelineIncludesUpstreamModelsOutsideSemanticDatasets(t *testing.T) {
	definition := &artifact.Definition{
		Models: map[string]*semanticmodel.Model{
			"sales": {
				Tables:   map[string]semanticmodel.Table{"orders": {ModelName: "orders"}},
				Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
			},
		},
		ModelTables: map[string]semanticmodel.Table{
			"staged_orders": {Execution: semanticmodel.ExecutionDefinition{Source: "source:raw_orders"}, SourceDependencies: []string{"source:raw_orders"}},
			"orders":        {ModelDependencies: []string{"staged_orders"}},
		},
		Pipelines: map[string]refreshschedule.Definition{
			"daily": {ID: "daily", SemanticModelID: "sales", SelectionDigest: testSelectionDigest},
		},
	}

	got, err := ForPipeline(definition, "project_acme", "daily")
	if err != nil {
		t.Fatalf("plan refresh pipeline: %v", err)
	}
	if want := []string{"staged_orders", "orders"}; !reflect.DeepEqual(got.MaterializationScope, want) {
		t.Fatalf("materialization scope = %#v, want %#v", got.MaterializationScope, want)
	}
	if want := []string{"source:raw_orders"}; !reflect.DeepEqual(got.SourceInputs, want) {
		t.Fatalf("source inputs = %#v, want %#v", got.SourceInputs, want)
	}
}

func TestForPipelineRejectsDependencyCycles(t *testing.T) {
	definition := &artifact.Definition{
		ModelTables: map[string]semanticmodel.Table{
			"orders":    {ModelDependencies: []string{"customers"}},
			"customers": {ModelDependencies: []string{"orders"}},
		},
		Models: map[string]*semanticmodel.Model{
			"sales": {
				Tables: map[string]semanticmodel.Table{
					"orders":    {ModelDependencies: []string{"customers"}},
					"customers": {ModelDependencies: []string{"orders"}},
				},
			},
		},
		Pipelines: map[string]refreshschedule.Definition{
			"daily": {ID: "daily", SemanticModelID: "sales", SelectionDigest: testSelectionDigest},
		},
	}

	if _, err := ForPipeline(definition, projectgraph.ResourceID("project_acme"), projectgraph.ResourceID("daily")); err == nil {
		t.Fatal("expected dependency cycle to be rejected")
	}
}

func TestForPipelineMaterializesDatasetAliasesOnceByModelName(t *testing.T) {
	definition := &artifact.Definition{
		ModelTables: map[string]semanticmodel.Table{
			"sales_orders":  {},
			"sales_summary": {ModelDependencies: []string{"sales_orders"}},
		},
		Models: map[string]*semanticmodel.Model{
			"sales": {
				Tables: map[string]semanticmodel.Table{
					"orders":    {ModelName: "sales_orders"},
					"purchases": {ModelName: "sales_orders"},
					"summary":   {ModelName: "sales_summary", ModelDependencies: []string{"sales_orders"}},
				},
				Datasets: map[string]semanticmodel.SemanticDatasetSpec{
					"orders":    {Model: "sales_orders"},
					"purchases": {Model: "sales_orders"},
					"summary":   {Model: "sales_summary"},
				},
			},
		},
		Pipelines: map[string]refreshschedule.Definition{
			"daily": {ID: "daily", SemanticModelID: "sales", SelectionDigest: testSelectionDigest},
		},
	}
	got, err := ForPipeline(definition, projectgraph.ResourceID("project_acme"), projectgraph.ResourceID("daily"))
	if err != nil {
		t.Fatalf("plan refresh pipeline: %v", err)
	}
	if want := []string{"sales_orders", "sales_summary"}; !reflect.DeepEqual(got.Tables, want) {
		t.Fatalf("tables = %#v, want %#v", got.Tables, want)
	}
}

func TestForPipelineResolvesDistinctAliasPhysicalDependencies(t *testing.T) {
	definition := &artifact.Definition{
		ModelTables: map[string]semanticmodel.Table{
			"sales_orders":  {},
			"sales_summary": {ModelDependencies: []string{"sales_orders"}},
		},
		Models: map[string]*semanticmodel.Model{
			"sales": {
				Tables: map[string]semanticmodel.Table{
					"orders_alias":  {ModelName: "sales_orders"},
					"summary_alias": {ModelName: "sales_summary", ModelDependencies: []string{"sales_orders"}},
				},
				Datasets: map[string]semanticmodel.SemanticDatasetSpec{
					"orders_alias":  {Model: "sales_orders"},
					"summary_alias": {Model: "sales_summary"},
				},
			},
		},
		Pipelines: map[string]refreshschedule.Definition{
			"daily": {ID: "daily", SemanticModelID: "sales", SelectionDigest: testSelectionDigest},
		},
	}
	got, err := ForPipeline(definition, projectgraph.ResourceID("project_acme"), projectgraph.ResourceID("daily"))
	if err != nil {
		t.Fatalf("plan refresh pipeline: %v", err)
	}
	if want := []string{"sales_orders", "sales_summary"}; !reflect.DeepEqual(got.Tables, want) {
		t.Fatalf("tables = %#v, want %#v", got.Tables, want)
	}
}

func TestForPipelineRejectsUnknownModelDependency(t *testing.T) {
	definition := &artifact.Definition{
		ModelTables: map[string]semanticmodel.Table{
			"sales_orders": {ModelDependencies: []string{"missing_model"}},
		},
		Models: map[string]*semanticmodel.Model{
			"sales": {
				Tables: map[string]semanticmodel.Table{
					"orders_alias": {ModelName: "sales_orders", ModelDependencies: []string{"missing_model"}},
				},
				Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders_alias": {Model: "sales_orders"}},
			},
		},
		Pipelines: map[string]refreshschedule.Definition{
			"daily": {ID: "daily", SemanticModelID: "sales", SelectionDigest: testSelectionDigest},
		},
	}
	if _, err := ForPipeline(definition, projectgraph.ResourceID("project_acme"), projectgraph.ResourceID("daily")); err == nil || !strings.Contains(err.Error(), "unknown model dependency") {
		t.Fatalf("plan refresh pipeline error = %v, want unknown dependency", err)
	}
}

func TestBindGenerationProducesStableGenerationBoundDigest(t *testing.T) {
	definition := &artifact.Definition{
		Models:      map[string]*semanticmodel.Model{"sales": {Tables: map[string]semanticmodel.Table{"orders": {ModelName: "orders"}}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}}},
		ModelTables: map[string]semanticmodel.Table{"orders": {}},
		Pipelines:   map[string]refreshschedule.Definition{"daily": {ID: "daily", SemanticModelID: "sales", SelectionDigest: testSelectionDigest}},
	}
	base, err := ForPipeline(definition, projectgraph.ResourceID("project_acme"), projectgraph.ResourceID("daily"))
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID("project_acme"), "prod", "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	first, err := base.BindGeneration(identity, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	second, err := base.BindGeneration(identity, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest == "" || first.Digest != second.Digest {
		t.Fatalf("digest is not deterministic: %q vs %q", first.Digest, second.Digest)
	}
	other, err := base.BindGeneration(projectgraph.ServingIdentity{ProjectID: identity.ProjectID, Environment: identity.Environment, GenerationID: "generation-2"}, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if other.Digest == first.Digest {
		t.Fatal("generation change did not change pipeline plan digest")
	}
	scheduleID := "weekdays 06:00 · " + strings.Repeat("evidence", 40)
	delivery, err := first.DeliveryPipelinePlan(InvocationPolicy{InvocationSource: "schedule", MatchingScheduleIDs: []string{scheduleID}, StartingDeadlineSeconds: 3600, ConcurrencyPolicy: "Replace"})
	if err != nil {
		t.Fatal(err)
	}
	if delivery.ProjectID != "project_acme" || delivery.Environment != "prod" || delivery.ServingGenerationID != "generation-1" || delivery.MaterializationScope[0] != "orders" || delivery.ModelExecutionOrder[0] != "orders" || delivery.Digest == "" {
		t.Fatalf("unexpected delivery pipeline plan: %#v", delivery)
	}
	if delivery.InvocationSource != "schedule" || len(delivery.MatchingScheduleIDs) != 1 || delivery.MatchingScheduleIDs[0] != scheduleID || delivery.StartingDeadlineSeconds != 3600 || delivery.ConcurrencyPolicy != "Replace" {
		t.Fatalf("effective invocation policy = %#v", delivery)
	}
	for name, value := range map[string]string{"execution": delivery.ExecutionDigest, "provenance": delivery.ProvenanceDigest, "governance": delivery.GovernanceDigest, "evidence": delivery.EvidenceDigest} {
		if value == "" {
			t.Errorf("%s digest is empty", name)
		}
	}
}

func TestInvocationEvidenceChangesGovernanceNotExecutionDigest(t *testing.T) {
	definition := &artifact.Definition{
		Models:      map[string]*semanticmodel.Model{"sales": {Tables: map[string]semanticmodel.Table{"orders": {ModelName: "orders"}}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}}},
		ModelTables: map[string]semanticmodel.Table{"orders": {}},
		Pipelines:   map[string]refreshschedule.Definition{"daily": {ID: "daily", SemanticModelID: "sales", SelectionDigest: testSelectionDigest}},
	}
	base, err := ForPipeline(definition, "project_acme", "daily")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project_acme", "prod", "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := base.BindGeneration(identity, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	manual, err := bound.DeliveryPipelinePlan(InvocationPolicy{InvocationSource: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	scheduled, err := bound.DeliveryPipelinePlan(InvocationPolicy{InvocationSource: "schedule", MatchingScheduleIDs: []string{"daily"}, StartingDeadlineSeconds: 60, ConcurrencyPolicy: "Forbid"})
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := bound.DeliveryPipelinePlan(InvocationPolicy{InvocationSource: "schedule", MatchingScheduleIDs: []string{"renamed"}, StartingDeadlineSeconds: 60, ConcurrencyPolicy: "Forbid"})
	if err != nil {
		t.Fatal(err)
	}
	if manual.ExecutionDigest != scheduled.ExecutionDigest {
		t.Fatalf("execution digest changed with admission evidence: %s/%s", manual.ExecutionDigest, scheduled.ExecutionDigest)
	}
	if manual.GovernanceDigest == scheduled.GovernanceDigest || manual.EvidenceDigest == scheduled.EvidenceDigest {
		t.Fatal("plan digests did not capture invocation evidence")
	}
	if scheduled.ExecutionDigest != renamed.ExecutionDigest || scheduled.GovernanceDigest != renamed.GovernanceDigest || scheduled.EvidenceDigest == renamed.EvidenceDigest {
		t.Fatalf("schedule evidence was folded into execution/governance: %#v / %#v", scheduled, renamed)
	}
	if manual.ConcurrencyPolicy != "" || manual.StartingDeadlineSeconds != 0 || len(manual.MatchingScheduleIDs) != 0 {
		t.Fatalf("manual plan unexpectedly carried scheduling policy: %#v", manual)
	}
}
