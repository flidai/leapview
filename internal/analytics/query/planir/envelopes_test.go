package planir

import (
	"strings"
	"testing"
)

func envelopeGraph(operation SpatialEnvelopeOperation) *Graph {
	graph := validPlan()
	meta := NodeMeta{NodeID: "spatial", FilterPhase: FilterPhasePostAggregate, RootDatasets: []string{"orders"}, AvailableFields: []Field{{Name: "tile"}}, AvailableMetrics: nil}
	graph.Nodes[meta.NodeID] = SpatialEnvelope{
		NodeMeta: meta, Operation: operation, Input: graph.Output,
		Latitude: "id", Longitude: "id", Metrics: []string{"revenue"},
		Properties: []SpatialProperty{{Name: "id", Source: "id", Type: "string"}},
		Identity:   []string{"id"}, Zoom: 2, TargetZoom: 3, CellPixels: 64, Buffer: 64, FeatureCap: 10, MaximumBytes: 1024,
	}
	graph.Output = meta.NodeID
	graph.NodeMeta = meta
	return graph
}

func TestSpatialEnvelopeValidationExplainAndCanonical(t *testing.T) {
	graph := envelopeGraph(SpatialEnvelopeTileRaw)
	if err := graph.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	explain, err := graph.Explain()
	if err != nil {
		t.Fatalf("Explain() error = %v", err)
	}
	if !strings.Contains(explain, "spatial [SpatialEnvelope]") || !strings.Contains(explain, "operation=tile_raw") {
		t.Fatalf("Explain() = %s", explain)
	}
	first, err := graph.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	second, err := graph.Fingerprint()
	if err != nil || first != second {
		t.Fatalf("fingerprint is unstable: %s %s (%v)", first, second, err)
	}
	changed := envelopeGraph(SpatialEnvelopeTileBudget)
	changed.Nodes["spatial"] = func() Node {
		n := changed.Nodes["spatial"].(SpatialEnvelope)
		return n
	}()
	changedNode := changed.Nodes["spatial"].(SpatialEnvelope)
	changedNode.Operation = SpatialEnvelopeTileBudget
	changed.Nodes["spatial"] = changedNode
	if other, err := changed.Fingerprint(); err != nil || other == first {
		t.Fatalf("envelope operation did not affect fingerprint: %s %v", other, err)
	}
}

func TestSpatialEnvelopeValidationRejectsMalformedOperation(t *testing.T) {
	graph := envelopeGraph(SpatialEnvelopeOperation("not_supported"))
	if err := graph.Validate(); err == nil || !strings.Contains(err.Error(), "unsupported spatial envelope operation") {
		t.Fatalf("Validate() error = %v, want unsupported operation", err)
	}
}

func TestAnalyticalEnvelopeRendererUsesClosedNode(t *testing.T) {
	graph := validPlan()
	meta := NodeMeta{NodeID: "histogram", FilterPhase: FilterPhasePostAggregate, RootDatasets: []string{"orders"}, AvailableFields: []Field{{Name: "bucket"}, {Name: "count"}, {Name: "start"}, {Name: "end"}}}
	graph.Nodes[meta.NodeID] = AnalyticalEnvelope{NodeMeta: meta, Operation: AnalyticalEnvelopeHistogram, Input: graph.Output, Value: "revenue", ValueType: "decimal", BinCount: 4}
	graph.Output, graph.NodeMeta = meta.NodeID, meta
	rendered, err := RenderDuckDB(graph)
	if err != nil {
		t.Fatalf("RenderDuckDB() error = %v", err)
	}
	if !strings.Contains(rendered.SQL, "quantile") && !strings.Contains(rendered.SQL, "FLOOR") {
		t.Fatalf("histogram SQL missing typed envelope body: %s", rendered.SQL)
	}
}
