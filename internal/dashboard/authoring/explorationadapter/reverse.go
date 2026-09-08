package explorationadapter

// This file contains the dashboard -> exploration handoff.  It intentionally
// consumes only authored DashboardDocument values.  In particular, it never
// consults rendered rows, generated SQL, or a dashboard result cache.

import (
	"fmt"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

// ReverseOptions supplies the canonical identity and semantic bindings which
// cannot be recovered from a dashboard document.  Bindings maps a dashboard
// field (or its output alias) to its canonical exploration field. It is
// intentionally one-way: callers provide the dashboard identifier as the
// map key instead of relying on an inferred inverse of forward bindings.
type ReverseOptions struct {
	ModelID        string
	DatasetID      *string
	Bindings       map[string]string
	FilterDatasets map[string]string
	TimeField      string
	// FilterTarget is an optional page/component identity (for example
	// "overview/revenue-card"). When present, filters targeted at that exact
	// placement are included alongside global and visual-targeted filters.
	// It is set only by the authenticated dashboard handoff.
	FilterTarget string
	Filters      []document.DashboardFilter
}

// FromDashboardDocument converts one named authored visual and the filters
// that target it into a canonical exploration.  A visual target list is an
// allow-list: filters targeted at another visual are not copied.
func FromDashboardDocument(value document.DashboardDocument, visualID string, options ReverseOptions) (exploration.ExplorationSpec, error) {
	visualID = strings.TrimSpace(visualID)
	if visualID == "" {
		return exploration.ExplorationSpec{}, fmt.Errorf("visual id is required")
	}
	visual, ok := value.Spec.Visuals[visualID]
	if !ok {
		return exploration.ExplorationSpec{}, fmt.Errorf("visual %q is not present in dashboard document", visualID)
	}
	if strings.TrimSpace(value.Spec.SemanticModel) == "" {
		return exploration.ExplorationSpec{}, fmt.Errorf("dashboard semantic model is required")
	}
	if strings.TrimSpace(options.ModelID) == "" {
		options.ModelID = strings.TrimSpace(value.Spec.SemanticModel)
	} else if strings.TrimSpace(options.ModelID) != strings.TrimSpace(value.Spec.SemanticModel) {
		return exploration.ExplorationSpec{}, fmt.Errorf("dashboard semantic model %q conflicts with model %q", value.Spec.SemanticModel, options.ModelID)
	}
	options.Filters = targetedDashboardFilters(value.Spec.Filters, visualID, options.FilterTarget)
	return FromDashboardVisual(visual, options)
}

// FromDashboardVisual converts a standalone authored visual.  Callers using
// a standalone visual should provide ModelID and Filters in ReverseOptions.
func FromDashboardVisual(value document.DashboardVisual, options ReverseOptions) (exploration.ExplorationSpec, error) {
	if strings.TrimSpace(options.ModelID) == "" {
		return exploration.ExplorationSpec{}, fmt.Errorf("model id is required; dashboard visual has no model identity")
	}
	if value.Datasets != nil {
		return exploration.ExplorationSpec{}, fmt.Errorf("visual datasets are not representable by one canonical exploration")
	}
	if value.Metadata != nil {
		return exploration.ExplorationSpec{}, fmt.Errorf("visual metadata bindings are not representable by one canonical exploration")
	}
	if value.Description != nil && strings.TrimSpace(*value.Description) != "" {
		return exploration.ExplorationSpec{}, fmt.Errorf("visual description is not representable by one canonical exploration")
	}
	if value.Accessibility != nil {
		return exploration.ExplorationSpec{}, fmt.Errorf("visual accessibility metadata is not representable by one canonical exploration")
	}
	if value.Calculations != nil && len(*value.Calculations) != 0 {
		return exploration.ExplorationSpec{}, fmt.Errorf("visual calculations are not representable by one canonical exploration")
	}
	if value.Interactions != nil && len(*value.Interactions) != 0 {
		return exploration.ExplorationSpec{}, fmt.Errorf("visual interactions are not representable by one canonical exploration")
	}

	ctx := reverseConverter{options: options}
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1,
		ModelID:       strings.TrimSpace(options.ModelID),
		DatasetID:     cloneString(options.DatasetID),
		Dimensions:    []exploration.ExplorationDimensionRef{},
		Metrics:       []exploration.ExplorationMetricRef{},
		Filters:       []exploration.ExplorationFilter{},
		Sort:          []exploration.ExplorationSort{},
		Limit:         1000,
	}

	outputs := map[string]string{}
	var err error
	switch query := value.Query.Value.(type) {
	case *document.AggregateDashboardQuery:
		if query == nil {
			return exploration.ExplorationSpec{}, fmt.Errorf("aggregate query is nil")
		}
		if query.Type != "" && query.Type != "aggregate" {
			return exploration.ExplorationSpec{}, fmt.Errorf("aggregate query has type %q", query.Type)
		}
		spec.Dimensions, err = ctx.dimensions(query.Dimensions, outputs)
		if err != nil {
			return exploration.ExplorationSpec{}, fmt.Errorf("aggregate dimensions: %w", err)
		}
		spec.Metrics, err = ctx.metrics(query.Metrics, outputs)
		if err != nil {
			return exploration.ExplorationSpec{}, fmt.Errorf("aggregate metrics: %w", err)
		}
		spec.Sort, err = ctx.sorts(query.Sort, outputs, selectionAliases(query.Dimensions, query.Metrics))
		if err != nil {
			return exploration.ExplorationSpec{}, fmt.Errorf("aggregate sort: %w", err)
		}
		if query.Limit != nil {
			spec.Limit = *query.Limit
		}
	case *document.PivotDashboardQuery:
		if query == nil {
			return exploration.ExplorationSpec{}, fmt.Errorf("pivot query is nil")
		}
		if query.Type != "" && query.Type != "pivot" {
			return exploration.ExplorationSpec{}, fmt.Errorf("pivot query has type %q", query.Type)
		}
		rows, rowsErr := ctx.dimensions(query.Rows, outputs)
		if rowsErr != nil {
			return exploration.ExplorationSpec{}, fmt.Errorf("pivot rows: %w", rowsErr)
		}
		columns, columnsErr := ctx.dimensions(query.Columns, outputs)
		if columnsErr != nil {
			return exploration.ExplorationSpec{}, fmt.Errorf("pivot columns: %w", columnsErr)
		}
		metrics, metricsErr := ctx.metrics(query.Metrics, outputs)
		if metricsErr != nil {
			return exploration.ExplorationSpec{}, fmt.Errorf("pivot metrics: %w", metricsErr)
		}
		pivotSort, sortErr := ctx.sorts(query.Sort, outputs, selectionAliases(append(append([]document.DashboardDimensionSelection{}, query.Rows...), query.Columns...), query.Metrics))
		if sortErr != nil {
			return exploration.ExplorationSpec{}, fmt.Errorf("pivot sort: %w", sortErr)
		}
		var pivotSortPtr *[]exploration.ExplorationSort
		if query.Sort != nil {
			pivotSortPtr = &pivotSort
		}
		spec.Pivot = &exploration.ExplorationPivotConfig{Rows: rows, Columns: columns, Metrics: metrics, Sort: pivotSortPtr}
		if query.Totals != nil {
			spec.Pivot.Totals = &exploration.ExplorationPivotTotals{Rows: cloneBool(query.Totals.Rows), Columns: cloneBool(query.Totals.Columns), Grand: cloneBool(query.Totals.Grand)}
		}
		if query.Window != nil {
			spec.Pivot.Window = &exploration.ExplorationPivotWindow{Offset: cloneInt32(query.Window.Offset), Limit: query.Window.Limit}
			spec.Limit = query.Window.Limit
		}
	default:
		if value.Query.Value == nil {
			return exploration.ExplorationSpec{}, fmt.Errorf("visual query is required")
		}
		return exploration.ExplorationSpec{}, fmt.Errorf("dashboard query %T cannot be represented by an exploration", value.Query.Value)
	}
	if spec.Limit < 1 || spec.Limit > 1000 {
		return exploration.ExplorationSpec{}, fmt.Errorf("visual query limit %d is outside exploration bounds", spec.Limit)
	}
	if value.DataBudget != nil {
		if value.DataBudget.MaxRows > 0 && value.DataBudget.MaxRows != spec.Limit {
			return exploration.ExplorationSpec{}, fmt.Errorf("visual data budget maxRows %d is not represented by query limit %d", value.DataBudget.MaxRows, spec.Limit)
		}
		if value.DataBudget.RequiredCompleteness != nil && *value.DataBudget.RequiredCompleteness != visualizationir.VisualizationCompletenessComplete {
			return exploration.ExplorationSpec{}, fmt.Errorf("visual data budget completeness %q is not representable by exploration", *value.DataBudget.RequiredCompleteness)
		}
	}

	spec.Filters, spec.Time, err = ctx.filters(options.Filters, spec.Dimensions, spec.Pivot, outputs)
	if err != nil {
		return exploration.ExplorationSpec{}, fmt.Errorf("dashboard filters: %w", err)
	}
	spec.Visualization, err = ctx.visualization(value, outputs, spec)
	if err != nil {
		return exploration.ExplorationSpec{}, err
	}
	if table := reverseTableConfig(value.Presentation); table != nil {
		spec.Table = table
	}
	if err := exploration.ValidateShape(&spec); err != nil {
		return exploration.ExplorationSpec{}, fmt.Errorf("canonical exploration shape: %w", err)
	}
	return spec, nil
}

type reverseConverter struct{ options ReverseOptions }

func targetedDashboardFilters(values []document.DashboardFilter, visualID, filterTarget string) []document.DashboardFilter {
	result := make([]document.DashboardFilter, 0, len(values))
	visualID = strings.TrimSpace(visualID)
	filterTarget = strings.TrimSpace(filterTarget)
	for _, value := range values {
		if value.Targets == nil {
			result = append(result, value)
			continue
		}
		for _, target := range *value.Targets {
			target = strings.TrimSpace(target)
			if target == visualID || (filterTarget != "" && target == filterTarget) {
				result = append(result, value)
				break
			}
		}
	}
	return result
}

func (c reverseConverter) field(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("field is required")
	}
	if mapped := strings.TrimSpace(c.options.Bindings[value]); mapped != "" {
		return mapped, nil
	}
	return value, nil
}

func (c reverseConverter) dimension(value document.DashboardDimensionSelection) (exploration.ExplorationDimensionRef, string, error) {
	if value.Reference != nil {
		field, err := c.field(value.Reference.Dimension)
		if err != nil {
			return exploration.ExplorationDimensionRef{}, "", err
		}
		alias := cloneString(value.Reference.Alias)
		if alias == nil && strings.TrimSpace(value.Reference.Dimension) != field {
			// A semantic dashboard name can lower to a qualified canonical
			// field. Retain the dashboard output as an explicit canonical
			// alias so visualization references remain selected fields.
			output := strings.TrimSpace(value.Reference.Dimension)
			alias = &output
		}
		result := exploration.ExplorationDimensionRef{Field: field, Alias: alias}
		if value.Reference.Grain != nil {
			grain := exploration.ExplorationTimeGrain(*value.Reference.Grain)
			if !validExplorationGrain(grain) {
				return exploration.ExplorationDimensionRef{}, "", fmt.Errorf("unsupported dashboard time grain %q", *value.Reference.Grain)
			}
			result.Grain = &grain
		}
		return result, dimensionOutput(value), nil
	}
	if value.String != nil {
		field, err := c.field(*value.String)
		alias := (*string)(nil)
		output := strings.TrimSpace(*value.String)
		if err == nil && output != field {
			alias = &output
		}
		return exploration.ExplorationDimensionRef{Field: field, Alias: alias}, output, err
	}
	return exploration.ExplorationDimensionRef{}, "", fmt.Errorf("dimension selection variant is required")
}

func (c reverseConverter) dimensions(values []document.DashboardDimensionSelection, outputs map[string]string) ([]exploration.ExplorationDimensionRef, error) {
	result := make([]exploration.ExplorationDimensionRef, 0, len(values))
	for index, value := range values {
		converted, output, err := c.dimension(value)
		if err != nil {
			return nil, fmt.Errorf("dimension %d: %w", index, err)
		}
		if output == "" {
			return nil, fmt.Errorf("dimension %d has no output", index)
		}
		if prior, exists := outputs[output]; exists && prior != converted.Field {
			return nil, fmt.Errorf("dimension output %q is ambiguous", output)
		}
		outputs[output] = converted.Field
		result = append(result, converted)
	}
	return result, nil
}

func (c reverseConverter) metric(value document.DashboardMetricSelection) (exploration.ExplorationMetricRef, string, error) {
	if value.Reference != nil {
		field, err := c.field(value.Reference.Metric)
		alias := cloneString(value.Reference.Alias)
		if alias == nil && strings.TrimSpace(value.Reference.Metric) != field {
			output := strings.TrimSpace(value.Reference.Metric)
			alias = &output
		}
		return exploration.ExplorationMetricRef{Field: field, Alias: alias}, metricOutput(value), err
	}
	if value.String != nil {
		field, err := c.field(*value.String)
		alias := (*string)(nil)
		output := strings.TrimSpace(*value.String)
		if err == nil && output != field {
			alias = &output
		}
		return exploration.ExplorationMetricRef{Field: field, Alias: alias}, output, err
	}
	return exploration.ExplorationMetricRef{}, "", fmt.Errorf("metric selection variant is required")
}

func (c reverseConverter) metrics(values []document.DashboardMetricSelection, outputs map[string]string) ([]exploration.ExplorationMetricRef, error) {
	result := make([]exploration.ExplorationMetricRef, 0, len(values))
	for index, value := range values {
		converted, output, err := c.metric(value)
		if err != nil {
			return nil, fmt.Errorf("metric %d: %w", index, err)
		}
		if output == "" {
			return nil, fmt.Errorf("metric %d has no output", index)
		}
		if prior, exists := outputs[output]; exists && prior != converted.Field {
			return nil, fmt.Errorf("metric output %q is ambiguous", output)
		}
		outputs[output] = converted.Field
		result = append(result, converted)
	}
	return result, nil
}

func selectionAliases(dimensions []document.DashboardDimensionSelection, metrics []document.DashboardMetricSelection) map[string]bool {
	result := make(map[string]bool)
	for _, value := range dimensions {
		if value.Reference != nil && value.Reference.Alias != nil && strings.TrimSpace(*value.Reference.Alias) != "" {
			result[strings.TrimSpace(*value.Reference.Alias)] = true
		}
	}
	for _, value := range metrics {
		if value.Reference != nil && value.Reference.Alias != nil && strings.TrimSpace(*value.Reference.Alias) != "" {
			result[strings.TrimSpace(*value.Reference.Alias)] = true
		}
	}
	return result
}

func (c reverseConverter) sorts(values *[]document.DashboardSort, outputs map[string]string, aliases map[string]bool) ([]exploration.ExplorationSort, error) {
	result := []exploration.ExplorationSort{}
	if values == nil {
		return result, nil
	}
	for index, value := range *values {
		field := strings.TrimSpace(value.Field)
		if field == "" {
			return nil, fmt.Errorf("sort %d field is required", index)
		}
		if aliases[field] {
			// A dashboard sort names the selected output. Keep an explicit
			// alias in the canonical sort so alias identity is not lost.
		} else if canonical, ok := outputs[field]; ok {
			field = canonical
		} else {
			var err error
			field, err = c.field(field)
			if err != nil {
				return nil, fmt.Errorf("sort %d: %w", index, err)
			}
		}
		direction := exploration.ExplorationSortDirection(value.Direction)
		if direction != exploration.ExplorationSortDirectionAsc && direction != exploration.ExplorationSortDirectionDesc {
			return nil, fmt.Errorf("sort %d has unsupported direction %q", index, value.Direction)
		}
		result = append(result, exploration.ExplorationSort{Field: field, Direction: direction})
	}
	return result, nil
}

func validExplorationGrain(value exploration.ExplorationTimeGrain) bool {
	switch value {
	case exploration.ExplorationTimeGrainSecond, exploration.ExplorationTimeGrainMinute, exploration.ExplorationTimeGrainHour, exploration.ExplorationTimeGrainDay, exploration.ExplorationTimeGrainWeek, exploration.ExplorationTimeGrainMonth, exploration.ExplorationTimeGrainQuarter, exploration.ExplorationTimeGrainYear:
		return true
	default:
		return false
	}
}

func reverseTableConfig(value document.DashboardPresentation) *exploration.ExplorationTableDisplayConfig {
	presentation, ok := value.Value.(*document.TableDashboardPresentation)
	if !ok || presentation == nil {
		return nil
	}
	result := &exploration.ExplorationTableDisplayConfig{}
	changed := false
	if presentation.RowHeight != 24 {
		result.RowHeight = cloneInt32(&presentation.RowHeight)
		changed = true
	}
	if !presentation.ShowHeader {
		result.ShowHeader = cloneBool(&presentation.ShowHeader)
		changed = true
	}
	if presentation.Striped {
		result.Striped = cloneBool(&presentation.Striped)
		changed = true
	}
	return resultIfTableChanged(result, changed)
}

func resultIfTableChanged(value *exploration.ExplorationTableDisplayConfig, changed bool) *exploration.ExplorationTableDisplayConfig {
	if !changed {
		return nil
	}
	return value
}
