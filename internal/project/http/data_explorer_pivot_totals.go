package http

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

// validateDataExplorerExecutionWindow rejects window operands that the
// governed semantic executor cannot honor. Silently ignoring an offset would
// return a different slice than the authored/restored exploration requested.
func validateDataExplorerExecutionWindow(spec exploration.ExplorationSpec) error {
	if spec.Pivot != nil && spec.Pivot.Window != nil && spec.Pivot.Window.Offset != nil && *spec.Pivot.Window.Offset != 0 {
		return fmt.Errorf("pivot window offset %d is not supported; use zero or omit offset", *spec.Pivot.Window.Offset)
	}
	return nil
}

type dataExplorerPivotTotalsQuery struct {
	name       string
	dimensions []dataquery.Field
}

// dataExplorerExecutePivotTotals runs only the explicitly requested exact
// totals. Each query clones the already-authorized main query, retaining its
// target, filters, time context, grain, policy metadata, and request identity;
// only the selected pivot dimensions are replaced. Totals are never derived
// by adding cells from the displayed aggregate frame.
func dataExplorerExecutePivotTotals(ctx context.Context, executor DataQueryExecutor, spec exploration.ExplorationSpec, baseQuery dataquery.Query) (*projectsignals.DataExplorePivotTotalsSignal, int64, []string) {
	if spec.Pivot == nil || !explorerPivotTotalsRequested(spec.Pivot.Totals) || executor == nil {
		return nil, 0, nil
	}
	pivot := spec.Pivot
	aliases := explorerSpecQueryAliases(spec)
	rowFields := explorerPivotDataQueryFields(pivot.Rows, aliases, spec.Time)
	columnFields := explorerPivotDataQueryFields(pivot.Columns, aliases, spec.Time)
	metricFields := explorerPivotMetricDataQueryFields(pivot.Metrics, aliases)
	queries := make([]dataExplorerPivotTotalsQuery, 0, 3)
	if projectsignals.ValueOrZero(pivot.Totals.Rows) {
		queries = append(queries, dataExplorerPivotTotalsQuery{name: "row", dimensions: rowFields})
	}
	if projectsignals.ValueOrZero(pivot.Totals.Columns) {
		queries = append(queries, dataExplorerPivotTotalsQuery{name: "column", dimensions: columnFields})
	}
	if projectsignals.ValueOrZero(pivot.Totals.Grand) {
		queries = append(queries, dataExplorerPivotTotalsQuery{name: "grand", dimensions: nil})
	}
	if len(queries) == 0 {
		return nil, 0, nil
	}
	result := &projectsignals.DataExplorePivotTotalsSignal{Rows: []projectsignals.DataExplorePivotTotalSignal{}, Columns: []projectsignals.DataExplorePivotTotalSignal{}, Grand: []projectsignals.DataExplorePivotTotalSignal{}, Status: "complete", Warnings: []string{}}
	var durationMS int64
	warnings := []string{}
	for _, requested := range queries {
		query := baseQuery
		query.Fields = append([]dataquery.Field(nil), requested.dimensions...)
		query.Metrics = append([]dataquery.Field(nil), metricFields...)
		// A sort on a dimension excluded from this total query would change the
		// governed shape. Totals are exact aggregates, so no sort is required.
		query.Sort = nil
		executed, err := executor.ExecuteDataQuery(ctx, query)
		if err != nil {
			upgradeDataExplorerPivotTotalsStatus(result, "error")
			warning := fmt.Sprintf("pivot %s totals query failed: %v", requested.name, err)
			result.Warnings = append(result.Warnings, warning)
			warnings = append(warnings, warning)
			continue
		}
		durationMS += executed.DurationMS
		if strings.TrimSpace(executed.Error) != "" {
			upgradeDataExplorerPivotTotalsStatus(result, "error")
			warning := fmt.Sprintf("pivot %s totals query failed: %s", requested.name, executed.Error)
			result.Warnings = append(result.Warnings, warning)
			warnings = append(warnings, warning)
			continue
		}
		if len(executed.Warnings) > 0 {
			result.Warnings = append(result.Warnings, executed.Warnings...)
			warnings = append(warnings, executed.Warnings...)
		}
		if dataExplorerPivotTotalsIncomplete(executed, query.Limit) {
			upgradeDataExplorerPivotTotalsStatus(result, "incomplete")
			warning := fmt.Sprintf("pivot %s totals are incomplete", requested.name)
			result.Warnings = append(result.Warnings, warning)
			warnings = append(warnings, warning)
			continue
		}
		values, err := dataExplorerPivotTotalRows(executed.Rows, requested.dimensions, metricFields, requested.name)
		if err != nil {
			upgradeDataExplorerPivotTotalsStatus(result, "error")
			warning := fmt.Sprintf("pivot %s totals are ambiguous: %v", requested.name, err)
			result.Warnings = append(result.Warnings, warning)
			warnings = append(warnings, warning)
			continue
		}
		switch requested.name {
		case "row":
			result.Rows = values
		case "column":
			result.Columns = values
		case "grand":
			result.Grand = values
		}
	}
	if result.Status == "complete" {
		// A requested query yielding no keyed rows is not an exact totals
		// payload for a non-empty governed frame; projection will fail closed.
		for _, requested := range queries {
			var values []projectsignals.DataExplorePivotTotalSignal
			switch requested.name {
			case "row":
				values = result.Rows
			case "column":
				values = result.Columns
			case "grand":
				values = result.Grand
			}
			if len(values) == 0 {
				upgradeDataExplorerPivotTotalsStatus(result, "error")
				warning := fmt.Sprintf("pivot %s totals are missing", requested.name)
				result.Warnings = append(result.Warnings, warning)
				warnings = append(warnings, warning)
			}
		}
	}
	return result, durationMS, warnings
}

func upgradeDataExplorerPivotTotalsStatus(result *projectsignals.DataExplorePivotTotalsSignal, status string) {
	if result == nil {
		return
	}
	rank := func(value string) int {
		switch value {
		case "error":
			return 3
		case "incomplete":
			return 2
		case "complete":
			return 1
		default:
			return 0
		}
	}
	if rank(status) > rank(result.Status) {
		result.Status = status
	}
}

func explorerPivotDataQueryFields(refs []exploration.ExplorationDimensionRef, aliases map[string]string, timeSelection *exploration.ExplorationTimeSelection) []dataquery.Field {
	fields := make([]dataquery.Field, 0, len(refs))
	for _, ref := range refs {
		grain := string(projectsignals.ValueOrZero(ref.Grain))
		if grain == "" && timeSelection != nil && timeSelection.Field == ref.Field {
			grain = string(timeSelection.Grain)
		}
		fields = append(fields, dataquery.Field{Field: ref.Field, Alias: firstExplorerNonEmpty(projectsignals.ValueOrZero(ref.Alias), aliases[ref.Field], ref.Field), Grain: grain})
	}
	return fields
}

func explorerPivotMetricDataQueryFields(refs []exploration.ExplorationMetricRef, aliases map[string]string) []dataquery.Field {
	fields := make([]dataquery.Field, 0, len(refs))
	for _, ref := range refs {
		fields = append(fields, dataquery.Field{Field: ref.Field, Alias: firstExplorerNonEmpty(projectsignals.ValueOrZero(ref.Alias), aliases[ref.Field], ref.Field)})
	}
	return fields
}

func dataExplorerPivotTotalsIncomplete(result dataquery.Result, limit int) bool {
	if limit > 0 && len(result.Rows) >= limit {
		return true
	}
	return result.TotalRowsKnown && result.TotalRows > len(result.Rows)
}

func dataExplorerPivotTotalRows(rows []dataquery.Row, dimensions, metrics []dataquery.Field, name string) ([]projectsignals.DataExplorePivotTotalSignal, error) {
	if name == "grand" && len(rows) != 1 {
		return nil, fmt.Errorf("expected one grand-total row, got %d", len(rows))
	}
	values := make([]projectsignals.DataExplorePivotTotalSignal, 0, len(rows))
	seen := map[string]struct{}{}
	for index, row := range rows {
		key := make(map[string]any, len(dimensions))
		identity := make([]any, 0, len(dimensions))
		for _, dimension := range dimensions {
			value, ok := row[dimension.Alias]
			if !ok {
				return nil, fmt.Errorf("row %d is missing dimension %q", index, dimension.Alias)
			}
			key[dimension.Alias] = value
			identity = append(identity, value)
		}
		identityKey := explorerPivotTupleIdentity(identity)
		if _, exists := seen[identityKey]; exists {
			return nil, fmt.Errorf("duplicate key at row %d", index)
		}
		seen[identityKey] = struct{}{}
		metricValues := make(map[string]any, len(metrics))
		for _, metric := range metrics {
			value, ok := row[metric.Alias]
			if !ok {
				return nil, fmt.Errorf("row %d is missing metric %q", index, metric.Alias)
			}
			metricValues[metric.Alias] = value
		}
		values = append(values, projectsignals.DataExplorePivotTotalSignal{Key: key, Values: metricValues})
	}
	return values, nil
}

// explorerSpecQueryAliases derives query output aliases, retaining explicit
// aliases authored on dimensions, metrics, and time selections. Pivot totals
// use the same aliases as the governed aggregate query.
func explorerSpecQueryAliases(spec exploration.ExplorationSpec) map[string]string {
	fields := make([]string, 0, len(spec.Dimensions)+len(spec.Metrics)+1)
	aliases := make(map[string]string, len(fields))
	dimensionFields := make(map[string]struct{}, len(spec.Dimensions))
	for _, dimension := range spec.Dimensions {
		fields = append(fields, dimension.Field)
		dimensionFields[dimension.Field] = struct{}{}
		if alias := strings.TrimSpace(projectsignals.ValueOrZero(dimension.Alias)); alias != "" {
			aliases[dimension.Field] = alias
		}
	}
	for _, metric := range spec.Metrics {
		fields = append(fields, metric.Field)
		if alias := strings.TrimSpace(projectsignals.ValueOrZero(metric.Alias)); alias != "" {
			aliases[metric.Field] = alias
		}
	}
	if spec.Time != nil {
		if _, exists := dimensionFields[spec.Time.Field]; !exists {
			fields = append(fields, spec.Time.Field)
		}
		if alias := strings.TrimSpace(projectsignals.ValueOrZero(spec.Time.Alias)); alias != "" {
			// A time selection may decorate an existing dimension. Preserve an
			// explicit dimension alias; otherwise the time alias is the merged
			// output alias for that one dimension.
			if _, exists := aliases[spec.Time.Field]; !exists {
				aliases[spec.Time.Field] = alias
			}
		}
	}
	derived := explorerQueryAliases(fields, nil)
	for field, alias := range aliases {
		derived[field] = alias
	}
	return derived
}
