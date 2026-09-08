package ci

import (
	"gopkg.in/yaml.v3"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestPRWorkflowConsumesPlannerOutputsAndAlwaysGates(t *testing.T) {
	data, err := os.ReadFile("../../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		On   map[string]map[string]any `yaml:"on"`
		Jobs map[string]struct {
			If      string            `yaml:"if"`
			Needs   []string          `yaml:"needs"`
			Outputs map[string]string `yaml:"outputs"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.On["pull_request"]["paths"] != nil || config.On["pull_request"]["paths-ignore"] != nil {
		t.Fatal("workflow path filtering bypasses required gate")
	}
	gate := config.Jobs["ci-gate"]
	if gate.If != "${{ always() }}" || !slices.Contains(gate.Needs, "prepare") {
		t.Fatal("gate does not require planning on every outcome")
	}
	for name := range FullPRJobs().Selected() {
		job, ok := config.Jobs[name]
		if !ok {
			t.Fatalf("missing lane %s", name)
		}
		key := strings.ReplaceAll(name, "-", "_")
		if !slices.Equal(job.Needs, []string{"prepare"}) || job.If != "needs.prepare.outputs."+key+" == 'true'" {
			t.Errorf("%s bypasses selection", name)
		}
		if config.Jobs["prepare"].Outputs[key] != "${{ steps.plan.outputs."+key+" }}" {
			t.Errorf("%s output disconnected", name)
		}
		if !slices.Contains(gate.Needs, name) {
			t.Errorf("gate omits %s", name)
		}
	}
	for _, fragment := range []string{"fetch-depth: 0", "--stack-base \"$STACK_BASE\"", "--head \"$GITHUB_SHA\"", "--expected-attempt \"$GITHUB_RUN_ATTEMPT\"", "--expected-deferred=\"$DEFERRED\"", "--frontend-matrix \"$FRONTEND_MATRIX\""} {
		if !strings.Contains(string(data), fragment) {
			t.Errorf("missing candidate/gate contract %s", fragment)
		}
	}
}
