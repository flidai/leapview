package composectl

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func setQualificationRawSamples(t *testing.T, report *qualificationPerformanceReport, field string, values []float64) {
	t.Helper()
	var samples map[string][]float64
	if err := json.Unmarshal(report.Samples, &samples); err != nil {
		t.Fatal(err)
	}
	samples[field] = values
	var err error
	report.Samples, err = json.Marshal(samples)
	if err != nil {
		t.Fatal(err)
	}
}

func TestFinalizeQualificationPerformanceAdmitsOnlyMeasuredEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		edit        func(map[string]any)
	}{
		{"complete worker", "", func(map[string]any) {}},
		{"worker budgets are not authority", "", func(r map[string]any) {
			p := r["policy"].(qualificationPerformancePolicy)
			p.Budgets.PeakResidentMemoryBytes = 0
			r["policy"] = p
		}},
		{"missing samples", "samples", func(r map[string]any) { delete(r, "samples") }},
		{"null samples", "samples", func(r map[string]any) { r["samples"] = nil }},
		{"nonobject samples", "samples", func(r map[string]any) { r["samples"] = []any{} }},
		{"missing phase", "samples.governedQueryMs", func(r map[string]any) { delete(r["samples"].(map[string]any), "governedQueryMs") }},
		{"null phase", "samples.governedQueryMs", func(r map[string]any) { r["samples"].(map[string]any)["governedQueryMs"] = nil }},
		{"empty phase", "samples.governedQueryMs", func(r map[string]any) { r["samples"].(map[string]any)["governedQueryMs"] = []float64{} }},
		{"short phase", "samples.governedQueryMs", func(r map[string]any) { r["samples"].(map[string]any)["governedQueryMs"] = []float64{3} }},
		{"extra phase samples", "samples.governedQueryMs", func(r map[string]any) {
			r["samples"].(map[string]any)["governedQueryMs"] = append(r["samples"].(map[string]any)["governedQueryMs"].([]float64), 3)
		}},
		{"nonarray phase", "samples.governedQueryMs", func(r map[string]any) { r["samples"].(map[string]any)["governedQueryMs"] = 3 }},
		{"null duration", "samples.governedQueryMs[0]", func(r map[string]any) {
			values := make([]any, 10)
			for i := range values {
				values[i] = 3
			}
			values[0] = nil
			r["samples"].(map[string]any)["governedQueryMs"] = values
		}},
		{"string duration", "samples.governedQueryMs", func(r map[string]any) { r["samples"].(map[string]any)["governedQueryMs"] = []string{"3"} }},
		{"nonfinite duration", "samples.governedQueryMs", func(r map[string]any) {
			r["samples"].(map[string]any)["governedQueryMs"] = json.RawMessage(`[1e999,3,3,3,3,3,3,3,3,3]`)
		}},
		{"negative duration", "samples.governedQueryMs[0]", func(r map[string]any) { r["samples"].(map[string]any)["governedQueryMs"].([]float64)[0] = -1 }},
		{"p50 mismatch", "latency.governedQueryMs.p50", func(r map[string]any) {
			r["latency"].(map[string]any)["governedQueryMs"] = qualificationDurationSummary{Samples: 10, P50: 2, P95: 3, Max: 3}
		}},
		{"p95 mismatch", "latency.governedQueryMs.p95", func(r map[string]any) {
			r["latency"].(map[string]any)["governedQueryMs"] = qualificationDurationSummary{Samples: 10, P50: 3, P95: 4, Max: 4}
		}},
		{"max mismatch", "latency.governedQueryMs.max", func(r map[string]any) {
			r["latency"].(map[string]any)["governedQueryMs"] = qualificationDurationSummary{Samples: 10, P50: 3, P95: 3, Max: 4}
		}},
		{"short concurrent samples", "samples.concurrentQueryMs", func(r map[string]any) { r["samples"].(map[string]any)["concurrentQueryMs"] = []float64{3} }},
		{"missing concurrency", "concurrency", func(r map[string]any) { delete(r, "concurrency") }},
		{"null concurrency", "concurrency", func(r map[string]any) { r["concurrency"] = nil }},
		{"wrong readers", "concurrency.readers", func(r map[string]any) { r["concurrency"].(map[string]any)["readers"] = 1 }},
		{"null readers", "concurrency.readers", func(r map[string]any) { r["concurrency"].(map[string]any)["readers"] = nil }},
		{"nonnumeric readers", "concurrency.readers", func(r map[string]any) { r["concurrency"].(map[string]any)["readers"] = "8" }},
		{"missing wave", "concurrency.waveMs", func(r map[string]any) { delete(r["concurrency"].(map[string]any), "waveMs") }},
		{"null wave", "concurrency.waveMs", func(r map[string]any) { r["concurrency"].(map[string]any)["waveMs"] = nil }},
		{"nonnumeric wave", "concurrency.waveMs", func(r map[string]any) { r["concurrency"].(map[string]any)["waveMs"] = "3" }},
		{"negative wave", "concurrency.waveMs", func(r map[string]any) { r["concurrency"].(map[string]any)["waveMs"] = -1 }},
		{"short wave", "concurrency.waveMs", func(r map[string]any) { r["concurrency"].(map[string]any)["waveMs"] = 2 }},
		{"negative errors", "reliability.errors", func(r map[string]any) { r["reliability"].(map[string]any)["errors"] = -1 }},
		{"missing errors", "reliability.errors", func(r map[string]any) { delete(r["reliability"].(map[string]any), "errors") }},
		{"null errors", "reliability.errors", func(r map[string]any) { r["reliability"].(map[string]any)["errors"] = nil }},
		{"missing requests", "reliability.requests", func(r map[string]any) { delete(r["reliability"].(map[string]any), "requests") }},
		{"null requests", "reliability.requests", func(r map[string]any) { r["reliability"].(map[string]any)["requests"] = nil }},
		{"negative requests", "reliability.requests", func(r map[string]any) { r["reliability"].(map[string]any)["requests"] = -1 }},
		{"insufficient requests", "reliability.requests", func(r map[string]any) { r["reliability"].(map[string]any)["requests"] = 42 }},
		{"missing schema", "schemaVersion", func(r map[string]any) { delete(r, "schemaVersion") }},
		{"unknown schema", "schemaVersion", func(r map[string]any) { r["schemaVersion"] = 2 }},
		{"missing protocol", "policy", func(r map[string]any) { delete(r, "policy") }},
		{"wrong workload", "policy.workload", func(r map[string]any) {
			p := r["policy"].(qualificationPerformancePolicy)
			p.Workload = "different"
			r["policy"] = p
		}},
		{"wrong protocol", "policy.assumptions.samples", func(r map[string]any) {
			p := r["policy"].(qualificationPerformancePolicy)
			p.Assumptions.Samples.GovernedQueries++
			r["policy"] = p
		}},
	} {
		for _, which := range []string{"candidate", "baseline"} {
			t.Run(which+"/"+tc.name, func(t *testing.T) {
				policy, worker := qualificationResourceWorkerEvidence(t)
				dir := t.TempDir()
				path, baselinePath := filepath.Join(dir, "candidate.json"), ""
				environment, _ := json.Marshal(map[string]any{"runtime": "Docker Engine test", "logicalCPUs": policy.Assumptions.MinimumLogicalCPUs, "memoryBytes": policy.Assumptions.MinimumMemoryBytes, "dataset": map[string]int64{"orders": 24}})
				if which == "baseline" {
					baselinePath = filepath.Join(dir, "baseline.json")
					if err := writeQualificationJSON(baselinePath, worker); err != nil {
						t.Fatal(err)
					}
					if err := finalizeQualificationPerformanceReport(baselinePath, policy, 0, 0, environment, "baseline-image", "amd64", ""); err != nil {
						t.Fatal(err)
					}
					var finalized map[string]any
					if err := readQualificationJSON(baselinePath, &finalized); err != nil {
						t.Fatal(err)
					}
					// Restore typed fixture fields used by mutations; all finalized outcome fields remain.
					finalized["policy"], finalized["latency"], finalized["samples"] = worker["policy"], worker["latency"], worker["samples"]
					tc.edit(finalized)
					if err := writeQualificationJSON(baselinePath, finalized); err != nil {
						t.Fatal(err)
					}
					_, worker = qualificationResourceWorkerEvidence(t)
				} else {
					tc.edit(worker)
				}
				if err := writeQualificationJSON(path, worker); err != nil {
					t.Fatal(err)
				}
				err := finalizeQualificationPerformanceReport(path, policy, 0, 0, environment, "candidate-image", "amd64", baselinePath)
				var report qualificationPerformanceReport
				if readErr := readQualificationJSON(path, &report); readErr != nil {
					t.Fatal(readErr)
				}
				if tc.field == "" {
					if err != nil || report.Result != "success" {
						t.Fatalf("valid worker failed: %v %+v", err, report)
					}
					return
				}
				if err == nil || report.Result != "failure" {
					t.Fatalf("invalid evidence admitted: error=%v result=%q", err, report.Result)
				}
				if !strings.Contains(strings.Join(report.Failures, "\n"), tc.field) {
					t.Fatalf("missing %s diagnostic: %v", tc.field, report.Failures)
				}
				if which == "baseline" && (report.Assertions.ComparisonTolerance || !strings.Contains(strings.Join(report.Comparison.Failures, "\n"), tc.field)) {
					t.Fatalf("invalid baseline compared: %+v", report.Comparison)
				}
			})
		}
	}
}

func TestFinalizeQualificationPerformancePreservesProducerSampleSemantics(t *testing.T) {
	for _, zero := range []bool{false, true} {
		policy, worker := qualificationResourceWorkerEvidence(t)
		values := []float64{9.999, 1.004, 4.004, 2.004, 3.004, 6.004, 5.004, 8.004, 7.004, 0}
		summary := qualificationDurationSummary{Samples: 10, P50: 4, P95: 10, Max: 10}
		if zero {
			values = make([]float64, 10)
			summary = qualificationDurationSummary{Samples: 10}
			worker["samples"].(map[string]any)["concurrentQueryMs"] = make([]float64, 8)
			worker["latency"].(map[string]any)["concurrentQueryMs"] = qualificationDurationSummary{Samples: 8}
			worker["concurrency"].(map[string]any)["waveMs"] = 0
		}
		worker["samples"].(map[string]any)["governedQueryMs"] = values
		worker["latency"].(map[string]any)["governedQueryMs"] = summary
		worker["reliability"].(map[string]any)["requests"] = 43 // One refresh-create and at least one refresh-poll per measured refresh.
		path := filepath.Join(t.TempDir(), "worker.json")
		if err := writeQualificationJSON(path, worker); err != nil {
			t.Fatal(err)
		}
		environment, _ := json.Marshal(map[string]any{"logicalCPUs": policy.Assumptions.MinimumLogicalCPUs, "memoryBytes": policy.Assumptions.MinimumMemoryBytes})
		if err := finalizeQualificationPerformanceReport(path, policy, 0, 0, environment, "image", "amd64", ""); err != nil {
			t.Fatalf("producer semantics changed (zero=%v): %v", zero, err)
		}
	}
}

func TestQualificationPerformanceReliabilityCountsObservations(t *testing.T) {
	report := completeQualificationLatencyReport()
	report.Reliability.Errors = resourceEvidencePointer(200)
	if failures := validateQualificationPerformanceMeasurements(report, report.Policy); len(failures) != 0 {
		t.Fatalf("browser error observations were incorrectly bounded by controlled operations: %v", failures)
	}
	if failures := evaluateQualificationPerformance(report, report.Policy); !strings.Contains(strings.Join(failures, "\n"), "request error rate") {
		t.Fatalf("measured errors evaded the unchanged zero-error budget: %v", failures)
	}
}
