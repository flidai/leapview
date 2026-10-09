package composectl

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func completeQualificationLatencyReport() qualificationPerformanceReport {
	report := qualificationPerformanceReport{
		SchemaVersion: 1,
		Latency:       make(map[string]qualificationDurationSummary),
	}
	for _, phase := range qualificationLatencyPhases {
		report.Latency[phase.Field] = qualificationDurationSummary{Samples: 1, P50: 3, P95: 3, Max: 3}
	}
	report.Reliability.Requests = 1
	report.Resources = completeQualificationResourceReport(validQualificationPerformancePolicy())
	return report
}

func TestQualificationPerformanceRejectsIncompleteLatencyEvidence(t *testing.T) {
	policy := validQualificationPerformancePolicy()
	if failures := evaluateQualificationPerformance(completeQualificationLatencyReport(), policy); len(failures) != 0 {
		t.Fatalf("complete report failed: %v", failures)
	}
	for _, phase := range qualificationLatencyPhases {
		t.Run("missing/"+phase.Field, func(t *testing.T) {
			report := completeQualificationLatencyReport()
			delete(report.Latency, phase.Field)
			if failures := evaluateQualificationPerformance(report, policy); !strings.Contains(strings.Join(failures, "\n"), phase.Field) {
				t.Fatalf("missing %s passed without actionable failure: %v", phase.Field, failures)
			}
		})
		for _, count := range []int{-1, 0, 2} {
			t.Run(phase.Field+"/sample-count/"+strconv.Itoa(count), func(t *testing.T) {
				report := completeQualificationLatencyReport()
				summary := report.Latency[phase.Field]
				summary.Samples = count
				report.Latency[phase.Field] = summary
				if failures := evaluateQualificationPerformance(report, policy); !strings.Contains(strings.Join(failures, "\n"), phase.Field) {
					t.Fatalf("sample count %d passed for %s: %v", count, phase.Field, failures)
				}
			})
		}
	}
}

func TestQualificationPerformanceRejectsInvalidDurations(t *testing.T) {
	for _, field := range []string{"p50", "p95", "max"} {
		for _, value := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1)} {
			t.Run(field, func(t *testing.T) {
				policy := validQualificationPerformancePolicy()
				report := completeQualificationLatencyReport()
				summary := report.Latency["governedQueryMs"]
				switch field {
				case "p50":
					summary.P50 = value
				case "p95":
					summary.P95 = value
				case "max":
					summary.Max = value
				}
				report.Latency["governedQueryMs"] = summary
				failures := evaluateQualificationPerformance(report, policy)
				if !strings.Contains(strings.Join(failures, "\n"), "governedQueryMs") {
					t.Fatalf("invalid %s=%v lacked a phase-specific diagnostic: %v", field, value, failures)
				}
			})
		}
	}
	for _, summary := range []qualificationDurationSummary{
		{Samples: 1, P50: 5, P95: 3, Max: 6},
		{Samples: 1, P50: 2, P95: 5, Max: 4},
	} {
		policy := validQualificationPerformancePolicy()
		report := completeQualificationLatencyReport()
		report.Latency["governedQueryMs"] = summary
		if failures := evaluateQualificationPerformance(report, policy); !strings.Contains(strings.Join(failures, "\n"), "governedQueryMs") {
			t.Fatalf("unordered summary passed: %+v; failures: %v", summary, failures)
		}
	}
}

func TestFinalizeQualificationPerformanceDoesNotClaimUnperformedComparison(t *testing.T) {
	policy := validQualificationPerformancePolicy()
	path := filepath.Join(t.TempDir(), "report.json")
	input := completeQualificationLatencyReport()
	staleBaseline := "previous-run.json"
	input.Comparison.Baseline = &staleBaseline
	if err := writeQualificationJSON(path, input); err != nil {
		t.Fatal(err)
	}
	environment, err := json.Marshal(map[string]any{
		"logicalCPUs": policy.Assumptions.MinimumLogicalCPUs,
		"memoryBytes": policy.Assumptions.MinimumMemoryBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := finalizeQualificationPerformanceReport(path, policy, 0, 0, environment, "test-image", "amd64", ""); err != nil {
		t.Fatal(err)
	}
	var report qualificationPerformanceReport
	if err := readQualificationJSON(path, &report); err != nil {
		t.Fatal(err)
	}
	if report.Result != "success" || !report.Assertions.AbsoluteBudgets || report.Assertions.ComparisonTolerance || report.Comparison.Baseline != nil {
		t.Fatalf("absolute-only evidence must not claim a baseline comparison: %+v", report)
	}
}

func TestQualificationComparisonDoesNotSkipMeasuredZeroBaseline(t *testing.T) {
	policy := validQualificationPerformancePolicy()
	baseline := finalizedQualificationPerformanceBaseline(t)
	candidate := comparableQualificationPerformanceReport()
	baseline.Latency["governedQueryMs"] = qualificationDurationSummary{Samples: 1}
	candidate.Latency["governedQueryMs"] = qualificationDurationSummary{Samples: 1, P50: 100, P95: 100, Max: 100}
	if failures := compareQualificationPerformance(candidate, baseline, policy); len(failures) != 1 || !strings.Contains(failures[0], "governed query") {
		t.Fatalf("regression from a measured zero was skipped: %v", failures)
	}
}

func TestQualificationComparisonRejectsMissingBaselineOrCandidatePhase(t *testing.T) {
	policy := validQualificationPerformancePolicy()
	for _, which := range []string{"baseline", "candidate"} {
		t.Run(which, func(t *testing.T) {
			candidate := comparableQualificationPerformanceReport()
			baseline := finalizedQualificationPerformanceBaseline(t)
			if which == "baseline" {
				delete(baseline.Latency, "governedQueryMs")
			} else {
				delete(candidate.Latency, "governedQueryMs")
			}
			failures := compareQualificationPerformance(candidate, baseline, policy)
			joined := strings.Join(failures, "\n")
			if !strings.Contains(joined, which) || !strings.Contains(joined, "governedQueryMs") {
				t.Fatalf("incomplete %s silently compared: %v", which, failures)
			}
		})
	}
}
