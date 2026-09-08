package ci

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestAnalyzeHealth(t *testing.T) {
	t.Parallel()

	full := FullJobs()
	selective := Jobs{Docs: true, SiteImage: true}
	runs := []HealthRun{
		{
			Workflow: "ci.yml", Event: "pull_request", Attempt: 2, DurationSeconds: 4, QueueSeconds: 2, Conclusion: "success", Deferred: true,
		},
		{
			Workflow: "merge-validation.yml", Event: "merge_group", DurationSeconds: 600, QueueSeconds: 20, Conclusion: "success",
			Plan:    Plan{Version: PlanVersion, Nominal: full, Effective: full},
			Results: healthSuccessfulResults(full),
		},
		{
			Workflow: "merge-validation.yml", Event: "merge_group", DurationSeconds: 700, QueueSeconds: 30, Conclusion: "success",
			Plan:    Plan{Version: PlanVersion, Nominal: full, Effective: full},
			Results: healthSuccessfulResults(full),
		},
		{
			Workflow: "merge-validation.yml", Event: "merge_group", DurationSeconds: 800, QueueSeconds: 140, Conclusion: "failure",
			Plan:    Plan{Version: PlanVersion, Nominal: full, Effective: full},
			Results: healthSuccessfulResults(full),
		},
		{
			Workflow: "ci.yml", Event: "pull_request", DurationSeconds: 240, QueueSeconds: 10, Conclusion: "success",
			Plan:    Plan{Version: PlanVersion, Nominal: selective, Effective: selective},
			Results: healthSuccessfulResults(selective),
		},
		{
			Workflow: "ci.yml", Event: "pull_request", DurationSeconds: 300, QueueSeconds: 15, Conclusion: "success", Attempt: 2,
			Plan: Plan{Version: PlanVersion, Nominal: selective, Effective: full, Audit: true},
			Results: func() map[string]string {
				results := healthSuccessfulResults(full)
				results["production-image"] = "failure"
				return results
			}(),
		},
	}
	got := AnalyzeHealth(runs)
	if got.RunCount != 6 || got.Deferred != 1 {
		t.Fatalf("run count/deferred = %d/%d, want 6/1", got.RunCount, got.Deferred)
	}
	if got.Full.P95Seconds != 800 {
		t.Fatalf("full p95 = %d, want 800", got.Full.P95Seconds)
	}
	if got.Selective.P95Seconds != 240 {
		t.Fatalf("selective p95 = %d, want 240", got.Selective.P95Seconds)
	}
	if got.Queue.P95Seconds != 140 {
		t.Fatalf("queue p95 = %d, want 140", got.Queue.P95Seconds)
	}
	if got.RerunPercent != 20 {
		t.Fatalf("rerun percentage = %.1f, want 20", got.RerunPercent)
	}
	if got.AuditMisses != 1 {
		t.Fatalf("audit misses = %d, want 1", got.AuditMisses)
	}
	for _, alert := range []string{
		"full CI p95 is 13m20s (limit 12m0s)",
		"queue p95 is 2m20s (limit 2m0s)",
		"rerun rate is 20.0% (limit 3.0%)",
		"selection audit detected 1 miss",
	} {
		if !slices.Contains(got.Alerts, alert) {
			t.Errorf("alerts %v do not contain %q", got.Alerts, alert)
		}
	}
	if got.Selection["docs"].Selected != 5 {
		t.Fatalf("docs selected count = %d, want 5", got.Selection["docs"].Selected)
	}
}

func TestAnalyzeHealthHealthyReportHasNoAlerts(t *testing.T) {
	t.Parallel()

	jobs := Jobs{Docs: true}
	got := AnalyzeHealth([]HealthRun{{
		Workflow: "ci.yml", Event: "pull_request",
		DurationSeconds: 120,
		QueueSeconds:    5,
		Conclusion:      "success",
		Plan:            Plan{Version: PlanVersion, Nominal: jobs, Effective: jobs},
		Results:         healthSuccessfulResults(jobs),
	}})
	if len(got.Alerts) != 0 {
		t.Fatalf("alerts = %v, want none", got.Alerts)
	}
}

func TestHealthMissingPlanDoesNotInventSelection(t *testing.T) {
	r := AnalyzeHealth([]HealthRun{{Workflow: "ci.yml", Event: "pull_request", Conclusion: "failure", DurationSeconds: 800, QueueSeconds: -1, Results: map[string]string{"go-packages-validation": "failure"}}})
	if len(r.Selection) != 0 || r.UnknownSelection != 1 || r.Unknown.Count != 1 || r.Failures != 1 {
		t.Fatalf("fabricated or lost evidence: %+v", r)
	}
	if r.Jobs["go-packages-validation"].Executed != 1 {
		t.Fatal("actual failed execution lost")
	}
	if len(r.Alerts) == 0 {
		t.Fatal("missing evidence must not yield a healthy report")
	}
}

func TestHealthPopulationsAndIncompleteEvidence(t *testing.T) {
	jobs := Jobs{Docs: true}
	plan := Plan{Version: PlanVersion, Nominal: jobs, Effective: jobs}
	runs := []HealthRun{
		{Workflow: "ci.yml", Event: "pull_request", Plan: plan, Conclusion: "success", DurationSeconds: 100, QueueSeconds: 0, Results: healthSuccessfulResults(jobs)},
		{Workflow: "merge-validation.yml", Event: "merge_group", Conclusion: "failure", DurationSeconds: 800, QueueSeconds: 0},
		{Workflow: "nightly.yml", Event: "schedule", Conclusion: "success", DurationSeconds: 900, QueueSeconds: 0},
		{Workflow: "ci.yml", Event: "pull_request", Conclusion: "cancelled", Attempt: 2, DurationSeconds: 50, QueueSeconds: -1},
		{Conclusion: "", DurationSeconds: -1, QueueSeconds: -1},
	}
	r := AnalyzeHealth(runs)
	if r.Selective.Count != 1 || r.Merge.P95Seconds != 800 || r.Nightly.P95Seconds != 900 || r.Unknown.P95Seconds != 50 {
		t.Fatalf("populations mixed: %+v", r)
	}
	if r.Cancellations != 1 || r.Reruns != 1 || r.UnknownConclusions != 1 || r.MissingDurations != 1 {
		t.Fatalf("edge cases lost: %+v", r)
	}
	if r.Selection["docs"].Percent != 100 || r.PlannedRuns != 1 {
		t.Fatal("selection denominator must include only supported plans")
	}
}

func TestUnsupportedPlanRemainsUnknown(t *testing.T) {
	r := AnalyzeHealth([]HealthRun{{Workflow: "ci.yml", Event: "pull_request", Plan: Plan{Version: 99, Effective: FullJobs()}, DurationSeconds: 20, QueueSeconds: -1}})
	if len(r.Selection) != 0 || r.UnknownSelection != 1 || r.Selective.Count != 0 {
		t.Fatalf("unsupported plan trusted: %+v", r)
	}
}

func healthSuccessfulResults(jobs Jobs) map[string]string {
	results := map[string]string{}
	for _, job := range expectedPlanJobs(Plan{Effective: jobs}) {
		results[job] = "success"
	}
	return results
}

func TestHistoricalHealthJSONRemainsReadableWithoutTrustingMissingMetadata(t *testing.T) {
	var run HealthRun
	if err := json.Unmarshal([]byte(`{"event":"pull_request","conclusion":"success","duration_seconds":123,"plan":{"version":1,"effective":{"docs":true},"nominal":{"docs":true}},"results":{"docs":"success"}}`), &run); err != nil {
		t.Fatal(err)
	}
	report := AnalyzeHealth([]HealthRun{run})
	if report.Unknown.Count != 1 || report.Selective.Count != 0 || report.Runs[0].SelectionConfidence == "verified" {
		t.Fatalf("historical metadata inferred: %+v", report)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded HealthReport
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.Version != 2 || decoded.Unknown.Count != 1 {
		t.Fatalf("report roundtrip failed: %s, %v", data, err)
	}
}

func TestMatrixSkipIsNotProofOfCompleteSelection(t *testing.T) {
	jobs := Jobs{Frontend: []string{"core", "site"}}
	report := AnalyzeHealth([]HealthRun{{Workflow: "ci.yml", Event: "pull_request", Conclusion: "success", Plan: Plan{Version: PlanVersion, Nominal: jobs, Effective: jobs}, Results: map[string]string{"frontend-tests/core": "success", "frontend-tests/site": "skipped"}}})
	if report.Runs[0].SelectionConfidence != "incomplete" || report.Jobs["frontend-tests/site"].Skipped != 1 || report.Jobs["frontend-tests/site"].Executed != 0 {
		t.Fatalf("skipped shard accepted: %+v", report)
	}
}
