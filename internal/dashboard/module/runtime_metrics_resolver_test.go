package module

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func TestRuntimeMetricsPlannerAdaptsConsumerPlannerPort(t *testing.T) {
	planner := &semanticquery.Planner{}
	runtime := &resolverTestRuntime{planner: planner}
	provider := &resolverTestProvider{runtime: runtime, stateID: "state-1"}
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{Provider: provider, ProjectID: "project_1"})

	got, ok := metrics.(runtimeMetrics).Planner("sales_model")
	if !ok || got != planner {
		t.Fatalf("planner = %p, ok=%v, want %p", got, ok, planner)
	}
	if provider.acquires != 1 || provider.lease == nil || provider.lease.releases != 1 {
		t.Fatalf("lease counts acquire=%d release=%d", provider.acquires, provider.lease.releases)
	}
}

func TestRuntimeMetricsResolverPublishedSuccessUsesOneLease(t *testing.T) {
	compiled := moduleCompiledRevision(t, "project_1", "published", "state-1")
	provider := &resolverTestProvider{runtime: &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}, stateID: "state-1"}
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{
		Provider: provider, ProjectID: "project_1", PublishedCompilationReader: moduleCompilationReader{compiled: compiled},
	})

	resolved, err := metrics.(interface {
		Resolver() dashboardresolver.Resolver
	}).Resolver().Resolve(projectgraph.ResourceID("published"))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Source.Kind != dashboardresolver.SourceInstance || resolved.Source.AuthoredRevision.ID != "revision-1" {
		t.Fatalf("source = %#v", resolved.Source)
	}
	if provider.acquires != 1 || provider.lease == nil || provider.lease.releases != 1 {
		t.Fatalf("lease counts acquire=%d release=%d", provider.acquires, provider.lease.releases)
	}
}

func TestRuntimeMetricsUnboundStartupUsesLeaseProjectIdentity(t *testing.T) {
	provider := &resolverTestProvider{runtime: &resolverTestRuntime{}, stateID: "state-1"}
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{Provider: provider})
	runtime := metrics.(runtimeMetrics)
	lease, err := provider.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	identity, err := runtime.identityForLease(lease)
	lease.Release()
	if err != nil {
		t.Fatal(err)
	}
	if identity.ProjectID != "project_1" || identity.GenerationID != "state-1" {
		t.Fatalf("identity = %#v, want lease-bound project/state-1", identity)
	}
}

func TestRuntimeMetricsResolverStalePublishedDoesNotFallbackToProject(t *testing.T) {
	compiled := moduleCompiledRevision(t, "project_1", "same", "old-state")
	provider := &resolverTestProvider{runtime: &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}, stateID: "state-1"}
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{
		Provider: provider, ProjectID: "project_1", PublishedCompilationReader: moduleCompilationReader{compiled: compiled},
	})

	_, err := metrics.(interface {
		Resolver() dashboardresolver.Resolver
	}).Resolver().Resolve(projectgraph.ResourceID("same"))
	if !errors.Is(err, dashboardresolver.ErrStaleSemanticState) {
		t.Fatalf("error = %v, want ErrStaleSemanticState", err)
	}
	if provider.lease == nil || provider.lease.releases != 1 {
		t.Fatalf("lease releases = %d, want 1", provider.lease.releases)
	}
}

func TestRuntimeMetricsResolverSameIDCollisionIsAmbiguous(t *testing.T) {
	compiled := moduleCompiledRevision(t, "project_1", "same", "state-1")
	provider := &resolverTestProvider{runtime: &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}, stateID: "state-1"}
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{
		Provider: provider, ProjectID: "project_1", PublishedCompilationReader: moduleCompilationReader{compiled: compiled},
	})
	_, err := metrics.(interface {
		Resolver() dashboardresolver.Resolver
	}).Resolver().Resolve(projectgraph.ResourceID("same"))
	if !errors.Is(err, dashboardresolver.ErrAmbiguous) {
		t.Fatalf("error = %v, want ErrAmbiguous", err)
	}
}

func TestRuntimeMetricsResolverPinsRuntimeProviderAcrossResolve(t *testing.T) {
	runtime := &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}
	provider := &resolverTestProvider{runtime: runtime, stateID: "state-1"}
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{Provider: provider, ProjectID: "project_1"})
	resolver := metrics.(interface {
		Resolver() dashboardresolver.Resolver
	}).Resolver()
	resolved, err := resolver.Resolve(projectgraph.ResourceID("project"))
	if err != nil {
		t.Fatal(err)
	}
	if provider.acquires != 1 || provider.lease.releases != 1 || runtime.resolveCalls != 1 || runtime.modelCalls != 0 {
		t.Fatalf("provider/runtime calls acquire=%d release=%d resolve=%d model=%d", provider.acquires, provider.lease.releases, runtime.resolveCalls, runtime.modelCalls)
	}
	if resolved.Source.Identity.GenerationID != "state-1" {
		t.Fatalf("source = %#v", resolved.Source)
	}
}

func TestRuntimeMetricsPublishedQueryExecutesExactCompiledDefinitionOnOneLease(t *testing.T) {
	compiled := moduleCompiledRevision(t, "project_1", "published", "state-1")
	runtime := &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}
	provider := &resolverTestProvider{runtime: runtime, stateID: "state-1"}
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{
		Provider: provider, ProjectID: "project_1", PublishedCompilationReader: moduleCompilationReader{compiled: compiled},
	})
	if _, err := metrics.QueryDashboardPage(context.Background(), "published", "overview", dashboard.Filters{}); err != nil {
		t.Fatal(err)
	}
	if provider.acquires != 1 || provider.lease.releases != 1 {
		t.Fatalf("lease counts acquire=%d release=%d", provider.acquires, provider.lease.releases)
	}
	if runtime.definitionPageCalls != 1 || runtime.nativePageCalls != 0 || runtime.lastDefinition.ID != "published" {
		t.Fatalf("definition calls=%d native calls=%d definition=%#v", runtime.definitionPageCalls, runtime.nativePageCalls, runtime.lastDefinition)
	}
}

func TestRuntimeMetricsPublishedVisualizationExecutesExactCompiledDefinition(t *testing.T) {
	compiled := moduleCompiledRevision(t, "project_1", "published", "state-1")
	runtime := &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}
	provider := &resolverTestProvider{runtime: runtime, stateID: "state-1"}
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{
		Provider: provider, ProjectID: "project_1", PublishedCompilationReader: moduleCompilationReader{compiled: compiled},
	})
	if _, err := metrics.QueryVisualization(context.Background(), "published", "overview", dashboard.Filters{}, "visual"); err != nil {
		t.Fatal(err)
	}
	if provider.acquires != 1 || provider.lease.releases != 1 {
		t.Fatalf("lease counts acquire=%d release=%d", provider.acquires, provider.lease.releases)
	}
	if runtime.definitionVisualizationCalls != 1 || runtime.nativeVisualizationCalls != 0 || runtime.lastDefinition.ID != "published" {
		t.Fatalf("definition calls=%d native calls=%d definition=%#v", runtime.definitionVisualizationCalls, runtime.nativeVisualizationCalls, runtime.lastDefinition)
	}
}

func TestRuntimeMetricsProjectQueryKeepsNativeRuntimePath(t *testing.T) {
	runtime := &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}
	provider := &resolverTestProvider{runtime: runtime, stateID: "state-1"}
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{Provider: provider, ProjectID: "project_1"})
	if _, err := metrics.QueryDashboardPage(context.Background(), "project", "overview", dashboard.Filters{}); err != nil {
		t.Fatal(err)
	}
	if runtime.nativePageCalls != 1 || runtime.definitionPageCalls != 0 {
		t.Fatalf("native calls=%d definition calls=%d", runtime.nativePageCalls, runtime.definitionPageCalls)
	}
}

func TestRuntimeMetricsPublishedStaleAndCollisionNeverExecute(t *testing.T) {
	for _, test := range []struct {
		name    string
		stateID string
		dashID  string
		wantErr string
	}{
		{name: "stale", stateID: "old-state", dashID: "published", wantErr: "stale"},
		{name: "collision", stateID: "state-1", dashID: "same", wantErr: "ambiguous"},
	} {
		t.Run(test.name, func(t *testing.T) {
			compiled := moduleCompiledRevision(t, "project_1", test.dashID, test.stateID)
			runtime := &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}
			provider := &resolverTestProvider{runtime: runtime, stateID: "state-1"}
			metrics := NewRuntimeMetrics(RuntimeMetricsOptions{
				Provider: provider, ProjectID: "project_1", PublishedCompilationReader: moduleCompilationReader{compiled: compiled},
			})
			patch, err := metrics.QueryDashboardPage(context.Background(), test.dashID, "overview", dashboard.Filters{})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(patch.Status.Error, test.wantErr) {
				t.Fatalf("patch error=%q, want %q", patch.Status.Error, test.wantErr)
			}
			if runtime.definitionPageCalls != 0 || runtime.nativePageCalls != 0 {
				t.Fatalf("query executed after resolver failure: definition=%d native=%d", runtime.definitionPageCalls, runtime.nativePageCalls)
			}
		})
	}
}

func TestRuntimeMetricsRefreshPinsPublishedDefinitionAndLease(t *testing.T) {
	first := moduleCompiledRevision(t, "project_1", "published", "state-1")
	second := first
	second.Definition.Title = "Changed"
	reader := &countingModuleCompilationReader{compiled: first}
	runtime := &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}
	provider := &resolverTestProvider{runtime: runtime, stateID: "state-1"}
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{Provider: provider, ProjectID: "project_1", PublishedCompilationReader: reader})
	err := metrics.(interface {
		WithDashboardRefreshLease(context.Context, func(context.Context) error) error
	}).WithDashboardRefreshLease(context.Background(), func(ctx context.Context) error {
		if _, err := metrics.QueryDashboardPage(ctx, "published", "overview", dashboard.Filters{}); err != nil {
			return err
		}
		reader.compiled = second
		_, err := metrics.QueryDashboardPage(ctx, "published", "overview", dashboard.Filters{})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.acquires != 1 || provider.lease.releases != 1 || reader.calls != 1 {
		t.Fatalf("acquire=%d release=%d reader calls=%d", provider.acquires, provider.lease.releases, reader.calls)
	}
	if runtime.definitionPageCalls != 2 || runtime.lastDefinition.Title != first.Definition.Title {
		t.Fatalf("definition calls=%d last=%#v", runtime.definitionPageCalls, runtime.lastDefinition)
	}
}

type resolverTestProvider struct {
	runtime  runtimehost.Runtime
	stateID  string
	acquires int
	lease    *resolverTestLease
}

func (p *resolverTestProvider) Acquire(context.Context) (runtimehost.Lease, error) {
	p.acquires++
	p.lease = &resolverTestLease{runtime: p.runtime, stateID: servingstate.ID(p.stateID)}
	return p.lease, nil
}

type resolverTestLease struct {
	runtime  runtimehost.Runtime
	stateID  servingstate.ID
	releases int
}

func (l *resolverTestLease) Runtime() runtimehost.Runtime { return l.runtime }
func (l *resolverTestLease) Identity() projectgraph.ServingIdentity {
	return projectgraph.ServingIdentity{ProjectID: "project_1", Environment: "dev", GenerationID: string(l.stateID)}
}
func (l *resolverTestLease) ServingStateID() servingstate.ID { return l.stateID }
func (l *resolverTestLease) DuckLakeSnapshotID() int64       { return 0 }
func (l *resolverTestLease) Release()                        { l.releases++ }

type resolverTestRuntime struct {
	model                        *semanticmodel.Model
	planner                      consumer.Planner
	resolveCalls                 int
	modelCalls                   int
	nativePageCalls              int
	definitionPageCalls          int
	nativeVisualizationCalls     int
	definitionVisualizationCalls int
	definitionConsumerCalls      int
	lastDefinition               dashboarddefinition.Definition
}

func (r *resolverTestRuntime) Close() error                         { return nil }
func (r *resolverTestRuntime) Resolver() dashboardresolver.Resolver { return r }
func (r *resolverTestRuntime) Resolve(id projectgraph.ResourceID) (dashboardresolver.Resolved, error) {
	r.resolveCalls++
	if id != "project" && id != "same" {
		return dashboardresolver.Resolved{}, dashboardresolver.ErrNotFound
	}
	return dashboardresolver.Resolved{
		Definition:      dashboarddefinition.Definition{ID: id.String(), SemanticModel: "sales_model"},
		Model:           r.model,
		SemanticModelID: projectgraph.ResourceID("sales_model"),
		Source: dashboardresolver.SourceMetadata{Identity: projectgraph.ServingIdentity{
			ProjectID: "project_1", Environment: "dev", GenerationID: "state-1",
		}},
	}, nil
}
func (r *resolverTestRuntime) SemanticModel(string) (*semanticmodel.Model, bool) {
	r.modelCalls++
	return r.model, r.model != nil
}
func (r *resolverTestRuntime) SemanticModelByID(projectgraph.ResourceID) (*semanticmodel.Model, bool) {
	r.modelCalls++
	return r.model, r.model != nil
}
func (r *resolverTestRuntime) DefaultFilters(string) dashboard.Filters { return dashboard.Filters{} }
func (r *resolverTestRuntime) Planner(string) (consumer.Planner, bool) {
	return r.planner, r.planner != nil
}

func (r *resolverTestRuntime) QueryDashboardPage(context.Context, string, string, dashboard.Filters) (dashboard.Patch, error) {
	r.nativePageCalls++
	return dashboard.Patch{}, nil
}
func (r *resolverTestRuntime) QueryDashboardPageForDefinition(_ context.Context, definition dashboarddefinition.Definition, _ string, _ dashboard.Filters) (dashboard.Patch, error) {
	r.definitionPageCalls++
	r.lastDefinition = definition
	return dashboard.Patch{}, nil
}
func (r *resolverTestRuntime) QueryVisualization(context.Context, string, string, dashboard.Filters, string) (visualizationir.VisualizationEnvelope, error) {
	r.nativeVisualizationCalls++
	return visualizationir.VisualizationEnvelope{}, nil
}
func (r *resolverTestRuntime) QueryVisualizationWindow(context.Context, string, string, dashboard.Filters, visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	return visualizationir.VisualizationEnvelope{}, nil
}
func (r *resolverTestRuntime) QueryVisualizationForDefinition(_ context.Context, definition dashboarddefinition.Definition, _ string, _ dashboard.Filters, _ string) (visualizationir.VisualizationEnvelope, error) {
	r.definitionVisualizationCalls++
	r.lastDefinition = definition
	return visualizationir.VisualizationEnvelope{}, nil
}
func (r *resolverTestRuntime) QueryVisualizationWindowForDefinition(_ context.Context, definition dashboarddefinition.Definition, _ string, _ dashboard.Filters, _ visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	r.lastDefinition = definition
	return visualizationir.VisualizationEnvelope{}, nil
}
func (r *resolverTestRuntime) NormalizeVisualizationWindowForDefinition(definition dashboarddefinition.Definition, request dashboard.TableRequest) dashboard.TableRequest {
	r.lastDefinition = definition
	return request
}
func (r *resolverTestRuntime) DefaultFiltersForDefinition(definition dashboarddefinition.Definition) dashboard.Filters {
	r.lastDefinition = definition
	return definition.DefaultFilters()
}
func (r *resolverTestRuntime) PagesForDefinition(definition dashboarddefinition.Definition) []dashboard.Page {
	r.lastDefinition = definition
	return definition.Pages
}
func (r *resolverTestRuntime) ModelIDForDashboardDefinition(definition dashboarddefinition.Definition) string {
	r.lastDefinition = definition
	return definition.SemanticModel
}
func (r *resolverTestRuntime) QueryCompiledFilterOptionsForDefinition(_ context.Context, definition dashboarddefinition.Definition, _ dashboardfilter.OptionQuery) (dashboardfilter.OptionResult, error) {
	r.lastDefinition = definition
	return dashboardfilter.OptionResult{}, nil
}
func (r *resolverTestRuntime) ExecuteConsumersPageForDefinition(_ context.Context, definition dashboarddefinition.Definition, _ consumer.Request, _ consumer.Publisher) error {
	r.definitionConsumerCalls++
	r.lastDefinition = definition
	return nil
}

type moduleCompilationReader struct{ compiled authoring.CompiledRevision }

func (r moduleCompilationReader) GetPublishedCompilation(context.Context, projectgraph.ResourceID, authoring.DashboardID) (authoring.CompiledRevision, error) {
	if r.compiled.Definition.ID == "" {
		return authoring.CompiledRevision{}, authoring.ErrNotFound
	}
	return r.compiled, nil
}

type countingModuleCompilationReader struct {
	compiled authoring.CompiledRevision
	calls    int
}

func (r *countingModuleCompilationReader) GetPublishedCompilation(context.Context, projectgraph.ResourceID, authoring.DashboardID) (authoring.CompiledRevision, error) {
	r.calls++
	return r.compiled, nil
}

func moduleCompiledRevision(t *testing.T, projectID, dashboardID, stateID string) authoring.CompiledRevision {
	t.Helper()
	definition, err := dashboarddefinition.New(dashboardID, "Sales", "", "sales_model", []dashboard.Page{{ID: "overview", Title: "Overview"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(projectID), "dev", stateID)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := authoring.NewCompiledRevision(projectgraph.ResourceID(projectID), authoring.DashboardID(dashboardID), authoring.RevisionToken{RevisionID: "revision-1", Number: 1, ContentHash: "sha256:" + strings.Repeat("b", 64)}, definition, identity, time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}
