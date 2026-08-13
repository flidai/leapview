package ir

import (
	"encoding/json"
	"math"
	"testing"
)

func TestSpecificationBasePreservesVariantKindAcrossJSONRoundTrip(t *testing.T) {
	spec := VisualizationSpec{Value: &KPIVisualizationSpec{
		VisualizationSpecBase: VisualizationSpecBase{Kind: "kpi", Title: "Orders"},
		Kind:                  "kpi",
	}}
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var decoded VisualizationSpec
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	base, err := SpecificationBase(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if base.Kind != "kpi" {
		t.Fatalf("kind = %q, want kpi", base.Kind)
	}
}

func TestValidateEnvelopeRejectsInvalidInlineFrames(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*VisualizationEnvelope){
		"unknown column": func(envelope *VisualizationEnvelope) {
			state := envelope.DataState.Value.(*InlineVisualizationDataState)
			state.Datasets[0].Columns[0] = "missing"
		},
		"row width mismatch": func(envelope *VisualizationEnvelope) {
			state := envelope.DataState.Value.(*InlineVisualizationDataState)
			state.Datasets[0].Rows[0] = state.Datasets[0].Rows[0][:1]
		},
		"non finite decimal": func(envelope *VisualizationEnvelope) {
			state := envelope.DataState.Value.(*InlineVisualizationDataState)
			state.Datasets[0].Rows[0][1] = math.Inf(1)
		},
		"row budget exceeded": func(envelope *VisualizationEnvelope) {
			spec := envelope.Spec.Value.(*CartesianVisualizationSpec)
			spec.DataBudget.MaxRows = 1
			state := envelope.DataState.Value.(*InlineVisualizationDataState)
			state.Datasets[0].Rows = append(state.Datasets[0].Rows, state.Datasets[0].Rows[0])
		},
	}

	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			envelope := readEnvelopeFixture(t, "cartesian-inline.json")
			mutate(&envelope)
			if err := ValidateEnvelope(envelope); err == nil {
				t.Fatal("expected invalid frame to fail")
			}
		})
	}
}

func TestValidateEnvelopeAcceptsConformanceFixtures(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"cartesian-inline.json", "table-windowed.json"} {
		envelope := readEnvelopeFixture(t, name)
		if err := ValidateEnvelope(envelope); err != nil {
			t.Fatalf("ValidateEnvelope(%s): %v", name, err)
		}
	}
}

func TestValidateSpecEnforcesGeographicLayerRequirements(t *testing.T) {
	base := VisualizationSpecBase{
		Kind: "geographic", Title: "Stores",
		Datasets: []VisualizationDatasetSchema{{ID: "primary", Fields: []VisualizationField{
			{ID: "lat", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeDecimal, Label: "Latitude"},
			{ID: "lon", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeDecimal, Label: "Longitude"},
		}}},
		DataBudget:    VisualizationDataBudget{MaxRows: 100, RequiredCompleteness: VisualizationCompletenessComplete},
		Accessibility: VisualizationAccessibility{Title: "Stores", Description: "Store locations"}, Interactions: []VisualizationInteraction{},
	}
	latitude := VisualizationFieldRef{Dataset: "primary", Field: "lat"}
	longitude := VisualizationFieldRef{Dataset: "primary", Field: "lon"}
	layerBase := VisualizationGeographicLayerBase{ID: "stores", Kind: "point", Tooltip: []VisualizationFieldRef{}, Position: VisualizationMapLayerPositionBelowLabels, Visibility: VisualizationMapVisibility{MaximumZoom: 24}}
	point := VisualizationSpec{Value: &GeographicVisualizationSpec{VisualizationSpecBase: base, Kind: "geographic", Layers: []VisualizationGeographicLayer{{Value: &VisualizationPointLayer{VisualizationGeographicLayerBase: layerBase, Kind: "point", Latitude: latitude, Longitude: longitude, Size: VisualizationMapSizeScale{MinimumRadius: 5, MaximumRadius: 28}, Cluster: VisualizationMapCluster{Radius: 50, MinimumPoints: 2}}}}, Presentation: GeographicVisualizationPresentation{VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionHidden)}}}
	if err := ValidateSpec(point); err != nil {
		t.Fatalf("point layer: %v", err)
	}
	point.Value.(*GeographicVisualizationSpec).Layers[0].Value.(*VisualizationPointLayer).Longitude = VisualizationFieldRef{}
	if err := ValidateSpec(point); err == nil {
		t.Fatal("point layer without longitude was accepted")
	}
	join := VisualizationFieldRef{Dataset: "primary", Field: "lat"}
	choropleth := VisualizationSpec{Value: &GeographicVisualizationSpec{VisualizationSpecBase: base, Kind: "geographic", Layers: []VisualizationGeographicLayer{{Value: &VisualizationChoroplethLayer{VisualizationGeographicLayerBase: VisualizationGeographicLayerBase{ID: "states", Kind: "choropleth", Tooltip: []VisualizationFieldRef{}, Position: VisualizationMapLayerPositionBelowLabels, Visibility: VisualizationMapVisibility{MaximumZoom: 24}}, Kind: "choropleth", Join: join}}}, Presentation: GeographicVisualizationPresentation{VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionHidden)}}}
	if err := ValidateSpec(choropleth); err == nil {
		t.Fatal("choropleth layer without geometry was accepted")
	}
}

func TestValidateEnvelopeEnforcesSpatialWindowInvariants(t *testing.T) {
	fields := []VisualizationField{
		{ID: "lat", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeDecimal, Label: "Latitude"},
		{ID: "lon", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeDecimal, Label: "Longitude"},
	}
	base := VisualizationSpecBase{Kind: "geographic", Title: "Stores", Datasets: []VisualizationDatasetSchema{{ID: "primary", Fields: fields}}, DataBudget: VisualizationDataBudget{MaxRows: 1_000_000, RequiredCompleteness: VisualizationCompletenessPartial}, Accessibility: VisualizationAccessibility{Title: "Stores", Description: "Stores"}, Interactions: []VisualizationInteraction{}}
	layerBase := VisualizationGeographicLayerBase{ID: "stores", Kind: "point", Tooltip: []VisualizationFieldRef{}, Position: VisualizationMapLayerPositionBelowLabels, Visibility: VisualizationMapVisibility{MaximumZoom: 24}}
	spec := VisualizationSpec{Value: &GeographicVisualizationSpec{VisualizationSpecBase: base, Kind: "geographic", Layers: []VisualizationGeographicLayer{{Value: &VisualizationPointLayer{VisualizationGeographicLayerBase: layerBase, Kind: "point", Latitude: VisualizationFieldRef{Dataset: "primary", Field: "lat"}, Longitude: VisualizationFieldRef{Dataset: "primary", Field: "lon"}, Size: VisualizationMapSizeScale{MinimumRadius: 5, MaximumRadius: 28}, Cluster: VisualizationMapCluster{Radius: 50, MinimumPoints: 2}}}}, Presentation: GeographicVisualizationPresentation{VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionHidden), Theme: VisualizationMapThemeAuto, LabelDensity: VisualizationMapLabelDensityNormal, Camera: VisualizationMapCamera{Mode: VisualizationMapCameraModeFitData, MaximumZoom: 14}}}}
	revision, err := ComputeSpecRevision(spec)
	if err != nil {
		t.Fatal(err)
	}
	state := SpatialWindowedVisualizationDataState{VisualizationDataStateBase: VisualizationDataStateBase{Kind: "spatial_windowed", SpecRevision: revision.String(), DataRevision: 3, Generation: 1}, Kind: "spatial_windowed", Schema: base.Datasets[0], Cardinality: VisualizationCardinality{Kind: VisualizationCardinalityKindUnknown}, Extent: VisualizationSpatialBounds{West: 170, South: -40, East: -170, North: 20}, RowCap: 1_000_000, FeatureCap: 5000, Window: &VisualizationSpatialWindowBlock{ID: "z4-a", Bounds: VisualizationSpatialBounds{West: 170, South: -30, East: -175, North: 10}, Zoom: 4, Width: 800, Height: 500, Precision: VisualizationSpatialPrecisionAggregated, Rows: [][]any{{-20.0, 175.0}}, RequestSeq: 2}}
	envelope := VisualizationEnvelope{SchemaVersion: CurrentSchemaVersion, VisualID: "stores", RendererID: "maplibre", SpecRevision: revision.String(), DataRevision: 3, Spec: spec, DataState: VisualizationDataState{Value: &state}, Selection: []VisualizationSelectionEntry{}, Status: VisualizationStatus{Kind: VisualizationStatusKindReady}, Diagnostics: []VisualizationDiagnostic{}}
	if err := ValidateEnvelope(envelope); err != nil {
		t.Fatalf("valid spatial envelope: %v", err)
	}
	state.Window.RequestSeq = 0
	if err := ValidateEnvelope(envelope); err == nil {
		t.Fatal("non-positive spatial request sequence accepted")
	}
	state.Window.RequestSeq = 2
	state.Window.Width = 16_385
	if err := ValidateEnvelope(envelope); err == nil {
		t.Fatal("oversized spatial viewport accepted")
	}
	state.Window.Width = 800
	state.Window.Rows = append(state.Window.Rows, []any{-10.0, 176.0})
	state.FeatureCap = 1
	if err := ValidateEnvelope(envelope); err == nil {
		t.Fatal("spatial feature cap overflow accepted")
	}
}

func TestValidateEnvelopeAcceptsRowFreeSpatialTiledState(t *testing.T) {
	fields := []VisualizationField{
		{ID: "lat", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeDecimal, Label: "Latitude"},
		{ID: "lon", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeDecimal, Label: "Longitude"},
		{ID: "revenue", Role: VisualizationFieldRoleMeasure, DataType: VisualizationDataTypeDecimal, Label: "Revenue"},
	}
	base := VisualizationSpecBase{Kind: "geographic", Title: "Stores", Datasets: []VisualizationDatasetSchema{{ID: "primary", Fields: fields}}, DataBudget: VisualizationDataBudget{MaxRows: 0, RequiredCompleteness: VisualizationCompletenessComplete}, Accessibility: VisualizationAccessibility{Title: "Stores", Description: "Stores"}, Interactions: []VisualizationInteraction{}}
	layerBase := VisualizationGeographicLayerBase{ID: "stores", Kind: "point", Tooltip: []VisualizationFieldRef{}, Position: VisualizationMapLayerPositionBelowLabels, Visibility: VisualizationMapVisibility{MaximumZoom: 18}}
	spec := VisualizationSpec{Value: &GeographicVisualizationSpec{VisualizationSpecBase: base, Kind: "geographic", Layers: []VisualizationGeographicLayer{{Value: &VisualizationPointLayer{VisualizationGeographicLayerBase: layerBase, Kind: "point", Latitude: VisualizationFieldRef{Dataset: "primary", Field: "lat"}, Longitude: VisualizationFieldRef{Dataset: "primary", Field: "lon"}, Size: VisualizationMapSizeScale{MinimumRadius: 5, MaximumRadius: 28}, Cluster: VisualizationMapCluster{Radius: 48, MinimumPoints: 2}}}}, Presentation: GeographicVisualizationPresentation{VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionHidden), Theme: VisualizationMapThemeAuto, LabelDensity: VisualizationMapLabelDensityNormal, Camera: VisualizationMapCamera{Mode: VisualizationMapCameraModeFitData, MaximumZoom: 14}}}}
	revision, err := ComputeSpecRevision(spec)
	if err != nil {
		t.Fatal(err)
	}
	count, minimum, maximum, total := int64(14_876), 1.0, 250.0, 2_000_000.0
	state := SpatialTiledVisualizationDataState{
		VisualizationDataStateBase: VisualizationDataStateBase{Kind: "spatial_tiled", SpecRevision: revision.String(), DataRevision: 3, Generation: 1},
		Kind:                       "spatial_tiled", Schema: base.Datasets[0], Cardinality: VisualizationCardinality{Kind: VisualizationCardinalityKindExact, Count: &count},
		Extent:           VisualizationSpatialBounds{West: -74, South: -34, East: -34, North: 6},
		RawDomains:       []VisualizationSpatialScaleDomain{{Field: "revenue", Minimum: &minimum, Maximum: &maximum, Total: &total}},
		AggregateDomains: []VisualizationSpatialScaleDomain{{Field: "revenue", Minimum: &minimum, Maximum: &total, Total: &total}},
		TileURL:          "/workspaces/sales/dashboards/orders/visuals/stores/tiles/rev/{z}/{x}/{y}.mvt",
		MinimumZoom:      0, MaximumZoom: 18, RawMinimumZoom: 10, FeatureCap: 5000, MaximumTileBytes: 512 * 1024,
	}
	envelope := VisualizationEnvelope{SchemaVersion: CurrentSchemaVersion, VisualID: "stores", RendererID: "maplibre", SpecRevision: revision.String(), DataRevision: 3, Spec: spec, DataState: VisualizationDataState{Value: &state}, Selection: []VisualizationSelectionEntry{}, Status: VisualizationStatus{Kind: VisualizationStatusKindReady}, Diagnostics: []VisualizationDiagnostic{}}
	if err := ValidateEnvelope(envelope); err != nil {
		t.Fatalf("valid spatial tiled envelope: %v", err)
	}
	state.RawMinimumZoom = state.MaximumZoom + 1
	if err := ValidateEnvelope(envelope); err != nil {
		t.Fatalf("aggregate-only spatial tiled envelope: %v", err)
	}
	state.RawMinimumZoom++
	if err := ValidateEnvelope(envelope); err == nil {
		t.Fatal("spatial tiled state accepted a raw transition beyond the aggregate-only sentinel")
	}
	state.RawMinimumZoom = 10
	state.TileURL = "/tiles/rev/{z}/{x}.mvt"
	if err := ValidateEnvelope(envelope); err == nil {
		t.Fatal("spatial tiled state without a complete XYZ URL was accepted")
	}
}

func TestValidateEnvelopeEnforcesHierarchySemantics(t *testing.T) {
	t.Parallel()

	valid := hierarchyEnvelope(t, VisualizationHierarchyMarkTree, []VisualizationField{
		{ID: "node", Role: VisualizationFieldRoleIdentity, DataType: VisualizationDataTypeString, Label: "Node"},
		{ID: "parent", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Nullable: true, Label: "Parent"},
		{ID: "value", Role: VisualizationFieldRoleMeasure, DataType: VisualizationDataTypeDecimal, Label: "Value"},
	}, [][]any{{"Americas", nil, 10.0}, {"Springfield", "Americas", 3.0}, {"Europe", nil, 5.0}, {"Springfield", "Europe", 5.0}})
	if err := ValidateEnvelope(valid); err != nil {
		t.Fatalf("valid hierarchy: %v", err)
	}

	tests := map[string][][]any{
		"duplicate node identity": {{"Americas", nil, 10.0}, {"Americas", nil, 5.0}},
		"missing parent":          {{"Springfield", "Americas", 3.0}},
	}
	for name, rows := range tests {
		t.Run(name, func(t *testing.T) {
			envelope := hierarchyEnvelope(t, VisualizationHierarchyMarkTree, []VisualizationField{
				{ID: "node", Role: VisualizationFieldRoleIdentity, DataType: VisualizationDataTypeString, Label: "Node"},
				{ID: "parent", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Nullable: true, Label: "Parent"},
				{ID: "value", Role: VisualizationFieldRoleMeasure, DataType: VisualizationDataTypeDecimal, Label: "Value"},
			}, rows)
			if err := ValidateEnvelope(envelope); err == nil {
				t.Fatal("expected invalid hierarchy data to fail")
			}
		})
	}
}

func TestValidateEnvelopeRejectsInvalidNetworkEndpoints(t *testing.T) {
	t.Parallel()

	envelope := hierarchyEnvelope(t, VisualizationHierarchyMarkGraph, []VisualizationField{
		{ID: "source", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Source"},
		{ID: "target", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Target"},
		{ID: "value", Role: VisualizationFieldRoleMeasure, DataType: VisualizationDataTypeDecimal, Label: "Value"},
	}, [][]any{{"orders", "", 3.0}})
	if err := ValidateEnvelope(envelope); err == nil {
		t.Fatal("expected an empty graph endpoint to fail")
	}
}

func hierarchyEnvelope(t *testing.T, mark VisualizationHierarchyMark, fields []VisualizationField, rows [][]any) VisualizationEnvelope {
	t.Helper()
	base := VisualizationSpecBase{
		Kind: "hierarchy", Title: "Hierarchy", Datasets: []VisualizationDatasetSchema{{ID: "primary", Fields: fields}},
		DataBudget:    VisualizationDataBudget{MaxRows: 100, RequiredCompleteness: VisualizationCompletenessComplete},
		Accessibility: VisualizationAccessibility{Title: "Hierarchy", Description: "Hierarchy data"}, Interactions: []VisualizationInteraction{},
	}
	field := func(id string) *VisualizationFieldRef { return &VisualizationFieldRef{Dataset: "primary", Field: id} }
	specification := &HierarchyVisualizationSpec{VisualizationSpecBase: base, Kind: "hierarchy", Mark: mark, Node: *field(fields[0].ID), Value: field("value"), Presentation: HierarchyVisualizationPresentation{VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionHidden)}}
	if mark == VisualizationHierarchyMarkGraph || mark == VisualizationHierarchyMarkSankey {
		specification.Source, specification.Target = field("source"), field("target")
	} else {
		specification.Parent = field("parent")
	}
	spec := VisualizationSpec{Value: specification}
	revision, err := ComputeSpecRevision(spec)
	if err != nil {
		t.Fatal(err)
	}
	columns := make([]string, len(fields))
	for index, item := range fields {
		columns[index] = item.ID
	}
	state := InlineVisualizationDataState{VisualizationDataStateBase: VisualizationDataStateBase{Kind: "inline", SpecRevision: revision.String(), DataRevision: 1, Generation: 1}, Kind: "inline", Datasets: []VisualizationInlineDataset{{ID: "primary", SpecRevision: revision.String(), DataRevision: 1, Generation: 1, Columns: columns, Rows: rows, Completeness: VisualizationCompletenessComplete}}}
	return VisualizationEnvelope{SchemaVersion: CurrentSchemaVersion, VisualID: "hierarchy", RendererID: "echarts", SpecRevision: revision.String(), DataRevision: 1, Spec: spec, DataState: VisualizationDataState{Value: &state}, Selection: []VisualizationSelectionEntry{}, Status: VisualizationStatus{Kind: VisualizationStatusKindReady}, Diagnostics: []VisualizationDiagnostic{}}
}
