package command_test

import (
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	. "github.com/flidai/leapview/internal/dashboard/command"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type canonicalMetrics struct {
	definition dashboarddefinition.Definition
	model      *semanticmodel.Model
}

func (m canonicalMetrics) DefaultFilters(string) dashboard.Filters {
	return dashboard.Filters{}.WithDefaults()
}
func (m canonicalMetrics) NormalizeVisualizationWindow(_ string, request dashboard.TableRequest) dashboard.TableRequest {
	return request.WithDefaults()
}
func (m canonicalMetrics) Resolver() dashboardresolver.Resolver { return canonicalResolver{metrics: m} }

type canonicalResolver struct{ metrics canonicalMetrics }

func (r canonicalResolver) Resolve(_ projectgraph.ResourceID) (dashboardresolver.Resolved, error) {
	return dashboardresolver.Resolved{Definition: r.metrics.definition, Model: r.metrics.model}, nil
}

func canonicalCommandFixture(t *testing.T) canonicalMetrics {
	t.Helper()
	fields := []visualizationir.VisualizationField{{ID: "label", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString, Label: "Label"}, {ID: "value", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Label: "Value"}}
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{VisualizationSpecBase: visualizationir.VisualizationSpecBase{Kind: "cartesian", Title: "Orders", Accessibility: visualizationir.VisualizationAccessibility{Title: "Orders", Description: "Orders"}, Datasets: []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: fields}}, Interactions: []visualizationir.VisualizationInteraction{{ID: "point_selection", Kind: visualizationir.VisualizationInteractionKindSelect, Mode: visualizationir.VisualizationSelectionModeSingle, RequiresStableIdentity: false, Mappings: []visualizationir.VisualizationInteractionMapping{{Source: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "label"}, TargetFieldID: "state"}}}}, DataBudget: visualizationir.VisualizationDataBudget{MaxRows: 100}}, Kind: "cartesian", Mark: visualizationir.VisualizationCartesianMarkBar, X: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "label"}, Y: []visualizationir.VisualizationFieldRef{{Dataset: "primary", Field: "value"}}, Presentation: visualizationir.CartesianVisualizationPresentation{VisualizationPresentation: visualizationir.VisualizationPresentation{Legend: visualizationir.VisualizationLegendPositionHidden, LabelPolicy: visualizationir.VisualizationLabelPolicy{Density: visualizationir.VisualizationLabelDensityHidden, MaxCharacters: 24, TooltipFallback: true}}}}}
	visual, err := visualizationdefinition.New("chart", spec, visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultCategoryValue, ModelID: "model", DatasetID: "primary", Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: "orders", Dimensions: []visualizationdefinition.FieldBinding{{FieldID: "state", Alias: "label"}}, Metrics: []visualizationdefinition.FieldBinding{{FieldID: "order_count", Alias: "value"}}, Limit: 100}})
	if err != nil {
		t.Fatal(err)
	}
	model := &semanticmodel.Model{Name: "model", Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}, Tables: map[string]semanticmodel.Table{"orders": {ModelName: "orders", GrainEntity: "state", Entities: map[string]semanticmodel.ModelEntitySpec{"state": {Type: "primary", Fields: []string{"state"}}}, Dimensions: map[string]semanticmodel.MetricDimension{"state": {Field: "orders.state", Type: "string", Datatype: semanticmodel.DataTypeString}}}}, Dimensions: map[string]semanticmodel.SemanticDimension{"state": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.state"}}}}, Metrics: map[string]semanticmodel.Metric{"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.state"}}}}
	return canonicalMetrics{definition: dashboarddefinition.Definition{ID: "dash", SemanticModel: "model", Pages: []dashboard.Page{{ID: "overview", Visuals: []dashboard.PageVisual{{ID: "chart", Kind: "visual", Visual: "chart"}}}}, Visualizations: map[string]visualizationdefinition.Definition{"chart": visual}}, model: model}
}

func TestCanonicalCommandDispatchAndRevisionAuthorization(t *testing.T) {
	fixture := canonicalCommandFixture(t)
	filters := dashboard.Filters{ServingStateID: "generation", DataRevisions: map[string]int64{"chart": 1}, CompiledState: &dashboardfilter.State{}}.WithDefaults()
	definition := fixture.definition
	command := dashboard.InteractionCommand{SourceKind: "visual", SourceID: "chart", InteractionKind: "point_selection", Action: "set", SpecRevision: definition.Visualizations["chart"].SpecRevision, DataRevision: 1, ServingStateID: "generation", FilterRevision: int64(filters.CompiledState.Revision), InteractionRevision: int64(filters.InteractionRevision), Mappings: []dashboard.InteractionCommandMapping{{Field: "state", Value: "CA"}}}
	prepared, err := (Service{Metrics: fixture}).PrepareSelect(Request{DashboardID: "dash", PageID: "overview", InteractionCommand: command}, filters)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Filters.Selections) != 1 || prepared.Filters.Selections[0].SourceID != "chart" {
		t.Fatalf("prepared selection = %#v", prepared)
	}
	command.ServingStateID = "stale-generation"
	if _, err := (Service{Metrics: fixture}).PrepareSelect(Request{DashboardID: "dash", PageID: "overview", InteractionCommand: command}, filters); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale command error = %v", err)
	}
}
