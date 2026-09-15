package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/manageddata/localplan"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	"github.com/flidai/leapview/internal/project/developmentinput"
)

func TestInitCommandCreatesCompilableReproducibleManagedProject(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "analytics")
	command := NewCommand(context.Background())
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"init", target})
	if err := command.Execute(); err != nil {
		t.Fatalf("init: %v\n%s", err, output.String())
	}
	if _, err := projectcompiler.LoadSourceRoot(filepath.Join(target, "dashboards")); err != nil {
		t.Fatalf("compile initialized source: %v", err)
	}
	selected, err := developmentinput.Load(target, "sample")
	if err != nil {
		t.Fatal(err)
	}
	dataCommand := NewCommand(context.Background())
	var planOutput bytes.Buffer
	dataCommand.SetOut(&planOutput)
	dataCommand.SetErr(&planOutput)
	dataCommand.SetArgs([]string{"data", "plan", "--development-input", "sample", "--project-root", target})
	if err := dataCommand.Execute(); err != nil {
		t.Fatalf("plan declared development input: %v\n%s", err, planOutput.String())
	}
	planner := localplan.NewService(loadManagedDataPlanCatalog)
	first, err := planner.Plan(context.Background(), localplan.Request{SourceRoot: filepath.Join(target, "dashboards"), Connection: selected.Connection, From: selected.Root})
	if err != nil {
		t.Fatalf("plan initialized fixture: %v", err)
	}
	secondTarget := filepath.Join(parent, "analytics-copy")
	secondCommand := NewCommand(context.Background())
	secondCommand.SetOut(&bytes.Buffer{})
	secondCommand.SetErr(&bytes.Buffer{})
	secondCommand.SetArgs([]string{"init", secondTarget})
	if err := secondCommand.Execute(); err != nil {
		t.Fatal(err)
	}
	secondSelected, err := developmentinput.Load(secondTarget, "sample")
	if err != nil {
		t.Fatal(err)
	}
	second, err := planner.Plan(context.Background(), localplan.Request{SourceRoot: filepath.Join(secondTarget, "dashboards"), Connection: secondSelected.Connection, From: secondSelected.Root})
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.RevisionID() == "" || first.Manifest.RevisionID() != second.Manifest.RevisionID() {
		t.Fatalf("fixture revisions differ: %q != %q", first.Manifest.RevisionID(), second.Manifest.RevisionID())
	}
	if !bytes.Contains(planOutput.Bytes(), []byte(first.Manifest.RevisionID())) {
		t.Fatalf("declared input plan omitted revision %q: %s", first.Manifest.RevisionID(), planOutput.String())
	}
	dashboardPath := filepath.Join(target, "dashboards", "dashboards", "sales-overview.yaml")
	dashboard, err := os.ReadFile(dashboardPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dashboardPath, append(dashboard, []byte("\n# presentation-only edit\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	afterCodeEdit, err := planner.Plan(context.Background(), localplan.Request{SourceRoot: filepath.Join(target, "dashboards"), Connection: selected.Connection, From: selected.Root})
	if err != nil {
		t.Fatal(err)
	}
	if afterCodeEdit.Manifest.RevisionID() != first.Manifest.RevisionID() {
		t.Fatalf("code edit changed data revision: %q != %q", afterCodeEdit.Manifest.RevisionID(), first.Manifest.RevisionID())
	}

	fixturePath := filepath.Join(target, "data", "sample", "sales.csv")
	fixture, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	changedFixture := bytes.Replace(fixture, []byte("1770.00"), []byte("1780.00"), 1)
	if bytes.Equal(changedFixture, fixture) {
		t.Fatal("fixture mutation did not change content")
	}
	if err := os.WriteFile(fixturePath, changedFixture, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(changedFixture)
	manifestPath := filepath.Join(target, filepath.FromSlash(developmentinput.DefaultRelativePath))
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest = bytes.Replace(manifest, []byte(selected.Files[0].SHA256), []byte(hex.EncodeToString(digest[:])), 1)
	if err := os.WriteFile(manifestPath, manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	changedSelection, err := developmentinput.Load(target, "sample")
	if err != nil {
		t.Fatal(err)
	}
	afterFixtureEdit, err := planner.Plan(context.Background(), localplan.Request{
		SourceRoot: filepath.Join(target, "dashboards"), Connection: changedSelection.Connection, From: changedSelection.Root, Previous: &first.Manifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if afterFixtureEdit.Manifest.RevisionID() == first.Manifest.RevisionID() || len(afterFixtureEdit.Diff.Changed) != 1 {
		t.Fatalf("explicit fixture update did not create a changed revision: %#v", afterFixtureEdit)
	}
	if _, err := os.Stat(filepath.Join(target, "dashboards", ".leapview")); !os.IsNotExist(err) {
		t.Fatal("machine state was written into the portable source root")
	}
}
