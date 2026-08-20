package compiler

import (
	"strings"
	"testing"

	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

func TestLowerRefreshPipelineScheduledContract(t *testing.T) {
	authored := projectcontracts.PipelineDocument{
		Kind:     projectcontracts.PipelineResourceKindPipeline,
		Metadata: projectcontracts.PipelineMetadata{ID: "pipeline:sales", Name: "sales_refresh"},
		Spec: projectcontracts.PipelineSpec{Value: &projectcontracts.ScheduledPipelineSpec{
			Selection: projectcontracts.PipelineSelection{SemanticModel: "sales"},
			Schedules: map[string]string{
				"zeta":  " 0  6 * * * ",
				"alpha": "0 6 * * *",
				"later": "0 7 * * *",
			},
			Timezone:                "Europe/Copenhagen",
			StartingDeadlineSeconds: 3600,
			ConcurrencyPolicy:       projectcontracts.PipelineConcurrencyPolicyReplace,
		}},
	}
	definition, err := lowerRefreshPipeline(authored)
	if err != nil {
		t.Fatalf("lowerRefreshPipeline() error = %v", err)
	}
	if definition.Timezone != "Europe/Copenhagen" || definition.StartingDeadlineSeconds != 3600 || definition.ConcurrencyPolicy != "Replace" {
		t.Fatalf("definition scheduling policy = %#v", definition)
	}
	if got := []string{definition.Schedules[0].ID, definition.Schedules[1].ID, definition.Schedules[2].ID}; strings.Join(got, ",") != "alpha,zeta,later" {
		t.Fatalf("schedule order = %v, want alpha,zeta,later", got)
	}
	if definition.Schedules[0].Expression != "0 6 * * *" {
		t.Fatalf("canonical expression = %q", definition.Schedules[0].Expression)
	}
}

func TestLowerRefreshPipelineManualOnlyOmitsSchedulingPolicy(t *testing.T) {
	authored := projectcontracts.PipelineDocument{
		Kind:     projectcontracts.PipelineResourceKindPipeline,
		Metadata: projectcontracts.PipelineMetadata{ID: "pipeline:manual", Name: "manual"},
		Spec: projectcontracts.PipelineSpec{Value: &projectcontracts.ManualPipelineSpec{
			Selection: projectcontracts.PipelineSelection{SemanticModel: "sales"},
		}},
	}
	definition, err := lowerRefreshPipeline(authored)
	if err != nil {
		t.Fatalf("lowerRefreshPipeline() error = %v", err)
	}
	if definition.Timezone != "" || definition.StartingDeadlineSeconds != 0 || definition.ConcurrencyPolicy != "" || len(definition.Schedules) != 0 {
		t.Fatalf("manual definition scheduling fields = %#v", definition)
	}
}

func TestLowerRefreshPipelineRejectsEmptyScheduledMap(t *testing.T) {
	authored := projectcontracts.PipelineDocument{
		Kind:     projectcontracts.PipelineResourceKindPipeline,
		Metadata: projectcontracts.PipelineMetadata{ID: "pipeline:empty", Name: "empty"},
		Spec: projectcontracts.PipelineSpec{Value: &projectcontracts.ScheduledPipelineSpec{
			Selection:               projectcontracts.PipelineSelection{SemanticModel: "sales"},
			Schedules:               map[string]string{},
			Timezone:                "Europe/Copenhagen",
			ConcurrencyPolicy:       projectcontracts.PipelineConcurrencyPolicyForbid,
			StartingDeadlineSeconds: 0,
		}},
	}
	if _, err := lowerRefreshPipeline(authored); err == nil || !strings.Contains(err.Error(), "at least one schedule") {
		t.Fatalf("lowerRefreshPipeline() error = %v, want empty schedule diagnostic", err)
	}
}
