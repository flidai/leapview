package analyticsruntime

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/catalogstats"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
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
	evidence, err := resultidentity.NewEvidence(resultidentity.EvidenceInput{
		SemanticModelID: "semantic:sales", SemanticModelDigest: analyticsRuntimeTestDigest('a'),
		DatasetRelations:   []resultidentity.DatasetRelation{{Dataset: "orders", Relation: resultidentity.RelationRevision{RelationID: "model:orders", RevisionDigest: analyticsRuntimeTestDigest('b')}}},
		BindingFingerprint: analyticsRuntimeTestDigest('c'), RuntimeDigest: analyticsRuntimeTestDigest('d'), CapabilityDigest: analyticsRuntimeTestDigest('e'),
	})
	if err != nil {
		t.Fatal(err)
	}
	factory := NewFactory(Options{
		Projects: analyticscontract.ProjectFactoryFunc(func(_ context.Context, got analyticscontract.ProjectRequest) (analyticscontract.Project, error) {
			request = got
			return nil, nil
		}),
		ProjectID: projectID, TargetID: "target-1", SnapshotSealID: "seal-1", CandidateID: "candidate-1", AuthorizationFingerprint: "auth-1", BindingFingerprint: "binding-1", RelationNamespace: "_candidate_namespace", SkipInitialRefresh: true,
		DependencyEvidence: map[string]resultidentity.Evidence{"semantic:sales": evidence},
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
	if request.TargetID != "target-1" || request.SnapshotSealID != "seal-1" {
		t.Fatalf("project request lost target/seal provenance: %#v", request)
	}
	if request.RelationNamespace != "_candidate_namespace" {
		t.Fatalf("project request lost relation namespace: %q", request.RelationNamespace)
	}
	if !request.DependencyEvidence["semantic:sales"].Available() {
		t.Fatal("project request lost dependency evidence")
	}
}

func TestDashboardRuntimeFactoryRequiresCacheAdmissionProvenance(t *testing.T) {
	projectID, err := projectgraph.NewResourceID("project-1")
	if err != nil {
		t.Fatal(err)
	}
	definition, err := dashboardruntime.NewProjectDefinition(projectID, "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	projects := analyticscontract.ProjectFactoryFunc(func(context.Context, analyticscontract.ProjectRequest) (analyticscontract.Project, error) {
		called = true
		return nil, nil
	})
	for name, options := range map[string]Options{
		"missing target":      {Projects: projects, SnapshotSealID: "seal-1"},
		"noncanonical target": {Projects: projects, TargetID: " target-1", SnapshotSealID: "seal-1"},
		"control target":      {Projects: projects, TargetID: "target\x00one", SnapshotSealID: "seal-1"},
		"missing seal":        {Projects: projects, TargetID: "target-1"},
		"noncanonical seal":   {Projects: projects, TargetID: "target-1", SnapshotSealID: " seal-1"},
		"control seal":        {Projects: projects, TargetID: "target-1", SnapshotSealID: "seal\none"},
	} {
		t.Run(name, func(t *testing.T) {
			called = false
			_, err := NewFactory(options).OpenDashboardProjectDataRuntimes(t.Context(), dashboardruntime.ProjectDataRuntimeConfig{Definition: definition})
			if err == nil {
				t.Fatal("dashboard runtime factory accepted incomplete cache-admission provenance")
			}
			if called {
				t.Fatal("analytical project factory was called before cache-admission validation")
			}
		})
	}
}

func analyticsRuntimeTestDigest(value byte) string {
	return "sha256:" + strings.Repeat(string(value), 64)
}

func TestProjectRuntimeAdaptsActivatedConcretePlanner(t *testing.T) {
	planner := &semanticquery.Planner{}
	runtime := projectRuntime{modelID: "sales", runtime: concretePlannerProjectStub{planner: planner}}
	got := runtime.Planner()
	if got != planner {
		t.Fatalf("planner = %p, want activated planner %p", got, planner)
	}
}

func TestProjectRuntimeForwardsCatalogStatistics(t *testing.T) {
	want := []catalogstats.Table{{Schema: "model", Name: "orders", RowCount: 42}}
	runtime := projectRuntime{modelID: "sales", runtime: concretePlannerProjectStub{statistics: want}}
	got, err := runtime.CatalogTableStatistics(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CatalogTableStatistics() = %#v, want %#v", got, want)
	}
}

// concretePlannerProjectStub models the analytics-owned activation runtime.
// Its concrete planner method is intentionally the only source-side shape;
// projectRuntime is the dashboard boundary that converts it to consumer.Planner.
type concretePlannerProjectStub struct {
	planner    *semanticquery.Planner
	statistics []catalogstats.Table
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
func (s concretePlannerProjectStub) CatalogTableStatistics(context.Context) ([]catalogstats.Table, error) {
	return s.statistics, nil
}

var _ analyticscontract.Project = concretePlannerProjectStub{}
var _ interface {
	Planner(string) (*semanticquery.Planner, bool)
} = concretePlannerProjectStub{}
