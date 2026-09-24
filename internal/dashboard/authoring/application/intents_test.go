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

func TestResolveVisualTypeFieldBindingsForTargetCompletesRequiredRolesFromSourceDataset(t *testing.T) {
	model := &semanticmodel.Model{
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"scenario":   {Bindings: map[string]semanticmodel.DimensionBinding{"cash_forecast": {Field: "cash_scenarios.scenario"}}},
			"week_start": {Bindings: map[string]semanticmodel.DimensionBinding{"cash_forecast": {Field: "cash_weeks.week_start"}}},
			"region":     {Bindings: map[string]semanticmodel.DimensionBinding{"sales": {Field: "sales.region"}}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"current_cash":      {Dataset: "cash_forecast", Input: &semanticmodel.MetricInput{Field: "cash_forecast.current_cash"}},
			"collections":       {Dataset: "cash_forecast", Input: &semanticmodel.MetricInput{Field: "cash_forecast.collections"}},
			"net_cash_flow":     {Dataset: "cash_forecast", Input: &semanticmodel.MetricInput{Field: "cash_forecast.net_cash_flow"}},
			"supplier_payments": {Dataset: "cash_forecast", Input: &semanticmodel.MetricInput{Field: "cash_forecast.supplier_payments"}},
			"sales_total":       {Dataset: "sales", Input: &semanticmodel.MetricInput{Field: "sales.total"}},
		},
		Tables: map[string]semanticmodel.Table{
			"cash_forecast": {Dimensions: map[string]semanticmodel.MetricDimension{
				"current_cash": {Datatype: semanticmodel.DataTypeDecimal},
				"scenario":     {Datatype: semanticmodel.DataTypeString},
				"week_start":   {Datatype: semanticmodel.DataTypeDate},
			}},
			"sales": {Dimensions: map[string]semanticmodel.MetricDimension{"region": {Datatype: semanticmodel.DataTypeString}}},
		},
	}
	currentCash := "current_cash"
	visual := document.DashboardVisual{Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
		Type: "aggregate", Metrics: []document.DashboardMetricSelection{{String: &currentCash}},
	}}}

	for _, entry := range authoring.CanonicalVisualCatalog() {
		t.Run(string(entry.Type), func(t *testing.T) {
			got := resolveVisualTypeFieldBindingsForTarget(model, visual, entry.Type)
			if got.Dataset != "cash_forecast" {
				t.Fatalf("dataset = %q, want cash_forecast", got.Dataset)
			}
			if len(got.Metrics) == 0 || got.Metrics[0] != "current_cash" {
				t.Fatalf("metrics = %#v, want current_cash retained first", got.Metrics)
			}
			for _, limit := range authoring.CanonicalVisualRoleLimits(entry.Type) {
				var count int
				switch limit.Role {
				case string(authoring.FieldRoleDimension):
					count = len(got.Dimensions)
				case string(authoring.FieldRoleMetric):
					count = len(got.Metrics)
				case string(authoring.FieldRoleDetail):
					count = len(got.Details)
				}
				if int32(count) < limit.Minimum {
					t.Fatalf("%s fields = %d, want at least %d: %#v", limit.Role, count, limit.Minimum, got)
				}
			}
			for _, dimension := range got.Dimensions {
				if dimension == "region" {
					t.Fatalf("cross-dataset dimension leaked into bindings: %#v", got)
				}
			}
			for _, metric := range got.Metrics {
				if metric == "sales_total" {
					t.Fatalf("cross-dataset metric leaked into bindings: %#v", got)
				}
			}
		})
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
