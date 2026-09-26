package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPIGenUsesTypedClientGenerator(t *testing.T) {
	root := projectRoot(t)
	manifest, err := os.ReadFile(filepath.Join(root, "api", "apigen.yaml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	manifestText := string(manifest)
	for _, source := range []string{
		"typespec_entrypoint: typespec/main.tsp",
		"typespec_entrypoint: signals/main.tsp",
		"typespec_entrypoint: visualization/main.tsp",
		"typespec_entrypoint: dashboard/main.tsp",
		"typespec_entrypoint: data-resources/main.tsp",
		"typespec_entrypoint: desktop-discovery/main.tsp",
		"typespec_entrypoint: internal/agent/contracts/typespec/main.tsp",
	} {
		if !strings.Contains(manifestText, source) {
			t.Fatalf("manifest should select TypeSpec source %q, got:\n%s", source, manifestText)
		}
	}
	if strings.Contains(manifestText, "cue_dir:") {
		t.Fatalf("manifest should not use cue_dir after APIGen v0.3.0 migration")
	}
	for _, want := range []string{
		"unmatched: error",
		"LeapViewAPI:",
		"LeapViewAPI.Access:",
		"LeapViewAPI.Agent:",
		"LeapViewAPI.Analytics:",
		"LeapViewAPI.Dashboard:",
		"LeapViewAPI.Deployment:",
		"LeapViewAPI.ManagedData:",
		"LeapViewAPI.Project:",
		"LeapViewAPI.Protocol:",
		"LeapViewAPI.Refresh:",
		"LeapViewAPI.Release:",
		"import_path: github.com/flidai/leapview/internal/app/api/gen",
	} {
		if !strings.Contains(manifestText, want) {
			t.Fatalf("manifest should define the coalesced capability package plan setting %q", want)
		}
	}

	taskfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	if err != nil {
		t.Fatalf("read Taskfile.yml: %v", err)
	}
	taskText := string(taskfile)
	for _, want := range []string{
		"- task: api:generate\n      - task: agent-contracts:generate\n      - task: ui-signals:generate\n      - task: schema:generate",
		"- task: desktop-discovery:generate",
		"schema:generate:\n    desc: Generate JSON Schema artifacts for LeapView YAML contracts\n    deps:\n      - db:generate\n      - config:generate\n      - api:generate\n      - ui-signals:generate",
		"ui-signals:generate:\n    desc: Generate UI signal Go and TypeScript contracts from TypeSpec\n    deps:\n      - api:generate",
	} {
		if !strings.Contains(taskText, want) {
			t.Fatalf("Taskfile.yml does not enforce generated-model ordering %q", want)
		}
	}
	assertAPIGenCommands := func(label, text string, targets []string) {
		for _, target := range targets {
			for _, command := range []string{"typespec-compile", "all"} {
				want := "go -C pkg/apigen run ./cmd/apigen " + command + " -manifest ../../api/apigen.yaml -target " + target
				if !strings.Contains(text, want) {
					t.Fatalf("%s missing nested APIGen command %q", label, want)
				}
			}
		}
	}
	assertAPIGenCommands("Taskfile.yml", taskText, []string{"leapview-v1", "agent-tool-contracts", "data-resource-contracts", "desktop-discovery-contracts", "ui-signals", "visualization-ir", "dashboard-contracts"})
	if !strings.Contains(taskText, "go -C pkg/apigen test ./...") {
		t.Fatal("Taskfile.yml missing APIGen module test command")
	}
	for _, forbidden := range []string{"cue-compile", "apigen@v0.2.0", "apigen@v0.3.0", "apigen@v0.3.2", "apigen@v0.3.3", "apigen@v0.4.0", "apigen@v0.5.0", "apigen@v0.5.1", "apigen@v0.5.2", "apigen@v0.5.3", "apigen@v0.6.0", "apigen@v0.6.1", "apigen@v0.6.2", "apigen@v0.6.3", "apigen@v0.6.4", "apigen@v0.6.5", "apigen@v0.7.0", "apigen@v0.7.1", "apigen@v0.7.2", "apigen@v0.7.3", "apigenpostprocess"} {
		if strings.Contains(taskText, forbidden) {
			t.Fatalf("Taskfile.yml should not contain superseded generator %q", forbidden)
		}
	}
	buildSources, err := os.ReadFile(filepath.Join(root, "scripts", "generate_build_sources.sh"))
	if err != nil {
		t.Fatalf("read container source-generation script: %v", err)
	}
	assertAPIGenCommands("container source-generation script", string(buildSources), []string{"leapview-v1", "data-resource-contracts", "ui-signals", "desktop-discovery-contracts", "visualization-ir"})
	goMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	if !strings.Contains(string(goMod), "replace github.com/Yacobolo/toolbelt/apigen => ./pkg/apigen") {
		t.Fatal("go.mod does not select the vendored APIGen module")
	}
	if _, err := os.Stat(filepath.Join(root, "pkg", "apigen", "UPSTREAM.md")); err != nil {
		t.Fatalf("vendored APIGen provenance is missing: %v", err)
	}
	if want := "go run ./internal/app/tools/layoutcontractgen"; !strings.Contains(string(buildSources), want) {
		t.Fatalf("container source-generation script missing layout contract generation %q", want)
	}

	ir, err := os.ReadFile(filepath.Join(root, "api", "gen", "json-ir.json"))
	if err != nil {
		t.Fatalf("read APIGen IR: %v", err)
	}
	var irDoc map[string]any
	if err := json.Unmarshal(ir, &irDoc); err != nil {
		t.Fatalf("decode APIGen IR: %v", err)
	}
	if got := irDoc["schema_version"]; got != "v4" {
		t.Fatalf("APIGen IR schema_version = %#v, want v4", got)
	}

	if _, err := os.Stat(filepath.Join(root, "internal", "tools", "apigenpostprocess")); !os.IsNotExist(err) {
		t.Fatalf("APIGen should not require a postprocessor, stat error = %v", err)
	}
	for path, forbidden := range map[string]string{
		filepath.Join(root, "api", "typespec", "bi.tsp"):                        "toolbelt#34",
		filepath.Join(root, "internal", "agent", "tools", "apigen_provider.go"): "projectUnionToolResult",
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(content), forbidden) {
			t.Fatalf("APIGen superseded workaround %q in %s", forbidden, path)
		}
	}
}
