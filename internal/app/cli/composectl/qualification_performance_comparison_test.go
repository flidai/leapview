package composectl

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func comparableQualificationPerformanceReport() qualificationPerformanceReport {
	report := completeQualificationLatencyReport()
	report.Policy = validQualificationPerformancePolicy()
	report.Architecture = "amd64"
	report.Environment.Runtime = "Docker Engine 28.5.1"
	report.Environment.LogicalCPUs = report.Policy.Assumptions.MinimumLogicalCPUs
	report.Environment.MemoryBytes = report.Policy.Assumptions.MinimumMemoryBytes
	report.Environment.Dataset = map[string]int64{"orders": 1000}
	return report
}

func TestFinalizeQualificationPerformanceRejectsIncompatibleBaseline(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field string
		edit  func(*qualificationPerformanceReport)
	}{
		{"comparable", "", func(*qualificationPerformanceReport) {}},
		{"schema", "schemaVersion", func(r *qualificationPerformanceReport) { r.SchemaVersion = 2 }},
		{"missing schema", "schemaVersion", func(r *qualificationPerformanceReport) { r.SchemaVersion = 0 }},
		{"workload", "policy", func(r *qualificationPerformanceReport) { r.Policy.Workload = "smaller-workload" }},
		{"missing policy", "policy", func(r *qualificationPerformanceReport) { r.Policy = qualificationPerformancePolicy{} }},
		{"changed absolute limit", "", func(r *qualificationPerformanceReport) { r.Policy.Budgets.CPUSecondsMax++ }},
		{"sampling protocol", "policy", func(r *qualificationPerformanceReport) { r.Policy.Assumptions.Samples.RefreshRuns++ }},
		{"architecture", "architecture", func(r *qualificationPerformanceReport) { r.Architecture = "arm64" }},
		{"missing architecture", "architecture", func(r *qualificationPerformanceReport) { r.Architecture = "" }},
		{"runtime", "runtime", func(r *qualificationPerformanceReport) { r.Environment.Runtime = "Docker Engine 27.0.0" }},
		{"missing runtime", "runtime", func(r *qualificationPerformanceReport) { r.Environment.Runtime = "" }},
		{"CPU allocation", "logicalCPUs", func(r *qualificationPerformanceReport) { r.Environment.LogicalCPUs++ }},
		{"missing CPU allocation", "logicalCPUs", func(r *qualificationPerformanceReport) { r.Environment.LogicalCPUs = 0 }},
		{"memory allocation", "memoryBytes", func(r *qualificationPerformanceReport) { r.Environment.MemoryBytes++ }},
		{"missing memory allocation", "memoryBytes", func(r *qualificationPerformanceReport) { r.Environment.MemoryBytes = 0 }},
		{"dataset count", "dataset", func(r *qualificationPerformanceReport) { r.Environment.Dataset["orders"]++ }},
		{"dataset keys", "dataset", func(r *qualificationPerformanceReport) { r.Environment.Dataset["customers"] = 10 }},
		{"missing dataset", "dataset", func(r *qualificationPerformanceReport) { r.Environment.Dataset = nil }},
		{"empty dataset", "dataset", func(r *qualificationPerformanceReport) { r.Environment.Dataset = map[string]int64{} }},
		{"invalid dataset count", "dataset", func(r *qualificationPerformanceReport) { r.Environment.Dataset["orders"] = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := validQualificationPerformancePolicy()
			candidate := comparableQualificationPerformanceReport()
			baseline := comparableQualificationPerformanceReport()
			tc.edit(&baseline)
			dir := t.TempDir()
			path, baselinePath := filepath.Join(dir, "candidate.json"), filepath.Join(dir, "baseline.json")
			if err := writeQualificationJSON(path, candidate); err != nil {
				t.Fatal(err)
			}
			if err := writeQualificationJSON(baselinePath, baseline); err != nil {
				t.Fatal(err)
			}
			environment, err := json.Marshal(candidate.Environment)
			if err != nil {
				t.Fatal(err)
			}
			err = finalizeQualificationPerformanceReport(path, policy, 0, 0, environment, "candidate-image", candidate.Architecture, baselinePath)
			var finalized qualificationPerformanceReport
			if readErr := readQualificationJSON(path, &finalized); readErr != nil {
				t.Fatal(readErr)
			}
			if tc.field == "" {
				if err != nil || finalized.Result != "success" || !finalized.Assertions.ComparisonTolerance {
					t.Fatalf("comparable report failed: error=%v report=%+v", err, finalized)
				}
				return
			}
			if err == nil || finalized.Result != "failure" || finalized.Assertions.ComparisonTolerance {
				t.Fatalf("incompatible %s accepted: error=%v result=%q comparisonTolerance=%v", tc.name, err, finalized.Result, finalized.Assertions.ComparisonTolerance)
			}
			if !strings.Contains(strings.Join(finalized.Comparison.Failures, "\n"), tc.field) {
				t.Fatalf("comparison diagnostic omitted %s: %v", tc.field, finalized.Comparison.Failures)
			}
			if finalized.Comparison.Baseline == nil || *finalized.Comparison.Baseline != baselinePath {
				t.Fatal("failed comparison lost its baseline reference")
			}
		})
	}
}

func TestQualificationComparisonRejectsMissingSharedIdentity(t *testing.T) {
	for _, tc := range []struct {
		field string
		edit  func(*qualificationPerformanceReport)
	}{
		{"schemaVersion", func(r *qualificationPerformanceReport) { r.SchemaVersion = 0 }},
		{"policy", func(r *qualificationPerformanceReport) { r.Policy = qualificationPerformancePolicy{} }},
		{"architecture", func(r *qualificationPerformanceReport) { r.Architecture = "" }},
		{"runtime", func(r *qualificationPerformanceReport) { r.Environment.Runtime = "" }},
		{"logicalCPUs", func(r *qualificationPerformanceReport) { r.Environment.LogicalCPUs = 0 }},
		{"memoryBytes", func(r *qualificationPerformanceReport) { r.Environment.MemoryBytes = 0 }},
		{"dataset", func(r *qualificationPerformanceReport) { r.Environment.Dataset = nil }},
		{"invalid dataset", func(r *qualificationPerformanceReport) { r.Environment.Dataset["orders"] = -1 }},
	} {
		t.Run(tc.field, func(t *testing.T) {
			candidate, baseline := comparableQualificationPerformanceReport(), comparableQualificationPerformanceReport()
			tc.edit(&candidate)
			tc.edit(&baseline)
			if failures := compareQualificationPerformance(candidate, baseline, validQualificationPerformancePolicy()); len(failures) == 0 {
				t.Fatalf("matching but invalid %s identity accepted", tc.field)
			}
		})
	}
}
