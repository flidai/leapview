package http

import (
	"bytes"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestDashboardTableRowsetIsTypedPrecisionSafeAndCursorPaged(t *testing.T) {
	envelope := rowsetTestEnvelope([]visualizationir.VisualizationField{{ID: "order_id", DataType: visualizationir.VisualizationDataTypeInteger}, {ID: "amount", DataType: visualizationir.VisualizationDataTypeDecimal}}, [][]any{{int64(9007199254740993), "9007199254740993.125"}}, 2)
	response, err := dashboardVisualizationRowset(envelope, "a", 0, 1, "scope-a", "snapshot-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Columns) != 2 || response.Columns[0].Type != "int64" || response.Columns[1].Type != "decimal" {
		t.Fatalf("columns = %#v", response.Columns)
	}
	if len(response.Rows) != 1 || response.Rows[0][0] != "9007199254740993" || response.Rows[0][1] != "9007199254740993.125" || response.Page.NextCursor == "" {
		t.Fatalf("rowset = %#v", response)
	}
	if offset, err := decodeIndexCursor(response.Page.NextCursor, "scope-a", "snapshot-a"); err != nil || offset != 1 {
		t.Fatalf("cursor offset=%d err=%v", offset, err)
	}
}

func TestDashboardTableRowsetMapsFloatToFloat64(t *testing.T) {
	envelope := rowsetTestEnvelope([]visualizationir.VisualizationField{{ID: "ratio", DataType: visualizationir.VisualizationDataTypeFloat}}, [][]any{{float64(1.25)}}, 1)
	response, err := dashboardVisualizationRowset(envelope, "a", 0, 100, "scope-a", "snapshot-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Columns) != 1 || response.Columns[0].Type != "float64" {
		t.Fatalf("columns = %#v", response.Columns)
	}
	if len(response.Rows) != 1 || response.Rows[0][0] != "1.25" {
		t.Fatalf("rows = %#v", response.Rows)
	}
}

func TestDashboardTableArrowMatchesJSONAndCarriesSnapshotMetadata(t *testing.T) {
	envelope := rowsetTestEnvelope([]visualizationir.VisualizationField{{ID: "order_id", DataType: visualizationir.VisualizationDataTypeInteger}}, [][]any{{int64(9007199254740993)}}, 1)
	response, err := dashboardVisualizationRowset(envelope, "a", 0, 100, "scope-a", "snapshot-a")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := encodeDashboardTableArrow(response)
	if err != nil {
		t.Fatalf("encode Arrow: %v", err)
	}
	reader, err := ipc.NewReader(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("open Arrow: %v", err)
	}
	defer reader.Release()
	if snapshot, ok := reader.Schema().Metadata().GetValue("leapview.serving_snapshot"); !ok || snapshot != "snapshot-a" {
		t.Fatalf("snapshot metadata = %q, %v", snapshot, ok)
	}
	if !reader.Next() {
		t.Fatalf("read record: %v", reader.Err())
	}
	if got := reader.Record().Column(0).(*array.String).Value(0); got != response.Rows[0][0] {
		t.Fatalf("Arrow value=%q JSON=%q", got, response.Rows[0][0])
	}
}

func rowsetTestEnvelope(fields []visualizationir.VisualizationField, rows [][]any, available int64) visualizationir.VisualizationEnvelope {
	base := visualizationir.VisualizationSpecBase{Kind: "table", Title: "Orders"}
	state := visualizationir.WindowedVisualizationDataState{
		VisualizationDataStateBase: visualizationir.VisualizationDataStateBase{Kind: "windowed"}, Kind: "windowed",
		Schema: visualizationir.VisualizationDatasetSchema{ID: "primary", Fields: fields}, AvailableRows: available,
		Blocks: map[string]visualizationir.VisualizationWindowBlock{"a": {ID: "a", Rows: rows}},
	}
	return visualizationir.VisualizationEnvelope{VisualID: "orders", Spec: visualizationir.VisualizationSpec{Value: &visualizationir.TableVisualizationSpec{VisualizationSpecBase: base, Kind: "table"}}, DataState: visualizationir.VisualizationDataState{Value: &state}}
}
