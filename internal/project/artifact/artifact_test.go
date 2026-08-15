package artifact

import (
	"encoding/json"
	"errors"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/publication"
	"github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/workspace"
)

func TestProjectDefensivelyCopiesNestedWorkspaceState(t *testing.T) {
	definition := &manifest.Workspace{Publications: map[string]publication.Definition{
		"public": {Name: "public", DependencyAssetIDs: []string{"dashboard:one"}},
	}}
	project, err := NewProject("example", map[string]WorkspaceInput{
		"sales": {Metadata: workspace.Workspace{ID: "sales", Graph: workspace.AssetGraph{Assets: []workspace.Asset{{ID: "dashboard:sales.one"}}}}, Manifest: definition},
	})
	if err != nil {
		t.Fatal(err)
	}
	definition.Publications["public"] = publication.Definition{Name: "changed"}

	compiled, ok := project.Workspace("sales")
	if !ok {
		t.Fatal("workspace is missing")
	}
	first := compiled.Manifest()
	first.Publications["public"] = publication.Definition{Name: "mutated"}
	first.Catalog.Workspace.Title = "mutated"
	second := compiled.Manifest()
	if got := second.Publications["public"].Name; got != "public" {
		t.Fatalf("publication name = %q, want immutable public", got)
	}
	if got := second.Catalog.Workspace.Title; got == "mutated" {
		t.Fatal("nested metadata escaped the immutable artifact")
	}
	metadata := compiled.Metadata()
	metadata.Graph.Assets[0].ID = "dashboard:changed"
	if got := compiled.Metadata().Graph.Assets[0].ID; got != "dashboard:sales.one" {
		t.Fatalf("asset id = %q, want defensive copy", got)
	}
}

func TestProjectCanonicalDigestIsStableAcrossMapInsertionOrder(t *testing.T) {
	first, err := NewProject("example", map[string]WorkspaceInput{
		"zeta":  {Metadata: workspace.Workspace{ID: "zeta"}, Manifest: &manifest.Workspace{}},
		"alpha": {Metadata: workspace.Workspace{ID: "alpha"}, Manifest: &manifest.Workspace{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewProject("example", map[string]WorkspaceInput{
		"alpha": {Metadata: workspace.Workspace{ID: "alpha"}, Manifest: &manifest.Workspace{}},
		"zeta":  {Metadata: workspace.Workspace{ID: "zeta"}, Manifest: &manifest.Workspace{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest() != second.Digest() || string(first.Canonical()) != string(second.Canonical()) {
		t.Fatalf("canonical artifacts differ:\n%s\n%s", first.Canonical(), second.Canonical())
	}
	ids := first.WorkspaceIDs()
	if got := ids[0] + "," + ids[1]; got != "alpha,zeta" {
		t.Fatalf("workspace ids = %q, want sorted", got)
	}
}

func TestDashboardDefinitionIsAnImmutableCapabilityProjection(t *testing.T) {
	project, err := NewProject("example", map[string]WorkspaceInput{
		"sales": {
			Metadata: workspace.Workspace{ID: "sales"},
			Manifest: &manifest.Workspace{
				Models: map[string]*semanticmodel.Model{
					"orders": {Name: "orders", Title: "Orders"},
				},
				DashboardDefinitions: map[string]dashboarddefinition.Definition{
					"overview": {ID: "overview", Title: "Overview"},
				},
				Catalog: manifest.Catalog{
					Workspace:  manifest.CatalogWorkspace{ID: "sales", Title: "Sales"},
					Dashboards: []manifest.CatalogDashboard{{ID: "overview", Title: "Overview", Tags: []string{"core"}}},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(project.Canonical(), &wire); err != nil {
		t.Fatal(err)
	}
	workspaces, ok := wire["workspaces"].(map[string]any)
	if !ok {
		t.Fatalf("canonical workspaces = %#v", wire["workspaces"])
	}
	sales, ok := workspaces["sales"].(map[string]any)
	if !ok {
		t.Fatalf("canonical sales workspace = %#v", workspaces["sales"])
	}
	manifest, ok := sales["manifest"].(map[string]any)
	if !ok {
		t.Fatalf("canonical manifest = %#v", sales["manifest"])
	}
	if _, ok := manifest["Dashboards"]; ok {
		t.Fatalf("canonical manifest retained authored Dashboards collection: %#v", manifest["Dashboards"])
	}
	if _, ok := manifest["DashboardDefinitions"]; !ok {
		t.Fatalf("canonical manifest omitted DashboardDefinitions: %#v", manifest)
	}
	compiled, ok := project.Workspace("sales")
	if !ok {
		t.Fatal("workspace is missing")
	}
	first := compiled.DashboardDefinition()
	first.Catalog.Workspace.Title = "changed"
	first.Catalog.Dashboards[0].Tags[0] = "changed"
	first.Models["orders"].Title = "Changed"
	changedDashboard := first.Dashboards["overview"]
	changedDashboard.Title = "Changed"
	first.Dashboards["overview"] = changedDashboard

	second := compiled.DashboardDefinition()
	if second.Catalog.Workspace.Title != "Sales" ||
		second.Catalog.Dashboards[0].Tags[0] != "core" ||
		second.Models["orders"].Title != "Orders" ||
		second.Dashboards["overview"].Title != "Overview" {
		t.Fatalf("dashboard projection retained caller mutation: %#v", second)
	}
}

func TestDecodeRejectsUnsupportedArtifactVersion(t *testing.T) {
	_, err := Decode([]byte(`{"version":0,"projectId":"example","workspaces":{}}`))
	var unsupported UnsupportedVersionError
	if !errors.As(err, &unsupported) {
		t.Fatalf("Decode() error = %v, want UnsupportedVersionError", err)
	}
}

func TestJSONRoundTripRetainsDigest(t *testing.T) {
	project, err := NewProject("example", map[string]WorkspaceInput{
		"sales": {Metadata: workspace.Workspace{ID: "sales"}, Manifest: &manifest.Workspace{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Digest() != project.Digest() {
		t.Fatalf("digest = %q, want %q", decoded.Digest(), project.Digest())
	}
}
