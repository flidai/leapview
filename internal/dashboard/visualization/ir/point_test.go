package ir

import (
	"strings"
	"testing"
)

func TestPointEnvelopeRejectsNullAndDuplicateStableIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rows [][]any
		want string
	}{
		{name: "null identity", rows: [][]any{{nil, 2.0, 80.0}}, want: "null identity"},
		{name: "duplicate identity", rows: [][]any{{"o-1", 2.0, 80.0}, {"o-1", 4.0, 120.0}}, want: "duplicate identity"},
		{name: "null coordinate", rows: [][]any{{"o-1", nil, 80.0}}, want: "null x or y"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			envelope := pointEnvelope(t, test.rows)
			err := ValidateEnvelope(envelope)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateEnvelope() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestPointEnvelopeAcceptsStableBivariateRows(t *testing.T) {
	t.Parallel()
	if err := ValidateEnvelope(pointEnvelope(t, [][]any{{"o-1", 2.0, 80.0}, {"o-2", 4.0, 120.0}})); err != nil {
		t.Fatalf("ValidateEnvelope() error = %v", err)
	}
}

func pointEnvelope(t *testing.T, rows [][]any) VisualizationEnvelope {
	t.Helper()
	ref := func(field string) VisualizationFieldRef {
		return VisualizationFieldRef{Dataset: "primary", Field: field}
	}
	base := VisualizationSpecBase{
		Kind: "point", Title: "Orders",
		Datasets: []VisualizationDatasetSchema{{ID: "primary", Fields: []VisualizationField{
			{ID: "order_id", Role: VisualizationFieldRoleIdentity, DataType: VisualizationDataTypeString, Nullable: true, Label: "Order"},
			{ID: "delivery_days", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeFloat, Nullable: true, Label: "Delivery days"},
			{ID: "revenue", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeFloat, Nullable: true, Label: "Revenue"},
		}}},
		DataBudget:    VisualizationDataBudget{MaxRows: 100, RequiredCompleteness: VisualizationCompletenessComplete},
		Accessibility: VisualizationAccessibility{Title: "Orders", Description: "Delivery and revenue by order"},
		Interactions:  []VisualizationInteraction{},
	}
	spec := VisualizationSpec{Value: &PointVisualizationSpec{
		VisualizationSpecBase: base, Kind: "point", Identity: []VisualizationFieldRef{ref("order_id")},
		X: ref("delivery_days"), Y: ref("revenue"),
		Presentation: PointVisualizationPresentation{
			VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionHidden),
			Overplot:                  VisualizationPointOverplotStrategyOpacity,
			Opacity:                   0.7,
			LargeMode:                 VisualizationPointLargeModeAutomatic,
			LargeThreshold:            2_000,
			Brush:                     []VisualizationPointBrushGesture{},
		},
	}}
	revision, err := ComputeSpecRevision(spec)
	if err != nil {
		t.Fatal(err)
	}
	state := InlineVisualizationDataState{
		VisualizationDataStateBase: VisualizationDataStateBase{Kind: "inline", SpecRevision: revision.String(), DataRevision: 1, Generation: 1},
		Kind:                       "inline",
		Datasets: []VisualizationInlineDataset{{
			ID: "primary", SpecRevision: revision.String(), DataRevision: 1, Generation: 1,
			Columns: []string{"order_id", "delivery_days", "revenue"}, Rows: rows, Completeness: VisualizationCompletenessComplete,
		}},
	}
	return VisualizationEnvelope{
		SchemaVersion: CurrentSchemaVersion, VisualID: "orders", RendererID: "echarts", SpecRevision: revision.String(),
		Spec: spec, DataRevision: 1, DataState: VisualizationDataState{Value: &state}, Selection: []VisualizationSelectionEntry{},
		Status: VisualizationStatus{Kind: VisualizationStatusKindReady}, Diagnostics: []VisualizationDiagnostic{},
	}
}
