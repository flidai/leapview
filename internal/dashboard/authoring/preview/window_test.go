package preview

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func (r *previewRuntime) QueryVisualizationWindowForDefinition(ctx context.Context, _ dashboarddefinition.Definition, _ string, _ dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	governor, ok := dataquery.GovernorFromContext(ctx)
	if !ok {
		return visualizationir.VisualizationEnvelope{}, errors.New("preview query governor was not propagated")
	}
	r.governorSeen = true
	if _, _, err := governor.GovernDataQuery(ctx, dataquery.Query{}); err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	return visualizationir.VisualizationEnvelope{
		VisualID: request.VisualID,
		DataState: visualizationir.VisualizationDataState{Value: &visualizationir.WindowedVisualizationDataState{
			ResetVersion: 9,
			Blocks:       map[string]visualizationir.VisualizationWindowBlock{"a": {ID: "a", ResetVersion: 9}},
		}},
	}, nil
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
