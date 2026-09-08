package ci

import (
	"slices"
	"testing"
)

func TestExpectedMergeJobsCannotBeOverriddenByPartialPlan(t *testing.T) {
	jobs := Jobs{Docs: true}
	report := AnalyzeHealth([]HealthRun{{Workflow: "merge-validation.yml", Event: "merge_group", Conclusion: "success", Plan: Plan{Version: PlanVersion, Nominal: jobs, Effective: jobs}, Results: map[string]string{"docs": "success"}}})
	run := report.Runs[0]
	if run.ExpectedSource != "workflow_registry" || !slices.Contains(run.UnknownJobs, "full-validation") || run.SelectionConfidence == "verified" {
		t.Fatalf("partial plan overrode merge contract: %+v", run)
	}
}
