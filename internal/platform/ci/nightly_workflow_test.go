package ci

import (
	"os"
	"reflect"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNightlyWorkflowFullValidationAndStrictGate(t *testing.T) {
	data, err := os.ReadFile("../../../.github/workflows/nightly.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Name  string   `yaml:"name"`
			If    string   `yaml:"if"`
			Needs []string `yaml:"needs"`
			Steps []struct {
				Name string            `yaml:"name"`
				If   string            `yaml:"if"`
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}

	full, ok := workflow.Jobs["full-validation"]
	if !ok {
		t.Fatal("nightly workflow must retain the full-validation job")
	}
	if full.Name != "Full nightly validation" || full.If != "github.repository == 'flidai/leapview'" {
		t.Fatal("full-validation job identity or repository guard changed")
	}
	if len(full.Needs) != 0 {
		t.Fatal("full-validation must start independently on its own runner")
	}
	if workflow.Jobs["frontend-validation"].Name != "Frontend tests (nightly, ${{ matrix.shard }})" {
		t.Fatal("nightly frontend check names must retain their reporting identity")
	}
	checkout := workflow.Jobs["security-validation"].Steps[0]
	if checkout.Name != "Check out repository" || checkout.With["fetch-depth"] != "0" {
		t.Fatal("nightly security must fetch the history baseline for branch dispatches")
	}
	prepared, validated := false, false
	for _, step := range full.Steps {
		if step.Run == "node scripts/ci_watchdog.mjs --timeout-seconds 420 --attempts 2 -- task ci:prepare" && step.If == "" {
			prepared = true
		}
		if step.Run == "task ci:full:extras:hosted" && step.If == "" {
			if !prepared {
				t.Fatal("full nightly validation must prepare its own inputs first")
			}
			validated = true
		}
	}
	if !validated {
		t.Fatal("full nightly validation must retain the complete extras command")
	}
	if !prepared {
		t.Fatal("full nightly validation must retain unconditional watchdog-wrapped preparation")
	}

	expectedNeeds := []string{
		"apigen-validation",
		"go-packages-validation",
		"go-application-validation",
		"frontend-validation",
		"full-validation",
		"security-validation",
		"dependency-evidence-refresh",
	}
	gate, ok := workflow.Jobs["ci-gate"]
	if !ok {
		t.Fatal("nightly workflow must retain the ci-gate job")
	}
	if gate.Name != "CI gate" || gate.If != "${{ always() }}" || !slices.Equal(gate.Needs, expectedNeeds) {
		t.Fatal("nightly CI gate identity, condition, or dependencies changed")
	}
	if len(gate.Steps) != 1 {
		t.Fatal("nightly CI gate must retain its exhaustive result check")
	}
	results := gate.Steps[0]
	if results.Name != "Require exhaustive nightly validation" {
		t.Fatal("nightly CI gate result check changed")
	}
	expectedResults := map[string]string{
		"APIGEN_RESULT":                      "${{ needs.apigen-validation.result }}",
		"GO_PACKAGES_RESULT":                 "${{ needs.go-packages-validation.result }}",
		"GO_APPLICATION_RESULT":              "${{ needs.go-application-validation.result }}",
		"FRONTEND_RESULT":                    "${{ needs.frontend-validation.result }}",
		"FULL_RESULT":                        "${{ needs.full-validation.result }}",
		"SECURITY_RESULT":                    "${{ needs.security-validation.result }}",
		"DEPENDENCY_EVIDENCE_REFRESH_RESULT": "${{ needs.dependency-evidence-refresh.result }}",
	}
	if len(results.Env) != len(expectedResults) {
		t.Fatalf("nightly CI gate must inspect every required job result, got %d entries", len(results.Env))
	}
	for key, expected := range expectedResults {
		if results.Env[key] != expected {
			t.Errorf("nightly CI gate result %s = %q, want %q", key, results.Env[key], expected)
		}
	}
}

// Nightly must retain the same independently scheduled frontend and backend
// coverage as merge validation, including failure artifacts and browser setup.
func TestNightlyUsesMergeValidationLayout(t *testing.T) {
	read := func(path string) map[string]any {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var workflow struct{ Jobs map[string]map[string]any }
		if err := yaml.Unmarshal(data, &workflow); err != nil {
			t.Fatal(err)
		}
		result := map[string]any{}
		for _, name := range []string{"frontend-validation", "full-validation"} {
			job := workflow.Jobs[name]
			delete(job, "name")
			steps := job["steps"].([]any)
			// Checkout differs only because merge candidates require full history.
			job["steps"] = steps[1:]
			result[name] = job
		}
		return result
	}
	if !reflect.DeepEqual(read("../../../.github/workflows/nightly.yml"), read("../../../.github/workflows/merge-validation.yml")) {
		t.Fatal("nightly must retain the merge layout, budgets, coverage, and failure artifacts")
	}
}
