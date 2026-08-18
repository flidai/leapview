package http

import (
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestDashboardVisualAgentProjectionUsesCanonicalSpecKind(t *testing.T) {
	base := visualizationir.VisualizationSpecBase{
		Title: "Revenue",
		Datasets: []visualizationir.VisualizationDatasetSchema{{
			ID: "primary",
			Fields: []visualizationir.VisualizationField{{
				ID: "value", Label: "Revenue", Role: visualizationir.VisualizationFieldRoleMetric,
				DataType: visualizationir.VisualizationDataTypeDecimal,
			}},
		}},
	}
	envelope := visualizationir.VisualizationEnvelope{
		VisualID: "revenue_kpi",
		Spec: visualizationir.VisualizationSpec{Value: &visualizationir.KPIVisualizationSpec{
			VisualizationSpecBase: base,
			Kind:                  "kpi",
			Value:                 visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "value"},
		}},
		DataState: visualizationir.VisualizationDataState{Value: &visualizationir.InlineVisualizationDataState{
			Kind: "inline",
			Datasets: []visualizationir.VisualizationInlineDataset{{
				ID: "primary", Columns: []string{"value"}, Rows: [][]any{{16008872.12}},
			}},
		}},
	}
	request := httptest.NewRequest(nethttp.MethodGet, "/dashboards/dash/visuals/revenue_kpi/query", nil)

	result, err := (Handler{ProjectID: "workspace"}).dashboardVisualAgentProjection(
		request, fakeMetrics{}, envelope, dashboard.Filters{}, 0, maxAgentDashboardVisualRows, "scope", "snapshot",
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Type != "kpi" {
		t.Fatalf("type = %q, want kpi", result.Type)
	}
}

func TestProjectDashboardAppliedFiltersIncludesTypedFilterState(t *testing.T) {
	state := dashboardfilter.State{AppliedControls: map[string]dashboardfilter.AppliedState{
		"fb_state": {
			Expression: dashboardfilter.Expression{
				Kind: dashboardfilter.ExpressionSet, Operator: dashboardfilter.OperatorIn,
				Values: []dashboardfilter.Value{{Kind: dashboardfilter.ValueString, Value: "WA"}},
			},
		},
	}}

	projected := projectDashboardAppliedFilters(dashboard.Filters{CompiledState: &state})
	control, ok := projected.Controls["fb_state"]
	if !ok || control.Type != "set" || control.Operator == nil || *control.Operator != "in" ||
		control.Values == nil || len(*control.Values) != 1 || (*control.Values)[0] != "WA" {
		t.Fatalf("projected controls = %#v", projected.Controls)
	}
}
