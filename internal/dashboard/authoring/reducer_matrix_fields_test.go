package authoring

import (
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestCanonicalMatrixAssignmentFillsBothAxes(t *testing.T) {
	_, revision := canonicalReducerFixture(t)
	revision.Document.Spec.Visuals["base"] = defaultCanonicalVisual(string(document.DashboardVisualTypeMatrix), "Matrix")
	for _, field := range []struct {
		id   string
		role FieldRole
	}{{"category", FieldRoleDimension}, {"purchase_month", FieldRoleDimension}, {"revenue", FieldRoleMetric}} {
		if err := assignCanonicalField(&revision.Document, AssignFieldPayload{PageID: "overview", VisualID: "base-component", FieldID: field.id, Role: field.role}); err != nil {
			t.Fatal(err)
		}
	}
	query := revision.Document.Spec.Visuals["base"].Query.Value.(*document.PivotDashboardQuery)
	if len(query.Rows) != 1 || len(query.Columns) != 1 || len(query.Metrics) != 1 {
		t.Fatalf("matrix fields = rows:%#v columns:%#v metrics:%#v", query.Rows, query.Columns, query.Metrics)
	}
}
