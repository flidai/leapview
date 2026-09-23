// Package lowering converts the renderer-neutral authored exploration
// contract into a governed dataquery request. It intentionally has no HTTP,
// browser-signal, or project-composition dependencies, so saved explorations
// and browser requests share one fail-closed execution boundary.
package lowering

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

// Query lowers one validated authored spec. Shape validation is repeated here
// because this package is also a standalone boundary; callers that have
// already validated the spec pay only the inexpensive deterministic check.
// Pivot is rejected rather than silently executing a non-pivot aggregate.
func Query(spec exploration.ExplorationSpec) (dataquery.Query, error) {
	return queryForModel(spec, nil)
}

// QueryForModel applies the active semantic projection's multi-root metric
// targeting rule in addition to the renderer-neutral lowering. A metric with
// no single dataset root must leave Target empty so the governed planner can
// infer all metric datasets; it must never be forced into the browser's
// selected dataset.
func QueryForModel(spec exploration.ExplorationSpec, model *semanticmodel.Model) (dataquery.Query, error) {
	return queryForModel(spec, model)
}

func queryForModel(spec exploration.ExplorationSpec, model *semanticmodel.Model) (dataquery.Query, error) {
	if err := exploration.ValidateShape(&spec); err != nil {
		return dataquery.Query{}, fmt.Errorf("invalid exploration spec: %w", err)
	}
	if spec.Pivot != nil {
		return dataquery.Query{}, errors.New("pivot exploration execution is not supported")
	}

	aliases := queryAliases(spec)
	dimensions := make([]dataquery.Field, 0, len(spec.Dimensions))
	timeDecoratesDimension := false
	for _, dimension := range spec.Dimensions {
		alias := pointerString(dimension.Alias)
		grain := pointerGrain(dimension.Grain)
		if spec.Time != nil && dimension.Field == spec.Time.Field {
			timeDecoratesDimension = true
			alias = firstNonEmpty(alias, pointerString(spec.Time.Alias))
			grain = firstNonEmpty(grain, string(spec.Time.Grain))
		}
		dimensions = append(dimensions, dataquery.Field{Field: dimension.Field, Alias: firstNonEmpty(alias, aliases[dimension.Field]), Grain: grain})
	}
	metrics := make([]dataquery.Field, 0, len(spec.Metrics))
	for _, metric := range spec.Metrics {
		metrics = append(metrics, dataquery.Field{Field: metric.Field, Alias: firstNonEmpty(pointerString(metric.Alias), aliases[metric.Field])})
	}
	filters, err := lowerFilters(spec)
	if err != nil {
		return dataquery.Query{}, err
	}
	sorts := make([]dataquery.Sort, 0, len(spec.Sort))
	for _, sort := range spec.Sort {
		sorts = append(sorts, dataquery.Sort{Field: sort.Field, Direction: string(sort.Direction)})
	}
	target := ""
	if spec.DatasetID != nil {
		target = strings.TrimSpace(*spec.DatasetID)
	}
	if model != nil && hasMultiRootMetric(spec, model) {
		target = ""
	}
	query := dataquery.SemanticAggregate(spec.ModelID, target, dimensions, metrics, filters, sorts, 0, int(spec.Limit)+1)
	if spec.Time != nil && !timeDecoratesDimension {
		query.Time = dataquery.Time{Field: spec.Time.Field, Grain: string(spec.Time.Grain), Alias: pointerString(spec.Time.Alias)}
	}
	return query, nil
}

func hasMultiRootMetric(spec exploration.ExplorationSpec, model *semanticmodel.Model) bool {
	if model == nil {
		return false
	}
	for _, selected := range spec.Metrics {
		metric, ok := model.Metrics[selected.Field]
		if ok && strings.TrimSpace(metric.Dataset) == "" {
			return true
		}
	}
	return false
}

func queryAliases(spec exploration.ExplorationSpec) map[string]string {
	fields := make([]string, 0, len(spec.Dimensions)+len(spec.Metrics)+1)
	aliases := make(map[string]string, len(fields))
	dimensionFields := make(map[string]struct{}, len(spec.Dimensions))
	for _, dimension := range spec.Dimensions {
		fields = append(fields, dimension.Field)
		dimensionFields[dimension.Field] = struct{}{}
		if alias := pointerString(dimension.Alias); alias != "" {
			aliases[dimension.Field] = alias
		}
	}
	for _, metric := range spec.Metrics {
		fields = append(fields, metric.Field)
		if alias := pointerString(metric.Alias); alias != "" {
			aliases[metric.Field] = alias
		}
	}
	if spec.Time != nil {
		if _, exists := dimensionFields[spec.Time.Field]; !exists {
			fields = append(fields, spec.Time.Field)
		}
		if alias := pointerString(spec.Time.Alias); alias != "" {
			if _, exists := aliases[spec.Time.Field]; !exists {
				aliases[spec.Time.Field] = alias
			}
		}
	}
	derived := make(map[string]string, len(fields))
	counts := make(map[string]int, len(fields))
	for _, field := range fields {
		counts[fieldName(field)]++
	}
	for _, field := range fields {
		name := fieldName(field)
		if table, value, ok := strings.Cut(field, "."); ok && counts[value] > 1 {
			name = table + "__" + value
		}
		derived[field] = name
	}
	for field, alias := range aliases {
		derived[field] = alias
	}
	return derived
}

func lowerFilters(spec exploration.ExplorationSpec) ([]dataquery.Filter, error) {
	filters := make([]dataquery.Filter, 0, len(spec.Filters)+2)
	for index, authored := range spec.Filters {
		dataset := pointerString(authored.DatasetID)
		if value, ok := authored.Expression.Value.(*exploration.RangeExplorationFilterExpression); ok {
			if value.Lower == nil && value.Upper == nil {
				return nil, fmt.Errorf("filter %d range requires a lower or upper bound", index+1)
			}
			if value.Lower != nil {
				literal, err := filterValue(value.Lower.Value)
				if err != nil {
					return nil, fmt.Errorf("filter %d lower bound: %w", index+1, err)
				}
				op := "greater_than"
				if value.Lower.Inclusive {
					op = "greater_than_or_equal"
				}
				filters = append(filters, dataquery.Filter{Field: authored.Field, Dataset: dataset, Operator: op, Values: []any{literal}})
			}
			if value.Upper != nil {
				literal, err := filterValue(value.Upper.Value)
				if err != nil {
					return nil, fmt.Errorf("filter %d upper bound: %w", index+1, err)
				}
				op := "less_than"
				if value.Upper.Inclusive {
					op = "less_than_or_equal"
				}
				filters = append(filters, dataquery.Filter{Field: authored.Field, Dataset: dataset, Operator: op, Values: []any{literal}})
			}
			continue
		}
		operator, values, err := filterExpression(authored.Expression)
		if err != nil {
			return nil, fmt.Errorf("filter %d: %w", index+1, err)
		}
		if operator != "" {
			filters = append(filters, dataquery.Filter{Field: authored.Field, Dataset: dataset, Operator: operator, Values: values})
		}
	}
	if spec.Time != nil && spec.Time.Range != nil {
		bounds, err := timeRange(spec.Time.Field, *spec.Time.Range)
		if err != nil {
			return nil, err
		}
		filters = append(filters, bounds...)
	}
	return filters, nil
}

func filterExpression(expression exploration.ExplorationFilterExpression) (string, []any, error) {
	switch value := expression.Value.(type) {
	case *exploration.UnfilteredExplorationFilterExpression:
		return "", nil, nil
	case *exploration.NullCheckExplorationFilterExpression:
		return string(value.Operator), nil, nil
	case *exploration.SetExplorationFilterExpression:
		values := make([]any, 0, len(value.Values))
		for _, item := range value.Values {
			literal, err := filterValue(item)
			if err != nil {
				return "", nil, err
			}
			values = append(values, literal)
		}
		return string(value.Operator), values, nil
	case *exploration.ComparisonExplorationFilterExpression:
		literal, err := filterValue(value.Value)
		if err != nil {
			return "", nil, err
		}
		return string(value.Operator), []any{literal}, nil
	case *exploration.RangeExplorationFilterExpression:
		return "", nil, errors.New("range expression must be lowered as two predicates")
	case *exploration.RelativePeriodExplorationFilterExpression:
		return "", nil, errors.New("relative-period filters are not supported by the exploration executor")
	case nil:
		return "", nil, errors.New("filter expression is required")
	default:
		return "", nil, fmt.Errorf("unsupported filter expression %T", value)
	}
}

func filterValue(value exploration.ExplorationFilterValue) (any, error) {
	switch typed := value.Value.(type) {
	case *exploration.StringExplorationFilterValue:
		return typed.Value, nil
	case *exploration.BooleanExplorationFilterValue:
		return typed.Value, nil
	case *exploration.IntegerExplorationFilterValue:
		parsed, err := strconv.ParseInt(typed.Value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid integer value %q", typed.Value)
		}
		return parsed, nil
	case *exploration.DecimalExplorationFilterValue:
		return json.Number(typed.Value), nil
	case *exploration.DateExplorationFilterValue:
		return typed.Value, nil
	case *exploration.TimestampExplorationFilterValue:
		return typed.Value, nil
	case nil:
		return nil, errors.New("filter value is required")
	default:
		return nil, fmt.Errorf("unsupported filter value %T", typed)
	}
}

func timeRange(field string, value exploration.ExplorationTimeRange) ([]dataquery.Filter, error) {
	switch typed := value.Value.(type) {
	case *exploration.AbsoluteExplorationTimeRange:
		filters := make([]dataquery.Filter, 0, 2)
		if typed.Lower != nil {
			literal, err := temporalValue(typed.Lower.Value)
			if err != nil {
				return nil, err
			}
			op := "greater_than"
			if typed.Lower.Inclusive {
				op = "greater_than_or_equal"
			}
			filters = append(filters, dataquery.Filter{Field: field, Operator: op, Values: []any{literal}})
		}
		if typed.Upper != nil {
			literal, err := temporalValue(typed.Upper.Value)
			if err != nil {
				return nil, err
			}
			op := "less_than"
			if typed.Upper.Inclusive {
				op = "less_than_or_equal"
			}
			filters = append(filters, dataquery.Filter{Field: field, Operator: op, Values: []any{literal}})
		}
		return filters, nil
	case *exploration.RelativeExplorationTimeRange:
		return nil, errors.New("relative time ranges are not supported by the exploration executor")
	case nil:
		return nil, errors.New("time range variant is required")
	default:
		return nil, fmt.Errorf("unsupported time range %T", typed)
	}
}

func temporalValue(value exploration.ExplorationTemporalValue) (any, error) {
	switch typed := value.Value.(type) {
	case *exploration.DateExplorationTemporalValue:
		return typed.Value, nil
	case *exploration.TimestampExplorationTemporalValue:
		return typed.Value, nil
	case nil:
		return nil, errors.New("time bound value is required")
	default:
		return nil, fmt.Errorf("unsupported time bound value %T", typed)
	}
}

func pointerString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func pointerGrain(value *exploration.ExplorationTimeGrain) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func fieldName(field string) string {
	if index := strings.LastIndex(field, "."); index >= 0 && index+1 < len(field) {
		return field[index+1:]
	}
	return field
}
