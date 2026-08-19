package duckdbsql

import (
	"context"
	"testing"
)

func TestWalkVisitsNestedChildren(t *testing.T) {
	query, err := Parse(context.Background(), `SELECT CASE WHEN a > 0 THEN sum(b) ELSE 0 END FROM source.orders`)
	if err != nil {
		t.Fatal(err)
	}
	var expressions, relations int
	if err := Walk(query, WalkCallbacks{Expression: func(Expression) error { expressions++; return nil }, Relation: func(Relation) error { relations++; return nil }}); err != nil {
		t.Fatal(err)
	}
	if expressions < 6 || relations != 1 {
		t.Fatalf("visited expressions=%d relations=%d", expressions, relations)
	}
}

func TestWalkVisitsQueryNodeBaseChildren(t *testing.T) {
	column := &ColumnExpression{Names: []string{"limit_value"}}
	recursive := &RecursiveCTEStatement{
		Modifiers: []Modifier{&LimitModifier{Limit: column}},
		CTEs:      []CTE{{Name: "base", Query: &SelectStatement{SelectList: []Expression{&ColumnExpression{Names: []string{"id"}}}, From: &EmptyRelation{}}}},
		Left:      &SelectStatement{From: &BaseTableRelation{Name: "seed"}},
		Right:     &SelectStatement{From: &BaseTableRelation{Name: "base"}},
	}
	var statements, expressions, ctes int
	if err := Walk(Query{Statements: []Statement{recursive}}, WalkCallbacks{
		Statement:  func(Statement) error { statements++; return nil },
		Expression: func(Expression) error { expressions++; return nil },
		CTE:        func(CTE) error { ctes++; return nil },
	}); err != nil {
		t.Fatal(err)
	}
	if statements < 4 || expressions != 2 || ctes != 1 {
		t.Fatalf("base children not visited: statements=%d expressions=%d ctes=%d", statements, expressions, ctes)
	}
}
