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
	doctorReport := `{"schemaVersion":1,"status":"fail","checks":[{"id":"local.identity","status":"pass","summary":"Identity verified."},{"id":"local.platform","status":"pass","summary":"Platform supported."},{"id":"local.runtime_package","status":"pass","summary":"Runtime package verified."},{"id":"local.docker_endpoint","status":"fail","summary":"Docker unavailable."},{"id":"local.docker_compose","status":"skip","summary":"Endpoint unavailable."},{"id":"local.runtime_state","status":"skip","summary":"Prerequisites unavailable."},{"id":"project.compiler","status":"skip","summary":"No project."},{"id":"project.profile","status":"skip","summary":"No project."},{"id":"project.credentials","status":"skip","summary":"No project."}],"summary":"1 required check(s) failed."}`
	rootHelp := `printf '%s\n' 'Usage: leapview [command]' 'Examples:' 'leapview init ./analytics' 'cd ./analytics && leapview dev' 'leapview validate' 'leapview help deploy' 'Authoring:' 'Delivery:' 'Data and Query:' 'Access:' 'Operations:' 'Reference:'`
	script := `#!/bin/sh
if [ "$1" = version ]; then
  if [ "$2" = --format ] && [ "$3" = json ]; then
    printf '%s\n' '` + version + `'
  else
    exit 42
  fi
elif [ "$1" = --llms ]; then
  printf '%s\n' '# LeapView CLI agent guide' 'Binary version: 1.2.3'
elif [ "$2" = --help ]; then
  printf '%s\n' "Usage: leapview $1 [flags]"
  if [ "$1" = init ]; then
    printf '%s\n' 'credentials: {"username":"demo","password":"qualification-secret"} https://user:url-secret@example.test/?token=query-secret' "env-probe=${QUALIFICATION_UNSAFE:-unset}"
  fi
elif [ "$1" = doctor ]; then
  printf '%s\n' '` + doctorReport + `'
  exit 1
elif [ "$#" = 0 ]; then
  ` + rootHelp + `
fi
exit 0
`
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
	rawResults, ok := evidence["rawResults"].([]any)
	if !ok {
		t.Fatalf("qualification rawResults = %#v, want command records", evidence["rawResults"])
	}
	var doctorRecord map[string]any
	for _, item := range rawResults {
		record, ok := item.(map[string]any)
		if ok && record["name"] == "cli-doctor" {
			doctorRecord = record
			break
		}
	}
	if doctorRecord == nil || doctorRecord["exitCode"] != float64(1) || doctorRecord["status"] != "failed" {
		t.Fatalf("qualification doctor result = %#v, want retained exit-1 failure", doctorRecord)
	}
	validateQualificationEvidenceSchema(t, root, evidence)

	// Required lifecycle mode must fail closed before Docker mutation when the
	// explicit local Docker endpoint is absent.
	lifecycleEvidenceDir := filepath.Join(t.TempDir(), "lifecycle-evidence")
	lifecycle := exec.Command(filepath.Join(root, "deploy", "local", "qualification", "qualify.sh"),
		"--archive", archive, "--required", "--run-lifecycle", "--evidence-dir", lifecycleEvidenceDir)
	lifecycle.Dir = root
	if combined, err := lifecycle.CombinedOutput(); err == nil || !strings.Contains(string(combined), "explicit --docker-host") {
		t.Fatalf("required lifecycle prerequisite result: %v\n%s", err, combined)
	}
}

func TestReleasedAuthoringQualificationInterruptionRetainsFailedEvidence(t *testing.T) {
	root := repositoryRoot(t)
	directory := t.TempDir()
	python := `import hashlib, importlib.util, json, os, pathlib, signal, sys
spec = importlib.util.spec_from_file_location("qualification", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
root = pathlib.Path(sys.argv[2])
archive = root / "archive.tar.gz"
archive.write_bytes(b"interrupt before extraction")
archive.with_suffix(".gz.sha256").write_text(hashlib.sha256(archive.read_bytes()).hexdigest() + "  archive.tar.gz\n")
workdirs = []
def interrupt_extraction(archive, directory):
    workdirs.append(directory)
    os.kill(os.getpid(), signal.SIGINT)
module.extract_archive = interrupt_extraction
result = module.main(["--archive", str(archive), "--required", "--evidence-dir", str(root / "evidence")])
assert result == 1, result
assert workdirs and all(not path.exists() for path in workdirs)
report = json.loads((root / "evidence" / "qualification-report.json").read_text())
assert report["result"] == "failed", report
assert report["failures"] == ["qualification interrupted"]
assert (root / "evidence" / "raw-results.json").is_file()
`
	command := exec.Command("python3", "-c", python, filepath.Join(root, "deploy/local/qualification/qualify.py"), directory)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("interrupted harness did not retain failed evidence and clean up: %v\n%s", err, output)
	}
	var evidence map[string]any
	body, err := os.ReadFile(filepath.Join(directory, "evidence/qualification-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &evidence); err != nil {
		t.Fatal(err)
	}
	validateQualificationEvidenceSchema(t, root, evidence)
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

func TestReleasedAuthoringQualificationDockerEngineProviderPolicy(t *testing.T) {
	root := repositoryRoot(t)
	python := `
import importlib.util
import json
import pathlib
import stat
import sys
import types
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("leapview_qualify", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
home = pathlib.Path("/Users/author")
expected = {
    "/var/run/docker.sock": "docker-desktop",
    "/private/var/run/docker.sock": "docker-desktop",
    "/Users/author/.orbstack/run/docker.sock": "orbstack",
    "/Users/author/.colima/default/docker.sock": "colima",
    "/Users/author/.colima/analytics/docker.sock": "colima",
    "/Users/author/.rd/docker.sock": "rancher-desktop",
}
for path, kind in expected.items():
    if module.local_socket_kind(path, home, "Darwin") != kind:
        raise SystemExit(f"unexpected socket policy for {path}")
for path in ("/Users/author/.colima/docker.sock", "/Users/author/.local/share/containers/podman/machine/podman.sock"):
    if module.local_socket_kind(path, home, "Darwin") is not None:
        raise SystemExit(f"unsupported socket accepted: {path}")

with patch.object(module.platform, "system", return_value="Darwin"), \
     patch.object(module.Path, "home", return_value=home), \
     patch.object(module.os.path, "realpath", return_value=str(home / ".orbstack/run/docker.sock")), \
     patch.object(module.os, "stat", return_value=types.SimpleNamespace(st_mode=stat.S_IFSOCK, st_uid=module.os.getuid())):
    if module.normalize_docker_host("unix:///private/var/run/docker.sock") != "unix:///Users/author/.orbstack/run/docker.sock":
        raise SystemExit("private default socket did not resolve to the OrbStack Engine socket")

with patch.object(module.platform, "system", return_value="Darwin"), \
     patch.object(module.Path, "home", return_value=home), \
     patch.object(module.os.path, "realpath", return_value=str(home / ".orbstack/run/docker.sock")), \
     patch.object(module.os, "stat", return_value=types.SimpleNamespace(st_mode=stat.S_IFSOCK, st_uid=module.os.getuid() + 1)):
    try:
        module.normalize_docker_host("unix:///private/var/run/docker.sock")
    except module.QualificationSkip as error:
        if "owner" not in str(error):
            raise
    else:
        raise SystemExit("provider socket owned by another user passed qualification")

module.run_command = lambda *args, **kwargs: {"output": json.dumps({"Components": [{"Name": "Podman Engine"}], "Version": "5.0"})}
try:
    module.docker_server_identity("docker", "unix:///var/run/docker.sock", [], 5, home, "probe")
except module.QualificationError as error:
    if "Docker Engine" not in str(error):
        raise
else:
    raise SystemExit("Podman compatibility endpoint passed Docker Engine qualification")
`
	command := exec.Command("python3", "-c", python, filepath.Join(root, "deploy", "local", "qualification", "qualify.py"))
	command.Dir = root
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Docker Engine provider policy: %v\n%s", err, combined)
	}
}

func TestReleasedAuthoringQualificationForwardsNativeKeyringSessionOnly(t *testing.T) {
	root := repositoryRoot(t)
	python := `
import importlib.util
import os
import pathlib
import sys
import tempfile

spec = importlib.util.spec_from_file_location("leapview_qualify", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
with tempfile.TemporaryDirectory() as temporary:
    environment = module.command_environment(None, pathlib.Path(temporary))
if environment.get("DBUS_SESSION_BUS_ADDRESS") != "unix:path=/tmp/qualification-bus":
    raise SystemExit("native keyring session was not forwarded")
if "QUALIFICATION_UNSAFE" in environment:
    raise SystemExit("unsafe environment variable was forwarded")
`
	command := exec.Command("python3", "-c", python, filepath.Join(root, "deploy", "local", "qualification", "qualify.py"))
	command.Dir = root
	command.Env = append(os.Environ(),
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/qualification-bus",
		"QUALIFICATION_UNSAFE=must-not-forward",
		"PYTHONDONTWRITEBYTECODE=1",
	)
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("inspect qualification environment: %v\n%s", err, combined)
	}
}

func TestReleasedAuthoringQualificationRejectsInvalidOfflineSurfaces(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("authoring archives support Linux and macOS")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("authoring archives support amd64 and arm64")
	}
	root := repositoryRoot(t)
	version := `{"version":"1.2.3","revision":"` + strings.Repeat("a", 40) + `","buildTime":"2026-09-15T12:00:00Z","dirty":false,"development":false}`
	doctorReport := `{"schemaVersion":1,"status":"fail","checks":[{"id":"local.identity","status":"pass","summary":"Identity verified."},{"id":"local.platform","status":"pass","summary":"Platform supported."},{"id":"local.runtime_package","status":"pass","summary":"Runtime package verified."},{"id":"local.docker_endpoint","status":"fail","summary":"Docker unavailable."},{"id":"local.docker_compose","status":"skip","summary":"Endpoint unavailable."},{"id":"local.runtime_state","status":"skip","summary":"Prerequisites unavailable."},{"id":"project.compiler","status":"skip","summary":"No project."},{"id":"project.profile","status":"skip","summary":"No project."},{"id":"project.credentials","status":"skip","summary":"No project."}],"summary":"1 required check(s) failed."}`
	rootHelp := `printf '%s\n' 'Usage: leapview [command]' 'Examples:' 'leapview init ./analytics' 'cd ./analytics && leapview dev' 'leapview validate' 'leapview help deploy' 'Authoring:' 'Delivery:' 'Data and Query:' 'Access:' 'Operations:' 'Reference:'`
	for _, test := range []struct {
		name         string
		help         string
		rootHelp     string
		guidance     string
		doctorOutput string
		doctorExit   string
		failure      string
	}{
		{name: "missing help", help: ":", failure: "cli-help-init"},
		{name: "root help instead of command help", help: `printf '%s\n' 'Usage: leapview [command]'`, failure: "cli-help-init"},
		{name: "command help creates local state", help: `printf '%s\n' "Usage: leapview $1 [flags]"; touch "$HOME/.config/unexpected-state"`, failure: "created local CLI state"},
		{name: "root help missing a public group", help: `printf '%s\n' "Usage: leapview $1 [flags]"`, rootHelp: strings.Replace(rootHelp, "'Authoring:' ", "", 1), failure: "grouped discovery content"},
		{name: "root help missing authoring example", help: `printf '%s\n' "Usage: leapview $1 [flags]"`, rootHelp: strings.Replace(rootHelp, "'leapview validate' ", "", 1), failure: "grouped discovery content"},
		{name: "missing agent guidance", help: `printf '%s\n' "Usage: leapview $1 [flags]"`, guidance: ":", failure: "cli-agent-guidance"},
		{name: "discovery creates local state", help: `printf '%s\n' "Usage: leapview $1 [flags]"`, guidance: `printf '%s\n' '# LeapView CLI agent guide'; touch "$HOME/.config/unexpected-state"`, failure: "created local CLI state"},
		{name: "doctor malformed JSON", help: `printf '%s\n' "Usage: leapview $1 [flags]"`, doctorOutput: "not-json", failure: "complete JSON report"},
		{name: "doctor false success", help: `printf '%s\n' "Usage: leapview $1 [flags]"`, doctorOutput: doctorReport, doctorExit: "0", failure: "exit status disagrees"},
		{name: "doctor invalid exit code", help: `printf '%s\n' "Usage: leapview $1 [flags]"`, doctorOutput: doctorReport, doctorExit: "2", failure: "outside the documented 0/1"},
		{name: "doctor missing a required local check", help: `printf '%s\n' "Usage: leapview $1 [flags]"`, doctorOutput: strings.Replace(doctorReport, `,{"id":"project.credentials","status":"skip","summary":"No project."}`, "", 1), failure: "omitted required local check IDs"},
	} {
		t.Run(test.name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "leapview")
			rootOutput := test.rootHelp
			if rootOutput == "" {
				rootOutput = rootHelp
			}
			doctorOutput := test.doctorOutput
			if doctorOutput == "" {
				doctorOutput = doctorReport
			}
			doctorExit := test.doctorExit
			if doctorExit == "" {
				doctorExit = "1"
			}
			guidance := test.guidance
			if guidance == "" {
				guidance = "printf '%s\\n' '# LeapView CLI agent guide'"
			}
			script := "#!/bin/sh\nif [ \"$1\" = version ]; then if [ \"$2\" = --format ] && [ \"$3\" = json ]; then printf '%s\\n' '" + version + "'; else exit 42; fi; elif [ \"$2\" = --help ]; then " + test.help + "; elif [ \"$1\" = --llms ]; then " + guidance + "; elif [ \"$1\" = doctor ]; then printf '%s\\n' '" + doctorOutput + "'; exit " + doctorExit + "; elif [ \"$#\" = 0 ]; then " + rootOutput + "; fi\nexit 0\n"
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
			if err == nil || !strings.Contains(string(combined), test.failure) {
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
		"leapview version --format json",
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
