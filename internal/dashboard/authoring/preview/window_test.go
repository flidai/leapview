package preview

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func (r *previewRuntime) QueryVisualizationWindowForDefinition(_ context.Context, _ dashboarddefinition.Definition, _ string, _ dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	r.windowQueryCalls++
	return visualizationir.VisualizationEnvelope{
		VisualID: request.VisualID,
		DataState: visualizationir.VisualizationDataState{Value: &visualizationir.WindowedVisualizationDataState{
			ResetVersion: 9,
			Blocks:       map[string]visualizationir.VisualizationWindowBlock{"a": {ID: "a", ResetVersion: 9}},
		}},
	}, nil
}

func TestPreviewWindowResolvesFiltersAndQueriesThroughOneCompilation(t *testing.T) {
	f := newPreviewFixture(t)
	f.request.Window = &visualizationir.VisualizationWindowRequest{VisualID: "orders", ResetVersion: 42}
	resolved := false
	result, err := f.service.PreviewWindow(t.Context(), f.request, func(compiled Compilation) (dashboard.Filters, error) {
		resolved = true
		if compiled.Definition.Visualizations["orders"].ID != "orders" {
			t.Fatalf("resolver received incomplete compilation: %#v", compiled.Definition.Visualizations)
		}
		state := dashboardfilter.State{Revision: 7}
		return dashboard.Filters{CompiledState: &state, ActivePageID: "overview"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resolved || f.provider.acquireCalls != 1 || f.provider.lease.releases != 1 || f.runtime.projectionCalls != 1 || f.runtime.windowQueryCalls != 1 {
		t.Fatalf("single-pass window calls: resolved=%t acquire=%d release=%d projection=%d query=%d", resolved, f.provider.acquireCalls, f.provider.lease.releases, f.runtime.projectionCalls, f.runtime.windowQueryCalls)
	}
	if result.PagePatch.Filters.CompiledState == nil || result.PagePatch.Filters.CompiledState.Revision != 7 {
		t.Fatalf("window filters = %#v, want resolved revision 7", result.PagePatch.Filters)
	}
}

func TestPreviewWindowPreservesIndependentResetIdentity(t *testing.T) {
	f := newPreviewFixture(t)
	state := dashboardfilter.State{Revision: 7}
	f.request.Filters = dashboard.Filters{CompiledState: &state}
	f.request.Window = &visualizationir.VisualizationWindowRequest{VisualID: "orders", ResetVersion: 42}
	result, err := f.service.Preview(t.Context(), f.request)
	if err != nil {
		t.Fatal(err)
	}
	envelope, ok := result.PagePatch.Visuals["orders"]
	if !ok {
		t.Fatalf("window patch visuals = %#v", result.PagePatch.Visuals)
	}
	window, ok := envelope.DataState.Value.(*visualizationir.WindowedVisualizationDataState)
	if !ok || window.ResetVersion != 9 || window.Blocks["a"].ResetVersion != 9 {
		t.Fatalf("window reset identity = %#v", envelope.DataState.Value)
	}
}
