package application

import (
	"reflect"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestResolveVisualTypeFieldBindingsMapsOnlyExactGovernedEquivalents(t *testing.T) {
	model := &semanticmodel.Model{
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"category": {Bindings: map[string]semanticmodel.DimensionBinding{"sales_orders": {Field: "sales_orders.category"}}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"revenue":     {Dataset: "sales_orders", Input: &semanticmodel.MetricInput{Field: "sales_orders.revenue"}},
			"order_count": {Dataset: "sales_orders", Input: &semanticmodel.MetricInput{Field: "sales_orders.order_id"}},
		},
	}
	revenue, orderID, customerID := "revenue", "order_id", "customer_id"
	visual := document.DashboardVisual{Query: document.DashboardQuery{Value: &document.RecordsDashboardQuery{
		Type: "records", Dataset: "sales_orders",
		Fields: []document.DashboardRecordFieldSelection{{String: &revenue}, {String: &orderID}, {String: &customerID}},
	}}}

	got := resolveVisualTypeFieldBindings(model, visual)
	want := authoring.VisualTypeFieldBindings{
		Metrics: []string{"revenue"}, Dataset: "sales_orders", Details: []string{"revenue", "order_id", "customer_id"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved bindings = %#v, want %#v", got, want)
	}
}

func TestResolveVisualTypeFieldBindingsMapsSemanticQueryBackToOneDataset(t *testing.T) {
	model := &semanticmodel.Model{
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"category": {Bindings: map[string]semanticmodel.DimensionBinding{"sales_orders": {Field: "sales_orders.category"}}},
			"state":    {Bindings: map[string]semanticmodel.DimensionBinding{"sales_orders": {Field: "sales_customers.state"}}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"revenue":     {Dataset: "sales_orders", Input: &semanticmodel.MetricInput{Field: "sales_orders.revenue"}},
			"order_count": {Dataset: "sales_orders", Input: &semanticmodel.MetricInput{Field: "sales_orders.order_id"}},
		},
	}
	category, state, revenue, orders := "category", "state", "revenue", "order_count"
	visual := document.DashboardVisual{Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
		Type:       "aggregate",
		Dimensions: []document.DashboardDimensionSelection{{String: &category}, {String: &state}},
		Metrics:    []document.DashboardMetricSelection{{String: &revenue}, {String: &orders}},
	}}}

	got := resolveVisualTypeFieldBindings(model, visual)
	want := authoring.VisualTypeFieldBindings{
		Dimensions: []string{"category", "state"}, Metrics: []string{"revenue", "order_count"},
		Dataset: "sales_orders", Details: []string{"category", "revenue"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved bindings = %#v, want %#v", got, want)
	}
}

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
