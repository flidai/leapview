package datastar

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboardcompiler "github.com/flidai/leapview/internal/dashboard/compiler"
	dashboardstream "github.com/flidai/leapview/internal/dashboard/stream"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
)

func testVisualDefinition(t *testing.T, id string) visualizationdefinition.Definition {
	t.Helper()
	definitions, err := dashboardcompiler.CompileVisualizationDefinitions(&dashboardauthoring.Dashboard{
		ID: "test", SemanticModel: "model",
		Visuals: dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{id: {Type: "bar", Title: id, Query: dashboardauthoring.VisualQuery{Table: "table", Measures: []dashboardauthoring.FieldRef{{Field: "measure"}}}}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return definitions[id]
}

func testTableDefinition(t *testing.T, id string, table dashboard.Table) visualizationdefinition.Definition {
	t.Helper()
	if len(table.Columns) == 0 {
		table.Columns = []dashboard.TableColumn{{Key: "value", Label: "Value"}}
	}
	fields := make([]string, len(table.Columns))
	for index, column := range table.Columns {
		fields[index] = column.Key
	}
	definitions, err := dashboardcompiler.CompileVisualizationDefinitions(&dashboardauthoring.Dashboard{
		ID: "test", SemanticModel: "model",
		Visuals: dashboardauthoring.TabularVisualizations("table", map[string]dashboardauthoring.TableVisual{id: {
			Title: table.Title, Columns: table.Columns, DefaultSort: table.Sort, Style: table.Style,
			Query: dashboardauthoring.TableQuery{Table: "table", Fields: fields},
		}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return definitions[id]
}

func testVisualEnvelope(t *testing.T, id string, dataRevision, generation int64) visualizationir.VisualizationEnvelope {
	t.Helper()
	envelope, err := visualizationruntime.EnvelopeFromFrame(testVisualDefinition(t, id), visualizationruntime.Frame{Columns: []string{"label", "value"}, Rows: [][]any{}}, nil, dataRevision, generation)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func testTableEnvelope(t *testing.T, id string, table dashboard.Table, dataRevision, generation int64) visualizationir.VisualizationEnvelope {
	t.Helper()
	envelope, err := visualizationruntime.WindowEnvelopeFromDefinition(testTableDefinition(t, id, table), table, dataRevision, generation)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func TestLoadingPatch(t *testing.T) {
	status, ok := LoadingPatch()["status"].(map[string]any)
	if !ok || status["loading"] != true {
		t.Fatalf("loading patch = %#v", LoadingPatch())
	}
	if _, exists := status["dataDirectory"]; exists {
		t.Fatalf("loading patch exposes dataDirectory: %#v", status)
	}
	progress, ok := status["progressPercent"].(*float64)
	if !ok || progress == nil || *progress != 0 {
		t.Fatalf("loading progress = %#v, want 0", status["progressPercent"])
	}
}

func TestRefreshCompletePreservesFatalError(t *testing.T) {
	patch := RefreshEventPatch(dashboardstream.RefreshEvent{
		Type: dashboardstream.RefreshEventComplete, RefreshID: "refresh-1", Generation: 1, Err: errors.New("refresh failed"),
	})
	status, ok := patch["status"].(map[string]any)
	if !ok || status["loading"] != false || status["error"] != "refresh failed" {
		t.Fatalf("terminal patch = %#v", patch)
	}
}

func TestRefreshProgressIsBackendOwned(t *testing.T) {
	for _, test := range []struct {
		name    string
		event   dashboardstream.RefreshEvent
		percent *float64
	}{
		{
			name: "planning",
			event: dashboardstream.RefreshEvent{
				Type: dashboardstream.RefreshEventStart, Generation: 4,
			},
			percent: testPercent(0),
		},
		{
			name: "executing",
			event: dashboardstream.RefreshEvent{
				Type: dashboardstream.RefreshEventProgress, Generation: 4,
				ProgressPercent: testPercent(50),
			},
			percent: testPercent(50),
		},
		{
			name: "complete",
			event: dashboardstream.RefreshEvent{
				Type: dashboardstream.RefreshEventComplete, Generation: 4,
				ProgressPercent: testPercent(100),
			},
			percent: testPercent(100),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			patch := RefreshEventPatch(test.event)
			status, ok := patch["status"].(map[string]any)
			progress, progressOK := status["progressPercent"].(*float64)
			if !ok || !progressOK || !equalOptionalPercent(progress, test.percent) {
				t.Fatalf("progress patch = %#v", patch)
			}
			if _, legacy := status["progress"]; legacy {
				t.Fatalf("progress patch retains legacy work counters: %#v", patch)
			}
		})
	}
}

func testPercent(value float64) *float64 { return &value }

func equalOptionalPercent(left, right *float64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func TestRefreshEventEnvelopeCarriesExplicitDeliveryMetadata(t *testing.T) {
	tests := []struct {
		name          string
		event         dashboardstream.RefreshEvent
		wantBoundary  bool
		wantGroup     string
		wantMergeRoot string
	}{
		{
			name: "progress boundary",
			event: dashboardstream.RefreshEvent{
				Type: dashboardstream.RefreshEventProgress, RefreshID: "refresh-9", Generation: 9,
			},
			wantBoundary: true,
		},
		{
			name: "visual result batch",
			event: dashboardstream.RefreshEvent{
				Type: dashboardstream.RefreshEventVisual, RefreshID: "refresh-9", Generation: 9, Target: "rating_count", Value: testVisualEnvelope(t, "rating_count", 1, 9),
			},
			wantGroup:     "dashboard-results",
			wantMergeRoot: "visuals",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			envelope := RefreshEventEnvelope(test.event)
			if envelope.Delivery.Generation != 9 || envelope.Delivery.Boundary != test.wantBoundary || envelope.Delivery.CoalesceGroup != test.wantGroup {
				t.Fatalf("delivery metadata = %#v", envelope.Delivery)
			}
			if envelope.Trace.Origin != "dashboard.refresh" || envelope.Trace.CorrelationID != "refresh-9" {
				t.Fatalf("trace metadata = %#v", envelope.Trace)
			}
			if test.wantMergeRoot != "" && !slices.Contains(envelope.Delivery.MergeRoots, test.wantMergeRoot) {
				t.Fatalf("merge roots = %#v", envelope.Delivery.MergeRoots)
			}
		})
	}
}

func TestVisualMetadataUpdatesDataWithoutChangingComponentStatus(t *testing.T) {
	table := dashboard.Table{Title: "Orders", Cardinality: dashboard.ExactCardinality(42)}
	patch := RefreshEventPatch(dashboardstream.RefreshEvent{
		Type: dashboardstream.RefreshEventVisualMetadata, Target: "orders", Value: testTableEnvelope(t, "orders", table, 1, 1),
	})
	visuals, ok := patch["visuals"].(map[string]VisualizationSignal)
	var dataState visualizationir.VisualizationDataState
	if ok {
		if err := json.Unmarshal([]byte(visuals["orders"].DataState.Payload), &dataState); err != nil {
			t.Fatal(err)
		}
	}
	state, stateOK := dataState.Value.(*visualizationir.WindowedVisualizationDataState)
	if !ok || !stateOK || state.Cardinality.Count == nil || *state.Cardinality.Count != 42 || state.Cardinality.Kind != visualizationir.VisualizationCardinalityKindExact {
		t.Fatalf("metadata patch = %#v", patch)
	}
	if _, ok := patch["componentStatus"]; ok {
		t.Fatalf("metadata patch changed target status: %#v", patch)
	}
}

func TestVisualizationEnvelopeUsesStreamOwnedRevisionAndStatus(t *testing.T) {
	patch := RefreshEventPatch(dashboardstream.RefreshEvent{
		Type: dashboardstream.RefreshEventVisual, Target: "orders", Generation: 7, DataRevision: 11,
		Value: testVisualEnvelope(t, "orders", 11, 7),
	})
	visuals, ok := patch["visuals"].(map[string]VisualizationSignal)
	if !ok {
		t.Fatalf("visual patch = %#v", patch)
	}
	envelope := visuals["orders"]
	if envelope.DataRevision != 11 || envelope.Status.Kind != visualizationir.VisualizationStatusKindNoData {
		t.Fatalf("visual envelope = %#v", envelope)
	}
	if _, legacy := patch["componentStatus"]; legacy {
		t.Fatalf("visual patch retains component status: %#v", patch)
	}

	loading := RefreshEventPatch(dashboardstream.RefreshEvent{Type: dashboardstream.RefreshEventStart, Generation: 8, Targets: []string{"visual:orders"}})
	loadingVisuals, ok := loading["visuals"].(map[string]any)
	if !ok {
		t.Fatalf("loading patch = %#v", loading)
	}
	orders, ok := loadingVisuals["orders"].(map[string]any)
	if !ok || orders["status"].(map[string]any)["kind"] != "loading" {
		t.Fatalf("loading visualization status = %#v", loadingVisuals)
	}
	if _, legacy := loading["componentStatus"]; legacy {
		t.Fatalf("loading patch retains component status: %#v", loading)
	}
}

type setupRequiredPatchError struct{}

func (setupRequiredPatchError) Error() string       { return "source data is missing" }
func (setupRequiredPatchError) SetupRequired() bool { return true }

func TestRefreshCompleteMarksSetupRequiredErrors(t *testing.T) {
	patch := RefreshEventPatch(dashboardstream.RefreshEvent{
		Type: dashboardstream.RefreshEventComplete, RefreshID: "refresh-1", Generation: 1, Err: setupRequiredPatchError{},
	})
	status, ok := patch["status"].(map[string]any)
	if !ok || status["setupRequired"] != true || status["error"] != "source data is missing" {
		t.Fatalf("terminal patch = %#v", patch)
	}
}

func TestDashboardViewStreamIDIsStableAcrossPageNavigation(t *testing.T) {
	overview := StreamID("client", "dashboard", "overview", "view-1")
	details := StreamID("client", "dashboard", "details", "view-1")
	if overview != details {
		t.Fatalf("dashboard view stream IDs differ across pages: %q != %q", overview, details)
	}
}
