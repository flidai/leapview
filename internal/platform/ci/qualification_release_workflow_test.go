package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNightlyRecoveryCannotPassWithNonSuccess(t *testing.T) {
	workflow := readQualificationWorkflow(t, "../../../.github/workflows/nightly.yml")
	recovery := workflow.Jobs["host-recovery-validation"]
	if recovery.Uses != "./.github/workflows/demo-upgrade-qualification.yml" || recovery.If != "github.repository == 'flidai/leapview'" {
		t.Fatal("nightly must call required recovery with the repository guard")
	}
	for _, permission := range []string{"contents", "packages", "attestations"} {
		if recovery.Permissions[permission] != "read" {
			t.Fatalf("missing %s permission", permission)
		}
	}
	gate := workflow.Jobs["ci-gate"]
	if !slices.Contains(qualificationNeeds(gate.Needs), "host-recovery-validation") {
		t.Fatal("nightly gate omits recovery")
	}
	step := gate.Steps[0]
	for _, result := range []string{"success", "failure", "skipped", "cancelled", ""} {
		t.Run(result, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", step.Run)
			cmd.Env = os.Environ()
			for name := range step.Env {
				value := "success"
				if name == "HOST_RECOVERY_RESULT" {
					value = result
				}
				cmd.Env = append(cmd.Env, name+"="+value)
			}
			err := cmd.Run()
			if (err == nil) != (result == "success") {
				t.Fatalf("recovery %q: gate error %v", result, err)
			}
		})
	}
}

func TestPublishedQualificationsAreIndependentRerunnableJobs(t *testing.T) {
	release := readQualificationWorkflow(t, "../../../.github/workflows/release.yml")
	for name, file := range map[string]string{"installed-qualification": "installed-candidate", "authoring-qualification": "authoring-package-qualification"} {
		caller := release.Jobs[name]
		if caller.Uses != "./.github/workflows/"+file+".yml" || !slices.Equal(qualificationNeeds(caller.Needs), []string{"image", "publish"}) {
			t.Fatalf("%s must qualify only after publication", name)
		}
		if caller.With["release_tag"] != "${{ needs.image.outputs.release_tag }}" || caller.Permissions["contents"] != "read" {
			t.Fatal("qualification caller must pass the published tag and contents permission")
		}
		if file == "installed-candidate" {
			for _, permission := range []string{"packages", "attestations"} {
				if caller.Permissions[permission] != "read" {
					t.Fatalf("caller missing %s", permission)
				}
			}
			if caller.Permissions["issues"] != "write" {
				t.Fatal("caller must permit qualification incidents")
			}
		}
		data, err := os.ReadFile("../../../.github/workflows/release.yml")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "release_tag: ${{ needs.image.outputs.release_tag }}") {
			t.Fatal("caller must pass published image tag")
		}
		called := readQualificationWorkflow(t, "../../../.github/workflows/"+file+".yml")
		inputs := called.On["workflow_call"].(map[string]any)["inputs"].(map[string]any)
		tag := inputs["release_tag"].(map[string]any)
		if tag["required"] != true || tag["type"] != "string" {
			t.Fatal("reusable qualification must require exact tag")
		}
		if file == "authoring-package-qualification" && inputs["run_lifecycle"].(map[string]any)["default"] != false {
			t.Fatal("lifecycle must remain opt-in")
		}
		for _, event := range []string{"release", "workflow_dispatch"} {
			if _, ok := called.On[event]; !ok {
				t.Fatalf("%s lost %s", file, event)
			}
		}
		// A failed qualification is downstream of successful publication. Rerunning
		// failed jobs must never make publication depend on qualification or replace
		// an existing release.
		if slices.Contains(qualificationNeeds(release.Jobs["publish"].Needs), name) {
			t.Fatal("publication depends on post-publication qualification")
		}
	}
}

func TestInstalledResolutionFailuresCreateIdentifiedIncidents(t *testing.T) {
	workflow := readQualificationWorkflow(t, "../../../.github/workflows/installed-candidate.yml")
	if strings.Contains(workflow.Concurrency.Group, "github.event.inputs") || !strings.Contains(workflow.Concurrency.Group, "inputs.release_tag") {
		t.Fatal("concurrency must use reusable inputs")
	}
	inputs := workflow.On["workflow_dispatch"].(map[string]any)["inputs"].(map[string]any)["release_tag"].(map[string]any)
	if _, exists := inputs["default"]; exists {
		t.Fatal("manual qualification must not default to a stale tag")
	}
	resolve := workflow.Jobs["resolve"]
	if !strings.Contains(resolve.If, "!startsWith(github.event.release.tag_name, 'desktop-')") {
		t.Fatal("desktop publication must be excluded")
	}
	incident := workflow.Jobs["incident"]
	if !strings.Contains(incident.If, "needs.resolve.result == 'failure'") {
		t.Fatal("resolution failures must be incidents")
	}
	if !strings.Contains(incident.Steps[0].Env["RELEASE_TAG"], "scheduled-resolution") {
		t.Fatal("resolver incident identity must be nonempty")
	}
}

func TestReleaseSelectionEntrypointsKeepExactInputsOnScheduledCallers(t *testing.T) {
	workflow := readQualificationWorkflow(t, "../../../.github/workflows/installed-candidate.yml")
	step := workflow.Jobs["resolve"].Steps[1]
	for _, scenario := range []struct{ event, inputs, tag, want string }{
		{"schedule", "{}", "", "--scheduled"},
		{"schedule", `{"release_tag":"v1.2.3"}`, "v1.2.3", "--tag v1.2.3"},
		{"schedule", `{"release_tag":""}`, "", "--tag "},
		{"workflow_dispatch", `{"release_tag":"v1.2.3"}`, "v1.2.3", "--tag v1.2.3"},
		{"release", "{}", "v1.2.3-rc.1", "--tag v1.2.3-rc.1"},
		{"release", `{"release_tag":""}`, "", "--tag "},
		{"push", `{"release_tag":"v1.2.3"}`, "v1.2.3", "--tag v1.2.3"},
	} {
		t.Run(scenario.event+scenario.inputs, func(t *testing.T) {
			arguments := filepath.Join(t.TempDir(), "arguments")
			cmd := exec.Command("bash", "-c", `python3() { printf '%s ' "$@" > "$ARGUMENTS"; printf 'v1.2.3\n'; }; `+step.Run)
			eventTag := scenario.tag
			if scenario.inputs != "{}" {
				eventTag = "v99.99.99"
			}
			cmd.Env = append(os.Environ(), "GITHUB_EVENT_NAME="+scenario.event,
				"EVENT_RELEASE_TAG="+eventTag,
				"RELEASE_INPUTS="+scenario.inputs, "REQUESTED_TAG="+scenario.tag,
				"ARGUMENTS="+arguments, "GITHUB_OUTPUT="+filepath.Join(t.TempDir(), "output"))
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("resolver: %v\n%s", err, output)
			}
			data, err := os.ReadFile(arguments)
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(string(data)) != "scripts/qualification_release.py "+strings.TrimSpace(scenario.want) {
				t.Fatalf("arguments = %q, want %q", data, scenario.want)
			}
		})
	}
}

func TestAuthoringExplicitTagCannotFallThroughToInheritedRelease(t *testing.T) {
	workflow := readQualificationWorkflow(t, "../../../.github/workflows/authoring-package-qualification.yml")
	step := workflow.Jobs["qualify"].Steps[1]
	for _, tag := range []string{"v1.2.3", ""} {
		t.Run(tag, func(t *testing.T) {
			directory := t.TempDir()
			calls := filepath.Join(directory, "calls")
			cmd := exec.Command("bash", "-c", `curl() { printf '%s ' "$@" >> "$CALLS"; }; `+step.Run)
			cmd.Env = append(os.Environ(), "INPUT_RELEASE_TAG="+tag, "EVENT_RELEASE_TAG=v99.99.99",
				`RELEASE_INPUTS={"release_tag":"`+tag+`"}`, "ARCH=amd64", "RUNNER_TEMP="+directory,
				"CALLS="+calls, "GITHUB_OUTPUT="+filepath.Join(directory, "output"))
			output, err := cmd.CombinedOutput()
			if tag == "" {
				if err == nil || !strings.Contains(string(output), "invalid release tag") {
					t.Fatalf("empty tag must fail: %v %s", err, output)
				}
				if _, err := os.Stat(calls); !os.IsNotExist(err) {
					t.Fatal("empty explicit tag reached download")
				}
				return
			}
			if err != nil {
				t.Fatalf("download: %v %s", err, output)
			}
			data, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "/releases/download/v1.2.3/") || strings.Contains(string(data), "v99.99.99") {
				t.Fatalf("wrong release downloaded: %s", data)
			}
		})
	}
}
