package compiler

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestLowerDashboardQueryRecordsRejectsQualifiedPhysicalFields(t *testing.T) {
	for _, field := range []string{"orders.id", "related.id"} {
		t.Run(field, func(t *testing.T) {
			query := document.DashboardQuery{Value: &document.RecordsDashboardQuery{
				Type: "records", Dataset: "orders",
				Fields: []document.DashboardRecordFieldSelection{{String: stringPtr(field)}},
			}}
			if _, err := LowerDashboardQuery(query, dashboardQueryTestModel(), "sales"); err == nil || !strings.Contains(err.Error(), "unqualified") {
				t.Fatalf("qualified records field %q error = %v, want unqualified-field rejection", field, err)
			}
		})
	}
}
