package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/localplan"
	"github.com/flidai/leapview/internal/platform/cliapi"
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

func TestDataPlanCommandUsesOnlyVerifiedDevelopmentInputSelection(t *testing.T) {
	selected := testDevelopmentInput()
	planner := &recordingDataPlanner{result: localplan.Result{Connection: "connection:sample", ConnectionName: selected.Connection, Root: selected.From, Manifest: selected.Manifest}}
	dependencies := Dependencies{ResolveDevelopmentInput: func(projectRoot, name string) (DevelopmentInput, error) {
		if projectRoot != "/project" || name != "sample" {
			t.Fatalf("development selection = %q %q", projectRoot, name)
		}
		return selected, nil
	}}
	command := dataCommandWithOptions(context.Background(), planner, dependencies, &options{})
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stdout)
	command.SetArgs([]string{"plan", "--development-input", "sample", "--project-root", "/project"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if planner.request.SourceRoot != "/project/dashboards" || planner.request.Connection != "sample" || planner.request.From != "/project/data/sample" {
		t.Fatalf("planner request = %#v", planner.request)
	}
	var output dataPlanOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if output.DevelopmentInput == nil || output.DevelopmentInput.Name != "sample" || output.DevelopmentInput.RevisionID != selected.Manifest.RevisionID() || !output.DevelopmentInput.Provenance.Bounded || output.DevelopmentInput.Provenance.Rows != 12 {
		t.Fatalf("development input evidence = %#v", output.DevelopmentInput)
	}

	conflict := dataCommandWithOptions(context.Background(), planner, dependencies, &options{})
	conflict.SetArgs([]string{"plan", "--development-input", "sample", "--connection", "other"})
	if err := conflict.Execute(); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestDataSyncCommandUsesOnlyVerifiedDevelopmentInputSelection(t *testing.T) {
	called := false
	selected := testDevelopmentInput()
	dependencies := Dependencies{ResolveDevelopmentInput: func(projectRoot, name string) (DevelopmentInput, error) {
		called = true
		if projectRoot != "/project" || name != "sample" {
			t.Fatalf("development selection = %q %q", projectRoot, name)
		}
		return selected, nil
	}}
	command := dataCommandWithOptions(context.Background(), &recordingDataPlanner{}, dependencies, &options{})
	command.SetArgs([]string{"sync", "--development-input", "sample", "--project-root", "/project"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "explicit --target and --project-id") {
		t.Fatalf("sync error = %v, want ambient target rejection after selection", err)
	}
	if !called {
		t.Fatal("sync did not resolve the declared development input")
	}

	called = false
	conflict := dataCommandWithOptions(context.Background(), &recordingDataPlanner{}, dependencies, &options{})
	conflict.SetArgs([]string{"sync", "--development-input", "sample", "--from", "/other"})
	if err := conflict.Execute(); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("conflict error = %v", err)
	}
	if called {
		t.Fatal("conflicting sync flags resolved development input")
	}

	production := dataCommandWithOptions(context.Background(), &recordingDataPlanner{result: localplan.Result{Connection: "connection:sample", ConnectionName: selected.Connection, Root: selected.From, Manifest: selected.Manifest}}, Dependencies{
		Client:                  developmentInputTargetClient{environment: "prod"},
		ResolveDevelopmentInput: dependencies.ResolveDevelopmentInput,
	}, &options{})
	production.SetArgs([]string{"sync", "--development-input", "sample", "--project-root", "/project", "--target", "https://prod.example", "--project-id", "project:prod"})
	if err := production.Execute(); err == nil || !strings.Contains(err.Error(), "verified dev environment") {
		t.Fatalf("production target error = %v", err)
	}
}

func TestDataPlanRejectsFixtureMutationAfterSelection(t *testing.T) {
	selected := testDevelopmentInput()
	changed := selected.Manifest
	changed.Files = append([]manageddata.File(nil), changed.Files...)
	changed.Files[0].SHA256 = strings.Repeat("b", 64)
	command := dataCommandWithOptions(context.Background(), &recordingDataPlanner{result: localplan.Result{
		Connection: "connection:sample", ConnectionName: selected.Connection, Root: selected.From, Manifest: changed,
	}}, Dependencies{ResolveDevelopmentInput: func(string, string) (DevelopmentInput, error) { return selected, nil }}, &options{})
	command.SetArgs([]string{"plan", "--development-input", "sample"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "changed after validation") {
		t.Fatalf("mutation error = %v", err)
	}
}

func testDevelopmentInput() DevelopmentInput {
	return DevelopmentInput{
		Name: "sample", SourceRoot: "/project/dashboards", Connection: "sample", From: "/project/data/sample",
		Manifest:   manageddata.Manifest{Files: []manageddata.File{{Path: "sales.csv", Size: 1, SHA256: strings.Repeat("a", 64)}}},
		Provenance: DevelopmentInputProvenance{Kind: "synthetic", Generator: "test/v1", Rows: 12, Bounded: true},
	}
}

type developmentInputTargetClient struct{ environment string }

func (client developmentInputTargetClient) Resolve(_ context.Context, credentials cliapi.Credentials) (cliapi.Credentials, error) {
	return credentials, nil
}

func (client developmentInputTargetClient) Environment(context.Context, cliapi.Credentials, string) (string, error) {
	return client.environment, nil
}

func (developmentInputTargetClient) Transport(context.Context, cliapi.Credentials) (apigenclient.Transport, error) {
	return nil, nil
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
	return slices.Equal(got, want)
}
