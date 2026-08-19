package command_test

import (
	"math"
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

func authoritativeFilters() dashboard.Filters {
	state := dashboardfilter.State{}
	return dashboard.Filters{CompiledState: &state, ServingStateID: "serving-test", DataRevisions: map[string]int64{"chart": 1, "orders": 1, "customer_map": 1, "boolean_chart": 1}}.WithDefaults()
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
	chart := canonicalCartesian(t, "chart", "state", "state", []string{"orders"})
	booleanChart := canonicalCartesian(t, "boolean_chart", "active", "active", []string{"orders"})
	customerMap := canonicalGeographic(t, "customer_map", []string{"chart", "orders"})
	orders := canonicalTable(t, "orders")
	model := &semanticmodel.Model{Name: "model", Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}, Tables: map[string]semanticmodel.Table{"orders": {ModelName: "orders", GrainEntity: "state", Entities: map[string]semanticmodel.EntityDefinition{"state": {Type: "primary", Fields: []string{"state"}}}, Dimensions: map[string]semanticmodel.MetricDimension{"state": {Field: "orders.state", Type: "string", Datatype: semanticmodel.DataTypeString}}}}, Dimensions: map[string]semanticmodel.SemanticDimension{"state": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.state"}}}}, Metrics: map[string]semanticmodel.Metric{"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.state"}}}}
	model.Tables["orders"].Dimensions["active"] = semanticmodel.MetricDimension{Field: "orders.active", Type: "boolean", Datatype: semanticmodel.DataTypeBoolean}
	model.Tables["orders"].Dimensions["latitude"] = semanticmodel.MetricDimension{Field: "orders.latitude", Type: "number", Datatype: semanticmodel.DataTypeFloat}
	model.Tables["orders"].Dimensions["longitude"] = semanticmodel.MetricDimension{Field: "orders.longitude", Type: "number", Datatype: semanticmodel.DataTypeFloat}
	model.Dimensions["active"] = semanticmodel.SemanticDimension{Type: "boolean", Datatype: semanticmodel.DataTypeBoolean, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.active"}}}
	model.Dimensions["latitude"] = semanticmodel.SemanticDimension{Type: "number", Datatype: semanticmodel.DataTypeFloat, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.latitude"}}}
	model.Dimensions["longitude"] = semanticmodel.SemanticDimension{Type: "number", Datatype: semanticmodel.DataTypeFloat, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.longitude"}}}
	return canonicalMetrics{definition: dashboarddefinition.Definition{ID: "dash", SemanticModel: "model", Pages: []dashboard.Page{{ID: "overview", Title: "Overview", Visuals: []dashboard.PageVisual{{ID: "chart", Kind: "visual", Visual: "chart"}, {ID: "customer-map", Kind: "visual", Visual: "customer_map"}, {ID: "orders", Kind: "visual", Visual: "orders"}}}, {ID: "boolean", Title: "Boolean", Visuals: []dashboard.PageVisual{{ID: "boolean-chart", Kind: "visual", Visual: "boolean_chart"}, {ID: "orders", Kind: "visual", Visual: "orders"}}}}, Visualizations: map[string]visualizationdefinition.Definition{"chart": chart, "boolean_chart": booleanChart, "customer_map": customerMap, "orders": orders}}, model: model}
}

func testDashboardDefinition() dashboarddefinition.Definition {
	return canonicalCommandFixture(&testing.T{}).definition
}

func canonicalBase(kind, title string, fields []visualizationir.VisualizationField) visualizationir.VisualizationSpecBase {
	return visualizationir.VisualizationSpecBase{Kind: kind, Title: title, Accessibility: visualizationir.VisualizationAccessibility{Title: title, Description: title}, Datasets: []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: fields}}, DataBudget: visualizationir.VisualizationDataBudget{MaxRows: 100}}
}

func canonicalCartesian(t *testing.T, id, field, target string, targets []string) visualizationdefinition.Definition {
	t.Helper()
	fields := []visualizationir.VisualizationField{{ID: field, Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString, Label: field}, {ID: "value", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Label: "Value"}}
	base := canonicalBase("cartesian", id, fields)
	interactionTargets := make([]visualizationir.VisualizationInteractionTarget, len(targets))
	for index, visualID := range targets {
		interactionTargets[index] = visualizationir.VisualizationInteractionTarget{VisualID: visualID, Effect: visualizationir.VisualizationInteractionEffectFilter}
	}
	base.Interactions = []visualizationir.VisualizationInteraction{{ID: "interaction-0", Kind: visualizationir.VisualizationInteractionKindSelect, Mode: visualizationir.VisualizationSelectionModeSingle, RequiresStableIdentity: false, Mappings: []visualizationir.VisualizationInteractionMapping{{Source: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: field}, TargetFieldID: target}}, Targets: interactionTargets}}
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{VisualizationSpecBase: base, Kind: "cartesian", Mark: visualizationir.VisualizationCartesianMarkBar, X: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: field}, Y: []visualizationir.VisualizationFieldRef{{Dataset: "primary", Field: "value"}}, Presentation: visualizationir.CartesianVisualizationPresentation{VisualizationPresentation: visualizationir.VisualizationPresentation{Legend: visualizationir.VisualizationLegendPositionHidden, LabelPolicy: visualizationir.VisualizationLabelPolicy{Density: visualizationir.VisualizationLabelDensityHidden, MaxCharacters: 24, TooltipFallback: true}}}}}
	definition, err := visualizationdefinition.New(id, spec, visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultCategoryValue, ModelID: "model", DatasetID: "primary", Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: "orders", Dimensions: []visualizationdefinition.FieldBinding{{FieldID: field, Alias: field}}, Metrics: []visualizationdefinition.FieldBinding{{FieldID: "order_count", Alias: "value"}}, Limit: 100}})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func canonicalGeographic(t *testing.T, id string, targets []string) visualizationdefinition.Definition {
	t.Helper()
	fields := []visualizationir.VisualizationField{{ID: "latitude", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeFloat, Label: "Latitude"}, {ID: "longitude", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeFloat, Label: "Longitude"}, {ID: "state", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString, Label: "State"}, {ID: "value", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Label: "Value"}}
	base := canonicalBase("geographic", id, fields)
	spatialTargets := make([]visualizationir.VisualizationInteractionTarget, len(targets))
	for index, visualID := range targets {
		spatialTargets[index] = visualizationir.VisualizationInteractionTarget{VisualID: visualID, Effect: visualizationir.VisualizationInteractionEffectFilter}
	}
	spatial := visualizationir.VisualizationSpatialSelectionInteraction{ID: "spatial_selection", Gestures: []visualizationir.VisualizationSpatialSelectionGesture{visualizationir.VisualizationSpatialSelectionGestureBox, visualizationir.VisualizationSpatialSelectionGestureLasso, visualizationir.VisualizationSpatialSelectionGestureRadius}, Latitude: visualizationir.VisualizationSpatialFieldMapping{Source: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "latitude"}, TargetFieldID: "latitude"}, Longitude: visualizationir.VisualizationSpatialFieldMapping{Source: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "longitude"}, TargetFieldID: "longitude"}, Targets: spatialTargets}
	baseFieldRef := func(field string) visualizationir.VisualizationFieldRef {
		return visualizationir.VisualizationFieldRef{Dataset: "primary", Field: field}
	}
	layer := visualizationir.VisualizationPointLayer{VisualizationGeographicLayerBase: visualizationir.VisualizationGeographicLayerBase{ID: "customers", Kind: "point", Tooltip: []visualizationir.VisualizationFieldRef{baseFieldRef("state")}, Visibility: visualizationir.VisualizationMapVisibility{MinimumZoom: 0, MaximumZoom: 24}}, Kind: "point", Latitude: baseFieldRef("latitude"), Longitude: baseFieldRef("longitude"), Value: func() *visualizationir.VisualizationFieldRef { value := baseFieldRef("value"); return &value }(), Size: visualizationir.VisualizationMapSizeScale{MinimumRadius: 4, MaximumRadius: 16}, Cluster: visualizationir.VisualizationMapCluster{Radius: 24, MaximumZoom: 14, MinimumPoints: 2}, Opacity: 1}
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.GeographicVisualizationSpec{VisualizationSpecBase: base, Kind: "geographic", Layers: []visualizationir.VisualizationGeographicLayer{{Value: &layer}}, SpatialInteractions: []visualizationir.VisualizationSpatialSelectionInteraction{spatial}, Presentation: visualizationir.GeographicVisualizationPresentation{VisualizationPresentation: visualizationir.VisualizationPresentation{Legend: visualizationir.VisualizationLegendPositionHidden, LabelPolicy: visualizationir.VisualizationLabelPolicy{Density: visualizationir.VisualizationLabelDensityHidden, MaxCharacters: 24, TooltipFallback: true}}}}}
	definition, err := visualizationdefinition.New(id, spec, visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QuerySpatial, ResultShape: visualizationdefinition.ResultGeographicFeatures, ModelID: "model", DatasetID: "primary", Spatial: &visualizationdefinition.SpatialQueryBinding{TableID: "orders", Dimensions: []visualizationdefinition.FieldBinding{{FieldID: "latitude", Alias: "latitude"}, {FieldID: "longitude", Alias: "longitude"}, {FieldID: "state", Alias: "state"}}, Metrics: []visualizationdefinition.FieldBinding{{FieldID: "order_count", Alias: "value"}}, Limit: 100}})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func canonicalTable(t *testing.T, id string) visualizationdefinition.Definition {
	t.Helper()
	fields := []visualizationir.VisualizationField{{ID: "state", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString, Label: "State"}}
	base := canonicalBase("table", "Orders", fields)
	base.Interactions = []visualizationir.VisualizationInteraction{{ID: "interaction-0", Kind: visualizationir.VisualizationInteractionKindSelect, Mode: visualizationir.VisualizationSelectionModeMultiple, Mappings: []visualizationir.VisualizationInteractionMapping{{Source: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "state"}, TargetFieldID: "state"}}, Targets: []visualizationir.VisualizationInteractionTarget{{VisualID: "chart", Effect: visualizationir.VisualizationInteractionEffectFilter}}}}
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.TableVisualizationSpec{VisualizationSpecBase: base, Kind: "table", Columns: []visualizationir.TableVisualizationColumn{{Field: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "state"}, Label: "State", Formatting: []visualizationir.TableVisualizationFormattingRule{}}}, Presentation: visualizationir.GridVisualizationPresentation{RowHeight: 28, ShowHeader: true}}}
	definition, err := visualizationdefinition.New(id, spec, visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryDetail, ResultShape: visualizationdefinition.ResultDetailWindow, ModelID: "model", DatasetID: "primary", Detail: &visualizationdefinition.DetailQueryBinding{TableID: "orders", Fields: []visualizationdefinition.FieldBinding{{FieldID: "state", Alias: "state"}}, Limit: 100}})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func TestCanonicalCommandDispatchAndRevisionAuthorization(t *testing.T) {
	fixture := canonicalCommandFixture(t)
	filters := dashboard.Filters{ServingStateID: "generation", DataRevisions: map[string]int64{"chart": 1}, CompiledState: &dashboardfilter.State{}}.WithDefaults()
	definition := fixture.definition
	command := dashboard.InteractionCommand{SourceKind: "visual", SourceID: "chart", InteractionKind: "interaction-0", Action: "set", SpecRevision: definition.Visualizations["chart"].SpecRevision, DataRevision: 1, ServingStateID: "generation", FilterRevision: int64(filters.CompiledState.Revision), InteractionRevision: int64(filters.InteractionRevision), Mappings: []dashboard.InteractionCommandMapping{{Field: "state", Value: "CA"}}}
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

func TestPrepareSpatialSelectValidatesGeometryAndUsesExplicitTargets(t *testing.T) {
	definition := testDashboardDefinition()
	command := dashboard.SpatialSelectionCommand{
		VisualID: "customer_map", SpecRevision: definition.Visualizations["customer_map"].SpecRevision, DataRevision: 1,
		ServingStateID: "serving-test", InteractionID: "spatial_selection", Action: "set", Gesture: visualizationir.VisualizationSpatialSelectionGestureBox,
		Geometry: visualizationir.VisualizationSpatialSelectionGeometry{Value: &visualizationir.VisualizationSpatialBoxSelection{VisualizationSpatialSelectionGeometryBase: visualizationir.VisualizationSpatialSelectionGeometryBase{Kind: "box"}, Kind: "box", Bounds: visualizationir.VisualizationSpatialBounds{West: -50, South: -25, East: -40, North: -15}}},
	}
	filters := authoritativeFilters()
	prepared, err := (Service{Metrics: canonicalCommandFixture(t)}).PrepareSpatialSelect(Request{DashboardID: "dash", PageID: "overview", SpatialInteractionCommand: command}, filters)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Filters.SpatialSelections) != 1 || len(prepared.Plan.Targets) != 2 || prepared.Plan.Targets[0].ID != "chart" || prepared.Plan.Targets[1].ID != "orders" {
		t.Fatalf("prepared = %#v", prepared)
	}
	command.Gesture = visualizationir.VisualizationSpatialSelectionGestureRadius
	if _, err := (Service{Metrics: canonicalCommandFixture(t)}).PrepareSpatialSelect(Request{DashboardID: "dash", PageID: "overview", SpatialInteractionCommand: command}, filters); err == nil {
		t.Fatal("mismatched gesture and geometry was accepted")
	}
	command.Gesture = visualizationir.VisualizationSpatialSelectionGestureBox
	box := command.Geometry.Value.(*visualizationir.VisualizationSpatialBoxSelection)
	box.Bounds.North = math.Inf(1)
	if _, err := (Service{Metrics: canonicalCommandFixture(t)}).PrepareSpatialSelect(Request{DashboardID: "dash", PageID: "overview", SpatialInteractionCommand: command}, filters); err == nil {
		t.Fatal("non-finite geometry was accepted")
	}
}

func TestPrepareVisualWindowValidatesTypedIdentityAndCoordinates(t *testing.T) {
	definition := testDashboardDefinition()
	request := dashboard.VisualizationWindowRequest{VisualID: "orders", SpecRevision: definition.Visualizations["orders"].SpecRevision, DataRevision: 1, RequestSeq: 7, ResetVersion: 2, Start: 150, Limit: 50, BlockID: "b", Sort: []visualizationir.VisualizationSort{{Field: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "state"}, Direction: visualizationir.VisualizationSortDirectionDescending}}}
	prepared, err := (Service{Metrics: canonicalCommandFixture(t)}).PrepareVisualWindow(Request{DashboardID: "dash", PageID: "overview", VisualWindowCommand: request}, dashboard.Filters{}.WithDefaults())
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
	if _, err := (Service{Metrics: canonicalCommandFixture(t)}).PrepareVisualWindow(Request{DashboardID: "dash", VisualWindowCommand: request}, dashboard.Filters{}); err == nil {
		t.Fatal("forged table revision was accepted")
	}
	request.SpecRevision = definition.Visualizations["orders"].SpecRevision
	request.RequestSeq = 0
	if _, err := (Service{Metrics: canonicalCommandFixture(t)}).PrepareVisualWindow(Request{DashboardID: "dash", VisualWindowCommand: request}, dashboard.Filters{}); err == nil {
		t.Fatal("non-positive table request sequence was accepted")
	}
}

func TestPrepareSelectUsesAuthoritativeSelectionsAndExplicitTargetsOnly(t *testing.T) {
	definition := testDashboardDefinition()
	authoritative := dashboard.Filters{Selections: []dashboard.InteractionSelection{{SourceKind: "visual", SourceID: "existing", InteractionKind: "interaction-0"}}, ServingStateID: "serving-test", CompiledState: &dashboardfilter.State{}, DataRevisions: map[string]int64{"chart": 1}}.WithDefaults()
	command := stampInteractionCommand(definition, authoritative, dashboard.InteractionCommand{SourceKind: "visual", SourceID: "chart", InteractionKind: "interaction-0", Action: "set", Mappings: []dashboard.InteractionCommandMapping{{Field: "state", Value: "RJ"}}})
	prepared, err := (Service{Metrics: canonicalCommandFixture(t)}).PrepareSelect(Request{DashboardID: "dash", PageID: "overview", InteractionCommand: command}, authoritative)
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
	spec.Interactions[0].Targets = []visualizationir.VisualizationInteractionTarget{{VisualID: "orders", Effect: visualizationir.VisualizationInteractionEffectFilter}, {VisualID: "boolean_chart", Effect: visualizationir.VisualizationInteractionEffectHighlight}}
	definition.Visualizations["chart"] = chart
	filters := authoritativeFilters()
	command := stampInteractionCommand(definition, filters, dashboard.InteractionCommand{SourceKind: "visual", SourceID: "chart", InteractionKind: "interaction-0", Action: "set", Mappings: []dashboard.InteractionCommandMapping{{Field: "state", Value: "RJ"}}})
	prepared, err := (Service{Metrics: canonicalMetrics{definition: definition, model: canonicalCommandFixture(t).model}}).PrepareSelect(Request{DashboardID: "dash", PageID: "overview", InteractionCommand: command}, filters)
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
	command := stampInteractionCommand(definition, filters, dashboard.InteractionCommand{SourceKind: "visual", SourceID: "boolean_chart", InteractionKind: "interaction-0", Action: "set", Mappings: []dashboard.InteractionCommandMapping{{Field: "active", Value: false}}})
	prepared, err := (Service{Metrics: canonicalCommandFixture(t)}).PrepareSelect(Request{DashboardID: "dash", PageID: "boolean", InteractionCommand: command}, filters)
	if err != nil {
		t.Fatal(err)
	}
	value := prepared.Filters.Selections[0].Entries[0].Mappings[0].Value
	if typed, ok := value.(bool); !ok || typed {
		t.Fatalf("typed value = %#v", value)
	}
}

func TestPrepareSelectUsesCompiledInteractionIDForSemanticTable(t *testing.T) {
	definition := testDashboardDefinition()
	filters := authoritativeFilters()
	command := stampInteractionCommand(definition, filters, dashboard.InteractionCommand{SourceKind: "visual", SourceID: "orders", InteractionKind: "interaction-0", Action: "set", Mappings: []dashboard.InteractionCommandMapping{{Field: "state", Value: "RJ"}}})
	prepared, err := (Service{Metrics: canonicalCommandFixture(t)}).PrepareSelect(Request{DashboardID: "dash", PageID: "overview", InteractionCommand: command}, filters)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Filters.Selections) != 1 || prepared.Filters.Selections[0].InteractionKind != "interaction-0" || len(prepared.Plan.Targets) != 1 || prepared.Plan.Targets[0].ID != "chart" {
		t.Fatalf("prepared = %#v", prepared)
	}
}

func TestPrepareSelectRejectsLegacyInteractionKindWhenCompiledIDDiffers(t *testing.T) {
	definition := testDashboardDefinition()
	filters := authoritativeFilters()
	command := stampInteractionCommand(definition, filters, dashboard.InteractionCommand{SourceKind: "visual", SourceID: "chart", InteractionKind: "point_selection", Action: "set", Mappings: []dashboard.InteractionCommandMapping{{Field: "state", Value: "RJ"}}})
	if _, err := (Service{Metrics: canonicalCommandFixture(t)}).PrepareSelect(Request{DashboardID: "dash", PageID: "overview", InteractionCommand: command}, filters); err == nil || !strings.Contains(err.Error(), "interaction ID") {
		t.Fatalf("error = %v, want compiled interaction ID rejection", err)
	}
}

func TestPrepareSelectRejectsForgedMapping(t *testing.T) {
	definition := testDashboardDefinition()
	filters := authoritativeFilters()
	command := stampInteractionCommand(definition, filters, dashboard.InteractionCommand{SourceKind: "visual", SourceID: "chart", InteractionKind: "interaction-0", Action: "set", Mappings: []dashboard.InteractionCommandMapping{{Field: "orders.secret", Value: "x"}}})
	if _, err := (Service{Metrics: canonicalCommandFixture(t)}).PrepareSelect(Request{DashboardID: "dash", PageID: "overview", InteractionCommand: command}, filters); err == nil {
		t.Fatal("forged mapping was accepted")
	}
}

func TestPrepareSelectRejectsEveryStaleRevisionBeforeApplyingState(t *testing.T) {
	definition := testDashboardDefinition()
	filters := authoritativeFilters()
	filters.InteractionRevision = 3
	command := stampInteractionCommand(definition, filters, dashboard.InteractionCommand{SourceKind: "visual", SourceID: "chart", InteractionKind: "interaction-0", Action: "set", Mappings: []dashboard.InteractionCommandMapping{{Field: "state", Value: "RJ"}}})
	for name, mutate := range map[string]func(*dashboard.InteractionCommand){"serving state": func(command *dashboard.InteractionCommand) { command.ServingStateID = "stale" }, "specification": func(command *dashboard.InteractionCommand) { command.SpecRevision = "sha256:stale" }, "data": func(command *dashboard.InteractionCommand) { command.DataRevision++ }, "filter": func(command *dashboard.InteractionCommand) { command.FilterRevision++ }, "interaction": func(command *dashboard.InteractionCommand) { command.InteractionRevision-- }} {
		t.Run(name, func(t *testing.T) {
			stale := command
			mutate(&stale)
			if _, err := (Service{Metrics: canonicalCommandFixture(t)}).PrepareSelect(Request{DashboardID: "dash", PageID: "overview", InteractionCommand: stale}, filters); err == nil || !strings.Contains(err.Error(), "stale") {
				t.Fatalf("error = %v, want stale rejection", err)
			}
		})
	}
}

func TestPrepareClearSelectionPlansAffectedTargetUnion(t *testing.T) {
	definition := testDashboardDefinition()
	chart := definition.Visualizations["chart"]
	spec := chart.Spec.Value.(*visualizationir.CartesianVisualizationSpec)
	spec.Interactions[0].Targets = []visualizationir.VisualizationInteractionTarget{{VisualID: "orders", Effect: visualizationir.VisualizationInteractionEffectFilter}, {VisualID: "customer_map", Effect: visualizationir.VisualizationInteractionEffectHighlight}}
	definition.Visualizations["chart"] = chart
	fixture := canonicalCommandFixture(t)
	fixture.definition = definition
	prepared, err := (Service{Metrics: fixture}).PrepareClearSelection(Request{DashboardID: "dash", PageID: "overview"}, dashboard.Filters{Selections: []dashboard.InteractionSelection{{SourceKind: "visual", SourceID: "chart", InteractionKind: "interaction-0"}, {SourceKind: "visual", SourceID: "boolean_chart", InteractionKind: "interaction-0"}}}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Filters.Selections) != 0 || len(prepared.Plan.Targets) != 2 {
		t.Fatalf("prepared = %#v", prepared)
	}
}
