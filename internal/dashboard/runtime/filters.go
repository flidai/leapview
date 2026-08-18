package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	"github.com/flidai/leapview/internal/dashboard/reportmodel"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

type FilterService struct{}

func (m *Service) QueryCompiledFilterOptions(ctx context.Context, dashboardID string, query dashboardfilter.OptionQuery) (dashboardfilter.OptionResult, error) {
	report, runtime, err := m.queries.snapshots.reports.reportRuntime(dashboardID, m.queries.snapshots.runtimes)
	if err != nil {
		return dashboardfilter.OptionResult{}, err
	}
	return m.filters.queryCompiledFilterOptions(ctx, runtime, report, query)
}

func (s *FilterService) queryCompiledFilterOptions(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, query dashboardfilter.OptionQuery) (dashboardfilter.OptionResult, error) {
	filters := []reportdef.QueryFilter{}
	bindings := report.CompiledFilterBindings()
	for key, expression := range query.Dependencies {
		binding, ok := bindings[key]
		if !ok {
			return dashboardfilter.OptionResult{}, fmt.Errorf("unknown option dependency binding %q", key)
		}
		definition, ok := report.FilterDefinitions[binding.Filter]
		if !ok {
			return dashboardfilter.OptionResult{}, fmt.Errorf("unknown option dependency filter %q", binding.Filter)
		}
		dependencyFilters, filterErr := semanticFiltersForExpression(definition, expression)
		if filterErr != nil {
			return dashboardfilter.OptionResult{}, filterErr
		}
		filters = append(filters, dependencyFilters...)
	}
	if query.Search != "" {
		filters = append(filters, reportdef.QueryFilter{Field: query.Field, Dataset: query.Dataset, Operator: "contains", Values: []any{query.Search}})
	}
	if query.After != "" {
		filters = append(filters, reportdef.QueryFilter{Field: query.Field, Dataset: query.Dataset, Operator: "greater_than", Values: []any{query.After}})
	}
	request := reportAggregateDataQuery(report.SemanticModel, reportdef.AggregateQuery{
		Dataset: tableForField(query.Field), Dimensions: []reportdef.QueryField{{Field: query.Field, Alias: "value"}},
		Filters: filters, Sort: []reportdef.QuerySort{{Field: "value", Direction: "asc"}}, Limit: query.Limit + 1,
	})
	request.Surface = dataquery.SurfaceDashboard
	request.Operation = dataquery.OperationDashboardFilterOptions
	result, err := runtime.data.ExecuteDataQuery(ctx, request)
	if err != nil {
		return dashboardfilter.OptionResult{}, err
	}
	rows := reportRowsFromDataQuery(result.Rows)
	complete := len(rows) <= query.Limit
	if len(rows) > query.Limit {
		rows = rows[:query.Limit]
	}
	items := make([]dashboardfilter.OptionItem, 0, len(rows))
	next := ""
	for _, row := range rows {
		raw, ok := row["value"]
		if !ok || raw == nil {
			continue
		}
		value := fmt.Sprint(normalizeDBValue(raw))
		typed := dashboardfilter.Value{Kind: query.ValueKind, Value: value}
		if query.ValueKind == dashboardfilter.ValueBoolean {
			typed.Value = value == "true"
		}
		items = append(items, dashboardfilter.OptionItem{Value: typed, Label: value, Available: true})
		next = value
	}
	return dashboardfilter.OptionResult{Items: items, Complete: complete, Next: next}, nil
}

func (s *FilterService) semanticFilters(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, filters dashboard.Filters, targetKind, targetID string) ([]reportdef.QueryFilter, error) {
	filters = filters.WithDefaults()
	result := []reportdef.QueryFilter{}
	if filters.CompiledState != nil {
		compiled, err := semanticBindingFiltersForTarget(report, *filters.CompiledState, filters.ActivePageID, targetKind, targetID)
		if err != nil {
			return nil, err
		}
		result = append(result, compiled...)
	}
	for _, selection := range filters.Selections {
		if selection.SourceKind == "" || selection.SourceID == "" || len(selection.Entries) == 0 {
			continue
		}
		if isUIOnlyRowSelection(selection) {
			continue
		}
		wantInteractionKind := "point_selection"
		if source, ok := report.Visualizations[selection.SourceID]; ok && isGridQuery(source.Query.Kind) && selection.SourceKind == "visual" {
			wantInteractionKind = "row_selection"
		}
		if selection.InteractionKind != wantInteractionKind {
			return nil, fmt.Errorf("selection source %s %q has invalid interaction kind %q", selection.SourceKind, selection.SourceID, selection.InteractionKind)
		}
		resolved, err := reportmodel.ResolveCompiledSelectionInteraction(report, runtime.model, selection.SourceKind, selection.SourceID)
		if err != nil {
			return nil, fmt.Errorf("resolve interaction selection: %w", err)
		}
		if resolvedSelectionEffect(resolved, targetKind, targetID) != string(visualizationir.VisualizationInteractionEffectFilter) {
			continue
		}
		groups := make([]reportdef.QueryFilterGroup, 0, len(selection.Entries))
		for _, entry := range selection.Entries {
			group := reportdef.QueryFilterGroup{}
			canonical, err := canonicalSelectionEntry(resolved, entry)
			if err != nil {
				return nil, fmt.Errorf("selection source %s %q: %w", selection.SourceKind, selection.SourceID, err)
			}
			for index, mapping := range resolved.Mappings {
				mappingFilters, err := selectionMappingFilters(mapping, canonical[index].Value)
				if err != nil {
					return nil, fmt.Errorf("selection source %s %q field %q: %w", selection.SourceKind, selection.SourceID, mapping.Field, err)
				}
				group.Filters = append(group.Filters, mappingFilters...)
			}
			groups = append(groups, group)
		}
		switch len(groups) {
		case 0:
			continue
		case 1:
			result = append(result, groups[0].Filters...)
		default:
			result = append(result, reportdef.QueryFilter{Groups: groups})
		}
	}
	for _, selection := range filters.SpatialSelections {
		if selection.VisualID == "" || selection.InteractionID == "" || selection.Geometry.Value == nil {
			continue
		}
		resolved, err := reportmodel.ResolveCompiledSpatialSelectionInteraction(report, runtime.model, selection.VisualID, selection.InteractionID)
		if err != nil {
			return nil, fmt.Errorf("resolve spatial interaction selection: %w", err)
		}
		if resolvedSpatialSelectionEffect(resolved, targetKind, targetID) != string(visualizationir.VisualizationInteractionEffectFilter) {
			continue
		}
		spatial, err := spatialFilterFromSelection(resolved, selection.Geometry)
		if err != nil {
			return nil, fmt.Errorf("spatial selection source visual %q: %w", selection.VisualID, err)
		}
		result = append(result, reportdef.QueryFilter{Spatial: &spatial})
	}
	return result, nil
}

func (s *FilterService) validateSelections(runtime *modelRuntime, report *dashboarddefinition.Definition, filters dashboard.Filters) error {
	for _, selection := range filters.Selections {
		if selection.SourceKind == "" || selection.SourceID == "" || len(selection.Entries) == 0 || isUIOnlyRowSelection(selection) {
			continue
		}
		wantInteractionKind := "point_selection"
		if source, ok := report.Visualizations[selection.SourceID]; ok && isGridQuery(source.Query.Kind) && selection.SourceKind == "visual" {
			wantInteractionKind = "row_selection"
		}
		if selection.InteractionKind != wantInteractionKind {
			return fmt.Errorf("selection source %s %q has invalid interaction kind %q", selection.SourceKind, selection.SourceID, selection.InteractionKind)
		}
		resolved, err := reportmodel.ResolveCompiledSelectionInteraction(report, runtime.model, selection.SourceKind, selection.SourceID)
		if err != nil {
			return fmt.Errorf("resolve interaction selection: %w", err)
		}
		for _, entry := range selection.Entries {
			if _, err := canonicalSelectionEntry(resolved, entry); err != nil {
				return fmt.Errorf("selection source %s %q: %w", selection.SourceKind, selection.SourceID, err)
			}
		}
	}
	for _, selection := range filters.SpatialSelections {
		if selection.VisualID == "" || selection.InteractionID == "" || selection.Geometry.Value == nil {
			continue
		}
		resolved, err := reportmodel.ResolveCompiledSpatialSelectionInteraction(report, runtime.model, selection.VisualID, selection.InteractionID)
		if err != nil {
			return fmt.Errorf("resolve spatial interaction selection: %w", err)
		}
		if _, err := spatialFilterFromSelection(resolved, selection.Geometry); err != nil {
			return fmt.Errorf("spatial selection source visual %q: %w", selection.VisualID, err)
		}
	}
	return nil
}

func resolvedSelectionEffect(selection reportmodel.ResolvedSelectionInteraction, targetKind, targetID string) string {
	for _, target := range selection.Targets {
		if target.Kind == targetKind && target.ID == targetID {
			return target.Effect
		}
	}
	return ""
}

func resolvedSpatialSelectionEffect(selection reportmodel.ResolvedSpatialSelectionInteraction, targetKind, targetID string) string {
	for _, target := range selection.Targets {
		if target.Kind == targetKind && target.ID == targetID {
			return target.Effect
		}
	}
	return ""
}

func spatialFilterFromSelection(selection reportmodel.ResolvedSpatialSelectionInteraction, geometry visualizationir.VisualizationSpatialSelectionGeometry) (reportdef.SpatialFilter, error) {
	filter := reportdef.SpatialFilter{
		LatitudeField: selection.Latitude.Field, LongitudeField: selection.Longitude.Field, Dataset: selection.Latitude.Dataset,
	}
	if selection.Latitude.Dataset != selection.Longitude.Dataset {
		return reportdef.SpatialFilter{}, fmt.Errorf("coordinate fields must resolve to the same dataset")
	}
	switch value := geometry.Value.(type) {
	case *visualizationir.VisualizationSpatialBoxSelection:
		if value == nil {
			break
		}
		filter.Kind, filter.West, filter.South, filter.East, filter.North = "box", value.Bounds.West, value.Bounds.South, value.Bounds.East, value.Bounds.North
	case *visualizationir.VisualizationSpatialLassoSelection:
		if value == nil {
			break
		}
		filter.Kind = "lasso"
		for _, point := range value.Points {
			filter.Points = append(filter.Points, reportdef.SpatialPoint{Longitude: point.Longitude, Latitude: point.Latitude})
		}
	case *visualizationir.VisualizationSpatialRadiusSelection:
		if value == nil {
			break
		}
		filter.Kind, filter.Center, filter.RadiusMeters = "radius", reportdef.SpatialPoint{Longitude: value.Center.Longitude, Latitude: value.Center.Latitude}, value.RadiusMeters
	}
	if filter.Kind == "" {
		return reportdef.SpatialFilter{}, fmt.Errorf("spatial selection geometry is required")
	}
	return filter, nil
}

func canonicalSelectionEntry(selection reportmodel.ResolvedSelectionInteraction, entry dashboard.InteractionSelectionEntry) ([]dashboard.InteractionSelectionMapping, error) {
	identities := make([]reportmodel.SelectionMappingIdentity, len(entry.Mappings))
	incoming := make(map[reportmodel.SelectionMappingIdentity]dashboard.InteractionSelectionMapping, len(entry.Mappings))
	for index, mapping := range entry.Mappings {
		if !mapping.HasValue() {
			return nil, fmt.Errorf("mapping %d must include value", index)
		}
		if !dashboard.IsInteractionSelectionScalar(mapping.Value) {
			return nil, fmt.Errorf("mapping %d value must be a JSON scalar", index)
		}
		identity := reportmodel.SelectionMappingIdentity{Field: mapping.Field, Dataset: mapping.Dataset, Grain: mapping.Grain}
		identities[index] = identity
		incoming[identity] = mapping
	}
	canonical, err := selection.CanonicalizeMappings(identities)
	if err != nil {
		return nil, err
	}
	result := make([]dashboard.InteractionSelectionMapping, 0, len(canonical))
	for _, mapping := range canonical {
		identity := reportmodel.SelectionMappingIdentity{Field: mapping.Field, Dataset: mapping.Dataset, Grain: mapping.Grain}
		value := incoming[identity]
		if !dashboard.InteractionSelectionValueMatchesType(value.Value, mapping.Type, mapping.Grain) {
			return nil, fmt.Errorf("mapping field %q value type %T does not match semantic type %q", mapping.Field, value.Value, mapping.Type)
		}
		result = append(result, value)
	}
	return result, nil
}

func selectionMappingFilters(mapping reportmodel.ResolvedSelectionMapping, value dashboard.InteractionSelectionValue) ([]reportdef.QueryFilter, error) {
	if value == nil {
		return []reportdef.QueryFilter{{Field: mapping.Field, Dataset: mapping.Dataset, Operator: "is_null"}}, nil
	}
	if mapping.Grain == "" {
		return []reportdef.QueryFilter{{Field: mapping.Field, Dataset: mapping.Dataset, Operator: "equals", Values: []any{value}}}, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("grained time value must be a string, got %T", value)
	}
	start, err := dashboard.ParseInteractionSelectionTime(text, mapping.Grain)
	if err != nil {
		return nil, err
	}
	var end time.Time
	switch mapping.Grain {
	case "day":
		end = start.AddDate(0, 0, 1)
	case "week":
		end = start.AddDate(0, 0, 7)
	case "month":
		end = start.AddDate(0, 1, 0)
	case "quarter":
		end = start.AddDate(0, 3, 0)
	case "year":
		end = start.AddDate(1, 0, 0)
	default:
		return nil, fmt.Errorf("unsupported time grain %q", mapping.Grain)
	}
	return []reportdef.QueryFilter{
		{Field: mapping.Field, Dataset: mapping.Dataset, Operator: "greater_than_or_equal", Values: []any{start}},
		{Field: mapping.Field, Dataset: mapping.Dataset, Operator: "less_than", Values: []any{end}},
	}, nil
}

func isUIOnlyRowSelection(selection dashboard.InteractionSelection) bool {
	if selection.SourceKind != "visual" || selection.InteractionKind != "row_selection" || len(selection.Entries) == 0 {
		return false
	}
	for _, entry := range selection.Entries {
		if len(entry.Mappings) != 1 {
			return false
		}
		mapping := entry.Mappings[0]
		if mapping.Field != dashboard.UIRowSelectionField || mapping.Dataset != "" || mapping.Grain != "" {
			return false
		}
	}
	return true
}

func (s *FilterService) countRows(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, table string, filters dashboard.Filters, targetKind, targetID string) (int, error) {
	queryFilters, err := s.semanticFilters(ctx, runtime, report, filters, targetKind, targetID)
	if err != nil {
		return 0, err
	}
	total, err := runtime.data.Count(ctx, reportdef.CountQuery{
		Dataset: table,
		Filters: queryFilters,
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

func tableForField(field string) string {
	if index := strings.IndexByte(field, '.'); index > 0 {
		return field[:index]
	}
	return ""
}

func isGridQuery(kind visualizationdefinition.QueryKind) bool {
	return kind == visualizationdefinition.QueryDetail || kind == visualizationdefinition.QueryMatrix || kind == visualizationdefinition.QueryPivot
}
