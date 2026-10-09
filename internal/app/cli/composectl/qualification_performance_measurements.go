package composectl

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
)

func validateQualificationPerformanceProtocol(report qualificationPerformanceReport, policy qualificationPerformancePolicy) []string {
	var failures []string
	if report.SchemaVersion != 1 {
		failures = append(failures, fmt.Sprintf("schemaVersion must be 1, got %d", report.SchemaVersion))
	}
	if report.Policy.SchemaVersion != policy.SchemaVersion {
		failures = append(failures, "policy.schemaVersion does not match the measurement protocol")
	}
	if report.Policy.Workload != policy.Workload {
		failures = append(failures, "policy.workload does not match the measurement protocol")
	}
	if report.Policy.Assumptions.Samples != policy.Assumptions.Samples {
		failures = append(failures, "policy.assumptions.samples does not match the measurement protocol")
	}
	return failures
}

func validateQualificationPerformanceMetricEvidence(report qualificationPerformanceReport, policy qualificationPerformancePolicy) []string {
	failures := validateQualificationPerformanceLatencies(report, policy)
	failures = append(failures, validateQualificationPerformanceMeasurements(report, policy)...)
	return append(failures, validateQualificationResources(report.Resources, policy)...)
}

func validateQualificationPerformanceMeasurements(report qualificationPerformanceReport, policy qualificationPerformancePolicy) []string {
	var failures []string
	expectedSamples := qualificationPerformanceSampleCounts(policy)
	var samples map[string]json.RawMessage
	if err := json.Unmarshal(report.Samples, &samples); err != nil || samples == nil {
		failures = append(failures, "samples must be an object containing every measured latency phase")
	}
	for _, phase := range qualificationLatencyPhases {
		path := "samples." + phase.Field
		var observations []*float64
		if err := json.Unmarshal(samples[phase.Field], &observations); err != nil || observations == nil {
			failures = append(failures, path+" must be a measured duration array")
			continue
		}
		expected := expectedSamples[phase.Field]
		if len(observations) != expected {
			failures = append(failures, fmt.Sprintf("%s has %d samples, expected %d", path, len(observations), expected))
			continue
		}
		values := make([]float64, len(observations))
		valid := true
		for index, value := range observations {
			if value == nil || *value < 0 || math.IsNaN(*value) || math.IsInf(*value, 0) {
				failures = append(failures, fmt.Sprintf("%s[%d] must be a finite nonnegative measured duration", path, index))
				valid = false
				continue
			}
			values[index] = *value
		}
		if !valid || len(values) == 0 {
			continue
		}
		slices.Sort(values)
		summary := report.Latency[phase.Field]
		for _, metric := range []struct {
			name             string
			actual, measured float64
		}{
			{"p50", summary.P50, qualificationMeasuredPercentile(values, 50)},
			{"p95", summary.P95, qualificationMeasuredPercentile(values, 95)},
			{"max", summary.Max, roundQualificationFloat(values[len(values)-1])},
		} {
			if metric.actual != metric.measured {
				failures = append(failures, fmt.Sprintf("latency.%s.%s %v does not match measured samples %v", phase.Field, metric.name, metric.actual, metric.measured))
			}
		}
	}
	var concurrency struct {
		Readers *int     `json:"readers"`
		WaveMs  *float64 `json:"waveMs"`
	}
	if err := json.Unmarshal(report.Concurrency, &concurrency); err != nil {
		failures = append(failures, "concurrency must contain measured readers and waveMs: "+err.Error())
	}
	if concurrency.Readers == nil || *concurrency.Readers != policy.Assumptions.Samples.ConcurrentReaders {
		failures = append(failures, "concurrency.readers must match policy.assumptions.samples.concurrentReaders")
	}
	if concurrency.WaveMs == nil || *concurrency.WaveMs < 0 || math.IsNaN(*concurrency.WaveMs) || math.IsInf(*concurrency.WaveMs, 0) {
		failures = append(failures, "concurrency.waveMs must be a finite nonnegative measured duration")
	} else if *concurrency.WaveMs < report.Latency["concurrentQueryMs"].Max {
		failures = append(failures, "concurrency.waveMs must cover the longest measured concurrent query")
	}
	// Requests count operations and refresh polls. Errors additionally count
	// console/response observations, so they are not bounded by requests.
	counts := policy.Assumptions.Samples
	minimumRequests := counts.WarmDashboardLoads + counts.FilterInteractions + counts.TableInteractions + counts.GovernedQueries + 2*counts.RefreshRuns + counts.ConcurrentReaders
	if report.Reliability.Requests == nil {
		failures = append(failures, "reliability.requests must be a measured operation count")
	} else if *report.Reliability.Requests < minimumRequests {
		failures = append(failures, fmt.Sprintf("reliability.requests must cover at least %d measured operations and refresh polls, got %d", minimumRequests, *report.Reliability.Requests))
	}
	if report.Reliability.Errors == nil || *report.Reliability.Errors < 0 {
		failures = append(failures, "reliability.errors must be a measured nonnegative count")
	}
	return failures
}

// The worker uses nearest-rank percentiles and rounds only the result to 0.01ms.
func qualificationMeasuredPercentile(sorted []float64, rank int) float64 {
	index := (rank*len(sorted)+99)/100 - 1
	return roundQualificationFloat(sorted[index])
}

func qualificationPerformanceSampleCounts(policy qualificationPerformancePolicy) map[string]int {
	samples := policy.Assumptions.Samples
	return map[string]int{
		"coldDashboardReadyMs": samples.ColdDashboardLoads,
		"warmDashboardReadyMs": samples.WarmDashboardLoads,
		"filterToSettleMs":     samples.FilterInteractions,
		"tableInteractionMs":   samples.TableInteractions,
		"governedQueryMs":      samples.GovernedQueries,
		"refreshMs":            samples.RefreshRuns,
		"concurrentQueryMs":    samples.ConcurrentReaders,
	}
}
