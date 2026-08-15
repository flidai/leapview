package artifact

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/manifest"
)

func projectFixture(t *testing.T) (projectgraph.ProjectGraph, manifest.Project) {
	t.Helper()
	graphValue, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project:demo", Kind: projectgraph.KindProject, Name: "demo"},
		{ID: "connection:warehouse", Kind: projectgraph.KindConnection, Name: "warehouse"},
		{ID: "dashboard:sales", Kind: projectgraph.KindDashboard, Name: "sales"},
	}, []projectgraph.Edge{{From: "project:demo", To: "connection:warehouse"}, {From: "project:demo", To: "dashboard:sales"}})
	if err != nil {
		t.Fatal(err)
	}
	return graphValue, manifest.Project{
		ID: "project:demo", Name: "demo", Title: "Demo",
		Connections:    map[string]semanticmodel.Connection{"connection:warehouse": {Kind: "managed"}},
		SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": {Name: "sales", Sources: map[string]semanticmodel.Source{"orders": {}}}},
		NameIndex:      manifest.NameIndex{Connections: map[string]string{"warehouse": "connection:warehouse"}, SemanticModels: map[string]string{"sales": "semantic:sales"}},
		DashboardSources: map[string]manifest.DashboardSource{
			"dashboard:sales": {Document: dashboardauthoring.Dashboard{ID: "dashboard:sales", SemanticModel: "semantic:sales"}, Path: "dashboards/sales.yaml"},
		},
		ResourceFiles: map[string]string{"dashboard:sales": "dashboards/sales.yaml"},
	}
}

func TestProjectIsDeterministicAndProjectWide(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	first, err := NewProject(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}
	projectManifest.Connections = map[string]semanticmodel.Connection{}
	second, err := NewProject(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest() == second.Digest() {
		t.Fatal("manifest mutation did not change project artifact digest")
	}
	decoded, err := Decode(first.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ProjectID() != graphValue.ProjectID() || decoded.Graph().Digest() != graphValue.Digest() {
		t.Fatalf("project identity = (%q, %q), want (%q, %q)", decoded.ProjectID(), decoded.Graph().Digest(), graphValue.ProjectID(), graphValue.Digest())
	}
	models := decoded.Models()
	model, ok := models["semantic:sales"]
	if !ok {
		t.Fatal("semantic model projection missing")
	}
	if _, ok := model.Sources["orders"]; !ok {
		t.Fatalf("semantic runtime symbolic ref was rewritten: %#v", model.Sources)
	}
	if got := decoded.Manifest().NameIndex.SemanticModels["sales"]; got != "semantic:sales" {
		t.Fatalf("name index semantic model = %q, want semantic:sales", got)
	}
	var wire map[string]any
	if err := json.Unmarshal(first.Canonical(), &wire); err != nil {
		t.Fatal(err)
	}
	if _, ok := wire["workspaces"]; ok {
		t.Fatalf("project artifact retained workspace key: %#v", wire)
	}
	if _, ok := wire["identity"]; ok {
		t.Fatalf("project artifact retained serving identity: %#v", wire)
	}
}

func TestProjectDefensivelyCopiesManifestProjections(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	project, err := NewProject(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}
	projectManifest.Connections["connection:warehouse"] = semanticmodel.Connection{Kind: "mutated"}
	connections := project.Connections()
	connections["connection:warehouse"] = semanticmodel.Connection{Kind: "mutated"}
	if got := project.Connections()["connection:warehouse"].Kind; got != "managed" {
		t.Fatalf("connection projection leaked mutation: %q", got)
	}
	source, ok := project.AuthoredDashboardSource("dashboard:sales")
	if !ok {
		t.Fatal("authored dashboard source missing")
	}
	source.Path = "mutated.yaml"
	if got, _ := project.AuthoredDashboardSource("dashboard:sales"); got.Path != "dashboards/sales.yaml" {
		t.Fatal("authored source projection leaked mutation")
	}
}

func TestProjectRejectsIdentityMismatch(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	projectManifest.ID = "project:other"
	if _, err := NewProject(graphValue, projectManifest); !errors.Is(err, projectgraph.ErrProjectIdentityMismatch) {
		t.Fatalf("NewProject() error = %v, want identity mismatch", err)
	}
}

func TestDecodeRejectsVersionUnknownDuplicateAndIdentity(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	project, err := NewProject(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		data string
		want func(error) bool
	}{
		{name: "version", data: `{"version":99}`, want: func(err error) bool { var unsupported UnsupportedVersionError; return errors.As(err, &unsupported) }},
		{name: "unknown", data: strings.Replace(string(project.Canonical()), `{"version":1,`, `{"unknown":true,"version":1,`, 1), want: func(err error) bool { return strings.Contains(err.Error(), "unknown field") }},
		{name: "duplicate case", data: strings.Replace(string(project.Canonical()), `{"version":1,`, `{"VERSION":1,"version":1,`, 1), want: func(err error) bool { return strings.Contains(err.Error(), "duplicate JSON field") }},
		{name: "trailing", data: string(project.Canonical()) + ` {"trailing":true}`, want: func(err error) bool { return strings.Contains(err.Error(), "trailing") }},
		{name: "identity", data: replaceManifestID(string(project.Canonical()), "project:other"), want: func(err error) bool { return errors.Is(err, projectgraph.ErrProjectIdentityMismatch) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Decode([]byte(test.data))
			if err == nil || !test.want(err) {
				t.Fatalf("Decode() error = %v", err)
			}
		})
	}
}

func replaceManifestID(value, replacement string) string {
	var wire map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &wire); err != nil {
		return value
	}
	var project map[string]any
	if err := json.Unmarshal(wire["manifest"], &project); err != nil {
		return value
	}
	project["id"] = replacement
	manifest, err := json.Marshal(project)
	if err != nil {
		return value
	}
	wire["manifest"] = manifest
	result, err := json.Marshal(wire)
	if err != nil {
		return value
	}
	return string(result)
}

func TestProjectRoundTripRetainsAuthoredSourceProvenance(t *testing.T) {
	graphValue, projectManifest := projectFixture(t)
	project, err := NewProject(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(project.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	source, ok := decoded.AuthoredDashboardSource("dashboard:sales")
	if !ok || source.Path != "dashboards/sales.yaml" || source.Document.ID != "dashboard:sales" {
		t.Fatalf("source = %#v, present = %v", source, ok)
	}
}

func TestCloneValueDoesNotSilentlyReturnZeroOnEncodingFailure(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("cloneValue() did not report an impossible encoding failure")
		}
	}()
	_ = cloneValue(func() {})
}
