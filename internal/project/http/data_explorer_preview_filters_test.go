package http

import (
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
	if query.Kind != dataquery.KindSemanticRows || !query.IncludeTotal || query.Offset != 0 || query.Limit != 100 {
		t.Fatalf("filtered query = %#v", query)
	}
	if len(query.Fields) != 2 || query.Fields[0].Field != "zip_geolocations.city" || query.Fields[0].Alias != "city" || len(query.Filters) != 1 || query.Filters[0].Field != filters[0].Field {
		t.Fatalf("filtered fields = %#v", query)
	}
	if len(query.Sort) != 1 || query.Sort[0].Field != "zip_geolocations.city" {
		t.Fatalf("filtered sort = %#v", query.Sort)
	}
}
