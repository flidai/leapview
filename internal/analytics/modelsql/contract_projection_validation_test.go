package modelsql

import (
	"encoding/json"
	"testing"
)

func TestValidateCanonicalProjectionRejectsCanonicalMutations(t *testing.T) {
	constant := sqlNode{
		Kind: "constant", Class: "CONSTANT", Type: "VALUE_CONSTANT",
		Value: &sqlValue{Kind: "number", Number: "1"}, LogicalType: &sqlType{ID: ""},
	}
	baseRelation := sqlNode{Kind: "base_table", Type: "BASE_TABLE", Name: "orders", Schema: "source"}
	baseSelect := func(expression sqlNode) sqlNode {
		return sqlNode{Kind: "select", Type: "SELECT_NODE", AggregateHandling: "STANDARD_HANDLING", Select: []sqlNode{expression}, From: &baseRelation}
	}
	validSubquery := sqlNode{Kind: "select", Type: "SELECT_NODE", AggregateHandling: "STANDARD_HANDLING", Select: []sqlNode{constant}, From: &baseRelation}
	orderValue := constant

	tests := []struct {
		name string
		ast  sqlAST
	}{
		{
			name: "missing constant value",
			ast:  sqlAST{Statements: []sqlNode{baseSelect(sqlNode{Kind: "constant", Class: "CONSTANT", Type: "VALUE_CONSTANT", LogicalType: &sqlType{ID: ""}})}},
		},
		{
			name: "missing comparison operand",
			ast: sqlAST{Statements: []sqlNode{baseSelect(sqlNode{
				Kind: "comparison", Class: "COMPARISON", Type: "COMPARE_EQUAL", Left: &constant,
			})}},
		},
		{
			name: "operator not wrong arity",
			ast: sqlAST{Statements: []sqlNode{baseSelect(sqlNode{
				Kind: "operator", Class: "OPERATOR", Type: "OPERATOR_NOT", Children: []sqlNode{constant, constant},
			})}},
		},
		{
			name: "empty conjunction",
			ast: sqlAST{Statements: []sqlNode{baseSelect(sqlNode{
				Kind: "conjunction", Class: "CONJUNCTION", Type: "CONJUNCTION_AND",
			})}},
		},
		{
			name: "missing cast logical type id",
			ast: sqlAST{Statements: []sqlNode{baseSelect(sqlNode{
				Kind: "cast", Class: "CAST", Type: "OPERATOR_CAST", Child: &constant, LogicalType: &sqlType{},
			})}},
		},
		{
			name: "ANY missing child",
			ast: sqlAST{Statements: []sqlNode{baseSelect(sqlNode{
				Kind: "subquery_expression", Class: "SUBQUERY", Type: "SUBQUERY", SubqueryType: "ANY", ComparisonType: "COMPARE_EQUAL", Query: &validSubquery,
			})}},
		},
		{
			name: "ANY missing comparison",
			ast: sqlAST{Statements: []sqlNode{baseSelect(sqlNode{
				Kind: "subquery_expression", Class: "SUBQUERY", Type: "SUBQUERY", SubqueryType: "ANY", Child: &constant, Query: &validSubquery,
			})}},
		},
		{
			name: "window expression boundary missing",
			ast: sqlAST{Statements: []sqlNode{baseSelect(sqlNode{
				Kind: "window", Class: "WINDOW", Type: "WINDOW_AGGREGATE", FunctionName: "sum", WindowStart: "EXPR_PRECEDING_ROWS", WindowEnd: "CURRENT_ROW_ROWS", Children: []sqlNode{constant},
			})}},
		},
		{
			name: "invalid order enum",
			ast: sqlAST{Statements: []sqlNode{baseSelect(sqlNode{
				Kind: "function", Class: "FUNCTION", Type: "FUNCTION", FunctionName: "sum", Children: []sqlNode{constant}, Orders: []sqlOrder{{Type: "FUTURE", NullOrder: "ORDER_DEFAULT", Value: orderValue}},
			})}},
		},
		{
			name: "invalid null-order enum",
			ast: sqlAST{Statements: []sqlNode{baseSelect(sqlNode{
				Kind: "function", Class: "FUNCTION", Type: "FUNCTION", FunctionName: "sum", Children: []sqlNode{constant}, Orders: []sqlOrder{{Type: "ORDER_DEFAULT", NullOrder: "FUTURE", Value: orderValue}},
			})}},
		},
		{
			name: "invalid value kind",
			ast: sqlAST{Statements: []sqlNode{baseSelect(sqlNode{
				Kind: "constant", Class: "CONSTANT", Type: "VALUE_CONSTANT", Value: &sqlValue{Kind: "future"}, LogicalType: &sqlType{ID: ""},
			})}},
		},
		{
			name: "invalid logical type id",
			ast: sqlAST{Statements: []sqlNode{baseSelect(sqlNode{
				Kind: "cast", Class: "CAST", Type: "OPERATOR_CAST", Child: &constant, LogicalType: &sqlType{ID: "FUTURE_TYPE"},
			})}},
		},
		{
			name: "invalid number text",
			ast: sqlAST{Statements: []sqlNode{baseSelect(sqlNode{
				Kind: "constant", Class: "CONSTANT", Type: "VALUE_CONSTANT", Value: &sqlValue{Kind: "number", Number: "bogus"}, LogicalType: &sqlType{ID: ""},
			})}},
		},
		{
			name: "invalid relation alias",
			ast: sqlAST{Statements: []sqlNode{{
				Kind: "select", Type: "SELECT_NODE", AggregateHandling: "STANDARD_HANDLING", Select: []sqlNode{constant},
				From: &sqlNode{Kind: "base_table", Type: "BASE_TABLE", Alias: "bad alias", Name: "orders", Schema: "source"},
			}}},
		},
		{
			name: "wrong-kind modifier field",
			ast:  sqlAST{Statements: []sqlNode{baseSelect(constant)}},
		},
		{
			name: "set operation nested invalid modifier",
			ast: sqlAST{Statements: []sqlNode{{
				Kind: "set_operation", Type: "SET_OPERATION_NODE", SetOperation: "UNION",
				Left:  &sqlNode{Kind: "select", Type: "SELECT_NODE", AggregateHandling: "STANDARD_HANDLING", Select: []sqlNode{constant}, From: &baseRelation, Modifiers: []sqlModifier{{Kind: "future"}}},
				Right: &validSubquery,
			}}},
		},
		{
			name: "set operation root invalid modifier",
			ast: sqlAST{Statements: []sqlNode{{
				Kind: "set_operation", Type: "SET_OPERATION_NODE", SetOperation: "UNION", Modifiers: []sqlModifier{{Kind: "future"}},
				Left: &validSubquery, Right: &validSubquery,
			}}},
		},
		{
			name: "multiple statements",
			ast:  sqlAST{Statements: []sqlNode{baseSelect(constant), baseSelect(constant)}},
		},
	}

	// Keep the wrong-kind modifier mutation separate from the table above so
	// its canonical DTO includes a non-empty field that must be role-rejected.
	for index := range tests {
		if tests[index].name == "wrong-kind modifier field" {
			tests[index].ast.Statements[0].Modifiers = []sqlModifier{{Kind: "distinct", Limit: &constant}}
		}
	}
	validEncoded, err := json.Marshal(sqlAST{Statements: []sqlNode{baseSelect(constant)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalProjection(string(validEncoded)); err != nil {
		t.Fatalf("base producer DTO unexpectedly rejected: %v\nprojection = %s", err, validEncoded)
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.ast)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateCanonicalProjection(string(encoded)); err == nil {
				t.Fatalf("accepted malformed canonical producer DTO: %s", encoded)
			}
		})
	}
}
