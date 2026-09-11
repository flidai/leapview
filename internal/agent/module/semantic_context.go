package module

import (
	"context"
	"errors"
	"strings"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/analytics/exploration/lowering"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

// Agent turn references cannot widen semantic access. Resource authorization
// above this boundary still applies; query execution subsequently uses the
// same shared consumer/planner boundary rather than trusting turn metadata.
func authorizeSemanticExploration(ctx context.Context, metrics any, modelID, dataset string, model *semanticmodel.Model, spec *exploration.ExplorationSpec) error {
	if model == nil {
		return errors.New("semantic context model is unavailable")
	}
	port, ok := metrics.(interface {
		SemanticConsumer(context.Context, string) (*semanticquery.SemanticAccessConsumer, error)
	})
	if !ok {
		if model.AccessPolicy.Empty() {
			return nil
		}
		return errors.New("semantic context authority is unavailable")
	}
	consumer, err := port.SemanticConsumer(ctx, modelID)
	if err != nil {
		return err
	}
	if consumer == nil || consumer.Planner() == nil || consumer.Planner().CompiledModel() == nil || !consumer.Planner().CompiledModel().MatchesModel(model) {
		return errors.New("semantic context snapshot is stale")
	}
	if !model.AccessPolicy.Empty() && consumer.ModelID() != modelID {
		return errors.New("semantic context model identity does not match")
	}
	if spec == nil {
		if dataset == "" {
			return errors.New("semantic context requires a dataset or exploration spec")
		}
		if err := consumer.Authorize(semanticquery.SemanticAccessTarget{Dataset: dataset}); err != nil {
			return err
		}
		return nil
	}
	planner := consumer.Planner()
	compiled := planner.CompiledModel()
	sourceModel := compiled.SourceModel()
	if sourceModel == nil {
		return errors.New("semantic context compiled model snapshot is unavailable")
	}
	request, err := semanticExplorationRequest(*spec, sourceModel)
	if err != nil {
		return err
	}
	// Plan through the same immutable compiled graph used for execution. This
	// derives metric dependencies, participating roots, semantic bindings, and
	// policy admission together. In particular, an omitted dataset target is
	// inferred by the planner instead of being sent as an invalid empty access
	// target or silently treated as public.
	if _, err := planner.Plan(request); err != nil {
		return err
	}
	return nil
}

func semanticExplorationRequest(spec exploration.ExplorationSpec, model *semanticmodel.Model) (semanticquery.Request, error) {
	query, err := lowering.QueryForModel(spec, model)
	if err != nil && spec.Pivot != nil {
		// Pivot execution is intentionally not supported by the shared lowering
		// boundary, but its members still need the same root/member admission
		// before an agent context can carry it. Probe the planner with the pivot
		// dimensions and metrics as one aggregate request; this has no execution
		// effect and preserves fail-closed authorization for every pivot member.
		probe := spec
		probe.Pivot = nil
		query, err = lowering.QueryForModel(probe, model)
		if err == nil {
			for _, dimension := range spec.Pivot.Rows {
				query.Fields = appendUniqueDataQueryField(query.Fields, dataquery.Field{Field: dimension.Field, Alias: stringPointerValue(dimension.Alias), Grain: grainPointerValue(dimension.Grain)})
			}
			for _, dimension := range spec.Pivot.Columns {
				query.Fields = appendUniqueDataQueryField(query.Fields, dataquery.Field{Field: dimension.Field, Alias: stringPointerValue(dimension.Alias), Grain: grainPointerValue(dimension.Grain)})
			}
			for _, metric := range spec.Pivot.Metrics {
				query.Metrics = appendUniqueDataQueryField(query.Metrics, dataquery.Field{Field: metric.Field, Alias: stringPointerValue(metric.Alias)})
				if authored, ok := model.Metrics[metric.Field]; ok && strings.TrimSpace(authored.Dataset) == "" {
					// QueryForModel only sees the non-pivot metric list. A
					// multi-root pivot metric must likewise leave the target
					// empty so Planner can infer all of its roots.
					query.Target = ""
				}
			}
		}
	}
	if err != nil {
		return semanticquery.Request{}, err
	}
	return semanticquery.Request{
		Dataset: query.Target, Dimensions: semanticQueryFields(query.Fields), Metrics: semanticQueryFields(query.Metrics),
		Time:    semanticquery.Time{Field: query.Time.Field, Grain: query.Time.Grain, Alias: query.Time.Alias},
		Filters: semanticQueryFilters(query.Filters), Sort: semanticQuerySorts(query.Sort), Limit: query.Limit, Offset: query.Offset,
	}, nil
}

func appendUniqueDataQueryField(fields []dataquery.Field, field dataquery.Field) []dataquery.Field {
	for _, existing := range fields {
		if existing.Field == field.Field {
			return fields
		}
	}
	return append(fields, field)
}

func semanticQueryFields(fields []dataquery.Field) []semanticquery.Field {
	result := make([]semanticquery.Field, 0, len(fields))
	for _, field := range fields {
		result = append(result, semanticquery.Field{Field: field.Field, Alias: field.Alias, Grain: field.Grain})
	}
	return result
}

func semanticQueryFilters(filters []dataquery.Filter) []semanticquery.Filter {
	result := make([]semanticquery.Filter, 0, len(filters))
	for _, filter := range filters {
		groups := make([]semanticquery.FilterGroup, 0, len(filter.Groups))
		for _, group := range filter.Groups {
			groups = append(groups, semanticquery.FilterGroup{Filters: semanticQueryFilters(group.Filters)})
		}
		result = append(result, semanticquery.Filter{
			Field: filter.Field, Dataset: filter.Dataset, Operator: filter.Operator,
			Values: append([]any(nil), filter.Values...), Groups: groups,
		})
	}
	return result
}

func semanticQuerySorts(sorts []dataquery.Sort) []semanticquery.Sort {
	result := make([]semanticquery.Sort, 0, len(sorts))
	for _, sort := range sorts {
		result = append(result, semanticquery.Sort{Field: sort.Field, Direction: sort.Direction})
	}
	return result
}

func stringPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func grainPointerValue(value *exploration.ExplorationTimeGrain) string {
	if value == nil {
		return ""
	}
	return string(*value)
}
