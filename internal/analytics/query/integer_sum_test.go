package query

import (
	"database/sql"
	"fmt"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/query/planir"
)

func TestAggregatePlanIRPreservesIntegerSumPrecision(t *testing.T) {
	planner := mustNewCompiledPlanner(t, integerSumModel())
	for _, test := range []struct{ metric, want string }{
		{"whole_sum", "decimal"},
		{"whole_avg", "decimal"},
		{"whole_min", "integer"},
		{"whole_max", "integer"},
		{"row_count", "integer"},
		{"distinct_count", "integer"},
		{"fractional_sum", "float"},
		{"fractional_avg", "float"},
	} {
		t.Run(test.metric, func(t *testing.T) {
			plan, err := planner.Plan(Request{Metrics: []Field{{Field: test.metric, Alias: "result"}}})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, node := range plan.IR.Nodes {
				if aggregate, ok := node.(planir.AggregateMetrics); ok {
					for _, metric := range aggregate.Metrics {
						if metric.Name == test.metric {
							found = true
							if metric.Type != test.want {
								t.Fatalf("aggregate datatype = %q, want %q", metric.Type, test.want)
							}
						}
					}
				}
			}
			if !found {
				t.Fatal("metric missing from aggregate PlanIR")
			}
			schema, err := planner.DescribeOutputSchema(plan)
			if err != nil {
				t.Fatal(err)
			}
			if len(schema.Fields) != 1 || schema.Fields[0].Alias != "result" || schema.Fields[0].LogicalType != test.want {
				t.Fatalf("output schema = %#v, want result with datatype %q", schema.Fields, test.want)
			}
		})
	}
}

func TestRenderedIntegerSumRetainsHugeIntegerAndFractionalFloat(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.observations (id BIGINT, whole BIGINT, fractional DOUBLE)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	planner := mustNewCompiledPlanner(t, integerSumModel(), WithTableRelation(func(table string) (string, error) { return "model." + table, nil }))
	plan, err := planner.Plan(Request{Metrics: []Field{{Field: "whole_sum"}, {Field: "fractional_sum"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, first, second, want string }{
		{"beyond browser integer precision", "9007199254740993", "0", "9007199254740993"},
		{"beyond signed 64 bit", "9223372036854775807", "9223372036854775807", "18446744073709551614"},
		{"negative beyond signed 64 bit", "-9223372036854775808", "-9223372036854775808", "-18446744073709551616"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := db.Exec("DELETE FROM model.observations"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(fmt.Sprintf("INSERT INTO model.observations VALUES (1, %s, 0.125), (2, %s, 0.25)", test.first, test.second)); err != nil {
				t.Fatal(err)
			}
			var wholeType, wholeText, fractionalType string
			var fractionalSum float64
			statement := fmt.Sprintf("SELECT typeof(whole_sum), CAST(whole_sum AS VARCHAR), typeof(fractional_sum), fractional_sum FROM (%s) result", plan.SQL)
			if err := db.QueryRow(statement, plan.Args...).Scan(&wholeType, &wholeText, &fractionalType, &fractionalSum); err != nil {
				t.Fatalf("execute aggregate SQL: %v\nSQL: %s", err, statement)
			}
			if wholeType != "HUGEINT" || wholeText != test.want {
				t.Fatalf("integer sum = %s %q, want HUGEINT %q", wholeType, wholeText, test.want)
			}
			if fractionalType != "DOUBLE" || fractionalSum != 0.375 {
				t.Fatalf("float sum = %s %v, want DOUBLE 0.375", fractionalType, fractionalSum)
			}
		})
	}
}

func integerSumModel() *semanticmodel.Model {
	metrics := map[string]semanticmodel.Metric{}
	for _, definition := range []struct{ name, aggregation, field string }{
		{"whole_sum", "sum", "whole"}, {"whole_avg", "avg", "whole"},
		{"whole_min", "min", "whole"}, {"whole_max", "max", "whole"},
		{"row_count", "count", "id"}, {"distinct_count", "count_distinct", "whole"},
		{"fractional_sum", "sum", "fractional"}, {"fractional_avg", "avg", "fractional"},
	} {
		metrics[definition.name] = semanticmodel.Metric{Type: "aggregate", Dataset: "observations", Aggregation: definition.aggregation,
			Input: &semanticmodel.MetricInput{Field: "observations." + definition.field}, Empty: "null", Format: "integer"}
	}
	return &semanticmodel.Model{
		Name: "aggregate_precision",
		Tables: map[string]semanticmodel.Table{"observations": {
			GrainEntity: "observation",
			Entities:    map[string]semanticmodel.EntityDefinition{"observation": {Type: "primary", Fields: []string{"id"}}},
			Dimensions: map[string]semanticmodel.MetricDimension{
				"id": {Datatype: semanticmodel.DataTypeInteger}, "whole": {Datatype: semanticmodel.DataTypeInteger},
				"fractional": {Datatype: semanticmodel.DataTypeFloat},
			},
		}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"observations": {Model: "observations"}},
		Metrics:  metrics,
	}
}
