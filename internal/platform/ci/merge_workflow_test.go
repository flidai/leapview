package ci

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMergeWorkflowIndependentLanesAndStrictGate(t *testing.T) {
	data, err := os.ReadFile("../../../.github/workflows/merge-validation.yml")
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
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	lanes := []string{"apigen-validation", "go-packages-validation", "go-application-validation", "frontend-validation", "full-validation"}
	if len(workflow.Jobs) != len(lanes)+1 {
		t.Fatal("merge job inventory changed; update the required gate contract")
	}
	for _, name := range lanes {
		job, ok := workflow.Jobs[name]
		if !ok || len(job.Needs) != 0 {
			t.Errorf("%s must start independently on its own runner", name)
		}
		if job.If != "github.repository == 'flidai/leapview'" {
			t.Errorf("%s must remain exhaustive for upstream merge candidates", name)
		}
	}
	prepared, validated := false, false
	for _, step := range workflow.Jobs["full-validation"].Steps {
		if step.Run == "node scripts/ci_watchdog.mjs --timeout-seconds 420 --attempts 2 -- task ci:prepare" && step.If == "" {
			prepared = true
		}
		if step.Run == "task ci:full:extras" && step.If == "" {
			if !prepared {
				t.Fatal("independent extras must prepare their own inputs first")
			}
			validated = true
		}
	}
	if !validated {
		t.Fatal("full merge validation must retain the complete extras contract")
	}
	gate := workflow.Jobs["ci-gate"]
	if gate.Name != "CI gate" || gate.If != "${{ always() }}" || !slices.Equal(gate.Needs, lanes) {
		t.Fatal("required CI gate must run on every outcome and require every lane")
	}
	if len(gate.Steps) != 2 || gate.Steps[0].Name != "Require full merge validation" || gate.Steps[1].Name != "Require native desktop merge proof" {
		t.Fatal("gate must retain validation and native proof checks")
	}
	results := gate.Steps[0]
	if len(results.Env) != len(lanes) {
		t.Fatal("gate must inspect every lane result")
	}
	for _, lane := range lanes {
		if !slices.ContainsFunc(mapValues(results.Env), func(value string) bool { return value == "${{ needs."+lane+".result }}" }) {
			t.Fatalf("gate omits result for %s", lane)
		}
	}
	// Execute the actual workflow gate script: every non-success conclusion,
	// including an intentional or dependency-induced skip, must block merging.
	run := func(changed, conclusion string) error {
		cmd := exec.Command("bash", "-e", "-c", results.Run)
		cmd.Env = os.Environ()
		for key := range results.Env {
			value := "success"
			if key == changed {
				value = conclusion
			}
			cmd.Env = append(cmd.Env, key+"="+value)
		}
		return cmd.Run()
	}
	if err := run("", ""); err != nil {
		t.Fatalf("successful lanes rejected: %v", err)
	}
	for key := range results.Env {
		for _, conclusion := range []string{"failure", "cancelled", "skipped", ""} {
			t.Run(key+"/"+conclusion, func(t *testing.T) {
				if run(key, conclusion) == nil {
					t.Fatal("non-success lane accepted")
				}
			})
		}
	}
	proof := gate.Steps[1]
	if proof.Env["REVISION"] != "${{ github.sha }}" || !strings.Contains(proof.Run, "electron-security-proof.yml/runs?head_sha=$REVISION&event=merge_group") || !strings.Contains(proof.Run, `test "$conclusion" = success`) {
		t.Fatal("native proof must succeed for the exact merge candidate and event")
	}
}

func mapValues(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}
