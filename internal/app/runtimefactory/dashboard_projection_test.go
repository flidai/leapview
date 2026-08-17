package runtimefactory

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

func TestProjectManifestReturnsDetachedCompiledDefinition(t *testing.T) {
	runtime := dashboardRuntimeWithGraph{projectManifest: projectmanifest.Project{
		ID: "project:demo",
		Models: map[string]semanticmodel.Table{
			"model:orders": {PrimaryKey: "order_id", Sources: []string{"orders"}},
		},
	}}

	first := runtime.ProjectManifest()
	model := first.Models["model:orders"]
	model.PrimaryKey = "changed"
	model.Sources[0] = "changed"
	first.Models["model:orders"] = model

	second := runtime.ProjectManifest()
	if second.Models["model:orders"].PrimaryKey != "order_id" || second.Models["model:orders"].Sources[0] != "orders" {
		t.Fatalf("project manifest aliases caller mutation: %#v", second.Models["model:orders"])
	}
}

func TestAuthoredDashboardSourcesRetainsDescriptiveDomain(t *testing.T) {
	projectID, err := projectgraph.NewResourceID("project:demo")
	if err != nil {
		t.Fatal(err)
	}
	dashboardID, err := projectgraph.NewResourceID("dashboard:sales")
	if err != nil {
		t.Fatal(err)
	}
	sources, err := authoredDashboardSources(projectmanifest.Project{
		DashboardSources: map[string]projectmanifest.DashboardSource{
			string(dashboardID): {
				Document: dashboardauthoring.Dashboard{ID: dashboardID},
				Metadata: projectmanifest.DashboardSourceMetadata{
					Name: "sales_dashboard", Domain: "revenue", Tags: []string{"finance"},
				},
			},
		},
	}, projectID)
	if err != nil {
		t.Fatal(err)
	}
	source, ok := sources[string(dashboardID)]
	if !ok {
		t.Fatal("authored dashboard source was not projected")
	}
	if source.Metadata.Domain != "revenue" {
		t.Fatalf("projected authored dashboard domain = %q, want revenue", source.Metadata.Domain)
	}
	if source.Metadata.Project != projectID {
		t.Fatalf("projected authored dashboard project = %q, want %q", source.Metadata.Project, projectID)
	}
}
