package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/localplan"
)

func TestDataPlanCommandPlansWithPreviousManifest(t *testing.T) {
	previous := manageddata.Manifest{Files: []manageddata.File{{
		Path: "old.csv", Size: 1, SHA256: strings.Repeat("a", 64),
	}}}
	previousBytes, err := previous.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	previousPath := filepath.Join(t.TempDir(), "previous.json")
	if err := os.WriteFile(previousPath, previousBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	manifest := manageddata.Manifest{Files: []manageddata.File{{
		Path: "new.csv", Size: 2, SHA256: strings.Repeat("b", 64),
	}}}
	planner := &recordingDataPlanner{result: localplan.Result{
		Connection: "warehouse",
		Root:       "/project/data",
		Sources:    []string{"warehouse.files"},
		Manifest:   manifest,
		Diff: manageddata.Diff{
			Added:   append([]manageddata.File(nil), manifest.Files...),
			Removed: append([]manageddata.File(nil), previous.Files...),
		},
	}}
	command := dataCommandWithPlanner(context.Background(), planner)
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stdout)
	command.SetArgs([]string{
		"plan",
		"--source-root", "/project",
		"--connection", "warehouse",
		"--from", "/local/export",
		"--previous-manifest", previousPath,
	})

	if err := command.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if planner.request.SourceRoot != "/project" || planner.request.Connection != "warehouse" {
		t.Fatalf("planner request = %#v", planner.request)
	}
	if planner.request.From != "/local/export" {
		t.Fatalf("planner from = %q", planner.request.From)
	}
	if planner.request.Previous == nil || len(planner.request.Previous.Files) != 1 || planner.request.Previous.Files[0].Path != "old.csv" {
		t.Fatalf("planner previous manifest = %#v", planner.request.Previous)
	}

	var output dataPlanOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output %q: %v", stdout.String(), err)
	}
	if output.Connection != "warehouse" || output.RevisionID != manifest.RevisionID() {
		t.Fatalf("output = %#v", output)
	}
	if !equalDataPlanFiles(output.Manifest.Files, manifest.Files) {
		t.Fatalf("output manifest = %#v", output.Manifest)
	}
	if len(output.Diff.Added) != 1 || len(output.Diff.Removed) != 1 {
		t.Fatalf("output diff = %#v", output.Diff)
	}
}

func TestDataCommandRetainsStagingAndInspectionCommandsOnly(t *testing.T) {
	command := dataCommandWithPlanner(context.Background(), &recordingDataPlanner{})
	got := map[string]bool{}
	for _, child := range command.Commands() {
		got[child.Name()] = true
	}
	for _, want := range []string{"plan", "sync", "revisions"} {
		if !got[want] {
			t.Fatalf("data command missing %q: %#v", want, got)
		}
	}
	if got["deploy"] {
		t.Fatalf("data command still registers removed deploy subcommand: %#v", got)
	}
}

func TestDataPlanCommandRequiresConnection(t *testing.T) {
	command := dataCommandWithPlanner(context.Background(), &recordingDataPlanner{})
	command.SetArgs([]string{"plan"})

	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "connection is required") {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestDataPlanCommandRequiresFrom(t *testing.T) {
	command := dataCommandWithPlanner(context.Background(), &recordingDataPlanner{})
	command.SetArgs([]string{"plan", "--connection", "warehouse"})

	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "from is required") {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestDataPlanCommandRejectsInvalidPreviousManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "previous.json")
	if err := os.WriteFile(path, []byte(`{"files":[]} trailing`), 0o600); err != nil {
		t.Fatal(err)
	}
	command := dataCommandWithPlanner(context.Background(), &recordingDataPlanner{})
	command.SetArgs([]string{"plan", "--connection", "warehouse", "--from", t.TempDir(), "--previous-manifest", path})

	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "previous manifest") {
		t.Fatalf("Execute() error = %v", err)
	}
}

type recordingDataPlanner struct {
	request localplan.Request
	result  localplan.Result
	err     error
}

func (p *recordingDataPlanner) Plan(_ context.Context, request localplan.Request) (localplan.Result, error) {
	p.request = request
	return p.result, p.err
}

func equalDataPlanFiles(got, want []manageddata.File) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
