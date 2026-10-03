package explorationadapter

import (
	"fmt"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/dashboard/document"
)

func (c converter) querySelections() ([]document.DashboardDimensionSelection, []document.DashboardMetricSelection, error) {
	if c.spec.Pivot != nil {
		rows := append([]exploration.ExplorationDimensionRef(nil), c.spec.Pivot.Rows...)
		columns := append([]exploration.ExplorationDimensionRef(nil), c.spec.Pivot.Columns...)
		if !containsTimeDimension(rows, c.spec.Time) && !containsTimeDimension(columns, c.spec.Time) {
			rows = appendTimeDimension(rows, c.spec.Time)
		}
		dimensions := make([]document.DashboardDimensionSelection, 0, len(rows)+len(columns))
		for _, value := range rows {
			value = applyTimeDimension(value, c.spec.Time)
			selection, err := c.dimension(value)
			if err != nil {
				return nil, nil, err
			}
			dimensions = append(dimensions, selection)
		}
		for _, value := range columns {
			value = applyTimeDimension(value, c.spec.Time)
			selection, err := c.dimension(value)
			if err != nil {
				return nil, nil, err
			}
			dimensions = append(dimensions, selection)
		}
		metrics := make([]document.DashboardMetricSelection, 0, len(c.spec.Pivot.Metrics))
		for _, value := range c.spec.Pivot.Metrics {
			selection, err := c.metric(value)
			if err != nil {
				return nil, nil, err
			}
			metrics = append(metrics, selection)
		}
		return dimensions, metrics, nil
	}
	dimensions := make([]document.DashboardDimensionSelection, 0, len(c.spec.Dimensions)+1)
	seenTime := false
	for _, value := range c.spec.Dimensions {
		if c.spec.Time != nil && value.Field == c.spec.Time.Field {
			seenTime = true
			value.Grain = &c.spec.Time.Grain
			if c.spec.Time.Alias != nil {
				value.Alias = c.spec.Time.Alias
			}
		}
		selection, err := c.dimension(value)
		if err != nil {
			return nil, nil, err
		}
		dimensions = append(dimensions, selection)
	}
	if c.spec.Time != nil && !seenTime {
		selection, err := c.dimension(exploration.ExplorationDimensionRef{Field: c.spec.Time.Field, Alias: c.spec.Time.Alias, Grain: &c.spec.Time.Grain})
		if err != nil {
			return nil, nil, err
		}
		dimensions = append(dimensions, selection)
	}
	metrics := make([]document.DashboardMetricSelection, 0, len(c.spec.Metrics))
	for _, value := range c.spec.Metrics {
		selection, err := c.metric(value)
		if err != nil {
			return nil, nil, err
		}
		metrics = append(metrics, selection)
	}
	return dimensions, metrics, nil
}

func appendTimeDimension(values []exploration.ExplorationDimensionRef, timeSelection *exploration.ExplorationTimeSelection) []exploration.ExplorationDimensionRef {
	if timeSelection == nil {
		return values
	}
	for _, value := range values {
		if value.Field == timeSelection.Field {
			return values
		}
	}
	return append(values, exploration.ExplorationDimensionRef{Field: timeSelection.Field, Alias: timeSelection.Alias, Grain: &timeSelection.Grain})
}

func containsTimeDimension(values []exploration.ExplorationDimensionRef, timeSelection *exploration.ExplorationTimeSelection) bool {
	if timeSelection == nil {
		return false
	}
	for _, value := range values {
		if value.Field == timeSelection.Field {
			return true
		}
	}
	return false
}

func applyTimeDimension(value exploration.ExplorationDimensionRef, timeSelection *exploration.ExplorationTimeSelection) exploration.ExplorationDimensionRef {
	if timeSelection == nil || value.Field != timeSelection.Field {
		return value
	}
	grain := timeSelection.Grain
	value.Grain = &grain
	if timeSelection.Alias != nil {
		value.Alias = cloneString(timeSelection.Alias)
	}
	return value
}

func (c converter) query(visualType string, dimensions []document.DashboardDimensionSelection, metrics []document.DashboardMetricSelection) (document.DashboardQuery, error) {
	if c.spec.Pivot != nil {
		rows := make([]document.DashboardDimensionSelection, 0, len(c.spec.Pivot.Rows)+1)
		for _, value := range c.spec.Pivot.Rows {
			value = applyTimeDimension(value, c.spec.Time)
			selection, err := c.dimension(value)
			if err != nil {
				return document.DashboardQuery{}, err
			}
			rows = append(rows, selection)
		}
		if c.spec.Time != nil {
			present := false
			for _, value := range c.spec.Pivot.Rows {
				if value.Field == c.spec.Time.Field {
					present = true
					break
				}
			}
			for _, value := range c.spec.Pivot.Columns {
				if value.Field == c.spec.Time.Field {
					present = true
					break
				}
			}
			if !present {
				selection, err := c.dimension(exploration.ExplorationDimensionRef{Field: c.spec.Time.Field, Alias: c.spec.Time.Alias, Grain: &c.spec.Time.Grain})
				if err != nil {
					return document.DashboardQuery{}, err
				}
				rows = append(rows, selection)
			}
		}
		columns := make([]document.DashboardDimensionSelection, 0, len(c.spec.Pivot.Columns))
		for _, value := range c.spec.Pivot.Columns {
			value = applyTimeDimension(value, c.spec.Time)
			selection, err := c.dimension(value)
			if err != nil {
				return document.DashboardQuery{}, err
			}
			columns = append(columns, selection)
		}
		sortSource := c.spec.Pivot.Sort
		if sortSource == nil && len(c.spec.Sort) > 0 {
			sortSource = &c.spec.Sort
		}
		sort, err := c.sorts(sortSource, append(append([]document.DashboardDimensionSelection{}, rows...), columns...), metrics)
		if err != nil {
			return document.DashboardQuery{}, err
		}
		pivot := &document.PivotDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "pivot"}, Type: "pivot", Rows: rows, Columns: columns, Metrics: metrics, Sort: sort}
		if c.spec.Pivot.Totals != nil {
			pivot.Totals = &document.DashboardPivotTotals{Rows: cloneBool(c.spec.Pivot.Totals.Rows), Columns: cloneBool(c.spec.Pivot.Totals.Columns), Grand: cloneBool(c.spec.Pivot.Totals.Grand)}
		}
		if c.spec.Pivot.Window != nil {
			pivot.Window = &document.DashboardPivotWindow{Offset: cloneInt32(c.spec.Pivot.Window.Offset), Limit: c.spec.Pivot.Window.Limit}
			if pivot.Window.Limit <= 0 {
				return document.DashboardQuery{}, fmt.Errorf("pivot window limit must be positive")
			}
		}
		return document.DashboardQuery{Value: pivot}, nil
	}
	sort, err := c.sorts(&c.spec.Sort, dimensions, metrics)
	if err != nil {
		return document.DashboardQuery{}, err
	}
	aggregate := &document.AggregateDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate", Dimensions: dimensions, Metrics: metrics, Sort: sort, Limit: cloneInt32(&c.spec.Limit)}
	return document.DashboardQuery{Value: aggregate}, nil
}

func (c converter) sorts(values *[]exploration.ExplorationSort, dimensions []document.DashboardDimensionSelection, metrics []document.DashboardMetricSelection) (*[]document.DashboardSort, error) {
	if values == nil || len(*values) == 0 {
		return nil, nil
	}
	result := make([]document.DashboardSort, 0, len(*values))
	for _, value := range *values {
		field := strings.TrimSpace(value.Field)
		if field == "" {
			return nil, fmt.Errorf("sort field is required")
		}
		output, err := c.selectedFieldOutput(field, "sort", dimensions, metrics)
		if err != nil {
			return nil, err
		}
		direction := document.DashboardSortDirection(value.Direction)
		if direction != document.DashboardSortDirectionAsc && direction != document.DashboardSortDirectionDesc {
			return nil, fmt.Errorf("unsupported sort direction %q", value.Direction)
		}
		result = append(result, document.DashboardSort{Field: output, Direction: direction})
	}
	return &result, nil
}
