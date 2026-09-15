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
	requireWriteFile(t, binary, "#!/bin/sh\nif [ \"$1\" = version ]; then printf '%s\\n' '"+version+"'; fi\nexit 0\n", 0o755)
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
	if combined, err := packageCommand.CombinedOutput(); err != nil {
		t.Fatalf("package authoring CLI: %v\n%s", err, combined)
	}
	archive := filepath.Join(output, "leapview-cli-v1.2.3-"+runtime.GOOS+"-"+runtime.GOARCH+".tar.gz")
	evidenceDir := filepath.Join(t.TempDir(), "evidence")
	qualify := exec.Command(filepath.Join(root, "deploy", "local", "qualification", "qualify.sh"),
		"--archive", archive, "--required", "--evidence-dir", evidenceDir)
	qualify.Dir = root
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
	if evidence["result"] != "passed" {
		t.Fatalf("qualification result = %#v, want passed", evidence["result"])
	}
	if strings.Contains(string(encoded), "ghcr.io/flidai/leapview@sha256:") == false {
		t.Fatal("qualification evidence should retain the immutable image identity")
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

func TestReleasedAuthoringQualificationContractDeclaresPendingMeasurements(t *testing.T) {
	root := repositoryRoot(t)
	contract := readFile(t, filepath.Join("qualification", "qualification-contract.json"))
	for _, required := range []string{
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
