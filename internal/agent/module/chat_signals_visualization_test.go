package module

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/flidai/leapview/internal/agent"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestVisualizationEnvelopeNumericRowsRoundTripAsNativeScalars(t *testing.T) {
	envelope := numericVisualizationEnvelope(t)
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal visualization envelope: %v", err)
	}

	var roundTrip visualizationir.VisualizationEnvelope
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("unmarshal visualization envelope: %v", err)
	}
	assertNumericVisualizationRows(t, roundTrip)
	if err := visualizationir.ValidateEnvelope(roundTrip); err != nil {
		t.Fatalf("ValidateEnvelope(roundTrip): %v", err)
	}
}

func TestTypedChatArtifactsRetainsNumericVisualizationAfterJSONRoundTrip(t *testing.T) {
	envelope := numericVisualizationEnvelope(t)
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal visualization artifact: %v", err)
	}
	var artifact map[string]any
	if err := json.Unmarshal(encoded, &artifact); err != nil {
		t.Fatalf("unmarshal visualization artifact: %v", err)
	}

	visuals := TypedChatArtifacts(agent.ChatArtifactSignals{
		Visuals: map[string]any{envelope.VisualID: artifact},
	})
	got, ok := visuals[envelope.VisualID]
	if !ok {
		t.Fatalf("TypedChatArtifacts omitted valid visual %q", envelope.VisualID)
	}
	assertNumericVisualizationRows(t, got)
}

func numericVisualizationEnvelope(t *testing.T) visualizationir.VisualizationEnvelope {
	t.Helper()
	fixture, err := os.ReadFile("../../../api/visualization/conformance/cartesian-inline.json")
	if err != nil {
		t.Fatalf("read visualization fixture: %v", err)
	}
	var envelope visualizationir.VisualizationEnvelope
	if err := json.Unmarshal(fixture, &envelope); err != nil {
		t.Fatalf("decode visualization fixture: %v", err)
	}

	spec, ok := envelope.Spec.Value.(*visualizationir.CartesianVisualizationSpec)
	if !ok {
		t.Fatalf("fixture spec type = %T, want cartesian", envelope.Spec.Value)
	}
	// Keep both numeric cases in the same inline dataset so the test exercises
	// the scalar types used by ValidateEnvelope after JSON decoding.
	spec.Datasets[0].Fields[0].DataType = visualizationir.VisualizationDataTypeInteger
	spec.Datasets[0].Fields[0].Time = nil
	spec.Datasets[0].Fields[1].DataType = visualizationir.VisualizationDataTypeFloat
	spec.Datasets[0].Fields[1].Format = nil
	envelope.Selection = []visualizationir.VisualizationSelectionEntry{}
	state, ok := envelope.DataState.Value.(*visualizationir.InlineVisualizationDataState)
	if !ok {
		t.Fatalf("fixture state type = %T, want inline", envelope.DataState.Value)
	}
	state.Datasets[0].Rows = [][]any{
		{int64(17), float32(2.5)},
		{int64(23), float32(7)},
	}

	revision, err := visualizationir.ComputeSpecRevision(envelope.Spec)
	if err != nil {
		t.Fatalf("compute visualization spec revision: %v", err)
	}
	envelope.SpecRevision = revision.String()
	state.SpecRevision = revision.String()
	state.Datasets[0].SpecRevision = revision.String()
	if err := visualizationir.ValidateEnvelope(envelope); err != nil {
		t.Fatalf("ValidateEnvelope(source): %v", err)
	}
	return envelope
}

func assertNumericVisualizationRows(t *testing.T, envelope visualizationir.VisualizationEnvelope) {
	t.Helper()
	state, ok := envelope.DataState.Value.(*visualizationir.InlineVisualizationDataState)
	if !ok || len(state.Datasets) != 1 {
		t.Fatalf("round-tripped state = %#v, want one inline dataset", envelope.DataState.Value)
	}
	rows := state.Datasets[0].Rows
	want := [][]float64{{17, 2.5}, {23, 7}}
	if len(rows) != len(want) {
		t.Fatalf("round-tripped row count = %d, want %d", len(rows), len(want))
	}
	for rowIndex, row := range rows {
		if len(row) != len(want[rowIndex]) {
			t.Fatalf("round-tripped row %d width = %d, want %d", rowIndex, len(row), len(want[rowIndex]))
		}
		for columnIndex, value := range row {
			number, ok := value.(float64)
			if !ok {
				t.Fatalf("round-tripped row %d column %d type = %T, want float64", rowIndex, columnIndex, value)
			}
			if number != want[rowIndex][columnIndex] {
				t.Fatalf("round-tripped row %d column %d = %v, want %v", rowIndex, columnIndex, number, want[rowIndex][columnIndex])
			}
		}
	}
}
