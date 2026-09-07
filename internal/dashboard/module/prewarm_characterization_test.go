package module

import (
	"context"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/publication"
)

// This is a readiness gate, not a coordinator test. Default preparation must
// not resolve a different generation from the enclosing refresh lease.
func TestPrewarmPreparationResolverUsesRefreshLeaseAcrossCutover(t *testing.T) {
	first := moduleCompiledRevision(t, "project_1", "published", "state-1")
	second := moduleCompiledRevision(t, "project_1", "published", "state-2")
	second.Definition.Title = "Generation two"
	second, err := authoring.NewCompiledRevision(second.ProjectID, second.DashboardID, second.AuthoredRevision, second.Definition, second.SemanticIdentity, second.CompiledAt)
	if err != nil {
		t.Fatal(err)
	}
	reader := &countingModuleCompilationReader{compiled: first}
	oldRuntime := &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}
	newRuntime := &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}
	provider := &resolverTestProvider{runtime: oldRuntime, stateID: "state-1"}
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{
		Provider: provider, ProjectID: "project_1", PublishedCompilationReader: reader,
	}).(runtimeMetrics)
	var preparedGeneration, executedTitle string
	err = metrics.WithDashboardRefreshLease(context.Background(), func(ctx context.Context) error {
		// Resolve/cache the leased definition, just as a refresh may do before
		// preparing another target. Then publish a successor runtime.
		if _, err := metrics.QueryDashboardPage(ctx, "published", "overview", dashboard.Filters{}); err != nil {
			return err
		}
		provider.runtime, provider.stateID = newRuntime, "state-2"
		reader.compiled = second
		request, _, err := preparePublicDashboardRefresh(ctx, publication.Publication{ProjectID: "project_1", Dashboard: "published", DefaultPage: "overview", Configured: true, ServingStateID: "state-1"})
		if err != nil {
			return err
		}
		preparedGeneration = ctx.Value(dashboardRefreshRuntimeKey{}).(dashboardRefreshRuntime).identity.GenerationID
		if request.PageID != "overview" || request.ModelID != "sales_model" || oldRuntime.lastDefinition.Title != first.Definition.Title {
			t.Fatal("default preparation did not use the leased definition")
		}
		if _, err := metrics.QueryDashboardPage(ctx, "published", "overview", dashboard.Filters{}); err != nil {
			return err
		}
		executedTitle = oldRuntime.lastDefinition.Title
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if preparedGeneration != "state-1" || executedTitle != first.Definition.Title || provider.acquires != 1 {
		t.Fatalf("preparation escaped refresh lease: prepared generation=%q, executed title=%q, acquisitions=%d; want state-1, %q, 1", preparedGeneration, executedTitle, provider.acquires, first.Definition.Title)
	}
	if newRuntime.definitionPageCalls != 0 {
		t.Fatal("refresh executed on successor runtime")
	}
}
