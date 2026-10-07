package http

import (
	"encoding/json"
	"fmt"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/dashboard"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

type explorerPivotAxisValue struct {
	identity string
	values   []any
	label    string
}

func explorerPivotEnvelope(spec exploration.ExplorationSpec, base visualizationir.VisualizationSpecBase, result projectsignals.DataExploreResultSignal, sourceColumns []explorerVisualizationColumn) (visualizationir.VisualizationEnvelope, bool, string) {
	pivot := spec.Pivot
	if pivot == nil {
		return visualizationir.VisualizationEnvelope{}, false, ""
	}
	fail := func(reason string) (visualizationir.VisualizationEnvelope, bool, string) {
		return visualizationir.VisualizationEnvelope{}, false, "pivot view omitted: " + reason
	}
	if result.Truncated {
		return fail("the governed result is truncated")
	}
	if len(pivot.Rows) == 0 || len(pivot.Columns) == 0 || len(pivot.Metrics) == 0 {
		return fail("row, column, and metric fields are required")
	}
	if len(pivot.Rows) > 10 || len(pivot.Columns) > 10 || len(pivot.Metrics) > 10 {
		return fail("pivot axes exceed the safe field limit")
	}
	if pivot.Window != nil && pivot.Window.Offset != nil && *pivot.Window.Offset != 0 {
		return fail("offset pivot windows cannot be reconstructed from the result")
	}
	if pivot.Totals != nil && (projectsignals.ValueOrZero(pivot.Totals.Rows) || projectsignals.ValueOrZero(pivot.Totals.Columns) || projectsignals.ValueOrZero(pivot.Totals.Grand)) {
		return fail("requested totals are not present in the governed result")
	}

	rowFields, ok := explorerPivotFields(pivot.Rows, sourceColumns, false)
	if !ok {
		return fail("a row field is unavailable")
	}
	columnFields, ok := explorerPivotFields(pivot.Columns, sourceColumns, false)
	if !ok {
		return fail("a column field is unavailable")
	}
	metricFields, ok := explorerPivotFields(pivot.Metrics, sourceColumns, true)
	if !ok {
		return fail("a metric field is unavailable or non-numeric")
	}

	rowAxes := make([]explorerPivotAxisValue, 0)
	columnAxes := make([]explorerPivotAxisValue, 0)
	rowIndex := map[string]int{}
	columnIndex := map[string]int{}
	cells := map[string]any{}
	for _, record := range result.Rows {
		rowValues := explorerPivotRecordValues(record, rowFields)
		columnValues := explorerPivotRecordValues(record, columnFields)
		rowIdentity, err := explorerPivotIdentity(rowValues)
		if err != nil {
			return fail("row axis contains an unsupported value")
		}
		columnIdentity, err := explorerPivotIdentity(columnValues)
		if err != nil {
			return fail("column axis contains an unsupported value")
		}
		if _, exists := rowIndex[rowIdentity]; !exists {
			rowIndex[rowIdentity] = len(rowAxes)
			rowAxes = append(rowAxes, explorerPivotAxisValue{identity: rowIdentity, values: rowValues, label: explorerPivotTupleLabel(rowValues)})
		}
		if _, exists := columnIndex[columnIdentity]; !exists {
			columnIndex[columnIdentity] = len(columnAxes)
			columnAxes = append(columnAxes, explorerPivotAxisValue{identity: columnIdentity, values: columnValues, label: explorerPivotTupleLabel(columnValues)})
			if len(columnAxes) > dataExplorerPivotMaxColumns {
				return fail("column axis exceeds its bounded width")
			}
		}
		for _, metric := range metricFields {
			key := rowIdentity + "\x00" + columnIdentity + "\x00" + metric.Semantic
			if _, exists := cells[key]; exists {
				return fail("the result contains duplicate row, column, and metric cells")
			}
			cells[key] = explorerVisualizationScalar(record[metric.Output])
			if len(cells) > dataExplorerPivotMaxCells {
				return fail("pivot cell count exceeds its bounded size")
			}
		}
	}
	if len(rowAxes) == 0 || len(columnAxes) == 0 {
		return fail("the result has no complete row and column axes")
	}
	if int64(len(rowAxes)) > base.DataBudget.MaxRows || len(rowAxes)*len(columnAxes)*len(metricFields) > dataExplorerPivotMaxCells {
		return fail("pivot shape exceeds its bounded size")
	}
	if pivot.Window != nil && pivot.Window.Limit > 0 && int64(len(rowAxes)) > int64(pivot.Window.Limit) {
		return fail("row axis exceeds the authored pivot window")
	}

	rowRefs := explorerPivotRefs(rowFields)
	columnRefs := explorerPivotRefs(columnFields)
	metricRefs := explorerPivotRefs(metricFields)
	base.Kind = "pivot"
	pivotSpec := visualizationir.VisualizationSpec{Value: &visualizationir.PivotVisualizationSpec{
		VisualizationSpecBase: base, Kind: "pivot", Rows: rowRefs, Columns: columnRefs, Metrics: metricRefs,
		MetricFormatting: map[string][]visualizationir.TableVisualizationFormattingRule{},
		Presentation:     visualizationir.GridVisualizationPresentation{RowHeight: 32, ShowHeader: true},
	}}
	query := visualizationdefinition.QueryBinding{
		Kind: visualizationdefinition.QueryPivot, ResultShape: visualizationdefinition.ResultPivotWindow,
		ModelID: explorerVisualizationModelID(spec), DatasetID: dataExplorerVisualizationDB,
		Pivot: &visualizationdefinition.PivotQueryBinding{
			TableID: explorerVisualizationDatasetID(spec), Rows: explorerVisualizationBindings(rowFields),
			Columns: explorerVisualizationBindings(columnFields), Metrics: explorerVisualizationBindings(metricFields), Limit: base.DataBudget.MaxRows,
		},
	}
	definition, err := explorerVisualizationDefinition(dataExplorerPivotViewID, pivotSpec, query)
	if err != nil {
		return fail("pivot specification failed validation")
	}

	usedKeys := map[string]struct{}{}
	dashboardColumns := make([]dashboard.TableColumn, 0, len(rowFields)+len(columnAxes)*len(metricFields))
	for index, field := range rowFields {
		role := "dimension"
		if index == 0 {
			role = "row_header"
		}
		dashboardColumns = append(dashboardColumns, dashboard.TableColumn{Key: field.Output, Label: field.Label, Role: role, DataType: string(field.DataType)})
	}
	dynamicKeys := map[string]string{}
	for axisIndex, axis := range columnAxes {
		group := explorerPivotSafeLabel(axis.label)
		for metricIndex, metric := range metricFields {
			identity := fmt.Sprintf("%d:%d", axisIndex, metricIndex)
			key := fmt.Sprintf("pivot_c%d_m%d", axisIndex, metricIndex)
			if _, duplicate := usedKeys[key]; duplicate {
				return fail("generated pivot field IDs are not unique")
			}
			usedKeys[key] = struct{}{}
			dynamicKeys[axis.identity+"\x00"+metric.Semantic] = key
			dashboardColumns = append(dashboardColumns, dashboard.TableColumn{
				Key: key, Label: explorerPivotSafeLabel(group + " / " + metric.Label), Role: "metric", Align: "right",
				Group: group, Metric: metric.Output, ColumnValue: group, DataType: string(metric.DataType),
			})
			_ = identity
		}
	}
	rowsByIdentity := make(map[string]map[string]any, len(rowAxes))
	for _, axis := range rowAxes {
		row := make(map[string]any, len(dashboardColumns))
		for index, field := range rowFields {
			row[field.Output] = axis.values[index]
		}
		rowsByIdentity[axis.identity] = row
	}
	for _, axis := range rowAxes {
		for _, columnAxis := range columnAxes {
			for _, metric := range metricFields {
				identity := axis.identity + "\x00" + columnAxis.identity + "\x00" + metric.Semantic
				value, ok := cells[identity]
				if !ok {
					continue
				}
				rowsByIdentity[axis.identity][dynamicKeys[columnAxis.identity+"\x00"+metric.Semantic]] = value
			}
		}
	}
	windowRows := make([]map[string]any, 0, len(rowAxes))
	for _, axis := range rowAxes {
		windowRows = append(windowRows, rowsByIdentity[axis.identity])
	}
	sort := dashboard.TableSort{Key: rowFields[0].Output, Direction: "asc"}
	if pivot.Sort != nil && len(*pivot.Sort) > 0 {
		if column, ok := explorerVisualizationColumnFor(rowFields, (*pivot.Sort)[0].Field); ok {
			sort.Key = column.Output
			if (*pivot.Sort)[0].Direction == exploration.ExplorationSortDirectionDesc {
				sort.Direction = "desc"
			}
		}
	}
	requestSeq := result.RequestSeq
	if requestSeq < 0 {
		requestSeq = 0
	}
	table := dashboard.Table{
		Kind: "pivot", Title: base.Title, Columns: dashboardColumns, Cardinality: dashboard.ExactCardinality(len(windowRows)),
		AvailableRows: len(windowRows), RowCap: int(base.DataBudget.MaxRows), ChunkSize: max(1, len(windowRows)), RowHeight: 32,
		ResetVersion: 0, Sort: sort,
		Blocks: map[string]dashboard.TableBlock{"a": {Start: 0, RequestSeq: int(requestSeq), ResetVersion: 0, Sort: sort, Rows: windowRows}},
	}
	envelope, err := visualizationruntime.WindowEnvelopeFromDefinition(definition, table, explorerVisualizationDataRevision(result), explorerVisualizationGeneration(result))
	if err != nil {
		return fail("pivot frame failed validation")
	}
	return envelope, true, ""
}

func explorerPivotFields[T interface {
	~[]exploration.ExplorationDimensionRef | ~[]exploration.ExplorationMetricRef
}](refs T, columns []explorerVisualizationColumn, requireMetric bool) ([]explorerVisualizationColumn, bool) {
	// Keep separate overload-like branches so generated exploration refs stay
	// typed, while the IR receives only columns present in the governed result.
	var fields []explorerVisualizationColumn
	switch typed := any(refs).(type) {
	case []exploration.ExplorationDimensionRef:
		for _, ref := range typed {
			column, ok := explorerVisualizationColumnFor(columns, ref.Field)
			if !ok || column.Role == visualizationir.VisualizationFieldRoleMetric || requireMetric {
				return nil, false
			}
			fields = append(fields, column)
		}
	case []exploration.ExplorationMetricRef:
		for _, ref := range typed {
			column, ok := explorerVisualizationColumnFor(columns, ref.Field)
			if !ok || column.Role != visualizationir.VisualizationFieldRoleMetric || !explorerVisualizationNumeric(column) || !requireMetric {
				return nil, false
			}
			fields = append(fields, column)
		}
	default:
		return nil, false
	}
	seen := map[string]struct{}{}
	for _, field := range fields {
		if _, duplicate := seen[field.Output]; duplicate {
			return nil, false
		}
		seen[field.Output] = struct{}{}
	}
	return fields, len(fields) > 0
}

func explorerPivotRecordValues(record map[string]any, fields []explorerVisualizationColumn) []any {
	values := make([]any, len(fields))
	for index, field := range fields {
		values[index] = explorerVisualizationScalar(record[field.Output])
	}
	return values
}

func explorerPivotIdentity(values []any) (string, error) {
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func explorerPivotTupleLabel(values []any) string {
	labels := make([]string, len(values))
	for index, value := range values {
		if value == nil {
			labels[index] = "(null)"
		} else {
			labels[index] = fmt.Sprint(value)
		}
	}
	return strings.Join(labels, " / ")
}

func explorerPivotSafeLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "(empty)"
	}
	if len(value) > 120 {
		return value[:117] + "..."
	}
	return value
}

func explorerPivotRefs(columns []explorerVisualizationColumn) []visualizationir.VisualizationFieldRef {
	refs := make([]visualizationir.VisualizationFieldRef, 0, len(columns))
	for _, column := range columns {
		refs = append(refs, visualizationir.VisualizationFieldRef{Dataset: dataExplorerVisualizationDB, Field: column.Output})
	}
	return refs
}
