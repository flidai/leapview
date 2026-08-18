package query

import (
	"reflect"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/semanticnumeric"
)

func TestProjectScalarFromCompleteGroupedAtomicMetrics(t *testing.T) {
	model := projectionModel()
	planner := mustNewCompiledPlanner(t, model)
	grouped := Request{
		Dimensions: []Field{{Field: "activity_date", Alias: "label"}},
		Metrics: []Field{
			{Field: "rating_count", Alias: "ratings"},
			{Field: "tag_count", Alias: "tags"},
		},
		Filters: []Filter{{Field: "release_decade", Operator: "equals", Values: []any{"1990s"}}},
		Limit:   360,
	}
	scalar := Request{
		Metrics: []Field{{Field: "tags_per_rating", Alias: "value"}},
		Filters: append([]Filter{}, grouped.Filters...),
	}
	rows := Rows{
		{"label": "2024-01-01", "ratings": int64(8), "tags": int64(3)},
		{"label": "2024-02-01", "ratings": int64(2), "tags": int64(1)},
	}

	projected, ok, err := planner.ProjectScalarFromGrouped(grouped, scalar, rows, true)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("projection was rejected")
	}
	want := Rows{{"value": semanticnumeric.Decimal("0.4")}}
	if !reflect.DeepEqual(projected, want) {
		t.Fatalf("projected = %#v, want %#v", projected, want)
	}
}

func TestRecombineAdditivePreservesExactDecimal(t *testing.T) {
	value, err := recombineAdditive(Rows{
		{"amount": "9007199254740993.125"},
		{"amount": "0.001"},
	}, "amount", "null")
	if err != nil {
		t.Fatal(err)
	}
	if value != semanticnumeric.Decimal("9007199254740993.126") {
		t.Fatalf("recombined Decimal = %#v", value)
	}
}

func TestProjectScalarFromGroupedRejectsUnsafeShapes(t *testing.T) {
	model := projectionModel()
	planner := mustNewCompiledPlanner(t, model)
	base := Request{Dimensions: []Field{{Field: "activity_date", Alias: "label"}}, Metrics: []Field{{Field: "rating_count", Alias: "ratings"}, {Field: "tag_count", Alias: "tags"}}}
	scalar := Request{Metrics: []Field{{Field: "tags_per_rating", Alias: "value"}}}
	rows := Rows{{"ratings": int64(2), "tags": int64(1)}}
	tests := []struct {
		name     string
		grouped  Request
		scalar   Request
		complete bool
	}{
		{name: "truncated", grouped: base, scalar: scalar, complete: false},
		{name: "different filters", grouped: base, scalar: Request{Metrics: scalar.Metrics, Filters: []Filter{{Field: "activity_date", Operator: "equals", Values: []any{"2024"}}}}, complete: true},
		{name: "different masks", grouped: Request{Dimensions: base.Dimensions, Metrics: base.Metrics, ColumnMasks: []ColumnMask{{Field: "rating_count", Mask: "null"}}}, scalar: scalar, complete: true},
		{name: "missing dependency", grouped: Request{Dimensions: base.Dimensions, Metrics: []Field{{Field: "rating_count", Alias: "ratings"}}}, scalar: scalar, complete: true},
		{name: "scalar grouped", grouped: base, scalar: Request{Dimensions: base.Dimensions, Metrics: scalar.Metrics}, complete: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, ok, err := planner.ProjectScalarFromGrouped(test.grouped, test.scalar, rows, test.complete)
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				t.Fatal("unsafe projection was accepted")
			}
		})
	}
}

func TestProjectScalarFromGroupedRejectsNonAdditiveDependency(t *testing.T) {
	model := projectionModel()
	model.Metrics["average_rating"] = semanticmodel.Metric{Type: "aggregate", Dataset: "ratings", Aggregation: "avg", Input: &semanticmodel.MetricInput{Field: "ratings.rating"}, Empty: "null"}
	model.Metrics["normalized_rating"] = semanticmodel.Metric{Type: "derived", Expression: "${average_rating} / 5"}
	grouped := Request{Dimensions: []Field{{Field: "activity_date"}}, Metrics: []Field{{Field: "average_rating", Alias: "average"}}}
	scalar := Request{Metrics: []Field{{Field: "normalized_rating", Alias: "value"}}}
	planner := mustNewCompiledPlanner(t, model)
	if _, ok, err := planner.ProjectScalarFromGrouped(grouped, scalar, Rows{{"average": 4.0}}, true); err != nil || ok {
		t.Fatalf("projection ok=%v err=%v, want safe rejection", ok, err)
	}
}

func TestProjectScalarFromGroupedRecombinesAdditiveSumsBeforeMetricEvaluation(t *testing.T) {
	model := projectionModel()
	model.Metrics["revenue"] = semanticmodel.Metric{Type: "aggregate", Dataset: "ratings", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "ratings.rating"}, Empty: "null"}
	model.Metrics["cost"] = semanticmodel.Metric{Type: "aggregate", Dataset: "ratings", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "ratings.rating"}, Empty: "zero"}
	model.Metrics["margin"] = semanticmodel.Metric{Type: "derived", Expression: "safe_divide(${revenue} - ${cost}, ${revenue})"}
	grouped := Request{Dimensions: []Field{{Field: "activity_date"}}, Metrics: []Field{{Field: "revenue", Alias: "revenue"}, {Field: "cost", Alias: "cost"}}}
	scalar := Request{Metrics: []Field{{Field: "margin", Alias: "value"}}}
	rows := Rows{{"revenue": 10.0, "cost": 3.0}, {"revenue": 30.0, "cost": 9.0}}
	planner := mustNewCompiledPlanner(t, model)
	projected, ok, err := planner.ProjectScalarFromGrouped(grouped, scalar, rows, true)
	if err != nil || !ok {
		t.Fatalf("projection ok=%v err=%v", ok, err)
	}
	if projected[0]["value"] != 0.7 {
		t.Fatalf("margin = %#v, want 0.7", projected)
	}
}

func projectionModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Tables: map[string]semanticmodel.Table{
			"ratings": {GrainEntity: "rating", Entities: map[string]semanticmodel.EntityDefinition{"rating": {Type: "primary", Fields: []string{"rating"}}}, Dimensions: map[string]semanticmodel.MetricDimension{
				"rating": {Type: "number", Datatype: semanticmodel.DataTypeDecimal}, "activity_date": {Type: "date", Datatype: semanticmodel.DataTypeDate}, "release_decade": {Type: "string", Datatype: semanticmodel.DataTypeString},
			}},
			"tags": {GrainEntity: "tag", Entities: map[string]semanticmodel.EntityDefinition{"tag": {Type: "primary", Fields: []string{"tag_id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{
				"tag_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "activity_date": {Type: "date", Datatype: semanticmodel.DataTypeDate}, "release_decade": {Type: "string", Datatype: semanticmodel.DataTypeString},
			}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"ratings": {Model: "ratings"}, "tags": {Model: "tags"}},
		Metrics: map[string]semanticmodel.Metric{
			"rating_count":    {Type: "aggregate", Dataset: "ratings", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "ratings.rating"}, Empty: "zero"},
			"tag_count":       {Type: "aggregate", Dataset: "tags", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "tags.tag_id"}, Empty: "zero"},
			"rating_share":    {Type: "derived", Expression: "${rating_count}"},
			"tags_per_rating": {Type: "derived", Expression: "safe_divide(${tag_count}, ${rating_share})"},
		},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"activity_date":  {Type: "date", Datatype: semanticmodel.DataTypeDate, Bindings: map[string]semanticmodel.DimensionBinding{"ratings": {Field: "ratings.activity_date"}, "tags": {Field: "tags.activity_date"}}},
			"release_decade": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"ratings": {Field: "ratings.release_decade"}, "tags": {Field: "tags.release_decade"}}},
		},
	}
}
