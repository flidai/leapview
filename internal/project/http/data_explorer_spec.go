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
	if state.Limit > 0 {
		spec.Limit = int32(state.Limit)
	}
	return spec
}

func explorationDimensionRefs(spec exploration.ExplorationSpec) []exploration.ExplorationDimensionRef {
	return append([]exploration.ExplorationDimensionRef(nil), spec.Dimensions...)
}

func explorationMetricRefs(spec exploration.ExplorationSpec) []exploration.ExplorationMetricRef {
	return append([]exploration.ExplorationMetricRef(nil), spec.Metrics...)
}
