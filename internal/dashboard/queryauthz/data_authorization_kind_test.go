package authz

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestAuthorizationFieldIsMetricPreservesExplicitKind(t *testing.T) {
	model := authorizationKindTestModel()
	tests := []struct {
		name       string
		field      dataquery.Field
		wantMetric bool
	}{
		{name: "explicit metric", field: dataquery.Field{Field: "shared", Kind: dataquery.FieldKindMetric}, wantMetric: true},
		{name: "explicit dimension", field: dataquery.Field{Field: "shared", Kind: dataquery.FieldKindDimension}, wantMetric: false},
		{name: "unambiguous metric", field: dataquery.Field{Field: "metric_only"}, wantMetric: true},
		{name: "unambiguous dimension", field: dataquery.Field{Field: "dimension_only"}, wantMetric: false},
		{name: "qualified physical dimension", field: dataquery.Field{Field: "orders.id"}, wantMetric: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := authorizationFieldIsMetric(model, tt.field)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.wantMetric {
				t.Fatalf("metric classification = %v, want %v", got, tt.wantMetric)
			}
		})
	}
}

func TestAuthorizationFieldIsMetricRejectsAmbiguousAndInvalidLegacyKinds(t *testing.T) {
	model := authorizationKindTestModel()
	for _, field := range []dataquery.Field{
		{Field: "shared"},
		{Field: "shared", Kind: "other"},
	} {
		_, err := authorizationFieldIsMetric(model, field)
		if err == nil {
			t.Fatalf("field %#v was accepted", field)
		}
		if field.Kind == "" && !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("ambiguous field error = %v", err)
		}
		if field.Kind != "" && !strings.Contains(err.Error(), "unsupported kind") {
			t.Fatalf("invalid kind error = %v", err)
		}
	}
}

func authorizationKindTestModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Tables: map[string]semanticmodel.Table{
			"orders": {Dimensions: map[string]semanticmodel.MetricDimension{
				"id": {Field: "orders.id", Table: "orders", Name: "id"},
			}},
		},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"shared":         {},
			"dimension_only": {},
		},
		Metrics: map[string]semanticmodel.Metric{
			"shared":      {},
			"metric_only": {},
		},
	}
}
