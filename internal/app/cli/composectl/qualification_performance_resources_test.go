package composectl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Exercise the real worker-file -> controller-finalizer -> persisted-report
// boundary. This fixture uses the shipped policy and the current worker's
// resource fields; disk, environment and image remain controller-owned inputs.
func qualificationResourceWorkerEvidence(t *testing.T) (qualificationPerformancePolicy, map[string]any) {
	t.Helper()
	var policy qualificationPerformancePolicy
	if err := readQualificationJSON(filepath.Join("..", "..", "..", "..", "deploy", "compose", "qualification", "performance-policy.json"), &policy); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{
		"coldDashboardReadyMs": policy.Assumptions.Samples.ColdDashboardLoads,
		"warmDashboardReadyMs": policy.Assumptions.Samples.WarmDashboardLoads,
		"filterToSettleMs":     policy.Assumptions.Samples.FilterInteractions,
		"tableInteractionMs":   policy.Assumptions.Samples.TableInteractions,
		"governedQueryMs":      policy.Assumptions.Samples.GovernedQueries,
		"refreshMs":            policy.Assumptions.Samples.RefreshRuns,
		"concurrentQueryMs":    policy.Assumptions.Samples.ConcurrentReaders,
	}
	latency, samples := make(map[string]any), make(map[string]any)
	for field, count := range counts {
		values := make([]float64, count)
		for index := range values {
			values[index] = 3
		}
		latency[field] = qualificationDurationSummary{Samples: count, P50: 3, P95: 3, Max: 3}
		samples[field] = values
	}
	return policy, map[string]any{
		"schemaVersion": 1,
		"generatedAt":   "2026-10-09T00:00:00Z",
		"policy":        policy,
		"latency":       latency,
		"samples":       samples,
		"concurrency":   map[string]any{"readers": policy.Assumptions.Samples.ConcurrentReaders, "waveMs": 3},
		"reliability":   map[string]any{"requests": 100, "errors": 0, "failures": []string{}},
		"resources": map[string]any{
			"peakResidentMemoryBytes":  int64(64 << 20),
			"cpuSeconds":               1.25,
			"temporaryDiskGrowthBytes": 0,
			"goroutinesBefore":         10,
			"goroutinesAfter":          11,
			"peakOpenConnections":      1,
			"metricSamples":            8,
		},
	}
}

func TestFinalizeQualificationPerformanceResourceEvidence(t *testing.T) {
	for _, test := range []struct {
		name string
		bad  bool
		edit func(map[string]any)
	}{
		{"complete worker evidence", false, func(map[string]any) {}},
		{"observed zero CPU and connections", false, func(report map[string]any) {
			resources := report["resources"].(map[string]any)
			resources["cpuSeconds"], resources["peakOpenConnections"] = 0, 0
		}},
		{"missing resources", true, func(report map[string]any) { delete(report, "resources") }},
		{"null resources", true, func(report map[string]any) { report["resources"] = nil }},
		{"missing RSS", true, func(report map[string]any) { delete(report["resources"].(map[string]any), "peakResidentMemoryBytes") }},
		{"null RSS", true, func(report map[string]any) { report["resources"].(map[string]any)["peakResidentMemoryBytes"] = nil }},
		{"zero RSS", true, func(report map[string]any) { report["resources"].(map[string]any)["peakResidentMemoryBytes"] = 0 }},
		{"negative RSS", true, func(report map[string]any) { report["resources"].(map[string]any)["peakResidentMemoryBytes"] = -1 }},
		{"missing CPU", true, func(report map[string]any) { delete(report["resources"].(map[string]any), "cpuSeconds") }},
		{"null CPU", true, func(report map[string]any) { report["resources"].(map[string]any)["cpuSeconds"] = nil }},
		{"negative CPU", true, func(report map[string]any) { report["resources"].(map[string]any)["cpuSeconds"] = -1 }},
		{"missing initial goroutines", true, func(report map[string]any) { delete(report["resources"].(map[string]any), "goroutinesBefore") }},
		{"missing final goroutines", true, func(report map[string]any) { delete(report["resources"].(map[string]any), "goroutinesAfter") }},
		{"negative goroutines", true, func(report map[string]any) { report["resources"].(map[string]any)["goroutinesAfter"] = -1 }},
		{"missing connections", true, func(report map[string]any) { delete(report["resources"].(map[string]any), "peakOpenConnections") }},
		{"negative connections", true, func(report map[string]any) { report["resources"].(map[string]any)["peakOpenConnections"] = -1 }},
		{"missing metric samples", true, func(report map[string]any) { delete(report["resources"].(map[string]any), "metricSamples") }},
		{"zero metric samples", true, func(report map[string]any) { report["resources"].(map[string]any)["metricSamples"] = 0 }},
		{"negative metric samples", true, func(report map[string]any) { report["resources"].(map[string]any)["metricSamples"] = -1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, worker := qualificationResourceWorkerEvidence(t)
			test.edit(worker)
			path := filepath.Join(t.TempDir(), "performance-report.json")
			input, err := json.Marshal(worker)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, input, 0o600); err != nil {
				t.Fatal(err)
			}
			environment, err := json.Marshal(map[string]any{
				"runtime": "Docker Engine test", "logicalCPUs": policy.Assumptions.MinimumLogicalCPUs,
				"memoryBytes": policy.Assumptions.MinimumMemoryBytes, "dataset": map[string]int{"orders": 24},
			})
			if err != nil {
				t.Fatal(err)
			}
			err = finalizeQualificationPerformanceReport(path, policy, 100, 100, environment, "qualified-fixture-image", "amd64", "")
			var report qualificationPerformanceReport
			if readErr := readQualificationJSON(path, &report); readErr != nil {
				t.Fatal(readErr)
			}
			if test.bad {
				if err == nil || report.Result != "failure" || report.Assertions.AbsoluteBudgets {
					t.Fatalf("invalid worker resource evidence qualified: error=%v result=%q absoluteBudgets=%v resources=%+v", err, report.Result, report.Assertions.AbsoluteBudgets, report.Resources)
				}
			} else if err != nil || report.Result != "success" || !report.Assertions.AbsoluteBudgets || report.Assertions.ComparisonTolerance {
				t.Fatalf("complete absolute-only worker evidence rejected or mislabeled: error=%v result=%q assertions=%+v", err, report.Result, report.Assertions)
			}
		})
	}
}
