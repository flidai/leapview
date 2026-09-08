package composectl

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

type qualificationFixtureManifestEntry struct {
	Path   string
	Digest string
}

// qualificationCPUIdentity returns a stable label made only from identifiers
// exposed by the running kernel. Most x86 systems expose a model name, while
// some ARM64 systems expose only the implementer/part tuple and optional
// architecture details.
func qualificationCPUIdentity(cpuinfo string) (string, error) {
	values := make(map[string]string)
	for _, line := range strings.Split(cpuinfo, "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		value := strings.TrimSpace(parts[1])
		if value == "" {
			continue
		}
		switch key {
		case "model name", "hardware", "model", "cpu implementer", "cpu architecture", "cpu variant", "cpu part", "cpu revision":
			if _, exists := values[key]; !exists {
				values[key] = value
			}
		}
	}
	for _, key := range []string{"model name", "hardware", "model"} {
		if value := values[key]; value != "" {
			return key + "=" + value, nil
		}
	}
	if values["cpu implementer"] == "" || values["cpu part"] == "" {
		return "", fmt.Errorf("cpuinfo has neither a preferred model nor ARM implementer and part identifiers")
	}
	identity := []string{
		"cpuImplementer=" + values["cpu implementer"],
		"cpuPart=" + values["cpu part"],
	}
	for _, field := range []struct {
		key   string
		label string
	}{
		{key: "cpu architecture", label: "cpuArchitecture"},
		{key: "cpu variant", label: "cpuVariant"},
		{key: "cpu revision", label: "cpuRevision"},
	} {
		if value := values[field.key]; value != "" {
			identity = append(identity, field.label+"="+value)
		}
	}
	return "ARM " + strings.Join(identity, " "), nil
}

func qualificationJSONFieldPresent(fields map[string]json.RawMessage, field string) bool {
	raw, ok := fields[field]
	return ok && strings.TrimSpace(string(raw)) != "null"
}

func qualificationValidateRawResourceEvidence(resources map[string]json.RawMessage) error {
	if raw, ok := resources["metricSnapshots"]; ok && qualificationJSONFieldPresent(resources, "metricSnapshots") {
		if err := qualificationValidateRawMetricSnapshots(raw); err != nil {
			return fmt.Errorf("resources.metricSnapshots: %w", err)
		}
	}
	if raw, ok := resources["coldMetricSnapshots"]; ok && qualificationJSONFieldPresent(resources, "coldMetricSnapshots") {
		var phases []json.RawMessage
		if err := json.Unmarshal(raw, &phases); err != nil {
			return fmt.Errorf("resources.coldMetricSnapshots: %w", err)
		}
		for index, phase := range phases {
			if err := qualificationValidateRawMetricSnapshots(phase); err != nil {
				return fmt.Errorf("resources.coldMetricSnapshots[%d]: %w", index, err)
			}
		}
	}
	return nil
}

func qualificationValidateRawMetricSnapshots(raw json.RawMessage) error {
	var snapshots []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &snapshots); err != nil {
		return err
	}
	for index, snapshot := range snapshots {
		for _, field := range []string{"processStartTimeSeconds", "cpuSeconds", "residentMemoryBytes", "goroutines", "openConnections"} {
			if !qualificationJSONFieldPresent(snapshot, field) {
				return fmt.Errorf("snapshot %d field %s is missing or null", index, field)
			}
		}
		var processStart, cpu float64
		if err := json.Unmarshal(snapshot["processStartTimeSeconds"], &processStart); err != nil {
			return fmt.Errorf("snapshot %d processStartTimeSeconds is not numeric", index)
		}
		if err := json.Unmarshal(snapshot["cpuSeconds"], &cpu); err != nil {
			return fmt.Errorf("snapshot %d cpuSeconds is not numeric", index)
		}
		for _, field := range []string{"residentMemoryBytes", "goroutines", "openConnections"} {
			var value int64
			if err := json.Unmarshal(snapshot[field], &value); err != nil {
				return fmt.Errorf("snapshot %d %s is not an integer", index, field)
			}
		}
	}
	return nil
}

func validateQualificationPerformanceResourceEvidence(report qualificationPerformanceReport, policy qualificationPerformancePolicy) []string {
	var failures []string
	warm := report.Resources.MetricSnapshots
	cold := report.Resources.ColdMetricSnapshots
	if len(warm) != qualificationPerformanceMetricSamples {
		failures = append(failures, fmt.Sprintf("resource metric snapshots %d does not match %d", len(warm), qualificationPerformanceMetricSamples))
	}
	if len(cold) != policy.Assumptions.Samples.ColdDashboardLoads {
		failures = append(failures, fmt.Sprintf("cold resource phases %d does not match %d", len(cold), policy.Assumptions.Samples.ColdDashboardLoads))
	}
	valid := len(warm) == qualificationPerformanceMetricSamples && len(cold) == policy.Assumptions.Samples.ColdDashboardLoads
	if valid {
		if phaseFailures := validateQualificationPerformanceResourcePhase(warm, qualificationPerformanceMetricSamples); len(phaseFailures) > 0 {
			failures = append(failures, "warm resource samples: "+strings.Join(phaseFailures, ", "))
			valid = false
		}
		for index, phase := range cold {
			if phaseFailures := validateQualificationPerformanceResourcePhase(phase, 2); len(phaseFailures) > 0 {
				failures = append(failures, fmt.Sprintf("cold resource samples %d: %s", index, strings.Join(phaseFailures, ", ")))
				valid = false
			}
		}
	}
	if !valid {
		return failures
	}
	want := qualificationPerformanceResourceSummaryFromSamples(warm, cold)
	if report.Resources.CPUSeconds != want.CPUSeconds {
		failures = append(failures, fmt.Sprintf("resource CPU summary %v does not match raw samples %v", report.Resources.CPUSeconds, want.CPUSeconds))
	}
	if report.Resources.PeakResidentMemoryBytes != want.PeakResidentMemoryBytes {
		failures = append(failures, fmt.Sprintf("resource resident-memory summary %d does not match raw samples %d", report.Resources.PeakResidentMemoryBytes, want.PeakResidentMemoryBytes))
	}
	if report.Resources.GoroutinesBefore != want.GoroutinesBefore || report.Resources.GoroutinesAfter != want.GoroutinesAfter {
		failures = append(failures, "resource goroutine summary does not match raw samples")
	}
	if report.Resources.PeakOpenConnections != want.PeakOpenConnections {
		failures = append(failures, fmt.Sprintf("resource connection summary %d does not match raw samples %d", report.Resources.PeakOpenConnections, want.PeakOpenConnections))
	}
	return failures
}

type qualificationPerformanceResourceSummary struct {
	CPUSeconds              float64
	PeakResidentMemoryBytes int64
	GoroutinesBefore        int64
	GoroutinesAfter         int64
	PeakOpenConnections     int64
}

func qualificationPerformanceResourceSummaryFromSamples(warm []qualificationPerformanceMetricSnapshot, cold [][]qualificationPerformanceMetricSnapshot) qualificationPerformanceResourceSummary {
	allSamples := append(append([]qualificationPerformanceMetricSnapshot(nil), warm...), flattenQualificationPerformanceResourceSamples(cold)...)
	cpuSeconds := warm[len(warm)-1].CPUSeconds - warm[0].CPUSeconds
	for _, phase := range cold {
		cpuSeconds += phase[len(phase)-1].CPUSeconds - phase[0].CPUSeconds
	}
	result := qualificationPerformanceResourceSummary{
		CPUSeconds:       roundQualificationFloat(cpuSeconds),
		GoroutinesBefore: warm[0].Goroutines,
		GoroutinesAfter:  warm[len(warm)-1].Goroutines,
	}
	for _, sample := range allSamples {
		result.PeakResidentMemoryBytes = max(result.PeakResidentMemoryBytes, sample.ResidentMemoryBytes)
		result.PeakOpenConnections = max(result.PeakOpenConnections, sample.OpenConnections)
	}
	return result
}

func flattenQualificationPerformanceResourceSamples(phases [][]qualificationPerformanceMetricSnapshot) []qualificationPerformanceMetricSnapshot {
	var result []qualificationPerformanceMetricSnapshot
	for _, phase := range phases {
		result = append(result, phase...)
	}
	return result
}

func validateQualificationPerformanceResourcePhase(samples []qualificationPerformanceMetricSnapshot, expectedCount int) []string {
	var failures []string
	if len(samples) != expectedCount {
		return []string{fmt.Sprintf("snapshot count %d does not match %d", len(samples), expectedCount)}
	}
	processStart := samples[0].ProcessStartTimeSeconds
	previousCPU := -1.0
	for index, sample := range samples {
		if !qualificationFiniteNonNegative(sample.ProcessStartTimeSeconds) || sample.ProcessStartTimeSeconds <= 0 {
			failures = append(failures, fmt.Sprintf("snapshot %d process identity is not finite and positive", index))
		} else if sample.ProcessStartTimeSeconds != processStart {
			failures = append(failures, fmt.Sprintf("snapshot %d changed process identity", index))
		}
		if !qualificationFiniteNonNegative(sample.CPUSeconds) {
			failures = append(failures, fmt.Sprintf("snapshot %d CPU counter is not finite and non-negative", index))
		} else if previousCPU >= 0 && sample.CPUSeconds < previousCPU {
			failures = append(failures, fmt.Sprintf("snapshot %d CPU counter fell", index))
		}
		if sample.ResidentMemoryBytes < 0 || sample.Goroutines < 0 || sample.OpenConnections < 0 {
			failures = append(failures, fmt.Sprintf("snapshot %d integer gauge is negative", index))
		}
		previousCPU = sample.CPUSeconds
	}
	return failures
}

// qualificationFixtureManifestDigest canonicalizes sha256sum output from the
// bundled evaluation project and data roots. The path is part of the digest so
// replacing or moving a file cannot leave the fixture identity unchanged.
func qualificationFixtureManifestDigest(manifest []byte) (string, error) {
	entries := make([]qualificationFixtureManifestEntry, 0)
	seen := make(map[string]struct{})
	hasProject := false
	hasData := false
	for _, line := range strings.Split(strings.TrimSuffix(string(manifest), "\n"), "\n") {
		if len(line) < 66 || line[64:66] != "  " {
			return "", fmt.Errorf("fixture manifest entry is malformed")
		}
		digest, ok := qualificationNormalizeDigest(line[:64])
		if !ok {
			return "", fmt.Errorf("fixture manifest entry digest is malformed")
		}
		path := line[66:]
		const appRoot = "/app/"
		if !strings.HasPrefix(path, appRoot) {
			return "", fmt.Errorf("fixture manifest path is outside /app")
		}
		relative := strings.TrimPrefix(path, appRoot)
		if relative == "" || strings.HasPrefix(relative, "/") {
			return "", fmt.Errorf("fixture manifest path is empty")
		}
		parts := strings.Split(relative, "/")
		for _, part := range parts {
			if part == "" || part == "." || part == ".." {
				return "", fmt.Errorf("fixture manifest path is not normalized")
			}
		}
		if relative == qualificationPerformanceFixturePath || strings.HasPrefix(relative, qualificationPerformanceDataRoot+"/") {
			hasData = true
		}
		if strings.HasPrefix(relative, qualificationPerformanceProjectRoot+"/") {
			hasProject = true
		}
		if _, exists := seen[relative]; exists {
			return "", fmt.Errorf("fixture manifest contains duplicate path %s", relative)
		}
		seen[relative] = struct{}{}
		entries = append(entries, qualificationFixtureManifestEntry{Path: relative, Digest: digest})
	}
	if len(entries) == 0 || !hasProject || !hasData {
		return "", fmt.Errorf("fixture manifest must contain project and data files")
	}
	orderFixture := false
	for _, entry := range entries {
		if entry.Path == qualificationPerformanceFixturePath {
			orderFixture = true
			break
		}
	}
	if !orderFixture {
		return "", fmt.Errorf("fixture manifest does not contain %s", qualificationPerformanceFixturePath)
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Path < entries[right].Path })
	var canonical strings.Builder
	for _, entry := range entries {
		canonical.WriteString(entry.Path)
		canonical.WriteByte('\t')
		canonical.WriteString(entry.Digest)
		canonical.WriteByte('\n')
	}
	return qualificationDigest([]byte(canonical.String())), nil
}

func validateQualificationPerformancePolicy(policy qualificationPerformancePolicy) []string {
	var failures []string
	if policy.SchemaVersion != qualificationPerformanceSchema {
		failures = append(failures, fmt.Sprintf("schemaVersion must be %d", qualificationPerformanceSchema))
	}
	if strings.TrimSpace(policy.Workload) == "" {
		failures = append(failures, "workload must be a non-empty string")
	}
	if strings.TrimSpace(policy.Fixture) == "" {
		failures = append(failures, "fixture must be a non-empty string")
	} else if strings.TrimSpace(policy.Fixture) != qualificationPerformanceFixturePath {
		failures = append(failures, "fixture must identify "+qualificationPerformanceFixturePath)
	}
	if strings.TrimSpace(policy.SampleProtocol) == "" {
		failures = append(failures, "sampleProtocol must be a non-empty string")
	} else if strings.TrimSpace(policy.SampleProtocol) != qualificationPerformanceSampleProtocol {
		failures = append(failures, "sampleProtocol is not supported")
	}
	if policy.Assumptions.MinimumLogicalCPUs <= 0 {
		failures = append(failures, "assumptions.minimumLogicalCPUs must be greater than 0")
	}
	if policy.Assumptions.MinimumMemoryBytes <= 0 {
		failures = append(failures, "assumptions.minimumMemoryBytes must be greater than 0")
	}
	if strings.TrimSpace(policy.Assumptions.Runtime) == "" {
		failures = append(failures, "assumptions.runtime must be a non-empty string")
	}
	if strings.TrimSpace(policy.Assumptions.Dataset.Name) == "" || policy.Assumptions.Dataset.Orders <= 0 {
		failures = append(failures, "assumptions.dataset must identify a positive order count")
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
		if math.IsNaN(value) || math.IsInf(value, 0) {
			failures = append(failures, "budgets."+field+" must be finite")
		} else if value < 0 {
			failures = append(failures, "budgets."+field+" must be at least 0")
		}
	}
	if math.IsNaN(policy.Budgets.ErrorRateMax) || math.IsInf(policy.Budgets.ErrorRateMax, 0) ||
		policy.Budgets.ErrorRateMax < 0 || policy.Budgets.ErrorRateMax > 1 {
		failures = append(failures, "budgets.errorRateMax must be between 0 and 1")
	}
	if math.IsNaN(policy.Comparison.MaxRegressionRatio) || math.IsInf(policy.Comparison.MaxRegressionRatio, 0) ||
		policy.Comparison.MaxRegressionRatio <= 1 {
		failures = append(failures, "comparison.maxRegressionRatio must be greater than 1")
	}
	if math.IsNaN(policy.Comparison.MinimumMeaningfulLatencyDeltaMs) || math.IsInf(policy.Comparison.MinimumMeaningfulLatencyDeltaMs, 0) ||
		policy.Comparison.MinimumMeaningfulLatencyDeltaMs < 0 {
		failures = append(failures, "comparison.minimumMeaningfulLatencyDeltaMs must be at least 0")
	}
	return failures
}

func qualificationPerformanceExpectedSamples(policy qualificationPerformancePolicy, field string) int {
	samples := policy.Assumptions.Samples
	switch field {
	case "coldDashboardReadyMs":
		return samples.ColdDashboardLoads
	case "warmDashboardReadyMs":
		return samples.WarmDashboardLoads
	case "filterToSettleMs":
		return samples.FilterInteractions
	case "tableInteractionMs":
		return samples.TableInteractions
	case "governedQueryMs":
		return samples.GovernedQueries
	case "refreshMs":
		return samples.RefreshRuns
	case "concurrentQueryMs":
		return samples.ConcurrentReaders
	default:
		return 0
	}
}

func qualificationPerformanceExpectedOperations(policy qualificationPerformancePolicy) int {
	samples := policy.Assumptions.Samples
	return samples.WarmDashboardLoads + samples.FilterInteractions + samples.TableInteractions +
		samples.GovernedQueries + samples.RefreshRuns + samples.ConcurrentReaders
}

func qualificationPerformanceExpectedRequests(policy qualificationPerformancePolicy) int {
	return qualificationPerformanceExpectedOperations(policy)
}

func qualificationFiniteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func qualificationParseEffectiveAppLimits(output string, fallbackCPUs int64) (float64, int64, error) {
	fields := strings.Fields(output)
	if len(fields) != 4 {
		return 0, 0, fmt.Errorf("effective app limits must contain nanoCPUs, CPU quota, CPU period, and memory")
	}
	values := make([]int64, len(fields))
	for index, field := range fields {
		value, err := strconv.ParseInt(field, 10, 64)
		if err != nil || value < 0 {
			return 0, 0, fmt.Errorf("effective app limit %q is not a non-negative integer", field)
		}
		values[index] = value
	}
	if fallbackCPUs <= 0 {
		return 0, 0, fmt.Errorf("fallback logical CPUs must be positive")
	}
	var cpus float64
	switch {
	case values[0] > 0:
		cpus = float64(values[0]) / 1_000_000_000
	case values[1] > 0 || values[2] > 0:
		if values[1] <= 0 || values[2] <= 0 {
			return 0, 0, fmt.Errorf("effective app CPU quota and period must be provided together")
		}
		cpus = float64(values[1]) / float64(values[2])
	default:
		cpus = float64(fallbackCPUs)
	}
	if !qualificationFiniteNonNegative(cpus) || cpus <= 0 || values[3] <= 0 {
		return 0, 0, fmt.Errorf("effective app limits must be finite and positive")
	}
	return cpus, values[3], nil
}

func qualificationPercentile(values []float64, rank float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	index := int(math.Ceil((rank/100)*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return roundQualificationFloat(sorted[index])
}

func qualificationDurationSummaryFromSamples(values []float64) qualificationDurationSummary {
	result := qualificationDurationSummary{Samples: len(values)}
	if len(values) == 0 {
		return result
	}
	result.P50 = qualificationPercentile(values, 50)
	result.P95 = qualificationPercentile(values, 95)
	maxValue := values[0]
	for _, value := range values[1:] {
		if value > maxValue {
			maxValue = value
		}
	}
	result.Max = roundQualificationFloat(maxValue)
	return result
}

func qualificationSummaryEqual(left, right qualificationDurationSummary) bool {
	return left.Samples == right.Samples && left.P50 == right.P50 && left.P95 == right.P95 && left.Max == right.Max
}

func evaluateQualificationPerformance(report qualificationPerformanceReport, policy qualificationPerformancePolicy) []string {
	var failures []string
	for _, phase := range qualificationLatencyPhases {
		summary, ok := report.Latency[phase.Field]
		if !ok {
			failures = append(failures, phase.Label+" latency summary is missing")
			continue
		}
		expected := qualificationPerformanceExpectedSamples(policy, phase.Field)
		if summary.Samples != expected {
			failures = append(failures, fmt.Sprintf("%s latency samples %d does not match %d", phase.Label, summary.Samples, expected))
		}
		if !qualificationFiniteNonNegative(summary.P50) || !qualificationFiniteNonNegative(summary.P95) || !qualificationFiniteNonNegative(summary.Max) {
			failures = append(failures, phase.Label+" latency summary contains a non-finite or negative value")
			continue
		}
		if summary.P50 > summary.P95 || summary.P95 > summary.Max {
			failures = append(failures, phase.Label+" latency summary percentiles are not ordered")
		}
		actual := summary.P95
		limit := phase.Budget(policy)
		if actual > limit {
			failures = append(failures, fmt.Sprintf("%s p95 %vms exceeds %vms", phase.Label, actual, limit))
		}
	}
	errorRate := 1.0
	if report.Reliability.Requests > 0 {
		errorRate = float64(report.Reliability.Errors) / float64(report.Reliability.Requests)
	}
	if report.Reliability.Requests <= 0 || report.Reliability.Errors < 0 || report.Reliability.Errors > report.Reliability.Requests {
		failures = append(failures, "reliability request/error counts are invalid")
	} else if errorRate > policy.Budgets.ErrorRateMax {
		failures = append(failures, fmt.Sprintf("request error rate %v exceeds %v", roundQualificationFloat(errorRate), policy.Budgets.ErrorRateMax))
	}
	resources := report.Resources
	if resources.PeakResidentMemoryBytes < 0 || resources.CPUSeconds < 0 || math.IsNaN(resources.CPUSeconds) || math.IsInf(resources.CPUSeconds, 0) || resources.TemporaryDiskGrowthBytes < 0 || resources.GoroutinesBefore < 0 || resources.GoroutinesAfter < 0 || resources.PeakOpenConnections < 0 {
		failures = append(failures, "resource measurements are invalid")
	}
	if resources.PeakResidentMemoryBytes > policy.Budgets.PeakResidentMemoryBytes {
		failures = append(failures, fmt.Sprintf("peak resident memory %d bytes exceeds %d bytes", resources.PeakResidentMemoryBytes, policy.Budgets.PeakResidentMemoryBytes))
	}
	if resources.CPUSeconds > policy.Budgets.CPUSecondsMax {
		failures = append(failures, fmt.Sprintf("CPU consumption %vs exceeds %vs", resources.CPUSeconds, policy.Budgets.CPUSecondsMax))
	}
	if resources.TemporaryDiskGrowthBytes > policy.Budgets.TemporaryDiskGrowthBytesMax {
		failures = append(failures, fmt.Sprintf("temporary disk growth %d bytes exceeds %d bytes", resources.TemporaryDiskGrowthBytes, policy.Budgets.TemporaryDiskGrowthBytesMax))
	}
	if growth := resources.GoroutinesAfter - resources.GoroutinesBefore; growth > policy.Budgets.GoroutineGrowthMax {
		failures = append(failures, fmt.Sprintf("steady-state goroutine growth %d exceeds %d", growth, policy.Budgets.GoroutineGrowthMax))
	}
	if resources.PeakOpenConnections > policy.Budgets.OpenConnectionsMax {
		failures = append(failures, fmt.Sprintf("peak open connections %d exceeds %d", resources.PeakOpenConnections, policy.Budgets.OpenConnectionsMax))
	}
	return failures
}
