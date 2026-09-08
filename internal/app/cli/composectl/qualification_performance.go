package composectl

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"
)

const (
	qualificationPerformanceSchema         = 1
	qualificationPerformanceSampleProtocol = "qualification-performance-v1"
	qualificationPerformanceModeBootstrap  = "bootstrap"
	qualificationPerformanceModeCompare    = "compare"
	qualificationPerformanceFixturePath    = "evaluation/data/orders.csv"
	qualificationPerformanceProjectRoot    = "evaluation/project"
	qualificationPerformanceDataRoot       = "evaluation/data"
	qualificationPerformanceMetricSamples  = 8
)

type qualificationPerformancePolicy struct {
	SchemaVersion  int    `json:"schemaVersion"`
	Workload       string `json:"workload"`
	Fixture        string `json:"fixture"`
	SampleProtocol string `json:"sampleProtocol"`
	Assumptions    struct {
		Runtime string `json:"runtime"`
		Dataset struct {
			Name   string `json:"name"`
			Orders int64  `json:"orders"`
		} `json:"dataset"`
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

type qualificationPerformanceToolchain struct {
	BrowserImage string `json:"browserImage"`
	Node         string `json:"node"`
	Playwright   string `json:"playwright"`
	PackageHash  string `json:"packageHash"`
	HarnessHash  string `json:"harnessHash"`
}

type qualificationPerformanceEnvironmentIdentity struct {
	Architecture              string           `json:"architecture"`
	Runtime                   string           `json:"runtime"`
	CPUModel                  string           `json:"cpuModel"`
	Kernel                    string           `json:"kernel"`
	LogicalCPUs               int64            `json:"logicalCPUs"`
	MemoryBytes               int64            `json:"memoryBytes"`
	EffectiveCPULimit         float64          `json:"effectiveCPULimit"`
	EffectiveMemoryLimitBytes int64            `json:"effectiveMemoryLimitBytes"`
	Dataset                   map[string]int64 `json:"dataset"`
}

type qualificationPerformanceMetadata struct {
	Commit                 string                            `json:"commit"`
	FixtureDigest          string                            `json:"fixtureDigest"`
	PolicyDigest           string                            `json:"policyDigest"`
	SampleProtocol         string                            `json:"sampleProtocol"`
	Toolchain              qualificationPerformanceToolchain `json:"toolchain"`
	EnvironmentFingerprint string                            `json:"environmentFingerprint"`
}

type qualificationPerformanceReport struct {
	SchemaVersion int                                     `json:"schemaVersion"`
	GeneratedAt   string                                  `json:"generatedAt"`
	Policy        qualificationPerformancePolicy          `json:"policy"`
	Latency       map[string]qualificationDurationSummary `json:"latency"`
	Reliability   struct {
		Requests   int      `json:"requests"`
		Operations int      `json:"operations"`
		Errors     int      `json:"errors"`
		Failures   []string `json:"failures"`
	} `json:"reliability"`
	Resources struct {
		PeakResidentMemoryBytes  int64   `json:"peakResidentMemoryBytes"`
		CPUSeconds               float64 `json:"cpuSeconds"`
		MetricSamples            int64   `json:"metricSamples"`
		TemporaryDiskBeforeBytes int64   `json:"temporaryDiskBeforeBytes"`
		TemporaryDiskAfterBytes  int64   `json:"temporaryDiskAfterBytes"`
		TemporaryDiskGrowthBytes int64   `json:"temporaryDiskGrowthBytes"`
		GoroutinesBefore         int64   `json:"goroutinesBefore"`
		GoroutinesAfter          int64   `json:"goroutinesAfter"`
		PeakOpenConnections      int64   `json:"peakOpenConnections"`
	} `json:"resources"`
	Samples     json.RawMessage `json:"samples,omitempty"`
	Concurrency json.RawMessage `json:"concurrency,omitempty"`
	Environment struct {
		Runtime                   string           `json:"runtime"`
		CPUModel                  string           `json:"cpuModel"`
		Kernel                    string           `json:"kernel"`
		LogicalCPUs               int64            `json:"logicalCPUs"`
		MemoryBytes               int64            `json:"memoryBytes"`
		EffectiveCPULimit         float64          `json:"effectiveCPULimit"`
		EffectiveMemoryLimitBytes int64            `json:"effectiveMemoryLimitBytes"`
		Dataset                   map[string]int64 `json:"dataset"`
	} `json:"environment"`
	Commit                 string                            `json:"commit"`
	Image                  string                            `json:"image"`
	Architecture           string                            `json:"architecture"`
	Fixture                string                            `json:"fixture"`
	FixtureDigest          string                            `json:"fixtureDigest"`
	PolicyDigest           string                            `json:"policyDigest"`
	SampleProtocol         string                            `json:"sampleProtocol"`
	Toolchain              qualificationPerformanceToolchain `json:"toolchain"`
	EnvironmentFingerprint string                            `json:"environmentFingerprint"`
	Comparison             struct {
		Mode                            string   `json:"mode"`
		Baseline                        *string  `json:"baseline"`
		BaselineCommit                  string   `json:"baselineCommit,omitempty"`
		BaselineImage                   string   `json:"baselineImage,omitempty"`
		BaselineFixtureDigest           string   `json:"baselineFixtureDigest,omitempty"`
		BaselinePolicyDigest            string   `json:"baselinePolicyDigest,omitempty"`
		BaselineSampleProtocol          string   `json:"baselineSampleProtocol,omitempty"`
		BaselineEnvironmentFingerprint  string   `json:"baselineEnvironmentFingerprint,omitempty"`
		MaxRegressionRatio              float64  `json:"maxRegressionRatio"`
		MinimumMeaningfulLatencyDeltaMs float64  `json:"minimumMeaningfulLatencyDeltaMs"`
		Failures                        []string `json:"failures"`
	} `json:"comparison"`
	Assertions struct {
		Environment         bool `json:"environment"`
		AbsoluteBudgets     bool `json:"absoluteBudgets"`
		ComparisonTolerance bool `json:"comparisonTolerance"`
		ErrorFree           bool `json:"errorFree"`
		EvidenceIdentity    bool `json:"evidenceIdentity"`
	} `json:"assertions"`
	Failures      []string        `json:"failures"`
	Result        string          `json:"result"`
	fieldPresence map[string]bool `json:"-"`
}

func (r *qualificationPerformanceReport) UnmarshalJSON(data []byte) error {
	type reportAlias qualificationPerformanceReport
	var decoded reportAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*r = qualificationPerformanceReport(decoded)
	r.fieldPresence = make(map[string]bool)
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	nested := func(name string) map[string]json.RawMessage {
		var value map[string]json.RawMessage
		if raw, ok := root[name]; ok && json.Unmarshal(raw, &value) == nil {
			return value
		}
		return nil
	}
	for _, field := range []string{"requests", "operations", "errors", "failures"} {
		raw, ok := nested("reliability")[field]
		if field == "failures" {
			var failures []string
			if ok && string(raw) != "null" && json.Unmarshal(raw, &failures) == nil && failures != nil {
				r.fieldPresence["reliability."+field] = true
			}
		} else if ok {
			r.fieldPresence["reliability."+field] = true
		}
	}
	for _, field := range []string{"peakResidentMemoryBytes", "cpuSeconds", "metricSamples", "temporaryDiskBeforeBytes", "temporaryDiskAfterBytes", "temporaryDiskGrowthBytes", "goroutinesBefore", "goroutinesAfter", "peakOpenConnections"} {
		if qualificationJSONFieldPresent(nested("resources"), field) {
			r.fieldPresence["resources."+field] = true
		}
	}
	for _, field := range []string{"runtime", "cpuModel", "kernel", "logicalCPUs", "memoryBytes", "effectiveCPULimit", "effectiveMemoryLimitBytes", "dataset"} {
		if qualificationJSONFieldPresent(nested("environment"), field) {
			r.fieldPresence["environment."+field] = true
		}
	}
	var latencies map[string]json.RawMessage
	if raw, ok := root["latency"]; ok && json.Unmarshal(raw, &latencies) == nil {
		for _, phase := range qualificationLatencyPhases {
			var summary map[string]json.RawMessage
			if rawSummary, ok := latencies[phase.Field]; ok && json.Unmarshal(rawSummary, &summary) == nil {
				for _, field := range []string{"samples", "p50", "p95", "max"} {
					if qualificationJSONFieldPresent(summary, field) {
						r.fieldPresence["latency."+phase.Field+"."+field] = true
					}
				}
			}
		}
	}
	return nil
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

func qualificationPerformanceSampleProtocolForPolicy(policy qualificationPerformancePolicy) string {
	if protocol := strings.TrimSpace(policy.SampleProtocol); protocol != "" {
		return protocol
	}
	return qualificationPerformanceSampleProtocol
}

func qualificationDigest(value []byte) string {
	hash := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func qualificationDigestFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func qualificationDigestValid(value string) bool {
	value = strings.TrimPrefix(strings.TrimSpace(value), "sha256:")
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func qualificationPerformanceRevisionValid(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 40 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func qualificationNormalizeDigest(value string) (string, bool) {
	raw := strings.TrimPrefix(strings.TrimSpace(value), "sha256:")
	if len(raw) != sha256.Size*2 || !qualificationDigestValid(raw) {
		return "", false
	}
	return "sha256:" + raw, true
}

func qualificationPerformanceEnvironmentFingerprint(report qualificationPerformanceReport) string {
	identity := qualificationPerformanceEnvironmentIdentity{
		Architecture:              report.Architecture,
		Runtime:                   report.Environment.Runtime,
		CPUModel:                  report.Environment.CPUModel,
		Kernel:                    report.Environment.Kernel,
		LogicalCPUs:               report.Environment.LogicalCPUs,
		MemoryBytes:               report.Environment.MemoryBytes,
		EffectiveCPULimit:         report.Environment.EffectiveCPULimit,
		EffectiveMemoryLimitBytes: report.Environment.EffectiveMemoryLimitBytes,
		Dataset:                   report.Environment.Dataset,
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return ""
	}
	return qualificationDigest(encoded)
}

func validateQualificationPerformanceEvidence(report qualificationPerformanceReport, policy qualificationPerformancePolicy) []string {
	var failures []string
	if report.SchemaVersion != qualificationPerformanceSchema {
		failures = append(failures, "report schemaVersion is missing or unsupported")
	}
	if strings.TrimSpace(report.GeneratedAt) == "" {
		failures = append(failures, "report generatedAt is required")
	} else if _, err := time.Parse(time.RFC3339Nano, report.GeneratedAt); err != nil {
		failures = append(failures, "report generatedAt is invalid")
	}
	for field, value := range map[string]string{
		"commit":                 report.Commit,
		"image":                  report.Image,
		"fixtureDigest":          report.FixtureDigest,
		"policyDigest":           report.PolicyDigest,
		"sampleProtocol":         report.SampleProtocol,
		"environmentFingerprint": report.EnvironmentFingerprint,
	} {
		if strings.TrimSpace(value) == "" {
			failures = append(failures, "evidence "+field+" is required")
		}
	}
	for field, value := range map[string]string{
		"fixtureDigest":          report.FixtureDigest,
		"policyDigest":           report.PolicyDigest,
		"toolchain.browserImage": report.Toolchain.BrowserImage,
		"toolchain.packageHash":  report.Toolchain.PackageHash,
		"toolchain.harnessHash":  report.Toolchain.HarnessHash,
	} {
		if strings.TrimSpace(value) != "" && !qualificationDigestValid(value) {
			failures = append(failures, "evidence "+field+" is not a sha256 digest")
		}
	}
	if report.SampleProtocol != qualificationPerformanceSampleProtocolForPolicy(policy) {
		failures = append(failures, "evidence sampleProtocol does not match policy")
	}
	if report.Fixture != policy.Fixture {
		failures = append(failures, "evidence fixture does not match policy fixture")
	}
	if strings.TrimSpace(report.Architecture) == "" {
		failures = append(failures, "evidence architecture is required")
	}
	if strings.TrimSpace(report.Toolchain.BrowserImage) == "" || strings.TrimSpace(report.Toolchain.Node) == "" || strings.TrimSpace(report.Toolchain.Playwright) == "" || strings.TrimSpace(report.Toolchain.PackageHash) == "" || strings.TrimSpace(report.Toolchain.HarnessHash) == "" {
		failures = append(failures, "evidence toolchain identity is incomplete")
	}
	if strings.TrimSpace(report.Environment.Runtime) == "" || strings.TrimSpace(report.Environment.CPUModel) == "" || strings.TrimSpace(report.Environment.Kernel) == "" || report.Environment.LogicalCPUs <= 0 || report.Environment.MemoryBytes <= 0 || !qualificationFiniteNonNegative(report.Environment.EffectiveCPULimit) || report.Environment.EffectiveCPULimit <= 0 || report.Environment.EffectiveMemoryLimitBytes <= 0 {
		failures = append(failures, "evidence environment identity is incomplete")
	}
	if expectedFingerprint := qualificationPerformanceEnvironmentFingerprint(report); expectedFingerprint == "" || report.EnvironmentFingerprint != expectedFingerprint {
		failures = append(failures, "evidence environment fingerprint does not match its canonical identity")
	}
	if len(report.Environment.Dataset) == 0 {
		failures = append(failures, "evidence environment dataset is empty")
	}
	for name, value := range report.Environment.Dataset {
		if strings.TrimSpace(name) == "" || value < 0 {
			failures = append(failures, "evidence environment dataset contains an invalid value")
			break
		}
	}
	if policy.Assumptions.Dataset.Orders > 0 && report.Environment.Dataset["orders"] != policy.Assumptions.Dataset.Orders {
		failures = append(failures, fmt.Sprintf("evidence dataset orders %d does not match %d", report.Environment.Dataset["orders"], policy.Assumptions.Dataset.Orders))
	}
	for _, phase := range qualificationLatencyPhases {
		summary, ok := report.Latency[phase.Field]
		if !ok {
			failures = append(failures, phase.Label+" latency summary is missing")
			continue
		}
		for _, field := range []string{"samples", "p50", "p95", "max"} {
			if !report.fieldPresence["latency."+phase.Field+"."+field] {
				failures = append(failures, fmt.Sprintf("%s latency %s is missing", phase.Label, field))
			}
		}
		if summary.Samples != qualificationPerformanceExpectedSamples(policy, phase.Field) {
			failures = append(failures, fmt.Sprintf("%s latency sample count %d does not match %d", phase.Label, summary.Samples, qualificationPerformanceExpectedSamples(policy, phase.Field)))
		}
		for name, value := range map[string]float64{"p50": summary.P50, "p95": summary.P95, "max": summary.Max} {
			if !qualificationFiniteNonNegative(value) {
				failures = append(failures, fmt.Sprintf("%s latency %s is not finite and non-negative", phase.Label, name))
			}
		}
		if summary.P50 > summary.P95 || summary.P95 > summary.Max {
			failures = append(failures, phase.Label+" latency summary percentiles are not ordered")
		}
	}
	expectedOperations := qualificationPerformanceExpectedOperations(policy)
	for _, field := range []string{"requests", "operations", "errors", "failures"} {
		if !report.fieldPresence["reliability."+field] {
			failures = append(failures, "reliability."+field+" is missing")
		}
	}
	for _, field := range []string{"peakResidentMemoryBytes", "cpuSeconds", "metricSamples", "temporaryDiskBeforeBytes", "temporaryDiskAfterBytes", "temporaryDiskGrowthBytes", "goroutinesBefore", "goroutinesAfter", "peakOpenConnections"} {
		if !report.fieldPresence["resources."+field] {
			failures = append(failures, "resources."+field+" is missing")
		}
	}
	for _, field := range []string{"runtime", "cpuModel", "kernel", "logicalCPUs", "memoryBytes", "effectiveCPULimit", "effectiveMemoryLimitBytes", "dataset"} {
		if !report.fieldPresence["environment."+field] {
			failures = append(failures, "environment."+field+" is missing")
		}
	}
	if report.Reliability.Operations != expectedOperations {
		failures = append(failures, fmt.Sprintf("reliability operation count %d does not match %d", report.Reliability.Operations, expectedOperations))
	}
	if report.Reliability.Requests < report.Reliability.Operations {
		failures = append(failures, fmt.Sprintf("reliability request count %d is below operation count %d", report.Reliability.Requests, report.Reliability.Operations))
	}
	if report.Reliability.Errors < 0 || report.Reliability.Errors > report.Reliability.Requests {
		failures = append(failures, "reliability error count is invalid")
	}
	if report.Reliability.Errors == 0 && len(report.Reliability.Failures) > 0 {
		failures = append(failures, "reliability failures are present with zero errors")
	}
	resources := report.Resources
	if resources.MetricSamples != qualificationPerformanceMetricSamples {
		failures = append(failures, fmt.Sprintf("resource metric sample count %d does not match %d", resources.MetricSamples, qualificationPerformanceMetricSamples))
	}
	if resources.PeakResidentMemoryBytes < 0 || !qualificationFiniteNonNegative(resources.CPUSeconds) || resources.TemporaryDiskBeforeBytes < 0 || resources.TemporaryDiskAfterBytes < 0 || resources.TemporaryDiskGrowthBytes < 0 || resources.GoroutinesBefore < 0 || resources.GoroutinesAfter < 0 || resources.PeakOpenConnections < 0 {
		failures = append(failures, "resource measurements are missing, negative, or non-finite")
	}
	if expectedGrowth := max(0, resources.TemporaryDiskAfterBytes-resources.TemporaryDiskBeforeBytes); resources.TemporaryDiskGrowthBytes != expectedGrowth {
		failures = append(failures, "temporary disk growth does not match before/after measurements")
	}
	var samples map[string][]float64
	if len(report.Samples) == 0 || json.Unmarshal(report.Samples, &samples) != nil {
		failures = append(failures, "raw latency samples are required and must be an object")
	} else {
		for _, phase := range qualificationLatencyPhases {
			values, ok := samples[phase.Field]
			if !ok || len(values) != qualificationPerformanceExpectedSamples(policy, phase.Field) {
				failures = append(failures, phase.Label+" raw samples are missing or incomplete")
				continue
			}
			for _, value := range values {
				if !qualificationFiniteNonNegative(value) {
					failures = append(failures, phase.Label+" raw samples contain a non-finite or negative value")
					break
				}
			}
			expectedSummary := qualificationDurationSummaryFromSamples(values)
			if summary, ok := report.Latency[phase.Field]; !ok || !qualificationSummaryEqual(summary, expectedSummary) {
				failures = append(failures, phase.Label+" latency summary does not match raw samples")
			}
		}
	}
	var concurrency struct {
		Readers int     `json:"readers"`
		WaveMs  float64 `json:"waveMs"`
	}
	if len(report.Concurrency) == 0 || json.Unmarshal(report.Concurrency, &concurrency) != nil || concurrency.Readers != policy.Assumptions.Samples.ConcurrentReaders || !qualificationFiniteNonNegative(concurrency.WaveMs) {
		failures = append(failures, "concurrency sample protocol is missing or invalid")
	}
	return failures
}

func validateQualificationPerformanceBaselineCompatibility(candidate, baseline qualificationPerformanceReport, policy qualificationPerformancePolicy) []string {
	var failures []string
	if baseline.Result != "success" {
		failures = append(failures, "baseline result must be success")
	}
	if baseline.Policy.Workload != candidate.Policy.Workload || baseline.SampleProtocol != candidate.SampleProtocol {
		failures = append(failures, "baseline workload or sample protocol is incompatible")
	}
	if baseline.FixtureDigest != candidate.FixtureDigest {
		failures = append(failures, "baseline fixture digest is incompatible")
	}
	if baseline.PolicyDigest != candidate.PolicyDigest {
		failures = append(failures, "baseline policy digest is incompatible")
	}
	if baseline.Toolchain != candidate.Toolchain {
		failures = append(failures, "baseline toolchain is incompatible")
	}
	if baseline.EnvironmentFingerprint != candidate.EnvironmentFingerprint {
		failures = append(failures, "baseline environment is incompatible; hardware is not universally comparable")
	}
	return failures
}

func compareQualificationPerformance(candidate, baseline qualificationPerformanceReport, policy qualificationPerformancePolicy) []string {
	var failures []string
	for _, phase := range qualificationLatencyPhases {
		baselineSummary, ok := baseline.Latency[phase.Field]
		if !ok {
			continue
		}
		previous := baselineSummary.P95
		current := candidate.Latency[phase.Field].P95
		if previous < 0 || current < 0 {
			failures = append(failures, phase.Label+" comparison contains a negative p95")
			continue
		}
		if previous == 0 {
			if current > 0 {
				failures = append(failures, phase.Label+" baseline p95 is zero and candidate p95 is positive; comparison is inconclusive")
			}
			continue
		}
		ratio := current / previous
		if ratio > policy.Comparison.MaxRegressionRatio && current-previous >= policy.Comparison.MinimumMeaningfulLatencyDeltaMs {
			failures = append(failures, fmt.Sprintf("%s p95 regressed from %vms to %vms (%vx, limit %vx)", phase.Label, previous, current, roundQualificationFloat(ratio), policy.Comparison.MaxRegressionRatio))
		}
	}
	for _, metric := range []struct {
		label    string
		current  float64
		previous float64
	}{
		{"peak resident memory", float64(candidate.Resources.PeakResidentMemoryBytes), float64(baseline.Resources.PeakResidentMemoryBytes)},
		{"CPU consumption", candidate.Resources.CPUSeconds, baseline.Resources.CPUSeconds},
		{"temporary disk growth", float64(candidate.Resources.TemporaryDiskGrowthBytes), float64(baseline.Resources.TemporaryDiskGrowthBytes)},
		{"goroutine growth", float64(max(0, candidate.Resources.GoroutinesAfter-candidate.Resources.GoroutinesBefore)), float64(max(0, baseline.Resources.GoroutinesAfter-baseline.Resources.GoroutinesBefore))},
		{"peak open connections", float64(candidate.Resources.PeakOpenConnections), float64(baseline.Resources.PeakOpenConnections)},
	} {
		if metric.previous == 0 {
			if metric.current > 0 {
				failures = append(failures, fmt.Sprintf("%s increased from zero baseline to %v and is not comparable", metric.label, metric.current))
			}
			continue
		}
		ratio := metric.current / metric.previous
		if ratio > policy.Comparison.MaxRegressionRatio {
			failures = append(failures, fmt.Sprintf("%s regressed from %v to %v (%vx, limit %vx)", metric.label, metric.previous, metric.current, roundQualificationFloat(ratio), policy.Comparison.MaxRegressionRatio))
		}
	}
	return failures
}

func finalizeQualificationPerformanceReport(path string, policy qualificationPerformancePolicy, diskBefore, diskAfter int64, environmentJSON []byte, image, architecture, baselinePath string, metadata ...qualificationPerformanceMetadata) error {
	var report qualificationPerformanceReport
	if err := readQualificationJSON(path, &report); err != nil {
		return err
	}
	if failures := validateQualificationPerformancePolicy(policy); len(failures) > 0 {
		return fmt.Errorf("invalid performance policy: %s", strings.Join(failures, "; "))
	}
	report.Policy = policy
	report.Fixture = strings.TrimSpace(policy.Fixture)
	if report.fieldPresence == nil {
		report.fieldPresence = make(map[string]bool)
	}
	report.Resources.TemporaryDiskBeforeBytes = diskBefore
	report.Resources.TemporaryDiskAfterBytes = diskAfter
	report.Resources.TemporaryDiskGrowthBytes = max(0, diskAfter-diskBefore)
	for _, field := range []string{"temporaryDiskBeforeBytes", "temporaryDiskAfterBytes", "temporaryDiskGrowthBytes"} {
		report.fieldPresence["resources."+field] = true
	}
	var environmentFields map[string]json.RawMessage
	if err := json.Unmarshal(environmentJSON, &environmentFields); err != nil {
		return fmt.Errorf("decode performance environment: %w", err)
	}
	if err := json.Unmarshal(environmentJSON, &report.Environment); err != nil {
		return fmt.Errorf("decode performance environment: %w", err)
	}
	for _, field := range []string{"runtime", "cpuModel", "kernel", "logicalCPUs", "memoryBytes", "effectiveCPULimit", "effectiveMemoryLimitBytes", "dataset"} {
		delete(report.fieldPresence, "environment."+field)
		if qualificationJSONFieldPresent(environmentFields, field) {
			report.fieldPresence["environment."+field] = true
		}
	}
	report.Image = image
	report.Architecture = architecture
	performanceMetadata := qualificationPerformanceMetadata{}
	if len(metadata) > 0 {
		performanceMetadata = metadata[0]
	}
	if strings.TrimSpace(performanceMetadata.Commit) == "" {
		performanceMetadata.Commit = report.Commit
	}
	if strings.TrimSpace(performanceMetadata.FixtureDigest) == "" {
		performanceMetadata.FixtureDigest = report.FixtureDigest
	}
	if strings.TrimSpace(performanceMetadata.PolicyDigest) == "" {
		canonicalPolicy, _ := json.Marshal(policy)
		performanceMetadata.PolicyDigest = qualificationDigest(canonicalPolicy)
	}
	if strings.TrimSpace(performanceMetadata.SampleProtocol) == "" {
		performanceMetadata.SampleProtocol = report.SampleProtocol
	}
	if strings.TrimSpace(performanceMetadata.SampleProtocol) == "" {
		performanceMetadata.SampleProtocol = qualificationPerformanceSampleProtocolForPolicy(policy)
	}
	if performanceMetadata.Toolchain == (qualificationPerformanceToolchain{}) {
		performanceMetadata.Toolchain = report.Toolchain
	}
	report.Commit = strings.TrimSpace(performanceMetadata.Commit)
	report.FixtureDigest = strings.TrimSpace(performanceMetadata.FixtureDigest)
	report.PolicyDigest = strings.TrimSpace(performanceMetadata.PolicyDigest)
	report.SampleProtocol = strings.TrimSpace(performanceMetadata.SampleProtocol)
	report.Toolchain = performanceMetadata.Toolchain
	report.EnvironmentFingerprint = qualificationPerformanceEnvironmentFingerprint(report)
	identityFailures := validateQualificationPerformanceEvidence(report, policy)
	mode := qualificationPerformanceMode(baselinePath)
	report.Comparison.Mode = mode
	var comparisonFailures []string
	if mode == qualificationPerformanceModeCompare {
		if strings.TrimSpace(baselinePath) == "" {
			comparisonFailures = append(comparisonFailures, "comparison mode requires an explicit baseline")
		} else {
			var baseline qualificationPerformanceReport
			if err := readQualificationJSON(baselinePath, &baseline); err != nil {
				comparisonFailures = append(comparisonFailures, "decode baseline: "+err.Error())
			} else {
				comparisonFailures = append(comparisonFailures, validateQualificationPerformanceEvidence(baseline, policy)...)
				comparisonFailures = append(comparisonFailures, validateQualificationPerformanceBaselineCompatibility(report, baseline, policy)...)
				comparisonFailures = append(comparisonFailures, compareQualificationPerformance(report, baseline, policy)...)
				value := baselinePath
				report.Comparison.Baseline = &value
				report.Comparison.BaselineCommit = baseline.Commit
				report.Comparison.BaselineImage = baseline.Image
				report.Comparison.BaselineFixtureDigest = baseline.FixtureDigest
				report.Comparison.BaselinePolicyDigest = baseline.PolicyDigest
				report.Comparison.BaselineSampleProtocol = baseline.SampleProtocol
				report.Comparison.BaselineEnvironmentFingerprint = baseline.EnvironmentFingerprint
			}
		}
	} else if mode != qualificationPerformanceModeBootstrap {
		comparisonFailures = append(comparisonFailures, "performance comparison mode must be bootstrap or compare")
	} else if strings.TrimSpace(baselinePath) != "" {
		comparisonFailures = append(comparisonFailures, "baseline is not allowed in bootstrap mode")
	}
	report.Comparison.MaxRegressionRatio = policy.Comparison.MaxRegressionRatio
	report.Comparison.MinimumMeaningfulLatencyDeltaMs = policy.Comparison.MinimumMeaningfulLatencyDeltaMs
	report.Comparison.Failures = comparisonFailures
	var environmentFailures []string
	if report.Environment.LogicalCPUs < policy.Assumptions.MinimumLogicalCPUs {
		environmentFailures = append(environmentFailures, fmt.Sprintf("runner has %d logical CPUs, requires %d", report.Environment.LogicalCPUs, policy.Assumptions.MinimumLogicalCPUs))
	}
	if report.Environment.MemoryBytes < policy.Assumptions.MinimumMemoryBytes {
		environmentFailures = append(environmentFailures, fmt.Sprintf("runner has %d bytes of memory, requires %d", report.Environment.MemoryBytes, policy.Assumptions.MinimumMemoryBytes))
	}
	absoluteFailures := evaluateQualificationPerformance(report, policy)
	report.Assertions.Environment = len(environmentFailures) == 0
	report.Assertions.AbsoluteBudgets = len(absoluteFailures) == 0
	report.Assertions.ComparisonTolerance = len(comparisonFailures) == 0
	report.Assertions.ErrorFree = report.Reliability.Errors == 0 && len(report.Reliability.Failures) == 0
	report.Assertions.EvidenceIdentity = len(identityFailures) == 0
	report.Failures = append(report.Failures, identityFailures...)
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
		return fmt.Errorf("installed-candidate performance budgets failed: %s", strings.Join(report.Failures, "; "))
	}
	return nil
}

func qualificationPerformanceMode(_ string) string {
	return strings.ToLower(strings.TrimSpace(os.Getenv("QUALIFICATION_PERFORMANCE_MODE")))
}

func roundQualificationFloat(value float64) float64 {
	return math.Round(value*100) / 100
}

func qualificationPerformanceBaseline() string {
	return strings.TrimSpace(os.Getenv("QUALIFICATION_PERFORMANCE_BASELINE"))
}
