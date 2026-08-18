package analyticsruntime

import (
	"context"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
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

func TestProjectRuntimeAdaptsActivatedConcretePlanner(t *testing.T) {
	planner := &semanticquery.Planner{}
	runtime := projectRuntime{modelID: "sales", runtime: concretePlannerProjectStub{planner: planner}}
	got := runtime.Planner()
	if got != planner {
		t.Fatalf("planner = %p, want activated planner %p", got, planner)
	}
}

// concretePlannerProjectStub models the analytics-owned activation runtime.
// Its concrete planner method is intentionally the only source-side shape;
// projectRuntime is the dashboard boundary that converts it to consumer.Planner.
type concretePlannerProjectStub struct {
	planner *semanticquery.Planner
}

func (concretePlannerProjectStub) ExecuteDataQuery(context.Context, dataquery.Query) (dataquery.Result, error) {
	return dataquery.Result{}, nil
}
func (concretePlannerProjectStub) ExecuteDataQueryArrow(context.Context, dataquery.Query, arrowquery.Sink) (dataquery.Result, error) {
	return dataquery.Result{}, nil
}
func (concretePlannerProjectStub) ExecuteDataQueryBundle(context.Context, []dataquery.BundleRequest) (dataquery.BundleResult, error) {
	return dataquery.BundleResult{}, nil
}
func (concretePlannerProjectStub) Refresh(context.Context) error { return nil }
func (concretePlannerProjectStub) RefreshModelTables(context.Context, string, []string) error {
	return nil
}
func (concretePlannerProjectStub) Close() error              { return nil }
func (concretePlannerProjectStub) LastRefresh() time.Time    { return time.Time{} }
func (concretePlannerProjectStub) DuckLakeSnapshotID() int64 { return 0 }
func (concretePlannerProjectStub) ReadConcurrency() int      { return 1 }
func (s concretePlannerProjectStub) Planner(string) (*semanticquery.Planner, bool) {
	return s.planner, s.planner != nil
}

var _ analyticscontract.Project = concretePlannerProjectStub{}
var _ interface {
	Planner(string) (*semanticquery.Planner, bool)
} = concretePlannerProjectStub{}
