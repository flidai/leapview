package compiler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestProjectPlanCompilerDeclaresDeterminismFromVolatileExpressions(t *testing.T) {
	deterministic := Project{ID: projectgraph.ResourceID("project"), Connections: map[string]semanticmodel.Connection{"files": {Kind: "managed"}}, Sources: map[string]semanticmodel.Source{"orders": {Connection: "files"}}, Models: map[string]semanticmodel.Table{
		"orders": {Execution: semanticmodel.ExecutionDefinition{Source: "orders"}, SourceDependencies: []string{"orders"}},
	}}
	if plan := planForProject(deterministic); !plan.Deterministic {
		t.Fatal("static source-backed project plan was not declared deterministic")
	}
	for _, expression := range []string{"SELECT id FROM source.orders", "SELECT safe_future_function(id) FROM source.orders", "SELECT now() AS observed_at", "SELECT random() AS sample", "SELECT uuid() AS key"} {
		volatile := deterministic
		volatile.Models = map[string]semanticmodel.Table{"orders": {Execution: semanticmodel.ExecutionDefinition{SQL: expression}}}
		if plan := planForProject(volatile); plan.Deterministic {
			t.Fatalf("authored SQL expression %q was declared deterministic", expression)
		}
	}
}

func TestPlanProjectAgainstArtifactDetectsSQLChangeWithStableGraphIdentity(t *testing.T) {
	files := map[string]string{
		"connections/warehouse.yaml": "apiVersion: leapview.dev/v1\nkind: Connection\nmetadata: {id: connection:warehouse, name: warehouse}\nspec: {type: managed}\n",
		"sources/orders.yaml":        "apiVersion: leapview.dev/v1\nkind: Source\nmetadata: {id: source:orders, name: orders}\nspec: {connection: warehouse, location: {type: path, path: orders.csv, format: csv}}\n",
		"models/orders.yaml":         "apiVersion: leapview.dev/v1\nkind: Model\nmetadata: {id: model:orders, name: orders_model}\nspec: {definition: {type: sql, sql: 'SELECT id FROM source.orders'}, fields: {id: {datatype: Integer}}, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}}\n",
	}
	projectPath := writeFlatProjectFixture(t, files)
	retained, err := LoadProject(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	active, err := projectartifact.NewProject(retained.Graph, retained.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	modelPath := filepath.Join(filepath.Dir(projectPath), "models", "orders.yaml")
	modelBytes, err := os.ReadFile(modelPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(modelBytes), "SELECT id FROM source.orders", "SELECT id, id AS changed FROM source.orders", 1)
	if err := os.WriteFile(modelPath, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanProjectAgainstArtifact(projectPath, active)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Summary.MaterializationImpact {
		t.Fatalf("stable graph SQL change omitted materialization impact: %#v", plan)
	}
}
