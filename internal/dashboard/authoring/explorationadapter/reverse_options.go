package explorationadapter

import (
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard/document"
)

// ReverseOptionsForActiveModel derives the server-only facts needed to
// reverse one authored dashboard visual. The dashboard document supplies
// semantic/output names; the active model and compiled serving planner supply
// the physical lineage. No result rows, SQL, or dashboard preview state are
// consulted.
//
// The result is intentionally fail-closed when a dashboard query spans more
// than one metric root or a semantic field has no unique active binding. A
// canonical exploration has one optional dataset root, so guessing would
// change the query's meaning.
func ReverseOptionsForActiveModel(value document.DashboardDocument, visualID string, model *semanticmodel.Model, compiled *semanticquery.CompiledModel) (ReverseOptions, error) {
	return ReverseOptionsForActiveModelAtTarget(value, visualID, "", model, compiled)
}

// ReverseOptionsForActiveModelAtTarget is the component-scoped variant used by
// the authenticated dashboard handoff. The target is part of the authored
// source binding identity, so filters for another placement of the same visual
// are never folded into this exploration.
func ReverseOptionsForActiveModelAtTarget(value document.DashboardDocument, visualID, filterTarget string, model *semanticmodel.Model, compiled *semanticquery.CompiledModel) (ReverseOptions, error) {
	if model == nil || compiled == nil {
		return ReverseOptions{}, fmt.Errorf("active semantic model and compiled planner are required")
	}
	visual, ok := value.Spec.Visuals[strings.TrimSpace(visualID)]
	if !ok {
		return ReverseOptions{}, fmt.Errorf("visual %q is not present in dashboard document", strings.TrimSpace(visualID))
	}
	dimensions, metrics, err := reverseSelectionNames(visual)
	if err != nil {
		return ReverseOptions{}, err
	}
	roots := map[string]struct{}{}
	for index, name := range metrics {
		if _, ok := model.Metrics[name]; !ok {
			return ReverseOptions{}, fmt.Errorf("dashboard metric %q is not present in active semantic model", name)
		}
		metric, ok := compiled.Metric(name)
		if !ok {
			return ReverseOptions{}, fmt.Errorf("dashboard metric %q is not present in active compiled planner", name)
		}
		if len(metric.RootDatasets) != 1 || strings.TrimSpace(metric.RootDatasets[0]) == "" {
			return ReverseOptions{}, fmt.Errorf("dashboard metric %q has ambiguous active root lineage", name)
		}
		roots[strings.TrimSpace(metric.RootDatasets[0])] = struct{}{}
		_ = index
	}
	if len(roots) > 1 {
		return ReverseOptions{}, fmt.Errorf("dashboard visual metrics span multiple dataset roots")
	}
	options := ReverseOptions{ModelID: strings.TrimSpace(value.Spec.SemanticModel), Bindings: map[string]string{}, FilterDatasets: map[string]string{}, FilterTarget: strings.TrimSpace(filterTarget)}
	if len(roots) == 1 {
		for root := range roots {
			if _, ok := compiled.Dataset(root); !ok {
				return ReverseOptions{}, fmt.Errorf("dashboard metric root dataset %q is not present in active compiled planner", root)
			}
			rootCopy := root
			options.DatasetID = &rootCopy
		}
	}
	for _, name := range metrics {
		// Canonical metric references retain semantic metric identity. A
		// dashboard query has no physical metric field to reverse.
		options.Bindings[name] = name
	}
	filterValues := targetedDashboardFilters(value.Spec.Filters, strings.TrimSpace(visualID), options.FilterTarget)
	for _, filter := range filterValues {
		if strings.TrimSpace(filter.ID) != "" && options.DatasetID != nil {
			options.FilterDatasets[filter.ID] = *options.DatasetID
		}
		if strings.TrimSpace(filter.Dimension) != "" {
			dimensions = append(dimensions, strings.TrimSpace(filter.Dimension))
		}
	}
	seenDimensions := map[string]struct{}{}
	for _, name := range dimensions {
		name = strings.TrimSpace(name)
		if name == "" {
			return ReverseOptions{}, fmt.Errorf("dashboard dimension name is required")
		}
		if _, seen := seenDimensions[name]; seen {
			continue
		}
		seenDimensions[name] = struct{}{}
		field, err := reverseActiveField(name, model, compiled, options.DatasetID)
		if err != nil {
			return ReverseOptions{}, err
		}
		options.Bindings[name] = field
		if options.DatasetID != nil {
			options.FilterDatasets[name] = *options.DatasetID
		}
	}
	if timeField, ok := reverseTimeField(visual, options.Bindings); ok {
		options.TimeField = timeField
	}
	return options, nil
}

func reverseSelectionNames(value document.DashboardVisual) ([]string, []string, error) {
	var dimensions []document.DashboardDimensionSelection
	var metrics []document.DashboardMetricSelection
	switch query := value.Query.Value.(type) {
	case *document.AggregateDashboardQuery:
		if query == nil {
			return nil, nil, fmt.Errorf("aggregate query is nil")
		}
		dimensions, metrics = query.Dimensions, query.Metrics
	case *document.PivotDashboardQuery:
		if query == nil {
			return nil, nil, fmt.Errorf("pivot query is nil")
		}
		dimensions = append(append([]document.DashboardDimensionSelection{}, query.Rows...), query.Columns...)
		metrics = query.Metrics
	default:
		return nil, nil, fmt.Errorf("dashboard query %T cannot be represented by an exploration", value.Query.Value)
	}
	dimensionNames := make([]string, 0, len(dimensions))
	for index, selection := range dimensions {
		name, err := reverseDimensionSelectionName(selection)
		if err != nil {
			return nil, nil, fmt.Errorf("dashboard dimension %d: %w", index+1, err)
		}
		dimensionNames = append(dimensionNames, name)
	}
	metricNames := make([]string, 0, len(metrics))
	for index, selection := range metrics {
		name, err := reverseMetricSelectionName(selection)
		if err != nil {
			return nil, nil, fmt.Errorf("dashboard metric %d: %w", index+1, err)
		}
		metricNames = append(metricNames, name)
	}
	return dimensionNames, metricNames, nil
}

func reverseDimensionSelectionName(value document.DashboardDimensionSelection) (string, error) {
	if value.Reference != nil {
		if value.Reference == nil || strings.TrimSpace(value.Reference.Dimension) == "" {
			return "", fmt.Errorf("dimension reference is required")
		}
		return strings.TrimSpace(value.Reference.Dimension), nil
	}
	if value.String != nil && strings.TrimSpace(*value.String) != "" {
		return strings.TrimSpace(*value.String), nil
	}
	return "", fmt.Errorf("dimension selection variant is required")
}

func reverseMetricSelectionName(value document.DashboardMetricSelection) (string, error) {
	if value.Reference != nil {
		if value.Reference == nil || strings.TrimSpace(value.Reference.Metric) == "" {
			return "", fmt.Errorf("metric reference is required")
		}
		return strings.TrimSpace(value.Reference.Metric), nil
	}
	if value.String != nil && strings.TrimSpace(*value.String) != "" {
		return strings.TrimSpace(*value.String), nil
	}
	return "", fmt.Errorf("metric selection variant is required")
}

func reverseActiveField(name string, model *semanticmodel.Model, compiled *semanticquery.CompiledModel, datasetID *string) (string, error) {
	if dimension, ok := model.Dimensions[name]; ok {
		if datasetID != nil {
			binding, found := compiled.DimensionBinding(name, *datasetID)
			if !found || strings.TrimSpace(binding.Physical.Field) == "" {
				return "", fmt.Errorf("semantic dimension %q has no active binding for dataset %q", name, *datasetID)
			}
			return strings.TrimSpace(binding.Physical.Field), nil
		}
		fields := map[string]struct{}{}
		datasets := make([]string, 0, len(dimension.Bindings))
		for dataset := range dimension.Bindings {
			datasets = append(datasets, dataset)
		}
		sort.Strings(datasets)
		for _, dataset := range datasets {
			binding, found := compiled.DimensionBinding(name, dataset)
			if found && strings.TrimSpace(binding.Physical.Field) != "" {
				fields[strings.TrimSpace(binding.Physical.Field)] = struct{}{}
			}
		}
		if len(fields) != 1 {
			return "", fmt.Errorf("semantic dimension %q has no unique active binding", name)
		}
		for field := range fields {
			return field, nil
		}
	}
	if datasetID != nil {
		if binding, ok := compiled.FieldBinding(*datasetID, name); ok && strings.TrimSpace(binding.Physical.Field) != "" {
			return strings.TrimSpace(binding.Physical.Field), nil
		}
	}
	if _, ok := compiled.PhysicalField(name); ok {
		return name, nil
	}
	return "", fmt.Errorf("dashboard dimension %q is not present in the active semantic model", name)
}

func reverseTimeField(value document.DashboardVisual, bindings map[string]string) (string, bool) {
	var selections []document.DashboardDimensionSelection
	switch query := value.Query.Value.(type) {
	case *document.AggregateDashboardQuery:
		if query == nil {
			return "", false
		}
		selections = query.Dimensions
	case *document.PivotDashboardQuery:
		if query == nil {
			return "", false
		}
		selections = append(append([]document.DashboardDimensionSelection{}, query.Rows...), query.Columns...)
	}
	var field string
	for _, selection := range selections {
		if selection.Reference == nil || selection.Reference.Grain == nil {
			continue
		}
		name := strings.TrimSpace(selection.Reference.Dimension)
		candidate := strings.TrimSpace(bindings[name])
		if candidate == "" {
			continue
		}
		if field != "" && field != candidate {
			return "", false
		}
		field = candidate
	}
	return field, field != ""
}
