package securitycontracts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRequiredSecurityWorkflowAggregatesEveryFailClosedLane(t *testing.T) {
	workflow := repositoryYAML(t, ".github/workflows/security.yml")
	for _, fragment := range []string{
		"pull_request:",
		"push:",
		"branches: [main]",
		"merge_group:",
		"policy-validation:",
		"dependency-validation:",
		"source-validation:",
		"uses: ./.github/actions/setup-ci",
		"task security:source",
		"sast-validation:",
		"build-mode: manual",
		"build-mode: ${{ matrix.build-mode }}",
		"name: Security gate",
		"if: ${{ always() && (github.event_name != 'pull_request' || !github.event.pull_request.draft) }}",
		"needs: [policy-validation, dependency-validation, source-validation, sast-validation]",
		"go run ./internal/app/tools/securityresults",
	} {
		if !strings.Contains(workflow, fragment) {
			t.Errorf("security workflow is missing %q", fragment)
		}
	}
}

func TestOCIAdmissionActionUsesRepositoryOwnedGoContract(t *testing.T) {
	action := repositoryYAML(t, ".github/actions/oci-admission/action.yml")
	for _, fragment := range []string{
		"go-version-file: go.mod",
		"go run ./internal/app/tools/ociadmission",
		"--image \"$IMAGE\"",
		"--output \"$output_path\"",
	} {
		if !strings.Contains(action, fragment) {
			t.Errorf("OCI admission action is missing %q", fragment)
		}
	}
	if !containsPinnedAction(action, "actions/setup-go") {
		t.Fatal("OCI admission action does not use a commit-pinned setup-go action")
	}
	if strings.Contains(action, "scripts/admit_oci_artifact.sh") {
		t.Fatal("OCI admission action still invokes the legacy shell implementation")
	}
}

func TestOCIAdmissionDiagnosticUploadsAreBoundedAndAttemptScoped(t *testing.T) {
	action := repositoryYAML(t, ".github/actions/oci-admission/action.yml")
	for _, fragment := range []string{
		"  vulnerability-report:",
		"VULNERABILITY_REPORT: ${{ inputs.vulnerability-report }}",
		"vulnerability_report_args=(--vulnerability-report \"$VULNERABILITY_REPORT\")",
		"\"${vulnerability_report_args[@]}\"",
	} {
		if !strings.Contains(action, fragment) {
			t.Errorf("OCI admission action is missing vulnerability diagnostics contract %q", fragment)
		}
	}
	if strings.Contains(action, "  vulnerability-report:\n    description: Optional path for a sanitized vulnerability diagnostic report.\n    required: true") {
		t.Fatal("OCI admission vulnerability report must remain optional for existing callers")
	}

	assertDiagnosticUpload := func(path, job string) {
		t.Helper()
		workflow := repositoryYAML(t, path)
		var document struct {
			Jobs map[string]struct {
				Steps []struct {
					Name string            `yaml:"name"`
					If   string            `yaml:"if"`
					Uses string            `yaml:"uses"`
					With map[string]string `yaml:"with"`
				} `yaml:"steps"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal([]byte(workflow), &document); err != nil {
			t.Fatal(err)
		}
		definition, ok := document.Jobs[job]
		if !ok {
			t.Fatalf("%s is missing job %q", path, job)
		}
		admissionIndex, uploadIndex := -1, -1
		for index, step := range definition.Steps {
			if step.Uses == "./.github/actions/oci-admission" {
				admissionIndex = index
				if step.With["platform"] != "linux/amd64" {
					t.Errorf("%s admission platform is %q, want linux/amd64", path, step.With["platform"])
				}
				if step.With["vulnerability-report"] != "${{ runner.temp }}/oci-vulnerability-report.json" {
					t.Errorf("%s does not direct admission diagnostics to the isolated JSON file", path)
				}
			}
			if step.Name == "Upload OCI vulnerability diagnostic" {
				uploadIndex = index
				if step.If != "always()" {
					t.Errorf("%s diagnostic upload condition is %q, want always()", path, step.If)
				}
				if step.Uses != "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a" {
					t.Errorf("%s diagnostic upload action is not commit-pinned", path)
				}
				if step.With["path"] != "${{ runner.temp }}/oci-vulnerability-report.json" {
					t.Errorf("%s diagnostic upload path is not the single report JSON: %q", path, step.With["path"])
				}
				if step.With["if-no-files-found"] != "ignore" || step.With["retention-days"] != "14" {
					t.Errorf("%s diagnostic upload handling/retention is unsafe: %#v", path, step.With)
				}
				name := step.With["name"]
				for _, fragment := range []string{"${{ github.job }}", "${{ github.run_id }}", "${{ github.run_attempt }}"} {
					if !strings.Contains(name, fragment) {
						t.Errorf("%s diagnostic artifact name %q omits %s", path, name, fragment)
					}
				}
			}
		}
		if admissionIndex < 0 || uploadIndex != admissionIndex+1 {
			t.Errorf("%s must upload the diagnostic immediately after admission, got indexes %d and %d", path, admissionIndex, uploadIndex)
		}
	}

	assertDiagnosticUpload(".github/workflows/artifacts.yml", "qualify-production-image")
	assertDiagnosticUpload(".github/workflows/demo-upgrade-qualification.yml", "historical-transition")

	artifactsWorkflow := repositoryText(t, ".github/workflows/artifacts.yml")
	for _, fragment := range []string{
		"if: github.event_name == 'workflow_dispatch'",
		"Require the exact head of one open pull request to main",
		".head.sha == $revision",
	} {
		if !strings.Contains(artifactsWorkflow, fragment) {
			t.Errorf("artifacts workflow lost protected candidate guard %q", fragment)
		}
	}
}

func TestAggregateJobChecksOutCandidateOwnedGoContract(t *testing.T) {
	workflow := repositoryYAML(t, ".github/workflows/security.yml")
	start := strings.Index(workflow, "  security-gate:")
	if start < 0 {
		t.Fatal("security-gate job is missing")
	}
	aggregate := workflow[start:]
	if !containsPinnedAction(aggregate, "actions/checkout") {
		t.Fatal("aggregate job does not use a commit-pinned checkout action")
	}
	if !strings.Contains(aggregate, "go run ./internal/app/tools/securityresults") {
		t.Fatal("aggregate job does not invoke the candidate-owned Go result contract")
	}
}

func TestScannerJobsInvokeTheirRepositoryOwnedGoGates(t *testing.T) {
	workflow := repositoryYAML(t, ".github/workflows/security.yml")
	var document struct {
		Jobs map[string]struct {
			Steps []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(workflow), &document); err != nil {
		t.Fatal(err)
	}
	for job, command := range map[string]string{
		"dependency-validation": "task security:dependencies",
		"source-validation":     "task security:source",
	} {
		definition, ok := document.Jobs[job]
		if !ok {
			t.Errorf("security workflow is missing %s", job)
			continue
		}
		found := false
		for _, step := range definition.Steps {
			if strings.TrimSpace(step.Run) == command {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s does not run %q", job, command)
		}
	}
}

func TestDependencyScannerPreparesGeneratedGoSourceImmediatelyBeforeGate(t *testing.T) {
	workflow := repositoryYAML(t, ".github/workflows/security.yml")
	var document struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(workflow), &document); err != nil {
		t.Fatal(err)
	}

	job, ok := document.Jobs["dependency-validation"]
	if !ok {
		t.Fatal("security workflow is missing dependency-validation")
	}
	gateIndex := -1
	for index, step := range job.Steps {
		if strings.TrimSpace(step.Run) == "task security:dependencies" {
			gateIndex = index
			break
		}
	}
	if gateIndex < 1 {
		t.Fatal("dependency-validation does not have a preparation step immediately before its dependency gate")
	}

	preparation := job.Steps[gateIndex-1]
	if preparation.Name != "Prepare generated Go source for dependency scan" {
		t.Fatalf("dependency gate preparation step is %q, want %q", preparation.Name, "Prepare generated Go source for dependency scan")
	}
	if preparation.Env["NPM_CONFIG_AUDIT"] != "false" {
		t.Fatalf("dependency gate preparation must disable npm audit, got NPM_CONFIG_AUDIT=%q", preparation.Env["NPM_CONFIG_AUDIT"])
	}
	var commands []string
	for _, line := range strings.Split(preparation.Run, "\n") {
		if command := strings.TrimSpace(line); command != "" {
			commands = append(commands, command)
		}
	}
	want := []string{"task db:generate", "task config:generate", "task ui-signals:generate"}
	if strings.Join(commands, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("dependency gate preparation runs %q, want exactly %q", commands, want)
	}
}

func TestDependencyGateUsesOfflineEvidenceAndSeparateRefresh(t *testing.T) {
	taskfile := repositoryText(t, "Taskfile.yml")
	required := taskDefinition(t, taskfile, "security:dependencies")
	refresh := taskDefinition(t, taskfile, "security:dependencies:evidence:refresh")
	workflow := repositoryYAML(t, ".github/workflows/security.yml")

	if !strings.Contains(required, ".security/javascript-vulnerability-evidence.json") {
		t.Fatal("required dependency task does not name the offline JavaScript evidence document")
	}
	if !strings.Contains(required, "go run ./internal/app/tools/securitydependencies -root .") {
		t.Fatal("required dependency task does not invoke the default security dependency evaluator")
	}
	if strings.Contains(required, "\n    deps:") {
		t.Fatal("required dependency task must not prepare or invoke a live dependency path")
	}
	if strings.Contains(required, "-refresh-javascript-evidence") {
		t.Fatal("required dependency task refreshes JavaScript evidence instead of evaluating the checked-in document")
	}
	if strings.Contains(required, "bun audit") || strings.Contains(required, "npm audit") {
		t.Fatal("required dependency task embeds a live JavaScript audit command")
	}
	if !strings.Contains(refresh, "go run ./internal/app/tools/securitydependencies -root . -refresh-javascript-evidence") {
		t.Fatal("JavaScript evidence refresh task does not invoke the explicit refresh flag")
	}
	if !strings.Contains(refresh, "deps:\n      - ci:prepare") {
		t.Fatal("JavaScript evidence refresh task must prepare its live provider environment explicitly")
	}
	if strings.Contains(workflow, "task security:dependencies:evidence:refresh") {
		t.Fatal("required Security workflow invokes the opt-in JavaScript evidence refresh")
	}
	if !strings.Contains(workflow, "task security:dependencies") {
		t.Fatal("required Security workflow does not invoke the offline dependency gate")
	}
}

func TestNativeOCIRefsAreAdmittedBeforeManifestAssembly(t *testing.T) {
	for _, path := range []string{".github/workflows/release.yml", ".github/workflows/site-image.yml"} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			workflow := repositoryYAML(t, path)
			attestation := strings.Index(workflow, "Attest native")
			admission := strings.Index(workflow, "      - name: Admit exact native")
			record := strings.Index(workflow, "Record admitted native")
			assembly := strings.Index(workflow, "docker buildx imagetools create")
			topLevelAdmission := strings.LastIndex(workflow, "uses: ./.github/actions/oci-admission")
			if !(attestation >= 0 && admission > attestation && record > admission && assembly > record && topLevelAdmission > assembly) {
				t.Fatalf("native admission order is invalid in %s", path)
			}
			admittedBlock := workflow[admission:record]
			for _, fragment := range []string{
				"uses: ./.github/actions/oci-admission",
				"image: ${{ env.IMAGE_NAME }}@${{ steps.publish.outputs.digest }}",
				"repository: ${{ env.IMAGE_NAME }}",
				"source-revision: ${{ needs.identity.outputs.revision }}",
			} {
				if !strings.Contains(admittedBlock, fragment) {
					t.Errorf("native admission block in %s is missing %q", path, fragment)
				}
			}
			if !strings.Contains(admittedBlock, "expected-workflow: flidai/leapview/.github/workflows/") {
				t.Errorf("native admission block in %s has no trusted workflow identity", path)
			}
			recordedBlock := workflow[record:assembly]
			if !strings.Contains(recordedBlock, "IMAGE_REFERENCE: ${{ steps.admission.outputs.image }}") ||
				!strings.Contains(recordedBlock, `printf '%s\n' "$IMAGE_REFERENCE"`) {
				t.Errorf("manifest assembly in %s does not consume the admitted reference", path)
			}
			if !strings.Contains(workflow[assembly:], `image_references[@]`) {
				t.Errorf("manifest assembly in %s does not use the admitted reference list", path)
			}
		})
	}
}

func repositoryYAML(t *testing.T, relative string) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve contract test location")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "../../../.."))
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse %s: %v", relative, err)
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		t.Fatalf("%s must contain one YAML document", relative)
	}
	return string(data)
}

func repositoryText(t *testing.T, relative string) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve contract test location")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "../../../.."))
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func taskDefinition(t *testing.T, taskfile, name string) string {
	t.Helper()
	start := strings.Index(taskfile, "\n  "+name+":\n")
	if start < 0 {
		t.Fatalf("Taskfile is missing task %q", name)
	}
	lines := strings.Split(taskfile[start+1:], "\n")
	for index := 1; index < len(lines); index++ {
		// Taskfile task names are indented by exactly two spaces; nested
		// properties and commands use at least four.
		if len(lines[index]) >= 3 && lines[index][:2] == "  " && lines[index][2] != ' ' {
			return strings.Join(lines[:index], "\n")
		}
	}
	return strings.Join(lines, "\n")
}

func containsPinnedAction(workflow, action string) bool {
	needle := "uses: " + action + "@"
	start := strings.Index(workflow, needle)
	if start < 0 {
		return false
	}
	reference := workflow[start+len(needle):]
	end := strings.IndexAny(reference, " \t\r\n#")
	if end >= 0 {
		reference = reference[:end]
	}
	if len(reference) != 40 {
		return false
	}
	for _, character := range reference {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func TestSecurityPolicyRuns386BoundariesOnItsLinuxRunner(t *testing.T) {
	var workflow struct {
		Jobs map[string]struct {
			Runner string `yaml:"runs-on"`
			Steps  []struct {
				Run string            `yaml:"run"`
				Env map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(repositoryYAML(t, ".github/workflows/security.yml")), &workflow); err != nil {
		t.Fatal(err)
	}
	job := workflow.Jobs["policy-validation"]
	if job.Runner != "ubuntu-24.04" {
		t.Fatal("386 execution requires the Linux x86 policy runner")
	}
	found := false
	for _, step := range job.Steps {
		if step.Env["GOARCH"] == "386" && step.Env["CGO_ENABLED"] == "0" && step.Run == "go test ./pkg/duckdbsql/decode_integer.go ./pkg/duckdbsql/decode_integer_test.go" {
			found = true
		}
	}
	if !found {
		t.Fatal("policy job must execute production decoder boundaries on 386")
	}
	if strings.Contains(taskDefinition(t, repositoryText(t, "Taskfile.yml"), "security:policy"), "GOARCH=386") {
		t.Fatal("portable policy task must not require native 386 execution")
	}
}

func TestSASTWorkflowPreparesEachWorkspaceAndRetainsFailureDiagnostics(t *testing.T) {
	var workflow struct {
		Jobs map[string]struct {
			Env      map[string]string `yaml:"env"`
			Strategy struct {
				FailFast bool `yaml:"fail-fast"`
				Matrix   struct {
					Include []map[string]string `yaml:"include"`
				} `yaml:"matrix"`
			} `yaml:"strategy"`
			Steps []struct {
				Name, ID, If, Uses, Run string
				With, Env               map[string]string
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(repositoryYAML(t, ".github/workflows/security.yml")), &workflow); err != nil {
		t.Fatal(err)
	}
	job := workflow.Jobs["sast-validation"]
	for key, value := range map[string]string{"GOFLAGS": "-tags=duckdb_arrow", "CODEQL_OVERLAY_DATABASE_MODE": "none", "CODEQL_ACTION_DIFF_INFORMED_QUERIES": "false", "CODEQL_ACTION_EXPORT_DIAGNOSTICS": "true"} {
		if job.Env[key] != value {
			t.Errorf("%s=%q, want %q", key, job.Env[key], value)
		}
	}
	if job.Strategy.FailFast || len(job.Strategy.Matrix.Include) != 2 {
		t.Fatal("languages must run independently")
	}
	for _, entry := range job.Strategy.Matrix.Include {
		switch entry["language"] {
		case "go":
			if entry["build-mode"] != "manual" || entry["sarif"] != "go.sarif" {
				t.Fatal(entry)
			}
		case "javascript-typescript":
			if entry["build-mode"] != "none" || entry["sarif"] != "javascript.sarif" {
				t.Fatal(entry)
			}
		default:
			t.Fatalf("unexpected language: %v", entry)
		}
	}
	indices := map[string]int{}
	for i, step := range job.Steps {
		indices[step.Name] = i
	}
	order := []string{"Check out the exact candidate", "Capture immutable SAST checkout baseline", "Set up the locked SAST toolchain", "Build the SAST contract helper before tracing", "Prepare generated source in this runner", "Check maintained TypeScript source graphs", "Verify checkout integrity before extraction", "Initialize selected CodeQL analysis", "Trace every maintained Go module", "Analyze selected source language", "Require complete analysis diagnostics", "Verify checkout integrity after analysis", "Retain raw CodeQL diagnostics"}
	previous := -1
	for _, name := range order {
		i, ok := indices[name]
		if !ok || i <= previous {
			t.Fatalf("missing or unordered step %q", name)
		}
		previous = i
	}
	setup := job.Steps[indices["Set up the locked SAST toolchain"]]
	if setup.Uses != "./.github/actions/setup-ci" || setup.With["profile"] != "validation" || setup.With["browser"] != "false" {
		t.Fatal("SAST must use the locked validation toolchain")
	}
	if job.Steps[indices["Prepare generated source in this runner"]].Run != "task security:sast:prepare" {
		t.Fatal("SAST must prepare each workspace")
	}
	init := job.Steps[indices["Initialize selected CodeQL analysis"]]
	if init.ID != "codeql_init" || init.Uses != "github/codeql-action/init@2892aa5e19bbd11bc0cff5427e3b750a04d9e3c2" {
		t.Fatal("review extraction assumptions when changing the action")
	}
	analyze := job.Steps[indices["Analyze selected source language"]]
	if analyze.With["output"] != "${{ runner.temp }}/codeql-results" || analyze.With["category"] != "/language:${{ matrix.language }}" {
		t.Fatal("raw output/category contract changed")
	}
	for _, name := range []string{"Require complete analysis diagnostics", "Retain raw CodeQL diagnostics"} {
		if job.Steps[indices[name]].If != "${{ !cancelled() && steps.codeql_init.outcome == 'success' }}" {
			t.Fatalf("%s must also run on analysis failure", name)
		}
	}
	integrity := job.Steps[indices["Verify checkout integrity after analysis"]]
	if integrity.If != "${{ !cancelled() && steps.sast_helper.outcome == 'success' }}" {
		t.Fatal("integrity must run on preparation/extraction failure")
	}
	upload := job.Steps[indices["Retain raw CodeQL diagnostics"]]
	if upload.Uses != "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a" || upload.With["retention-days"] != "14" || upload.With["if-no-files-found"] != "ignore" {
		t.Fatal("diagnostic retention contract changed")
	}
	for _, fragment := range []string{"matrix.language", "github.job", "github.run_id", "github.run_attempt"} {
		if !strings.Contains(upload.With["name"], fragment) {
			t.Errorf("artifact name missing %s", fragment)
		}
	}
}

func TestSASTPreparationUsesSequentialGenerationAndBootstrapsAPI(t *testing.T) {
	var task struct {
		Cmds []struct {
			Task string `yaml:"task"`
		} `yaml:"cmds"`
		Deps []string `yaml:"deps"`
	}
	var tasks map[string]yaml.Node
	if err := yaml.Unmarshal([]byte(taskDefinition(t, repositoryText(t, "Taskfile.yml"), "security:sast:prepare")), &tasks); err != nil {
		t.Fatal(err)
	}
	node := tasks["security:sast:prepare"]
	if err := node.Decode(&task); err != nil {
		t.Fatal(err)
	}
	want := []string{"go:deps", "db:generate", "config:generate", "api:generate", "ui-signals:generate", "agent-contracts:generate", "data-resource-contracts:generate", "pipeline-contracts:generate", "desktop-discovery:generate", "layout-contract:generate", "map-style:generate", "lucide-icons:generate", "visual-docs:generate"}
	if len(task.Deps) != 0 || len(task.Cmds) != len(want) {
		t.Fatal("source preparation must be sequential")
	}
	for i, name := range want {
		if task.Cmds[i].Task != name {
			t.Errorf("generation step %d: %q, want %q", i, task.Cmds[i].Task, name)
		}
	}
}

func TestSASTShellBuildPreservesTracingAndPropagatesCommandFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux SAST runner contract")
	}
	var document struct {
		Jobs map[string]struct {
			Steps []struct{ Name, Run, Shell string } `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(repositoryYAML(t, ".github/workflows/security.yml")), &document); err != nil {
		t.Fatal(err)
	}
	var script string
	for _, step := range document.Jobs["sast-validation"].Steps {
		if step.Name == "Trace every maintained Go module" {
			script = step.Run
			if step.Shell != "/bin/bash --noprofile --norc -eo pipefail {0}" {
				t.Fatal("manual Go tracing must enter through the dynamic system shell")
			}
		}
	}
	if script == "" {
		t.Fatal("missing build step")
	}
	for _, failAt := range []int{0, 1, 2, 3, 4} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			root := t.TempDir()
			nested := filepath.Join(root, "module with space")
			if err := os.Mkdir(nested, 0700); err != nil {
				t.Fatal(err)
			}
			write := func(name, body string, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, name), []byte(body), mode); err != nil {
					t.Fatal(err)
				}
			}
			write("modules", root+"\x00"+nested+"\x00", 0600)
			write("securitysast", "#!/bin/bash\ncat \"$TEST_MODULES\"\n", 0700)
			write("go", `#!/bin/bash
printf '%s: %s\n' "$PWD" "$*" >> "$TEST_CALLS"
count=$(wc -l < "$TEST_CALLS")
if [ "$count" -eq "$TEST_FAIL" ]; then exit 23; fi
`, 0700)
			write("build.sh", script, 0600)
			calls := filepath.Join(root, "calls")
			cmd := exec.Command("/bin/bash", "--noprofile", "--norc", "-eo", "pipefail", filepath.Join(root, "build.sh"))
			cmd.Env = append(os.Environ(), "RUNNER_TEMP="+root, "PATH="+root+":"+os.Getenv("PATH"), "TEST_MODULES="+filepath.Join(root, "modules"), "TEST_CALLS="+calls, fmt.Sprintf("TEST_FAIL=%d", failAt))
			output, err := cmd.CombinedOutput()
			if (err == nil) != (failAt == 0) {
				t.Fatalf("failure=%d err=%v output=%s", failAt, err, output)
			}
			data, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			expected := []string{root + ": list -mod=readonly -deps -tags=duckdb_arrow ./...", root + ": build -a -p=2 -mod=readonly -tags=duckdb_arrow ./...", nested + ": list -mod=readonly -deps -tags=duckdb_arrow ./...", nested + ": build -a -p=2 -mod=readonly -tags=duckdb_arrow ./..."}
			if failAt > 0 {
				expected = expected[:failAt]
			}
			if strings.Join(lines, "\n") != strings.Join(expected, "\n") {
				t.Fatalf("calls=%q want=%q", lines, expected)
			}
		})
	}
}
