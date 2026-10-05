package http

import (
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func TestDataExplorerPreviewFiltersUseGovernedRowsAndFilteredTotal(t *testing.T) {
	executor := &browserDataQueryStub{result: dataquery.Result{
		Rows: []dataquery.Row{{"city": "sao paulo", "state": "SP"}}, TotalRows: 1, TotalRowsKnown: true,
	}}
	columns := []projectsignals.DataPreviewColumnSignal{{Key: "city"}, {Key: "state"}}
	object := projectsignals.DataExplorerObjectSignal{
		Key: "model:zip", ResourceID: "model:zip", Layer: "model",
		SemanticModelID: projectsignals.Pointer("semantic-model:visuals"), DatasetID: projectsignals.Pointer("zip_geolocations"), Columns: &columns,
	}
	filters := []dataquery.Filter{{Field: "zip_geolocations.state", Operator: "equals", Values: []any{"SP"}}}
	preview := dataExplorerPreviewWithFilters(t.Context(), executor, "project:test", object, projectsignals.DataExplorerCommand{
		ObjectKey: projectsignals.Pointer(object.Key), Count: 100, Limit: 100, Block: projectsignals.Pointer("all"),
		Sort: projectsignals.DataPreviewSortSignal{Column: projectsignals.Pointer("city"), Direction: projectsignals.Pointer("asc")},
	}, filters)
	if preview.Error != nil || preview.TotalRows != 1 || len(preview.Blocks["a"].Rows) != 1 {
		t.Fatalf("filtered preview = %#v", preview)
	}
	query := executor.query
	// The three table blocks share one governed query and one filtered total.
	if query.Kind != dataquery.KindSemanticRows || !query.IncludeTotal || query.Offset != 0 || query.Limit != 300 {
		t.Fatalf("filtered query = %#v", query)
	}
	if len(query.Fields) != 2 || query.Fields[0].Field != "zip_geolocations.city" || query.Fields[0].Alias != "city" || len(query.Filters) != 1 || query.Filters[0].Field != filters[0].Field {
		t.Fatalf("filtered fields = %#v", query)
	}
	if len(query.Sort) != 1 || query.Sort[0].Field != "zip_geolocations.city" {
		t.Fatalf("filtered sort = %#v", query.Sort)
	}
}

func TestDataExplorerPreviewBudgetFallbackRetainsFilters(t *testing.T) {
	for _, reason := range []dataquery.ResultLimitReason{dataquery.ResultRows, dataquery.ResultBytes} {
		t.Run(string(reason), func(t *testing.T) {
			ctx := t.Context()
			executor := &performancePreviewExecutor{ctx: ctx, known: true, total: 250, maxRows: 200, limitReason: reason}
			columns := []projectsignals.DataPreviewColumnSignal{{Key: "id"}, {Key: "status"}}
			object := performancePreviewObject()
			object.Columns = &columns
			filters := []dataquery.Filter{{Field: "orders.status", Dataset: "orders", Operator: "equals", Values: []any{"paid"}}}
			preview := dataExplorerPreviewWithFilters(ctx, executor, "project:test", object, projectsignals.DataExplorerCommand{
				Count: 100, Block: projectsignals.Pointer("all"),
				Sort: projectsignals.DataPreviewSortSignal{Column: projectsignals.Pointer("id"), Direction: projectsignals.Pointer("asc")},
			}, filters)
			if preview.Error != nil || len(executor.queries) != 4 || preview.TotalRows != 250 || len(preview.Blocks["c"].Rows) != 50 {
				t.Fatalf("filtered fallback=%#v queries=%#v", preview, executor.queries)
			}
			combined := executor.queries[0]
			if combined.Kind != dataquery.KindSemanticRows || combined.Limit != 300 || !combined.IncludeTotal || !reflect.DeepEqual(combined.Filters, filters) {
				t.Fatalf("combined filtered query=%#v", combined)
			}
			if combined.Target != "orders" || combined.ModelID != "semantic:sales" || combined.ProjectID != "project:test" || combined.Surface != dataquery.SurfaceDataExplorer || combined.Operation != dataquery.OperationPreviewWindow {
				t.Fatalf("filtered query governance metadata=%#v", combined)
			}
			// A budget fallback may change only the window and total request;
			// each block retains the exact governed filters, fields, sort, and metadata.
			for index, query := range executor.queries[1:] {
				want := combined
				want.Offset, want.Limit, want.IncludeTotal = index*100, 100, index == 0
				if !reflect.DeepEqual(query, want) {
					t.Fatalf("fallback block %d query=%#v, want=%#v", index, query, want)
				}
			}
		})
	}
}
