package composectl

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
)

type qualificationPerformancePolicy struct {
	SchemaVersion int    `json:"schemaVersion"`
	Workload      string `json:"workload"`
	Assumptions   struct {
		MinimumLogicalCPUs int64 `json:"minimumLogicalCPUs"`
		MinimumMemoryBytes int64 `json:"minimumMemoryBytes"`
		Samples            struct {
			ColdDashboardLoads int `json:"coldDashboardLoads"`
			WarmDashboardLoads int `json:"warmDashboardLoads"`
			FilterInteractions int `json:"filterInteractions"`
			TableInteractions  int `json:"tableInteractions"`
			GovernedQueries    int `json:"governedQueries"`
			RefreshRuns        int `json:"refreshRuns"`
			ConcurrentReaders  int `json:"concurrentReaders"`
		} `json:"samples"`
	} `json:"assumptions"`
	Budgets struct {
		ColdDashboardReadyP95Ms     float64 `json:"coldDashboardReadyP95Ms"`
		WarmDashboardReadyP95Ms     float64 `json:"warmDashboardReadyP95Ms"`
		FilterToSettleP95Ms         float64 `json:"filterToSettleP95Ms"`
		TableInteractionP95Ms       float64 `json:"tableInteractionP95Ms"`
		GovernedQueryP95Ms          float64 `json:"governedQueryP95Ms"`
		RefreshP95Ms                float64 `json:"refreshP95Ms"`
		ConcurrentQueryP95Ms        float64 `json:"concurrentQueryP95Ms"`
		ErrorRateMax                float64 `json:"errorRateMax"`
		PeakResidentMemoryBytes     int64   `json:"peakResidentMemoryBytes"`
		CPUSecondsMax               float64 `json:"cpuSecondsMax"`
		TemporaryDiskGrowthBytesMax int64   `json:"temporaryDiskGrowthBytesMax"`
		GoroutineGrowthMax          int64   `json:"goroutineGrowthMax"`
		OpenConnectionsMax          int64   `json:"openConnectionsMax"`
	} `json:"budgets"`
	Comparison struct {
		MaxRegressionRatio              float64 `json:"maxRegressionRatio"`
		MinimumMeaningfulLatencyDeltaMs float64 `json:"minimumMeaningfulLatencyDeltaMs"`
	} `json:"comparison"`
}

type qualificationDurationSummary struct {
	Samples int     `json:"samples"`
	P50     float64 `json:"p50"`
	P95     float64 `json:"p95"`
	Max     float64 `json:"max"`
}

type qualificationPerformanceReport struct {
	SchemaVersion int                                     `json:"schemaVersion"`
	GeneratedAt   string                                  `json:"generatedAt"`
	Policy        qualificationPerformancePolicy          `json:"policy"`
	Latency       map[string]qualificationDurationSummary `json:"latency"`
	Reliability   struct {
		Requests *int     `json:"requests"`
		Errors   *int     `json:"errors"`
		Failures []string `json:"failures"`
	} `json:"reliability"`
	Resources   qualificationResourceReport `json:"resources"`
	Samples     json.RawMessage             `json:"samples,omitempty"`
	Concurrency json.RawMessage             `json:"concurrency,omitempty"`
	Environment struct {
		Runtime     string           `json:"runtime"`
		LogicalCPUs int64            `json:"logicalCPUs"`
		MemoryBytes int64            `json:"memoryBytes"`
		Dataset     map[string]int64 `json:"dataset"`
	} `json:"environment"`
	Image        string `json:"image"`
	Architecture string `json:"architecture"`
	Comparison   struct {
		Baseline                        *string  `json:"baseline"`
		MaxRegressionRatio              float64  `json:"maxRegressionRatio"`
		MinimumMeaningfulLatencyDeltaMs float64  `json:"minimumMeaningfulLatencyDeltaMs"`
		Failures                        []string `json:"failures"`
	} `json:"comparison"`
	Assertions struct {
		Environment         bool `json:"environment"`
		AbsoluteBudgets     bool `json:"absoluteBudgets"`
		ComparisonTolerance bool `json:"comparisonTolerance"`
		ErrorFree           bool `json:"errorFree"`
	} `json:"assertions"`
	Failures []string `json:"failures"`
	Result   string   `json:"result"`
}

var qualificationLatencyPhases = []struct {
	Field, Label string
	Budget       func(qualificationPerformancePolicy) float64
}{
	{"coldDashboardReadyMs", "cold dashboard readiness", func(p qualificationPerformancePolicy) float64 { return p.Budgets.ColdDashboardReadyP95Ms }},
	{"warmDashboardReadyMs", "warm dashboard readiness", func(p qualificationPerformancePolicy) float64 { return p.Budgets.WarmDashboardReadyP95Ms }},
	{"filterToSettleMs", "filter-to-settle", func(p qualificationPerformancePolicy) float64 { return p.Budgets.FilterToSettleP95Ms }},
	{"tableInteractionMs", "table interaction", func(p qualificationPerformancePolicy) float64 { return p.Budgets.TableInteractionP95Ms }},
	{"governedQueryMs", "governed query", func(p qualificationPerformancePolicy) float64 { return p.Budgets.GovernedQueryP95Ms }},
	{"refreshMs", "refresh/materialization", func(p qualificationPerformancePolicy) float64 { return p.Budgets.RefreshP95Ms }},
	{"concurrentQueryMs", "concurrent query", func(p qualificationPerformancePolicy) float64 { return p.Budgets.ConcurrentQueryP95Ms }},
}

func validateQualificationPerformancePolicy(policy qualificationPerformancePolicy) []string {
	var failures []string
	if policy.SchemaVersion != 1 {
		failures = append(failures, "schemaVersion must be 1")
	}
	if strings.TrimSpace(policy.Workload) == "" {
		failures = append(failures, "workload must be a non-empty string")
	}
	if policy.Assumptions.MinimumLogicalCPUs <= 0 {
		failures = append(failures, "assumptions.minimumLogicalCPUs must be greater than 0")
	}
	if policy.Assumptions.MinimumMemoryBytes <= 0 {
		failures = append(failures, "assumptions.minimumMemoryBytes must be greater than 0")
	}
	for field, value := range map[string]int{
		"coldDashboardLoads": policy.Assumptions.Samples.ColdDashboardLoads,
		"warmDashboardLoads": policy.Assumptions.Samples.WarmDashboardLoads,
		"filterInteractions": policy.Assumptions.Samples.FilterInteractions,
		"tableInteractions":  policy.Assumptions.Samples.TableInteractions,
		"governedQueries":    policy.Assumptions.Samples.GovernedQueries,
		"refreshRuns":        policy.Assumptions.Samples.RefreshRuns,
		"concurrentReaders":  policy.Assumptions.Samples.ConcurrentReaders,
	} {
		if value <= 0 {
			failures = append(failures, "assumptions.samples."+field+" must be greater than 0")
		}
	}
	for field, value := range map[string]float64{
		"coldDashboardReadyP95Ms":     policy.Budgets.ColdDashboardReadyP95Ms,
		"warmDashboardReadyP95Ms":     policy.Budgets.WarmDashboardReadyP95Ms,
		"filterToSettleP95Ms":         policy.Budgets.FilterToSettleP95Ms,
		"tableInteractionP95Ms":       policy.Budgets.TableInteractionP95Ms,
		"governedQueryP95Ms":          policy.Budgets.GovernedQueryP95Ms,
		"refreshP95Ms":                policy.Budgets.RefreshP95Ms,
		"concurrentQueryP95Ms":        policy.Budgets.ConcurrentQueryP95Ms,
		"peakResidentMemoryBytes":     float64(policy.Budgets.PeakResidentMemoryBytes),
		"cpuSecondsMax":               policy.Budgets.CPUSecondsMax,
		"temporaryDiskGrowthBytesMax": float64(policy.Budgets.TemporaryDiskGrowthBytesMax),
		"goroutineGrowthMax":          float64(policy.Budgets.GoroutineGrowthMax),
		"openConnectionsMax":          float64(policy.Budgets.OpenConnectionsMax),
	} {
		if value < 0 {
			failures = append(failures, "budgets."+field+" must be at least 0")
		}
	}
	if policy.Budgets.ErrorRateMax < 0 || policy.Budgets.ErrorRateMax > 1 {
		failures = append(failures, "budgets.errorRateMax must be between 0 and 1")
	}
	if policy.Comparison.MaxRegressionRatio <= 1 {
		failures = append(failures, "comparison.maxRegressionRatio must be greater than 1")
	}
	if policy.Comparison.MinimumMeaningfulLatencyDeltaMs < 0 {
		failures = append(failures, "comparison.minimumMeaningfulLatencyDeltaMs must be at least 0")
	}
	return failures
}

func evaluateQualificationPerformance(
	report qualificationPerformanceReport,
	policy qualificationPerformancePolicy,
) []string {
	failures := validateQualificationPerformanceProtocol(report, policy)
	failures = append(failures, validateQualificationPerformanceMetricEvidence(report, policy)...)
	for _, phase := range qualificationLatencyPhases {
		actual := report.Latency[phase.Field].P95
		limit := phase.Budget(policy)
		if actual > limit {
			failures = append(failures, fmt.Sprintf(
				"%s p95 %vms exceeds %vms",
				phase.Label, actual, limit,
			))
		}
	}
	errorRate := 1.0
	if report.Reliability.Requests != nil && *report.Reliability.Requests > 0 && report.Reliability.Errors != nil {
		errorRate = float64(*report.Reliability.Errors) /
			float64(*report.Reliability.Requests)
	}
	if errorRate > policy.Budgets.ErrorRateMax {
		failures = append(failures, fmt.Sprintf(
			"request error rate %v exceeds %v",
			roundQualificationFloat(errorRate), policy.Budgets.ErrorRateMax,
		))
	}
	resources := report.Resources
	if qualificationResourceValue(resources.PeakResidentMemoryBytes) > policy.Budgets.PeakResidentMemoryBytes {
		failures = append(failures, fmt.Sprintf(
			"peak resident memory %d bytes exceeds %d bytes",
			qualificationResourceValue(resources.PeakResidentMemoryBytes),
			policy.Budgets.PeakResidentMemoryBytes,
		))
	}
	if qualificationResourceValue(resources.CPUSeconds) > policy.Budgets.CPUSecondsMax {
		failures = append(failures, fmt.Sprintf(
			"CPU consumption %vs exceeds %vs",
			qualificationResourceValue(resources.CPUSeconds), policy.Budgets.CPUSecondsMax,
		))
	}
	if resources.TemporaryDiskGrowthBytes > policy.Budgets.TemporaryDiskGrowthBytesMax {
		failures = append(failures, fmt.Sprintf(
			"temporary disk growth %d bytes exceeds %d bytes",
			resources.TemporaryDiskGrowthBytes,
			policy.Budgets.TemporaryDiskGrowthBytesMax,
		))
	}
	if growth := qualificationResourceValue(resources.GoroutinesAfter) - qualificationResourceValue(resources.GoroutinesBefore); growth > policy.Budgets.GoroutineGrowthMax {
		failures = append(failures, fmt.Sprintf(
			"steady-state goroutine growth %d exceeds %d",
			growth, policy.Budgets.GoroutineGrowthMax,
		))
	}
	if qualificationResourceValue(resources.PeakOpenConnections) > policy.Budgets.OpenConnectionsMax {
		failures = append(failures, fmt.Sprintf(
			"peak open connections %d exceeds %d",
			qualificationResourceValue(resources.PeakOpenConnections),
			policy.Budgets.OpenConnectionsMax,
		))
	}
	return failures
}

func validateQualificationPerformanceLatencies(report qualificationPerformanceReport, policy qualificationPerformancePolicy) []string {
	expectedSamples := qualificationPerformanceSampleCounts(policy)
	var failures []string
	for _, phase := range qualificationLatencyPhases {
		summary, present := report.Latency[phase.Field]
		if !present {
			failures = append(failures, "latency."+phase.Field+" is missing")
			continue
		}
		expected := expectedSamples[phase.Field]
		if summary.Samples <= 0 || summary.Samples != expected {
			failures = append(failures, fmt.Sprintf("latency.%s has %d samples, expected %d", phase.Field, summary.Samples, expected))
		}
		valid := true
		for _, metric := range []struct {
			name  string
			value float64
		}{{"p50", summary.P50}, {"p95", summary.P95}, {"max", summary.Max}} {
			if metric.value < 0 || math.IsNaN(metric.value) || math.IsInf(metric.value, 0) {
				failures = append(failures, fmt.Sprintf("latency.%s.%s must be finite and nonnegative", phase.Field, metric.name))
				valid = false
			}
		}
		if valid && (summary.P50 > summary.P95 || summary.P95 > summary.Max) {
			failures = append(failures, "latency."+phase.Field+" must satisfy p50 <= p95 <= max")
		}
	}
	return failures
}

func compareQualificationPerformance(
	candidate,
	baseline qualificationPerformanceReport,
	policy qualificationPerformancePolicy,
) []string {
	failures := qualificationPerformanceComparisonIdentity(candidate, baseline, policy)
	failures = append(failures, qualificationPerformanceBaselineFailures(baseline)...)
	for _, failure := range validateQualificationPerformanceMetricEvidence(candidate, policy) {
		failures = append(failures, "candidate "+failure)
	}
	for _, failure := range validateQualificationPerformanceMetricEvidence(baseline, policy) {
		failures = append(failures, "baseline "+failure)
	}
	if len(failures) > 0 {
		return failures
	}
	for _, phase := range qualificationLatencyPhases {
		previous := baseline.Latency[phase.Field].P95
		current := candidate.Latency[phase.Field].P95
		ratio := 1.0
		if previous > 0 {
			ratio = current / previous
		} else if current > 0 {
			ratio = math.Inf(1)
		}
		if ratio > policy.Comparison.MaxRegressionRatio &&
			current-previous >= policy.Comparison.MinimumMeaningfulLatencyDeltaMs {
			failures = append(failures, fmt.Sprintf(
				"%s p95 regressed from %vms to %vms (%vx, limit %vx)",
				phase.Label,
				previous,
				current,
				roundQualificationFloat(ratio),
				policy.Comparison.MaxRegressionRatio,
			))
		}
	}
	return failures
}

func finalizeQualificationPerformanceReport(
	path string,
	policy qualificationPerformancePolicy,
	diskBefore,
	diskAfter int64,
	environmentJSON []byte,
	image,
	architecture,
	baselinePath string,
) error {
	var report qualificationPerformanceReport
	if err := readQualificationJSON(path, &report); err != nil {
		return err
	}
	if failures := validateQualificationPerformancePolicy(policy); len(failures) > 0 {
		return fmt.Errorf("invalid performance policy: %s", strings.Join(failures, "; "))
	}
	report.Resources.TemporaryDiskBeforeBytes = diskBefore
	report.Resources.TemporaryDiskAfterBytes = diskAfter
	report.Resources.TemporaryDiskGrowthBytes = max(0, diskAfter-diskBefore)
	if err := json.Unmarshal(environmentJSON, &report.Environment); err != nil {
		return fmt.Errorf("decode performance environment: %w", err)
	}
	report.Image = image
	report.Architecture = architecture
	var comparisonFailures []string
	report.Comparison.Baseline = nil
	if strings.TrimSpace(baselinePath) != "" {
		var baseline qualificationPerformanceReport
		if err := readQualificationJSON(baselinePath, &baseline); err != nil {
			return err
		}
		comparisonFailures = compareQualificationPerformance(report, baseline, policy)
		value := baselinePath
		report.Comparison.Baseline = &value
	}
	report.Comparison.MaxRegressionRatio = policy.Comparison.MaxRegressionRatio
	report.Comparison.MinimumMeaningfulLatencyDeltaMs =
		policy.Comparison.MinimumMeaningfulLatencyDeltaMs
	report.Comparison.Failures = comparisonFailures
	var environmentFailures []string
	if report.Environment.LogicalCPUs < policy.Assumptions.MinimumLogicalCPUs {
		environmentFailures = append(environmentFailures, fmt.Sprintf(
			"runner has %d logical CPUs, requires %d",
			report.Environment.LogicalCPUs,
			policy.Assumptions.MinimumLogicalCPUs,
		))
	}
	if report.Environment.MemoryBytes < policy.Assumptions.MinimumMemoryBytes {
		environmentFailures = append(environmentFailures, fmt.Sprintf(
			"runner has %d bytes of memory, requires %d",
			report.Environment.MemoryBytes,
			policy.Assumptions.MinimumMemoryBytes,
		))
	}
	absoluteFailures := evaluateQualificationPerformance(report, policy)
	// Validate the worker's protocol before recording controller-owned budgets.
	report.Policy = policy
	report.Assertions.Environment = len(environmentFailures) == 0
	report.Assertions.AbsoluteBudgets = len(absoluteFailures) == 0
	report.Assertions.ComparisonTolerance = report.Comparison.Baseline != nil && len(comparisonFailures) == 0
	report.Assertions.ErrorFree = report.Reliability.Errors != nil && *report.Reliability.Errors == 0 &&
		len(report.Reliability.Failures) == 0
	report.Failures = append(report.Failures, environmentFailures...)
	report.Failures = append(report.Failures, absoluteFailures...)
	report.Failures = append(report.Failures, comparisonFailures...)
	report.Failures = append(report.Failures, report.Reliability.Failures...)
	report.Result = "success"
	if len(report.Failures) > 0 {
		report.Result = "failure"
	}
	if err := writeQualificationJSON(path, report); err != nil {
		return err
	}
	if report.Result != "success" {
		return fmt.Errorf(
			"installed-candidate performance budgets failed: %s",
			strings.Join(report.Failures, "; "),
		)
	}
	return nil
}

func roundQualificationFloat(value float64) float64 {
	return math.Round(value*100) / 100
}

func qualificationPerformanceBaseline() string {
	return strings.TrimSpace(os.Getenv("QUALIFICATION_PERFORMANCE_BASELINE"))
}
