package http

import (
	"fmt"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func explorerChartEnvelope(spec exploration.ExplorationSpec, base visualizationir.VisualizationSpecBase, frame visualizationruntime.Frame, result projectsignals.DataExploreResultSignal, columns []explorerVisualizationColumn) (visualizationir.VisualizationEnvelope, bool, string) {
	var authored *exploration.CartesianExplorationVisualization
	if spec.Visualization != nil {
		switch value := spec.Visualization.Value.(type) {
		case *exploration.CartesianExplorationVisualization:
			authored = value
		case *exploration.TableExplorationVisualization, *exploration.PivotExplorationVisualization:
			return visualizationir.VisualizationEnvelope{}, false, ""
		case nil:
			return visualizationir.VisualizationEnvelope{}, false, "chart visualization settings are unavailable"
		default:
			return visualizationir.VisualizationEnvelope{}, false, "this authored visualization type has no safe chart projection"
		}
	}

	dimensions := explorerVisualizationSelectedDimensions(spec, columns)
	metrics := explorerVisualizationSelectedMetrics(spec, columns)
	if len(dimensions) == 0 || len(dimensions) > 2 || len(metrics) != 1 || !explorerVisualizationNumeric(metrics[0]) {
		if authored != nil {
			return visualizationir.VisualizationEnvelope{}, false, "cartesian chart requires one numeric metric and one or two result dimensions"
		}
		return visualizationir.VisualizationEnvelope{}, false, ""
	}

	xColumn := dimensions[0]
	if len(dimensions) == 2 && dimensions[1].Temporal && !dimensions[0].Temporal {
		xColumn = dimensions[1]
	}
	if authored != nil && authored.X != nil {
		selected, ok := explorerVisualizationColumnFor(columns, authored.X.Field)
		if !ok || selected.Role == visualizationir.VisualizationFieldRoleMetric {
			return visualizationir.VisualizationEnvelope{}, false, "authored chart x field is unavailable"
		}
		xColumn = selected
	}

	metric := metrics[0]
	if authored != nil && authored.Y != nil {
		if len(*authored.Y) != 1 {
			return visualizationir.VisualizationEnvelope{}, false, "authored chart must select exactly one y metric in this view"
		}
		selected, ok := explorerVisualizationColumnFor(columns, (*authored.Y)[0].Field)
		if !ok || selected.Role != visualizationir.VisualizationFieldRoleMetric || !explorerVisualizationNumeric(selected) {
			return visualizationir.VisualizationEnvelope{}, false, "authored chart y field is unavailable or non-numeric"
		}
		metric = selected
	}

	var series *visualizationir.VisualizationFieldRef
	seriesColumn := explorerVisualizationColumn{}
	if authored != nil && authored.Series != nil {
		selected, ok := explorerVisualizationColumnFor(columns, authored.Series.Field)
		if !ok || selected.Role == visualizationir.VisualizationFieldRoleMetric || selected.Output == xColumn.Output {
			return visualizationir.VisualizationEnvelope{}, false, "authored chart series field is unavailable"
		}
		seriesColumn = selected
	} else if len(dimensions) == 2 {
		for _, dimension := range dimensions {
			if dimension.Output != xColumn.Output {
				seriesColumn = dimension
				break
			}
		}
	}
	if seriesColumn.Output != "" {
		if result.Truncated {
			return visualizationir.VisualizationEnvelope{}, false, "chart view omitted: grouped series is truncated; add filters to narrow the query"
		}
		seriesValues := make(map[string]struct{})
		for _, row := range result.Rows {
			value := row[seriesColumn.Output]
			seriesValues[fmt.Sprintf("%T:%v", value, value)] = struct{}{}
			if len(seriesValues) > 8 {
				return visualizationir.VisualizationEnvelope{}, false, "chart view omitted: more than eight series would be difficult to read; filter the series field"
			}
		}
		ref := visualizationir.VisualizationFieldRef{Dataset: dataExplorerVisualizationDB, Field: seriesColumn.Output}
		series = &ref
	}

	mark := visualizationir.VisualizationCartesianMarkBar
	if xColumn.Temporal {
		mark = visualizationir.VisualizationCartesianMarkLine
	}
	if authored != nil && authored.Mark != "" {
		mark = visualizationir.VisualizationCartesianMark(authored.Mark)
	}
	switch mark {
	case visualizationir.VisualizationCartesianMarkLine,
		visualizationir.VisualizationCartesianMarkArea,
		visualizationir.VisualizationCartesianMarkBar,
		visualizationir.VisualizationCartesianMarkColumn:
	default:
		return visualizationir.VisualizationEnvelope{}, false, "authored chart mark is not supported for a governed exploration result"
	}

	base.Kind = "cartesian"
	base.Interactions = []visualizationir.VisualizationInteraction{}
	presentation := visualizationir.CartesianVisualizationPresentation{
		VisualizationPresentation: explorerVisualizationPresentation(spec),
		ShowSymbols:               true,
	}
	if series != nil && authored == nil {
		presentation.Legend = visualizationir.VisualizationLegendPositionBottom
	}
	if authored != nil {
		presentation.Smooth = projectsignals.ValueOrZero(authored.Smooth)
		if authored.ShowSymbols != nil {
			presentation.ShowSymbols = *authored.ShowSymbols
		}
		if authored.Orientation != nil {
			orientation := visualizationir.VisualizationOrientation(*authored.Orientation)
			presentation.Orientation = &orientation
		}
		if authored.Stacking != nil {
			stacking := visualizationir.VisualizationStackingMode(*authored.Stacking)
			presentation.Stacking = &stacking
		}
	}
	x := visualizationir.VisualizationFieldRef{Dataset: dataExplorerVisualizationDB, Field: xColumn.Output}
	y := visualizationir.VisualizationFieldRef{Dataset: dataExplorerVisualizationDB, Field: metric.Output}
	visualSpec := visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{
		VisualizationSpecBase: base, Kind: "cartesian", Mark: mark, X: x, Y: []visualizationir.VisualizationFieldRef{y}, Series: series, Presentation: presentation,
	}}
	queryDimensions := []explorerVisualizationColumn{xColumn}
	if seriesColumn.Output != "" {
		queryDimensions = append(queryDimensions, seriesColumn)
	}
	shape := visualizationdefinition.ResultCategoryValue
	if series != nil {
		shape = visualizationdefinition.ResultCategorySeriesValue
	}
	query := visualizationdefinition.QueryBinding{
		Kind: visualizationdefinition.QueryAggregate, ResultShape: shape,
		ModelID: explorerVisualizationModelID(spec), DatasetID: dataExplorerVisualizationDB,
		Aggregate: &visualizationdefinition.AggregateQueryBinding{
			TableID: explorerVisualizationDatasetID(spec), Dimensions: explorerVisualizationBindings(queryDimensions),
			Metrics: explorerVisualizationBindings([]explorerVisualizationColumn{metric}), Limit: base.DataBudget.MaxRows,
		},
	}
	definition, err := explorerVisualizationDefinition(dataExplorerChartViewID, visualSpec, query)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, false, "chart visualization settings failed validation"
	}
	envelope, err := visualizationruntime.EnvelopeFromFrame(definition, frame, nil, explorerVisualizationDataRevision(result), explorerVisualizationGeneration(result))
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, false, "chart result frame failed validation"
	}
	return envelope, true, ""
}

func explorerVisualizationPresentation(spec exploration.ExplorationSpec) visualizationir.VisualizationPresentation {
	presentation := visualizationir.VisualizationPresentation{
		Legend: visualizationir.VisualizationLegendPositionHidden,
		LabelPolicy: visualizationir.VisualizationLabelPolicy{
			Density: visualizationir.VisualizationLabelDensityHidden, Priority: []visualizationir.VisualizationLabelPriority{},
			MaxCharacters: 24, MinimumSpacing: 0, TooltipFallback: true,
		},
	}
	if spec.Visualization != nil {
		if base, err := spec.Visualization.Base(); err == nil && base != nil {
			if base.Legend != nil {
				presentation.Legend = visualizationir.VisualizationLegendPosition(*base.Legend)
			}
			if base.DisplayUnits != nil {
				units := visualizationir.VisualizationDisplayUnits(*base.DisplayUnits)
				presentation.DisplayUnits = &units
			}
		}
	}
	return presentation
}

func explorerVisualizationNumeric(column explorerVisualizationColumn) bool {
	switch column.DataType {
	case visualizationir.VisualizationDataTypeInteger, visualizationir.VisualizationDataTypeDecimal, visualizationir.VisualizationDataTypeFloat:
		return true
	default:
		return false
	}
}

func explorerVisualizationBindings(columns []explorerVisualizationColumn) []visualizationdefinition.FieldBinding {
	bindings := make([]visualizationdefinition.FieldBinding, 0, len(columns))
	for _, column := range columns {
		bindings = append(bindings, visualizationdefinition.FieldBinding{FieldID: strings.TrimSpace(column.Semantic), Alias: column.Output, Grain: column.Grain})
	}
	return bindings
}
