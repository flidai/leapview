package explorationadapter

import (
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/dashboard/document"
)

func (c converter) resolveRecordField(field string) (string, error) {
	physical := strings.TrimSpace(c.options.RecordFields[field])
	if physical == "" {
		return "", fmt.Errorf("record field %q has no active physical binding", field)
	}
	if err := validateDashboardResultField(physical); err != nil {
		return "", err
	}
	return physical, nil
}

func recordDimensionSelection(value exploration.ExplorationDimensionRef, physical string) document.DashboardDimensionSelection {
	alias := cloneString(value.Alias)
	if alias == nil {
		output := value.Field
		if index := strings.LastIndex(output, "."); index >= 0 {
			output = output[index+1:]
		}
		alias = &output
	}
	return document.DashboardDimensionSelection{Reference: &document.DashboardDimensionReference{Dimension: physical, Alias: alias}}
}

func (c converter) recordsQuery(dimensions []document.DashboardDimensionSelection) (document.DashboardQuery, error) {
	fields := make([]document.DashboardRecordFieldSelection, 0, len(dimensions))
	for _, dimension := range dimensions {
		field, _ := dimensionName(dimension)
		alias := dimensionOutput(dimension)
		fields = append(fields, document.DashboardRecordFieldSelection{Reference: &document.DashboardRecordFieldReference{Field: field, Alias: &alias}})
	}
	sort, err := c.sorts(&c.spec.Sort, dimensions, nil)
	if err != nil {
		return document.DashboardQuery{}, err
	}
	return document.DashboardQuery{Value: &document.RecordsDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "records"}, Type: "records", Dataset: *c.spec.DatasetID, Fields: fields, Sort: sort, Limit: cloneInt32(&c.spec.Limit)}}, nil
}

func (c converter) recordsPresentation() (string, document.DashboardPresentation, error) {
	if c.spec.Visualization != nil {
		table, ok := c.spec.Visualization.Value.(*exploration.TableExplorationVisualization)
		if !ok || table == nil {
			return "", document.DashboardPresentation{}, fmt.Errorf("records exploration requires a table visualization")
		}
		if err := c.validateTableColumns(table.Columns); err != nil {
			return "", document.DashboardPresentation{}, err
		}
	}
	if err := c.validateBase("table", false, false, false, false); err != nil {
		return "", document.DashboardPresentation{}, err
	}
	return "table", document.DashboardPresentation{Value: c.tablePresentation()}, nil
}
