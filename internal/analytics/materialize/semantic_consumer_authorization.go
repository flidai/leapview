package materialize

import (
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

// validateSemanticAuthorizationProjection admits count-only requests through
// the same request planner as the executing consumer. Count plans deliberately
// omit their authorization projection from PlanIR, so without this
// non-executing projection a caller could hide a denied semantic member in a
// count request. Field classification is activation-owned: compiled metrics
// are metrics, and every other reference is resolved by the planner as a
// dimension. Unknown references therefore fail through the existing resolver
// rather than a string heuristic.
func validateSemanticAuthorizationProjection(request dataquery.Query, consumer *semanticquery.SemanticAccessConsumer) error {
	if request.Kind != dataquery.KindSemanticRows || !request.IncludeTotal ||
		len(request.Fields) != 0 || len(request.Metrics) != 0 || len(request.AuthorizationFields) == 0 {
		return nil
	}
	if consumer == nil {
		return fmt.Errorf("semantic authorization projection consumer is unavailable")
	}

	planner := consumer.Planner()
	if planner == nil || planner.CompiledModel() == nil {
		return fmt.Errorf("semantic authorization projection planner is unavailable")
	}
	dimensions := make([]semanticquery.Field, 0, len(request.AuthorizationFields))
	metrics := make([]semanticquery.Field, 0, len(request.AuthorizationFields))
	for _, field := range request.AuthorizationFields {
		if strings.TrimSpace(field.Field) == "" {
			return fmt.Errorf("semantic authorization projection contains an empty field")
		}
		if _, ok := planner.CompiledModel().Metric(field.Field); ok {
			metrics = append(metrics, semanticquery.Field{Field: field.Field, Alias: field.Alias})
			continue
		}
		dimensions = append(dimensions, semanticquery.Field{Field: field.Field, Alias: field.Alias})
	}
	if _, err := planner.Plan(semanticquery.Request{
		Dataset: request.Target, Dimensions: dimensions, Metrics: metrics,
		Filters: dataQueryFilters(request.Filters),
	}); err != nil {
		return fmt.Errorf("authorize semantic count projection: %w", err)
	}
	return nil
}
