package definition

import (
	"testing"

	"github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestDefinitionValidateRejectsRendererAndQueryMismatches(t *testing.T) {
	t.Parallel()

	tests := map[string]Definition{
		"missing identity": {ID: "orders", RendererID: RendererTanStack, Query: QueryBinding{}},
		"wrong renderer":   {ID: "orders", RendererID: RendererECharts, Query: QueryBinding{Kind: QueryDetail, ResultShape: ResultDetailWindow}, Spec: tableSpec()},
		"wrong query":      {ID: "orders", RendererID: RendererTanStack, Query: QueryBinding{Kind: QueryAggregate, ResultShape: ResultCategoryValue}, Spec: tableSpec()},
	}
	for name, definition := range tests {
		definition := definition
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := definition.Validate(); err == nil {
				t.Fatal("expected invalid definition to fail")
			}
		})
	}
}

func TestNewComputesRevisionAndSelectsOwnedRenderer(t *testing.T) {
	t.Parallel()

	definition, err := New("orders", tableSpec(), QueryBinding{
		Kind: QueryDetail, ResultShape: ResultDetailWindow, ModelID: "sales", DatasetID: "primary",
		Detail: &DetailQueryBinding{TableID: "orders", Fields: []FieldBinding{{FieldID: "orders.order_id", Alias: "order_id"}}, Limit: 1000},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if definition.RendererID != RendererTanStack {
		t.Fatalf("renderer = %q, want %q", definition.RendererID, RendererTanStack)
	}
	computed, err := ir.ComputeSpecRevision(definition.Spec)
	if err != nil {
		t.Fatalf("ComputeSpecRevision: %v", err)
	}
	if definition.SpecRevision != computed.String() {
		t.Fatalf("revision = %q, want %q", definition.SpecRevision, computed)
	}
}

func TestQueryBindingRejectsMissingAndConflictingBranches(t *testing.T) {
	t.Parallel()

	for name, binding := range map[string]QueryBinding{
		"missing branch": {Kind: QueryDetail, ResultShape: ResultDetailWindow, ModelID: "sales", DatasetID: "primary"},
		"conflicting branch": {
			Kind: QueryDetail, ResultShape: ResultDetailWindow, ModelID: "sales", DatasetID: "primary",
			Detail:    &DetailQueryBinding{TableID: "orders", Fields: []FieldBinding{{FieldID: "orders.id", Alias: "id"}}, Limit: 100},
			Aggregate: &AggregateQueryBinding{TableID: "orders", Metrics: []FieldBinding{{FieldID: "orders.count", Alias: "value"}}, Limit: 1},
		},
		"spatial tiles without coordinates": {
			Kind: QuerySpatial, ResultShape: ResultGeographicFeatures, ModelID: "sales", DatasetID: "primary",
			Spatial: &SpatialQueryBinding{
				TableID: "orders", Dimensions: []FieldBinding{{FieldID: "orders.state", Alias: "state"}},
				Tiles: &SpatialTileBinding{FeatureCap: 5000, MaximumBytes: 512 * 1024, MetatileSize: 4, CellRadius: 48, MaximumZoom: 18},
			},
		},
	} {
		binding := binding
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := binding.Validate(); err == nil {
				t.Fatal("expected invalid query binding to fail")
			}
		})
	}
}

func TestQueryBindingRejectsIncompatibleResultShape(t *testing.T) {
	t.Parallel()
	binding := QueryBinding{
		Kind: QueryDetail, ResultShape: ResultGeographicFeatures, ModelID: "sales", DatasetID: "primary",
		Detail: &DetailQueryBinding{TableID: "orders", Fields: []FieldBinding{{FieldID: "orders.id", Alias: "id"}}, Limit: 100},
	}
	if err := binding.Validate(); err == nil {
		t.Fatal("incompatible query kind and result shape passed validation")
	}
}

func TestQueryBindingRejectsIncompleteCompiledReferences(t *testing.T) {
	valid := func() QueryBinding {
		return QueryBinding{
			Kind: QueryAggregate, ResultShape: ResultCategoryValue, ModelID: "sales", DatasetID: "primary",
			Identity: []string{"orders.state"},
			Aggregate: &AggregateQueryBinding{
				TableID:    "orders",
				Dimensions: []FieldBinding{{FieldID: "orders.state", Alias: "state"}},
				Metrics:    []FieldBinding{{FieldID: "orders.revenue", Alias: "value"}},
				Sort:       []Sort{{FieldID: "value", Direction: "desc"}},
				Limit:      100,
			},
		}
	}

	tests := map[string]func(*QueryBinding){
		"invalid dimension": func(binding *QueryBinding) {
			binding.Aggregate.Dimensions[0].Alias = ""
		},
		"duplicate alias": func(binding *QueryBinding) {
			binding.Aggregate.Metrics[0].Alias = "state"
		},
		"unknown identity": func(binding *QueryBinding) {
			binding.Identity = []string{"orders.customer_id"}
		},
		"empty sort": func(binding *QueryBinding) {
			binding.Aggregate.Sort[0].FieldID = ""
		},
		"invalid sort direction": func(binding *QueryBinding) {
			binding.Aggregate.Sort[0].Direction = "sideways"
		},
		"missing time grain": func(binding *QueryBinding) {
			binding.Aggregate.Time = &TimeBinding{FieldID: "orders.created_at", Alias: "created_at"}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			binding := valid()
			mutate(&binding)
			if err := binding.Validate(); err == nil {
				t.Fatal("expected invalid compiled reference to fail")
			}
		})
	}
}

func TestDefinitionRejectsSortOutsideCompiledDataset(t *testing.T) {
	_, err := New("orders", tableSpec(), QueryBinding{
		Kind: QueryDetail, ResultShape: ResultDetailWindow, ModelID: "sales", DatasetID: "primary",
		Detail: &DetailQueryBinding{
			TableID: "orders",
			Fields:  []FieldBinding{{FieldID: "orders.order_id", Alias: "order_id"}},
			DefaultSort: []Sort{{
				FieldID: "missing", Direction: "asc",
			}},
			Limit: 100,
		},
	})
	if err == nil {
		t.Fatal("sort outside the compiled dataset passed validation")
	}
}

func TestQueryBindingValidatesEveryMatrixField(t *testing.T) {
	binding := QueryBinding{
		Kind: QueryMatrix, ResultShape: ResultMatrixWindow, ModelID: "sales", DatasetID: "primary",
		Matrix: &MatrixQueryBinding{
			TableID: "orders",
			Rows:    []FieldBinding{{FieldID: "orders.state", Alias: ""}},
			Metrics: []FieldBinding{{FieldID: "orders.revenue", Alias: "revenue"}},
			Limit:   100,
		},
	}
	if err := binding.Validate(); err == nil {
		t.Fatal("invalid matrix row binding passed validation")
	}
}

func TestGeographicDefinitionOwnsExplicitSpatialQuery(t *testing.T) {
	t.Parallel()

	binding := QueryBinding{
		Kind: QuerySpatial, ResultShape: ResultGeographicFeatures, ModelID: "sales", DatasetID: "primary", Identity: []string{"orders.order_id"},
		Spatial: &SpatialQueryBinding{
			TableID: "orders",
			Dimensions: []FieldBinding{
				{FieldID: "orders.order_id", Alias: "order_id"},
				{FieldID: "orders.latitude", Alias: "latitude"},
				{FieldID: "orders.longitude", Alias: "longitude"},
			},
			Metrics: []FieldBinding{{FieldID: "orders.revenue", Alias: "revenue"}},
			Tiles: &SpatialTileBinding{
				Latitude: FieldBinding{FieldID: "orders.latitude", Alias: "latitude"}, Longitude: FieldBinding{FieldID: "orders.longitude", Alias: "longitude"},
				FeatureCap: 5000, MaximumBytes: 512 * 1024, MetatileSize: 4, CellRadius: 48, MaximumZoom: 18, RawMinimumZoom: 10,
			},
		},
	}
	definition, err := New("orders", geographicSpec(), binding)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if definition.RendererID != RendererMapLibre || definition.Query.Kind != QuerySpatial || definition.Query.Spatial == nil {
		t.Fatalf("geographic ownership = %#v", definition)
	}
}

func tableSpec() ir.VisualizationSpec {
	return ir.VisualizationSpec{Value: &ir.TableVisualizationSpec{
		VisualizationSpecBase: ir.VisualizationSpecBase{
			Kind: "table", Title: "Orders",
			Datasets:      []ir.VisualizationDatasetSchema{{ID: "primary", Fields: []ir.VisualizationField{{ID: "order_id", Role: ir.VisualizationFieldRoleIdentity, DataType: ir.VisualizationDataTypeString, Label: "Order ID"}}}},
			DataBudget:    ir.VisualizationDataBudget{MaxRows: 1000, RequiredCompleteness: ir.VisualizationCompletenessPartial},
			Accessibility: ir.VisualizationAccessibility{Title: "Orders", Description: "Order details"}, Interactions: []ir.VisualizationInteraction{},
		},
		Kind: "table", Columns: []ir.TableVisualizationColumn{{Field: ir.VisualizationFieldRef{Dataset: "primary", Field: "order_id"}, Label: "Order ID"}},
		Presentation: ir.GridVisualizationPresentation{RowHeight: 32, ShowHeader: true},
	}}
}

func geographicSpec() ir.VisualizationSpec {
	latitude := ir.VisualizationField{ID: "latitude", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeDecimal, Nullable: true, Label: "Latitude"}
	longitude := ir.VisualizationField{ID: "longitude", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeDecimal, Nullable: true, Label: "Longitude"}
	return ir.VisualizationSpec{Value: &ir.GeographicVisualizationSpec{
		VisualizationSpecBase: ir.VisualizationSpecBase{
			Kind: "geographic", Title: "Orders", Datasets: []ir.VisualizationDatasetSchema{{ID: "primary", Fields: []ir.VisualizationField{latitude, longitude}}},
			DataBudget:    ir.VisualizationDataBudget{RequiredCompleteness: ir.VisualizationCompletenessComplete},
			Accessibility: ir.VisualizationAccessibility{Title: "Orders", Description: "Order locations"}, Interactions: []ir.VisualizationInteraction{},
		},
		Kind: "geographic",
		Layers: []ir.VisualizationGeographicLayer{{Value: &ir.VisualizationPointLayer{
			VisualizationGeographicLayerBase: ir.VisualizationGeographicLayerBase{ID: "orders", Kind: "point", Position: ir.VisualizationMapLayerPositionAboveLabels, Visibility: ir.VisualizationMapVisibility{MinimumZoom: 0, MaximumZoom: 22}},
			Kind:                             "point", Latitude: ir.VisualizationFieldRef{Dataset: "primary", Field: "latitude"}, Longitude: ir.VisualizationFieldRef{Dataset: "primary", Field: "longitude"},
			Size: ir.VisualizationMapSizeScale{MinimumRadius: 3, MaximumRadius: 20}, Color: ir.VisualizationMapColorScale{Kind: ir.VisualizationMapColorScaleKindSequential, Palette: "blues", NullColor: "#ccc"}, Stroke: ir.VisualizationMapStroke{Color: "#fff", Width: 1, Opacity: 1}, Cluster: ir.VisualizationMapCluster{Radius: 50, MaximumZoom: 14, MinimumPoints: 2}, Opacity: 1,
		}}},
		Presentation: ir.GeographicVisualizationPresentation{
			VisualizationPresentation: ir.VisualizationPresentation{
				Legend: ir.VisualizationLegendPositionHidden,
				LabelPolicy: ir.VisualizationLabelPolicy{
					Density: ir.VisualizationLabelDensityHidden, Priority: []ir.VisualizationLabelPriority{},
					MaxCharacters: 24, MinimumSpacing: 0, TooltipFallback: true,
				},
			}, Roam: true, Theme: ir.VisualizationMapThemeAuto, LabelDensity: ir.VisualizationMapLabelDensityNormal,
			Camera: ir.VisualizationMapCamera{Mode: ir.VisualizationMapCameraModeFitData, Padding: 20, MinimumZoom: 0, MaximumZoom: 22}, Controls: ir.VisualizationMapControls{},
		},
	}}
}
