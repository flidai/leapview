package ci

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"
)

type HealthRun struct {
	ID         int64  `json:"id"`
	Workflow   string `json:"workflow"`
	Event      string `json:"event"`
	Attempt    int    `json:"attempt"`
	Conclusion string `json:"conclusion"`
	// Negative durations denote unavailable timestamps, never zero latency.
	DurationSeconds     int64             `json:"duration_seconds"`
	QueueSeconds        int64             `json:"queue_seconds"`
	Deferred            bool              `json:"deferred,omitempty"`
	Plan                Plan              `json:"plan"`
	PlanIssue           string            `json:"plan_issue,omitempty"`
	Results             map[string]string `json:"results"`
	Category            string            `json:"category"`
	SelectionConfidence string            `json:"selection_confidence"`
	ExpectedSource      string            `json:"expected_source"`
	PlannedJobs         []string          `json:"planned_jobs"`
	ExpectedJobs        []string          `json:"expected_jobs"`
	ExecutedJobs        []string          `json:"executed_jobs"`
	SkippedJobs         []string          `json:"skipped_jobs"`
	UnknownJobs         []string          `json:"unknown_jobs"`
	Problems            []string          `json:"problems"`
}

type DurationMetric struct {
	Count      int   `json:"count"`
	P50Seconds int64 `json:"p50_seconds"`
	P95Seconds int64 `json:"p95_seconds"`
}

type SelectionMetric struct {
	Selected int     `json:"selected"`
	Percent  float64 `json:"percent"`
}

type JobMetric struct {
	Expected int `json:"expected"`
	Executed int `json:"executed"`
	Skipped  int `json:"skipped"`
	Unknown  int `json:"unknown"`
}

type HealthReport struct {
	Version            int       `json:"version"`
	GeneratedAt        time.Time `json:"generated_at"`
	RunCount           int       `json:"run_count"`
	Successes          int       `json:"successes"`
	Failures           int       `json:"failures"`
	Cancellations      int       `json:"cancellations"`
	Skipped            int       `json:"skipped"`
	UnknownConclusions int       `json:"unknown_conclusions"`
	Deferred           int       `json:"deferred"`
	// Full is retained as a JSON compatibility alias for Merge, not a mixed population.
	Full             DurationMetric             `json:"full"`
	Merge            DurationMetric             `json:"merge"`
	Nightly          DurationMetric             `json:"nightly"`
	FullPR           DurationMetric             `json:"full_pr"`
	Selective        DurationMetric             `json:"selective"`
	Unknown          DurationMetric             `json:"unknown"`
	Queue            DurationMetric             `json:"queue"`
	MissingDurations int                        `json:"missing_durations"`
	Reruns           int                        `json:"reruns"`
	RerunPercent     float64                    `json:"rerun_percent"`
	PlannedRuns      int                        `json:"planned_runs"`
	UnknownSelection int                        `json:"unknown_selection"`
	Incomplete       int                        `json:"incomplete"`
	AuditSamples     int                        `json:"audit_samples"`
	AuditMisses      int                        `json:"audit_misses"`
	Selection        map[string]SelectionMetric `json:"selection"`
	Jobs             map[string]JobMetric       `json:"jobs"`
	Runs             []HealthRun                `json:"runs"`
	Alerts           []string                   `json:"alerts"`
}

func knownConclusion(result string) bool {
	switch result {
	case "success", "failure", "cancelled", "skipped", "timed_out", "action_required", "startup_failure":
		return true
	default:
		return false
	}
}

func expectedPlanJobs(plan Plan) []string {
	if plan.Version == PRPlanVersion && plan.PR != nil {
		return append([]string{"prepare"}, plan.PR.Effective.ExpectedJobs()...)
	}
	var result []string
	for name, selected := range plan.Effective.Selected() {
		if !selected {
			continue
		}
		switch name {
		case "go-tests":
			for _, shard := range plan.Effective.GoMatrix {
				result = append(result, "go-tests/"+shard.Name)
			}
		case "frontend-tests":
			for _, shard := range plan.Effective.Frontend {
				result = append(result, "frontend-tests/"+shard)
			}
		default:
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

func classifyHealthRun(run HealthRun) HealthRun {
	run.Category = "unknown"
	run.SelectionConfidence = "unknown"
	run.ExpectedSource = "unknown"
	run.PlannedJobs = nil
	run.ExpectedJobs = nil
	run.ExecutedJobs = nil
	run.SkippedJobs = nil
	run.UnknownJobs = nil
	run.Problems = nil
	supported := ((run.Plan.Version == PlanVersion && run.Plan.PR == nil) || (run.Plan.Version == PRPlanVersion && ValidatePRPlan(run.Plan) == nil)) && run.PlanIssue == "" && len(expectedPlanJobs(run.Plan)) > 0
	if run.Workflow == "merge-validation.yml" && run.Event == "merge_group" {
		run.Category = "merge"
	}
	if run.Workflow == "nightly.yml" && (run.Event == "schedule" || run.Event == "workflow_dispatch") {
		run.Category = "nightly"
	}
	if run.Workflow == "ci.yml" && run.Event == "pull_request" && supported {
		run.Category = "selective"
		if run.Plan.Audit || (run.Plan.PR == nil && reflect.DeepEqual(run.Plan.Effective, FullJobs())) || (run.Plan.PR != nil && reflect.DeepEqual(run.Plan.PR.Effective, FullPRJobs())) {
			run.Category = "full_pr"
		}
	}
	if supported {
		run.ExpectedSource = "plan"
		run.PlannedJobs = expectedPlanJobs(run.Plan)
		run.ExpectedJobs = run.PlannedJobs
		run.SelectionConfidence = "verified"
	} else {
		if run.PlanIssue == "" {
			run.PlanIssue = "missing, empty or unsupported plan"
		}
		run.Problems = append(run.Problems, run.PlanIssue)
	}
	// Exhaustive workflows have an independent required inventory. A partial or
	// historical plan must not redefine their current validation obligations.
	if run.Category == "merge" || run.Category == "nightly" {
		run.ExpectedSource = "workflow_registry"
		run.ExpectedJobs = ExpectedHealthJobs(run.Workflow)
	}
	if run.Category == "unknown" {
		run.Problems = append(run.Problems, "execution category unknown")
	}
	for name, result := range run.Results {
		if strings.HasPrefix(name, "unknown/") || !knownConclusion(result) {
			run.UnknownJobs = append(run.UnknownJobs, name)
		}
		switch {
		case result == "skipped":
			run.SkippedJobs = append(run.SkippedJobs, name)
		case knownConclusion(result):
			run.ExecutedJobs = append(run.ExecutedJobs, name)
		}
	}
	for _, name := range run.ExpectedJobs {
		result, present := run.Results[name]
		if !present {
			run.UnknownJobs = append(run.UnknownJobs, name)
		}
		if result != "success" {
			run.Problems = append(run.Problems, fmt.Sprintf("expected %s: %s", name, result))
		}
	}
	if supported {
		expected := map[string]bool{}
		for _, name := range run.ExpectedJobs {
			expected[name] = true
		}
		for name, result := range run.Results {
			if name != "ci-gate" && !expected[name] && result != "skipped" {
				run.Problems = append(run.Problems, "unplanned execution: "+name)
			}
		}
		if len(run.Problems) > 0 || len(run.UnknownJobs) > 0 {
			run.SelectionConfidence = "incomplete"
		}
	}
	if len(run.UnknownJobs) > 0 {
		run.Problems = append(run.Problems, "unknown or missing job evidence")
	}
	if run.DurationSeconds < 0 {
		run.Problems = append(run.Problems, "duration unavailable")
	}
	if !knownConclusion(run.Conclusion) {
		run.Problems = append(run.Problems, "unknown run conclusion")
	}
	if run.Deferred {
		run.Category = "deferred"
	}
	sort.Strings(run.ExecutedJobs)
	sort.Strings(run.SkippedJobs)
	sort.Strings(run.UnknownJobs)
	sort.Strings(run.Problems)
	return run
}

func AnalyzeHealth(runs []HealthRun) HealthReport {
	report := HealthReport{Version: 2, GeneratedAt: time.Now().UTC(), RunCount: len(runs), Selection: map[string]SelectionMetric{}, Jobs: map[string]JobMetric{}}
	populations := map[string][]int64{}
	var queues []int64
	for _, input := range runs {
		run := classifyHealthRun(input)
		report.Runs = append(report.Runs, run)
		switch run.Conclusion {
		case "success":
			report.Successes++
		case "failure", "timed_out", "action_required", "startup_failure":
			report.Failures++
		case "cancelled":
			report.Cancellations++
		case "skipped":
			report.Skipped++
		default:
			report.UnknownConclusions++
		}
		if len(run.Problems) > 0 {
			report.Incomplete++
		}
		if len(run.PlannedJobs) > 0 {
			report.PlannedRuns++
			selection := run.Plan.Effective.Selected()
			if run.Plan.PR != nil {
				selection = run.Plan.PR.Effective.Selected()
				selection["prepare"] = true
			}
			for job, selected := range selection {
				metric := report.Selection[job]
				if selected {
					metric.Selected++
				}
				report.Selection[job] = metric
			}
			if run.Plan.Audit {
				report.AuditSamples++
				nominal := map[string]bool{}
				nominalPlan := Plan{Effective: run.Plan.Nominal}
				if run.Plan.PR != nil {
					nominalPlan.Version = PRPlanVersion
					nominalPlan.PR = &PRPlan{Effective: run.Plan.PR.Nominal}
				}
				for _, job := range expectedPlanJobs(nominalPlan) {
					nominal[job] = true
				}
				for _, job := range run.PlannedJobs {
					if !nominal[job] && run.Results[job] != "success" {
						report.AuditMisses++
					}
				}
			}
		} else {
			report.UnknownSelection++
		}
		for _, job := range run.ExpectedJobs {
			m := report.Jobs[job]
			m.Expected++
			report.Jobs[job] = m
		}
		for _, job := range run.ExecutedJobs {
			m := report.Jobs[job]
			m.Executed++
			report.Jobs[job] = m
		}
		for _, job := range run.SkippedJobs {
			m := report.Jobs[job]
			m.Skipped++
			report.Jobs[job] = m
		}
		for _, job := range run.UnknownJobs {
			m := report.Jobs[job]
			m.Unknown++
			report.Jobs[job] = m
		}
		if run.Deferred {
			report.Deferred++
			continue
		}
		if run.Attempt > 1 {
			report.Reruns++
		}
		if run.QueueSeconds >= 0 {
			queues = append(queues, run.QueueSeconds)
		}
		if run.DurationSeconds >= 0 {
			populations[run.Category] = append(populations[run.Category], run.DurationSeconds)
		} else {
			report.MissingDurations++
		}
	}
	report.Merge = durationMetric(populations["merge"])
	report.Full = report.Merge
	report.Nightly = durationMetric(populations["nightly"])
	report.FullPR = durationMetric(populations["full_pr"])
	report.Selective = durationMetric(populations["selective"])
	report.Unknown = durationMetric(populations["unknown"])
	report.Queue = durationMetric(queues)
	if executed := len(runs) - report.Deferred; executed > 0 {
		report.RerunPercent = float64(report.Reruns) * 100 / float64(executed)
	}
	if report.PlannedRuns > 0 {
		for job, m := range report.Selection {
			m.Percent = float64(m.Selected) * 100 / float64(report.PlannedRuns)
			report.Selection[job] = m
		}
	}
	for _, population := range []struct {
		name   string
		metric DurationMetric
		limit  time.Duration
	}{
		{"full CI", report.Merge, 12 * time.Minute}, {"nightly CI", report.Nightly, 12 * time.Minute},
		{"full PR", report.FullPR, 12 * time.Minute}, {"unknown CI", report.Unknown, 12 * time.Minute},
		{"selective PR", report.Selective, 6 * time.Minute}, {"queue", report.Queue, 2 * time.Minute},
	} {
		if population.metric.Count > 0 && population.metric.P95Seconds > int64(population.limit.Seconds()) {
			report.Alerts = append(report.Alerts, fmt.Sprintf("%s p95 is %s (limit %s)", population.name, duration(population.metric.P95Seconds), population.limit))
		}
	}
	if report.RerunPercent > 3 {
		report.Alerts = append(report.Alerts, fmt.Sprintf("rerun rate is %.1f%% (limit 3.0%%)", report.RerunPercent))
	}
	if report.AuditMisses > 0 {
		report.Alerts = append(report.Alerts, fmt.Sprintf("selection audit detected %d miss%s", report.AuditMisses, plural(report.AuditMisses)))
	}
	if report.Incomplete > 0 {
		report.Alerts = append(report.Alerts, fmt.Sprintf("%d runs have incomplete reporting evidence; health is not established", report.Incomplete))
	}
	if len(runs) == 0 {
		report.Alerts = append(report.Alerts, "no CI runs available; health is not established")
	}
	return report
}

func durationMetric(values []int64) DurationMetric {
	return DurationMetric{
		Count:      len(values),
		P50Seconds: percentile(values, 0.50),
		P95Seconds: percentile(values, 0.95),
	}
}

func percentile(values []int64, p float64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := int(math.Ceil(p*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	return sorted[index]
}

func duration(seconds int64) time.Duration {
	return time.Duration(seconds) * time.Second
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "es"
}
