package compiler

import (
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestTranslatedTablesForRuntimeRewritesOnlySourceRelations(t *testing.T) {
	input := map[string]semanticmodel.Table{
		"orders_model": {
			Execution: semanticmodel.ExecutionDefinition{
				Source: "orders",
				SQL:    "-- source.orders\nWITH source_orders AS (SELECT 'source.orders' AS note)\nSELECT o.id, source_orders.note FROM source.orders AS o",
			},
			SourceDependencies: []string{"orders"},
		},
	}

	got, err := translatedTablesForRuntime(input, map[string]string{"orders": "orders_runtime"})
	if err != nil {
		t.Fatal(err)
	}
	if got["orders_model"].Execution.Source != "orders_runtime" {
		t.Fatalf("runtime execution source = %q, want orders_runtime", got["orders_model"].Execution.Source)
	}
	wantSQL := "-- source.orders\nWITH source_orders AS (SELECT 'source.orders' AS note)\nSELECT o.id, source_orders.note FROM source.orders_runtime AS o"
	if got["orders_model"].Execution.SQL != wantSQL {
		t.Fatalf("runtime SQL = %q, want %q", got["orders_model"].Execution.SQL, wantSQL)
	}
	if strings.Contains(got["orders_model"].Execution.SQL, "source.orders_runtime'") {
		t.Fatal("runtime rewrite changed a string literal")
	}
	if got["orders_model"].SourceDependencies[0] != "orders_runtime" {
		t.Fatalf("runtime source dependency = %#v, want orders_runtime", got["orders_model"].SourceDependencies)
	}
	if input["orders_model"].Execution.SQL != "-- source.orders\nWITH source_orders AS (SELECT 'source.orders' AS note)\nSELECT o.id, source_orders.note FROM source.orders AS o" {
		t.Fatal("runtime translation mutated its input SQL")
	}
}

func TestTranslatedTablesForRuntimeDoesNotShareMutableTables(t *testing.T) {
	input := map[string]semanticmodel.Table{
		"orders_model": {SourceDependencies: []string{"orders"}},
	}
	first, err := translatedTablesForRuntime(input, map[string]string{"orders": "orders_runtime"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := translatedTablesForRuntime(input, map[string]string{"orders": "orders_runtime"})
	if err != nil {
		t.Fatal(err)
	}
	first["orders_model"].SourceDependencies[0] = "changed"
	if second["orders_model"].SourceDependencies[0] != "orders_runtime" {
		t.Fatalf("translated table dependencies share mutable state: %#v", second["orders_model"].SourceDependencies)
	}
	if input["orders_model"].SourceDependencies[0] != "orders" {
		t.Fatalf("translated table mutated input dependencies: %#v", input["orders_model"].SourceDependencies)
	}
}

func TestCanonicalRefResolvesSemanticModelNameThroughGenericIndex(t *testing.T) {
	project := sourceAssembly{
		ResourceIDs: map[string]string{"semantic_model:sales": "semantic:sales"},
		ResourceIDOwners: map[string]string{
			"semantic:sales": "semantic_model:sales",
		},
	}
	if got := canonicalRef(project, "semantic_model", "sales"); got != "semantic:sales" {
		t.Fatalf("canonicalRef() = %q, want semantic:sales", got)
	}
	if got := canonicalRef(project, "semantic_model", "semantic:sales"); got != "semantic:sales" {
		t.Fatalf("canonicalRef() with ID = %q, want semantic:sales", got)
	}
}
