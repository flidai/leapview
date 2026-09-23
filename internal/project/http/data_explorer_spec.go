package http

import (
	"fmt"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

// dataExploreState is the small, renderer-independent view used by the
// existing projection helpers. The canonical ExplorationSpec remains the
// source of truth; this view is only used for field/table inference.
type dataExploreState struct {
	ModelID    *string
	DatasetID  *string
	Dimensions []string
	Metrics    []string
	Filters    []dataExploreFilter
	Sort       []dataExploreSort
	Time       *dataExploreTime
	Limit      int64
}

type dataExploreFilter struct {
	Dataset  *string
	Field    string
	Operator string
	Values   []string
}

type dataExploreSort struct {
	Direction string
	Field     string
}

type dataExploreTime struct {
	Alias *string
	Field string
	Grain string
}

func defaultExplorationSpec() exploration.ExplorationSpec {
	return exploration.ExplorationSpec{
		SchemaVersion: 1,
		Dimensions:    []exploration.ExplorationDimensionRef{},
		Metrics:       []exploration.ExplorationMetricRef{},
		Filters:       []exploration.ExplorationFilter{},
		Sort:          []exploration.ExplorationSort{},
		Limit:         100,
	}
}

func normalizeExplorationSpec(spec exploration.ExplorationSpec) exploration.ExplorationSpec {
	if !explorationSpecCanDefault(spec) {
		return spec
	}
	if spec.SchemaVersion == 0 {
		spec.SchemaVersion = 1
	}
	if spec.Dimensions == nil {
		spec.Dimensions = []exploration.ExplorationDimensionRef{}
	}
	if spec.Metrics == nil {
		spec.Metrics = []exploration.ExplorationMetricRef{}
	}
	if spec.Filters == nil {
		spec.Filters = []exploration.ExplorationFilter{}
	}
	if spec.Sort == nil {
		spec.Sort = []exploration.ExplorationSort{}
	}
	// Zero is the omitted/default value used by incremental commands. Preserve
	// negative and over-limit values so the execution boundary can reject an
	// explicitly malformed canonical spec instead of silently clamping it.
	if spec.Limit == 0 {
		spec.Limit = 100
	}
	return spec
}

// explorationSpecCanDefault is deliberately narrow. Only the uninitialized
// command used by the incremental explorer may acquire canonical empty arrays
// and the default limit. Once a model, dataset, selection, display setting,
// or other operand is authored, missing required arrays and invalid values
// must survive normalization so ValidateShape can reject them.
func explorationSpecCanDefault(spec exploration.ExplorationSpec) bool {
	if spec.SchemaVersion != 0 && spec.SchemaVersion != 1 {
		return false
	}
	return strings.TrimSpace(spec.ModelID) == "" && spec.DatasetID == nil &&
		len(spec.Dimensions) == 0 && len(spec.Metrics) == 0 && len(spec.Filters) == 0 && len(spec.Sort) == 0 &&
		spec.Time == nil && spec.Pivot == nil && spec.Table == nil && spec.Visualization == nil &&
		(spec.Limit == 0 || spec.Limit == int32(dataExplorerDefaultLimit))
}

// explorationSpecIsEmpty recognizes the intentionally empty incremental
// command used while the explorer has no selected model or fields. It must
// not treat any authored operand (including an invalid limit or display
// config) as empty, because those operands require strict validation before
// execution.
func explorationSpecIsEmpty(spec exploration.ExplorationSpec) bool {
	return (spec.SchemaVersion == 0 || spec.SchemaVersion == 1) &&
		strings.TrimSpace(spec.ModelID) == "" && spec.DatasetID == nil &&
		len(spec.Dimensions) == 0 && len(spec.Metrics) == 0 && len(spec.Filters) == 0 && len(spec.Sort) == 0 &&
		spec.Time == nil && spec.Pivot == nil && spec.Table == nil && spec.Visualization == nil &&
		spec.Limit == int32(dataExplorerDefaultLimit)
}

func dataExploreStateFromSpec(spec exploration.ExplorationSpec) dataExploreState {
	state := dataExploreState{
		Dimensions: make([]string, 0, len(spec.Dimensions)),
		Metrics:    make([]string, 0, len(spec.Metrics)),
		Filters:    make([]dataExploreFilter, 0, len(spec.Filters)),
		Sort:       make([]dataExploreSort, 0, len(spec.Sort)),
		Limit:      int64(spec.Limit),
	}
	if strings.TrimSpace(spec.ModelID) != "" {
		state.ModelID = &spec.ModelID
	}
	if spec.DatasetID != nil && strings.TrimSpace(*spec.DatasetID) != "" {
		state.DatasetID = spec.DatasetID
	}
	for _, dimension := range spec.Dimensions {
		state.Dimensions = append(state.Dimensions, dimension.Field)
	}
	for _, metric := range spec.Metrics {
		state.Metrics = append(state.Metrics, metric.Field)
	}
	for _, filter := range spec.Filters {
		item := dataExploreFilter{Field: filter.Field, Dataset: filter.DatasetID}
		if filter.Expression.Value != nil {
			switch expression := filter.Expression.Value.(type) {
			case *exploration.NullCheckExplorationFilterExpression:
				item.Operator = string(expression.Operator)
			case *exploration.SetExplorationFilterExpression:
				item.Operator = string(expression.Operator)
				for _, value := range expression.Values {
					item.Values = append(item.Values, explorationFilterValueString(value))
				}
			case *exploration.ComparisonExplorationFilterExpression:
				item.Operator = string(expression.Operator)
				item.Values = []string{explorationFilterValueString(expression.Value)}
			case *exploration.RangeExplorationFilterExpression:
				item.Operator = "range"
			case *exploration.RelativePeriodExplorationFilterExpression:
				item.Operator = "relative_period"
			case *exploration.UnfilteredExplorationFilterExpression:
				item.Operator = "unfiltered"
			}
		}
		state.Filters = append(state.Filters, item)
	}
	for _, sorting := range spec.Sort {
		state.Sort = append(state.Sort, dataExploreSort{Field: sorting.Field, Direction: string(sorting.Direction)})
	}
	if spec.Time != nil {
		state.Time = &dataExploreTime{Field: spec.Time.Field, Grain: string(spec.Time.Grain), Alias: spec.Time.Alias}
	}
	return state
}

func explorationFilterValueString(value exploration.ExplorationFilterValue) string {
	switch value := value.Value.(type) {
	case *exploration.StringExplorationFilterValue:
		return value.Value
	case *exploration.BooleanExplorationFilterValue:
		return fmt.Sprintf("%t", value.Value)
	case *exploration.IntegerExplorationFilterValue:
		return value.Value
	case *exploration.DecimalExplorationFilterValue:
		return value.Value
	case *exploration.DateExplorationFilterValue:
		return value.Value
	case *exploration.TimestampExplorationFilterValue:
		return value.Value
	default:
		return ""
	}
}

func explorationSpecWithState(spec exploration.ExplorationSpec, state dataExploreState) exploration.ExplorationSpec {
	spec.ModelID = strings.TrimSpace(projectsignals.ValueOrZero(state.ModelID))
	if dataset := strings.TrimSpace(projectsignals.ValueOrZero(state.DatasetID)); dataset != "" {
		spec.DatasetID = &dataset
	} else {
		spec.DatasetID = nil
	}
	oldDimensions := make(map[string]exploration.ExplorationDimensionRef, len(spec.Dimensions))
	for _, dimension := range spec.Dimensions {
		oldDimensions[dimension.Field] = dimension
	}
	spec.Dimensions = make([]exploration.ExplorationDimensionRef, 0, len(state.Dimensions))
	for _, field := range state.Dimensions {
		if dimension, ok := oldDimensions[field]; ok {
			spec.Dimensions = append(spec.Dimensions, dimension)
		} else {
			spec.Dimensions = append(spec.Dimensions, exploration.ExplorationDimensionRef{Field: field})
		}
	}
	oldMetrics := make(map[string]exploration.ExplorationMetricRef, len(spec.Metrics))
	for _, metric := range spec.Metrics {
		oldMetrics[metric.Field] = metric
	}
	spec.Metrics = make([]exploration.ExplorationMetricRef, 0, len(state.Metrics))
	for _, field := range state.Metrics {
		if metric, ok := oldMetrics[field]; ok {
			spec.Metrics = append(spec.Metrics, metric)
		} else {
			spec.Metrics = append(spec.Metrics, exploration.ExplorationMetricRef{Field: field})
		}
	}
	spec.Sort = make([]exploration.ExplorationSort, 0, len(state.Sort))
	for _, sorting := range state.Sort {
		spec.Sort = append(spec.Sort, exploration.ExplorationSort{Field: sorting.Field, Direction: exploration.ExplorationSortDirection(sorting.Direction)})
	}
	if state.Time != nil {
		timeSelection := spec.Time
		if timeSelection == nil {
			timeSelection = &exploration.ExplorationTimeSelection{}
		}
		timeSelection.Field, timeSelection.Grain, timeSelection.Alias = state.Time.Field, exploration.ExplorationTimeGrain(state.Time.Grain), state.Time.Alias
		spec.Time = timeSelection
	} else {
		spec.Time = nil
	}
	spec.Filters = make([]exploration.ExplorationFilter, 0, len(state.Filters))
	for _, filter := range state.Filters {
		values := make([]exploration.ExplorationFilterValue, 0, len(filter.Values))
		for _, value := range filter.Values {
			values = append(values, exploration.ExplorationFilterValue{Value: &exploration.StringExplorationFilterValue{ExplorationFilterValueBase: exploration.ExplorationFilterValueBase{Kind: "string"}, Kind: "string", Value: value}})
		}
		var expression exploration.ExplorationFilterExpressionVariant
		switch filter.Operator {
		case "is_null", "is_not_null":
			expression = &exploration.NullCheckExplorationFilterExpression{ExplorationFilterExpressionBase: exploration.ExplorationFilterExpressionBase{Kind: "null_check"}, Kind: "null_check", Operator: filter.Operator}
		case "in", "not_in":
			expression = &exploration.SetExplorationFilterExpression{ExplorationFilterExpressionBase: exploration.ExplorationFilterExpressionBase{Kind: "set"}, Kind: "set", Operator: filter.Operator, Values: values}
		default:
			if len(values) == 0 {
				continue
			}
			expression = &exploration.ComparisonExplorationFilterExpression{ExplorationFilterExpressionBase: exploration.ExplorationFilterExpressionBase{Kind: "comparison"}, Kind: "comparison", Operator: filter.Operator, Value: values[0]}
		}
		spec.Filters = append(spec.Filters, exploration.ExplorationFilter{Field: filter.Field, DatasetID: filter.Dataset, Expression: exploration.ExplorationFilterExpression{Value: expression}})
	}
	if state.Limit > 0 {
		spec.Limit = int32(state.Limit)
	}
	return spec
}

func dataExploreCommandWithCanonicalSpec(command projectsignals.DataExploreCommand) projectsignals.DataExploreCommand {
	if command.Spec.SchemaVersion == 0 && strings.TrimSpace(command.Spec.ModelID) == "" {
		spec := defaultExplorationSpec()
		state := dataExploreState{
			ModelID: command.SemanticModelID, DatasetID: command.DatasetID,
			Dimensions: append([]string(nil), command.Dimensions...), Metrics: append([]string(nil), command.Metrics...),
			Filters: make([]dataExploreFilter, 0, len(command.Filters)), Sort: make([]dataExploreSort, 0, len(command.Sort)), Limit: command.Limit,
		}
		for _, filter := range command.Filters {
			state.Filters = append(state.Filters, dataExploreFilter{Dataset: filter.DatasetID, Field: filter.Field, Operator: filter.Operator, Values: append([]string(nil), filter.Values...)})
		}
		for _, sort := range command.Sort {
			state.Sort = append(state.Sort, dataExploreSort{Field: sort.Field, Direction: sort.Direction})
		}
		if command.Time != nil {
			state.Time = &dataExploreTime{Field: command.Time.Field, Grain: command.Time.Grain, Alias: command.Time.Alias}
		}
		command.Spec = explorationSpecWithState(spec, state)
		return command
	}
	state := dataExploreStateFromSpec(command.Spec)
	command.SemanticModelID, command.DatasetID = state.ModelID, state.DatasetID
	command.Dimensions, command.Metrics, command.Limit = state.Dimensions, state.Metrics, state.Limit
	command.Filters = make([]projectsignals.DataExploreFilterSignal, 0, len(state.Filters))
	for _, filter := range state.Filters {
		command.Filters = append(command.Filters, projectsignals.DataExploreFilterSignal{DatasetID: filter.Dataset, Field: filter.Field, Operator: filter.Operator, Values: filter.Values})
	}
	command.Sort = make([]projectsignals.DataExploreSortSignal, 0, len(state.Sort))
	for _, sort := range state.Sort {
		command.Sort = append(command.Sort, projectsignals.DataExploreSortSignal{Field: sort.Field, Direction: sort.Direction})
	}
	if state.Time == nil {
		command.Time = nil
	} else {
		command.Time = &projectsignals.DataExploreTimeSignal{Field: state.Time.Field, Grain: state.Time.Grain, Alias: state.Time.Alias}
	}
	return command
}

func dataExploreCommandRefreshSpec(command projectsignals.DataExploreCommand) projectsignals.DataExploreCommand {
	spec := command.Spec
	if spec.SchemaVersion == 0 {
		spec = defaultExplorationSpec()
	}
	state := dataExploreState{
		ModelID: command.SemanticModelID, DatasetID: command.DatasetID,
		Dimensions: append([]string(nil), command.Dimensions...), Metrics: append([]string(nil), command.Metrics...),
		Filters: make([]dataExploreFilter, 0, len(command.Filters)), Sort: make([]dataExploreSort, 0, len(command.Sort)), Limit: command.Limit,
	}
	for _, filter := range command.Filters {
		state.Filters = append(state.Filters, dataExploreFilter{Dataset: filter.DatasetID, Field: filter.Field, Operator: filter.Operator, Values: append([]string(nil), filter.Values...)})
	}
	for _, sort := range command.Sort {
		state.Sort = append(state.Sort, dataExploreSort{Field: sort.Field, Direction: sort.Direction})
	}
	if command.Time != nil {
		state.Time = &dataExploreTime{Field: command.Time.Field, Grain: command.Time.Grain, Alias: command.Time.Alias}
	}
	command.Spec = explorationSpecWithState(spec, state)
	return command
}

func explorationDimensionRefs(spec exploration.ExplorationSpec) []exploration.ExplorationDimensionRef {
	return append([]exploration.ExplorationDimensionRef(nil), spec.Dimensions...)
}

func explorationMetricRefs(spec exploration.ExplorationSpec) []exploration.ExplorationMetricRef {
	return append([]exploration.ExplorationMetricRef(nil), spec.Metrics...)
}
