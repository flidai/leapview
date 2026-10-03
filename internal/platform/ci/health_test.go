package ci

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
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
			Results: healthSuccessfulExhaustiveResults(),
		},
		{
			Workflow: "merge-validation.yml", Event: "merge_group", DurationSeconds: 700, QueueSeconds: 30, Conclusion: "success",
			Plan:    Plan{Version: PlanVersion, Nominal: full, Effective: full},
			Results: healthSuccessfulExhaustiveResults(),
		},
		{
			Workflow: "merge-validation.yml", Event: "merge_group", DurationSeconds: 800, QueueSeconds: 140, Conclusion: "failure",
			Plan:    Plan{Version: PlanVersion, Nominal: full, Effective: full},
			Results: healthSuccessfulExhaustiveResults(),
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
	got := analyzeVerifiedTests(runs)
	if got.RunCount != 6 || got.Deferred != 1 {
		t.Fatalf("run count/deferred = %d/%d, want 6/1", got.RunCount, got.Deferred)
	}
	if got.Full.Count != 3 || got.Full.P95Seconds != nil || got.Selective.Count != 1 || got.Selective.P95Seconds != nil || got.Queue.Count != 5 || got.Queue.P95Seconds != nil {
		t.Fatalf("small-sample p95 should be unavailable while counts remain: full=%+v selective=%+v queue=%+v", got.Full, got.Selective, got.Queue)
	}
	if got.RerunPercent != 20 {
		t.Fatalf("rerun percentage = %.1f, want 20", got.RerunPercent)
	}
	if got.AuditPotentialMisses != 1 {
		t.Fatalf("audit potential misses = %d, want 1", got.AuditPotentialMisses)
	}
	for _, alert := range []string{
		"rerun rate is 20.0% (limit 3.0%)",
		"selection audit detected 1 potential miss",
	} {
		if !slices.Contains(got.Alerts, alert) {
			t.Errorf("alerts %v do not contain %q", got.Alerts, alert)
		}
	}
	if got.Selection["docs"].Selected != 2 {
		t.Fatalf("docs selected count = %d, want 2", got.Selection["docs"].Selected)
	}
}

func TestAnalyzeHealthHealthyReportHasNoAlerts(t *testing.T) {
	t.Parallel()

	jobs := Jobs{Docs: true}
	got := analyzeVerifiedTests([]HealthRun{{
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

func TestAuditOnlyJobsCountOnlyConclusiveFailuresAsPotentialMisses(t *testing.T) {
	for _, conclusion := range []string{"failure", "timed_out", "cancelled", "skipped", "missing"} {
		t.Run(conclusion, func(t *testing.T) {
			nominal := Jobs{Docs: true}
			effective := Jobs{Docs: true, SiteImage: true}
			results := healthSuccessfulResults(effective)
			if conclusion == "missing" {
				delete(results, "site-image")
			} else {
				results["site-image"] = conclusion
			}
			run := verifiedTestContract(HealthRun{
				Workflow: "ci.yml", Event: "pull_request", Conclusion: "success", DurationSeconds: 30,
				Plan: Plan{Version: PlanVersion, Audit: true, Nominal: nominal, Effective: effective}, Results: results,
			})
			report := AnalyzeHealth([]HealthRun{run})
			if conclusion == "failure" || conclusion == "timed_out" {
				if report.AuditPotentialMisses != 1 || report.Incomplete != 0 {
					t.Fatalf("conclusive audit failure not counted: %+v", report)
				}
			} else if report.AuditPotentialMisses != 0 || report.Incomplete != 1 {
				t.Fatalf("inconclusive audit result was reported as a miss or healthy: %+v", report)
			}
		})
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
	r := analyzeVerifiedTests(runs)
	if r.Selective.Count != 1 || r.Merge.Count != 1 || r.Merge.P50Seconds != 800 || r.Merge.P95Seconds != nil || r.Nightly.Count != 1 || r.Nightly.P50Seconds != 900 || r.Nightly.P95Seconds != nil || r.Unknown.Count != 1 || r.Unknown.P50Seconds != 50 || r.Unknown.P95Seconds != nil {
		t.Fatalf("populations mixed: %+v", r)
	}
	if r.Cancellations != 1 || r.Reruns != 1 || r.UnknownConclusions != 1 || r.MissingDurations != 1 {
		t.Fatalf("edge cases lost: %+v", r)
	}
	if r.Selection["docs"].Percent != 100 || r.PlannedRuns != 1 {
		t.Fatal("selection denominator must include only supported plans")
	}
}

func TestColdDraftSkipWithoutPlanRemainsIncomplete(t *testing.T) {
	results := map[string]string{"prepare": "skipped", "ci-gate": "skipped"}
	run := HealthRun{Workflow: "ci.yml", Event: "pull_request", Conclusion: "success", Attempt: 1,
		DurationSeconds: -1, QueueSeconds: -1, PlanIssue: "missing, expired or invalid ci-plan artifact", Results: results}
	report := AnalyzeHealth([]HealthRun{run})
	if report.Incomplete != 1 || report.UnknownSelection != 1 || len(report.Alerts) == 0 {
		t.Fatalf("cold draft run inferred a healthy skip: %+v", report)
	}
	if report.Runs[0].Category != "unknown" || report.Runs[0].SelectionConfidence != "unknown" {
		t.Fatalf("cold draft run provenance was not left unknown: %+v", report.Runs[0])
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

func healthSuccessfulExhaustiveResults() map[string]string {
	return map[string]string{"ci-gate": "success"}
}

// verifiedTestContract marks synthetic test runs with explicit immutable
// workflow metadata. Production analysis never supplies this evidence itself.
func verifiedTestContract(run HealthRun) HealthRun {
	run.WorkflowSHA = strings.Repeat("a", 40)
	run.WorkflowJobs = append([]string(nil), expectedPlanJobs(run.Plan)...)
	run.WorkflowJobs = append(run.WorkflowJobs, "ci-gate")
	for job := range run.Results {
		run.WorkflowJobs = append(run.WorkflowJobs, job)
	}
	run.WorkflowRequiredJobs = []string{"ci-gate"}
	return run
}

func analyzeVerifiedTests(runs []HealthRun) HealthReport {
	for index := range runs {
		if validHealthPlan(runs[index].Plan) || runs[index].Workflow == "merge-validation.yml" || runs[index].Workflow == "nightly.yml" {
			runs[index] = verifiedTestContract(runs[index])
		}
	}
	return AnalyzeHealth(runs)
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
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.Version != 3 || decoded.Unknown.Count != 1 {
		t.Fatalf("report roundtrip failed: %s, %v", data, err)
	}
}

func TestP95RequiresTwentySamples(t *testing.T) {
	for _, count := range []int{19, 20} {
		t.Run(fmt.Sprintf("%d samples", count), func(t *testing.T) {
			runs := make([]HealthRun, count)
			for i := range runs {
				runs[i] = HealthRun{
					Workflow: "merge-validation.yml", Event: "merge_group", Conclusion: "success",
					DurationSeconds: 800, QueueSeconds: 140, Results: healthSuccessfulExhaustiveResults(),
				}
			}
			report := analyzeVerifiedTests(runs)
			for name, metric := range map[string]DurationMetric{"merge": report.Merge, "queue": report.Queue} {
				if metric.Count != count || metric.P50Seconds == 0 {
					t.Errorf("%s count/p50 = %d/%d, want %d/nonzero", name, metric.Count, metric.P50Seconds, count)
				}
				data, err := json.Marshal(metric)
				if err != nil {
					t.Fatal(err)
				}
				var encoded map[string]json.RawMessage
				if err := json.Unmarshal(data, &encoded); err != nil {
					t.Fatal(err)
				}
				if count < MinimumP95Samples {
					if metric.P95Seconds != nil || encoded["p95_seconds"] != nil {
						t.Errorf("%s p95 at %d samples = %v / %s, want omitted", name, count, metric.P95Seconds, encoded["p95_seconds"])
					}
				} else if metric.P95Seconds == nil || encoded["p95_seconds"] == nil {
					t.Errorf("%s p95 at %d samples = %v / %s, want emitted", name, count, metric.P95Seconds, encoded["p95_seconds"])
				}
			}
			p95Alerts := 0
			for _, alert := range report.Alerts {
				if strings.Contains(alert, " p95 is ") {
					p95Alerts++
				}
			}
			wantAlerts := 0
			if count == MinimumP95Samples {
				wantAlerts = 2
			}
			if p95Alerts != wantAlerts {
				t.Errorf("p95 alerts = %d, want %d: %v", p95Alerts, wantAlerts, report.Alerts)
			}
		})
	}
}

func TestHistoricalV2HealthProjectionRemainsReadOnly(t *testing.T) {
	plan := PlanChanges(Input{Event: "pull_request", PullRequestNumber: 1}, []Change{{Status: "M", Paths: []string{"README.md"}}})
	plan.Version = HistoricalPRPlanVersion
	plan.PR.Nominal.Quality = false
	plan.PR.Effective.Quality = false
	run := HealthRun{
		Workflow: "ci.yml", Event: "pull_request", Conclusion: "success", DurationSeconds: 10, QueueSeconds: 1,
		Plan: plan,
		Results: map[string]string{
			"prepare": "success", "docs-validation": "success", "frontend-validation/site": "success",
		},
	}
	report := AnalyzeHealth([]HealthRun{run})
	if report.Selective.Count != 1 || report.Runs[0].SelectionConfidence != "incomplete" {
		t.Fatalf("historical v2 plan without workflow source was not left incomplete: %+v", report.Runs[0])
	}
	if _, present := report.Selection["quality-validation"]; present {
		t.Fatalf("historical v2 report fabricated quality selection: %+v", report)
	}

	plan.PR.Effective.Quality = true
	report = AnalyzeHealth([]HealthRun{{Workflow: "ci.yml", Event: "pull_request", Conclusion: "success", Plan: plan, Results: run.Results}})
	if report.Selective.Count != 0 || report.UnknownSelection != 1 {
		t.Fatalf("historical v2 quality tamper was trusted: %+v", report)
	}
}

func TestMatrixSkipIsNotProofOfCompleteSelection(t *testing.T) {
	jobs := Jobs{Frontend: []string{"core", "site"}}
	report := analyzeVerifiedTests([]HealthRun{{Workflow: "ci.yml", Event: "pull_request", Conclusion: "success", Plan: Plan{Version: PlanVersion, Nominal: jobs, Effective: jobs}, Results: map[string]string{"frontend-tests/core": "success", "frontend-tests/site": "skipped"}}})
	if report.Runs[0].SelectionConfidence != "incomplete" || report.Jobs["frontend-tests/site"].Skipped != 1 || report.Jobs["frontend-tests/site"].Executed != 0 {
		t.Fatalf("skipped shard accepted: %+v", report)
	}
}

func TestExhaustiveWorkflowUsesProvidedContractWithoutAPlan(t *testing.T) {
	t.Parallel()

	results := map[string]string{}
	results["ci-gate"] = "success"
	report := analyzeVerifiedTests([]HealthRun{{
		Workflow: "merge-validation.yml", Event: "merge_group", Conclusion: "success",
		DurationSeconds: 600, QueueSeconds: 3, Results: results,
	}})
	run := report.Runs[0]
	if report.Incomplete != 0 || report.UnknownSelection != 0 {
		t.Fatalf("exhaustive workflow treated as missing planner evidence: %+v", report)
	}
	if run.ExpectedSource != "workflow_contract" || run.SelectionConfidence != "verified" || len(run.Problems) != 0 {
		t.Fatalf("workflow contract was not accepted as complete evidence: %+v", run)
	}
}

func TestHistoricalExhaustiveRunWithoutWorkflowSourceRemainsIncomplete(t *testing.T) {
	result := AnalyzeHealth([]HealthRun{{
		Workflow: "merge-validation.yml", Event: "merge_group", Conclusion: "success",
		DurationSeconds: 600, QueueSeconds: 3, Results: map[string]string{"unknown/old gate name": "success"},
	}})
	run := result.Runs[0]
	if run.ExpectedSource != "workflow_contract" || run.SelectionConfidence != "incomplete" || result.Incomplete != 1 {
		t.Fatalf("missing historical source was treated as a current inventory: %+v", result)
	}
	if len(run.ExpectedJobs) != 0 || run.ContractIssue == "" || len(result.Alerts) == 0 {
		t.Fatalf("unavailable historical workflow evidence was hidden: %+v", result)
	}
}

func TestPlanningMetricsOnlyUsePullRequestPlans(t *testing.T) {
	t.Parallel()

	jobs := Jobs{Docs: true}
	plan := Plan{Version: PlanVersion, Nominal: jobs, Effective: jobs}
	full := FullJobs()
	fullPlan := Plan{Version: PlanVersion, Nominal: full, Effective: full}
	results := healthSuccessfulResults(jobs)
	report := analyzeVerifiedTests([]HealthRun{
		{Workflow: "ci.yml", Event: "pull_request", Conclusion: "success", DurationSeconds: 100, QueueSeconds: 1, Plan: plan, Results: results},
		{Workflow: "ci.yml", Event: "workflow_dispatch", Conclusion: "success", DurationSeconds: 110, QueueSeconds: 1, Plan: fullPlan, Results: healthSuccessfulResults(full)},
		{Workflow: "ci.yml", Event: "workflow_dispatch", Conclusion: "success", DurationSeconds: 120, QueueSeconds: 1, Plan: plan, Results: results},
	})
	if report.PlannedRuns != 1 || report.UnknownSelection != 0 || report.Selection["docs"].Selected != 1 {
		t.Fatalf("manual full run polluted PR selection metrics: %+v", report)
	}
	if report.FullPR.Count != 1 || report.Runs[1].Category != "full_pr" {
		t.Fatalf("manual full validation was not classified: %+v", report.Runs)
	}
	if report.Unknown.Count != 1 || report.Runs[2].Category != "unknown" {
		t.Fatalf("manual selective validation was classified as full: %+v", report.Runs)
	}
}

func TestFailedExpectedJobIsCompleteFailureEvidence(t *testing.T) {
	t.Parallel()

	jobs := Jobs{Docs: true}
	results := healthSuccessfulResults(jobs)
	results["docs"] = "failure"
	report := analyzeVerifiedTests([]HealthRun{{
		Workflow: "ci.yml", Event: "pull_request", Conclusion: "failure",
		DurationSeconds: 100, QueueSeconds: 1,
		Plan: Plan{Version: PlanVersion, Nominal: jobs, Effective: jobs}, Results: results,
	}})
	if report.Incomplete != 0 || report.Failures != 1 || report.Runs[0].SelectionConfidence != "verified" {
		t.Fatalf("known failure mislabeled as incomplete evidence: %+v", report)
	}
}

func TestMandatoryHostRecoveryChildJobsAugmentSelectivePlanEvidence(t *testing.T) {
	plan := PlanChanges(Input{Event: "pull_request", PullRequestNumber: 1}, []Change{{Status: "M", Paths: []string{"README.md"}}})
	results := map[string]string{}
	for _, job := range expectedPlanJobs(plan) {
		results[job] = "success"
	}
	planIndependent := []string{"qualification", "qualification/recovery", "qualification/transition"}
	for _, job := range planIndependent {
		results[job] = "success"
	}
	run := HealthRun{
		Workflow: "ci.yml", Event: "pull_request", Conclusion: "success", DurationSeconds: 10, QueueSeconds: 1,
		Plan: plan, Results: results, PlanIndependentJobs: planIndependent,
	}
	complete := analyzeVerifiedTests([]HealthRun{run})
	if complete.Runs[0].SelectionConfidence != "verified" || complete.Incomplete != 0 {
		t.Fatalf("mandatory host recovery lane was treated as unplanned or incomplete: %+v", complete.Runs[0])
	}
	for _, job := range planIndependent {
		if !slices.Contains(complete.Runs[0].ExpectedJobs, job) {
			t.Errorf("selective plan omitted workflow-independent evidence %q: %+v", job, complete.Runs[0].ExpectedJobs)
		}
	}

	delete(results, "qualification/transition")
	incomplete := analyzeVerifiedTests([]HealthRun{run})
	if incomplete.Incomplete != 1 || !slices.Contains(incomplete.Runs[0].UnknownJobs, "qualification/transition") {
		t.Fatalf("missing workflow-independent child was accepted: %+v", incomplete.Runs[0])
	}
}
