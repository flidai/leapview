package composectl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFinalizeQualificationPerformanceSerializesFailureArrays(t *testing.T) {
	for _, mode := range []string{"bootstrap", "compared", "failed comparison"} {
		t.Run(mode, func(t *testing.T) {
			candidate := comparableQualificationPerformanceReport()
			dir := t.TempDir()
			path := filepath.Join(dir, "candidate.json")
			baselinePath := ""
			if mode != "bootstrap" {
				baselinePath = filepath.Join(dir, "baseline.json")
				if err := writeQualificationJSON(baselinePath, finalizedQualificationPerformanceBaseline(t)); err != nil {
					t.Fatal(err)
				}
			}
			failed := mode == "failed comparison"
			if failed {
				candidate.Failures = []string{"worker failure retained"}
				candidate.Reliability.Failures = []string{"request failure retained"}
				candidate.Latency["governedQueryMs"] = qualificationDurationSummary{Samples: 1, P50: 100, P95: 100, Max: 100}
				setQualificationRawSamples(t, &candidate, "governedQueryMs", []float64{100})
			}
			if err := writeQualificationJSON(path, candidate); err != nil {
				t.Fatal(err)
			}
			environment, err := json.Marshal(candidate.Environment)
			if err != nil {
				t.Fatal(err)
			}
			err = finalizeQualificationPerformanceReport(path, candidate.Policy, 0, 0, environment, candidate.Image, candidate.Architecture, baselinePath)
			if (err != nil) != failed {
				t.Fatalf("finalizer error = %v, expected failure = %v", err, failed)
			}
			// Read the persisted JSON boundary: decoding null into a Go slice
			// otherwise hides the contract violation from Go-only consumers.
			encoded, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var report struct {
				Failures   json.RawMessage `json:"failures"`
				Comparison struct {
					Failures json.RawMessage `json:"failures"`
					Baseline *string         `json:"baseline"`
				} `json:"comparison"`
				Assertions struct {
					ComparisonTolerance bool `json:"comparisonTolerance"`
				} `json:"assertions"`
				Result string `json:"result"`
			}
			if err := json.Unmarshal(encoded, &report); err != nil {
				t.Fatal(err)
			}
			arrays := make(map[string][]string)
			for name, raw := range map[string]json.RawMessage{"failures": report.Failures, "comparison.failures": report.Comparison.Failures} {
				var failures []string
				if err := json.Unmarshal(raw, &failures); err != nil || failures == nil {
					t.Errorf("%s must serialize as an array, got %s (decode error: %v)", name, raw, err)
				}
				arrays[name] = failures
			}
			if !failed {
				if report.Result != "success" || len(arrays["failures"]) != 0 || len(arrays["comparison.failures"]) != 0 {
					t.Fatalf("successful report has failures: %s", encoded)
				}
				compared := mode == "compared"
				if report.Assertions.ComparisonTolerance != compared || (report.Comparison.Baseline != nil) != compared {
					t.Fatalf("comparison semantics changed: %s", encoded)
				}
				return
			}
			if report.Result != "failure" || report.Assertions.ComparisonTolerance {
				t.Fatalf("failed comparison claimed success: %s", encoded)
			}
			for _, failure := range []string{"worker failure retained", "request failure retained", "governed query p95 100ms exceeds 10ms"} {
				if !slices.Contains(arrays["failures"], failure) {
					t.Errorf("lost existing failure %q: %v", failure, arrays["failures"])
				}
			}
			if len(arrays["comparison.failures"]) != 1 || !strings.Contains(arrays["comparison.failures"][0], "governed query p95 regressed") {
				t.Fatalf("comparison regression was lost: %v", arrays["comparison.failures"])
			}
			if !slices.Contains(arrays["failures"], arrays["comparison.failures"][0]) {
				t.Fatalf("comparison failure missing from report failures: %v", arrays["failures"])
			}
		})
	}
}
