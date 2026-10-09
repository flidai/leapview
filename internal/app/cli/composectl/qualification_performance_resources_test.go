package composectl

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resourceEvidencePointer[T ~int | ~int64 | ~float64](value T) *T { return &value }

func completeQualificationResourceReport(policy qualificationPerformancePolicy) qualificationResourceReport {
	// Synthetic fixture only. Production must retain actual snapshots.
	warm := make([]qualificationResourceMeasurement, qualificationWarmResourceSamples)
	for index := range warm {
		warm[index] = qualificationResourceMeasurement{
			CPUSeconds: resourceEvidencePointer(0.0), ResidentMemoryBytes: resourceEvidencePointer(int64(64)),
			Goroutines: resourceEvidencePointer(int64(10)), OpenConnections: resourceEvidencePointer(int64(1)),
		}
	}
	cold := make([][]qualificationResourceMeasurement, policy.Assumptions.Samples.ColdDashboardLoads)
	for index := range cold {
		cold[index] = append([]qualificationResourceMeasurement(nil), warm[:qualificationColdResourceSamples]...)
	}
	return qualificationResourceReport{
		SchemaVersion:           qualificationResourceSchemaVersion,
		PeakResidentMemoryBytes: resourceEvidencePointer(int64(64)), CPUSeconds: resourceEvidencePointer(0.0),
		GoroutinesBefore: resourceEvidencePointer(int64(10)), GoroutinesAfter: resourceEvidencePointer(int64(10)),
		PeakOpenConnections: resourceEvidencePointer(int64(1)), MetricSamples: resourceEvidencePointer(qualificationWarmResourceSamples),
		Measurements: warm, ColdMeasurements: cold,
	}
}

func qualificationResourceWorkerMap(t *testing.T, policy qualificationPerformancePolicy) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(completeQualificationResourceReport(policy))
	if err != nil {
		t.Fatal(err)
	}
	var worker map[string]any
	if err := json.Unmarshal(encoded, &worker); err != nil {
		t.Fatal(err)
	}
	// These are measured independently by the controller, not supplied by the
	// worker except its old placeholder growth value.
	delete(worker, "temporaryDiskBeforeBytes")
	delete(worker, "temporaryDiskAfterBytes")
	return worker
}

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
		"resources":     qualificationResourceWorkerMap(t, policy),
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
			for _, raw := range resources["measurements"].([]any) {
				sample := raw.(map[string]any)
				sample["cpuSeconds"], sample["openConnections"] = 0, 0
			}
		}},
		{"missing resources", true, func(report map[string]any) { delete(report, "resources") }},
		{"legacy unversioned resources", true, func(report map[string]any) { delete(report["resources"].(map[string]any), "schemaVersion") }},
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

func TestQualificationResourcesRejectInvalidMeasurements(t *testing.T) {
	for _, test := range []struct {
		name  string
		field string
		edit  func(*qualificationResourceReport)
	}{
		{"unknown resource schema", "resources.schemaVersion", func(r *qualificationResourceReport) { r.SchemaVersion = 2 }},
		{"missing warm measurements", "measurements", func(r *qualificationResourceReport) { r.Measurements = nil }},
		{"short warm measurements", "measurements", func(r *qualificationResourceReport) { r.Measurements = r.Measurements[:7] }},
		{"missing cold measurements", "coldMeasurements", func(r *qualificationResourceReport) { r.ColdMeasurements = nil }},
		{"short cold pair", "coldMeasurements[0]", func(r *qualificationResourceReport) { r.ColdMeasurements[0] = r.ColdMeasurements[0][:1] }},
		{"missing CPU", "measurements[0].cpuSeconds", func(r *qualificationResourceReport) { r.Measurements[0].CPUSeconds = nil }},
		{"negative CPU", "measurements[0].cpuSeconds", func(r *qualificationResourceReport) { r.Measurements[0].CPUSeconds = resourceEvidencePointer(-1.0) }},
		{"NaN CPU", "measurements[0].cpuSeconds", func(r *qualificationResourceReport) {
			r.Measurements[0].CPUSeconds = resourceEvidencePointer(math.NaN())
		}},
		{"infinite CPU", "measurements[0].cpuSeconds", func(r *qualificationResourceReport) {
			r.Measurements[0].CPUSeconds = resourceEvidencePointer(math.Inf(1))
		}},
		{"warm process reset", "decreased", func(r *qualificationResourceReport) { r.Measurements[0].CPUSeconds = resourceEvidencePointer(1.0) }},
		{"cold process reset", "coldMeasurements[0][1].cpuSeconds", func(r *qualificationResourceReport) {
			r.ColdMeasurements[0][0].CPUSeconds = resourceEvidencePointer(1.0)
		}},
		{"missing RSS", "residentMemoryBytes", func(r *qualificationResourceReport) { r.Measurements[0].ResidentMemoryBytes = nil }},
		{"zero RSS", "residentMemoryBytes", func(r *qualificationResourceReport) {
			r.Measurements[0].ResidentMemoryBytes = resourceEvidencePointer(int64(0))
		}},
		{"missing goroutines", "goroutines", func(r *qualificationResourceReport) { r.Measurements[0].Goroutines = nil }},
		{"negative goroutines", "goroutines", func(r *qualificationResourceReport) {
			r.Measurements[0].Goroutines = resourceEvidencePointer(int64(-1))
		}},
		{"missing connections", "openConnections", func(r *qualificationResourceReport) { r.Measurements[0].OpenConnections = nil }},
		{"negative connections", "openConnections", func(r *qualificationResourceReport) {
			r.Measurements[0].OpenConnections = resourceEvidencePointer(int64(-1))
		}},
		{"nonfinite summary", "resources.cpuSeconds", func(r *qualificationResourceReport) { r.CPUSeconds = resourceEvidencePointer(math.NaN()) }},
		{"inconsistent RSS", "resources.peakResidentMemoryBytes", func(r *qualificationResourceReport) { r.PeakResidentMemoryBytes = resourceEvidencePointer(int64(1)) }},
		{"inconsistent CPU", "resources.cpuSeconds", func(r *qualificationResourceReport) { r.CPUSeconds = resourceEvidencePointer(1.0) }},
		{"inconsistent goroutines", "resources.goroutinesAfter", func(r *qualificationResourceReport) { r.GoroutinesAfter = resourceEvidencePointer(int64(11)) }},
		{"inconsistent connections", "resources.peakOpenConnections", func(r *qualificationResourceReport) { r.PeakOpenConnections = resourceEvidencePointer(int64(0)) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := validQualificationPerformancePolicy()
			resources := completeQualificationResourceReport(policy)
			test.edit(&resources)
			if failures := validateQualificationResources(resources, policy); !strings.Contains(strings.Join(failures, "; "), test.field) {
				t.Fatalf("invalid resource evidence lacks %s diagnostic: %v", test.field, failures)
			}
			for _, which := range []string{"candidate", "baseline"} {
				candidate, baseline := comparableQualificationPerformanceReport(), comparableQualificationPerformanceReport()
				if which == "candidate" {
					candidate.Resources = resources
				} else {
					baseline.Resources = resources
				}
				failures := compareQualificationPerformance(candidate, baseline, policy)
				if !strings.Contains(strings.Join(failures, "; "), which+" resources") {
					t.Fatalf("incomplete %s used for comparison: %v", which, failures)
				}
			}
		})
	}
}

func TestQualificationResourcesAggregateSeparateProcessMeasurements(t *testing.T) {
	policy := validQualificationPerformancePolicy()
	resources := completeQualificationResourceReport(policy)
	for index := range resources.Measurements {
		resources.Measurements[index] = qualificationResourceMeasurement{
			CPUSeconds:          resourceEvidencePointer(20 + float64(index)*0.25),
			ResidentMemoryBytes: resourceEvidencePointer(int64(64 + index)),
			Goroutines:          resourceEvidencePointer(int64(10 + index)), OpenConnections: resourceEvidencePointer(int64(index % 3)),
		}
	}
	resources.ColdMeasurements[0][0].CPUSeconds = resourceEvidencePointer(0.0)
	resources.ColdMeasurements[0][1].CPUSeconds = resourceEvidencePointer(0.126)
	resources.ColdMeasurements[0][1].ResidentMemoryBytes = resourceEvidencePointer(int64(90))
	resources.CPUSeconds = resourceEvidencePointer(1.88)
	resources.PeakResidentMemoryBytes = resourceEvidencePointer(int64(90))
	resources.GoroutinesAfter = resourceEvidencePointer(int64(17))
	resources.PeakOpenConnections = resourceEvidencePointer(int64(2))
	if failures := validateQualificationResources(resources, policy); len(failures) != 0 {
		t.Fatalf("independent process baselines or existing rounding rejected: %v", failures)
	}
}

func TestQualificationResourceEvidencePreservesMeasurementAndControllerInputs(t *testing.T) {
	policy, worker := qualificationResourceWorkerEvidence(t)
	resources := worker["resources"].(map[string]any)
	// A cold process can start its own counter at zero while the warm process
	// starts at an independent higher value. Only within-process resets fail.
	for _, raw := range resources["measurements"].([]any) {
		raw.(map[string]any)["cpuSeconds"] = 20.0
	}
	path := filepath.Join(t.TempDir(), "performance-report.json")
	if err := writeQualificationJSON(path, worker); err != nil {
		t.Fatal(err)
	}
	environment, _ := json.Marshal(map[string]any{"logicalCPUs": policy.Assumptions.MinimumLogicalCPUs, "memoryBytes": policy.Assumptions.MinimumMemoryBytes})
	if err := finalizeQualificationPerformanceReport(path, policy, 100, 103, environment, "controller-image", "amd64", ""); err != nil {
		t.Fatal(err)
	}
	var finalized qualificationPerformanceReport
	if err := readQualificationJSON(path, &finalized); err != nil {
		t.Fatal(err)
	}
	if len(finalized.Resources.Measurements) != 8 || *finalized.Resources.Measurements[0].CPUSeconds != 20 || len(finalized.Resources.ColdMeasurements) != policy.Assumptions.Samples.ColdDashboardLoads {
		t.Fatalf("actual resource snapshots dropped: %+v", finalized.Resources)
	}
	if finalized.Resources.TemporaryDiskBeforeBytes != 100 || finalized.Resources.TemporaryDiskGrowthBytes != 3 || finalized.Image != "controller-image" {
		t.Fatalf("controller inputs changed: %+v", finalized)
	}
}

func TestFinalizeQualificationPerformanceRejectsIncompleteBaselineResources(t *testing.T) {
	policy, candidate := qualificationResourceWorkerEvidence(t)
	_, baseline := qualificationResourceWorkerEvidence(t)
	delete(baseline["resources"].(map[string]any), "metricSamples")
	environment := map[string]any{
		"runtime": "Docker Engine test", "logicalCPUs": policy.Assumptions.MinimumLogicalCPUs,
		"memoryBytes": policy.Assumptions.MinimumMemoryBytes, "dataset": map[string]int{"orders": 24},
	}
	baseline["environment"], baseline["architecture"], baseline["image"] = environment, "amd64", "reference-fixture-image"
	dir := t.TempDir()
	path, baselinePath := filepath.Join(dir, "candidate.json"), filepath.Join(dir, "baseline.json")
	for path, report := range map[string]map[string]any{path: candidate, baselinePath: baseline} {
		if err := writeQualificationJSON(path, report); err != nil {
			t.Fatal(err)
		}
	}
	environmentJSON, err := json.Marshal(environment)
	if err != nil {
		t.Fatal(err)
	}
	err = finalizeQualificationPerformanceReport(path, policy, 0, 0, environmentJSON, "candidate-fixture-image", "amd64", baselinePath)
	if err == nil || !strings.Contains(err.Error(), "baseline resources.metricSamples") {
		t.Fatalf("incomplete baseline was compared or lacked attribution: %v", err)
	}
	var finalized qualificationPerformanceReport
	if err := readQualificationJSON(path, &finalized); err != nil {
		t.Fatal(err)
	}
	if finalized.Result != "failure" || finalized.Assertions.ComparisonTolerance {
		t.Fatalf("incomplete baseline reported comparison success: %+v", finalized)
	}
}
