package main

import (
	"bytes"
	"debug/buildinfo"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func binaryStream(info *debug.BuildInfo) []byte {
	config := strings.ReplaceAll(govulnConfigMessage, `"source"`, `"binary"`)
	sbom := govulnSBOM{GoVersion: info.GoVersion, Roots: []string{info.Main.Path}, Modules: binaryModules(info)}
	data, _ := json.Marshal(map[string]any{"SBOM": sbom})
	return append([]byte(config+"\n"), data...)
}

func TestBinaryEvidenceRejectsIncompleteOrSubstitutedScans(t *testing.T) {
	info := &debug.BuildInfo{GoVersion: "go1.26.8", Main: debug.Module{Path: "example.com/app", Version: "(devel)"}, Deps: []*debug.Module{{Path: "example.com/lib", Version: "v1.2.3", Replace: &debug.Module{Path: "example.com/replacement", Version: "v1.3.0"}}}}
	valid := binaryStream(info)
	for _, test := range []struct {
		name string
		data []byte
	}{
		{"source mode", bytes.ReplaceAll(valid, []byte(`"binary"`), []byte(`"source"`))},
		{"wrong Go version", bytes.ReplaceAll(valid, []byte("go1.26.8"), []byte("go1.25.0"))},
		{"wrong root", bytes.ReplaceAll(valid, []byte("example.com/app"), []byte("example.com/other"))},
		{"wrong replacement", bytes.ReplaceAll(valid, []byte("example.com/replacement"), []byte("example.com/lib"))},
		{"wrong module version", bytes.ReplaceAll(valid, []byte("v1.3.0"), []byte("v1.0.0"))},
		{"truncated", valid[:len(valid)-1]},
		{"duplicate config field", bytes.ReplaceAll(valid, []byte(`"scan_mode":"binary"`), []byte(`"scan_mode":"source","scan_mode":"binary"`))},
		{"wrong database", bytes.ReplaceAll(valid, []byte("https://vuln.go.dev"), []byte("file:///tmp/db"))},
		{"warning", append(append([]byte(nil), valid...), []byte("\n"+`{"progress":{"message":"warning: incomplete binary"}}`)...)},
		{"vulnerable symbol", append(append([]byte(nil), valid...), []byte("\n"+`{"osv":{"id":"GO-2026-0001"}}`+"\n"+`{"finding":{"osv":"GO-2026-0001","trace":[{"module":"stdlib","package":"net/http","function":"*"}]}}`)...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validateBinaryStream(test.data, info); err == nil {
				t.Fatal("unsafe binary evidence accepted")
			}
		})
	}
	if _, err := validateBinaryStream(valid, info); err != nil {
		t.Fatalf("valid binary evidence: %v", err)
	}
}

func TestSiteBinaryRequiresExactStaticCGOFreeELF(t *testing.T) {
	module := t.TempDir()
	if err := os.WriteFile(filepath.Join(module, "go.mod"), []byte("module github.com/flidai/leapview\n\ngo 1.26.8\n"), 0600); err != nil {
		t.Fatal(err)
	}
	siteDir := filepath.Join(module, "cmd", "leapview-site")
	if err := os.MkdirAll(siteDir, 0700); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(siteDir, "main.go")
	writeMain := func(source string) {
		t.Helper()
		if err := os.WriteFile(main, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	build := func(output, cgo string) []byte {
		t.Helper()
		command := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", output, "./cmd/leapview-site")
		command.Dir = module
		command.Env = make([]string, 0, len(os.Environ())+4)
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "CGO_ENABLED=") && !strings.HasPrefix(value, "GOOS=") &&
				!strings.HasPrefix(value, "GOARCH=") && !strings.HasPrefix(value, "GOTOOLCHAIN=") {
				command.Env = append(command.Env, value)
			}
		}
		command.Env = append(command.Env, "CGO_ENABLED="+cgo, "GOOS=linux", "GOARCH=amd64", "GOTOOLCHAIN=local")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build site fixture with CGO_ENABLED=%s: %v: %s", cgo, err, output)
		}
		data, err := os.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	writeMain("package main\nfunc main() {}\n")
	static := build(filepath.Join(module, "site-static"), "0")
	if _, _, err := inspectExactBinary(static, siteMainPackage, "linux/amd64"); err != nil {
		t.Fatalf("static site binary rejected: %v", err)
	}
	if _, _, err := inspectExactBinary(static, "github.com/flidai/leapview/cmd/leapviewctl", "linux/amd64"); err == nil {
		t.Fatal("wrong main package accepted for site binary")
	}
	if _, _, err := inspectExactBinary(build(filepath.Join(module, "site-cgo-enabled"), "1"), siteMainPackage, "linux/amd64"); err == nil {
		t.Fatal("CGO_ENABLED=1 site binary accepted")
	}
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("C compiler unavailable for dynamic ELF fixture")
	}
	writeMain("package main\n/* int site_value(void) { return 1; } */\nimport \"C\"\nfunc main() { _ = C.site_value() }\n")
	dynamic := build(filepath.Join(module, "site-dynamic"), "1")
	if _, _, err := inspectExactBinary(dynamic, siteMainPackage, "linux/amd64"); err == nil {
		t.Fatal("dynamically linked site binary accepted")
	}
}

func TestExactBinaryEvidenceRoundTripAndFailurePaths(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n\ngo 1.26\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "app")
	command := exec.Command("go", "build", "-ldflags=-s -w", "-o", binary, ".")
	command.Dir = dir
	command.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v: %s", err, output)
	}
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	r := &runner{root: dir, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, timeout: time.Second, now: func() time.Time { return now }, govulncheckPath: "/trusted/govulncheck"}
	r.govulnCommand = func(_ string, executable string, args ...string) commandResult {
		if executable != "/trusted/govulncheck" || len(args) != 4 || strings.Join(args[:3], " ") != "-mode=binary -scan=symbol -json" || args[3] == binary {
			t.Fatalf("unexpected scan: %s %v", executable, args)
		}
		original, _ := os.ReadFile(binary)
		snapshot, err := os.ReadFile(args[3])
		if err != nil || !bytes.Equal(original, snapshot) {
			t.Fatal("scanner did not read exact private snapshot")
		}
		return commandResult{stdout: binaryStream(info)}
	}
	out := filepath.Join(dir, "evidence")
	if err := r.scanBinaryEvidence(binary, "example.com/app", "linux/amd64", out); err != nil {
		t.Fatal(err)
	}
	// Verification is offline and never provisions or executes the scanner/binary.
	r.govulnCommand = func(string, string, ...string) commandResult {
		t.Fatal("verification executed a command")
		return commandResult{}
	}
	if err := r.verifyBinaryEvidence(binary, "example.com/app", "linux/amd64", out); err != nil {
		t.Fatal(err)
	}
	if err := r.verifyBinaryEvidence(binary, "example.com/other", "linux/amd64", out); err == nil {
		t.Fatal("wrong program accepted")
	}
	if err := r.verifyBinaryEvidence(binary, "example.com/app", "linux/arm64", out); err == nil {
		t.Fatal("wrong architecture accepted")
	}
	r.now = func() time.Time { return now.Add(120 * time.Hour) }
	if err := r.verifyBinaryEvidence(binary, "example.com/app", "linux/amd64", out); err == nil {
		t.Fatal("expired evidence accepted")
	}
	r.now = func() time.Time { return now.Add(-time.Second) }
	if err := r.verifyBinaryEvidence(binary, "example.com/app", "linux/amd64", out); err == nil {
		t.Fatal("future evidence accepted")
	}
	r.now = func() time.Time { return now }
	originalSummary, err := os.ReadFile(filepath.Join(out, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func([]byte) []byte{
		func(data []byte) []byte { return append(data, []byte("{}")...) },
		func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"scope": "go-binary-only"`), []byte(`"scope": "go-binary-only", "scope": "go-binary-only"`), 1)
		},
		func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"schemaVersion": 1`), []byte(`"schemaVersion": true`), 1)
		},
		func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"scanner_name": "govulncheck"`), []byte(`"scanner_name": "substituted"`), 1)
		},
	} {
		if err := os.WriteFile(filepath.Join(out, "summary.json"), mutate(originalSummary), 0600); err != nil {
			t.Fatal(err)
		}
		if err := r.verifyBinaryEvidence(binary, "example.com/app", "linux/amd64", out); err == nil {
			t.Fatal("mutated summary accepted")
		}
	}
	if err := os.WriteFile(filepath.Join(out, "summary.json"), originalSummary, 0600); err != nil {
		t.Fatal(err)
	}
	for _, result := range []commandResult{{stdout: binaryStream(info), status: 1}, {stdout: binaryStream(info), timedOut: true}, {stdout: binaryStream(info), stderr: []byte("diagnostic")}, {stdout: []byte("{}")}} {
		r.govulnCommand = func(string, string, ...string) commandResult { return result }
		failureDir := filepath.Join(t.TempDir(), "failed")
		if err := r.scanBinaryEvidence(binary, "example.com/app", "linux/amd64", failureDir); err == nil {
			t.Fatal("failed scanner accepted")
		}
		if _, err := os.Stat(filepath.Join(failureDir, "summary.json")); !os.IsNotExist(err) {
			t.Fatal("failure left a success receipt")
		}
	}
	if err := os.WriteFile(filepath.Join(out, "govulncheck.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.verifyBinaryEvidence(binary, "example.com/app", "linux/amd64", out); err == nil {
		t.Fatal("mutated raw report accepted")
	}
	if err := os.WriteFile(filepath.Join(out, "govulncheck.json"), binaryStream(info), 0600); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, append(original, 'x'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.verifyBinaryEvidence(binary, "example.com/app", "linux/amd64", out); err == nil {
		t.Fatal("mutated binary accepted")
	}
	if err := os.WriteFile(binary, original, 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(binary, alias); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readExactBinary(alias, "example.com/app", "linux/amd64"); err == nil {
		t.Fatal("binary symlink accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "blob"), []byte(`{"path":"example.com/app","goVersion":"go1.26.8"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readExactBinary(filepath.Join(dir, "blob"), "example.com/app", "linux/amd64"); err == nil {
		t.Fatal("serialized govulncheck input accepted")
	}
}

func TestBinaryScannerCaptureIsBoundedAndFailsOnOverflow(t *testing.T) {
	capture := commandCapture{limit: 4}
	for _, chunk := range []string{"abc", "def", "ghi"} {
		if count, err := capture.Write([]byte(chunk)); err != nil || count != len(chunk) {
			t.Fatal("capture stopped draining child output")
		}
	}
	if capture.String() != "abcd" || !capture.overflow {
		t.Fatalf("capture is not bounded: %q overflow=%v", capture.String(), capture.overflow)
	}
	r := &runner{timeout: time.Second}
	result := r.commandWithEnvLimited(t.TempDir(), "go", nil, 4, 4, "version")
	if result.err == nil || len(result.stdout) > 4 {
		t.Fatal("successful oversized command was accepted")
	}
}
