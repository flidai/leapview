package builderview

import (
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestProjectCanonicalInteractionProjectionClassifiesAuthoredModes(t *testing.T) {
	field, targetDataset, label := "status", "orders", "Status"
	targets, highlights, none := []string{"target"}, []string{"highlight"}, []string{"none"}
	selection := document.SelectionDashboardInteraction{
		DashboardInteractionBase: document.DashboardInteractionBase{Type: "selection", Targets: &targets},
		Type:                     "selection", Mode: document.DashboardSelectionModeMultiple, Toggle: false,
		Mappings:         []document.DashboardInteractionMapping{{Field: field, Value: field, Dataset: &targetDataset, Label: &label}},
		HighlightTargets: &highlights, NoneTargets: &none,
	}
	visual := document.DashboardVisual{Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{Type: "aggregate"}}, Interactions: &[]document.DashboardInteraction{{Value: &selection}}}
	projected, err := projectCanonicalInteraction(visual)
	if err != nil {
		t.Fatal(err)
	}
	if projected == nil || !projected.Configured || !projected.Editable || projected.Mode == nil || *projected.Mode != "multiple" || projected.Toggle || len(projected.Mappings) != 1 {
		t.Fatalf("selection projection = %#v", projected)
	}
	if projected.Mappings[0].Field != field || projected.Mappings[0].Value != field || projected.Mappings[0].Dataset == nil || *projected.Mappings[0].Dataset != targetDataset || projected.Mappings[0].Label == nil || *projected.Mappings[0].Label != label {
		t.Fatalf("mapping projection = %#v", projected.Mappings[0])
	}
	if !reflect.DeepEqual(projected.Targets, targets) || !reflect.DeepEqual(projected.HighlightTargets, highlights) || !reflect.DeepEqual(projected.NoneTargets, none) {
		t.Fatalf("target projection = %#v", projected)
	}
}

func TestProjectCanonicalInteractionProjectionInfersOnlyStableQueryFields(t *testing.T) {
	field := "status"
	tests := []struct {
		name       string
		query      document.DashboardQuery
		configured bool
		expected   bool
	}{
		{name: "aggregate dimension", query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{String: &field}}}}, expected: true},
		{name: "pivot row", query: document.DashboardQuery{Value: &document.PivotDashboardQuery{Type: "pivot", Rows: []document.DashboardDimensionSelection{{String: &field}}}}, expected: true},
		{name: "records field", query: document.DashboardQuery{Value: &document.RecordsDashboardQuery{Type: "records", Dataset: "orders", Fields: []document.DashboardRecordFieldSelection{{String: &field}}}}, expected: true},
		{name: "metric only", query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{Type: "aggregate"}}, expected: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projected, err := projectCanonicalInteraction(document.DashboardVisual{Query: test.query})
			if err != nil {
				t.Fatal(err)
			}
			if test.expected && (projected == nil || projected.Configured != test.configured || projected.Editable != test.expected) {
				t.Fatalf("projection = %#v", projected)
			}
			if !test.expected && projected != nil {
				t.Fatalf("unsupported projection = %#v, want omitted", projected)
			}
			if test.expected && (len(projected.Mappings) != 1 || projected.Mappings[0].Field != field || projected.Mappings[0].Value != field) {
				t.Fatalf("inferred mapping = %#v", projected.Mappings)
			}
		})
	}
}

func TestProjectCanonicalInteractionProjectionDisablesMultipleAndSpatial(t *testing.T) {
	selection := document.DashboardInteraction{Value: &document.SelectionDashboardInteraction{DashboardInteractionBase: document.DashboardInteractionBase{Type: "selection"}, Type: "selection", Mode: document.DashboardSelectionModeSingle, Toggle: true, Mappings: []document.DashboardInteractionMapping{}}}
	for name, interactions := range map[string][]document.DashboardInteraction{
		"multiple": {selection, selection},
		"spatial":  {{Value: &document.SpatialSelectionDashboardInteraction{DashboardInteractionBase: document.DashboardInteractionBase{Type: "spatialSelection"}, Type: "spatialSelection"}}},
	} {
		t.Run(name, func(t *testing.T) {
			visual := document.DashboardVisual{Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{Type: "aggregate"}}, Interactions: &interactions}
			projected, err := projectCanonicalInteraction(visual)
			if err != nil {
				t.Fatal(err)
			}
			if projected == nil || !projected.Configured || projected.Editable || projected.Message == nil || strings.TrimSpace(*projected.Message) == "" {
				t.Fatalf("unsupported projection = %#v", projected)
			}
		})
	}
}
