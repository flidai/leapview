package compiler

import (
	"strings"
	"testing"

	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestCompiledVisualCalculationAddsGovernedFieldAndVisibleBinding(t *testing.T) {
	t.Parallel()

	report := &dashboardauthoring.Dashboard{
		ID: "sales", SemanticModel: "sales",
		Visuals: map[string]dashboardauthoring.AuthoringVisualization{
			"revenue": dashboardauthoring.ChartVisualization(dashboardauthoring.Visual{
				Type: "line",
				Query: dashboardauthoring.VisualQuery{
					Dimensions: []dashboardauthoring.FieldRef{{Field: "orders.month", Alias: "month"}},
					Metrics:    []dashboardauthoring.FieldRef{{Field: "revenue", Alias: "revenue"}},
					Sort:       []dashboardauthoring.Sort{{Field: "orders.month", Direction: "asc"}},
				},
				Calculations: []dashboardauthoring.VisualCalculation{{
					ID: "running_revenue", Label: "Running revenue", Template: "running_total", Source: "value",
					OrderBy: []dashboardauthoring.VisualCalculationOrder{{Field: "label", Direction: "asc"}}, Format: "currency",
				}},
			}),
		},
	}
	definitions, err := CompileVisualizationDefinitions(report)
	if err != nil {
		t.Fatalf("CompileVisualizationDefinitions(): %v", err)
	}
	chart := definitions["revenue"].Spec.Value.(*visualizationir.CartesianVisualizationSpec)
	if chart.Calculations == nil || len(*chart.Calculations) != 1 {
		t.Fatalf("calculations = %#v, want one compiled calculation", chart.Calculations)
	}
	calculation := (*chart.Calculations)[0]
	if calculation.Template != visualizationir.VisualizationCalculationTemplateRunningTotal ||
		calculation.Source.Field != "value" || calculation.OrderBy[0].Field.Field != "label" {
		t.Fatalf("compiled calculation = %#v", calculation)
	}
	fields := chart.Datasets[0].Fields
	calculated := fields[len(fields)-1]
	if calculated.ID != "running_revenue" || calculated.Provenance == nil ||
		calculated.Provenance.Kind != visualizationir.VisualizationFieldProvenanceKindVisualCalculation ||
		calculated.Provenance.CalculationID == nil || *calculated.Provenance.CalculationID != "running_revenue" {
		t.Fatalf("calculated field = %#v", calculated)
	}
	if chart.Y[len(chart.Y)-1].Field != "running_revenue" {
		t.Fatalf("y bindings = %#v, want visible calculation", chart.Y)
	}
}

func TestCompiledVisualCalculationRejectsInvalidPlans(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		calculations []dashboardauthoring.VisualCalculation
		want         string
	}{
		"unsupported template": {
			calculations: []dashboardauthoring.VisualCalculation{{ID: "bad", Template: "arbitrary_formula", Source: "value", OrderBy: []dashboardauthoring.VisualCalculationOrder{{Field: "label"}}}},
			want:         "unsupported template",
		},
		"cycle": {
			calculations: []dashboardauthoring.VisualCalculation{
				{ID: "a", Template: "running_total", Source: "b", OrderBy: []dashboardauthoring.VisualCalculationOrder{{Field: "label"}}},
				{ID: "b", Template: "running_total", Source: "a", OrderBy: []dashboardauthoring.VisualCalculationOrder{{Field: "label"}}},
			},
			want: "cycle",
		},
		"ambiguous order": {
			calculations: []dashboardauthoring.VisualCalculation{{ID: "running", Template: "running_total", Source: "value"}},
			want:         "order_by",
		},
		"invalid axis": {
			calculations: []dashboardauthoring.VisualCalculation{{ID: "running", Template: "running_total", Source: "value", Axis: "pages", OrderBy: []dashboardauthoring.VisualCalculationOrder{{Field: "label"}}}},
			want:         "axis",
		},
	}
	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			report := &dashboardauthoring.Dashboard{
				ID: "sales", SemanticModel: "sales",
				Visuals: map[string]dashboardauthoring.AuthoringVisualization{
					"revenue": dashboardauthoring.ChartVisualization(dashboardauthoring.Visual{
						Type: "line",
						Query: dashboardauthoring.VisualQuery{
							Dimensions: []dashboardauthoring.FieldRef{{Field: "orders.month", Alias: "month"}},
							Metrics:    []dashboardauthoring.FieldRef{{Field: "revenue", Alias: "revenue"}},
						},
						Calculations: test.calculations,
					}),
				},
			}
			_, err := CompileVisualizationDefinitions(report)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCompiledHiddenTableCalculationRemainsAvailableWithoutDisplayColumn(t *testing.T) {
	t.Parallel()

	report := &dashboardauthoring.Dashboard{
		ID: "sales", SemanticModel: "sales",
		Visuals: map[string]dashboardauthoring.AuthoringVisualization{
			"orders": dashboardauthoring.TabularVisualization("table", dashboardauthoring.TableVisual{
				Query:   dashboardauthoring.TableQuery{Table: "orders", Fields: []string{"month", "revenue"}},
				Columns: nil,
				Calculations: []dashboardauthoring.VisualCalculation{{
					ID: "running_revenue", Template: "running_total", Source: "revenue", Hidden: true,
					OrderBy: []dashboardauthoring.VisualCalculationOrder{{Field: "month", Direction: "asc"}},
				}},
			}),
		},
	}
	definitions, err := CompileVisualizationDefinitions(report)
	if err != nil {
		t.Fatalf("CompileVisualizationDefinitions(): %v", err)
	}
	table := definitions["orders"].Spec.Value.(*visualizationir.TableVisualizationSpec)
	if len(table.Datasets[0].Fields) != len(table.Columns)+1 {
		t.Fatalf("fields = %d, columns = %d, want one hidden support field", len(table.Datasets[0].Fields), len(table.Columns))
	}
	for _, column := range table.Columns {
		if column.Field.Field == "running_revenue" {
			t.Fatalf("hidden calculation leaked into columns: %#v", table.Columns)
		}
	}
}
