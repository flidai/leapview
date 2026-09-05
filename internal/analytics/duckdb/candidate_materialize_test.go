package duckdb

import (
	"context"
	"strings"
	"testing"

	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

type candidateMaterializeExecutor struct {
	statements []string
}

func (e *candidateMaterializeExecutor) Exec(_ context.Context, statement string) error {
	e.statements = append(e.statements, statement)
	return nil
}

func TestNonCommitterCandidateRuntimeRefreshUsesPreparedSourcesNamespacePlanner(t *testing.T) {
	const namespace = "_candidate_namespace"
	model := &semanticmodel.Model{
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Execution:           semanticmodel.ExecutionDefinition{SQL: "SELECT 1 AS id"},
				SQLAnalysisEvidence: &semanticmodel.SQLAnalysisEvidence{Validated: true},
			},
		},
	}
	prepared := &PreparedSources{model: model}
	var _ analyticsmaterialize.NamespacedModelTablePlanner = prepared
	executor := &candidateMaterializeExecutor{}

	if _, err := refreshModelTablesInNamespace(context.Background(), executor, prepared, model, []string{"orders"}, namespace); err != nil {
		t.Fatal(err)
	}
	if len(executor.statements) != 2 {
		t.Fatalf("candidate refresh executed %d statements, want schema plus table DDL: %v", len(executor.statements), executor.statements)
	}
	if executor.statements[0] != "CREATE SCHEMA IF NOT EXISTS "+namespace {
		t.Fatalf("schema statement = %q", executor.statements[0])
	}
	if !strings.HasPrefix(executor.statements[1], "CREATE OR REPLACE TABLE "+namespace+".orders AS") {
		t.Fatalf("table statement = %q", executor.statements[1])
	}
	if strings.Contains(executor.statements[1], "model.") {
		t.Fatalf("candidate table statement leaked the shared model schema: %q", executor.statements[1])
	}
}
