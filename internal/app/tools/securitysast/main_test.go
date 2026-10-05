package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, root, path, value string) {
	t.Helper()
	name := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func moduleFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module example.com/root\n\ngo 1.26\n")
	writeFixture(t, root, "nested/go.mod", "module example.com/nested\n\ngo 1.26\n")
	writeFixture(t, root, ".security/coverage.yaml", `version: 1
surfaces:
  - path: nested/go.mod
    kind: go-module
    updater: {ecosystem: gomod, directory: /nested}
    scanners: [govulncheck]
  - path: go.mod
    kind: go-module
    updater: {ecosystem: gomod, directory: /}
    scanners: [govulncheck]
`)
	return root
}

func TestModuleDirectoriesUseEveryInventoriedModule(t *testing.T) {
	root := moduleFixture(t)
	modules, err := moduleDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(modules) != 2 || modules[0] != root || modules[1] != filepath.Join(root, "nested") {
		t.Fatalf("modules=%v", modules)
	}
}

func TestBuildRejectsIncompleteAndUnsafeInventory(t *testing.T) {
	for _, name := range []string{"empty", "duplicate", "escape", "missing", "uncovered", "symlink"} {
		t.Run(name, func(t *testing.T) {
			root := moduleFixture(t)
			file := filepath.Join(root, ".security/coverage.yaml")
			data, _ := os.ReadFile(file)
			switch name {
			case "empty":
				data = []byte("version: 1\nsurfaces: []\n")
			case "duplicate":
				data = []byte(strings.ReplaceAll(string(data), "nested/go.mod", "go.mod"))
			case "escape":
				data = []byte(strings.ReplaceAll(string(data), "nested/go.mod", "../go.mod"))
			case "missing":
				if err := os.Remove(filepath.Join(root, "nested/go.mod")); err != nil {
					t.Fatal(err)
				}
			case "uncovered":
				writeFixture(t, root, "extra/go.mod", "module example.com/extra\n")
			case "symlink":
				outside := t.TempDir()
				writeFixture(t, outside, "go.mod", "module example.com/external\n")
				if err := os.Remove(filepath.Join(root, "nested/go.mod")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "go.mod"), filepath.Join(root, "nested/go.mod")); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(file, data, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := moduleDirectories(root)
			if err == nil {
				t.Fatal("accepted invalid inventory")
			}
		})
	}
}

func gitFixture(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestIntegrityDetectsMutationDeletionAndNewIgnoredDependencyFiles(t *testing.T) {
	for _, mutation := range []string{"none", "modified", "deleted", "new sum", "ignored lock", "new module", "tracked source", "symlink", "dependency cache"} {
		t.Run(mutation, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, "go.mod", "module example.com/test\n")
			writeFixture(t, root, "code.go", "package test\n")
			writeFixture(t, root, ".gitignore", "*.lock\nnode_modules/\n")
			gitFixture(t, root, "init", "--quiet")
			gitFixture(t, root, "add", ".")
			gitFixture(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "-m", "fixture")
			revision := gitFixture(t, root, "rev-parse", "HEAD")
			switch mutation {
			case "modified":
				writeFixture(t, root, "go.mod", "module example.com/changed\n")
			case "deleted":
				if err := os.Remove(filepath.Join(root, "go.mod")); err != nil {
					t.Fatal(err)
				}
			case "new sum":
				writeFixture(t, root, "go.sum", "unexpected\n")
			case "ignored lock":
				writeFixture(t, root, "bun.lock", "{}\n")
			case "new module":
				writeFixture(t, root, "unexpected/go.mod", "module example.com/new\n")
			case "tracked source":
				writeFixture(t, root, "code.go", "package changed\n")
			case "symlink":
				if err := os.Remove(filepath.Join(root, "go.mod")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("code.go", filepath.Join(root, "go.mod")); err != nil {
					t.Fatal(err)
				}
			case "dependency cache":
				writeFixture(t, root, "node_modules/example/package.json", "{}\n")
			}
			err := checkIntegrity(context.Background(), root, revision)
			shouldPass := mutation == "none" || mutation == "dependency cache"
			if (err == nil) != shouldPass {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func healthySARIF() map[string]any {
	return map[string]any{"version": "2.1.0", "runs": []any{map[string]any{
		"tool":              map[string]any{"driver": map[string]any{"name": "CodeQL"}},
		"automationDetails": map[string]any{"id": "/language:go/"},
		"invocations":       []any{map[string]any{"executionSuccessful": true}},
		"results":           []any{map[string]any{"ruleId": "go/example", "level": "error"}},
	}}}
}

func TestSARIFHealth(t *testing.T) {
	for _, name := range []string{"healthy with finding", "note", "none", "warning", "error", "default warning", "configuration warning", "failed", "missing success", "no invocations", "no runs", "wrong tool", "wrong category", "bad version", "invalid level", "second run warning", "second invocation warning", "indexed descriptor", "invalid descriptor index"} {
		t.Run(name, func(t *testing.T) {
			doc := healthySARIF()
			runs := doc["runs"].([]any)
			run := runs[0].(map[string]any)
			invocation := run["invocations"].([]any)[0].(map[string]any)
			notification := map[string]any{"descriptor": map[string]any{"id": "go/missing-package"}, "message": map[string]any{"text": "missing generated package"}, "level": "warning"}
			switch name {
			case "note", "none", "warning", "error", "invalid level", "default warning":
				notification["level"] = name
				if name == "default warning" {
					delete(notification, "level")
				}
				invocation["toolExecutionNotifications"] = []any{notification}
			case "configuration warning":
				invocation["toolConfigurationNotifications"] = []any{notification}
			case "failed":
				invocation["executionSuccessful"] = false
			case "missing success":
				delete(invocation, "executionSuccessful")
			case "no invocations":
				delete(run, "invocations")
			case "no runs":
				doc["runs"] = []any{}
			case "wrong tool":
				run["tool"].(map[string]any)["driver"].(map[string]any)["name"] = "GitHub Code Scanning"
			case "wrong category":
				run["automationDetails"] = map[string]any{"id": "/language:javascript-typescript/"}
			case "bad version":
				doc["version"] = "2.0.0"
			case "second run warning":
				other := healthySARIF()["runs"].([]any)[0].(map[string]any)
				other["invocations"].([]any)[0].(map[string]any)["toolExecutionNotifications"] = []any{notification}
				doc["runs"] = append(runs, other)
			case "second invocation warning":
				run["invocations"] = append(run["invocations"].([]any), map[string]any{"executionSuccessful": true, "toolExecutionNotifications": []any{notification}})
			case "indexed descriptor", "invalid descriptor index":
				driver := run["tool"].(map[string]any)["driver"].(map[string]any)
				driver["notifications"] = []any{map[string]any{"id": "go/missing-package"}}
				index := 0
				if name == "invalid descriptor index" {
					index = 1
				}
				notification["descriptor"] = map[string]any{"index": index}
				invocation["toolExecutionNotifications"] = []any{notification}
			}
			encoded, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			err = validateSARIF(encoded, "/language:go")
			wantPass := name == "healthy with finding" || name == "note" || name == "none"
			if (err == nil) != wantPass {
				t.Fatalf("error=%v", err)
			}
			if name == "indexed descriptor" && !strings.Contains(err.Error(), "go/missing-package") {
				t.Fatalf("lost diagnostic id: %v", err)
			}
		})
	}
	for _, data := range []string{"", "null", "{}", "{", `{"version":"2.1.0","runs":[]} {}`} {
		if err := validateSARIF([]byte(data), "/language:go"); err == nil {
			t.Fatalf("accepted invalid SARIF %q", data)
		}
	}
}

func TestCLIRejectsMissingSARIFAndInvalidCommands(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"sarif"}, {"sarif", "-input", filepath.Join(t.TempDir(), "missing.sarif"), "-category", "/language:go"}, {"modules", "unexpected"}, {"integrity", "-revision", "HEAD"}} {
		if err := run(context.Background(), args); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

func TestSARIFDescriptorReferencesAndDiagnosticLocations(t *testing.T) {
	for _, test := range []struct {
		name, descriptor string
		valid            bool
	}{
		{"driver index", `{"index":0}`, true},
		{"driver id/index", `{"id":"go/extraction","index":0}`, true},
		{"extension index", `{"index":0,"toolComponent":{"index":0,"name":"extractor"}}`, true},
		{"extension name", `{"index":0,"toolComponent":{"name":"extractor"}}`, true},
		{"mismatched id", `{"index":0,"id":"different"}`, false},
		{"negative descriptor", `{"index":-1}`, false},
		{"missing extension", `{"index":0,"toolComponent":{"name":"missing"}}`, false},
		{"bad component index", `{"index":0,"toolComponent":{"index":1}}`, false},
		{"bad component name", `{"index":0,"toolComponent":{"index":0,"name":"other"}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"CodeQL","notifications":[{"id":"go/extraction"}]},"extensions":[{"name":"extractor","notifications":[{"id":"go/extraction"}]}]},"automationDetails":{"id":"/language:go/"},"invocations":[{"executionSuccessful":true,"toolExecutionNotifications":[{"level":"warning","descriptor":%s,"message":{"text":"missing import"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"internal/generated.go"},"region":{"startLine":7}}}]}]}]}]}`, test.descriptor)
			err := validateSARIF([]byte(raw), "/language:go")
			if err == nil {
				t.Fatal("warning passed")
			}
			if test.valid {
				for _, fragment := range []string{"go/extraction", "missing import", "internal/generated.go:7"} {
					if !strings.Contains(err.Error(), fragment) {
						t.Errorf("lost diagnostic context %q: %v", fragment, err)
					}
				}
			}
			note := strings.Replace(raw, `"level":"warning"`, `"level":"note"`, 1)
			if err := validateSARIF([]byte(note), "/language:go"); (err == nil) != test.valid {
				t.Fatalf("descriptor validity: %v", err)
			}
		})
	}
}

func TestRawHostedJavaScriptHealthFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/javascript-raw-health.sarif")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSARIF(data, "/language:javascript-typescript"); err != nil {
		t.Fatal(err)
	}
	warning := strings.Replace(string(data), `"level": "none"`, `"level": "warning"`, 1)
	if err := validateSARIF([]byte(warning), "/language:javascript-typescript"); err == nil {
		t.Fatal("injected warning accepted")
	}
}
