package compiler

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	configschema "github.com/flidai/leapview/internal/project/schema"
)

func TestExportDashboardIsDeterministicAndUsesCanonicalEnvelope(t *testing.T) {
	project := mustExportFixtureProject(t)
	dashboard := *project.Workspaces["sales"].Dashboards["executive-sales"]
	metadata := DashboardExportMetadata{
		Workspace: "sales", Title: dashboard.Title, Description: dashboard.Description,
		Owner: "bi@example.test", Tags: []string{"sales", "revenue"},
	}
	first, err := ExportDashboard(dashboard, metadata)
	if err != nil {
		t.Fatalf("ExportDashboard() error = %v", err)
	}
	second, err := ExportDashboard(dashboard, metadata)
	if err != nil {
		t.Fatalf("second ExportDashboard() error = %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("export is not deterministic:\n%s\n---\n%s", first, second)
	}
	if err := configschema.ValidateBytes(configschema.KindDashboardResource, "dashboard.yaml", first); err != nil {
		t.Fatalf("exported bytes fail schema validation: %v", err)
	}
	text := string(first)
	for _, required := range []string{"semanticModel: sales", "components:", "workspace: sales", "title: Executive Sales", "description: Revenue, AOV, category, and order volume overview.", "owner: bi@example.test", "tags:"} {
		if !strings.Contains(text, required) {
			t.Fatalf("required canonical field %q missing from export:\n%s", required, text)
		}
	}
	for _, forbidden := range []string{"chart:", "tabular:"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("non-canonical authoring union field %q leaked into export", forbidden)
		}
	}
}

func TestExportDashboardRoundTripsThroughProjectLoaderAndCompiler(t *testing.T) {
	project := mustExportFixtureProject(t)
	authored := *project.Workspaces["sales"].Dashboards["executive-sales"]
	metadata := DashboardExportMetadata{
		Workspace: "sales", Title: authored.Title, Description: authored.Description,
		Owner: "bi@example.test", Tags: []string{"sales", "revenue"},
	}
	content, err := ExportDashboard(authored, metadata)
	if err != nil {
		t.Fatalf("ExportDashboard() error = %v", err)
	}
	root := copyExportFixture(t)
	dashboardPath := filepath.Join(root, "workspaces", "sales", "dashboards", "executive-sales.yaml")
	if err := os.WriteFile(dashboardPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadProject(filepath.Join(root, "leapview.yaml"))
	if err != nil {
		t.Fatalf("LoadProject(exported) error = %v", err)
	}
	envelope, err := readEnvelope(dashboardPath)
	if err != nil {
		t.Fatalf("read exported envelope: %v", err)
	}
	if envelope.Metadata.Workspace != metadata.Workspace || envelope.Metadata.Title != metadata.Title || envelope.Metadata.Description != metadata.Description || envelope.Metadata.Owner != metadata.Owner || !reflect.DeepEqual(envelope.Metadata.Tags, metadata.Tags) {
		t.Fatalf("exported metadata = %#v, want %#v", envelope.Metadata, metadata)
	}
	roundTripped := *loaded.Workspaces["sales"].Dashboards["executive-sales"]
	if !reflect.DeepEqual(authored, roundTripped) {
		t.Fatalf("authored dashboard changed after loader round-trip:\noriginal=%#v\nround-trip=%#v", authored, roundTripped)
	}
	if _, err := CompileProject(filepath.Join(root, "leapview.yaml"), Options{}); err != nil {
		t.Fatalf("CompileProject(exported) error = %v", err)
	}
}

func TestExportDashboardRejectsInvalidDocumentAndCompiledOnlyInput(t *testing.T) {
	project := mustExportFixtureProject(t)
	dashboard := *project.Workspaces["sales"].Dashboards["executive-sales"]
	dashboard.ID = ""
	if _, err := ExportDashboard(dashboard, DashboardExportMetadata{}); err == nil {
		t.Fatal("ExportDashboard() accepted an invalid authored dashboard")
	}
	if _, err := ExportDashboard(*project.Workspaces["sales"].Dashboards["executive-sales"], DashboardExportMetadata{Name: "different"}); err == nil {
		t.Fatal("ExportDashboard() accepted mismatched metadata name")
	}
	_, err := ExportDashboardDefinition(dashboarddefinition.Definition{ID: "executive-sales"}, DashboardExportMetadata{})
	if !errors.Is(err, ErrDashboardSourceUnavailable) {
		t.Fatalf("compiled-only export error = %v, want ErrDashboardSourceUnavailable", err)
	}
}

func TestExportDashboardVisualShowcase(t *testing.T) {
	project := mustExportFixtureProject(t)
	dashboard := *project.Workspaces["visuals"].Dashboards["visual-showcase"]
	if _, err := ExportDashboard(dashboard, DashboardExportMetadata{Workspace: "visuals"}); err != nil {
		t.Fatal(err)
	}
}

func mustExportFixtureProject(t *testing.T) Project {
	t.Helper()
	project, err := LoadProject("../../../dashboards/leapview.yaml")
	if err != nil {
		t.Fatalf("LoadProject(fixture) error = %v", err)
	}
	return project
}

func copyExportFixture(t *testing.T) string {
	t.Helper()
	source := filepath.Clean("../../../dashboards")
	destination := t.TempDir()
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy dashboard fixture: %v", err)
	}
	return destination
}
