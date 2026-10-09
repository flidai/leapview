package composectl

import (
	"fmt"
	"math"
)

// The worker captures one snapshot before work, after each of the six warm
// phases, and after settling. Restart-cold loads have their own process pair.
const qualificationWarmResourceSamples = 8
const qualificationColdResourceSamples = 2
const qualificationResourceSchemaVersion = 1

type qualificationResourceMeasurement struct {
	CPUSeconds          *float64 `json:"cpuSeconds"`
	ResidentMemoryBytes *int64   `json:"residentMemoryBytes"`
	Goroutines          *int64   `json:"goroutines"`
	OpenConnections     *int64   `json:"openConnections"`
}

type qualificationResourceReport struct {
	SchemaVersion            int                                  `json:"schemaVersion"`
	PeakResidentMemoryBytes  *int64                               `json:"peakResidentMemoryBytes"`
	CPUSeconds               *float64                             `json:"cpuSeconds"`
	GoroutinesBefore         *int64                               `json:"goroutinesBefore"`
	GoroutinesAfter          *int64                               `json:"goroutinesAfter"`
	PeakOpenConnections      *int64                               `json:"peakOpenConnections"`
	MetricSamples            *int                                 `json:"metricSamples"`
	Measurements             []qualificationResourceMeasurement   `json:"measurements"`
	ColdMeasurements         [][]qualificationResourceMeasurement `json:"coldMeasurements"`
	TemporaryDiskBeforeBytes int64                                `json:"temporaryDiskBeforeBytes"`
	TemporaryDiskAfterBytes  int64                                `json:"temporaryDiskAfterBytes"`
	TemporaryDiskGrowthBytes int64                                `json:"temporaryDiskGrowthBytes"`
}

func qualificationResourceValue[T ~int | ~int64 | ~float64](value *T) T {
	if value == nil {
		return 0
	}
	return *value
}

func validateQualificationResources(resources qualificationResourceReport, policy qualificationPerformancePolicy) []string {
	var failures []string
	if resources.SchemaVersion != qualificationResourceSchemaVersion {
		failures = append(failures, "resources.schemaVersion must be 1; aggregate-only resource evidence is not qualified")
	}
	for _, field := range []struct {
		name  string
		value *int64
		min   int64
	}{
		{"peakResidentMemoryBytes", resources.PeakResidentMemoryBytes, 1},
		{"goroutinesBefore", resources.GoroutinesBefore, 1},
		{"goroutinesAfter", resources.GoroutinesAfter, 1},
		{"peakOpenConnections", resources.PeakOpenConnections, 0},
	} {
		if field.value == nil || *field.value < field.min {
			failures = append(failures, fmt.Sprintf("resources.%s must be present and at least %d", field.name, field.min))
		}
	}
	if resources.CPUSeconds == nil || !validQualificationCPU(*resources.CPUSeconds) {
		failures = append(failures, "resources.cpuSeconds must be present, finite and nonnegative")
	}
	if resources.MetricSamples == nil || *resources.MetricSamples != qualificationWarmResourceSamples {
		failures = append(failures, fmt.Sprintf("resources.metricSamples must be present and equal %d", qualificationWarmResourceSamples))
	}
	failures = append(failures, validateQualificationResourceMeasurements("resources.measurements", resources.Measurements, qualificationWarmResourceSamples)...)
	if len(resources.ColdMeasurements) != policy.Assumptions.Samples.ColdDashboardLoads {
		failures = append(failures, fmt.Sprintf("resources.coldMeasurements has %d cold loads, expected %d", len(resources.ColdMeasurements), policy.Assumptions.Samples.ColdDashboardLoads))
		return failures
	}
	for index, measurements := range resources.ColdMeasurements {
		failures = append(failures, validateQualificationResourceMeasurements(fmt.Sprintf("resources.coldMeasurements[%d]", index), measurements, qualificationColdResourceSamples)...)
	}
	if len(failures) > 0 {
		return failures
	}

	// Derive exactly the worker's existing aggregation: RSS across cold/warm,
	// CPU delta per process (cold deltas rounded separately), and warm-only
	// steady-state goroutines/connections. Controller-owned disk is independent.
	warm := resources.Measurements
	coldCPU := 0.0
	peakMemory, peakConnections := int64(0), int64(0)
	for _, sample := range warm {
		peakMemory = max(peakMemory, *sample.ResidentMemoryBytes)
		peakConnections = max(peakConnections, *sample.OpenConnections)
	}
	for _, cold := range resources.ColdMeasurements {
		coldCPU += roundQualificationFloat(*cold[1].CPUSeconds - *cold[0].CPUSeconds)
		for _, sample := range cold {
			peakMemory = max(peakMemory, *sample.ResidentMemoryBytes)
		}
	}
	for _, field := range []struct {
		name             string
		actual, expected int64
	}{
		{"peakResidentMemoryBytes", *resources.PeakResidentMemoryBytes, peakMemory},
		{"goroutinesBefore", *resources.GoroutinesBefore, *warm[0].Goroutines},
		{"goroutinesAfter", *resources.GoroutinesAfter, *warm[len(warm)-1].Goroutines},
		{"peakOpenConnections", *resources.PeakOpenConnections, peakConnections},
	} {
		if field.actual != field.expected {
			failures = append(failures, fmt.Sprintf("resources.%s is %d, measurements require %d", field.name, field.actual, field.expected))
		}
	}
	if expected := roundQualificationFloat(coldCPU + (*warm[len(warm)-1].CPUSeconds - *warm[0].CPUSeconds)); !validQualificationCPU(expected) || *resources.CPUSeconds != expected {
		failures = append(failures, fmt.Sprintf("resources.cpuSeconds is %v, measurements require %v", *resources.CPUSeconds, expected))
	}
	return failures
}

func validQualificationCPU(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validateQualificationResourceMeasurements(path string, measurements []qualificationResourceMeasurement, count int) []string {
	var failures []string
	if len(measurements) != count {
		return []string{fmt.Sprintf("%s has %d samples, expected %d", path, len(measurements), count)}
	}
	for index, sample := range measurements {
		prefix := fmt.Sprintf("%s[%d]", path, index)
		if sample.CPUSeconds == nil || !validQualificationCPU(*sample.CPUSeconds) {
			failures = append(failures, prefix+".cpuSeconds must be present, finite and nonnegative")
		} else if index > 0 && measurements[index-1].CPUSeconds != nil && *sample.CPUSeconds < *measurements[index-1].CPUSeconds {
			failures = append(failures, prefix+".cpuSeconds decreased within one process")
		}
		for _, field := range []struct {
			name  string
			value *int64
			min   int64
		}{
			{"residentMemoryBytes", sample.ResidentMemoryBytes, 1},
			{"goroutines", sample.Goroutines, 1},
			{"openConnections", sample.OpenConnections, 0},
		} {
			if field.value == nil || *field.value < field.min {
				failures = append(failures, fmt.Sprintf("%s.%s must be present and at least %d", prefix, field.name, field.min))
			}
		}
	}
	return failures
}
