package composectl

import (
	"fmt"
	"maps"
	"strings"
)

// Compare the recorded workload and environment before interpreting relative
// latency. This does not establish independent acceptance of the baseline or
// identity for fixture contents and toolchains that the report does not record.
func qualificationPerformanceComparisonIdentity(candidate, baseline qualificationPerformanceReport, policy qualificationPerformancePolicy) []string {
	var failures []string
	for _, item := range []struct {
		name   string
		report qualificationPerformanceReport
	}{{"candidate", candidate}, {"baseline", baseline}} {
		for _, failure := range validateQualificationPerformanceProtocol(item.report, policy) {
			failures = append(failures, item.name+" "+failure)
		}
	}
	// Image versions are expected to differ. Absolute budgets and regression
	// thresholds come from the current policy, not from the baseline report.
	if strings.TrimSpace(candidate.Architecture) == "" || strings.TrimSpace(baseline.Architecture) == "" || candidate.Architecture != baseline.Architecture {
		failures = append(failures, fmt.Sprintf("comparison architecture must be present and match: candidate=%q baseline=%q", candidate.Architecture, baseline.Architecture))
	}
	current, previous := candidate.Environment, baseline.Environment
	if strings.TrimSpace(current.Runtime) == "" || strings.TrimSpace(previous.Runtime) == "" || current.Runtime != previous.Runtime {
		failures = append(failures, fmt.Sprintf("comparison environment.runtime must be present and match: candidate=%q baseline=%q", current.Runtime, previous.Runtime))
	}
	if current.LogicalCPUs <= 0 || previous.LogicalCPUs <= 0 || current.LogicalCPUs != previous.LogicalCPUs {
		failures = append(failures, fmt.Sprintf("comparison environment.logicalCPUs must be positive and match: candidate=%d baseline=%d", current.LogicalCPUs, previous.LogicalCPUs))
	}
	if current.MemoryBytes <= 0 || previous.MemoryBytes <= 0 || current.MemoryBytes != previous.MemoryBytes {
		failures = append(failures, fmt.Sprintf("comparison environment.memoryBytes must be positive and match: candidate=%d baseline=%d", current.MemoryBytes, previous.MemoryBytes))
	}
	if !validQualificationDatasetIdentity(current.Dataset) || !validQualificationDatasetIdentity(previous.Dataset) || !maps.Equal(current.Dataset, previous.Dataset) {
		failures = append(failures, "comparison environment.dataset must contain matching named, nonnegative row counts")
	}
	return failures
}

func validQualificationDatasetIdentity(dataset map[string]int64) bool {
	if len(dataset) == 0 {
		return false
	}
	for name, rows := range dataset {
		if strings.TrimSpace(name) == "" || rows < 0 {
			return false
		}
	}
	return true
}

func qualificationPerformanceBaselineFailures(baseline qualificationPerformanceReport) []string {
	// The first successful absolute-only report has no comparison assertion.
	// Its finalized outcome is required; independent acceptance is a separate gate.
	var failures []string
	if baseline.Result != "success" {
		failures = append(failures, fmt.Sprintf("baseline result must be success, got %q", baseline.Result))
	}
	for _, assertion := range []struct {
		name   string
		passed bool
	}{
		{"environment", baseline.Assertions.Environment},
		{"absoluteBudgets", baseline.Assertions.AbsoluteBudgets},
		{"errorFree", baseline.Assertions.ErrorFree},
	} {
		if !assertion.passed {
			failures = append(failures, "baseline assertions."+assertion.name+" must be true")
		}
	}
	if len(baseline.Failures) > 0 {
		failures = append(failures, "baseline failures: "+strings.Join(baseline.Failures, "; "))
	}
	if baseline.Reliability.Errors == nil {
		failures = append(failures, "baseline reliability.errors must be present")
	} else if *baseline.Reliability.Errors != 0 {
		failures = append(failures, fmt.Sprintf("baseline reliability.errors must be zero, got %d", *baseline.Reliability.Errors))
	}
	if len(baseline.Reliability.Failures) > 0 {
		failures = append(failures, "baseline reliability.failures: "+strings.Join(baseline.Reliability.Failures, "; "))
	}
	return failures
}
