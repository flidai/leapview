package composectl

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFinalizeQualificationPerformanceReportBootstrapPassesCompleteEvidence(t *testing.T) {
	t.Setenv("QUALIFICATION_PERFORMANCE_MODE", qualificationPerformanceModeBootstrap)
	t.Setenv("QUALIFICATION_PERFORMANCE_BASELINE", "")
	policy := validQualificationPerformancePolicy()
	path := writeCompleteQualificationPerformanceReport(t, policy, "sha256:"+strings.Repeat("a", 64))
	if err := finalizeQualificationPerformanceReport(path, policy, 100, 100, completePerformanceEnvironment(policy), "image@sha256:"+strings.Repeat("b", 64), "amd64", "", completePerformanceMetadata()); err != nil {
		t.Fatalf("bootstrap finalization failed: %v", err)
	}
	var report qualificationPerformanceReport
	if err := readQualificationJSON(path, &report); err != nil {
		t.Fatal(err)
	}
	if report.Result != "success" || !report.Assertions.EvidenceIdentity || report.Comparison.Mode != qualificationPerformanceModeBootstrap {
		t.Fatalf("bootstrap report = %+v", report)
	}
}

func TestFinalizeQualificationPerformanceReportRequiresExplicitMode(t *testing.T) {
	t.Setenv("QUALIFICATION_PERFORMANCE_MODE", "")
	t.Setenv("QUALIFICATION_PERFORMANCE_BASELINE", "")
	policy := validQualificationPerformancePolicy()
	path := writeCompleteQualificationPerformanceReport(t, policy, "sha256:"+strings.Repeat("a", 64))
	if err := finalizeQualificationPerformanceReport(path, policy, 100, 100, completePerformanceEnvironment(policy), "image", "amd64", "", completePerformanceMetadata()); err == nil || !strings.Contains(err.Error(), "mode must be bootstrap or compare") {
		t.Fatalf("unset mode error = %v", err)
	}
}

func TestFinalizeQualificationPerformanceReportAllowsRefreshPollingRequests(t *testing.T) {
	t.Setenv("QUALIFICATION_PERFORMANCE_MODE", qualificationPerformanceModeBootstrap)
	policy := validQualificationPerformancePolicy()
	path := writeCompleteQualificationPerformanceReport(t, policy, "sha256:"+strings.Repeat("a", 64))
	var report qualificationPerformanceReport
	if err := readQualificationJSON(path, &report); err != nil {
		t.Fatal(err)
	}
	report.Reliability.Requests += 3
	if err := writeQualificationJSON(path, report); err != nil {
		t.Fatal(err)
	}
	if err := finalizeQualificationPerformanceReport(path, policy, 100, 100, completePerformanceEnvironment(policy), "image", "amd64", "", completePerformanceMetadata()); err != nil {
		t.Fatalf("refresh polling finalization failed: %v", err)
	}
	if err := readQualificationJSON(path, &report); err != nil {
		t.Fatal(err)
	}
	if report.Reliability.Operations != qualificationPerformanceExpectedOperations(policy) || report.Reliability.Requests <= report.Reliability.Operations {
		t.Fatalf("reliability accounting = %+v", report.Reliability)
	}
}

func TestFinalizeQualificationPerformanceReportRejectsMissingOperations(t *testing.T) {
	t.Setenv("QUALIFICATION_PERFORMANCE_MODE", qualificationPerformanceModeBootstrap)
	policy := validQualificationPerformancePolicy()
	path := writeCompleteQualificationPerformanceReport(t, policy, "sha256:"+strings.Repeat("a", 64))
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(contents, &root); err != nil {
		t.Fatal(err)
	}
	var reliability map[string]json.RawMessage
	if err := json.Unmarshal(root["reliability"], &reliability); err != nil {
		t.Fatal(err)
	}
	delete(reliability, "operations")
	root["reliability"], _ = json.Marshal(reliability)
	contents, _ = json.Marshal(root)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := finalizeQualificationPerformanceReport(path, policy, 100, 100, completePerformanceEnvironment(policy), "image", "amd64", "", completePerformanceMetadata()); err == nil || !strings.Contains(err.Error(), "reliability.operations is missing") {
		t.Fatalf("missing operations error = %v", err)
	}
}

func TestFinalizeQualificationPerformanceReportRejectsMissingReliabilityFailures(t *testing.T) {
	t.Setenv("QUALIFICATION_PERFORMANCE_MODE", qualificationPerformanceModeBootstrap)
	policy := validQualificationPerformancePolicy()
	path := writeCompleteQualificationPerformanceReport(t, policy, "sha256:"+strings.Repeat("a", 64))
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(contents, &root); err != nil {
		t.Fatal(err)
	}
	var reliability map[string]json.RawMessage
	if err := json.Unmarshal(root["reliability"], &reliability); err != nil {
		t.Fatal(err)
	}
	delete(reliability, "failures")
	root["reliability"], _ = json.Marshal(reliability)
	contents, _ = json.Marshal(root)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := finalizeQualificationPerformanceReport(path, policy, 100, 100, completePerformanceEnvironment(policy), "image", "amd64", "", completePerformanceMetadata()); err == nil || !strings.Contains(err.Error(), "reliability.failures is missing") {
		t.Fatalf("missing failures error = %v", err)
	}
}

func TestFinalizeQualificationPerformanceReportRejectsMalformedReliabilityFailures(t *testing.T) {
	t.Setenv("QUALIFICATION_PERFORMANCE_MODE", qualificationPerformanceModeBootstrap)
	policy := validQualificationPerformancePolicy()
	path := writeCompleteQualificationPerformanceReport(t, policy, "sha256:"+strings.Repeat("a", 64))
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(contents, &root); err != nil {
		t.Fatal(err)
	}
	var reliability map[string]json.RawMessage
	if err := json.Unmarshal(root["reliability"], &reliability); err != nil {
		t.Fatal(err)
	}
	reliability["failures"] = json.RawMessage(`null`)
	root["reliability"], _ = json.Marshal(reliability)
	contents, _ = json.Marshal(root)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := finalizeQualificationPerformanceReport(path, policy, 100, 100, completePerformanceEnvironment(policy), "image", "amd64", "", completePerformanceMetadata()); err == nil || !strings.Contains(err.Error(), "reliability.failures is missing") {
		t.Fatalf("malformed failures error = %v", err)
	}
}

func TestFinalizeQualificationPerformanceReportRejectsMissingEvidence(t *testing.T) {
	t.Setenv("QUALIFICATION_PERFORMANCE_MODE", qualificationPerformanceModeBootstrap)
	policy := validQualificationPerformancePolicy()
	path := writeCompleteQualificationPerformanceReport(t, policy, "sha256:"+strings.Repeat("a", 64))
	var report qualificationPerformanceReport
	if err := readQualificationJSON(path, &report); err != nil {
		t.Fatal(err)
	}
	delete(report.Latency, "warmDashboardReadyMs")
	if err := writeQualificationJSON(path, report); err != nil {
		t.Fatal(err)
	}
	if err := finalizeQualificationPerformanceReport(path, policy, 100, 100, completePerformanceEnvironment(policy), "image", "amd64", "", completePerformanceMetadata()); err == nil || !strings.Contains(err.Error(), "warm dashboard readiness latency summary is missing") {
		t.Fatalf("missing evidence error = %v", err)
	}
}

func TestFinalizeQualificationPerformanceReportRejectsMalformedIdentity(t *testing.T) {
	t.Setenv("QUALIFICATION_PERFORMANCE_MODE", qualificationPerformanceModeBootstrap)
	policy := validQualificationPerformancePolicy()
	path := writeCompleteQualificationPerformanceReport(t, policy, "sha256:"+strings.Repeat("a", 64))
	metadata := completePerformanceMetadata()
	metadata.FixtureDigest = "not-a-digest"
	if err := finalizeQualificationPerformanceReport(path, policy, 100, 100, completePerformanceEnvironment(policy), "image", "amd64", "", metadata); err == nil || !strings.Contains(err.Error(), "fixtureDigest is not a sha256 digest") {
		t.Fatalf("malformed identity error = %v", err)
	}
}

func TestFinalizeQualificationPerformanceReportRejectsPolicyDigestMismatch(t *testing.T) {
	t.Setenv("QUALIFICATION_PERFORMANCE_MODE", qualificationPerformanceModeBootstrap)
	policy := validQualificationPerformancePolicy()
	path := writeCompleteQualificationPerformanceReport(t, policy, "sha256:"+strings.Repeat("a", 64))
	metadata := completePerformanceMetadata()
	metadata.PolicyDigest = "sha256:" + strings.Repeat("e", 64)
	if err := finalizeQualificationPerformanceReport(path, policy, 100, 100, completePerformanceEnvironment(policy), "image", "amd64", "", metadata); err == nil || !strings.Contains(err.Error(), "policyDigest does not match") {
		t.Fatalf("policy digest mismatch error = %v", err)
	}
}

func TestQualificationPerformancePolicyDigestBindsLoadedSourceBytes(t *testing.T) {
	t.Setenv("QUALIFICATION_PERFORMANCE_MODE", qualificationPerformanceModeBootstrap)
	policy := validQualificationPerformancePolicy()
	contents, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(t.TempDir(), "first-policy.json")
	secondPath := filepath.Join(t.TempDir(), "second-policy.json")
	if err := os.WriteFile(firstPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	secondContents := append([]byte(" \n"), contents...)
	secondContents = append(secondContents, '\n')
	if err := os.WriteFile(secondPath, secondContents, 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := readQualificationPerformancePolicy(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := readQualificationPerformancePolicy(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	firstLogical, _ := json.Marshal(first)
	secondLogical, _ := json.Marshal(second)
	if string(firstLogical) != string(secondLogical) {
		t.Fatal("semantically equivalent policy sources decoded to different logical policies")
	}
	if firstDigest, secondDigest := qualificationPerformancePolicyDigest(first), qualificationPerformancePolicyDigest(second); firstDigest == secondDigest {
		t.Fatalf("policy source digests are equal: %s", firstDigest)
	}

	reportPath := writeCompleteQualificationPerformanceReport(t, first, "sha256:"+strings.Repeat("a", 64))
	metadata := completePerformanceMetadata()
	metadata.PolicyDigest = qualificationPerformancePolicyDigest(second)
	if err := finalizeQualificationPerformanceReport(reportPath, first, 100, 100, completePerformanceEnvironment(first), "image", "amd64", "", metadata); err == nil || !strings.Contains(err.Error(), "policyDigest does not match") {
		t.Fatalf("wrong raw policy digest error = %v", err)
	}
}

func TestValidateQualificationPerformancePolicyRequiresExpectedFixture(t *testing.T) {
	policy := validQualificationPerformancePolicy()
	policy.Fixture = "evaluation/data/other.csv"
	if failures := validateQualificationPerformancePolicy(policy); !containsQualificationFailure(failures, "fixture must identify "+qualificationPerformanceFixturePath) {
		t.Fatalf("fixture contract failures = %v", failures)
	}
}

func TestFinalizeQualificationPerformanceReportRejectsMissingNumericResource(t *testing.T) {
	t.Setenv("QUALIFICATION_PERFORMANCE_MODE", qualificationPerformanceModeBootstrap)
	policy := validQualificationPerformancePolicy()
	path := writeCompleteQualificationPerformanceReport(t, policy, "sha256:"+strings.Repeat("a", 64))
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(contents, &root); err != nil {
		t.Fatal(err)
	}
	var resources map[string]json.RawMessage
	if err := json.Unmarshal(root["resources"], &resources); err != nil {
		t.Fatal(err)
	}
	delete(resources, "cpuSeconds")
	root["resources"], _ = json.Marshal(resources)
	contents, _ = json.Marshal(root)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := finalizeQualificationPerformanceReport(path, policy, 100, 100, completePerformanceEnvironment(policy), "image", "amd64", "", completePerformanceMetadata()); err == nil || !strings.Contains(err.Error(), "resources.cpuSeconds is missing") {
		t.Fatalf("missing resource error = %v", err)
	}
}

func TestFinalizeQualificationPerformanceReportRejectsNullNumericResource(t *testing.T) {
	t.Setenv("QUALIFICATION_PERFORMANCE_MODE", qualificationPerformanceModeBootstrap)
	policy := validQualificationPerformancePolicy()
	path := writeCompleteQualificationPerformanceReport(t, policy, "sha256:"+strings.Repeat("a", 64))
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(contents, &root); err != nil {
		t.Fatal(err)
	}
	var resources map[string]json.RawMessage
	if err := json.Unmarshal(root["resources"], &resources); err != nil {
		t.Fatal(err)
	}
	resources["peakResidentMemoryBytes"] = json.RawMessage(`null`)
	root["resources"], _ = json.Marshal(resources)
	contents, _ = json.Marshal(root)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := finalizeQualificationPerformanceReport(path, policy, 100, 100, completePerformanceEnvironment(policy), "image", "amd64", "", completePerformanceMetadata()); err == nil || !strings.Contains(err.Error(), "resources.peakResidentMemoryBytes is missing") {
		t.Fatalf("null resource error = %v", err)
	}
}

func TestFinalizeQualificationPerformanceReportRejectsMissingEnvironmentField(t *testing.T) {
	t.Setenv("QUALIFICATION_PERFORMANCE_MODE", qualificationPerformanceModeBootstrap)
	policy := validQualificationPerformancePolicy()
	path := writeCompleteQualificationPerformanceReport(t, policy, "sha256:"+strings.Repeat("a", 64))
	environment := []byte(`{"runtime":"Docker Engine test","logicalCPUs":2,"memoryBytes":1024}`)
	if err := finalizeQualificationPerformanceReport(path, policy, 100, 100, environment, "image", "amd64", "", completePerformanceMetadata()); err == nil || !strings.Contains(err.Error(), "environment.dataset is missing") {
		t.Fatalf("missing environment error = %v", err)
	}
}

func TestFinalizeQualificationPerformanceReportRejectsMissingResourceMetricSamples(t *testing.T) {
	t.Setenv("QUALIFICATION_PERFORMANCE_MODE", qualificationPerformanceModeBootstrap)
	policy := validQualificationPerformancePolicy()
	path := writeCompleteQualificationPerformanceReport(t, policy, "sha256:"+strings.Repeat("a", 64))
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(contents, &root); err != nil {
		t.Fatal(err)
	}
	var resources map[string]json.RawMessage
	if err := json.Unmarshal(root["resources"], &resources); err != nil {
		t.Fatal(err)
	}
	delete(resources, "metricSamples")
	root["resources"], _ = json.Marshal(resources)
	contents, _ = json.Marshal(root)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := finalizeQualificationPerformanceReport(path, policy, 100, 100, completePerformanceEnvironment(policy), "image", "amd64", "", completePerformanceMetadata()); err == nil || !strings.Contains(err.Error(), "resources.metricSamples is missing") {
		t.Fatalf("missing metric sample error = %v", err)
	}
}

func TestFinalizeQualificationPerformanceReportRejectsMissingRawResources(t *testing.T) {
	t.Setenv("QUALIFICATION_PERFORMANCE_MODE", qualificationPerformanceModeBootstrap)
	policy := validQualificationPerformancePolicy()
	path := writeCompleteQualificationPerformanceReport(t, policy, "sha256:"+strings.Repeat("a", 64))
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(contents, &root); err != nil {
		t.Fatal(err)
	}
	var resources map[string]json.RawMessage
	if err := json.Unmarshal(root["resources"], &resources); err != nil {
		t.Fatal(err)
	}
	delete(resources, "metricSnapshots")
	root["resources"], _ = json.Marshal(resources)
	contents, _ = json.Marshal(root)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := finalizeQualificationPerformanceReport(path, policy, 100, 100, completePerformanceEnvironment(policy), "image", "amd64", "", completePerformanceMetadata()); err == nil || !strings.Contains(err.Error(), "resources.metricSnapshots is missing") {
		t.Fatalf("missing raw resource error = %v", err)
	}
}

func TestFinalizeQualificationPerformanceReportRejectsMalformedRawResourceGauge(t *testing.T) {
	policy := validQualificationPerformancePolicy()
	path := writeCompleteQualificationPerformanceReport(t, policy, "sha256:"+strings.Repeat("a", 64))
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(contents, &root); err != nil {
		t.Fatal(err)
	}
	var resources map[string]json.RawMessage
	if err := json.Unmarshal(root["resources"], &resources); err != nil {
		t.Fatal(err)
	}
	var snapshots []map[string]json.RawMessage
	if err := json.Unmarshal(resources["metricSnapshots"], &snapshots); err != nil {
		t.Fatal(err)
	}
	snapshots[0]["residentMemoryBytes"] = json.RawMessage(`1.5`)
	resources["metricSnapshots"], _ = json.Marshal(snapshots)
	root["resources"], _ = json.Marshal(resources)
	contents, _ = json.Marshal(root)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := finalizeQualificationPerformanceReport(path, policy, 100, 100, completePerformanceEnvironment(policy), "image", "amd64", "", completePerformanceMetadata()); err == nil {
		t.Fatal("malformed raw resource gauge was accepted")
	}
}

func TestFinalizeQualificationPerformanceReportRejectsResetRawCPU(t *testing.T) {
	t.Setenv("QUALIFICATION_PERFORMANCE_MODE", qualificationPerformanceModeBootstrap)
	policy := validQualificationPerformancePolicy()
	path := writeCompleteQualificationPerformanceReport(t, policy, "sha256:"+strings.Repeat("a", 64))
	var report qualificationPerformanceReport
	if err := readQualificationJSON(path, &report); err != nil {
		t.Fatal(err)
	}
	report.Resources.MetricSnapshots[2].CPUSeconds = 0.5
	if err := writeQualificationJSON(path, report); err != nil {
		t.Fatal(err)
	}
	if err := finalizeQualificationPerformanceReport(path, policy, 100, 100, completePerformanceEnvironment(policy), "image", "amd64", "", completePerformanceMetadata()); err == nil || !strings.Contains(err.Error(), "CPU counter fell") {
		t.Fatalf("reset raw CPU error = %v", err)
	}
}

func TestFinalizeQualificationPerformanceReportRejectsRawResourceSummaryMismatch(t *testing.T) {
	t.Setenv("QUALIFICATION_PERFORMANCE_MODE", qualificationPerformanceModeBootstrap)
	policy := validQualificationPerformancePolicy()
	path := writeCompleteQualificationPerformanceReport(t, policy, "sha256:"+strings.Repeat("a", 64))
	var report qualificationPerformanceReport
	if err := readQualificationJSON(path, &report); err != nil {
		t.Fatal(err)
	}
	report.Resources.CPUSeconds++
	if err := writeQualificationJSON(path, report); err != nil {
		t.Fatal(err)
	}
	if err := finalizeQualificationPerformanceReport(path, policy, 100, 100, completePerformanceEnvironment(policy), "image", "amd64", "", completePerformanceMetadata()); err == nil || !strings.Contains(err.Error(), "resource CPU summary") {
		t.Fatalf("raw resource summary error = %v", err)
	}
}

func TestFinalizeQualificationPerformanceReportRejectsChangedRawProcess(t *testing.T) {
	t.Setenv("QUALIFICATION_PERFORMANCE_MODE", qualificationPerformanceModeBootstrap)
	policy := validQualificationPerformancePolicy()
	path := writeCompleteQualificationPerformanceReport(t, policy, "sha256:"+strings.Repeat("a", 64))
	var report qualificationPerformanceReport
	if err := readQualificationJSON(path, &report); err != nil {
		t.Fatal(err)
	}
	report.Resources.ColdMetricSnapshots[0][1].ProcessStartTimeSeconds++
	if err := writeQualificationJSON(path, report); err != nil {
		t.Fatal(err)
	}
	if err := finalizeQualificationPerformanceReport(path, policy, 100, 100, completePerformanceEnvironment(policy), "image", "amd64", "", completePerformanceMetadata()); err == nil || !strings.Contains(err.Error(), "changed process identity") {
		t.Fatalf("changed raw process error = %v", err)
	}
}

func TestFinalizeQualificationPerformanceReportRejectsTamperedRawLatency(t *testing.T) {
	t.Setenv("QUALIFICATION_PERFORMANCE_MODE", qualificationPerformanceModeBootstrap)
	policy := validQualificationPerformancePolicy()
	path := writeCompleteQualificationPerformanceReport(t, policy, "sha256:"+strings.Repeat("a", 64))
	var report qualificationPerformanceReport
	if err := readQualificationJSON(path, &report); err != nil {
		t.Fatal(err)
	}
	var samples map[string][]float64
	if err := json.Unmarshal(report.Samples, &samples); err != nil {
		t.Fatal(err)
	}
	samples["warmDashboardReadyMs"][0] = 100
	report.Samples, _ = json.Marshal(samples)
	if err := writeQualificationJSON(path, report); err != nil {
		t.Fatal(err)
	}
	if err := finalizeQualificationPerformanceReport(path, policy, 100, 100, completePerformanceEnvironment(policy), "image", "amd64", "", completePerformanceMetadata()); err == nil || !strings.Contains(err.Error(), "warm dashboard readiness latency summary does not match raw samples") {
		t.Fatalf("tampered raw latency error = %v", err)
	}
}

func TestFinalizeQualificationPerformanceReportCompareRequiresExplicitBaseline(t *testing.T) {
	t.Setenv("QUALIFICATION_PERFORMANCE_MODE", qualificationPerformanceModeCompare)
	policy := validQualificationPerformancePolicy()
	path := writeCompleteQualificationPerformanceReport(t, policy, "sha256:"+strings.Repeat("a", 64))
	if err := finalizeQualificationPerformanceReport(path, policy, 100, 100, completePerformanceEnvironment(policy), "image", "amd64", "", completePerformanceMetadata()); err == nil || !strings.Contains(err.Error(), "comparison mode requires an explicit baseline") {
		t.Fatalf("missing baseline error = %v", err)
	}
}

func TestEvaluateQualificationPerformanceRejectsMalformedLatency(t *testing.T) {
	policy := validQualificationPerformancePolicy()
	report := completeQualificationPerformanceReport(policy, "sha256:"+strings.Repeat("a", 64))
	report.Latency["coldDashboardReadyMs"] = qualificationDurationSummary{Samples: 1, P50: math.NaN(), P95: 1, Max: 1}
	failures := evaluateQualificationPerformance(report, policy)
	if !containsQualificationFailure(failures, "non-finite") {
		t.Fatalf("malformed latency failures = %v", failures)
	}
}

func TestFinalizeQualificationPerformanceReportRejectsIncompatibleBaseline(t *testing.T) {
	policy := validQualificationPerformancePolicy()
	baselinePath := writeCompleteQualificationPerformanceReport(t, policy, "sha256:"+strings.Repeat("a", 64))
	t.Setenv("QUALIFICATION_PERFORMANCE_MODE", qualificationPerformanceModeBootstrap)
	if err := finalizeQualificationPerformanceReport(baselinePath, policy, 100, 100, completePerformanceEnvironment(policy), "baseline-image", "amd64", "", completePerformanceMetadata()); err != nil {
		t.Fatalf("baseline finalization failed: %v", err)
	}
	candidatePath := writeCompleteQualificationPerformanceReport(t, policy, "sha256:"+strings.Repeat("c", 64))
	t.Setenv("QUALIFICATION_PERFORMANCE_MODE", qualificationPerformanceModeCompare)
	candidateMetadata := completePerformanceMetadata()
	candidateMetadata.FixtureDigest = "sha256:" + strings.Repeat("c", 64)
	if err := finalizeQualificationPerformanceReport(candidatePath, policy, 100, 100, completePerformanceEnvironment(policy), "candidate-image", "amd64", baselinePath, candidateMetadata); err == nil || !strings.Contains(err.Error(), "fixture digest is incompatible") {
		t.Fatalf("incompatible baseline error = %v", err)
	}
}

func TestFinalizeQualificationPerformanceReportRejectsLatencyAndMemoryBudgets(t *testing.T) {
	policy := validQualificationPerformancePolicy()
	for _, test := range []struct {
		name   string
		mutate func(*qualificationPerformanceReport)
		want   string
	}{
		{"latency", func(report *qualificationPerformanceReport) {
			summary := report.Latency["warmDashboardReadyMs"]
			summary.P95 = policy.Budgets.WarmDashboardReadyP95Ms + 1
			summary.Max = summary.P95
			report.Latency["warmDashboardReadyMs"] = summary
		}, "warm dashboard readiness p95"},
		{"memory", func(report *qualificationPerformanceReport) {
			report.Resources.PeakResidentMemoryBytes = policy.Budgets.PeakResidentMemoryBytes + 1
		}, "peak resident memory"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("QUALIFICATION_PERFORMANCE_MODE", qualificationPerformanceModeBootstrap)
			path := filepath.Join(t.TempDir(), "performance-report.json")
			report := completeQualificationPerformanceReport(policy, "sha256:"+strings.Repeat("a", 64))
			test.mutate(&report)
			if err := writeQualificationJSON(path, report); err != nil {
				t.Fatal(err)
			}
			if err := finalizeQualificationPerformanceReport(path, policy, 100, 100, completePerformanceEnvironment(policy), "image", "amd64", "", completePerformanceMetadata()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("budget error = %v", err)
			}
		})
	}
}

func TestCompareQualificationPerformanceAppliesRelativeResourceLimits(t *testing.T) {
	policy := validQualificationPerformancePolicy()
	baseline := completeQualificationPerformanceReport(policy, "sha256:"+strings.Repeat("a", 64))
	candidate := completeQualificationPerformanceReport(policy, "sha256:"+strings.Repeat("a", 64))
	candidate.Resources.PeakResidentMemoryBytes = 130
	baseline.Resources.PeakResidentMemoryBytes = 100
	failures := compareQualificationPerformance(candidate, baseline, policy)
	if !containsQualificationFailure(failures, "peak resident memory regressed") {
		t.Fatalf("relative resource failures = %v", failures)
	}
}

func TestCompareQualificationPerformanceRejectsZeroLatencyBaseline(t *testing.T) {
	policy := validQualificationPerformancePolicy()
	baseline := completeQualificationPerformanceReport(policy, "sha256:"+strings.Repeat("a", 64))
	candidate := completeQualificationPerformanceReport(policy, "sha256:"+strings.Repeat("a", 64))
	summary := baseline.Latency["warmDashboardReadyMs"]
	summary.P50, summary.P95, summary.Max = 0, 0, 0
	baseline.Latency["warmDashboardReadyMs"] = summary
	summary = candidate.Latency["warmDashboardReadyMs"]
	summary.P95, summary.Max = 1, 1
	candidate.Latency["warmDashboardReadyMs"] = summary
	if failures := compareQualificationPerformance(candidate, baseline, policy); !containsQualificationFailure(failures, "baseline p95 is zero") {
		t.Fatalf("zero latency baseline failures = %v", failures)
	}
}

func TestQualificationPerformanceEnvironmentFingerprintIsMapOrderIndependent(t *testing.T) {
	policy := validQualificationPerformancePolicy()
	left := completeQualificationPerformanceReport(policy, "sha256:"+strings.Repeat("a", 64))
	right := left
	left.Architecture, right.Architecture = "amd64", "amd64"
	left.Environment.Dataset = map[string]int64{"orders": 24, "customers": 7}
	right.Environment.Dataset = map[string]int64{"customers": 7, "orders": 24}
	if qualificationPerformanceEnvironmentFingerprint(left) != qualificationPerformanceEnvironmentFingerprint(right) {
		t.Fatal("environment fingerprint depends on map insertion order")
	}
}

func TestQualificationFixtureManifestDigestIsCanonicalAndRequiresContract(t *testing.T) {
	first := strings.Repeat("a", 64)
	second := strings.Repeat("b", 64)
	left, err := qualificationFixtureManifestDigest([]byte(
		second + "  /app/evaluation/project/leapview.yaml\n" +
			first + "  /app/evaluation/data/orders.csv\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	right, err := qualificationFixtureManifestDigest([]byte(
		first + "  /app/evaluation/data/orders.csv\n" +
			second + "  /app/evaluation/project/leapview.yaml\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("manifest digest depends on file order: %s != %s", left, right)
	}
	changed, err := qualificationFixtureManifestDigest([]byte(
		strings.Repeat("c", 64) + "  /app/evaluation/project/leapview.yaml\n" +
			first + "  /app/evaluation/data/orders.csv\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	if left == changed {
		t.Fatal("manifest digest ignored a per-file digest change")
	}
	for _, malformed := range [][]byte{
		[]byte(""),
		[]byte(first + "  /app/evaluation/data/other.csv\n"),
		[]byte(first + "  /tmp/orders.csv\n"),
	} {
		if _, err := qualificationFixtureManifestDigest(malformed); err == nil {
			t.Fatalf("malformed manifest was accepted: %q", malformed)
		}
	}
}

func TestQualificationParseEffectiveAppLimits(t *testing.T) {
	if cpus, memory, err := qualificationParseEffectiveAppLimits("2000000000 0 0 3221225472", 8); err != nil || cpus != 2 || memory != 3221225472 {
		t.Fatalf("nanoCPU limits = %v, %d, %v", cpus, memory, err)
	}
	if cpus, memory, err := qualificationParseEffectiveAppLimits("0 250000 100000 1073741824", 8); err != nil || cpus != 2.5 || memory != 1073741824 {
		t.Fatalf("quota limits = %v, %d, %v", cpus, memory, err)
	}
	for _, malformed := range []string{"0 0 0 0", "0 1 0 10", "bad 0 0 10"} {
		if _, _, err := qualificationParseEffectiveAppLimits(malformed, 8); err == nil {
			t.Fatalf("malformed effective limits were accepted: %q", malformed)
		}
	}
}

func TestQualificationCPUIdentityPrefersX86Model(t *testing.T) {
	identity, err := qualificationCPUIdentity("processor : 0\nmodel name : Intel(R) Xeon(R) CPU\nCPU implementer : 0x00\nCPU part : 0x000\n")
	if err != nil || identity != "model name=Intel(R) Xeon(R) CPU" {
		t.Fatalf("x86 CPU identity = %q, %v", identity, err)
	}
}

func TestQualificationCPUIdentityUsesARMIdentifiers(t *testing.T) {
	identity, err := qualificationCPUIdentity("processor : 0\nCPU implementer : 0x41\nCPU architecture : 8\nCPU variant : 0x0\nCPU part : 0xd0b\nCPU revision : 1\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ARM ", "cpuImplementer=0x41", "cpuPart=0xd0b", "cpuArchitecture=8", "cpuVariant=0x0", "cpuRevision=1"} {
		if !strings.Contains(identity, want) {
			t.Fatalf("ARM CPU identity %q does not contain %q", identity, want)
		}
	}
}

func TestQualificationCPUIdentityRejectsMissingIdentifiers(t *testing.T) {
	if identity, err := qualificationCPUIdentity("processor : 0\nFeatures : fp asimd\n"); err == nil || identity != "" {
		t.Fatalf("missing CPU identity = %q, %v", identity, err)
	}
}

func TestValidateQualificationPerformanceEvidenceRejectsTamperedEnvironmentFingerprint(t *testing.T) {
	policy := validQualificationPerformancePolicy()
	report := completeQualificationPerformanceReport(policy, "sha256:"+strings.Repeat("a", 64))
	report.Architecture = "amd64"
	if err := json.Unmarshal(completePerformanceEnvironment(policy), &report.Environment); err != nil {
		t.Fatal(err)
	}
	report.Commit = "commit-1"
	report.Image = "image"
	report.Fixture = policy.Fixture
	report.PolicyDigest = "sha256:" + strings.Repeat("e", 64)
	report.EnvironmentFingerprint = "sha256:" + strings.Repeat("f", 64)
	report.fieldPresence = completePerformanceFieldPresence(report)
	if failures := validateQualificationPerformanceEvidence(report, policy); !containsQualificationFailure(failures, "environment fingerprint does not match") {
		t.Fatalf("tampered environment fingerprint failures = %v", failures)
	}
}

func completePerformanceFieldPresence(report qualificationPerformanceReport) map[string]bool {
	presence := map[string]bool{}
	for _, phase := range qualificationLatencyPhases {
		for _, field := range []string{"samples", "p50", "p95", "max"} {
			presence["latency."+phase.Field+"."+field] = true
		}
	}
	for _, field := range []string{"requests", "operations", "errors", "failures"} {
		presence["reliability."+field] = true
	}
	for _, field := range []string{"peakResidentMemoryBytes", "cpuSeconds", "metricSamples", "metricSnapshots", "coldMetricSnapshots", "temporaryDiskBeforeBytes", "temporaryDiskAfterBytes", "temporaryDiskGrowthBytes", "goroutinesBefore", "goroutinesAfter", "peakOpenConnections"} {
		presence["resources."+field] = true
	}
	for _, field := range []string{"runtime", "cpuModel", "kernel", "logicalCPUs", "memoryBytes", "effectiveCPULimit", "effectiveMemoryLimitBytes", "dataset"} {
		presence["environment."+field] = true
	}
	return presence
}

func completeQualificationPerformanceReport(policy qualificationPerformancePolicy, fixtureDigest string) qualificationPerformanceReport {
	report := qualificationPerformanceReport{SchemaVersion: qualificationPerformanceSchema, GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), Latency: map[string]qualificationDurationSummary{}}
	for _, phase := range qualificationLatencyPhases {
		samples := qualificationPerformanceExpectedSamples(policy, phase.Field)
		report.Latency[phase.Field] = qualificationDurationSummary{Samples: samples, P50: 1, P95: 1, Max: 1}
	}
	raw := map[string][]float64{}
	for _, phase := range qualificationLatencyPhases {
		values := make([]float64, qualificationPerformanceExpectedSamples(policy, phase.Field))
		for index := range values {
			values[index] = 1
		}
		raw[phase.Field] = values
	}
	report.Samples, _ = json.Marshal(raw)
	report.Concurrency, _ = json.Marshal(map[string]any{"readers": policy.Assumptions.Samples.ConcurrentReaders, "waveMs": 1})
	report.Reliability.Operations = qualificationPerformanceExpectedOperations(policy)
	report.Reliability.Requests = qualificationPerformanceExpectedRequests(policy)
	report.Reliability.Failures = []string{}
	report.Resources.PeakResidentMemoryBytes = 512
	report.Resources.CPUSeconds = 7 + 0.1*float64(policy.Assumptions.Samples.ColdDashboardLoads)
	report.Resources.MetricSamples = qualificationPerformanceMetricSamples
	report.Resources.MetricSnapshots = make([]qualificationPerformanceMetricSnapshot, qualificationPerformanceMetricSamples)
	for index := range report.Resources.MetricSnapshots {
		report.Resources.MetricSnapshots[index] = qualificationPerformanceMetricSnapshot{
			ProcessStartTimeSeconds: 100,
			CPUSeconds:              float64(index + 1),
			ResidentMemoryBytes:     512,
			Goroutines:              1,
			OpenConnections:         1,
		}
	}
	report.Resources.ColdMetricSnapshots = make([][]qualificationPerformanceMetricSnapshot, policy.Assumptions.Samples.ColdDashboardLoads)
	for index := range report.Resources.ColdMetricSnapshots {
		report.Resources.ColdMetricSnapshots[index] = []qualificationPerformanceMetricSnapshot{
			{ProcessStartTimeSeconds: float64(101 + index), CPUSeconds: 0, ResidentMemoryBytes: 512, Goroutines: 1, OpenConnections: 1},
			{ProcessStartTimeSeconds: float64(101 + index), CPUSeconds: 0.1, ResidentMemoryBytes: 512, Goroutines: 1, OpenConnections: 1},
		}
	}
	report.Resources.TemporaryDiskBeforeBytes = 100
	report.Resources.TemporaryDiskAfterBytes = 100
	report.Resources.TemporaryDiskGrowthBytes = 0
	report.Resources.GoroutinesBefore = 1
	report.Resources.GoroutinesAfter = 1
	report.Resources.PeakOpenConnections = 1
	report.Commit = "commit-1"
	report.FixtureDigest = fixtureDigest
	report.SampleProtocol = qualificationPerformanceSampleProtocolForPolicy(policy)
	report.Toolchain = completePerformanceMetadata().Toolchain
	return report
}

func writeCompleteQualificationPerformanceReport(t *testing.T, policy qualificationPerformancePolicy, fixtureDigest string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "performance-report.json")
	if err := writeQualificationJSON(path, completeQualificationPerformanceReport(policy, fixtureDigest)); err != nil {
		t.Fatal(err)
	}
	return path
}

func completePerformanceEnvironment(policy qualificationPerformancePolicy) []byte {
	value, _ := json.Marshal(map[string]any{
		"runtime": "Docker Engine test", "cpuModel": "test CPU", "kernel": "Linux test",
		"logicalCPUs": policy.Assumptions.MinimumLogicalCPUs, "memoryBytes": policy.Assumptions.MinimumMemoryBytes,
		"effectiveCPULimit": float64(policy.Assumptions.MinimumLogicalCPUs), "effectiveMemoryLimitBytes": policy.Assumptions.MinimumMemoryBytes,
		"dataset": map[string]int64{"orders": policy.Assumptions.Dataset.Orders},
	})
	return value
}

func completePerformanceMetadata() qualificationPerformanceMetadata {
	return qualificationPerformanceMetadata{Commit: "commit-1", FixtureDigest: "sha256:" + strings.Repeat("a", 64), Toolchain: qualificationPerformanceToolchain{BrowserImage: "sha256:" + strings.Repeat("b", 64), Node: "v22.0.0", Playwright: "1.62.1", PackageHash: "sha256:" + strings.Repeat("c", 64), HarnessHash: "sha256:" + strings.Repeat("d", 64)}}
}

func containsQualificationFailure(failures []string, want string) bool {
	for _, failure := range failures {
		if strings.Contains(failure, want) {
			return true
		}
	}
	return false
}
