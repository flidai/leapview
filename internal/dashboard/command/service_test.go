package command_test

import (
	"math"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	. "github.com/flidai/leapview/internal/dashboard/command"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/flidai/leapview/internal/project/testing/dashboardfixture"
)

type fakeMetrics struct {
	report *dashboarddefinition.Definition
}

func authoritativeFilters() dashboard.Filters {
	state := dashboardfilter.State{}
	return dashboard.Filters{
		CompiledState:  &state,
		ServingStateID: "serving-test",
		DataRevisions:  map[string]int64{"chart": 1, "orders": 1, "customer_map": 1, "boolean_chart": 1},
	}.WithDefaults()
}

func stampInteractionCommand(definition dashboarddefinition.Definition, filters dashboard.Filters, command dashboard.InteractionCommand) dashboard.InteractionCommand {
	source := definition.Visualizations[command.SourceID]
	command.SpecRevision = source.SpecRevision
	command.DataRevision = filters.DataRevisions[command.SourceID]
	command.ServingStateID = filters.ServingStateID
	if filters.CompiledState != nil {
		command.FilterRevision = int64(filters.CompiledState.Revision)
	}
	command.InteractionRevision = int64(filters.InteractionRevision)
	return command
}

func (fakeMetrics) DefaultFilters(string) dashboard.Filters {
	return dashboard.Filters{}.WithDefaults()
}
func (fakeMetrics) NormalizeVisualizationWindow(_ string, request dashboard.TableRequest) dashboard.TableRequest {
	if request.Table == "" {
		request.Table = "orders"
	}
	return request.WithDefaults()
}
func (m fakeMetrics) dashboardDefinition(string) (dashboarddefinition.Definition, *semanticmodel.Model, bool) {
	authored := dashboardauthoring.Dashboard{
		ID: "dash", Title: "Dashboard", SemanticModel: "model",
		FilterDefinitions: map[string]dashboardfilter.Definition{
			"state": {
				Label: "State", Field: "state",
				Predicates: []dashboardfilter.PredicatePolicy{{
					Kind: dashboardfilter.ExpressionSet, Operators: []dashboardfilter.Operator{dashboardfilter.OperatorIn},
				}},
			},
		},
		Visuals: dashboardauthoring.MergeVisualizations(dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{
			"chart": {
				Type: "bar", Title: "Chart",
				Query: dashboardauthoring.VisualQuery{Dimensions: []dashboardauthoring.FieldRef{{Field: "state", Alias: "label"}}, Measures: []dashboardauthoring.FieldRef{{Field: "order_count", Alias: "value"}}},
				Interaction: dashboardauthoring.Interaction{PointSelection: dashboardauthoring.SelectionInteraction{
					Toggle: true, Mappings: []dashboardauthoring.SelectionMapping{{Field: "state", Value: "label"}}, Targets: []string{"orders"},
				}},
			},
			"boolean_chart": {
				Type: "bar", Title: "Boolean chart",
				Query: dashboardauthoring.VisualQuery{Dimensions: []dashboardauthoring.FieldRef{{Field: "active", Alias: "label"}}, Measures: []dashboardauthoring.FieldRef{{Field: "order_count", Alias: "value"}}},
				Interaction: dashboardauthoring.Interaction{PointSelection: dashboardauthoring.SelectionInteraction{
					Toggle: true, Mappings: []dashboardauthoring.SelectionMapping{{Field: "active", Value: "label"}}, Targets: []string{"orders"},
				}},
			},
			"customer_map": {
				Type: "map", Title: "Customer map",
				Query: dashboardauthoring.VisualQuery{
					Table:      "orders",
					Dimensions: []dashboardauthoring.FieldRef{{Field: "latitude", Alias: "latitude"}, {Field: "longitude", Alias: "longitude"}, {Field: "state", Alias: "state"}},
					Measures:   []dashboardauthoring.FieldRef{{Field: "order_count", Alias: "value"}},
				},
				Geo: dashboardauthoring.VisualGeo{Basemap: "blank", Layers: []dashboardauthoring.VisualGeoLayer{{ID: "customers", Kind: "point", Latitude: "latitude", Longitude: "longitude", Value: "value"}}},
				Interaction: dashboardauthoring.Interaction{SpatialSelection: dashboardauthoring.SpatialSelectionInteraction{
					Gestures:  []string{"box", "lasso", "radius"},
					Latitude:  dashboardauthoring.SpatialSelectionMapping{Source: "latitude", Field: "latitude"},
					Longitude: dashboardauthoring.SpatialSelectionMapping{Source: "longitude", Field: "longitude"},
					Targets:   []string{"chart", "orders"},
				}},
			},
		}), dashboardauthoring.TabularVisualizations("table", map[string]dashboardauthoring.TableVisual{"orders": {Title: "Orders", Query: dashboardauthoring.TableQuery{Table: "orders", Fields: []string{"orders.state"}}}})),
		Pages: []dashboard.Page{
			{ID: "overview", Title: "Overview", FilterBindings: map[string]dashboardfilter.Binding{
				"state": {Filter: "state", Default: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionUnfiltered}},
			}, Visuals: []dashboard.PageVisual{
				{ID: "state-slicer", Kind: "slicer", Binding: dashboardfilter.BindingRef{Scope: dashboardfilter.ScopePage, ID: "state"}, Placement: dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 3, RowSpan: 2}},
				{ID: "chart", Kind: "visual", Visual: "chart", Placement: dashboard.PagePlacement{Col: 4, Row: 1, ColSpan: 4, RowSpan: 4}}, {ID: "customer-map", Kind: "visual", Visual: "customer_map", Placement: dashboard.PagePlacement{Col: 8, Row: 1, ColSpan: 4, RowSpan: 4}}, {ID: "orders", Kind: "visual", Visual: "orders", Placement: dashboard.PagePlacement{Col: 1, Row: 5, ColSpan: 12, RowSpan: 4}},
			}},
			{ID: "boolean", Title: "Boolean", Visuals: []dashboard.PageVisual{{ID: "boolean-chart", Kind: "visual", Visual: "boolean_chart", Placement: dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 6, RowSpan: 4}}, {ID: "orders", Kind: "visual", Visual: "orders", Placement: dashboard.PagePlacement{Col: 7, Row: 1, ColSpan: 6, RowSpan: 4}}}},
		},
	}
	model := &semanticmodel.Model{
		Name: "model",
		Tables: map[string]semanticmodel.Table{"orders": {Dimensions: map[string]semanticmodel.MetricDimension{
			"state": {Type: "string"}, "active": {Type: "boolean"}, "latitude": {Type: "number"}, "longitude": {Type: "number"},
		}}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"state":     {Type: "string", Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.state"}}},
			"active":    {Type: "boolean", Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.active"}}},
			"latitude":  {Type: "number", Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.latitude"}}},
			"longitude": {Type: "number", Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.longitude"}}},
		},
		Measures: map[string]semanticmodel.MetricMeasure{"order_count": {Fact: "orders", Aggregation: "count"}},
	}
	definition := dashboardfixture.Compile(authored, model)
	if m.report != nil {
		definition = *m.report
	}
	return definition, model, true
}

func (m fakeMetrics) Resolver() dashboardresolver.Resolver {
	return fakeDashboardResolver{metrics: m}
}

type fakeDashboardResolver struct{ metrics fakeMetrics }

func (r fakeDashboardResolver) Resolve(dashboardID string) (dashboardresolver.Resolved, error) {
	definition, model, ok := r.metrics.dashboardDefinition(dashboardID)
	if !ok {
		return dashboardresolver.Resolved{}, dashboardresolver.ErrNotFound
	}
	return dashboardresolver.Resolved{Definition: definition, Model: model, Source: dashboardresolver.SourceMetadata{Kind: dashboardresolver.SourceProject, WorkspaceID: "workspace"}}, nil
}

func testDashboardDefinition() dashboarddefinition.Definition {
	resolved, err := (fakeMetrics{}).Resolver().Resolve("dash")
	if err != nil {
		panic(err)
	}
	return resolved.Definition
}

func TestPrepareSpatialSelectValidatesGeometryAndUsesExplicitTargets(t *testing.T) {
	definition := testDashboardDefinition()
	command := dashboard.SpatialSelectionCommand{
		VisualID: "customer_map", SpecRevision: definition.Visualizations["customer_map"].SpecRevision, DataRevision: 1,
		ServingStateID: "serving-test",
		InteractionID:  "spatial_selection", Action: "set", Gesture: visualizationir.VisualizationSpatialSelectionGestureBox,
		Geometry: visualizationir.VisualizationSpatialSelectionGeometry{Value: &visualizationir.VisualizationSpatialBoxSelection{
			VisualizationSpatialSelectionGeometryBase: visualizationir.VisualizationSpatialSelectionGeometryBase{Kind: "box"}, Kind: "box",
			Bounds: visualizationir.VisualizationSpatialBounds{West: -50, South: -25, East: -40, North: -15},
		}},
	}
	filters := authoritativeFilters()
	prepared, err := (Service{Metrics: fakeMetrics{}}).PrepareSpatialSelect(Request{DashboardID: "dash", PageID: "overview", SpatialInteractionCommand: command}, filters)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Filters.SpatialSelections) != 1 || len(prepared.Plan.Targets) != 2 || prepared.Plan.Targets[0].ID != "chart" || prepared.Plan.Targets[1].ID != "orders" {
		t.Fatalf("prepared = %#v", prepared)
	}

	command.Gesture = visualizationir.VisualizationSpatialSelectionGestureRadius
	if _, err := (Service{Metrics: fakeMetrics{}}).PrepareSpatialSelect(Request{DashboardID: "dash", PageID: "overview", SpatialInteractionCommand: command}, filters); err == nil {
		t.Fatal("mismatched gesture and geometry was accepted")
	}
	command.Gesture = visualizationir.VisualizationSpatialSelectionGestureBox
	box := command.Geometry.Value.(*visualizationir.VisualizationSpatialBoxSelection)
	box.Bounds.North = math.Inf(1)
	if _, err := (Service{Metrics: fakeMetrics{}}).PrepareSpatialSelect(Request{DashboardID: "dash", PageID: "overview", SpatialInteractionCommand: command}, filters); err == nil {
		t.Fatal("non-finite geometry was accepted")
	}
}

func TestPrepareVisualWindowValidatesTypedIdentityAndCoordinates(t *testing.T) {
	definition := testDashboardDefinition()
	request := dashboard.VisualizationWindowRequest{
		VisualID: "orders", SpecRevision: definition.Visualizations["orders"].SpecRevision, DataRevision: 9,
		RequestSeq: 7, ResetVersion: 2, Start: 150, Limit: 50, BlockID: "b",
		Sort: []visualizationir.VisualizationSort{{
			Field:     visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "state"},
			Direction: visualizationir.VisualizationSortDirectionDescending,
		}},
	}
	prepared, err := (Service{Metrics: fakeMetrics{}}).PrepareVisualWindow(Request{DashboardID: "dash", PageID: "overview", VisualWindowCommand: request}, dashboard.Filters{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Plan.Targets) != 1 {
		t.Fatalf("targets = %#v", prepared.Plan.Targets)
	}
	target := prepared.Plan.Targets[0]
	if target.Kind != TargetWindow || target.ID != "orders" || target.WindowRequest.Block != "b" || target.WindowRequest.Start != 150 || target.WindowRequest.Count != 50 || target.WindowRequest.RequestSeq != 7 || target.WindowRequest.Sort.Key != "state" || target.WindowRequest.Sort.Direction != "desc" {
		t.Fatalf("target = %#v", target)
	}

	request.SpecRevision = "sha256:forged"
	if _, err := (Service{Metrics: fakeMetrics{}}).PrepareVisualWindow(Request{DashboardID: "dash", VisualWindowCommand: request}, dashboard.Filters{}); err == nil {
		t.Fatal("forged table revision was accepted")
	}
	request.SpecRevision = definition.Visualizations["orders"].SpecRevision
	request.RequestSeq = 0
	if _, err := (Service{Metrics: fakeMetrics{}}).PrepareVisualWindow(Request{DashboardID: "dash", VisualWindowCommand: request}, dashboard.Filters{}); err == nil {
		t.Fatal("non-positive table request sequence was accepted")
	}
}

func TestPrepareSelectUsesAuthoritativeSelectionsAndExplicitTargetsOnly(t *testing.T) {
	definition := testDashboardDefinition()
	authoritative := dashboard.Filters{
		Selections: []dashboard.InteractionSelection{{
			SourceKind: "visual", SourceID: "existing", InteractionKind: "point_selection",
		}},
		ServingStateID: "serving-test",
		CompiledState:  &dashboardfilter.State{},
		DataRevisions:  map[string]int64{"chart": 1},
	}.WithDefaults()
	command := stampInteractionCommand(definition, authoritative, dashboard.InteractionCommand{
		SourceKind: "visual", SourceID: "chart", InteractionKind: "point_selection", Action: "set",
		Mappings: []dashboard.InteractionCommandMapping{{Field: "state", Value: "RJ"}},
	})
	prepared, err := (Service{Metrics: fakeMetrics{}}).PrepareSelect(Request{
		DashboardID: "dash", PageID: "overview",
		InteractionCommand: command,
	}, authoritative)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Filters.Selections) != 2 || prepared.Filters.Selections[0].SourceID != "existing" {
		t.Fatalf("authoritative selections = %#v", prepared.Filters.Selections)
	}
	if len(prepared.Plan.Targets) != 1 || prepared.Plan.Targets[0].Kind != TargetWindow || prepared.Plan.Targets[0].ID != "orders" {
		t.Fatalf("targets = %#v", prepared.Plan.Targets)
	}
}

func TestPrepareSelectRestrictsExplicitTargetsToActivePage(t *testing.T) {
	definition := testDashboardDefinition()
	chart := definition.Visualizations["chart"]
	spec := chart.Spec.Value.(*visualizationir.CartesianVisualizationSpec)
	spec.Interactions[0].Targets = []visualizationir.VisualizationInteractionTarget{
		{VisualID: "orders", Effect: visualizationir.VisualizationInteractionEffectFilter},
		{VisualID: "boolean_chart", Effect: visualizationir.VisualizationInteractionEffectHighlight},
	}
	definition.Visualizations["chart"] = chart
	filters := authoritativeFilters()
	command := stampInteractionCommand(definition, filters, dashboard.InteractionCommand{
		SourceKind: "visual", SourceID: "chart", InteractionKind: "point_selection", Action: "set",
		Mappings: []dashboard.InteractionCommandMapping{{Field: "state", Value: "RJ"}},
	})

	prepared, err := (Service{Metrics: fakeMetrics{report: &definition}}).PrepareSelect(Request{
		DashboardID: "dash", PageID: "overview",
		InteractionCommand: command,
	}, filters)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Plan.Targets) != 1 || prepared.Plan.Targets[0].ID != "orders" {
		t.Fatalf("off-page targets leaked into active stream plan: %#v", prepared.Plan.Targets)
	}
}

func TestPrepareSelectCanonicalizesTypedMappings(t *testing.T) {
	definition := testDashboardDefinition()
	filters := authoritativeFilters()
	command := stampInteractionCommand(definition, filters, dashboard.InteractionCommand{
		SourceKind: "visual", SourceID: "boolean_chart", InteractionKind: "point_selection", Action: "set",
		Mappings: []dashboard.InteractionCommandMapping{{Field: "active", Value: false}},
	})
	prepared, err := (Service{Metrics: fakeMetrics{}}).PrepareSelect(Request{
		DashboardID: "dash", PageID: "boolean",
		InteractionCommand: command,
	}, filters)
	if err != nil {
		t.Fatal(err)
	}
	value := prepared.Filters.Selections[0].Entries[0].Mappings[0].Value
	if typed, ok := value.(bool); !ok || typed {
		t.Fatalf("typed value = %#v", value)
	}
}

func TestPrepareSelectRejectsForgedMapping(t *testing.T) {
	definition := testDashboardDefinition()
	filters := authoritativeFilters()
	command := stampInteractionCommand(definition, filters, dashboard.InteractionCommand{
		SourceKind: "visual", SourceID: "chart", InteractionKind: "point_selection", Action: "set",
		Mappings: []dashboard.InteractionCommandMapping{{Field: "orders.secret", Value: "x"}},
	})
	_, err := (Service{Metrics: fakeMetrics{}}).PrepareSelect(Request{
		DashboardID: "dash", PageID: "overview",
		InteractionCommand: command,
	}, filters)
	if err == nil {
		t.Fatal("forged mapping was accepted")
	}
}

func TestPrepareSelectRejectsEveryStaleRevisionBeforeApplyingState(t *testing.T) {
	definition := testDashboardDefinition()
	filters := authoritativeFilters()
	filters.InteractionRevision = 3
	command := stampInteractionCommand(definition, filters, dashboard.InteractionCommand{
		SourceKind: "visual", SourceID: "chart", InteractionKind: "point_selection", Action: "set",
		Mappings: []dashboard.InteractionCommandMapping{{Field: "state", Value: "RJ"}},
	})
	for name, mutate := range map[string]func(*dashboard.InteractionCommand){
		"serving state": func(command *dashboard.InteractionCommand) { command.ServingStateID = "stale" },
		"specification": func(command *dashboard.InteractionCommand) { command.SpecRevision = "sha256:stale" },
		"data":          func(command *dashboard.InteractionCommand) { command.DataRevision++ },
		"filter":        func(command *dashboard.InteractionCommand) { command.FilterRevision++ },
		"interaction":   func(command *dashboard.InteractionCommand) { command.InteractionRevision-- },
	} {
		t.Run(name, func(t *testing.T) {
			stale := command
			mutate(&stale)
			_, err := (Service{Metrics: fakeMetrics{}}).PrepareSelect(Request{
				DashboardID: "dash", PageID: "overview", InteractionCommand: stale,
			}, filters)
			if err == nil || !strings.Contains(err.Error(), "stale") {
				t.Fatalf("error = %v, want stale rejection", err)
			}
		})
	}
}

func TestPrepareClearSelectionPlansAffectedTargetUnion(t *testing.T) {
	definition := testDashboardDefinition()
	chart := definition.Visualizations["chart"]
	spec := chart.Spec.Value.(*visualizationir.CartesianVisualizationSpec)
	spec.Interactions[0].Targets = []visualizationir.VisualizationInteractionTarget{
		{VisualID: "orders", Effect: visualizationir.VisualizationInteractionEffectFilter},
		{VisualID: "customer_map", Effect: visualizationir.VisualizationInteractionEffectHighlight},
	}
	definition.Visualizations["chart"] = chart
	prepared, err := (Service{Metrics: fakeMetrics{report: &definition}}).PrepareClearSelection(Request{
		DashboardID: "dash", PageID: "overview",
	}, dashboard.Filters{Selections: []dashboard.InteractionSelection{
		{SourceKind: "visual", SourceID: "chart", InteractionKind: "point_selection"},
		{SourceKind: "visual", SourceID: "boolean_chart", InteractionKind: "point_selection"},
	}}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Filters.Selections) != 0 || len(prepared.Plan.Targets) != 2 {
		t.Fatalf("prepared = %#v", prepared)
	}
}
