package materialize

import (
	"context"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

func TestProtectedCountAuthorizationPreservesSameNameMemberKinds(t *testing.T) {
	tests := []struct {
		name        string
		allowMetric bool
		deniedKind  string
		allowedKind string
	}{
		{name: "dimension denied metric allowed", allowMetric: true, deniedKind: dataquery.FieldKindDimension, allowedKind: dataquery.FieldKindMetric},
		{name: "metric denied dimension allowed", allowMetric: false, deniedKind: dataquery.FieldKindMetric, allowedKind: dataquery.FieldKindDimension},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime, governor, _ := protectedConsumerFixtureWithMemberAccess(t, tt.allowMetric)
			ctx := semanticquery.WithSemanticAccessConsumer(context.Background(), governor.consumer, governor.binding)
			request := dataquery.Query{
				ModelID: "sales", Kind: dataquery.KindSemanticRows, Target: "orders", IncludeTotal: true,
				AuthorizationFields: []dataquery.Field{{Field: "shared", Kind: tt.deniedKind}},
			}
			if _, err := runtime.planOwnedArrowQueryContext(ctx, request); err == nil || !strings.Contains(err.Error(), "semantic access denied for "+tt.deniedKind) {
				t.Fatalf("denied %s count authorization error = %v", tt.deniedKind, err)
			}
			request.AuthorizationFields[0].Kind = tt.allowedKind
			if _, err := runtime.planOwnedArrowQueryContext(ctx, request); err != nil {
				t.Fatalf("allowed %s count authorization error = %v", tt.allowedKind, err)
			}
		})
	}
}

func TestSemanticAuthorizationProjectionPreservesMemberKind(t *testing.T) {
	planner := sameNameAuthorizationPlanner(t)

	tests := []struct {
		name          string
		field         dataquery.Field
		wantDimension bool
		wantMetric    bool
	}{
		{name: "explicit dimension", field: dataquery.Field{Field: "shared", Kind: dataquery.FieldKindDimension}, wantDimension: true},
		{name: "explicit metric", field: dataquery.Field{Field: "shared", Kind: dataquery.FieldKindMetric}, wantMetric: true},
		{name: "unambiguous dimension", field: dataquery.Field{Field: "dimension_only"}, wantDimension: true},
		{name: "unambiguous metric", field: dataquery.Field{Field: "metric_only"}, wantMetric: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dimensions, metrics, err := semanticAuthorizationProjectionFields(planner, dataquery.Query{
				AuthorizationFields: []dataquery.Field{tt.field},
			})
			if err != nil {
				t.Fatal(err)
			}
			if (len(dimensions) == 1) != tt.wantDimension || (len(metrics) == 1) != tt.wantMetric {
				t.Fatalf("dimensions=%#v metrics=%#v, want dimension=%v metric=%v", dimensions, metrics, tt.wantDimension, tt.wantMetric)
			}
		})
	}
}

func TestSemanticAuthorizationProjectionRejectsAmbiguousLegacyMember(t *testing.T) {
	planner := sameNameAuthorizationPlanner(t)
	_, _, err := semanticAuthorizationProjectionFields(planner, dataquery.Query{
		AuthorizationFields: []dataquery.Field{{Field: "shared"}},
	})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous legacy projection error = %v", err)
	}
}

func sameNameAuthorizationPlanner(t *testing.T) *semanticquery.Planner {
	t.Helper()
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName:   "orders",
				Execution:   semanticmodel.ExecutionDefinition{SQL: "SELECT 1 AS id, 1 AS shared"},
				GrainEntity: "order",
				Entities:    map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"id"}}},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"id":     {Field: "orders.id", Table: "orders", Name: "id", Type: "number", Datatype: semanticmodel.DataTypeInteger},
					"shared": {Field: "orders.shared", Table: "orders", Name: "shared", Type: "number", Datatype: semanticmodel.DataTypeInteger},
				},
			},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"shared":         {Name: "shared", Datatype: semanticmodel.DataTypeInteger, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.shared"}}},
			"dimension_only": {Name: "dimension_only", Datatype: semanticmodel.DataTypeInteger, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.id"}}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"shared":      {Name: "shared", Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.id"}},
			"metric_only": {Name: "metric_only", Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.id"}},
		},
	}
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	return planner
}
