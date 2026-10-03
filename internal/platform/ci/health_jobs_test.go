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

func TestQualityAndHostRecoveryHealthLaneRegistryAndHistoricalInventory(t *testing.T) {
	if got := HealthJobName("Cross-language quality (PR)"); got != "quality-validation" {
		t.Fatalf("quality display name = %q, want quality-validation", got)
	}
	if !slices.Contains(ExpectedHealthJobs("ci.yml"), "quality-validation") {
		t.Fatal("current CI inventory omits quality lane")
	}
	if slices.Contains(HistoricalExpectedHealthJobs("ci.yml"), "quality-validation") {
		t.Fatal("historical CI inventory fabricated quality lane")
	}
	if !slices.Contains(ExpectedHealthJobs("merge-validation.yml"), "host-recovery-validation") {
		t.Fatal("current merge inventory omits host recovery qualification")
	}
	for _, child := range []string{"host-recovery-validation/recovery", "host-recovery-validation/historical-transition"} {
		if !slices.Contains(ExpectedHealthJobs("merge-validation.yml"), child) {
			t.Errorf("current merge inventory omits reusable child %q", child)
		}
	}
	if slices.Contains(HistoricalExpectedHealthJobs("merge-validation.yml"), "host-recovery-validation") {
		t.Fatal("historical merge inventory fabricated host recovery qualification")
	}
}

func TestReusableHostQualificationChildrenNormalizeToRequiredJobs(t *testing.T) {
	for name, want := range map[string]string{
		"host-recovery-validation / recovery":              hostRecoveryRecoveryJob,
		"host-recovery-validation / historical-transition": hostRecoveryHistoricalJob,
		"Isolated host recovery and migration boundary contracts / Isolated host recovery and migration boundary contracts": hostRecoveryRecoveryJob,
		"Isolated host recovery and migration boundary contracts / Schema-32 legacy access transition":                      hostRecoveryHistoricalJob,
		"Qualification / Host recovery and migration / Schema-32 legacy access transition":                                         hostRecoveryHistoricalJob,
		"Qualify schema-32 predecessor transition against exact image / Schema-32 legacy access transition":                 hostRecoveryHistoricalJob,
		"host-recovery-validation / Qualification / Host recovery and migration / recovery":                                        hostRecoveryRecoveryJob,
		"Isolated host recovery and migration boundary contracts":                                                           hostRecoveryHealthLane,
	} {
		if got := HealthJobName(name); got != want {
			t.Errorf("HealthJobName(%q) = %q, want %q", name, got, want)
		}
	}
	if got := HealthJobName("unrelated reusable workflow / Schema-32 legacy access transition"); got == "host-recovery-validation" {
		t.Fatal("an unrelated reusable workflow was accepted as host recovery")
	}
}
