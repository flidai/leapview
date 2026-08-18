package analyticsruntime

import (
	"context"
	"testing"

	analyticscontract "github.com/flidai/leapview/internal/analytics/runtime"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestRequiredProjectExtensionsIncludesSpatialForTiledMaps(t *testing.T) {
	if got := requiredProjectExtensions((*dashboardruntime.ProjectDefinition)(nil)); got != nil {
		t.Fatalf("required extensions = %#v, want nil for missing project", got)
	}
}

func TestSkipInitialRefreshPropagatesToProjectRequest(t *testing.T) {
	projectID, err := projectgraph.NewResourceID("project-1")
	if err != nil {
		t.Fatal(err)
	}
	definition, err := dashboardruntime.NewProjectDefinition(projectID, "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var request analyticscontract.ProjectRequest
	factory := NewFactory(Options{
		Projects: analyticscontract.ProjectFactoryFunc(func(_ context.Context, got analyticscontract.ProjectRequest) (analyticscontract.Project, error) {
			request = got
			return nil, nil
		}),
		ProjectID: projectID, CandidateID: "candidate-1", AuthorizationFingerprint: "auth-1", BindingFingerprint: "binding-1", SkipInitialRefresh: true,
	})
	if _, err := factory.OpenDashboardProjectDataRuntimes(context.Background(), dashboardruntime.ProjectDataRuntimeConfig{Definition: definition}); err != nil {
		t.Fatal(err)
	}
	if !request.SkipInitialRefresh {
		t.Fatal("project request did not preserve SkipInitialRefresh")
	}
	if request.CandidateID != "candidate-1" || request.AuthorizationFingerprint != "auth-1" || request.BindingFingerprint != "binding-1" {
		t.Fatalf("project request lost candidate cache/auth identity: %#v", request)
	}
}
