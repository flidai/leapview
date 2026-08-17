package runtimefactory

import (
	"slices"
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
			"model:orders": {
				Entities:    map[string]semanticmodel.ModelEntitySpec{"order_id": {Type: "primary", Fields: []string{"order_id"}}},
				GrainEntity: "order_id",
				Sources:     []string{"orders"},
			},
		},
	}}

	first := runtime.ProjectManifest()
	model := first.Models["model:orders"]
	model.GrainEntity = "changed"
	model.Entities["order_id"] = semanticmodel.ModelEntitySpec{Type: "unique", Fields: []string{"changed"}}
	model.Sources[0] = "changed"
	first.Models["model:orders"] = model

	second := runtime.ProjectManifest()
	model = second.Models["model:orders"]
	if model.GrainEntity != "order_id" || !slices.Equal(model.GrainFields(), []string{"order_id"}) || model.Sources[0] != "orders" {
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
