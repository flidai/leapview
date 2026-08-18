package query

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/query/planir"
	"github.com/flidai/leapview/internal/analytics/semanticnumeric"
)

// TestDecimalExpressionAndPlanIRConformance keeps the expression corpus
// independent of a renderer implementation, then checks that the same
// expression is lowered as a typed PlanIR tree without losing its source
// decimal token.
func TestDecimalExpressionAndPlanIRConformance(t *testing.T) {
	cases := []struct {
		name   string
		source string
		value  map[string]any
		want   any
	}{
		{
			name:   "high precision division",
			source: "safe_divide(${amount}, ${count})",
			value:  map[string]any{"amount": semanticnumeric.Decimal("9007199254740993.125"), "count": int64(1)},
			want:   semanticnumeric.Decimal("9007199254740993.125"),
		},
		{
			name:   "null denominator",
			source: "safe_divide(${amount}, ${count})",
			value:  map[string]any{"amount": semanticnumeric.Decimal("9007199254740993.125"), "count": nil},
			want:   nil,
		},
		{
			name:   "zero denominator",
			source: "safe_divide(${amount}, ${count})",
			value:  map[string]any{"amount": semanticnumeric.Decimal("9007199254740993.125"), "count": int64(0)},
			want:   nil,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			expression, err := semanticmodel.ParseExpression(test.source)
			if err != nil {
				t.Fatal(err)
			}
			got, err := expression.Evaluate(func(name string) (any, error) { return test.value[name], nil })
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Evaluate() = %#v (%T), want %#v (%T)", got, got, test.want, test.want)
			}
			model := decimalConformanceModel(test.source)
			populateFixtureTableModelNames(model)
			planner, err := NewCompiledPlanner(model)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := planner.Plan(Request{Metrics: []Field{{Field: "derived"}}})
			if err != nil {
				t.Fatal(err)
			}
			var found bool
			for _, node := range plan.IR.Nodes {
				derived, ok := node.(planir.ComputeDerived)
				if !ok || derived.Output != "derived" {
					continue
				}
				found = true
				if derived.Expression.Kind != planir.ScalarFunction || strings.ToLower(derived.Expression.Function) != "safe_divide" {
					t.Fatalf("PlanIR expression = %#v, want safe_divide function", derived.Expression)
				}
				if len(derived.Expression.Children) != 2 || derived.Expression.Children[0].Kind != planir.ScalarMetricRef || derived.Expression.Children[0].Metric != "amount" {
					t.Fatalf("PlanIR numerator = %#v, want amount metric reference", derived.Expression.Children)
				}
			}
			if !found {
				t.Fatal("PlanIR did not contain derived computation")
			}
		})
	}
}

// TestRenderedDecimalDivisionRecordsDuckDBType verifies the renderer's exact
// Decimal quotient contract and keeps Float division explicitly approximate.
func TestRenderedDecimalDivisionRecordsDuckDBType(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.decimal_datasets (id BIGINT, amount DECIMAL(38,3), float_amount DOUBLE, customer_id VARCHAR)",
		"INSERT INTO model.decimal_datasets VALUES (1, 9007199254740993.125, 9007199254740993.125, 'customer-1')",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	model := decimalConformanceModel("safe_divide(${amount}, ${count})")
	populateFixtureTableModelNames(model)
	planner, err := NewCompiledPlanner(model, WithTableRelation(func(table string) (string, error) {
		return "model." + table, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(Request{Metrics: []Field{{Field: "amount"}, {Field: "count"}, {Field: "derived"}, {Field: "ratio"}, {Field: "float_amount"}, {Field: "float_ratio", Alias: "float_ratio_alias"}, {Field: "avg_amount"}, {Field: "avg_float"}, {Field: "distinct_ratio"}}})
	if err != nil {
		t.Fatal(err)
	}
	var derivedType, derivedText, ratioType, ratioText, floatRatioType, floatRatioText, avgAmountType, avgAmountText, avgFloatType, avgFloatText, distinctRatioType, distinctRatioText string
	query := fmt.Sprintf("SELECT typeof(derived), CAST(derived AS VARCHAR), typeof(ratio), CAST(ratio AS VARCHAR), typeof(float_ratio_alias), CAST(float_ratio_alias AS VARCHAR), typeof(avg_amount), CAST(avg_amount AS VARCHAR), typeof(avg_float), CAST(avg_float AS VARCHAR), typeof(distinct_ratio), CAST(distinct_ratio AS VARCHAR) FROM (%s) result", plan.SQL)
	if err := db.QueryRow(query, plan.Args...).Scan(&derivedType, &derivedText, &ratioType, &ratioText, &floatRatioType, &floatRatioText, &avgAmountType, &avgAmountText, &avgFloatType, &avgFloatText, &distinctRatioType, &distinctRatioText); err != nil {
		t.Fatalf("inspect rendered decimal result: %v\nSQL: %s", err, query)
	}
	if derivedType != "DECIMAL(38,18)" || ratioType != "DECIMAL(38,18)" {
		t.Fatalf("DuckDB quotient types = derived %q ratio %q, want DECIMAL(38,18)", derivedType, ratioType)
	}
	if floatRatioType != "DOUBLE" {
		t.Fatalf("DuckDB float quotient type = %q, want DOUBLE", floatRatioType)
	}
	if avgAmountType != "DECIMAL(38,18)" || avgAmountText != derivedText {
		t.Fatalf("DuckDB Decimal AVG = %s %q, want DECIMAL(38,18) exact value", avgAmountType, avgAmountText)
	}
	if avgFloatType != "DOUBLE" || avgFloatText == avgAmountText {
		t.Fatalf("DuckDB Float AVG = %s %q, want approximate DOUBLE", avgFloatType, avgFloatText)
	}
	if distinctRatioType != "DECIMAL(38,18)" || distinctRatioText != derivedText {
		t.Fatalf("DuckDB count-distinct ratio = %s %q, want exact Decimal value", distinctRatioType, distinctRatioText)
	}
	if derivedText != "9007199254740993.125000000000000000" || ratioText != derivedText {
		t.Fatalf("DuckDB quotient values = derived %q ratio %q, want exact high-precision quotient", derivedText, ratioText)
	}
	if floatRatioText == derivedText {
		t.Fatalf("DuckDB float quotient unexpectedly retained exact Decimal text %q", floatRatioText)
	}
	if _, err := db.Exec("DELETE FROM model.decimal_datasets"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO model.decimal_datasets VALUES (NULL, 9007199254740993.125, 9007199254740993.125, NULL)"); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(plan.SQL, plan.Args...)
	if err != nil {
		t.Fatal(err)
	}
	values := make([]any, len(plan.Columns))
	scans := make([]any, len(values))
	for i := range scans {
		scans[i] = &values[i]
	}
	if !rows.Next() {
		rows.Close()
		t.Fatal("zero denominator did not return a row")
	}
	if err := rows.Scan(scans...); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	for _, column := range []string{"derived", "ratio", "float_ratio_alias", "avg_amount", "distinct_ratio"} {
		if value := planColumnValue(plan.Columns, values, column); value != nil {
			t.Fatalf("zero denominator %s = %#v, want nil", column, value)
		}
	}
	if _, err := db.Exec("DELETE FROM model.decimal_datasets"); err != nil {
		t.Fatal(err)
	}
	rows, err = db.Query(plan.SQL, plan.Args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("empty aggregate did not return a null row")
	}
	values = make([]any, len(plan.Columns))
	scans = make([]any, len(values))
	for i := range scans {
		scans[i] = &values[i]
	}
	if err := rows.Scan(scans...); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"derived", "ratio", "float_ratio_alias", "avg_amount", "avg_float", "distinct_ratio"} {
		if value := planColumnValue(plan.Columns, values, column); value != nil {
			t.Fatalf("empty aggregate %s = %#v, want nil", column, value)
		}
	}
}

func TestRenderedDecimalBinaryDivisionReturnsNullOnZeroAndNull(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.decimal_datasets (id BIGINT, amount DECIMAL(38,3), float_amount DOUBLE, customer_id VARCHAR)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	model := decimalConformanceModel("${amount} / ${count}")
	populateFixtureTableModelNames(model)
	planner, err := NewCompiledPlanner(model, WithTableRelation(func(table string) (string, error) {
		return "model." + table, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(Request{Metrics: []Field{{Field: "derived"}}})
	if err != nil {
		t.Fatal(err)
	}
	checkNull := func(name, values string) {
		t.Helper()
		if _, err := db.Exec("DELETE FROM model.decimal_datasets"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO model.decimal_datasets VALUES " + values); err != nil {
			t.Fatal(err)
		}
		var value any
		if err := db.QueryRow(plan.SQL, plan.Args...).Scan(&value); err != nil {
			t.Fatalf("%s query: %v\nSQL: %s", name, err, plan.SQL)
		}
		if value != nil {
			t.Fatalf("%s derived = %#v, want nil", name, value)
		}
	}
	checkNull("zero denominator", "(NULL, 1.0, 1.0, NULL)")
	checkNull("null numerator", "(1, NULL, 1.0, NULL)")
}

func TestRenderedDecimalDivisionFailsClosedOnIntermediateOverflow(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.decimal_wide (id BIGINT, left_value DECIMAL(38,25), right_value DECIMAL(38,25))",
		"INSERT INTO model.decimal_wide VALUES (1, 1234567890123.1234567890123456789012345, 1234567890123.1234567890123456789012345)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	model := decimalIntermediateOverflowModel()
	populateFixtureTableModelNames(model)
	planner, err := NewCompiledPlanner(model, WithTableRelation(func(table string) (string, error) {
		return "model." + table, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(Request{Metrics: []Field{{Field: "ratio"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Query(plan.SQL, plan.Args...); err == nil {
		t.Fatal("exact quotient intermediate overflow was accepted")
	}
}

func TestRenderedDecimalQuotientCorpusMatchesEvaluator(t *testing.T) {
	type quotientCase struct {
		id          int
		left, right *string
		want        string
	}
	decimal := func(value string) *string { return &value }
	cases := []quotientCase{
		{id: 1, left: decimal("0.1234567890123456765"), right: decimal("1"), want: "0.123456789012345676"},   // even tie: stay down
		{id: 2, left: decimal("0.1234567890123456775"), right: decimal("1"), want: "0.123456789012345678"},   // odd tie: round up
		{id: 3, left: decimal("-0.1234567890123456775"), right: decimal("1"), want: "-0.123456789012345678"}, // negative quotient
		{id: 4, left: decimal("1"), right: nil},          // null denominator
		{id: 5, left: decimal("1"), right: decimal("0")}, // zero denominator
	}

	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.decimal_quotients (case_id INTEGER, left_value DECIMAL(38,19), right_value DECIMAL(38,19))",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range cases {
		leftSQL := "NULL"
		if test.left != nil {
			leftSQL = "CAST('" + *test.left + "' AS DECIMAL(38,19))"
		}
		rightSQL := "NULL"
		if test.right != nil {
			rightSQL = "CAST('" + *test.right + "' AS DECIMAL(38,19))"
		}
		if _, err := db.Exec(fmt.Sprintf("INSERT INTO model.decimal_quotients VALUES (%d, %s, %s)", test.id, leftSQL, rightSQL)); err != nil {
			t.Fatal(err)
		}
	}

	model := decimalQuotientCorpusModel()
	populateFixtureTableModelNames(model)
	planner, err := NewCompiledPlanner(model, WithTableRelation(func(table string) (string, error) {
		return "model." + table, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(Request{
		Dimensions: []Field{{Field: "decimal_quotients.case_id", Alias: "case_id"}},
		Metrics:    []Field{{Field: "quotient"}},
		Sort:       []Sort{{Field: "case_id", Direction: "asc"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(fmt.Sprintf("SELECT case_id, CAST(quotient AS VARCHAR) FROM (%s) rendered", plan.SQL), plan.Args...)
	if err != nil {
		t.Fatalf("rendered quotient corpus query: %v", err)
	}
	defer rows.Close()
	expression, err := semanticmodel.ParseExpression("safe_divide(${left}, ${right})")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range cases {
		if !rows.Next() {
			t.Fatalf("missing rendered row for case %d", test.id)
		}
		var id int
		var rendered sql.NullString
		if err := rows.Scan(&id, &rendered); err != nil {
			t.Fatal(err)
		}
		if id != test.id {
			t.Fatalf("rendered case id = %d, want %d", id, test.id)
		}
		values := map[string]any{}
		if test.left != nil {
			values["left"] = semanticnumeric.Decimal(*test.left)
		}
		if test.right != nil {
			values["right"] = semanticnumeric.Decimal(*test.right)
		}
		want, err := expression.Evaluate(func(name string) (any, error) { return values[name], nil })
		if err != nil {
			t.Fatalf("evaluate case %d: %v", test.id, err)
		}
		if want == nil {
			if rendered.Valid {
				t.Fatalf("rendered case %d = %q, want NULL", test.id, rendered.String)
			}
			continue
		}
		wantText, ok := want.(semanticnumeric.Decimal)
		if !ok {
			t.Fatalf("evaluate case %d type = %T, want Decimal", test.id, want)
		}
		if test.want != "" && string(wantText) != test.want {
			t.Fatalf("evaluate case %d = %q, want known half-even result %q", test.id, wantText, test.want)
		}
		if !rendered.Valid {
			t.Fatalf("rendered case %d = NULL, want %q", test.id, wantText)
		}
		gotNumber, err := semanticnumeric.Parse(rendered.String)
		if err != nil {
			t.Fatalf("rendered case %d text %q: %v", test.id, rendered.String, err)
		}
		if got := gotNumber.Value(); got != wantText {
			t.Fatalf("rendered case %d = %q (canonical %v), evaluator = %q", test.id, rendered.String, got, wantText)
		}
	}
	if rows.Next() {
		t.Fatal("rendered quotient corpus returned an unexpected extra row")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func decimalConformanceModel(expression string) *semanticmodel.Model {
	return &semanticmodel.Model{
		Name: "decimal_conformance",
		Tables: map[string]semanticmodel.Table{
			"decimal_datasets": {
				GrainEntity: "dataset",
				Entities:    map[string]semanticmodel.ModelEntitySpec{"dataset": {Type: "primary", Fields: []string{"id"}}},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"id":           {Datatype: semanticmodel.DataTypeInteger},
					"amount":       {Type: "number", Datatype: semanticmodel.DataTypeDecimal},
					"float_amount": {Type: "number", Datatype: semanticmodel.DataTypeFloat},
					"customer_id":  {Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"decimal_datasets": {Model: "decimal_datasets"}},
		Filters: map[string]semanticmodel.SemanticFilterSpec{
			"only_first_row": {Field: "decimal_datasets.id", Operator: "equals", Value: 1},
		},
		Metrics: map[string]semanticmodel.Metric{
			"amount":         {Type: "aggregate", Dataset: "decimal_datasets", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "decimal_datasets.amount"}, Empty: "null"},
			"count":          {Type: "aggregate", Dataset: "decimal_datasets", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "decimal_datasets.id"}, Empty: "zero"},
			"derived":        {Type: "derived", Expression: expression},
			"ratio":          {Type: "ratio", Numerator: "amount", Denominator: "count"},
			"float_amount":   {Type: "aggregate", Dataset: "decimal_datasets", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "decimal_datasets.float_amount"}, Empty: "null"},
			"float_ratio":    {Type: "ratio", Numerator: "float_amount", Denominator: "count"},
			"avg_amount":     {Type: "aggregate", Dataset: "decimal_datasets", Aggregation: "avg", Input: &semanticmodel.MetricInput{Field: "decimal_datasets.amount"}, Where: []string{"only_first_row"}, Empty: "null"},
			"avg_float":      {Type: "aggregate", Dataset: "decimal_datasets", Aggregation: "avg", Input: &semanticmodel.MetricInput{Field: "decimal_datasets.float_amount"}, Empty: "null"},
			"unique_count":   {Type: "aggregate", Dataset: "decimal_datasets", Aggregation: "count_distinct", Input: &semanticmodel.MetricInput{Field: "decimal_datasets.customer_id"}, Where: []string{"only_first_row"}, Empty: "zero"},
			"distinct_ratio": {Type: "ratio", Numerator: "amount", Denominator: "unique_count"},
		},
	}
}

func planColumnValue(columns []string, values []any, name string) any {
	for index, column := range columns {
		if column == name && index < len(values) {
			return values[index]
		}
	}
	return nil
}

func decimalIntermediateOverflowModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name: "decimal_intermediate_overflow",
		Tables: map[string]semanticmodel.Table{
			"decimal_wide": {
				GrainEntity: "row",
				Entities:    map[string]semanticmodel.ModelEntitySpec{"row": {Type: "primary", Fields: []string{"id"}}},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"id":          {Datatype: semanticmodel.DataTypeInteger},
					"left_value":  {Type: "number", Datatype: semanticmodel.DataTypeDecimal},
					"right_value": {Type: "number", Datatype: semanticmodel.DataTypeDecimal},
				},
			},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"decimal_wide": {Model: "decimal_wide"}},
		Metrics: map[string]semanticmodel.Metric{
			"left":  {Type: "aggregate", Dataset: "decimal_wide", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "decimal_wide.left_value"}, Empty: "null"},
			"right": {Type: "aggregate", Dataset: "decimal_wide", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "decimal_wide.right_value"}, Empty: "null"},
			"ratio": {Type: "ratio", Numerator: "left", Denominator: "right"},
		},
	}
}

func decimalQuotientCorpusModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name: "decimal_quotient_corpus",
		Tables: map[string]semanticmodel.Table{
			"decimal_quotients": {
				GrainEntity: "row",
				Entities:    map[string]semanticmodel.ModelEntitySpec{"row": {Type: "primary", Fields: []string{"case_id"}}},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"case_id":     {Datatype: semanticmodel.DataTypeInteger},
					"left_value":  {Type: "number", Datatype: semanticmodel.DataTypeDecimal},
					"right_value": {Type: "number", Datatype: semanticmodel.DataTypeDecimal},
				},
			},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"decimal_quotients": {Model: "decimal_quotients"}},
		Metrics: map[string]semanticmodel.Metric{
			"left":     {Type: "aggregate", Dataset: "decimal_quotients", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "decimal_quotients.left_value"}, Empty: "null"},
			"right":    {Type: "aggregate", Dataset: "decimal_quotients", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "decimal_quotients.right_value"}, Empty: "null"},
			"quotient": {Type: "derived", Expression: "safe_divide(${left}, ${right})"},
		},
	}
}
