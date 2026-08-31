package application

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestRecordDetailFieldIDForValidationQualifiesAgainstRecordsDataset(t *testing.T) {
	field := "customer_id"
	doc := document.DashboardDocument{Spec: document.DashboardSpec{Visuals: map[string]document.DashboardVisual{
		"orders": {Query: document.DashboardQuery{Value: &document.RecordsDashboardQuery{
			Type: "records", Dataset: "sales_orders",
			Fields: []document.DashboardRecordFieldSelection{{String: &field}},
		}}},
	}}}

	model := &semanticmodel.Model{Tables: map[string]semanticmodel.Table{
		"sales_orders":    {Dimensions: map[string]semanticmodel.MetricDimension{"customer_id": {}}},
		"sales_customers": {Dimensions: map[string]semanticmodel.MetricDimension{"customer_id": {}}},
	}}

	qualified := recordDetailFieldIDForValidation(doc, "orders", " customer_id ", authoring.FieldRoleDetail)
	if qualified != "sales_orders.customer_id" {
		t.Fatalf("validation field ID = %q, want sales_orders.customer_id", qualified)
	}
	if err := validateGovernedField(model, qualified, authoring.FieldRoleDetail); err != nil {
		t.Fatalf("qualified records detail rejected: %v", err)
	}

	for _, test := range []struct {
		name    string
		fieldID string
		role    authoring.FieldRole
		want    string
	}{
		{name: "already qualified", fieldID: "sales_orders.customer_id", role: authoring.FieldRoleDetail, want: "sales_orders.customer_id"},
		{name: "pending dataset", fieldID: "customer_id", role: authoring.FieldRoleDetail, want: "customer_id"},
		{name: "aggregate role", fieldID: "customer_id", role: authoring.FieldRoleDimension, want: "customer_id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			testDoc := doc
			if test.name == "pending dataset" {
				testDoc.Spec.Visuals = map[string]document.DashboardVisual{
					"orders": {Query: document.DashboardQuery{Value: &document.RecordsDashboardQuery{Type: "records", Dataset: "pending_dataset"}}},
				}
			}
			if got := recordDetailFieldIDForValidation(testDoc, "orders", test.fieldID, test.role); got != test.want {
				t.Fatalf("validation field ID = %q, want %q", got, test.want)
			}
		})
	}
}
