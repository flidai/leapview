package query

import (
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestPrepareRepresentativePlansCoversMetricDependenciesAndBindings(t *testing.T) {
	model := &semanticmodel.Model{
		Name:    "sales",
		Sources: map[string]semanticmodel.Source{"orders_source": {Schema: semanticmodel.TableSchema{Columns: []semanticmodel.ColumnSchema{{Name: "order_id", PhysicalType: "BIGINT"}, {Name: "revenue", PhysicalType: "DECIMAL(12,2)"}}}}},
		Tables: map[string]semanticmodel.Table{"orders": {
			Dimensions: map[string]semanticmodel.MetricDimension{
				"order_id": {Type: "number", Datatype: semanticmodel.DataTypeInteger},
				"revenue":  {Type: "number", Datatype: semanticmodel.DataTypeDecimal},
			},
			Schema: semanticmodel.TableSchema{Columns: []semanticmodel.ColumnSchema{{Name: "order_id", PhysicalType: "BIGINT"}, {Name: "revenue", PhysicalType: "DECIMAL(12,2)"}}},
		}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"order_key": {Type: "number", Datatype: semanticmodel.DataTypeInteger, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.order_id"}}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}},
			"revenue":     {Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.revenue"}},
			"average":     {Type: "ratio", Numerator: "revenue", Denominator: "order_count"},
		},
	}
	plans, err := PrepareRepresentativePlans(model, func(table string) (string, error) { return "model." + table, nil })
	if err != nil {
		t.Fatalf("PrepareRepresentativePlans() error = %v", err)
	}
	routes := make(map[string]bool, len(plans))
	for _, plan := range plans {
		routes[plan.Route] = true
	}
	for _, route := range []string{"metric:order_count", "metric:revenue", "metric:average", "binding:order_key@orders"} {
		if !routes[route] {
			t.Fatalf("representative route %q was not prepared; routes=%v", route, routes)
		}
	}
}

func TestPrepareRepresentativePlansReportsDeterministicSchemaFailure(t *testing.T) {
	model := &semanticmodel.Model{
		Sources: map[string]semanticmodel.Source{"source": {Schema: semanticmodel.TableSchema{Columns: []semanticmodel.ColumnSchema{{Name: "id", PhysicalType: "VARCHAR"}}}}},
		Tables: map[string]semanticmodel.Table{"orders": {
			Dimensions: map[string]semanticmodel.MetricDimension{"id": {Datatype: semanticmodel.DataTypeInteger}},
			Schema:     semanticmodel.TableSchema{Columns: []semanticmodel.ColumnSchema{{Name: "id", PhysicalType: "VARCHAR"}}},
		}},
	}
	_, err := PrepareRepresentativePlans(model, nil)
	if err == nil || !strings.Contains(err.Error(), `model table "orders" field "id" datatype "Integer" is incompatible`) {
		t.Fatalf("error = %v, want contextual discovered datatype failure", err)
	}
}

func TestPrepareRepresentativePlansForcesEachExplicitRelationshipAndComposedFilter(t *testing.T) {
	model := verificationRouteModel()
	plans, err := PrepareRepresentativePlans(model, func(table string) (string, error) { return "model." + table, nil })
	if err != nil {
		t.Fatalf("PrepareRepresentativePlans() error = %v", err)
	}
	byRoute := map[string]Plan{}
	for _, prepared := range plans {
		byRoute[prepared.Route] = prepared.Plan
	}
	for route, token := range map[string]string{
		"relationship:orders_customer_primary": "customer_id",
		"relationship:orders_customer_alt":     "customer_code",
	} {
		plan, ok := byRoute[route]
		if !ok || !strings.Contains(plan.SQL, token) {
			t.Fatalf("route %q plan=%q, want join token %q", route, plan.SQL, token)
		}
	}
	if _, ok := byRoute["filter:captured@orders"]; !ok {
		t.Fatalf("composed filter route was not prepared: %v", byRoute)
	}
	if _, ok := byRoute["metric:combined"]; !ok {
		t.Fatalf("multi-fact derived metric route was not prepared: %v", byRoute)
	}
}

func verificationRouteModel() *semanticmodel.Model {
	columns := func(names ...string) []semanticmodel.ColumnSchema {
		result := make([]semanticmodel.ColumnSchema, 0, len(names))
		for _, name := range names {
			result = append(result, semanticmodel.ColumnSchema{Name: name, PhysicalType: "VARCHAR"})
		}
		return result
	}
	return &semanticmodel.Model{
		Tables: map[string]semanticmodel.Table{
			"orders": {Dimensions: map[string]semanticmodel.MetricDimension{
				"order_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString},
			}, Schema: semanticmodel.TableSchema{Columns: columns("order_id", "customer_id")}},
			"events": {Dimensions: map[string]semanticmodel.MetricDimension{"event_id": {Type: "string", Datatype: semanticmodel.DataTypeString}}, Schema: semanticmodel.TableSchema{Columns: columns("event_id")}},
			"customers": {Dimensions: map[string]semanticmodel.MetricDimension{
				"customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "customer_code": {Type: "string", Datatype: semanticmodel.DataTypeString}, "state": {Type: "string", Datatype: semanticmodel.DataTypeString},
			}, Schema: semanticmodel.TableSchema{Columns: columns("customer_id", "customer_code", "state")}},
		},
		Relationships: []semanticmodel.Relationship{
			{ID: "orders_customer_primary", FromDataset: "orders", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"},
			{ID: "orders_customer_alt", FromDataset: "orders", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_code"}, Cardinality: "many_to_one"},
		},
		Filters: map[string]semanticmodel.SemanticFilterSpec{"captured": {All: []semanticmodel.SemanticFilterSpec{{Field: "customers.state", Operator: "equals", Value: "CA", Path: []string{"orders_customer_primary"}}}}},
		Metrics: map[string]semanticmodel.Metric{
			"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}},
			"event_count": {Type: "aggregate", Dataset: "events", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "events.event_id"}},
			"combined":    {Type: "derived", Expression: "${order_count} + ${event_count}"},
		},
	}
}
