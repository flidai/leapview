package tools

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestValidateAgentRecordsFieldsRequiresExplicitPhysicalFields(t *testing.T) {
	for _, name := range []string{"revenue", "order_id"} {
		visual := document.DashboardVisual{Query: document.DashboardQuery{Value: &document.RecordsDashboardQuery{
			Type: "records", Dataset: "orders",
			Fields: []document.DashboardRecordFieldSelection{{String: &name}},
		}}}
		if err := validateAgentRecordsFields(visual); err == nil || !strings.Contains(err.Error(), "explicit physical field") || strings.Contains(err.Error(), name) {
			t.Fatalf("records shorthand %q error = %v, want non-enumerating physical field guidance", name, err)
		}
		visual.Query.Value.(*document.RecordsDashboardQuery).Fields = []document.DashboardRecordFieldSelection{{Reference: &document.DashboardRecordFieldReference{Field: name}}}
		if err := validateAgentRecordsFields(visual); err != nil {
			t.Fatalf("explicit physical records field %q: %v", name, err)
		}
	}
}
