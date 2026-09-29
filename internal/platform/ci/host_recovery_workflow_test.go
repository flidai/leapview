package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type qualificationWorkflow struct {
	On          map[string]any `yaml:"on"`
	Concurrency struct {
		Group string `yaml:"group"`
	} `yaml:"concurrency"`
	Jobs map[string]struct {
		Name           string            `yaml:"name"`
		If             string            `yaml:"if"`
		Uses           string            `yaml:"uses"`
		TimeoutMinutes string            `yaml:"timeout-minutes"`
		Needs          any               `yaml:"needs"`
		Permissions    map[string]string `yaml:"permissions"`
		Steps          []struct {
			Name string            `yaml:"name"`
			ID   string            `yaml:"id"`
			If   string            `yaml:"if"`
			Run  string            `yaml:"run"`
			Uses string            `yaml:"uses"`
			With map[string]string `yaml:"with"`
			Env  map[string]string `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func qualificationNeeds(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		result := make([]string, 0, len(typed))
		for _, entry := range typed {
			if name, ok := entry.(string); ok {
				result = append(result, name)
			}
		}
		return result
	default:
		return nil
	}
}

func readQualificationWorkflow(t *testing.T, path string) qualificationWorkflow {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var workflow qualificationWorkflow
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow
}

func TestIsolatedHostRecoveryQualificationIsARequiredReusableContract(t *testing.T) {
	workflow := readQualificationWorkflow(t, "../../../.github/workflows/demo-upgrade-qualification.yml")
	if _, ok := workflow.On["workflow_call"]; !ok {
		t.Fatal("host recovery qualification must support workflow_call")
	}
	if _, ok := workflow.On["workflow_dispatch"]; !ok {
		t.Fatal("host recovery qualification must retain manual runs")
	}
	if len(workflow.Jobs) != 2 {
		t.Fatalf("qualification job inventory changed: %v", workflow.Jobs)
	}
	recovery := workflow.Jobs["recovery"]
	if recovery.Name != "Isolated host recovery and migration boundary contracts" || recovery.If != "${{ !inputs.final_artifact }}" || recovery.TimeoutMinutes != "45" {
		t.Fatal("qualification must run as a bounded required host recovery contract")
	}
	if len(recovery.Steps) != 4 || recovery.Steps[0].Name != "Check out the candidate" || recovery.Steps[1].Name != "Set up the pinned toolchain" || recovery.Steps[2].Uses != "docker/login-action@dbcb813823bdd20940b903addbd779551569679f" || recovery.Steps[3].Run != "task test:qualification:demo-host-recovery" {
		t.Fatal("reusable qualification must run the canonical Taskfile contract")
	}
	historical := workflow.Jobs["historical-transition"]
	if historical.Name != "Schema-32 legacy access transition" || historical.TimeoutMinutes != "120" || historical.Permissions["packages"] != "read" {
		t.Fatal("historical transition must be a bounded reusable GHCR-qualified contract")
	}
	var historicalLogin, requiredTransition, finalAdmission, localImage, receiptUpload bool
	for _, step := range historical.Steps {
		switch {
		case step.Uses == "docker/login-action@dbcb813823bdd20940b903addbd779551569679f" && step.If == "":
			historicalLogin = true
		case step.Name == "Run the required historical transition fixture":
			requiredTransition = step.Run == "task test:qualification:historical-transition" &&
				step.Env["LEAPVIEW_HISTORICAL_TRANSITION_REQUIRED"] == "1" &&
				step.Env["LEAPVIEW_HISTORICAL_TRANSITION_CANDIDATE_IMAGE"] == "${{ steps.candidate.outputs.image }}" &&
				step.Env["LEAPVIEW_HISTORICAL_TRANSITION_CANDIDATE_REVISION"] == "${{ steps.candidate.outputs.revision }}"
		case step.ID == "admission" && step.If == "${{ inputs.final_artifact }}":
			finalAdmission = true
		case step.Name == "Build a local candidate image for pre-merge transition testing" && step.If == "${{ !inputs.final_artifact }}":
			localImage = strings.Contains(step.Run, "docker buildx build --load")
		case step.Name == "Upload passed historical transition receipt":
			receiptUpload = step.With["name"] == "historical-transition-${{ github.run_attempt }}" &&
				step.With["path"] == "${{ runner.temp }}/historical-transition/transition.json" &&
				step.With["if-no-files-found"] == "error"
		}
	}
	if !historicalLogin || !requiredTransition || !finalAdmission || !localImage || !receiptUpload {
		t.Fatal("historical transition must pull the private predecessor, use an exact candidate identity, require its E2E receipt, and retain that receipt")
	}

	for _, path := range []string{"../../../.github/workflows/ci.yml", "../../../.github/workflows/merge-validation.yml"} {
		caller := readQualificationWorkflow(t, path)
		job, ok := caller.Jobs["host-recovery-validation"]
		if !ok || job.Uses != "./.github/workflows/demo-upgrade-qualification.yml" || job.Permissions["packages"] != "read" || job.Permissions["attestations"] != "read" {
			t.Fatalf("%s must call the isolated host recovery workflow", path)
		}
		gate := caller.Jobs["ci-gate"]
		if !slices.Contains(qualificationNeeds(gate.Needs), "host-recovery-validation") {
			t.Fatalf("%s CI gate must depend on host recovery qualification", path)
		}
		var requiresHostRecovery bool
		for _, step := range gate.Steps {
			if step.Env["HOST_RECOVERY_RESULT"] == "${{ needs.host-recovery-validation.result }}" && strings.Contains(step.Run, "HOST_RECOVERY_RESULT") && strings.Contains(step.Run, "= success") {
				requiresHostRecovery = true
			}
		}
		if !requiresHostRecovery {
			t.Fatalf("%s gate must reject missing, failed, or skipped host recovery qualification", path)
		}
	}

	taskfile, err := os.ReadFile("../../../Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"test:qualification:demo-host-recovery:",
		"go test -race ./internal/app/cli/hostinstall -count=1",
		"LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=true",
		"LEAPVIEW_HOST_UPGRADE_QUALIFICATION=1",
		"TestColdPostgreSQLPairRecovery",
		"TestInternalNetworkRecoveryRelay",
		"TestVolumeTLSMountQualification",
		"test:qualification:historical-transition:",
		"LEAPVIEW_HISTORICAL_TRANSITION_REQUIRED=1",
		"TestQualificationHistoricalTransitionEndToEnd",
		"test:qualification:historical-transition:local",
		"task: test:qualification:historical-transition:local",
	} {
		if !strings.Contains(string(taskfile), required) {
			t.Errorf("host recovery task lost required qualification command %q", required)
		}
	}
}

func TestHistoricalQualificationConcurrencySeparatesCallingWorkflows(t *testing.T) {
	workflow := readQualificationWorkflow(t, "../../../.github/workflows/demo-upgrade-qualification.yml")
	// CI and Main artifacts can qualify the same main ref concurrently. The
	// reusable workflow must not cancel the other caller's required proof.
	if !strings.Contains(workflow.Concurrency.Group, "${{ github.workflow }}") {
		t.Fatal("historical qualification concurrency must distinguish calling workflows")
	}
}

func TestHistoricalTransitionLocalTaskPreservesDockerImageIDTemplate(t *testing.T) {
	if _, err := exec.LookPath("task"); err != nil {
		t.Skip("Task CLI is required to verify Task template expansion")
	}
	root, err := filepath.Abs("../../../")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("task", "--dry", "test:qualification:historical-transition:local")
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("render local historical-transition task: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), `docker image inspect "${tag}" --format '{{.Id}}'`) {
		t.Fatalf("Task must pass Docker's literal Go template through to image inspect; output was:\n%s", output)
	}
}

func TestMainArtifactBundlesExactHistoricalTransitionReceiptAtRoot(t *testing.T) {
	workflow := readQualificationWorkflow(t, "../../../.github/workflows/artifacts.yml")
	transition := workflow.Jobs["qualify-historical-transition"]
	if transition.Uses != "./.github/workflows/demo-upgrade-qualification.yml" || transition.Permissions["packages"] != "read" || transition.Permissions["attestations"] != "read" {
		t.Fatal("main image artifacts must qualify the exact published digest against the historical predecessor")
	}
	record := workflow.Jobs["record-production-image-qualification"]
	if !slices.Contains(qualificationNeeds(record.Needs), "qualify-production-image") || !slices.Contains(qualificationNeeds(record.Needs), "qualify-historical-transition") || record.If != "${{ always() }}" {
		t.Fatal("the qualification receipt must require both exact-image qualification contracts")
	}
	var checksBoth, downloadsTransition, bindsRunAttempt, bundlesRootTransition bool
	for _, step := range record.Steps {
		switch {
		case step.Name == "Require both exact-image qualifications":
			checksBoth = strings.Contains(step.Run, `test "${HISTORICAL_TRANSITION_RESULT}" = success`) && strings.Contains(step.Run, `test "${IMAGE_QUALIFICATION_RESULT}" = success`)
		case step.Name == "Download the exact historical transition receipt":
			downloadsTransition = step.With["name"] == "historical-transition-${{ github.run_attempt }}"
		case step.Name == "Bind both receipts to this exact workflow attempt":
			bindsRunAttempt = strings.Contains(step.Run, "transition.json") && strings.Contains(step.Run, "runId") && strings.Contains(step.Run, "runAttempt")
		case step.Name == "Upload exact image and historical transition qualification":
			path := step.With["path"]
			bundlesRootTransition = strings.Contains(path, "qualification.json") && strings.Contains(path, "transition.json") && !strings.Contains(path, "transition-evidence/transition.json")
		}
	}
	if !checksBoth || !downloadsTransition || !bindsRunAttempt || !bundlesRootTransition {
		t.Fatal("the exact image qualification artifact must contain a run-bound root-level transition.json beside qualification.json")
	}
}
