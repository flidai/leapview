package http

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

type semanticAuthorizationCapabilityMetrics struct {
	semanticProjectionMetrics
	plannerErr error
	targets    []semanticquery.SemanticAccessTarget
	fields     [][3]string
}

func (m *semanticAuthorizationCapabilityMetrics) SemanticPlanner(context.Context, string) (*semanticquery.Planner, error) {
	return nil, m.plannerErr
}

func (m *semanticAuthorizationCapabilityMetrics) AuthorizeSemanticTarget(_ context.Context, _ string, target semanticquery.SemanticAccessTarget) error {
	m.targets = append(m.targets, target)
	return nil
}

func (m *semanticAuthorizationCapabilityMetrics) AuthorizeSemanticField(_ context.Context, _ string, dataset, field string) error {
	m.fields = append(m.fields, [3]string{dataset, field})
	return nil
}

func TestSemanticPlannerForRequestFailsClosedWhenContextAuthorityErrors(t *testing.T) {
	model := &semanticmodel.Model{Name: "sales"}
	metrics := &semanticAuthorizationCapabilityMetrics{
		semanticProjectionMetrics: semanticProjectionMetrics{model: model},
		plannerErr:                errors.New("authority unavailable"),
	}
	if _, err := semanticPlannerForRequest(context.Background(), metrics, "sales"); !errors.Is(err, metrics.plannerErr) {
		t.Fatalf("planner error = %v, want authority error", err)
	}
}

func TestAuthorizeSemanticRequestUsesFieldCapabilityForQualifiedPhysicalDimension(t *testing.T) {
	model := &semanticmodel.Model{Name: "sales"}
	metrics := &semanticAuthorizationCapabilityMetrics{semanticProjectionMetrics: semanticProjectionMetrics{model: model}}
	request := semanticquery.Request{
		Dataset:    "orders",
		Dimensions: []semanticquery.Field{{Field: "orders.created_at"}},
	}
	if err := authorizeSemanticRequest(context.Background(), metrics, "sales", request); err != nil {
		t.Fatalf("authorize request: %v", err)
	}
	if len(metrics.targets) != 1 || metrics.targets[0] != (semanticquery.SemanticAccessTarget{Dataset: "orders"}) {
		t.Fatalf("targets = %#v, want dataset only", metrics.targets)
	}
	if len(metrics.fields) != 1 || metrics.fields[0] != [3]string{"orders", "orders.created_at", ""} {
		t.Fatalf("fields = %#v", metrics.fields)
	}
}

func TestAuthorizeSemanticRequestPreservesQualifiedPhysicalReferences(t *testing.T) {
	request := semanticquery.Request{
		Dataset: "orders",
		Dimensions: []semanticquery.Field{
			{Field: "logicalAllowed"},
			{Field: "orders.allowed"},
			{Field: "orders.status"},
		},
		Filters: []semanticquery.Filter{
			{Dataset: "orders", Field: "logicalAllowed"},
			{Field: "orders.allowed"},
			{Dataset: "orders", Field: "orders.status"},
			{Spatial: &semanticquery.SpatialFilter{LatitudeField: "orders.latitude", LongitudeField: "orders.longitude"}},
		},
	}
	want := [][3]string{
		{"orders", "logicalAllowed", ""},
		{"orders", "orders.allowed", ""},
		{"orders", "orders.status", ""},
		{"orders", "logicalAllowed", ""},
		{"orders", "orders.allowed", ""},
		{"orders", "orders.status", ""},
		{"orders", "orders.latitude", ""},
		{"orders", "orders.longitude", ""},
	}

	for iteration := 0; iteration < 10; iteration++ {
		metrics := &semanticAuthorizationCapabilityMetrics{semanticProjectionMetrics: semanticProjectionMetrics{model: &semanticmodel.Model{Name: "sales"}}}
		if err := authorizeSemanticRequest(context.Background(), metrics, "sales", request); err != nil {
			t.Fatalf("iteration %d: authorize request: %v", iteration, err)
		}
		if !reflect.DeepEqual(metrics.fields, want) {
			t.Fatalf("iteration %d: fields = %#v, want %#v", iteration, metrics.fields, want)
		}
	}
}

func TestAuthorizeSemanticRequestUsesPhysicalAndSemanticIdentitiesTogether(t *testing.T) {
	metrics := semanticPhysicalFieldConsumerFixture(t)
	request := semanticquery.Request{
		Dataset:    "orders",
		Dimensions: []semanticquery.Field{{Field: "logicalAllowed"}, {Field: "orders.allowed"}},
		Filters: []semanticquery.Filter{
			{Dataset: "orders", Field: "logicalStatus", Operator: "equals", Values: []any{"ready"}},
			{Field: "orders.status", Operator: "equals", Values: []any{"ready"}},
		},
	}
	if _, err := metrics.consumer.Planner().PlanRows(semanticquery.RowRequest{
		Dataset:    request.Dataset,
		Dimensions: request.Dimensions,
		Filters:    request.Filters,
		Limit:      1,
	}); err != nil {
		t.Fatalf("ordinary planner rejected mixed semantic and physical request: %v", err)
	}
	if err := authorizeSemanticRequest(context.Background(), metrics, "sales", request); err != nil {
		t.Fatalf("authorization rejected mixed semantic and physical request: %v", err)
	}
}

func TestAuthorizeSemanticRequestDistinguishesCollidingPhysicalAndSemanticNames(t *testing.T) {
	metrics, _ := semanticMetadataConsumerFixture(t)
	physical := semanticquery.RowRequest{Dataset: "orders", Dimensions: []semanticquery.Field{{Field: "orders.allowed"}}}
	if _, err := metrics.consumer.Planner().PlanRows(physical); err != nil {
		t.Fatalf("ordinary planner rejected physical orders.allowed: %v", err)
	}
	if err := authorizeSemanticRequest(context.Background(), metrics, "sales", physical); err != nil {
		t.Fatalf("physical orders.allowed was denied by colliding semantic member: %v", err)
	}

	semantic := semanticquery.RowRequest{Dataset: "orders", Dimensions: []semanticquery.Field{{Field: "allowed"}}}
	err := authorizeSemanticRequest(context.Background(), metrics, "sales", semantic)
	if err == nil || !strings.Contains(err.Error(), `semantic access denied for dimension "allowed"`) {
		t.Fatalf("semantic allowed error = %v, want denied semantic member", err)
	}
}

func TestAuthorizeSemanticRequestPreservesQualifiedPhysicalFilterWithoutDataset(t *testing.T) {
	metrics := semanticPhysicalFieldConsumerFixture(t)
	ordinary, err := metrics.consumer.Planner().PlanRows(semanticquery.RowRequest{
		Dataset:    "orders",
		Filters:    []semanticquery.Filter{{Field: "orders.allowed", Operator: "equals", Values: []any{"yes"}}},
		Dimensions: []semanticquery.Field{{Field: "orders.allowed"}},
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("ordinary PlanRows rejected qualified physical field: %v", err)
	}
	if ordinary.SQL == "" {
		t.Fatal("ordinary PlanRows returned an empty SQL plan")
	}
	if err := authorizeSemanticRequest(context.Background(), metrics, "sales", semanticquery.Request{
		Filters: []semanticquery.Filter{{Field: "orders.allowed", Operator: "equals", Values: []any{"yes"}}},
	}); err != nil {
		t.Fatalf("authorization rejected qualified physical filter: %v", err)
	}
	if err := authorizeSemanticRequest(context.Background(), metrics, "sales", semanticquery.Request{
		Dimensions: []semanticquery.Field{{Field: "orders.allowed"}},
	}); err != nil {
		t.Fatalf("authorization rejected qualified physical dimension without dataset: %v", err)
	}
}

func TestAuthorizeSemanticRequestRejectsInvalidPhysicalReferences(t *testing.T) {
	metrics := semanticPhysicalFieldConsumerFixture(t)
	for _, test := range []struct {
		name  string
		field string
		want  string
	}{
		{name: "unqualified", field: "allowed", want: "must be qualified"},
		{name: "unknown qualified", field: "orders.missing", want: "unknown field"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := authorizeSemanticRequest(context.Background(), metrics, "sales", semanticquery.RowRequest{
				Dataset:    "orders",
				Dimensions: []semanticquery.Field{{Field: test.field}},
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func semanticPhysicalFieldConsumerFixture(t *testing.T) *semanticMetadataMetrics {
	t.Helper()
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName:   "orders",
				GrainEntity: "order",
				Entities: map[string]semanticmodel.EntityDefinition{
					"order": {Type: "primary", Fields: []string{"allowed"}},
				},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"allowed": {Field: "orders.allowed", Table: "orders", Name: "allowed", Type: "string", Datatype: semanticmodel.DataTypeString},
					"status":  {Field: "orders.status", Table: "orders", Name: "status", Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"logicalAllowed": {Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.allowed"}}},
			"logicalStatus":  {Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.status"}}},
		},
	}
	planner, err := semanticquery.NewCompiledPlanner(model, semanticquery.WithTableRelation(func(table string) (string, error) { return table, nil }))
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := semanticquery.NewSemanticAccessConsumer(planner, semanticquery.SemanticAccessConsumerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return &semanticMetadataMetrics{semanticProjectionMetrics: semanticProjectionMetrics{model: model, planner: planner, plannerOkay: true}, consumer: consumer}
}

func TestSemanticConsumerMetadataFailsClosedForUnknownModel(t *testing.T) {
	metrics := semanticProjectionMetrics{}
	if err := authorizeSemanticTarget(context.Background(), metrics, "missing", semanticquery.SemanticAccessTarget{Dataset: "orders"}); !semanticAuthorizationUnavailable(err) {
		t.Fatalf("error = %v, want unavailable", err)
	}
}
