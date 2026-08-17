package runtimefactory

import (
	"testing"

	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

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
