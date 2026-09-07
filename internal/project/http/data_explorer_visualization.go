package http

// This file is the deliberately small bridge between the governed Data
// Explorer result and the renderer-independent visualization IR.  It does
// not execute a query or select a renderer-owned protocol: all rows and
// metadata come from the already-authorized result signal.

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/dashboard"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

const (
	dataExplorerTableViewID       = "table"
	dataExplorerKPIViewID         = "kpi"
	dataExplorerLineViewID        = "line"
	dataExplorerBarViewID         = "bar"
	dataExplorerDonutViewID       = "donut"
	dataExplorerPivotViewID       = "pivot"
	dataExplorerDatasetIDFallback = "result"
	dataExplorerPivotMaxColumns   = 64
	dataExplorerPivotMaxCells     = 4096
)

// DataExplorerVisualizationProjection contains only existing Visualization
// Envelope values. The table is always attempted and is the safe default.
type DataExplorerVisualizationProjection struct {
	Views           map[string]visualizationir.VisualizationEnvelope
	RecommendedView string
	DefaultView     string
	Warnings        []string
}

type explorerVisualizationColumn struct {
	Output   string
	Semantic string
	Dataset  string
	Label    string
	Role     visualizationir.VisualizationFieldRole
	DataType visualizationir.VisualizationDataType
	Temporal bool
	Grain    string
	Format   *visualizationir.VisualizationFormat
}

// ProjectDataExplorerViews lowers one canonical exploration result into
// validated IR envelopes. It is intentionally non-panicking: malformed or
// unsupported authored visualization settings only add a bounded warning and
// leave the table view selected.
func ProjectDataExplorerViews(spec exploration.ExplorationSpec, result projectsignals.DataExploreResultSignal, fields []projectsignals.DataExploreFieldSignal) DataExplorerVisualizationProjection {
	projection := DataExplorerVisualizationProjection{
		Views:           map[string]visualizationir.VisualizationEnvelope{},
		RecommendedView: dataExplorerTableViewID,
		DefaultView:     dataExplorerTableViewID,
	}
	columns, warnings := explorerVisualizationColumns(spec, result, fields)
	projection.Warnings = append(projection.Warnings, warnings...)
	if len(columns) == 0 {
		projection.Warnings = append(projection.Warnings, "visualization projection has no result columns; table view is unavailable")
		return projection
	}
	// The execution boundary normally truncates before projection. Keep this
	// boundary safe for callers that provide a result directly: never emit a
	// complete-looking pivot (or materialize rows beyond its configured window).
	if limit := explorerEffectiveLimit(spec); limit > 0 && int64(len(result.Rows)) > limit {
		projection.Warnings = append(projection.Warnings, fmt.Sprintf("result exceeds effective row budget %d; truncating visualization frame", limit))
		result.Rows = append([]map[string]any(nil), result.Rows[:limit]...)
		result.Truncated = true
	}

	modelID := strings.TrimSpace(spec.ModelID)
	if modelID == "" {
		modelID = "exploration"
	}
	datasetID := strings.TrimSpace(projectsignals.ValueOrZero(spec.DatasetID))
	if datasetID == "" {
		datasetID = dataExplorerDatasetIDFallback
	}
	base := explorerVisualizationBase(spec, columns, result, result.Truncated)
	frame := explorerVisualizationFrame(result, columns)

	if envelope, err := explorerTableEnvelope(spec, base, frame, modelID, datasetID, result, columns); err == nil {
		projection.Views[dataExplorerTableViewID] = envelope
	} else {
		projection.Warnings = append(projection.Warnings, "table visualization projection failed: "+err.Error())
		return projection
	}

	viewID, candidate, warning := explorerVisualizationCandidate(spec, result, columns, base, frame, modelID, datasetID)
	if warning != "" {
		projection.Warnings = append(projection.Warnings, warning)
	}
	if candidate != nil {
		projection.Views[viewID] = *candidate
		projection.RecommendedView = viewID
	}
	return projection
}

func explorerVisualizationBase(spec exploration.ExplorationSpec, columns []explorerVisualizationColumn, result projectsignals.DataExploreResultSignal, truncated bool) visualizationir.VisualizationSpecBase {
	title := "Explore"
	var authoredBase *exploration.ExplorationVisualizationConfigBase
	if base, err := spec.Visualization.Base(); err == nil && base != nil {
		authoredBase = base
	}
	if authoredBase != nil && strings.TrimSpace(projectsignals.ValueOrZero(authoredBase.Title)) != "" {
		title = strings.TrimSpace(projectsignals.ValueOrZero(authoredBase.Title))
	} else if dataset := strings.TrimSpace(projectsignals.ValueOrZero(spec.DatasetID)); dataset != "" {
		title = "Explore " + dataset
	}
	maxRows := explorerEffectiveLimit(spec)
	if maxRows <= 0 {
		maxRows = int64(len(result.Rows))
	}
	if maxRows < 1 {
		maxRows = 1
	}
	// A truncated result cannot promise complete data to the IR validator.
	required := visualizationir.VisualizationCompletenessComplete
	if truncated {
		required = visualizationir.VisualizationCompletenessTruncated
	}
	interactions := explorerVisualizationInteractions(columns)
	var subtitle *string
	if authoredBase != nil {
		subtitle = authoredBase.Subtitle
	}
	return visualizationir.VisualizationSpecBase{
		Kind:          title,
		Title:         title,
		Subtitle:      subtitle,
		Datasets:      []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: explorerIRFields(columns)}},
		DataBudget:    visualizationir.VisualizationDataBudget{MaxRows: maxRows, RequiredCompleteness: required},
		Accessibility: visualizationir.VisualizationAccessibility{Title: title, Description: "Governed Data Explorer result"},
		Interactions:  interactions,
	}
}

func explorerIRFields(columns []explorerVisualizationColumn) []visualizationir.VisualizationField {
	fields := make([]visualizationir.VisualizationField, 0, len(columns))
	for _, column := range columns {
		field := visualizationir.VisualizationField{ID: column.Output, Role: column.Role, DataType: column.DataType, Nullable: true, Label: column.Label}
		if column.Semantic != "" && column.Semantic != column.Output {
			semantic := column.Semantic
			field.SourceRef = &semantic
		}
		if column.Temporal {
			field.Time = &visualizationir.VisualizationTemporalMetadata{Grain: column.Grain, Timezone: "UTC", Calendar: "gregorian", WeekStart: visualizationir.VisualizationWeekStartMonday, Meaning: visualizationir.VisualizationTemporalMeaningBucket}
		}
		field.Format = column.Format
		fields = append(fields, field)
	}
	return fields
}

func explorerVisualizationInteractions(columns []explorerVisualizationColumn) []visualizationir.VisualizationInteraction {
	mappings := make([]visualizationir.VisualizationInteractionMapping, 0, len(columns))
	for _, column := range columns {
		if column.Role != visualizationir.VisualizationFieldRoleDimension || column.Semantic == "" {
			continue
		}
		targetDataset := column.Dataset
		mapping := visualizationir.VisualizationInteractionMapping{
			Source: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: column.Output}, TargetFieldID: column.Semantic,
		}
		if targetDataset != "" {
			mapping.TargetDatasetID = &targetDataset
		}
		if column.Grain != "" {
			grain := column.Grain
			mapping.Grain = &grain
		}
		mappings = append(mappings, mapping)
	}
	if len(mappings) == 0 {
		return []visualizationir.VisualizationInteraction{}
	}
	mode := visualizationir.VisualizationSelectionModeMultiple
	if len(mappings) == 1 {
		mode = visualizationir.VisualizationSelectionModeSingle
	}
	// The existing interaction command path consumes selection interactions;
	// the stable ID and semantic target preserve the drill lineage without
	// inventing a renderer-specific drill protocol.
	return []visualizationir.VisualizationInteraction{{ID: "explore-drill", Kind: visualizationir.VisualizationInteractionKindSelect, Mappings: mappings, Targets: []visualizationir.VisualizationInteractionTarget{}, Mode: mode, RequiresStableIdentity: false}}
}

func explorerVisualizationFrame(result projectsignals.DataExploreResultSignal, columns []explorerVisualizationColumn) visualizationruntime.Frame {
	rows := make([][]any, len(result.Rows))
	for rowIndex, record := range result.Rows {
		rows[rowIndex] = make([]any, len(columns))
		for columnIndex, column := range columns {
			rows[rowIndex][columnIndex] = record[column.Output]
		}
	}
	columnNames := make([]string, len(columns))
	for index, column := range columns {
		columnNames[index] = column.Output
	}
	completeness := visualizationir.VisualizationCompletenessComplete
	if result.Truncated {
		completeness = visualizationir.VisualizationCompletenessTruncated
	}
	return visualizationruntime.Frame{Columns: columnNames, Rows: rows, Completeness: completeness}
}

func explorerTableEnvelope(spec exploration.ExplorationSpec, base visualizationir.VisualizationSpecBase, frame visualizationruntime.Frame, modelID, datasetID string, result projectsignals.DataExploreResultSignal, sourceColumns []explorerVisualizationColumn) (visualizationir.VisualizationEnvelope, error) {
	base.Kind = "table"
	selectedColumns := sourceColumns
	if spec.Table != nil && spec.Table.Columns != nil && len(*spec.Table.Columns) > 0 {
		selectedColumns = make([]explorerVisualizationColumn, 0, len(*spec.Table.Columns))
		for _, authored := range *spec.Table.Columns {
			column, ok := explorerColumnFor(sourceColumns, authored.Field)
			if !ok {
				return visualizationir.VisualizationEnvelope{}, fmt.Errorf("table field %q is not present in the governed result", authored.Field)
			}
			if authored.Label != nil && strings.TrimSpace(*authored.Label) != "" {
				column.Label = strings.TrimSpace(*authored.Label)
			}
			selectedColumns = append(selectedColumns, column)
		}
	}
	columns := make([]visualizationir.TableVisualizationColumn, 0, len(selectedColumns))
	queryFields := make([]visualizationdefinition.FieldBinding, 0, len(selectedColumns))
	for index := range selectedColumns {
		source := selectedColumns[index]
		column := visualizationir.TableVisualizationColumn{Field: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: source.Output}, Label: source.Label, Formatting: []visualizationir.TableVisualizationFormattingRule{}}
		if spec.Table != nil && spec.Table.Columns != nil && index < len(*spec.Table.Columns) {
			authored := (*spec.Table.Columns)[index]
			if authored.Width != nil {
				width := int64(*authored.Width)
				column.Width = &width
			}
			selectedColumns[index].Format = explorerVisualizationFormat(authored.Format)
		}
		columns = append(columns, column)
		queryFields = append(queryFields, visualizationdefinition.FieldBinding{FieldID: source.Semantic, Alias: source.Output, Grain: source.Grain})
	}
	rowHeight, striped, showHeader := int64(32), false, true
	if spec.Table != nil {
		if spec.Table.RowHeight != nil {
			rowHeight = int64(*spec.Table.RowHeight)
		} else if spec.Table.Density != nil {
			switch *spec.Table.Density {
			case exploration.ExplorationTableDensityCompact:
				rowHeight = 28
			case exploration.ExplorationTableDensityComfortable:
				rowHeight = 36
			}
		}
		if spec.Table.Striped != nil {
			striped = *spec.Table.Striped
		}
		if spec.Table.ShowHeader != nil {
			showHeader = *spec.Table.ShowHeader
		}
	}
	base.Datasets = []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: explorerIRFields(selectedColumns)}}
	base.Interactions = explorerVisualizationInteractions(selectedColumns)
	visualSpec := visualizationir.VisualizationSpec{Value: &visualizationir.TableVisualizationSpec{VisualizationSpecBase: base, Kind: "table", Columns: columns, Presentation: visualizationir.GridVisualizationPresentation{RowHeight: rowHeight, Striped: striped, ShowHeader: showHeader}}}
	query := visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryDetail, ResultShape: visualizationdefinition.ResultDetailWindow, ModelID: modelID, DatasetID: "primary", Detail: &visualizationdefinition.DetailQueryBinding{TableID: datasetID, Fields: queryFields, Limit: base.DataBudget.MaxRows}}
	definition, err := visualizationdefinition.New("table", visualSpec, query)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	tableSort := explorerDashboardTableSort(spec.Sort, selectedColumns)
	table := explorerDashboardTable(base, frame, result, selectedColumns, rowHeight, tableSort)
	return visualizationruntime.WindowEnvelopeFromDefinition(definition, table, explorerDataRevision(result), explorerGeneration(result))
}

func explorerDashboardTableSort(sorts []exploration.ExplorationSort, columns []explorerVisualizationColumn) dashboard.TableSort {
	if len(columns) == 0 {
		return dashboard.TableSort{}
	}
	result := dashboard.TableSort{Key: columns[0].Output, Direction: "asc"}
	if len(sorts) == 0 {
		return result
	}
	column, ok := explorerColumnFor(columns, sorts[0].Field)
	if !ok {
		return result
	}
	result.Key = column.Output
	if sorts[0].Direction == exploration.ExplorationSortDirectionDesc {
		result.Direction = "desc"
	}
	return result
}

func explorerDashboardTable(base visualizationir.VisualizationSpecBase, frame visualizationruntime.Frame, result projectsignals.DataExploreResultSignal, sourceColumns []explorerVisualizationColumn, rowHeight int64, tableSort dashboard.TableSort) dashboard.Table {
	columns := make([]dashboard.TableColumn, 0, len(sourceColumns))
	for _, source := range sourceColumns {
		role := "dimension"
		if source.Role == visualizationir.VisualizationFieldRoleMetric {
			role = "metric"
		}
		columns = append(columns, dashboard.TableColumn{Key: source.Output, Label: source.Label, Role: role, DataType: string(source.DataType)})
	}
	rows := make([]map[string]any, len(result.Rows))
	for rowIndex, source := range result.Rows {
		row := make(map[string]any, len(columns))
		for _, column := range columns {
			row[column.Key] = source[column.Key]
		}
		rows[rowIndex] = row
	}
	cardinality := dashboard.ExactCardinality(len(rows))
	if result.Truncated {
		cardinality = dashboard.LowerBoundCardinality(len(rows))
	}
	block := dashboard.TableBlock{Start: 0, RequestSeq: int(result.RequestSeq), Rows: rows, Sort: tableSort}
	return dashboard.Table{Kind: "table", Title: base.Title, Columns: columns, Cardinality: cardinality, AvailableRows: len(rows), IsCapped: result.Truncated, RowCap: int(base.DataBudget.MaxRows), ChunkSize: explorerMaterializedChunkSize(len(rows)), RowHeight: int(rowHeight), Blocks: map[string]dashboard.TableBlock{"a": block}, Sort: block.Sort, ResetVersion: 0}
}

func explorerMaterializedChunkSize(rowCount int) int {
	if rowCount < 1 {
		return 1
	}
	return rowCount
}

func explorerVisualizationFormat(format *exploration.VisualizationFormat) *visualizationir.VisualizationFormat {
	if format == nil || format.Value == nil {
		return nil
	}
	var value visualizationir.VisualizationFormatVariant
	switch variant := format.Value.(type) {
	case *exploration.NumberVisualizationFormat:
		value = &visualizationir.NumberVisualizationFormat{Kind: "number", MinimumFractionDigits: variant.MinimumFractionDigits, MaximumFractionDigits: variant.MaximumFractionDigits}
	case *exploration.CurrencyVisualizationFormat:
		value = &visualizationir.CurrencyVisualizationFormat{Kind: "currency", Currency: variant.Currency, MinimumFractionDigits: variant.MinimumFractionDigits, MaximumFractionDigits: variant.MaximumFractionDigits}
	case *exploration.PercentVisualizationFormat:
		value = &visualizationir.PercentVisualizationFormat{Kind: "percent", MinimumFractionDigits: variant.MinimumFractionDigits, MaximumFractionDigits: variant.MaximumFractionDigits}
	case *exploration.CompactVisualizationFormat:
		value = &visualizationir.CompactVisualizationFormat{Kind: "compact", MaximumFractionDigits: variant.MaximumFractionDigits}
	case *exploration.DurationVisualizationFormat:
		value = &visualizationir.DurationVisualizationFormat{Kind: "duration", Unit: variant.Unit}
	case *exploration.TemporalVisualizationFormat:
		value = &visualizationir.TemporalVisualizationFormat{Kind: "temporal", DateStyle: variant.DateStyle, TimeStyle: variant.TimeStyle}
	default:
		return nil
	}
	return &visualizationir.VisualizationFormat{Value: value}
}

func explorerDataRevision(result projectsignals.DataExploreResultSignal) int64 {
	if result.RequestSeq > 0 {
		return result.RequestSeq
	}
	return 1
}

func explorerGeneration(result projectsignals.DataExploreResultSignal) int64 {
	if result.RequestSeq > 0 {
		return result.RequestSeq
	}
	return 0
}

func explorerVisualizationCandidate(spec exploration.ExplorationSpec, result projectsignals.DataExploreResultSignal, columns []explorerVisualizationColumn, base visualizationir.VisualizationSpecBase, frame visualizationruntime.Frame, modelID, datasetID string) (string, *visualizationir.VisualizationEnvelope, string) {
	if spec.Pivot != nil {
		id, envelope, warning := explorerPivotEnvelope(spec, *spec.Pivot, base, result, columns, modelID, datasetID)
		if envelope == nil {
			return dataExplorerTableViewID, nil, warning
		}
		return id, envelope, warning
	}

	kind := ""
	if spec.Visualization != nil && spec.Visualization.Value != nil {
		kind, _ = spec.Visualization.Kind()
	}
	if kind != "" {
		switch value := spec.Visualization.Value.(type) {
		case *exploration.KPIExplorationVisualization:
			return explorerKPIEnvelope(spec, value, columns, base, frame, modelID, datasetID, result)
		case *exploration.CartesianExplorationVisualization:
			return explorerCartesianEnvelope(spec, value, columns, base, frame, modelID, datasetID, result)
		case *exploration.ProportionalExplorationVisualization:
			return explorerProportionalEnvelope(spec, value, columns, base, frame, modelID, datasetID, result)
		case *exploration.TableExplorationVisualization:
			return dataExplorerTableViewID, nil, ""
		default:
			return dataExplorerTableViewID, nil, "unsupported authored visualization kind " + kind + "; showing table"
		}
	}

	dimensions := explorerSelectedDimensions(spec, columns)
	metrics := explorerSelectedMetrics(spec, columns)
	if len(metrics) == 1 && len(dimensions) == 0 {
		return explorerKPIEnvelope(spec, nil, columns, base, frame, modelID, datasetID, result)
	}
	if len(dimensions) == 1 && len(metrics) >= 1 && dimensions[0].Temporal {
		return explorerCartesianEnvelope(spec, &exploration.CartesianExplorationVisualization{Kind: "cartesian", Mark: exploration.VisualizationCartesianMarkLine}, columns, base, frame, modelID, datasetID, result)
	}
	if len(dimensions) == 1 && len(metrics) == 1 && explorerCategoricalDimension(dimensions[0]) && explorerResultHasAtMostCategories(result, dimensions[0].Output, 7) && explorerResultHasNonnegativeValues(result, metrics[0].Output) {
		return explorerProportionalEnvelope(spec, &exploration.ProportionalExplorationVisualization{Kind: "proportional", Mark: exploration.VisualizationProportionalMarkDonut}, columns, base, frame, modelID, datasetID, result)
	}
	if len(dimensions) == 1 && len(metrics) >= 1 {
		return explorerCartesianEnvelope(spec, &exploration.CartesianExplorationVisualization{Kind: "cartesian", Mark: exploration.VisualizationCartesianMarkBar}, columns, base, frame, modelID, datasetID, result)
	}
	if len(dimensions) == 2 && len(metrics) >= 1 {
		// A temporal/category pair is a time series regardless of which axis
		// the authored selection listed first. Reorder only the inferred
		// projection so temporal data remains X and category remains series.
		if dimensions[0].Temporal != dimensions[1].Temporal {
			temporal, category := dimensions[0], dimensions[1]
			if !temporal.Temporal {
				temporal, category = category, temporal
			}
			if explorerCategoricalDimension(category) {
				ordered := explorerSpecWithDimensionOrder(spec, temporal, category)
				return explorerCartesianEnvelope(ordered, &exploration.CartesianExplorationVisualization{Kind: "cartesian", Mark: exploration.VisualizationCartesianMarkLine}, columns, base, frame, modelID, datasetID, result)
			}
		}
		return explorerCartesianEnvelope(spec, &exploration.CartesianExplorationVisualization{Kind: "cartesian", Mark: exploration.VisualizationCartesianMarkBar}, columns, base, frame, modelID, datasetID, result)
	}
	return dataExplorerTableViewID, nil, "result shape has no safe chart projection; showing table"
}

func explorerSpecWithDimensionOrder(spec exploration.ExplorationSpec, first, second explorerVisualizationColumn) exploration.ExplorationSpec {
	refs := make([]exploration.ExplorationDimensionRef, 0, 2)
	for _, column := range []explorerVisualizationColumn{first, second} {
		found := false
		for _, ref := range spec.Dimensions {
			if ref.Field == column.Output || ref.Field == column.Semantic {
				refs = append(refs, ref)
				found = true
				break
			}
		}
		if !found {
			ref := exploration.ExplorationDimensionRef{Field: column.Semantic}
			if spec.Time != nil && (spec.Time.Field == column.Output || spec.Time.Field == column.Semantic) {
				ref.Field = spec.Time.Field
				ref.Alias = spec.Time.Alias
				ref.Grain = &spec.Time.Grain
			}
			refs = append(refs, ref)
		}
	}
	spec.Dimensions = refs
	return spec
}

func explorerSelectedDimensions(spec exploration.ExplorationSpec, columns []explorerVisualizationColumn) []explorerVisualizationColumn {
	selected := make([]explorerVisualizationColumn, 0, len(spec.Dimensions)+1)
	for _, dimension := range spec.Dimensions {
		if column, ok := explorerColumnFor(columns, dimension.Field); ok {
			selected = append(selected, column)
		}
	}
	if spec.Time != nil {
		if column, ok := explorerColumnFor(columns, spec.Time.Field); ok {
			found := false
			for _, selectedColumn := range selected {
				if selectedColumn.Output == column.Output {
					found = true
				}
			}
			if !found {
				column.Temporal = true
				column.Grain = string(spec.Time.Grain)
				selected = append(selected, column)
			}
		}
	}
	return selected
}

func explorerSelectedMetrics(spec exploration.ExplorationSpec, columns []explorerVisualizationColumn) []explorerVisualizationColumn {
	selected := make([]explorerVisualizationColumn, 0, len(spec.Metrics))
	for _, metric := range spec.Metrics {
		if column, ok := explorerColumnFor(columns, metric.Field); ok {
			selected = append(selected, column)
		}
	}
	return selected
}

func explorerColumnFor(columns []explorerVisualizationColumn, ref string) (explorerVisualizationColumn, bool) {
	ref = strings.TrimSpace(ref)
	for _, column := range columns {
		if column.Output == ref || column.Semantic == ref {
			return column, true
		}
	}
	return explorerVisualizationColumn{}, false
}

func explorerCategoricalDimension(column explorerVisualizationColumn) bool {
	return !column.Temporal && column.DataType != visualizationir.VisualizationDataTypeInteger && column.DataType != visualizationir.VisualizationDataTypeDecimal && column.DataType != visualizationir.VisualizationDataTypeFloat
}

func explorerResultHasAtMostCategories(result projectsignals.DataExploreResultSignal, key string, maximum int) bool {
	seen := map[string]struct{}{}
	for _, row := range result.Rows {
		encoded, err := json.Marshal(row[key])
		if err != nil {
			return false
		}
		seen[string(encoded)] = struct{}{}
		if len(seen) > maximum {
			return false
		}
	}
	return len(seen) > 0
}

func explorerResultHasNonnegativeValues(result projectsignals.DataExploreResultSignal, key string) bool {
	if len(result.Rows) == 0 {
		return false
	}
	for _, row := range result.Rows {
		value, ok := explorerNumber(row[key])
		if !ok || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

func explorerNumber(value any) (float64, bool) {
	switch value := value.(type) {
	case int:
		return float64(value), true
	case int8:
		return float64(value), true
	case int16:
		return float64(value), true
	case int32:
		return float64(value), true
	case int64:
		return float64(value), true
	case uint:
		return float64(value), true
	case uint8:
		return float64(value), true
	case uint16:
		return float64(value), true
	case uint32:
		return float64(value), true
	case uint64:
		return float64(value), true
	case float32:
		return float64(value), true
	case float64:
		return value, true
	case json.Number:
		n, err := strconv.ParseFloat(string(value), 64)
		return n, err == nil
	case string:
		n, err := strconv.ParseFloat(value, 64)
		return n, err == nil
	default:
		return 0, false
	}
}

func explorerKPIEnvelope(spec exploration.ExplorationSpec, authored *exploration.KPIExplorationVisualization, columns []explorerVisualizationColumn, base visualizationir.VisualizationSpecBase, frame visualizationruntime.Frame, modelID, datasetID string, result projectsignals.DataExploreResultSignal) (string, *visualizationir.VisualizationEnvelope, string) {
	if authored != nil && (len(spec.Dimensions) > 0 || spec.Time != nil) {
		return dataExplorerTableViewID, nil, "authored KPI requires a scalar result with no dimensions or time grouping; showing table"
	}
	if len(result.Rows) != 1 {
		return dataExplorerTableViewID, nil, fmt.Sprintf("KPI visualization requires exactly one scalar row; got %d; showing table", len(result.Rows))
	}
	for _, column := range columns {
		if column.Role != visualizationir.VisualizationFieldRoleMetric {
			return dataExplorerTableViewID, nil, "KPI visualization requires scalar result columns without dimensions; showing table"
		}
	}
	metrics := explorerSelectedMetrics(spec, columns)
	if authored != nil && authored.Value.Field != "" {
		ref, ok := explorerColumnFor(columns, authored.Value.Field)
		if !ok || ref.Role != visualizationir.VisualizationFieldRoleMetric || !numericExplorerColumn(ref) {
			return dataExplorerTableViewID, nil, "authored KPI value is unavailable or non-numeric; showing table"
		}
		metrics = []explorerVisualizationColumn{ref}
	}
	if len(metrics) != 1 {
		return dataExplorerTableViewID, nil, "KPI visualization requires exactly one selected metric; showing table"
	}
	metric := metrics[0]
	value := visualizationir.VisualizationFieldRef{Dataset: "primary", Field: metric.Output}
	value.Field = metric.Output
	base.Kind = "kpi"
	presentation := visualizationir.KPIVisualizationPresentation{Mode: visualizationir.VisualizationKPIModeCompact, Delta: visualizationir.VisualizationKPIDeltaModeAbsolute, FavorableDirection: visualizationir.VisualizationKPIDirectionNeutral, MissingComparison: visualizationir.VisualizationKPIMissingComparisonShowUnavailable, Ranges: []visualizationir.VisualizationKPIQualitativeRange{}}
	if authored != nil && authored.Presentation != nil {
		if authored.Presentation.Mode != nil {
			presentation.Mode = visualizationir.VisualizationKPIMode(*authored.Presentation.Mode)
		}
		if authored.Presentation.Delta != nil {
			presentation.Delta = visualizationir.VisualizationKPIDeltaMode(*authored.Presentation.Delta)
		}
		if authored.Presentation.FavorableDirection != nil {
			presentation.FavorableDirection = visualizationir.VisualizationKPIDirection(*authored.Presentation.FavorableDirection)
		}
		if authored.Presentation.MissingComparison != nil {
			presentation.MissingComparison = visualizationir.VisualizationKPIMissingComparison(*authored.Presentation.MissingComparison)
		}
		presentation.DisplayUnits = (*visualizationir.VisualizationDisplayUnits)(authored.Presentation.DisplayUnits)
		presentation.Note = authored.Presentation.Note
		presentation.Tone = (*visualizationir.VisualizationTone)(authored.Presentation.Tone)
	}
	if presentation.DisplayUnits == nil {
		if common, err := spec.Visualization.Base(); err == nil && common != nil && common.DisplayUnits != nil {
			presentation.DisplayUnits = (*visualizationir.VisualizationDisplayUnits)(common.DisplayUnits)
		}
	}
	visualSpec := visualizationir.VisualizationSpec{Value: &visualizationir.KPIVisualizationSpec{VisualizationSpecBase: base, Kind: "kpi", Value: value, Presentation: presentation}}
	query := visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultScalar, ModelID: modelID, DatasetID: "primary", Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: datasetID, Metrics: []visualizationdefinition.FieldBinding{{FieldID: metric.Semantic, Alias: metric.Output}}, Limit: base.DataBudget.MaxRows}}
	return explorerBuildInlineEnvelope(dataExplorerKPIViewID, visualSpec, query, frame, modelID, result)
}

func explorerCartesianEnvelope(spec exploration.ExplorationSpec, authored *exploration.CartesianExplorationVisualization, columns []explorerVisualizationColumn, base visualizationir.VisualizationSpecBase, frame visualizationruntime.Frame, modelID, datasetID string, result projectsignals.DataExploreResultSignal) (string, *visualizationir.VisualizationEnvelope, string) {
	dimensions := explorerSelectedDimensions(spec, columns)
	metrics := explorerSelectedMetrics(spec, columns)
	if len(dimensions) == 0 || len(metrics) == 0 || len(dimensions) > 2 {
		return dataExplorerTableViewID, nil, "cartesian visualization requires a dimension and metric; showing table"
	}
	mark := visualizationir.VisualizationCartesianMarkBar
	if authored != nil && authored.Mark != "" {
		mark = visualizationir.VisualizationCartesianMark(authored.Mark)
	}
	if mark != visualizationir.VisualizationCartesianMarkLine && mark != visualizationir.VisualizationCartesianMarkArea && mark != visualizationir.VisualizationCartesianMarkBar && mark != visualizationir.VisualizationCartesianMarkColumn {
		return dataExplorerTableViewID, nil, fmt.Sprintf("cartesian mark %q is not supported for exploration; showing table", mark)
	}
	xColumn := dimensions[0]
	x := visualizationir.VisualizationFieldRef{Dataset: "primary", Field: xColumn.Output}
	seriesColumn := explorerVisualizationColumn{}
	var series *visualizationir.VisualizationFieldRef
	if len(dimensions) == 2 {
		seriesColumn = dimensions[1]
		seriesValue := visualizationir.VisualizationFieldRef{Dataset: "primary", Field: seriesColumn.Output}
		series = &seriesValue
	}
	metricColumns := append([]explorerVisualizationColumn{}, metrics...)
	y := make([]visualizationir.VisualizationFieldRef, 0, len(metrics))
	for _, metric := range metricColumns {
		if !numericExplorerColumn(metric) {
			return dataExplorerTableViewID, nil, "cartesian visualization metric is non-numeric; showing table"
		}
		y = append(y, visualizationir.VisualizationFieldRef{Dataset: "primary", Field: metric.Output})
	}
	if authored != nil {
		if authored.X != nil {
			ref, ok := explorerColumnFor(columns, authored.X.Field)
			if !ok || ref.Role == visualizationir.VisualizationFieldRoleMetric {
				return dataExplorerTableViewID, nil, "authored cartesian x field is unavailable; showing table"
			}
			xColumn = ref
			x.Field = ref.Output
		}
		if authored.Y != nil {
			y = y[:0]
			metricColumns = metricColumns[:0]
			for _, authoredY := range *authored.Y {
				ref, ok := explorerColumnFor(columns, authoredY.Field)
				if !ok || ref.Role != visualizationir.VisualizationFieldRoleMetric || !numericExplorerColumn(ref) {
					return dataExplorerTableViewID, nil, "authored cartesian y field is unavailable or non-numeric; showing table"
				}
				y = append(y, visualizationir.VisualizationFieldRef{Dataset: "primary", Field: ref.Output})
				metricColumns = append(metricColumns, ref)
			}
		}
		if authored.Series != nil {
			ref, ok := explorerColumnFor(columns, authored.Series.Field)
			if !ok || ref.Role == visualizationir.VisualizationFieldRoleMetric {
				return dataExplorerTableViewID, nil, "authored cartesian series field is unavailable; showing table"
			}
			seriesValue := visualizationir.VisualizationFieldRef{Dataset: "primary", Field: ref.Output}
			if ref.Output == xColumn.Output {
				return dataExplorerTableViewID, nil, "authored cartesian x and series fields must differ; showing table"
			}
			seriesColumn = ref
			series = &seriesValue
		}
	}
	if series != nil && seriesColumn.Output == xColumn.Output {
		return dataExplorerTableViewID, nil, "authored cartesian x and series fields must differ; showing table"
	}
	queryDimensions := []explorerVisualizationColumn{xColumn}
	if series != nil {
		queryDimensions = append(queryDimensions, seriesColumn)
	}
	base.Kind = "cartesian"
	presentation := visualizationir.CartesianVisualizationPresentation{VisualizationPresentation: explorerVisualizationPresentation(spec), ShowSymbols: true}
	if authored != nil {
		presentation.Smooth = projectsignals.ValueOrZero(authored.Smooth)
		presentation.ShowSymbols = projectsignals.ValueOrZero(authored.ShowSymbols)
		if authored.Orientation != nil {
			orientation := visualizationir.VisualizationOrientation(*authored.Orientation)
			presentation.Orientation = &orientation
		}
		if authored.Stacking != nil {
			stacking := visualizationir.VisualizationStackingMode(*authored.Stacking)
			presentation.Stacking = &stacking
		}
		if authored.DisplayUnits != nil {
			presentation.DisplayUnits = (*visualizationir.VisualizationDisplayUnits)(authored.DisplayUnits)
		}
	}
	visualSpec := visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{VisualizationSpecBase: base, Kind: "cartesian", Mark: mark, X: x, Y: y, Series: series, Presentation: presentation}}
	shape := visualizationdefinition.ResultCategoryValue
	if series != nil {
		shape = visualizationdefinition.ResultCategorySeriesValue
	} else if len(y) > 1 {
		shape = visualizationdefinition.ResultCategoryMultiMeasure
	}
	query := visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryAggregate, ResultShape: shape, ModelID: modelID, DatasetID: "primary", Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: datasetID, Dimensions: explorerAggregateFields(queryDimensions), Metrics: explorerAggregateFields(metricColumns), Limit: base.DataBudget.MaxRows}}
	return explorerBuildInlineEnvelope(viewIDForCartesian(mark), visualSpec, query, frame, modelID, result)
}

func viewIDForCartesian(mark visualizationir.VisualizationCartesianMark) string {
	if mark == visualizationir.VisualizationCartesianMarkLine {
		return dataExplorerLineViewID
	}
	return dataExplorerBarViewID
}

func explorerProportionalEnvelope(spec exploration.ExplorationSpec, authored *exploration.ProportionalExplorationVisualization, columns []explorerVisualizationColumn, base visualizationir.VisualizationSpecBase, frame visualizationruntime.Frame, modelID, datasetID string, result projectsignals.DataExploreResultSignal) (string, *visualizationir.VisualizationEnvelope, string) {
	dimensions := explorerSelectedDimensions(spec, columns)
	metrics := explorerSelectedMetrics(spec, columns)
	if authored == nil && (len(dimensions) != 1 || len(metrics) != 1 || !numericExplorerColumn(metrics[0])) {
		return dataExplorerTableViewID, nil, "proportional visualization requires one categorical dimension and one numeric metric; showing table"
	}
	category, value := explorerVisualizationColumn{}, explorerVisualizationColumn{}
	if authored == nil {
		category, value = dimensions[0], metrics[0]
	}
	mark := visualizationir.VisualizationProportionalMarkDonut
	if authored != nil {
		mark = visualizationir.VisualizationProportionalMark(authored.Mark)
		if authored.Category.Field == "" {
			if len(dimensions) != 1 {
				return dataExplorerTableViewID, nil, "authored proportional category is ambiguous; showing table"
			}
			category = dimensions[0]
		} else {
			ref, ok := explorerColumnFor(columns, authored.Category.Field)
			if !ok || ref.Role == visualizationir.VisualizationFieldRoleMetric {
				return dataExplorerTableViewID, nil, "authored proportional category is unavailable; showing table"
			}
			category = ref
		}
		if authored.Value.Field == "" {
			if len(metrics) != 1 {
				return dataExplorerTableViewID, nil, "authored proportional value is ambiguous; showing table"
			}
			value = metrics[0]
		} else {
			ref, ok := explorerColumnFor(columns, authored.Value.Field)
			if !ok || ref.Role != visualizationir.VisualizationFieldRoleMetric || !numericExplorerColumn(ref) {
				return dataExplorerTableViewID, nil, "authored proportional value is unavailable or non-numeric; showing table"
			}
			value = ref
		}
	}
	if category.Output == "" || value.Output == "" || category.Role == visualizationir.VisualizationFieldRoleMetric || value.Role != visualizationir.VisualizationFieldRoleMetric || !numericExplorerColumn(value) {
		return dataExplorerTableViewID, nil, "proportional visualization requires a categorical dimension and numeric metric; showing table"
	}
	if len(dimensions) != 1 || dimensions[0].Output != category.Output || !explorerCategoricalDimension(category) {
		return dataExplorerTableViewID, nil, "authored proportional visualization requires exactly one selected categorical dimension matching the category; showing table"
	}
	if mark != visualizationir.VisualizationProportionalMarkPie && mark != visualizationir.VisualizationProportionalMarkDonut && mark != visualizationir.VisualizationProportionalMarkFunnel {
		return dataExplorerTableViewID, nil, fmt.Sprintf("proportional mark %q is unsupported; showing table", mark)
	}
	base.Kind = "proportional"
	propPresentation := visualizationir.ProportionalVisualizationPresentation{VisualizationPresentation: explorerVisualizationPresentation(spec), Orientation: visualizationir.VisualizationOrientationVertical}
	if authored != nil && authored.Orientation != nil {
		propPresentation.Orientation = visualizationir.VisualizationOrientation(*authored.Orientation)
	}
	visualSpec := visualizationir.VisualizationSpec{Value: &visualizationir.ProportionalVisualizationSpec{VisualizationSpecBase: base, Kind: "proportional", Mark: mark, Category: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: category.Output}, Value: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: value.Output}, Presentation: propPresentation}}
	query := visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultCategoryValue, ModelID: modelID, DatasetID: "primary", Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: datasetID, Dimensions: explorerAggregateFields([]explorerVisualizationColumn{category}), Metrics: explorerAggregateFields([]explorerVisualizationColumn{value}), Limit: base.DataBudget.MaxRows}}
	return explorerBuildInlineEnvelope(dataExplorerDonutViewID, visualSpec, query, frame, modelID, result)
}

func explorerVisualizationPresentation(spec exploration.ExplorationSpec) visualizationir.VisualizationPresentation {
	presentation := visualizationir.VisualizationPresentation{Legend: visualizationir.VisualizationLegendPositionHidden, LabelPolicy: visualizationir.VisualizationLabelPolicy{Density: visualizationir.VisualizationLabelDensityAutomatic, Priority: []visualizationir.VisualizationLabelPriority{}, MaxCharacters: 24, MinimumSpacing: 0, TooltipFallback: true}}
	if base, err := spec.Visualization.Base(); err == nil && base != nil {
		if base.Legend != nil {
			presentation.Legend = visualizationir.VisualizationLegendPosition(*base.Legend)
		}
		if base.DisplayUnits != nil {
			presentation.DisplayUnits = (*visualizationir.VisualizationDisplayUnits)(base.DisplayUnits)
		}
	}
	return presentation
}

func numericExplorerColumn(column explorerVisualizationColumn) bool {
	return column.DataType == visualizationir.VisualizationDataTypeInteger || column.DataType == visualizationir.VisualizationDataTypeDecimal || column.DataType == visualizationir.VisualizationDataTypeFloat
}

func explorerAggregateFields(columns []explorerVisualizationColumn) []visualizationdefinition.FieldBinding {
	fields := make([]visualizationdefinition.FieldBinding, 0, len(columns))
	for _, column := range columns {
		fields = append(fields, visualizationdefinition.FieldBinding{FieldID: column.Semantic, Alias: column.Output, Grain: column.Grain})
	}
	return fields
}

func explorerBuildInlineEnvelope(id string, spec visualizationir.VisualizationSpec, query visualizationdefinition.QueryBinding, frame visualizationruntime.Frame, modelID string, result projectsignals.DataExploreResultSignal) (string, *visualizationir.VisualizationEnvelope, string) {
	definition, err := visualizationdefinition.New(id, spec, query)
	if err != nil {
		return dataExplorerTableViewID, nil, "visualization validation failed: " + err.Error()
	}
	envelope, err := visualizationruntime.EnvelopeFromFrame(definition, frame, nil, explorerDataRevision(result), explorerGeneration(result))
	if err != nil {
		return dataExplorerTableViewID, nil, "visualization frame failed validation: " + err.Error()
	}
	return id, &envelope, ""
}

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

func explorerVisualizationColumns(spec exploration.ExplorationSpec, result projectsignals.DataExploreResultSignal, fields []projectsignals.DataExploreFieldSignal) ([]explorerVisualizationColumn, []string) {
	fieldByID := make(map[string]projectsignals.DataExploreFieldSignal, len(fields))
	for _, field := range fields {
		fieldByID[field.ID] = field
	}
	aliases := make(map[string]string)
	for _, field := range spec.Dimensions {
		aliases[field.Field] = firstExplorerNonEmpty(projectsignals.ValueOrZero(field.Alias), field.Field)
	}
	for _, field := range spec.Metrics {
		aliases[field.Field] = firstExplorerNonEmpty(projectsignals.ValueOrZero(field.Alias), field.Field)
	}
	if spec.Pivot != nil {
		for _, field := range spec.Pivot.Rows {
			aliases[field.Field] = firstExplorerNonEmpty(projectsignals.ValueOrZero(field.Alias), field.Field)
		}
		for _, field := range spec.Pivot.Columns {
			aliases[field.Field] = firstExplorerNonEmpty(projectsignals.ValueOrZero(field.Alias), field.Field)
		}
		for _, field := range spec.Pivot.Metrics {
			aliases[field.Field] = firstExplorerNonEmpty(projectsignals.ValueOrZero(field.Alias), field.Field)
		}
	}
	if spec.Time != nil {
		aliases[spec.Time.Field] = firstExplorerNonEmpty(aliases[spec.Time.Field], projectsignals.ValueOrZero(spec.Time.Alias), spec.Time.Field)
	}
	// Result columns use the deterministic query aliases (for example,
	// `status` for `orders.status`) when no authored alias exists. Keep the
	// semantic field ID qualified so pivot totals and drill targets retain
	// lineage while still matching the returned output key.
	for field, alias := range explorerSpecQueryAliases(spec) {
		if aliases[field] == "" || aliases[field] == field {
			aliases[field] = alias
		}
	}
	columns := make([]explorerVisualizationColumn, 0, len(result.Columns))
	warnings := []string{}
	seen := map[string]struct{}{}
	for _, column := range result.Columns {
		output := strings.TrimSpace(column.Key)
		if output == "" {
			warnings = append(warnings, "visualization projection ignored a result column without a key")
			continue
		}
		if _, exists := seen[output]; exists {
			warnings = append(warnings, fmt.Sprintf("visualization projection ignored duplicate result column %q", output))
			continue
		}
		seen[output] = struct{}{}
		semantic := output
		if field, ok := fieldByID[output]; ok {
			semantic = field.ID
		} else {
			for fieldID, alias := range aliases {
				if alias == output {
					semantic = fieldID
					break
				}
			}
		}
		metadata, hasMetadata := fieldByID[semantic]
		role := visualizationir.VisualizationFieldRoleDimension
		dataType := visualizationDataType(projectsignals.ValueOrZero(column.Type))
		dataset := strings.TrimSpace(projectsignals.ValueOrZero(spec.DatasetID))
		label := strings.TrimSpace(column.Label)
		if hasMetadata {
			dataset = firstExplorerNonEmpty(metadata.DatasetID, dataset)
			label = firstExplorerNonEmpty(label, metadata.Label, output)
			if metadata.Kind == "metric" {
				role = visualizationir.VisualizationFieldRoleMetric
			}
			if metadata.Type != nil {
				dataType = visualizationDataType(*metadata.Type)
			}
		} else {
			label = firstExplorerNonEmpty(label, output)
			for _, metric := range spec.Metrics {
				if metric.Field == semantic || aliases[metric.Field] == output {
					role = visualizationir.VisualizationFieldRoleMetric
				}
			}
		}
		temporal := dataType == visualizationir.VisualizationDataTypeDate || dataType == visualizationir.VisualizationDataTypeTemporal
		grain := ""
		if spec.Time != nil && (spec.Time.Field == semantic || aliases[spec.Time.Field] == output) {
			temporal = true
			grain = string(spec.Time.Grain)
		}
		for _, dimension := range spec.Dimensions {
			if dimension.Field == semantic || aliases[dimension.Field] == output {
				if dimension.Grain != nil {
					grain = string(*dimension.Grain)
				}
			}
		}
		columns = append(columns, explorerVisualizationColumn{Output: output, Semantic: semantic, Dataset: dataset, Label: label, Role: role, DataType: dataType, Temporal: temporal, Grain: grain})
	}
	return columns, warnings
}

func visualizationDataType(value string) visualizationir.VisualizationDataType {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "bool", "boolean":
		return visualizationir.VisualizationDataTypeBoolean
	case "int", "integer", "int32", "int64":
		return visualizationir.VisualizationDataTypeInteger
	case "decimal", "numeric":
		return visualizationir.VisualizationDataTypeDecimal
	case "float", "float32", "float64", "number":
		return visualizationir.VisualizationDataTypeFloat
	case "date":
		return visualizationir.VisualizationDataTypeDate
	case "datetime", "timestamp", "time", "temporal":
		return visualizationir.VisualizationDataTypeTemporal
	default:
		return visualizationir.VisualizationDataTypeString
	}
}
