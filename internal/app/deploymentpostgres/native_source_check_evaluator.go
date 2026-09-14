package deploymentpostgres

import (
	"context"

	"github.com/flidai/leapview/internal/analytics/gates"
	"github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

// sourceCheckEvaluator keeps the typed gate engine outside the DuckDB package
// while its runtime-owned relation and query session remain live.
func sourceCheckEvaluator(ctx context.Context, id, relation string, checks []semanticmodel.ModelCheck, refs map[string]string, budget materialize.ObservationBudget, query func(context.Context, semanticquery.Plan) (semanticquery.Rows, error)) ([]materialize.SourceCheckEvidence, error) {
	evidence, err := gates.EvaluateSourceChecks(ctx, id, relation, checks, refs, gates.Bounds{MaxQueries: budget.MaxQueries, MaxRows: budget.MaxRows, MaxMillis: budget.MaxMillis}, query)
	result := make([]materialize.SourceCheckEvidence, len(evidence))
	for index, check := range evidence {
		result[index] = materialize.SourceCheckEvidence{Identity: check.Identity, Kind: check.Kind, ResourceID: check.ResourceID, Outcome: string(check.Outcome), Severity: check.Severity, ObservedRows: check.ObservedRows, Queries: check.Queries, ObservationDigest: check.ObservationDigest}
	}
	return result, err
}
