package local

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestReleasedAuthoringQualificationStaticJourney(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("authoring archives support Linux and macOS")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("authoring archives support amd64 and arm64")
	}
	root := repositoryRoot(t)
	binary := filepath.Join(t.TempDir(), "leapview")
	version := `{"version":"1.2.3","revision":"` + strings.Repeat("a", 40) + `","buildTime":"2026-09-15T12:00:00Z","dirty":false,"development":false}`
	requireWriteFile(t, binary, "#!/bin/sh\nif [ \"$1\" = version ]; then printf '%s\\n' '"+version+"'; elif [ \"$2\" = --help ]; then case \"$1\" in init) printf '%s\\n' 'Usage:' '  leapview init [flags]' 'credentials: {\"username\":\"demo\",\"password\":\"qualification-secret\"} https://user:url-secret@example.test/?token=query-secret' \"env-probe=${QUALIFICATION_UNSAFE:-unset}\";; dev) printf '%s\\n' 'Usage: leapview dev [flags]';; plan) printf '%s\\n' 'Usage: leapview plan [flags]';; build) printf '%s\\n' 'Usage: leapview build [flags]';; publish) printf '%s\\n' 'Usage: leapview publish [flags]';; deploy) printf '%s\\n' 'Usage: leapview deploy [flags]';; esac; fi\nexit 0\n", 0o755)
	output := t.TempDir()
	packageCommand := exec.Command(filepath.Join(root, "scripts", "package-authoring-cli.sh"), binary, output, runtime.GOOS, runtime.GOARCH)
	packageCommand.Dir = root
	packageCommand.Env = append(os.Environ(),
		"BUILD_VERSION=1.2.3",
		"BUILD_REVISION="+strings.Repeat("a", 40),
		"BUILD_TIME=2026-09-15T12:00:00Z",
		"BUILD_RELEASE=true",
		"IMAGE_REFERENCE=ghcr.io/flidai/leapview@sha256:"+strings.Repeat("b", 64),
		"RELEASE_TAG=candidate-12345-1",
	)
	if combined, err := packageCommand.CombinedOutput(); err != nil {
		t.Fatalf("package authoring CLI: %v\n%s", err, combined)
	}
	archive := filepath.Join(output, "leapview-cli-candidate-12345-1-"+runtime.GOOS+"-"+runtime.GOARCH+".tar.gz")
	evidenceDir := filepath.Join(t.TempDir(), "evidence")
	qualify := exec.Command(filepath.Join(root, "deploy", "local", "qualification", "qualify.sh"),
		"--archive", archive, "--required", "--evidence-dir", evidenceDir)
	qualify.Dir = root
	qualify.Env = append(os.Environ(), "QUALIFICATION_UNSAFE=must-not-forward", "DOCKER_CONTEXT=must-not-forward")
	if combined, err := qualify.CombinedOutput(); err != nil {
		t.Fatalf("static qualification: %v\n%s", err, combined)
	}
	var evidence map[string]any
	encoded, err := os.ReadFile(filepath.Join(evidenceDir, "qualification-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence["result"] != "partial" {
		t.Fatalf("qualification result = %#v, want partial static result", evidence["result"])
	}
	if strings.Contains(string(encoded), "ghcr.io/flidai/leapview@sha256:") == false {
		t.Fatal("qualification evidence should retain the immutable image identity")
	}
	for _, secret := range []string{"qualification-secret", "url-secret", "query-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("qualification evidence contains an unredacted secret %q", secret)
		}
	}
	if !strings.Contains(string(encoded), "env-probe=unset") {
		t.Fatal("qualification subprocess inherited an unsafe host environment variable")
	}
	if strings.Contains(string(encoded), "password=") || strings.Contains(string(encoded), "Authorization: Bearer") {
		t.Fatal("qualification evidence contains an unredacted credential-shaped value")
	}
	validateQualificationEvidenceSchema(t, root, evidence)

	// Required lifecycle mode must fail closed before Docker mutation when the
	// explicit human authentication prerequisite is absent.
	lifecycleEvidenceDir := filepath.Join(t.TempDir(), "lifecycle-evidence")
	lifecycle := exec.Command(filepath.Join(root, "deploy", "local", "qualification", "qualify.sh"),
		"--archive", archive, "--required", "--run-lifecycle", "--evidence-dir", lifecycleEvidenceDir)
	lifecycle.Dir = root
	if combined, err := lifecycle.CombinedOutput(); err == nil || !strings.Contains(string(combined), "device authentication") {
		t.Fatalf("required lifecycle prerequisite result: %v\n%s", err, combined)
	}
}

func TestReleasedAuthoringQualificationRejectsArchiveChecksumDrift(t *testing.T) {
	root := repositoryRoot(t)
	archive := filepath.Join(t.TempDir(), "missing.tar.gz")
	checksum := archive + ".sha256"
	if err := os.WriteFile(archive, []byte("not-an-archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checksum, []byte(strings.Repeat("0", 64)+"  missing.tar.gz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	evidenceDir := filepath.Join(t.TempDir(), "evidence")
	qualify := exec.Command(filepath.Join(root, "deploy", "local", "qualification", "qualify.sh"),
		"--archive", archive, "--required", "--evidence-dir", evidenceDir)
	qualify.Dir = root
	if combined, err := qualify.CombinedOutput(); err == nil {
		t.Fatalf("checksum drift unexpectedly passed: %s", combined)
	}
	encoded, err := os.ReadFile(filepath.Join(evidenceDir, "qualification-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"result": "failed"`)) {
		t.Fatalf("checksum drift evidence did not fail: %s", encoded)
	}
}

func TestReleasedAuthoringQualificationStreamsInteractiveOutputAndRetainsEvidence(t *testing.T) {
	root := repositoryRoot(t)
	python := `
import importlib.util
import pathlib
import sys
import tempfile

spec = importlib.util.spec_from_file_location("leapview_qualify", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
with tempfile.TemporaryDirectory() as temporary:
    result = module.run_command(
        "interactive-test",
        [sys.executable, "-c", "print('Open http://127.0.0.1/device and enter code ABCD-EFGH')"],
        [],
        10,
        pathlib.Path(temporary),
        live_output=True,
    )
if result["status"] != "passed" or "ABCD-EFGH" not in result["output"]:
    raise SystemExit("interactive output was not retained")
print("retained")
`
	command := exec.Command("python3", "-c", python, filepath.Join(root, "deploy", "local", "qualification", "qualify.py"))
	command.Dir = root
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("stream interactive qualification output: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "retained" {
		t.Fatalf("retained result = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Open http://127.0.0.1/device and enter code ABCD-EFGH") {
		t.Fatalf("live output = %q", stderr.String())
	}
}

func TestReleasedAuthoringQualificationRejectsMissingOrWrongCommandHelp(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("authoring archives support Linux and macOS")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("authoring archives support amd64 and arm64")
	}
	root := repositoryRoot(t)
	version := `{"version":"1.2.3","revision":"` + strings.Repeat("a", 40) + `","buildTime":"2026-09-15T12:00:00Z","dirty":false,"development":false}`
	for _, test := range []struct {
		name string
		help string
	}{
		{name: "missing help", help: ":"},
		{name: "root help instead of command help", help: "printf '%s\\n' 'Usage: leapview [command]'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "leapview")
			script := "#!/bin/sh\nif [ \"$1\" = version ]; then printf '%s\\n' '" + version + "'; elif [ \"$2\" = --help ]; then " + test.help + "; fi\nexit 0\n"
			requireWriteFile(t, binary, script, 0o755)
			output := t.TempDir()
			packageCommand := exec.Command(filepath.Join(root, "scripts", "package-authoring-cli.sh"), binary, output, runtime.GOOS, runtime.GOARCH)
			packageCommand.Dir = root
			packageCommand.Env = append(os.Environ(),
				"BUILD_VERSION=1.2.3",
				"BUILD_REVISION="+strings.Repeat("a", 40),
				"BUILD_TIME=2026-09-15T12:00:00Z",
				"BUILD_RELEASE=true",
				"IMAGE_REFERENCE=ghcr.io/flidai/leapview@sha256:"+strings.Repeat("b", 64),
				"RELEASE_TAG=v1.2.3",
			)
			combined, err := packageCommand.CombinedOutput()
			if err != nil {
				t.Fatalf("package authoring CLI: %v\n%s", err, combined)
			}
			archive := strings.TrimSpace(string(combined))
			evidenceDir := filepath.Join(t.TempDir(), "evidence")
			qualify := exec.Command(filepath.Join(root, "deploy", "local", "qualification", "qualify.sh"), "--archive", archive, "--required", "--evidence-dir", evidenceDir)
			qualify.Dir = root
			combined, err = qualify.CombinedOutput()
			if err == nil || !strings.Contains(string(combined), "cli-help-init") {
				t.Fatalf("unrecognized command help unexpectedly passed: %v\n%s", err, combined)
			}
		})
	}
}

func TestReleasedAuthoringQualificationContractDeclaresPendingMeasurements(t *testing.T) {
	root := repositoryRoot(t)
	contract := readFile(t, filepath.Join("qualification", "qualification-contract.json"))
	for _, required := range []string{
		"milestone-5-static",
		"staticResult",
		"partial",
		"outerChecksum",
		"innerManifest",
		"leapview version --json",
		"coldUncached",
		"coldCached",
		"warmRestart",
		"editToVisible",
		"semantic",
		"model",
		"dashboard",
		"presentation",
		"invalid",
		"Only observed command or browser samples",
		"previewEditToVisibleQualification",
	} {
		if !strings.Contains(contract, required) {
			t.Errorf("qualification contract missing %q", required)
		}
	}
	for _, path := range []string{
		filepath.Join(root, "deploy", "local", "qualification", "qualify.sh"),
		filepath.Join(root, "deploy", "local", "qualification", "qualify.py"),
		filepath.Join(root, "deploy", "local", "qualification", "evidence.schema.json"),
		filepath.Join(root, "deploy", "local", "qualification", "qualification-contract.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("qualification asset missing: %v", err)
		}
	}
}

func validateQualificationEvidenceSchema(t *testing.T, root string, evidence map[string]any) {
	t.Helper()
	encoded := readFile(t, filepath.Join(root, "deploy", "local", "qualification", "evidence.schema.json"))
	document, err := jsonschema.UnmarshalJSON(bytes.NewBufferString(encoded))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("qualification-evidence.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("qualification-evidence.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(evidence); err != nil {
		t.Fatalf("qualification evidence does not match schema: %v", err)
	}
}
