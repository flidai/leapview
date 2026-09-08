package http

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

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
		return explorerCartesianEnvelope(spec, &exploration.CartesianExplorationVisualization{Kind: "cartesian", Mark: exploration.ExplorationVisualizationCartesianMarkLine}, columns, base, frame, modelID, datasetID, result)
	}
	if len(dimensions) == 1 && len(metrics) == 1 && explorerCategoricalDimension(dimensions[0]) && explorerResultHasAtMostCategories(result, dimensions[0].Output, 7) && explorerResultHasNonnegativeValues(result, metrics[0].Output) {
		return explorerProportionalEnvelope(spec, &exploration.ProportionalExplorationVisualization{Kind: "proportional", Mark: exploration.ExplorationVisualizationProportionalMarkDonut}, columns, base, frame, modelID, datasetID, result)
	}
	if len(dimensions) == 1 && len(metrics) >= 1 {
		return explorerCartesianEnvelope(spec, &exploration.CartesianExplorationVisualization{Kind: "cartesian", Mark: exploration.ExplorationVisualizationCartesianMarkBar}, columns, base, frame, modelID, datasetID, result)
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
				return explorerCartesianEnvelope(ordered, &exploration.CartesianExplorationVisualization{Kind: "cartesian", Mark: exploration.ExplorationVisualizationCartesianMarkLine}, columns, base, frame, modelID, datasetID, result)
			}
		}
		return explorerCartesianEnvelope(spec, &exploration.CartesianExplorationVisualization{Kind: "cartesian", Mark: exploration.ExplorationVisualizationCartesianMarkBar}, columns, base, frame, modelID, datasetID, result)
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
