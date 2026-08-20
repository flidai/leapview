package duckdbsql

import (
	"context"
	"testing"
)

func TestAnalyzePreservesFunctionMetadata(t *testing.T) {
	query, err := Parse(context.Background(), `SELECT sum(DISTINCT o.amount) FILTER (WHERE o.ok) FROM source.orders o`)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Analyze(query)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Functions) != 1 || result.Functions[0].Name != "sum" || result.Functions[0].Filter == nil {
		t.Fatalf("functions = %#v", result.Functions)
	}
	if result.Relations[0].Alias != "o" {
		t.Fatalf("relations = %#v", result.Relations)
	}
}

func TestAnalyzeScopesCTEsAndMarksForwardReferences(t *testing.T) {
	base := func(name string) *SelectStatement {
		return &SelectStatement{SelectList: []Expression{&ColumnExpression{Names: []string{"id"}}}, From: &BaseTableRelation{Name: name}}
	}
	query := Query{Statements: []Statement{&SelectStatement{
		CTEs: []CTE{
			{Name: "a", Query: base("source_a")},
			{Name: "b", Query: base("a")},
		},
		From: &BaseTableRelation{Name: "b"},
	}}}
	result, err := Analyze(query)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Relations) != 3 {
		t.Fatalf("relations = %#v", result.Relations)
	}
	if !result.Relations[1].CTE || result.Relations[1].CTEDeclarationIndex != 0 || result.Relations[2].CTEDeclarationIndex != 1 {
		t.Fatalf("scoped CTE refs = %#v", result.Relations)
	}

	forward := Query{Statements: []Statement{&SelectStatement{
		CTEs: []CTE{{Name: "first", Query: base("later")}, {Name: "later", Query: base("source")}},
		From: &BaseTableRelation{Name: "first"},
	}}}
	forwardResult, err := Analyze(forward)
	if err != nil {
		t.Fatal(err)
	}
	if !forwardResult.Relations[0].CTEForward || forwardResult.Relations[0].CTE {
		t.Fatalf("forward reference = %#v", forwardResult.Relations[0])
	}
}

func TestAnalyzeNestedShadowingAndRecursiveEvidence(t *testing.T) {
	inner := &SelectStatement{CTEs: []CTE{{Name: "x", Query: &SelectStatement{From: &BaseTableRelation{Name: "source"}}}}, From: &BaseTableRelation{Name: "x"}}
	query := Query{Statements: []Statement{&SelectStatement{
		CTEs: []CTE{{Name: "x", Query: &SelectStatement{From: &BaseTableRelation{Name: "source"}}}},
		From: &SubqueryRelation{Query: inner},
	}}}
	result, err := Analyze(query)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Relations) != 4 || !result.Relations[3].CTE || result.Relations[3].CTEDeclarationIndex != 1 || result.Relations[3].CTEDepth <= result.Relations[0].CTEDepth {
		t.Fatalf("nested shadowing = %#v", result.Relations)
	}

	recursive := Query{Statements: []Statement{&RecursiveCTEStatement{Name: "walk", Left: &SelectStatement{From: &BaseTableRelation{Name: "seed"}}, Right: &SelectStatement{From: &BaseTableRelation{Name: "walk"}}}}}
	recursiveResult, err := Analyze(recursive)
	if err != nil {
		t.Fatal(err)
	}
	if len(recursiveResult.CTEs) != 1 || !recursiveResult.CTEs[0].Recursive || !recursiveResult.Relations[1].CTERecursive {
		t.Fatalf("recursive evidence = %#v %#v", recursiveResult.CTEs, recursiveResult.Relations)
	}
}

func TestAnalyzeCTELookupIsCaseInsensitiveButSpellingPreserved(t *testing.T) {
	query := Query{Statements: []Statement{&SelectStatement{
		CTEs: []CTE{{Name: "OrdersCTE", Query: &SelectStatement{From: &BaseTableRelation{Name: "source"}}}},
		From: &BaseTableRelation{Name: "orderscte"},
	}}}
	result, err := Analyze(query)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Relations) != 2 || !result.Relations[1].CTE || result.Relations[1].CTEDeclarationIndex != 0 {
		t.Fatalf("case-insensitive CTE lookup = %#v", result.Relations)
	}
	if result.Relations[0].CTEDeclarationIndex != -1 {
		t.Fatalf("non-CTE declaration index = %d", result.Relations[0].CTEDeclarationIndex)
	}
	if result.CTEs[0].Name != "OrdersCTE" {
		t.Fatalf("CTE spelling = %#v", result.CTEs)
	}
}
