package dashboard

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"testing"

	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestNormalizeProgressPercentKeepsThePublicSignalBounded(t *testing.T) {
	if progress := NormalizeProgressPercent(nil, true); progress == nil || *progress != 0 {
		t.Fatalf("planning progress = %v, want 0", progress)
	}
	if progress := NormalizeProgressPercent(nil, false); progress == nil || *progress != 100 {
		t.Fatalf("complete progress = %v, want 100", progress)
	}
	for _, test := range []struct {
		name  string
		value float64
		want  float64
	}{
		{name: "lower bound", value: -5, want: 0},
		{name: "middle", value: 37.5, want: 37.5},
		{name: "upper bound", value: 105, want: 100},
	} {
		t.Run(test.name, func(t *testing.T) {
			progress := NormalizeProgressPercent(&test.value, true)
			if progress == nil || *progress != test.want {
				t.Fatalf("progress = %v, want %v", progress, test.want)
			}
		})
	}
	notANumber := math.NaN()
	if progress := NormalizeProgressPercent(&notANumber, true); progress == nil || *progress != 0 {
		t.Fatalf("invalid progress = %v, want 0", progress)
	}
}

func TestTableRequestWithDefaultsClampsRuntimePolicy(t *testing.T) {
	request := TableRequest{
		Table:      "orders",
		Block:      "z",
		Start:      -20,
		Count:      TableMaxRequestCount + 500,
		RequestSeq: -8,
		Sort:       TableSort{Key: "revenue", Direction: "sideways"},
	}.WithDefaults()

	if request.Block != "all" {
		t.Fatalf("block = %q, want all", request.Block)
	}
	if request.Start != 0 {
		t.Fatalf("start = %d, want 0", request.Start)
	}
	if request.Count != TableMaxRequestCount {
		t.Fatalf("count = %d, want %d", request.Count, TableMaxRequestCount)
	}
	if request.RequestSeq != 0 {
		t.Fatalf("request seq = %d, want 0", request.RequestSeq)
	}
	if request.Sort.Direction != "desc" {
		t.Fatalf("sort direction = %q, want desc", request.Sort.Direction)
	}
}

func TestTableRequestResetRequestsInitialBlocks(t *testing.T) {
	request := TableRequest{
		Table:        "orders",
		Block:        "b",
		Start:        600,
		Count:        500,
		RequestSeq:   42,
		ResetVersion: 4,
		Sort:         TableSort{Key: "revenue", Direction: "asc"},
	}.Reset()

	if request.Block != "all" || request.Start != 0 || request.Count != TableChunkSize {
		t.Fatalf("reset request = %#v, want all at top with chunk size", request)
	}
	if request.ResetVersion != 5 {
		t.Fatalf("reset version = %d, want 5", request.ResetVersion)
	}
	if request.RequestSeq != 42 {
		t.Fatalf("request seq = %d, want 42", request.RequestSeq)
	}
	if request.Sort.Key != "revenue" || request.Sort.Direction != "asc" {
		t.Fatalf("reset sort = %#v, want preserved revenue asc", request.Sort)
	}
}

func TestApplyInteractionReplaceUpdatesOnlySourceSelection(t *testing.T) {
	filters := Filters{
		Selections: []InteractionSelection{
			interactionSelectionFixture("visual", "orders", "point_selection", entryFixture("orders.status", "delivered")),
			interactionSelectionFixture("visual", "orders_table", "row_selection", entryFixture("orders.order_id", "o1")),
		},
	}

	next := filters.ApplyInteraction(InteractionCommand{
		SourceKind:      "visual",
		SourceID:        "orders",
		InteractionKind: "point_selection",
		Action:          "replace",
		Mappings:        []InteractionCommandMapping{{Field: "orders.status", Value: "shipped", Label: "shipped"}},
	})

	if len(next.Selections) != 2 {
		t.Fatalf("selection count = %d, want 2: %#v", len(next.Selections), next.Selections)
	}
	if got := selectionValues(next, "visual", "orders", "orders.status"); len(got) != 1 || got[0] != "shipped" {
		t.Fatalf("orders visual values = %#v, want [shipped]", got)
	}
	if got := selectionValues(next, "visual", "orders_table", "orders.order_id"); len(got) != 1 || got[0] != "o1" {
		t.Fatalf("orders table values = %#v, want [o1]", got)
	}
}

func TestInteractionRevisionAdvancesOnlyWhenSelectionStateChanges(t *testing.T) {
	filters := Filters{}.WithDefaults()
	command := InteractionCommand{
		SourceKind: "visual", SourceID: "orders", InteractionKind: "point_selection", Action: "replace",
		Mappings: []InteractionCommandMapping{{Field: "orders.status", Value: "delivered"}},
	}
	selected := filters.ApplyInteraction(command)
	if selected.InteractionRevision != 1 {
		t.Fatalf("interaction revision = %d, want 1", selected.InteractionRevision)
	}
	unchanged := selected.ApplyInteraction(command)
	if unchanged.InteractionRevision != selected.InteractionRevision {
		t.Fatalf("unchanged replacement advanced revision from %d to %d", selected.InteractionRevision, unchanged.InteractionRevision)
	}
	cleared := unchanged.ApplyInteraction(InteractionCommand{
		SourceKind: "visual", SourceID: "orders", InteractionKind: "point_selection", Action: "clear",
	})
	if cleared.InteractionRevision != 2 {
		t.Fatalf("cleared interaction revision = %d, want 2", cleared.InteractionRevision)
	}
}

func TestInteractionSelectionActionConformanceFixture(t *testing.T) {
	var fixture struct {
		SchemaVersion   int    `json:"schemaVersion"`
		SourceKind      string `json:"sourceKind"`
		SourceID        string `json:"sourceId"`
		InteractionKind string `json:"interactionKind"`
		Field           string `json:"field"`
		Cases           []struct {
			Name  string `json:"name"`
			Steps []struct {
				Action string `json:"action"`
				Toggle bool   `json:"toggle"`
				Value  any    `json:"value"`
			} `json:"steps"`
			ExpectedValues []string `json:"expectedValues"`
		} `json:"cases"`
	}
	data, err := os.ReadFile("../../api/visualization/conformance/interactions/selection-actions.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SchemaVersion != 1 {
		t.Fatalf("fixture schema version = %d, want 1", fixture.SchemaVersion)
	}
	for _, test := range fixture.Cases {
		t.Run(test.Name, func(t *testing.T) {
			filters := Filters{}.WithDefaults()
			for _, step := range test.Steps {
				command := InteractionCommand{
					SourceKind: fixture.SourceKind, SourceID: fixture.SourceID,
					InteractionKind: fixture.InteractionKind, Action: step.Action, Toggle: step.Toggle,
				}
				if step.Action != "clear" {
					command.Mappings = []InteractionCommandMapping{{Field: fixture.Field, Value: step.Value}}
				}
				filters = filters.ApplyInteraction(command)
			}
			values := selectionValues(filters, fixture.SourceKind, fixture.SourceID, fixture.Field)
			if len(values) != len(test.ExpectedValues) {
				t.Fatalf("values = %#v, want %#v", values, test.ExpectedValues)
			}
			for index := range test.ExpectedValues {
				if values[index] != test.ExpectedValues[index] {
					t.Fatalf("values = %#v, want %#v", values, test.ExpectedValues)
				}
			}
		})
	}
}

func TestApplyInteractionClearsOnlySourceSelection(t *testing.T) {
	filters := Filters{
		Selections: []InteractionSelection{
			interactionSelectionFixture("visual", "orders", "point_selection", entryFixture("orders.status", "delivered")),
			interactionSelectionFixture("visual", "orders_table", "row_selection", entryFixture("orders.order_id", "o1")),
		},
	}

	next := filters.ApplyInteraction(InteractionCommand{
		SourceKind:      "visual",
		SourceID:        "orders",
		InteractionKind: "point_selection",
		Action:          "clear",
	})

	if len(next.Selections) != 1 {
		t.Fatalf("selection count = %d, want 1: %#v", len(next.Selections), next.Selections)
	}
	if got := selectionValues(next, "visual", "orders_table", "orders.order_id"); len(got) != 1 || got[0] != "o1" {
		t.Fatalf("remaining table values = %#v, want [o1]", got)
	}
}

func TestApplyInteractionReplaceStoresSingleSelectionAsFullTupleEntry(t *testing.T) {
	next := Filters{}.ApplyInteraction(InteractionCommand{
		SourceKind:      "visual",
		SourceID:        "state_status",
		InteractionKind: "point_selection",
		Action:          "replace",
		Mappings: []InteractionCommandMapping{
			{Field: "customers.state", Value: "SP", Label: "Sao Paulo"},
			{Field: "orders.status", Value: "delivered", Label: "Delivered"},
		},
	})

	if len(next.Selections) != 1 {
		t.Fatalf("selection count = %d, want 1: %#v", len(next.Selections), next.Selections)
	}
	selection := next.Selections[0]
	if len(selection.Entries) != 1 {
		t.Fatalf("entry count = %d, want 1: %#v", len(selection.Entries), selection.Entries)
	}
	entry := selection.Entries[0]
	if len(entry.Mappings) != 2 {
		t.Fatalf("entry mappings = %#v, want two tuple mappings", entry.Mappings)
	}
	if entry.Mappings[0].Field != "customers.state" || entry.Mappings[0].Value != "SP" {
		t.Fatalf("first tuple mapping = %#v, want customers.state=SP", entry.Mappings[0])
	}
	if entry.Mappings[1].Field != "orders.status" || entry.Mappings[1].Value != "delivered" {
		t.Fatalf("second tuple mapping = %#v, want orders.status=delivered", entry.Mappings[1])
	}
}

func TestApplyInteractionMultiTogglesExactTupleEntries(t *testing.T) {
	filters := Filters{}
	first := []InteractionCommandMapping{
		{Field: "customers.state", Value: "SP", Label: "SP"},
		{Field: "orders.status", Value: "delivered", Label: "Delivered"},
	}
	second := []InteractionCommandMapping{
		{Field: "customers.state", Value: "RJ", Label: "RJ"},
		{Field: "orders.status", Value: "shipped", Label: "Shipped"},
	}

	filters = filters.ApplyInteraction(interactionCommandFixture(first))
	filters = filters.ApplyInteraction(interactionCommandFixture(second))

	if len(filters.Selections) != 1 || len(filters.Selections[0].Entries) != 2 {
		t.Fatalf("selections = %#v, want one selection with two tuple entries", filters.Selections)
	}
	if got := selectionEntryValues(filters.Selections[0], "customers.state"); len(got) != 2 || got[0] != "SP" || got[1] != "RJ" {
		t.Fatalf("state tuple values = %#v, want [SP RJ]", got)
	}

	filters = filters.ApplyInteraction(interactionCommandFixture(first))
	if len(filters.Selections) != 1 || len(filters.Selections[0].Entries) != 1 {
		t.Fatalf("after exact tuple toggle selections = %#v, want one remaining tuple", filters.Selections)
	}
	remaining := filters.Selections[0].Entries[0]
	if got := tupleValue(remaining, "customers.state"); got != "RJ" {
		t.Fatalf("remaining tuple state = %q, want RJ", got)
	}
	if got := tupleValue(remaining, "orders.status"); got != "shipped" {
		t.Fatalf("remaining tuple status = %q, want shipped", got)
	}
}

func TestApplyInteractionSetAccumulatesEntries(t *testing.T) {
	filters := Filters{}
	filters = filters.ApplyInteraction(InteractionCommand{
		SourceKind:      "visual",
		SourceID:        "orders",
		InteractionKind: "point_selection",
		Action:          "set",
		Toggle:          true,
		Mappings:        []InteractionCommandMapping{{Field: "orders.status", Value: "delivered", Label: "delivered"}},
	})
	filters = filters.ApplyInteraction(InteractionCommand{
		SourceKind:      "visual",
		SourceID:        "orders",
		InteractionKind: "point_selection",
		Action:          "set",
		Toggle:          true,
		Mappings:        []InteractionCommandMapping{{Field: "orders.status", Value: "shipped", Label: "shipped"}},
	})

	if len(filters.Selections) != 1 {
		t.Fatalf("selection count = %d, want 1: %#v", len(filters.Selections), filters.Selections)
	}
	if got := selectionValues(filters, "visual", "orders", "orders.status"); len(got) != 2 || got[0] != "delivered" || got[1] != "shipped" {
		t.Fatalf("set values = %#v, want [delivered shipped]", got)
	}
}

func TestApplyInteractionReplaceReplacesEntries(t *testing.T) {
	filters := Filters{}.ApplyInteraction(InteractionCommand{
		SourceKind:      "visual",
		SourceID:        "orders_table",
		InteractionKind: "row_selection",
		Action:          "set",
		Toggle:          true,
		Mappings:        []InteractionCommandMapping{{Field: "orders.order_id", Value: "o1", Label: "o1"}},
	})
	filters = filters.ApplyInteraction(InteractionCommand{
		SourceKind:      "visual",
		SourceID:        "orders_table",
		InteractionKind: "row_selection",
		Action:          "set",
		Toggle:          true,
		Mappings:        []InteractionCommandMapping{{Field: "orders.order_id", Value: "o2", Label: "o2"}},
	})

	if got := selectionValues(filters, "visual", "orders_table", "orders.order_id"); len(got) != 2 || got[0] != "o1" || got[1] != "o2" {
		t.Fatalf("multi toggle table values = %#v, want [o1 o2]", got)
	}

	filters = filters.ApplyInteraction(InteractionCommand{
		SourceKind:      "visual",
		SourceID:        "orders_table",
		InteractionKind: "row_selection",
		Action:          "replace",
		Toggle:          false,
		Mappings:        []InteractionCommandMapping{{Field: "orders.order_id", Value: "o3", Label: "o3"}},
	})

	if len(filters.Selections) != 1 {
		t.Fatalf("selection count = %d, want 1: %#v", len(filters.Selections), filters.Selections)
	}
	if got := selectionValues(filters, "visual", "orders_table", "orders.order_id"); len(got) != 1 || got[0] != "o3" {
		t.Fatalf("replace table values = %#v, want [o3]", got)
	}
}

func TestApplyInteractionPreservesTypedScalarValuesAndCanonicalIdentity(t *testing.T) {
	filters := Filters{}
	for _, value := range []any{0.0, false, nil, "0"} {
		filters = filters.ApplyInteraction(InteractionCommand{
			SourceKind:      "visual",
			SourceID:        "typed",
			InteractionKind: "point_selection",
			Action:          "set",
			Toggle:          true,
			Mappings: []InteractionCommandMapping{{
				Field:   "rating_bucket",
				Dataset: "ratings",
				Grain:   "month",
				Value:   value,
			}},
		})
	}

	if got := len(filters.Selections[0].Entries); got != 4 {
		t.Fatalf("entry count = %d, want 4 typed identities: %#v", got, filters.Selections)
	}
	for index, want := range []any{0.0, false, nil, "0"} {
		mapping := filters.Selections[0].Entries[index].Mappings[0]
		if mapping.Field != "rating_bucket" || mapping.Dataset != "ratings" || mapping.Grain != "month" {
			t.Fatalf("mapping %d identity = %#v", index, mapping)
		}
		if fmt.Sprintf("%T:%v", mapping.Value, mapping.Value) != fmt.Sprintf("%T:%v", want, want) {
			t.Fatalf("mapping %d value = %T(%v), want %T(%v)", index, mapping.Value, mapping.Value, want, want)
		}
	}
}

func TestApplyInteractionIdentityIncludesDatasetAndGrain(t *testing.T) {
	filters := Filters{}
	for _, mapping := range []InteractionCommandMapping{
		{Field: "activity_date", Dataset: "ratings", Grain: "month", Value: "2026-01-01"},
		{Field: "activity_date", Dataset: "tags", Grain: "month", Value: "2026-01-01"},
		{Field: "activity_date", Dataset: "ratings", Grain: "year", Value: "2026-01-01"},
	} {
		filters = filters.ApplyInteraction(interactionCommandFixture([]InteractionCommandMapping{mapping}))
	}
	if got := len(filters.Selections[0].Entries); got != 3 {
		t.Fatalf("entry count = %d, want distinct dataset/grain identities: %#v", got, filters.Selections)
	}
}

func TestInteractionSelectionValueMatchesSemanticType(t *testing.T) {
	for _, test := range []struct {
		value    any
		typeName string
		want     bool
	}{
		{value: nil, typeName: "number", want: true},
		{value: 0.0, typeName: "number", want: true},
		{value: "0", typeName: "number", want: false},
		{value: false, typeName: "boolean", want: true},
		{value: "false", typeName: "boolean", want: false},
		{value: "2026-01-01", typeName: "date", want: true},
		{value: "2026-02-30", typeName: "date", want: false},
		{value: "not-a-date", typeName: "date", want: false},
		{value: 20260101, typeName: "date", want: false},
		{value: "2026-01-01T12:30:45Z", typeName: "timestamp", want: true},
		{value: "2026-01-01 12:30:45", typeName: "timestamp", want: true},
		{value: "2026-01-01", typeName: "timestamp", want: true},
		{value: "not-a-timestamp", typeName: "timestamp", want: false},
	} {
		if got := InteractionSelectionValueMatchesType(test.value, test.typeName); got != test.want {
			t.Fatalf("value %T(%v) matches %q = %t, want %t", test.value, test.value, test.typeName, got, test.want)
		}
	}
}

func TestInteractionSelectionValueMatchesGrainedTemporalLabels(t *testing.T) {
	for _, test := range []struct {
		value string
		grain string
		want  bool
	}{
		{value: "2026-02-03", grain: "day", want: true},
		{value: "2026-02-02", grain: "week", want: true},
		{value: "2026-02", grain: "month", want: true},
		{value: "2026-Q2", grain: "quarter", want: true},
		{value: "2026", grain: "year", want: true},
		{value: "2026-13", grain: "month", want: false},
		{value: "2026-Q5", grain: "quarter", want: false},
	} {
		if got := InteractionSelectionValueMatchesType(test.value, "timestamp", test.grain); got != test.want {
			t.Fatalf("value %q grain %q matches timestamp = %t, want %t", test.value, test.grain, got, test.want)
		}
	}
}

func TestInteractionCommandMappingDistinguishesOmittedValueFromExplicitNull(t *testing.T) {
	var command InteractionCommand
	if err := json.Unmarshal([]byte(`{
		"sourceKind":"visual",
		"sourceId":"chart",
		"interactionKind":"point_selection",
		"action":"set",
		"mappings":[
			{"field":"missing"},
			{"field":"explicit_null","value":null}
		]
	}`), &command); err != nil {
		t.Fatal(err)
	}
	if len(command.Mappings) != 2 {
		t.Fatalf("mappings = %#v", command.Mappings)
	}
	if command.Mappings[0].HasValue() {
		t.Fatal("omitted mapping value was treated as present")
	}
	if !command.Mappings[1].HasValue() || command.Mappings[1].Value != nil {
		t.Fatalf("explicit null mapping = %#v, want present null", command.Mappings[1])
	}
}

func TestFiltersApplySpatialInteractionReplacesAndClearsOnlyItsSource(t *testing.T) {
	box := visualizationir.VisualizationSpatialSelectionGeometry{Value: &visualizationir.VisualizationSpatialBoxSelection{
		VisualizationSpatialSelectionGeometryBase: visualizationir.VisualizationSpatialSelectionGeometryBase{Kind: "box"}, Kind: "box",
		Bounds: visualizationir.VisualizationSpatialBounds{West: -50, South: -25, East: -40, North: -15},
	}}
	radius := visualizationir.VisualizationSpatialSelectionGeometry{Value: &visualizationir.VisualizationSpatialRadiusSelection{
		VisualizationSpatialSelectionGeometryBase: visualizationir.VisualizationSpatialSelectionGeometryBase{Kind: "radius"}, Kind: "radius",
		Center: visualizationir.VisualizationSpatialCoordinate{Longitude: -46, Latitude: -23}, RadiusMeters: 10_000,
	}}
	filters := Filters{}.ApplySpatialInteraction(SpatialSelectionCommand{VisualID: "map-a", InteractionID: "spatial_selection", Action: "set", Geometry: box})
	filters = filters.ApplySpatialInteraction(SpatialSelectionCommand{VisualID: "map-b", InteractionID: "spatial_selection", Action: "set", Geometry: radius})
	filters = filters.ApplySpatialInteraction(SpatialSelectionCommand{VisualID: "map-a", InteractionID: "spatial_selection", Action: "set", Geometry: radius})
	if len(filters.SpatialSelections) != 2 || filters.SpatialSelections[0].VisualID != "map-b" || filters.SpatialSelections[1].VisualID != "map-a" {
		t.Fatalf("spatial selections = %#v", filters.SpatialSelections)
	}
	filters = filters.ApplySpatialInteraction(SpatialSelectionCommand{VisualID: "map-a", InteractionID: "spatial_selection", Action: "clear"})
	if len(filters.SpatialSelections) != 1 || filters.SpatialSelections[0].VisualID != "map-b" {
		t.Fatalf("spatial selection clear = %#v", filters.SpatialSelections)
	}
}

func interactionCommandFixture(mappings []InteractionCommandMapping) InteractionCommand {
	return InteractionCommand{
		SourceKind:      "visual",
		SourceID:        "state_status",
		InteractionKind: "point_selection",
		Action:          "set",
		Toggle:          true,
		Mappings:        mappings,
	}
}

func interactionSelectionFixture(sourceKind, sourceID, interactionKind string, entries ...InteractionSelectionEntry) InteractionSelection {
	return InteractionSelection{
		ID:              sourceKind + ":" + sourceID + ":" + interactionKind,
		SourceKind:      sourceKind,
		SourceID:        sourceID,
		InteractionKind: interactionKind,
		Entries:         append([]InteractionSelectionEntry{}, entries...),
	}
}

func entryFixture(field, value string) InteractionSelectionEntry {
	return InteractionSelectionEntry{
		Mappings: []InteractionSelectionMapping{{
			Field: field,
			Value: value,
			Label: value,
		}},
		Label: value,
	}
}

func selectionValues(filters Filters, sourceKind, sourceID, field string) []string {
	for _, selection := range filters.Selections {
		if selection.SourceKind != sourceKind || selection.SourceID != sourceID {
			continue
		}
		return selectionEntryValues(selection, field)
	}
	return nil
}

func selectionEntryValues(selection InteractionSelection, field string) []string {
	values := []string{}
	for _, entry := range selection.Entries {
		for _, mapping := range entry.Mappings {
			if mapping.Field == field {
				values = append(values, fmt.Sprint(mapping.Value))
			}
		}
	}
	return values
}

func tupleValue(entry InteractionSelectionEntry, field string) string {
	for _, mapping := range entry.Mappings {
		if mapping.Field == field {
			return fmt.Sprint(mapping.Value)
		}
	}
	return ""
}
