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
	Identity string
	Values   []any
	Label    string
}

type explorerPivotCell struct {
	RowIdentity    string
	ColumnIdentity string
	Metric         explorerVisualizationColumn
	Value          any
}

// explorerPivotEnvelope performs the only safe long-to-wide conversion we can
// make at this boundary: the governed result is already aggregated, so a
// repeated row/column/metric cell is an ambiguity and is rejected rather than
// summed. Dynamic keys carry typed tuple identity through collision-safe
// suffixes; no totals are synthesized from the aggregate rows.
func explorerPivotEnvelope(spec exploration.ExplorationSpec, pivot exploration.ExplorationPivotConfig, base visualizationir.VisualizationSpecBase, result projectsignals.DataExploreResultSignal, sourceColumns []explorerVisualizationColumn, modelID, datasetID string) (string, *visualizationir.VisualizationEnvelope, string) {
	fallback := func(reason string) (string, *visualizationir.VisualizationEnvelope, string) {
		return dataExplorerTableViewID, nil, "pivot visualization omitted: " + reason + "; no safe wide pivot, showing table"
	}
	if result.Truncated {
		return fallback("governed result is truncated")
	}
	if len(pivot.Rows) == 0 || len(pivot.Columns) == 0 || len(pivot.Metrics) == 0 {
		return fallback("row, column, and metric fields are required")
	}
	rowFields := make([]explorerVisualizationColumn, 0, len(pivot.Rows))
	for _, ref := range pivot.Rows {
		column, ok := explorerColumnFor(sourceColumns, ref.Field)
		if !ok || column.Role == visualizationir.VisualizationFieldRoleMetric {
			return fallback(fmt.Sprintf("row field %q is unavailable", ref.Field))
		}
		rowFields = append(rowFields, column)
	}
	columnFields := make([]explorerVisualizationColumn, 0, len(pivot.Columns))
	for _, ref := range pivot.Columns {
		column, ok := explorerColumnFor(sourceColumns, ref.Field)
		if !ok || column.Role == visualizationir.VisualizationFieldRoleMetric {
			return fallback(fmt.Sprintf("column field %q is unavailable", ref.Field))
		}
		columnFields = append(columnFields, column)
	}
	metricFields := make([]explorerVisualizationColumn, 0, len(pivot.Metrics))
	for _, ref := range pivot.Metrics {
		column, ok := explorerColumnFor(sourceColumns, ref.Field)
		if !ok || column.Role != visualizationir.VisualizationFieldRoleMetric || !numericExplorerColumn(column) {
			return fallback(fmt.Sprintf("metric field %q is unavailable or non-numeric", ref.Field))
		}
		metricFields = append(metricFields, column)
	}

	rowAxes := []explorerPivotAxisValue{}
	columnAxes := []explorerPivotAxisValue{}
	rowAxisByIdentity := map[string]int{}
	columnAxisByIdentity := map[string]int{}
	cells := map[string]explorerPivotCell{}
	for rowIndex, record := range result.Rows {
		rowValues := explorerPivotValues(record, rowFields)
		columnValues := explorerPivotValues(record, columnFields)
		rowIdentity := explorerPivotTupleIdentity(rowValues)
		columnIdentity := explorerPivotTupleIdentity(columnValues)
		if _, ok := rowAxisByIdentity[rowIdentity]; !ok {
			rowAxisByIdentity[rowIdentity] = len(rowAxes)
			rowAxes = append(rowAxes, explorerPivotAxisValue{Identity: rowIdentity, Values: rowValues, Label: explorerPivotTupleLabel(rowValues)})
		}
		if _, ok := columnAxisByIdentity[columnIdentity]; !ok {
			columnAxisByIdentity[columnIdentity] = len(columnAxes)
			columnAxes = append(columnAxes, explorerPivotAxisValue{Identity: columnIdentity, Values: columnValues, Label: explorerPivotTupleLabel(columnValues)})
			if len(columnAxes) > dataExplorerPivotMaxColumns {
				return fallback(fmt.Sprintf("column axis exceeds %d columns", dataExplorerPivotMaxColumns))
			}
		}
		for _, metric := range metricFields {
			cellIdentity := rowIdentity + "\x00" + columnIdentity + "\x00" + metric.Semantic
			if _, exists := cells[cellIdentity]; exists {
				return fallback(fmt.Sprintf("result contains duplicate cell at row %d", rowIndex))
			}
			cells[cellIdentity] = explorerPivotCell{RowIdentity: rowIdentity, ColumnIdentity: columnIdentity, Metric: metric, Value: record[metric.Output]}
			if len(cells) > dataExplorerPivotMaxCells {
				return fallback(fmt.Sprintf("pivot cell count exceeds %d cells", dataExplorerPivotMaxCells))
			}
		}
	}
	if len(rowAxes) == 0 || len(columnAxes) == 0 {
		return fallback("result has no complete row and column axes")
	}
	if base.DataBudget.MaxRows > 0 && int64(len(rowAxes)) > base.DataBudget.MaxRows {
		return fallback(fmt.Sprintf("row axis exceeds row budget %d", base.DataBudget.MaxRows))
	}
	totalRowRequested := pivot.Totals != nil && (projectsignals.ValueOrZero(pivot.Totals.Columns) || projectsignals.ValueOrZero(pivot.Totals.Grand))
	if totalRowRequested && base.DataBudget.MaxRows > 0 && int64(len(rowAxes)+1) > base.DataBudget.MaxRows {
		return fallback(fmt.Sprintf("pivot total row exceeds row budget %d", base.DataBudget.MaxRows))
	}
	if int64(len(rowAxes))*int64(len(columnAxes))*int64(len(metricFields)) > dataExplorerPivotMaxCells {
		return fallback(fmt.Sprintf("pivot shape exceeds %d cells", dataExplorerPivotMaxCells))
	}
	pivotTotals, err := explorerPivotExactTotals(pivot.Totals, result.PivotTotals, rowFields, columnFields, metricFields, rowAxes, columnAxes)
	if err != nil {
		return fallback(err.Error())
	}

	usedKeys := map[string]string{}
	dynamicKeys := map[string]string{}
	rowTotalKeys := map[string]string{}
	grandTotalKeys := map[string]string{}
	dynamicColumns := make([]dashboard.TableColumn, 0, len(columnAxes)*len(metricFields))
	dynamicFields := make([]visualizationir.VisualizationField, 0, cap(dynamicColumns))
	for _, axis := range columnAxes {
		for _, metric := range metricFields {
			identity := axis.Identity + "\x00" + metric.Semantic
			key := explorerPivotUniqueKey(axis.Label+"_"+metric.Label, identity, usedKeys)
			dynamicKeys[identity] = key
			label := axis.Label + " / " + metric.Label
			dynamicColumns = append(dynamicColumns, dashboard.TableColumn{Key: key, Label: label, Align: "right", Role: "metric", Group: axis.Label, Metric: metric.Semantic, ColumnValue: axis.Label, DataType: string(metric.DataType)})
			dynamicFields = append(dynamicFields, visualizationir.VisualizationField{ID: key, Role: visualizationir.VisualizationFieldRoleMetric, DataType: metric.DataType, Nullable: true, Label: label, Format: metric.Format, Grid: &visualizationir.VisualizationGridFieldMetadata{Group: optionalExplorerString(axis.Label), Metric: optionalExplorerString(metric.Semantic), ColumnValue: optionalExplorerString(axis.Label)}})
		}
	}
	if pivotTotals.RowsRequested {
		for _, metric := range metricFields {
			identity := "total\x00" + metric.Semantic
			key := explorerPivotUniqueKey("total_"+metric.Label, identity, usedKeys)
			rowTotalKeys[metric.Semantic] = key
			label := "Total / " + metric.Label
			dynamicColumns = append(dynamicColumns, dashboard.TableColumn{Key: key, Label: label, Align: "right", Role: "metric", Group: "Total", Metric: metric.Semantic, ColumnValue: "Total", DataType: string(metric.DataType)})
			dynamicFields = append(dynamicFields, visualizationir.VisualizationField{ID: key, Role: visualizationir.VisualizationFieldRoleMetric, DataType: metric.DataType, Nullable: true, Label: label, Format: metric.Format, Grid: &visualizationir.VisualizationGridFieldMetadata{Group: optionalExplorerString("Total"), Metric: optionalExplorerString(metric.Semantic), ColumnValue: optionalExplorerString("Total")}})
		}
	}
	if pivotTotals.GrandRequested {
		for _, metric := range metricFields {
			identity := "grand\x00" + metric.Semantic
			key := explorerPivotUniqueKey("grand_"+metric.Label, identity, usedKeys)
			grandTotalKeys[metric.Semantic] = key
			label := "Grand total / " + metric.Label
			dynamicColumns = append(dynamicColumns, dashboard.TableColumn{Key: key, Label: label, Align: "right", Role: "metric", Group: "Grand total", Metric: metric.Semantic, ColumnValue: "Grand total", DataType: string(metric.DataType)})
			dynamicFields = append(dynamicFields, visualizationir.VisualizationField{ID: key, Role: visualizationir.VisualizationFieldRoleMetric, DataType: metric.DataType, Nullable: true, Label: label, Format: metric.Format, Grid: &visualizationir.VisualizationGridFieldMetadata{Group: optionalExplorerString("Grand total"), Metric: optionalExplorerString(metric.Semantic), ColumnValue: optionalExplorerString("Grand total")}})
		}
	}

	windowColumns := make([]dashboard.TableColumn, 0, len(rowFields)+len(dynamicColumns))
	for index, rowField := range rowFields {
		role := "dimension"
		if index == 0 {
			role = "row_header"
		}
		windowColumns = append(windowColumns, dashboard.TableColumn{Key: rowField.Output, Label: rowField.Label, Role: role, DataType: string(rowField.DataType)})
	}
	windowColumns = append(windowColumns, dynamicColumns...)

	rowsByIdentity := make(map[string]map[string]any, len(rowAxes))
	for _, axis := range rowAxes {
		row := make(map[string]any, len(windowColumns))
		for index, field := range rowFields {
			row[field.Output] = axis.Values[index]
		}
		rowsByIdentity[axis.Identity] = row
	}
	for _, cell := range cells {
		key := dynamicKeys[cell.ColumnIdentity+"\x00"+cell.Metric.Semantic]
		rowsByIdentity[cell.RowIdentity][key] = cell.Value
	}
	if pivotTotals.RowsRequested {
		for _, axis := range rowAxes {
			row := rowsByIdentity[axis.Identity]
			total := pivotTotals.Rows[axis.Identity]
			for _, metric := range metricFields {
				row[rowTotalKeys[metric.Semantic]] = explorerPivotMetricTotalValue(total, metric)
			}
		}
	}
	windowRows := make([]map[string]any, 0, len(rowAxes))
	for _, axis := range rowAxes {
		windowRows = append(windowRows, rowsByIdentity[axis.Identity])
	}
	if pivotTotals.ColumnsRequested || pivotTotals.GrandRequested {
		totalRow := make(map[string]any, len(windowColumns))
		// Keep every row identity typed. A synthetic "Total" string would
		// violate numeric/date row schemas, so total rows use null identities.
		for _, field := range rowFields {
			totalRow[field.Output] = nil
		}
		if pivotTotals.ColumnsRequested {
			for _, axis := range columnAxes {
				total := pivotTotals.Columns[axis.Identity]
				for _, metric := range metricFields {
					key := dynamicKeys[axis.Identity+"\x00"+metric.Semantic]
					totalRow[key] = explorerPivotMetricTotalValue(total, metric)
				}
			}
		}
		if pivotTotals.GrandRequested {
			for _, metric := range metricFields {
				totalRow[grandTotalKeys[metric.Semantic]] = explorerPivotMetricTotalValue(pivotTotals.Grand, metric)
			}
		}
		windowRows = append(windowRows, totalRow)
	}

	pivotFields := append([]visualizationir.VisualizationField{}, explorerIRFields(sourceColumns)...)
	pivotFields = append(pivotFields, dynamicFields...)
	pivotBase := base
	pivotBase.Kind = "pivot"
	pivotBase.Datasets = []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: pivotFields}}
	// Column/grand totals add a synthetic row with null row-axis values. Do
	// not expose that row as a drill selection: it would otherwise become an
	// incorrect governed is-null filter. Row totals alone retain safe row
	// interactions because their row-axis identity remains concrete.
	if pivotTotals.ColumnsRequested || pivotTotals.GrandRequested {
		pivotBase.Interactions = []visualizationir.VisualizationInteraction{}
	} else {
		pivotBase.Interactions = explorerVisualizationInteractions(rowFields)
	}
	rows := explorerPivotIRRefs(rowFields)
	columnRefs := explorerPivotIRRefs(columnFields)
	metricRefs := explorerPivotIRRefs(metricFields)
	visualSpec := visualizationir.VisualizationSpec{Value: &visualizationir.PivotVisualizationSpec{VisualizationSpecBase: pivotBase, Kind: "pivot", Rows: rows, Columns: columnRefs, Metrics: metricRefs, MetricFormatting: map[string][]visualizationir.TableVisualizationFormattingRule{}, Presentation: visualizationir.GridVisualizationPresentation{RowHeight: explorerPivotRowHeight(spec), ShowHeader: true}}}
	query := visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryPivot, ResultShape: visualizationdefinition.ResultPivotWindow, ModelID: modelID, DatasetID: "primary", Pivot: &visualizationdefinition.PivotQueryBinding{TableID: datasetID, Rows: explorerAggregateFields(rowFields), Columns: explorerAggregateFields(columnFields), Metrics: explorerAggregateFields(metricFields), Limit: pivotBase.DataBudget.MaxRows}}
	pivotSorts := []exploration.ExplorationSort(nil)
	if pivot.Sort != nil {
		pivotSorts = *pivot.Sort
	}
	tableSort := explorerDashboardTableSort(pivotSorts, rowFields)
	table := dashboard.Table{Kind: "pivot_table", Title: pivotBase.Title, Columns: windowColumns, Cardinality: dashboard.ExactCardinality(len(windowRows)), AvailableRows: len(windowRows), IsCapped: false, RowCap: int(pivotBase.DataBudget.MaxRows), ChunkSize: explorerMaterializedChunkSize(len(windowRows)), RowHeight: int(explorerPivotRowHeight(spec)), ResetVersion: 0, Sort: tableSort, Blocks: map[string]dashboard.TableBlock{"a": {Start: 0, RequestSeq: int(explorerDataRevision(result)), ResetVersion: 0, Sort: tableSort, Rows: windowRows}}}
	definition, err := visualizationdefinition.New(dataExplorerPivotViewID, visualSpec, query)
	if err != nil {
		return fallback("compiled pivot failed validation: " + err.Error())
	}
	envelope, err := visualizationruntime.WindowEnvelopeFromDefinition(definition, table, explorerDataRevision(result), explorerGeneration(result))
	if err != nil {
		return fallback("pivot frame failed validation: " + err.Error())
	}
	return dataExplorerPivotViewID, &envelope, ""
}

type explorerPivotTotalsProjection struct {
	RowsRequested    bool
	ColumnsRequested bool
	GrandRequested   bool
	Rows             map[string]projectsignals.DataExplorePivotTotalSignal
	Columns          map[string]projectsignals.DataExplorePivotTotalSignal
	Grand            projectsignals.DataExplorePivotTotalSignal
}

func explorerPivotExactTotals(requested *exploration.ExplorationPivotTotals, payload *projectsignals.DataExplorePivotTotalsSignal, rowFields, columnFields, metricFields []explorerVisualizationColumn, rowAxes, columnAxes []explorerPivotAxisValue) (explorerPivotTotalsProjection, error) {
	projection := explorerPivotTotalsProjection{Rows: map[string]projectsignals.DataExplorePivotTotalSignal{}, Columns: map[string]projectsignals.DataExplorePivotTotalSignal{}}
	if requested == nil || !explorerPivotTotalsRequested(requested) {
		return projection, nil
	}
	projection.RowsRequested = projectsignals.ValueOrZero(requested.Rows)
	projection.ColumnsRequested = projectsignals.ValueOrZero(requested.Columns)
	projection.GrandRequested = projectsignals.ValueOrZero(requested.Grand)
	if payload == nil {
		return projection, fmt.Errorf("exact governed totals are missing")
	}
	if payload.Status != "complete" {
		return projection, fmt.Errorf("exact governed totals are %s", payload.Status)
	}
	if projection.RowsRequested {
		values, err := explorerPivotTotalIndex(payload.Rows, rowFields, metricFields, rowAxes, "row")
		if err != nil {
			return projection, err
		}
		projection.Rows = values
	}
	if projection.ColumnsRequested {
		values, err := explorerPivotTotalIndex(payload.Columns, columnFields, metricFields, columnAxes, "column")
		if err != nil {
			return projection, err
		}
		projection.Columns = values
	}
	if projection.GrandRequested {
		if len(payload.Grand) != 1 {
			return projection, fmt.Errorf("exact governed grand totals require one keyed result, got %d", len(payload.Grand))
		}
		if err := explorerPivotValidateTotalValues(payload.Grand[0], metricFields, "grand"); err != nil {
			return projection, err
		}
		projection.Grand = payload.Grand[0]
	}
	return projection, nil
}

func explorerPivotTotalIndex(entries []projectsignals.DataExplorePivotTotalSignal, fields, metrics []explorerVisualizationColumn, axes []explorerPivotAxisValue, name string) (map[string]projectsignals.DataExplorePivotTotalSignal, error) {
	if len(entries) != len(axes) {
		return nil, fmt.Errorf("exact governed %s totals are incomplete: got %d entries for %d keys", name, len(entries), len(axes))
	}
	indexed := make(map[string]projectsignals.DataExplorePivotTotalSignal, len(entries))
	for index, entry := range entries {
		identity, err := explorerPivotTotalKey(entry, fields)
		if err != nil {
			return nil, fmt.Errorf("exact governed %s totals entry %d: %w", name, index, err)
		}
		if _, exists := indexed[identity]; exists {
			return nil, fmt.Errorf("exact governed %s totals contain duplicate key", name)
		}
		if err := explorerPivotValidateTotalValues(entry, metrics, name); err != nil {
			return nil, err
		}
		indexed[identity] = entry
	}
	for _, axis := range axes {
		if _, exists := indexed[axis.Identity]; !exists {
			return nil, fmt.Errorf("exact governed %s totals are missing key", name)
		}
	}
	return indexed, nil
}

func explorerPivotTotalKey(entry projectsignals.DataExplorePivotTotalSignal, fields []explorerVisualizationColumn) (string, error) {
	values := make([]any, len(fields))
	for index, field := range fields {
		value, ok := entry.Key[field.Output]
		if !ok && field.Semantic != field.Output {
			value, ok = entry.Key[field.Semantic]
		}
		if !ok {
			return "", fmt.Errorf("missing key field %q", field.Output)
		}
		values[index] = value
	}
	return explorerPivotTupleIdentity(values), nil
}

func explorerPivotValidateTotalValues(entry projectsignals.DataExplorePivotTotalSignal, metrics []explorerVisualizationColumn, name string) error {
	for _, metric := range metrics {
		if _, ok := entry.Values[metric.Output]; !ok {
			if _, ok = entry.Values[metric.Semantic]; !ok {
				return fmt.Errorf("exact governed %s totals are missing metric %q", name, metric.Output)
			}
		}
	}
	return nil
}

func explorerPivotMetricTotalValue(entry projectsignals.DataExplorePivotTotalSignal, metric explorerVisualizationColumn) any {
	if value, ok := entry.Values[metric.Output]; ok {
		return value
	}
	return entry.Values[metric.Semantic]
}

func explorerPivotTotalsRequested(totals *exploration.ExplorationPivotTotals) bool {
	return totals != nil && (projectsignals.ValueOrZero(totals.Rows) || projectsignals.ValueOrZero(totals.Columns) || projectsignals.ValueOrZero(totals.Grand))
}

func explorerPivotValues(record map[string]any, fields []explorerVisualizationColumn) []any {
	values := make([]any, len(fields))
	for index, field := range fields {
		values[index] = record[field.Output]
	}
	return values
}

func explorerPivotTupleIdentity(values []any) string {
	var result strings.Builder
	for _, value := range values {
		if value == nil {
			result.WriteString("<nil>;0:")
			continue
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			encoded = []byte(fmt.Sprintf("%v", value))
		}
		result.WriteString(fmt.Sprintf("%T;%d:", value, len(encoded)))
		result.Write(encoded)
	}
	return result.String()
}

func explorerPivotTupleLabel(values []any) string {
	labels := make([]string, len(values))
	for index, value := range values {
		if value == nil {
			labels[index] = "null"
		} else {
			labels[index] = fmt.Sprint(value)
		}
	}
	return strings.Join(labels, " / ")
}

func explorerPivotUniqueKey(label, identity string, used map[string]string) string {
	base := strings.ToLower(strings.TrimSpace(label))
	var cleaned strings.Builder
	for _, character := range base {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			cleaned.WriteRune(character)
		} else {
			cleaned.WriteByte('_')
		}
	}
	base = strings.Trim(cleaned.String(), "_")
	if base == "" {
		base = "value"
	}
	if len(base) > 48 {
		base = base[:48]
	}
	base = "pivot_" + base
	key := base
	for suffix := 2; ; suffix++ {
		if previous, exists := used[key]; !exists || previous == identity {
			used[key] = identity
			return key
		}
		key = fmt.Sprintf("%s_%d", base, suffix)
	}
}

func explorerPivotIRRefs(fields []explorerVisualizationColumn) []visualizationir.VisualizationFieldRef {
	refs := make([]visualizationir.VisualizationFieldRef, 0, len(fields))
	for _, field := range fields {
		refs = append(refs, visualizationir.VisualizationFieldRef{Dataset: "primary", Field: field.Output})
	}
	return refs
}

func optionalExplorerString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func explorerPivotRowHeight(spec exploration.ExplorationSpec) int64 {
	if spec.Table != nil {
		if spec.Table.RowHeight != nil && *spec.Table.RowHeight > 0 {
			return int64(*spec.Table.RowHeight)
		}
		if spec.Table.Density != nil {
			switch *spec.Table.Density {
			case exploration.ExplorationTableDensityCompact:
				return 28
			case exploration.ExplorationTableDensityComfortable:
				return 36
			}
		}
	}
	return 32
}
