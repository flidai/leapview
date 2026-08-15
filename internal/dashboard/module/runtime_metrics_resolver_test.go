package module

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func TestRuntimeMetricsResolverPublishedSuccessUsesOneLease(t *testing.T) {
	compiled := moduleCompiledRevision(t, "workspace", "published", "state-1")
	provider := &resolverTestProvider{runtime: &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}, stateID: "state-1"}
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{
		Provider: provider, WorkspaceID: "workspace", PublishedCompilationReader: moduleCompilationReader{compiled: compiled},
	})

	resolved, err := metrics.(interface {
		Resolver() dashboardresolver.Resolver
	}).Resolver().Resolve("published")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Source.Kind != dashboardresolver.SourceWorkspace || resolved.Source.AuthoredRevision.ID != "revision-1" {
		t.Fatalf("source = %#v", resolved.Source)
	}
	if provider.acquires != 1 || provider.lease == nil || provider.lease.releases != 1 {
		t.Fatalf("lease counts acquire=%d release=%d", provider.acquires, provider.lease.releases)
	}
}

func TestRuntimeMetricsResolverStalePublishedDoesNotFallbackToProject(t *testing.T) {
	compiled := moduleCompiledRevision(t, "workspace", "same", "old-state")
	provider := &resolverTestProvider{runtime: &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}, stateID: "state-1"}
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{
		Provider: provider, WorkspaceID: "workspace", PublishedCompilationReader: moduleCompilationReader{compiled: compiled},
	})

	_, err := metrics.(interface {
		Resolver() dashboardresolver.Resolver
	}).Resolver().Resolve("same")
	if !errors.Is(err, dashboardresolver.ErrStaleSemanticState) {
		t.Fatalf("error = %v, want ErrStaleSemanticState", err)
	}
	if provider.lease == nil || provider.lease.releases != 1 {
		t.Fatalf("lease releases = %d, want 1", provider.lease.releases)
	}
}

func TestRuntimeMetricsResolverSameIDCollisionIsAmbiguous(t *testing.T) {
	compiled := moduleCompiledRevision(t, "workspace", "same", "state-1")
	provider := &resolverTestProvider{runtime: &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}, stateID: "state-1"}
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{
		Provider: provider, WorkspaceID: "workspace", PublishedCompilationReader: moduleCompilationReader{compiled: compiled},
	})
	_, err := metrics.(interface {
		Resolver() dashboardresolver.Resolver
	}).Resolver().Resolve("same")
	if !errors.Is(err, dashboardresolver.ErrAmbiguous) {
		t.Fatalf("error = %v, want ErrAmbiguous", err)
	}
}

func TestRuntimeMetricsResolverPinsRuntimeProviderAcrossResolve(t *testing.T) {
	runtime := &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}
	provider := &resolverTestProvider{runtime: runtime, stateID: "state-1"}
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{Provider: provider, WorkspaceID: "workspace"})
	resolver := metrics.(interface {
		Resolver() dashboardresolver.Resolver
	}).Resolver()
	resolved, err := resolver.Resolve("project")
	if err != nil {
		t.Fatal(err)
	}
	if provider.acquires != 1 || provider.lease.releases != 1 || runtime.resolveCalls != 1 || runtime.modelCalls != 0 {
		t.Fatalf("provider/runtime calls acquire=%d release=%d resolve=%d model=%d", provider.acquires, provider.lease.releases, runtime.resolveCalls, runtime.modelCalls)
	}
	if resolved.Source.ServingStateID != "state-1" {
		t.Fatalf("source = %#v", resolved.Source)
	}
}

func TestRuntimeMetricsPublishedQueryExecutesExactCompiledDefinitionOnOneLease(t *testing.T) {
	compiled := moduleCompiledRevision(t, "workspace", "published", "state-1")
	runtime := &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}
	provider := &resolverTestProvider{runtime: runtime, stateID: "state-1"}
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{
		Provider: provider, WorkspaceID: "workspace", PublishedCompilationReader: moduleCompilationReader{compiled: compiled},
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
	compiled := moduleCompiledRevision(t, "workspace", "published", "state-1")
	runtime := &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}
	provider := &resolverTestProvider{runtime: runtime, stateID: "state-1"}
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{
		Provider: provider, WorkspaceID: "workspace", PublishedCompilationReader: moduleCompilationReader{compiled: compiled},
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
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{Provider: provider, WorkspaceID: "workspace"})
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
			compiled := moduleCompiledRevision(t, "workspace", test.dashID, test.stateID)
			runtime := &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}
			provider := &resolverTestProvider{runtime: runtime, stateID: "state-1"}
			metrics := NewRuntimeMetrics(RuntimeMetricsOptions{
				Provider: provider, WorkspaceID: "workspace", PublishedCompilationReader: moduleCompilationReader{compiled: compiled},
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
	first := moduleCompiledRevision(t, "workspace", "published", "state-1")
	second := first
	second.Definition.Title = "Changed"
	reader := &countingModuleCompilationReader{compiled: first}
	runtime := &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}
	provider := &resolverTestProvider{runtime: runtime, stateID: "state-1"}
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{Provider: provider, WorkspaceID: "workspace", PublishedCompilationReader: reader})
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

func (l *resolverTestLease) Runtime() runtimehost.Runtime    { return l.runtime }
func (l *resolverTestLease) ServingStateID() servingstate.ID { return l.stateID }
func (l *resolverTestLease) DuckLakeSnapshotID() int64       { return 0 }
func (l *resolverTestLease) Release()                        { l.releases++ }

type resolverTestRuntime struct {
	model                        *semanticmodel.Model
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
func (r *resolverTestRuntime) Resolve(id string) (dashboardresolver.Resolved, error) {
	r.resolveCalls++
	if id != "project" && id != "same" {
		return dashboardresolver.Resolved{}, dashboardresolver.ErrNotFound
	}
	return dashboardresolver.Resolved{Definition: dashboarddefinition.Definition{ID: id, SemanticModel: "sales_model"}, Model: r.model}, nil
}
func (r *resolverTestRuntime) SemanticModel(string) (*semanticmodel.Model, bool) {
	r.modelCalls++
	return r.model, r.model != nil
}
func (r *resolverTestRuntime) DefaultFilters(string) dashboard.Filters { return dashboard.Filters{} }

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

func (r moduleCompilationReader) GetPublishedCompilation(context.Context, string, authoring.DashboardID) (authoring.CompiledRevision, error) {
	if r.compiled.Definition.ID == "" {
		return authoring.CompiledRevision{}, authoring.ErrNotFound
	}
	return r.compiled, nil
}

type countingModuleCompilationReader struct {
	compiled authoring.CompiledRevision
	calls    int
}

func (r *countingModuleCompilationReader) GetPublishedCompilation(context.Context, string, authoring.DashboardID) (authoring.CompiledRevision, error) {
	r.calls++
	return r.compiled, nil
}

func moduleCompiledRevision(t *testing.T, workspace, dashboardID, stateID string) authoring.CompiledRevision {
	t.Helper()
	definition, err := dashboarddefinition.New(dashboardID, "Sales", "", "sales_model", []dashboard.Page{{ID: "overview", Title: "Overview"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := authoring.NewCompiledRevision(workspace, authoring.DashboardID(dashboardID), authoring.RevisionToken{RevisionID: "revision-1", Number: 1, ContentHash: "sha256:" + strings.Repeat("b", 64)}, definition, stateID, time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}
