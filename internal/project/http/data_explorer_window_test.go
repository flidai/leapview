package http

import (
	"fmt"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func TestDataExplorerSemanticWindowsReachBeyondLegacyLimit(t *testing.T) {
	model := browserSemanticExploreTestModel()
	for id, table := range model.Tables {
		table.GrainEntity = "record"
		table.Entities = map[string]semanticmodel.EntityDefinition{"record": {Type: "primary", Fields: []string{"id"}}}
		model.Tables[id] = table
	}
	dataset := "orders"
	for _, block := range []string{"a", "all"} {
		t.Run(block, func(t *testing.T) {
			rows := make([]dataquery.Row, 100)
			for i := range rows {
				rows[i] = dataquery.Row{"status": "paid"}
			}
			executor := &browserDataQueryStub{result: dataquery.Result{Columns: []dataquery.Column{{Name: "status"}}, Rows: rows, TotalRows: 2345, TotalRowsKnown: true}}
			command := projectsignals.DataExploreCommand{Spec: exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "sales", DatasetID: &dataset, Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}}, Metrics: []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{{Field: "orders.status", Direction: "asc"}}, Limit: 1000}, RequestSeq: 12, ResetVersion: 2, Window: &projectsignals.DataExploreWindowCommand{Start: 1500, Count: 100, Block: block, RequestSeq: 7, ResetVersion: 2}}
			_, result := dataExplorerSemanticResult(t.Context(), executor, testDataExplorerQueryLowerer, "project:test", command, []projectsignals.DataExploreFieldSignal{{ID: "orders.status", Kind: "dimension", DatasetID: "orders", Compatible: true}}, model, mustCompileDataExplorerModel(t, model))
			if result.Error != nil {
				t.Fatal(*result.Error)
			}
			offset, limit := 1500, 100
			if block == "all" {
				offset, limit = 1400, 300
			}
			if executor.query.Offset != offset || executor.query.Limit != limit || !executor.query.IncludeTotal {
				t.Fatalf("window query=%#v", executor.query)
			}
			if result.Window == nil || result.Window.TotalRows != 2345 || result.Window.AvailableRows != 2345 || projectsignals.ValueOrZero(result.Window.TotalRowLabel) != "2345" {
				t.Fatalf("total=%#v", result.Window)
			}
			if got := result.Window.Blocks["a"]; got.Start != int64(offset) || got.RequestSeq != 7 || got.ResetVersion != 2 || len(got.Rows) != 100 {
				t.Fatalf("window=%#v", got)
			}
		})
	}
}

func TestDataExplorerTableWindowsPreserveAuthoredChartLimit(t *testing.T) {
	model := browserSemanticExploreTestModel()
	for id, table := range model.Tables {
		table.GrainEntity = "record"
		table.Entities = map[string]semanticmodel.EntityDefinition{"record": {Type: "primary", Fields: []string{"id"}}}
		model.Tables[id] = table
	}
	model.Metrics["revenue"] = model.Metrics["orders"]
	fields := explorerVisualizationTestFields()
	rows := make([]dataquery.Row, 300)
	for i := range rows {
		rows[i] = dataquery.Row{"status": fmt.Sprintf("status-%03d", i), "revenue": float64(300 - i)}
	}
	for _, sorted := range []bool{false, true} {
		t.Run(fmt.Sprintf("sorted=%v", sorted), func(t *testing.T) {
			spec := explorerVisualizationTestSpec()
			spec.Limit = 10
			spec.Sort = []exploration.ExplorationSort{{Field: "revenue", Direction: "desc"}}
			command := projectsignals.DataExploreCommand{Spec: spec, RequestSeq: 7, ResetVersion: 2}
			if sorted {
				command.Window = &projectsignals.DataExploreWindowCommand{Block: "all", Start: 0, Count: 100, RequestSeq: 3, ResetVersion: 2}
			}
			executor := &browserDataQueryStub{result: dataquery.Result{Columns: []dataquery.Column{{Name: "status"}, {Name: "revenue"}}, Rows: rows, TotalRows: 2345, TotalRowsKnown: true}}
			_, result := dataExplorerSemanticResult(t.Context(), executor, testDataExplorerQueryLowerer, "project:test", command, fields, model, mustCompileDataExplorerModel(t, model))
			if result.Error != nil {
				t.Fatal(*result.Error)
			}
			if executor.query.Limit != 300 || result.Window.TotalRows != 2345 {
				t.Fatalf("table limit/total=%d/%d", executor.query.Limit, result.Window.TotalRows)
			}
			loaded := 0
			for _, block := range result.Window.Blocks {
				loaded += len(block.Rows)
			}
			if loaded != 300 {
				t.Fatalf("native table loaded %d rows, want 300", loaded)
			}
			views, _, warnings := ProjectDataExplorerViews(spec, result, fields)
			chart, ok := views["chart"].DataState.Value.(*visualizationir.InlineVisualizationDataState)
			if !ok || len(chart.Datasets) != 1 || len(chart.Datasets[0].Rows) != 10 {
				t.Fatalf("authored top10 changed: chart=%#v warnings=%v", chart, warnings)
			}
			if chart.Datasets[0].Completeness != visualizationir.VisualizationCompletenessTruncated {
				t.Fatalf("chart sample completeness=%s", chart.Datasets[0].Completeness)
			}
			if len(result.Rows) != 300 || len(result.Window.Blocks["c"].Rows) != 100 {
				t.Fatal("chart projection mutated native table window")
			}
		})
	}
}
