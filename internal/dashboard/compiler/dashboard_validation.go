package compiler

import (
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/reportmodel"
)

// ValidateAndNormalizeDashboard validates authored dashboard input and returns the
// compiler-owned normalized value. Authored documents are immutable inputs:
// all compiler normalization is applied to a deep clone and never to d.
func ValidateAndNormalizeDashboard(d *dashboardauthoring.Dashboard, models map[string]*semanticmodel.Model) (*dashboardauthoring.Dashboard, error) {
	if d == nil {
		return nil, fmt.Errorf("dashboard is nil")
	}
	cloned, err := d.Clone()
	if err != nil {
		return nil, fmt.Errorf("clone dashboard %q: %w", d.ID, err)
	}
	normalized := &cloned
	if err := normalized.ValidateContract(); err != nil {
		return nil, err
	}
	model, ok := models[normalized.SemanticModel.String()]
	if !ok {
		return nil, fmt.Errorf("dashboard %q references unknown semantic model %q", normalized.ID, normalized.SemanticModel)
	}
	if err := validateFilterArchitecture(normalized, model); err != nil {
		return nil, err
	}
	if err := validateWidgetPlacements(normalized); err != nil {
		return nil, err
	}
	for name, authored := range normalized.Visuals {
		if authored.Chart == nil {
			continue
		}
		visual := *authored.Chart
		if visual.Query.Table != "" {
			if _, ok := model.Tables[visual.Query.Table]; !ok {
				return nil, fmt.Errorf("visual %q query.table references unknown table %q", name, visual.Query.Table)
			}
		}
		for _, dimension := range visual.Query.Dimensions {
			if err := model.ValidateQueryDimension(dimension.Field); err != nil {
				return nil, fmt.Errorf("visual %q references unknown dimension %q", name, dimension.Field)
			}
		}
		if !visual.Query.Series.IsZero() {
			if err := model.ValidateQueryDimension(visual.Query.Series.Field); err != nil {
				return nil, fmt.Errorf("visual %q references unknown series dimension %q", name, visual.Query.Series.Field)
			}
		}
		for _, measure := range visual.Query.Measures {
			if err := model.ValidateAggregateMember(measure.Field); err != nil {
				return nil, fmt.Errorf("visual %q references unknown measure %q", name, measure.Field)
			}
		}
		if !visual.Interaction.PointSelection.IsZero() {
			if err := dashboardauthoring.ValidateVisualPointSelectionMappingKeys(name, visual); err != nil {
				return nil, err
			}
			if _, err := reportmodel.ResolveSelectionInteraction(normalized, model, "visual", name); err != nil {
				return nil, err
			}
		}
		if !visual.Interaction.SpatialSelection.IsZero() {
			if _, err := reportmodel.ResolveSpatialSelectionInteraction(normalized, model, name); err != nil {
				return nil, err
			}
		}
		if err := validateVisualQueryPlan(normalized, model, name, visual); err != nil {
			return nil, err
		}
	}
	for name, authored := range normalized.Visuals {
		if authored.Tabular == nil {
			continue
		}
		table := *authored.Tabular
		normalizeTableFormatting(model, &table)
		for measure := range table.MeasureFormatting {
			if err := model.ValidateAggregateMember(measure); err != nil {
				return nil, fmt.Errorf("table %q measure_formatting references unknown measure %q", name, measure)
			}
		}
		switch authored.Type {
		case "table":
			if err := normalizeDataTableFields(name, model, &table); err != nil {
				return nil, err
			}
		case "matrix", "pivot":
			if err := normalizeTableFields(name, model, &table); err != nil {
				return nil, err
			}
		}
		// Selection resolution reads sources from the dashboard definition. Publish
		// the normalized table before resolving its configured row interaction.
		authored.Tabular = &table
		normalized.Visuals[name] = authored
		for _, mapping := range table.Interaction.RowSelection.Mappings {
			if !tableHasOutputColumn(table, mapping.Value) {
				return nil, fmt.Errorf("table %q interaction references unknown value column %q", name, mapping.Value)
			}
			if mapping.Label != "" && !tableHasOutputColumn(table, mapping.Label) {
				return nil, fmt.Errorf("table %q interaction references unknown label column %q", name, mapping.Label)
			}
		}
		if !table.Interaction.RowSelection.IsZero() {
			if _, err := reportmodel.ResolveSelectionInteraction(normalized, model, "visual", name); err != nil {
				return nil, err
			}
		}
		if err := validateTableQueryPlan(normalized, model, name, authored.Type, table); err != nil {
			return nil, err
		}
		authored.Tabular = &table
		normalized.Visuals[name] = authored
	}
	if err := validateFilterTargets(normalized, model); err != nil {
		return nil, err
	}
	return normalized, nil
}

func validateVisualQueryPlan(d *dashboardauthoring.Dashboard, model *semanticmodel.Model, name string, visual dashboardauthoring.Visual) error {
	planner := semanticquery.NewPlanner(model)
	plan := func(query dashboardauthoring.VisualQuery) error {
		dimensions := reportFieldRefsToQueryFields(query.Dimensions)
		if !query.Series.IsZero() {
			dimensions = append(dimensions, reportFieldRefToQueryField(query.Series))
		}
		_, err := planner.Plan(semanticquery.Request{
			Table:      query.Table,
			Dimensions: dimensions,
			Measures:   reportFieldRefsToQueryFields(query.Measures),
			Time: semanticquery.Time{
				Field: query.Time.Field,
				Grain: query.Time.Grain,
				Alias: query.Time.Alias,
			},
			Filters: scopedQueryFilters(d, model, "visual", name),
			Limit:   query.Limit,
		})
		return err
	}
	err := plan(visual.Query)
	if err != nil {
		return fmt.Errorf("visual %q query is invalid: %w", name, err)
	}
	for _, datasetID := range sortedMapKeys(visual.Datasets) {
		query := visual.Datasets[datasetID]
		if query.Table == "" {
			query.Table = visual.Query.Table
		}
		if err := plan(query); err != nil {
			return fmt.Errorf("visual %q context dataset %q query is invalid: %w", name, datasetID, err)
		}
	}
	return nil
}

func validateTableQueryPlan(d *dashboardauthoring.Dashboard, model *semanticmodel.Model, name, visualType string, table dashboardauthoring.TableVisual) error {
	planner := semanticquery.NewPlanner(model)
	filters := scopedQueryFilters(d, model, "visual", name)
	var err error
	switch visualType {
	case "matrix", "pivot":
		dimensions := reportFieldRefsToQueryFields(table.Query.Rows)
		dimensions = append(dimensions, reportFieldRefsToQueryFields(table.Query.Columns)...)
		_, err = planner.Plan(semanticquery.Request{
			Table:      table.Query.Table,
			Dimensions: dimensions,
			Measures:   reportFieldRefsToQueryFields(table.Query.Measures),
			Filters:    filters,
		})
	default:
		dimensions := []semanticquery.Field{}
		measures := []semanticquery.Field{}
		for _, column := range table.DataColumns {
			if _, resolveErr := model.ResolveDimension(column.Field); resolveErr == nil {
				dimensions = append(dimensions, reportFieldRefToQueryField(column))
				continue
			}
			measures = append(measures, reportFieldRefToQueryField(column))
		}
		_, err = planner.PlanRows(semanticquery.RowRequest{
			Table:      table.Query.Table,
			Dimensions: dimensions,
			Measures:   measures,
			Filters:    filters,
		})
	}
	if err != nil {
		return fmt.Errorf("table %q query is invalid: %w", name, err)
	}
	return nil
}

func scopedQueryFilters(_ *dashboardauthoring.Dashboard, _ *semanticmodel.Model, _, _ string) []semanticquery.Filter {
	return nil
}

func reportFieldRefsToQueryFields(fields []dashboardauthoring.FieldRef) []semanticquery.Field {
	out := make([]semanticquery.Field, 0, len(fields))
	for _, field := range fields {
		out = append(out, reportFieldRefToQueryField(field))
	}
	return out
}

func reportFieldRefToQueryField(field dashboardauthoring.FieldRef) semanticquery.Field {
	return semanticquery.Field{
		Field: field.Field,
		Alias: field.Alias,
	}
}

func tableHasOutputColumn(table dashboardauthoring.TableVisual, key string) bool {
	if key == "" {
		return false
	}
	for _, column := range table.DataColumns {
		if column.Alias == key {
			return true
		}
	}
	for _, column := range table.Columns {
		if column.Key == key {
			return true
		}
	}
	for _, row := range table.Rows {
		if displayField(row) == key {
			return true
		}
	}
	for _, measure := range table.Measures {
		if displayField(measure) == key {
			return true
		}
	}
	return false
}

func displayField(field string) string {
	if index := strings.LastIndex(field, "."); index >= 0 && index+1 < len(field) {
		return field[index+1:]
	}
	return field
}

func normalizeTableFields(name string, model *semanticmodel.Model, table *dashboardauthoring.TableVisual) error {
	table.Rows = make([]string, len(table.Query.Rows))
	for index, dimension := range table.Query.Rows {
		item, err := model.ResolveDimension(dimension.Field)
		if err != nil {
			return fmt.Errorf("table %q query.rows references unknown dimension %q", name, dimension.Field)
		}
		table.Rows[index] = item.Field
	}
	table.ColumnDims = make([]string, len(table.Query.Columns))
	for index, dimension := range table.Query.Columns {
		item, err := model.ResolveDimension(dimension.Field)
		if err != nil {
			return fmt.Errorf("table %q query.columns references unknown dimension %q", name, dimension.Field)
		}
		table.ColumnDims[index] = item.Field
	}
	table.Measures = make([]string, len(table.Query.Measures))
	for index, measure := range table.Query.Measures {
		if err := model.ValidateAggregateMember(measure.Field); err != nil {
			return fmt.Errorf("table %q query.measures references unknown measure %q", name, measure.Field)
		}
		table.Measures[index] = measure.Field
	}
	return nil
}

func normalizeDataTableFields(name string, model *semanticmodel.Model, table *dashboardauthoring.TableVisual) error {
	columns := make([]dashboardauthoring.FieldRef, 0, len(table.Query.Columns)+len(table.Query.Fields))
	if len(table.Query.Columns) > 0 {
		columns = append(columns, table.Query.Columns...)
	} else {
		for _, field := range table.Query.Fields {
			columns = append(columns, dashboardauthoring.FieldRef{Field: field, Alias: fieldRefAlias(field)})
		}
	}
	if len(columns) == 0 {
		return fmt.Errorf("table %q kind data_table requires query.fields or query.columns", name)
	}
	seenAliases := map[string]struct{}{}
	for index, column := range columns {
		if column.Alias == "" {
			column.Alias = fieldRefAlias(column.Field)
		}
		if _, exists := seenAliases[column.Alias]; exists {
			return fmt.Errorf("table %q has duplicate query output alias %q", name, column.Alias)
		}
		seenAliases[column.Alias] = struct{}{}
		if dimension, err := model.ResolveDimension(column.Field); err == nil {
			column.Field = dimension.Field
			columns[index] = column
			continue
		}
		measure, err := model.ResolveMeasure(column.Field)
		if err != nil {
			return fmt.Errorf("table %q query column %q references unknown field %q", name, column.Alias, column.Field)
		}
		column.Field = measure.Field
		columns[index] = column
	}
	table.DataColumns = columns
	if len(table.Columns) == 0 {
		table.Columns = make([]dashboard.TableColumn, 0, len(columns))
		for _, column := range columns {
			format := "text"
			role := ""
			align := ""
			if measure, err := model.ResolveMeasure(column.Field); err == nil {
				role = "measure"
				align = "right"
				if measure.Format != "" {
					format = measure.Format
				} else {
					format = "decimal"
				}
			}
			table.Columns = append(table.Columns, dashboard.TableColumn{
				Key:    column.Alias,
				Label:  titleFromIdentifier(column.Alias),
				Format: format,
				Role:   role,
				Align:  align,
			})
		}
		return nil
	}
	for _, column := range table.Columns {
		if !tableHasQueryAlias(table.DataColumns, column.Key) {
			return fmt.Errorf("table %q column %q has no matching query column alias", name, column.Key)
		}
	}
	return nil
}

func normalizeTableFormatting(model *semanticmodel.Model, table *dashboardauthoring.TableVisual) {
	if len(table.MeasureFormatting) == 0 {
		return
	}
	next := map[string][]dashboard.TableFormattingRule{}
	for measure, rules := range table.MeasureFormatting {
		next[measure] = rules
	}
	table.MeasureFormatting = next
}

func validateFilterTargets(_ *dashboardauthoring.Dashboard, _ *semanticmodel.Model) error {
	return nil
}

func tableHasQueryAlias(columns []dashboardauthoring.FieldRef, alias string) bool {
	for _, column := range columns {
		if column.Alias == alias {
			return true
		}
	}
	return false
}

func fieldRefAlias(field string) string {
	if field == "" {
		return ""
	}
	parts := strings.Split(field, ".")
	return parts[len(parts)-1]
}

func titleFromIdentifier(value string) string {
	value = strings.ReplaceAll(value, "_", " ")
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}
