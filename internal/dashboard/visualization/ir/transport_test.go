package ir

import (
	"encoding/json"
	"testing"
)

func TestEncodeDataStateTransportCarriesClosedRevisionHeader(t *testing.T) {
	state := SpatialTiledVisualizationDataState{
		VisualizationDataStateBase: VisualizationDataStateBase{Kind: "spatial_tiled", SpecRevision: "sha256:test", DataRevision: 7, Generation: 9},
		Kind:                       "spatial_tiled", Schema: VisualizationDatasetSchema{ID: "primary", Fields: []VisualizationField{}},
		Cardinality: VisualizationCardinality{Kind: VisualizationCardinalityKindUnknown},
		Extent:      VisualizationSpatialBounds{West: -1, South: -1, East: 1, North: 1}, TileURL: "/tiles/test/{z}/{x}/{y}.mvt", FeatureCap: 5, MaximumTileBytes: 512 * 1024, MaximumZoom: 18,
	}
	transport, err := EncodeDataStateTransport(VisualizationDataState{Value: &state})
	if err != nil {
		t.Fatal(err)
	}
	if transport.SchemaVersion != 1 || transport.Encoding != "json" || transport.Kind != "spatial_tiled" || transport.SpecRevision != "sha256:test" || transport.DataRevision != 7 || transport.Generation != 9 {
		t.Fatalf("transport header = %#v", transport)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(transport.Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["kind"] != "spatial_tiled" || payload["dataRevision"] != float64(7) {
		t.Fatalf("transport payload = %#v", payload)
	}
}
