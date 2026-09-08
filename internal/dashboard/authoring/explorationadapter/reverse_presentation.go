package explorationadapter

import (
	"fmt"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func (c reverseConverter) visualization(value document.DashboardVisual, outputs map[string]string, spec exploration.ExplorationSpec) (*exploration.ExplorationVisualizationConfig, error) {
	base := func(kind string) exploration.ExplorationVisualizationConfigBase {
		return exploration.ExplorationVisualizationConfigBase{Kind: kind, Title: cloneString(value.Title), Subtitle: cloneString(value.Subtitle)}
	}
	if spec.Pivot != nil {
		if value.Type != document.DashboardVisualTypePivot && value.Type != document.DashboardVisualTypeMatrix && value.Type != document.DashboardVisualTypeTable {
			return nil, fmt.Errorf("visual type %q cannot render a pivot query", value.Type)
		}
		if value.Type == document.DashboardVisualTypeTable {
			if err := c.requireTablePresentation(value.Presentation); err != nil {
				return nil, err
			}
			return &exploration.ExplorationVisualizationConfig{Value: &exploration.TableExplorationVisualization{ExplorationVisualizationConfigBase: base("table"), Kind: "table", Columns: refsForPivot(spec.Pivot, outputs)}}, nil
		}
		if err := c.requireTablePresentation(value.Presentation); err != nil {
			return nil, err
		}
		rows := refsForDimensions(spec.Pivot.Rows, outputs)
		columns := refsForDimensions(spec.Pivot.Columns, outputs)
		metrics := refsForMetrics(spec.Pivot.Metrics, outputs)
		if value.Type == document.DashboardVisualTypeMatrix {
			return &exploration.ExplorationVisualizationConfig{Value: &exploration.MatrixExplorationVisualization{ExplorationVisualizationConfigBase: base("matrix"), Kind: "matrix", Rows: rows, Columns: columns, Metrics: metrics}}, nil
		}
		return &exploration.ExplorationVisualizationConfig{Value: &exploration.PivotExplorationVisualization{ExplorationVisualizationConfigBase: base("pivot"), Kind: "pivot", Rows: rows, Columns: columns, Metrics: metrics}}, nil
	}

	switch value.Type {
	case document.DashboardVisualTypeTable:
		if err := c.requireTablePresentation(value.Presentation); err != nil {
			return nil, err
		}
		return &exploration.ExplorationVisualizationConfig{Value: &exploration.TableExplorationVisualization{ExplorationVisualizationConfigBase: base("table"), Kind: "table", Columns: refsForAggregate(spec, outputs)}}, nil
	case document.DashboardVisualTypeLine, document.DashboardVisualTypeArea, document.DashboardVisualTypeBar, document.DashboardVisualTypeColumn:
		return c.reverseCartesian(value, outputs, spec, base("cartesian"))
	case document.DashboardVisualTypeKpi:
		return c.reverseKPI(value, outputs, spec, base("kpi"))
	case document.DashboardVisualTypeScatter:
		return c.reversePoint(value, outputs, spec, base("point"))
	case document.DashboardVisualTypePie, document.DashboardVisualTypeDonut, document.DashboardVisualTypeFunnel:
		return c.reverseProportional(value, outputs, spec, base("proportional"))
	case document.DashboardVisualTypeRadar, document.DashboardVisualTypeGauge:
		return c.reversePolar(value, outputs, spec, base("polar"))
	default:
		return nil, fmt.Errorf("dashboard visual type %q cannot be represented by an exploration", value.Type)
	}
}

func refsForAggregate(spec exploration.ExplorationSpec, outputs map[string]string) []exploration.ExplorationVisualizationFieldRef {
	refs := make([]exploration.ExplorationVisualizationFieldRef, 0, len(spec.Dimensions)+len(spec.Metrics))
	refs = append(refs, refsForDimensions(spec.Dimensions, outputs)...)
	refs = append(refs, refsForMetrics(spec.Metrics, outputs)...)
	return refs
}

func refsForPivot(value *exploration.ExplorationPivotConfig, outputs map[string]string) []exploration.ExplorationVisualizationFieldRef {
	refs := make([]exploration.ExplorationVisualizationFieldRef, 0, len(value.Rows)+len(value.Columns)+len(value.Metrics))
	refs = append(refs, refsForDimensions(value.Rows, outputs)...)
	refs = append(refs, refsForDimensions(value.Columns, outputs)...)
	refs = append(refs, refsForMetrics(value.Metrics, outputs)...)
	return refs
}

func refsForDimensions(values []exploration.ExplorationDimensionRef, outputs map[string]string) []exploration.ExplorationVisualizationFieldRef {
	result := make([]exploration.ExplorationVisualizationFieldRef, 0, len(values))
	for _, value := range values {
		result = append(result, exploration.ExplorationVisualizationFieldRef{Field: fieldOutput(value.Field, value.Alias, outputs)})
	}
	return result
}

func refsForMetrics(values []exploration.ExplorationMetricRef, outputs map[string]string) []exploration.ExplorationVisualizationFieldRef {
	result := make([]exploration.ExplorationVisualizationFieldRef, 0, len(values))
	for _, value := range values {
		result = append(result, exploration.ExplorationVisualizationFieldRef{Field: fieldOutput(value.Field, value.Alias, outputs)})
	}
	return result
}

func fieldOutput(field string, alias *string, outputs map[string]string) string {
	if alias != nil && strings.TrimSpace(*alias) != "" {
		return *alias
	}
	for output, canonical := range outputs {
		if canonical == field {
			return output
		}
	}
	return field
}

func (c reverseConverter) fieldRef(value string, outputs map[string]string) (exploration.ExplorationVisualizationFieldRef, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return exploration.ExplorationVisualizationFieldRef{}, fmt.Errorf("visualization field is required")
	}
	if field, ok := outputs[value]; ok {
		return exploration.ExplorationVisualizationFieldRef{Field: field}, nil
	}
	for output, field := range outputs {
		if output == value {
			return exploration.ExplorationVisualizationFieldRef{Field: field}, nil
		}
	}
	return exploration.ExplorationVisualizationFieldRef{}, fmt.Errorf("visualization field %q is not selected", value)
}

func (c reverseConverter) reverseCartesian(value document.DashboardVisual, outputs map[string]string, spec exploration.ExplorationSpec, base exploration.ExplorationVisualizationConfigBase) (*exploration.ExplorationVisualizationConfig, error) {
	presentation, ok := value.Presentation.Value.(*document.CartesianDashboardPresentation)
	if !ok || presentation == nil {
		return nil, fmt.Errorf("%s visual requires cartesian presentation", value.Type)
	}
	if err := rejectCartesianExtras(presentation); err != nil {
		return nil, err
	}
	dimensions, metrics := spec.Dimensions, spec.Metrics
	if len(dimensions) > 2 {
		return nil, fmt.Errorf("cartesian dashboard query has more than two dimensions; series mapping is ambiguous")
	}
	var x exploration.ExplorationVisualizationFieldRef
	if len(dimensions) > 0 {
		x = exploration.ExplorationVisualizationFieldRef{Field: fieldOutput(dimensions[0].Field, dimensions[0].Alias, outputs)}
	} else if len(metrics) > 0 {
		x = exploration.ExplorationVisualizationFieldRef{Field: fieldOutput(metrics[0].Field, metrics[0].Alias, outputs)}
	} else {
		return nil, fmt.Errorf("cartesian query requires a dimension or metric")
	}
	y := refsForMetrics(metrics, outputs)
	mark := exploration.ExplorationVisualizationCartesianMark(value.Type)
	result := &exploration.CartesianExplorationVisualization{ExplorationVisualizationConfigBase: base, Kind: "cartesian", Mark: mark, X: &x, Y: &y, Smooth: cloneBool(presentation.Smooth), ShowSymbols: cloneBool(presentation.ShowSymbols)}
	if len(dimensions) == 2 {
		series := exploration.ExplorationVisualizationFieldRef{Field: fieldOutput(dimensions[1].Field, dimensions[1].Alias, outputs)}
		result.Series = &series
	}
	applyBasePresentation(&result.ExplorationVisualizationConfigBase, presentation.Legend, presentation.DisplayUnits, presentation.Orientation, presentation.Stacking)
	return &exploration.ExplorationVisualizationConfig{Value: result}, nil
}

func rejectCartesianExtras(value *document.CartesianDashboardPresentation) error {
	if value.DashboardPresentationBase.ConditionalFormatting != nil && len(*value.DashboardPresentationBase.ConditionalFormatting) != 0 {
		return fmt.Errorf("cartesian conditional formatting is not representable by exploration")
	}
	if value.Labels != nil || value.Step != nil || value.DataZoom != nil || value.SymbolSize != nil || value.LabelPosition != nil || value.Series != nil || value.Axes != nil || value.ReferenceLines != nil || value.ReferenceBands != nil || value.EventAnnotations != nil {
		return fmt.Errorf("dashboard cartesian renderer options are not representable by exploration")
	}
	return nil
}

func applyBasePresentation(base *exploration.ExplorationVisualizationConfigBase, legend *document.DashboardLegendPosition, units *visualizationir.VisualizationDisplayUnits, orientation *document.DashboardOrientation, stacking *document.DashboardStackingMode) {
	if legend != nil {
		converted := exploration.ExplorationVisualizationLegendPosition(*legend)
		if *legend == document.DashboardLegendPositionNone {
			converted = exploration.ExplorationVisualizationLegendPositionHidden
		}
		base.Legend = &converted
	}
	if units != nil {
		converted := exploration.ExplorationVisualizationDisplayUnits(*units)
		base.DisplayUnits = &converted
	}
	if orientation != nil {
		converted := exploration.ExplorationVisualizationOrientation(*orientation)
		base.Orientation = &converted
	}
	if stacking != nil {
		converted := exploration.ExplorationVisualizationStackingMode(*stacking)
		base.Stacking = &converted
	}
}

func (c reverseConverter) requireTablePresentation(value document.DashboardPresentation) error {
	presentation, ok := value.Value.(*document.TableDashboardPresentation)
	if !ok || presentation == nil {
		return fmt.Errorf("table visual requires table presentation")
	}
	if presentation.DashboardPresentationBase.ConditionalFormatting != nil && len(*presentation.DashboardPresentationBase.ConditionalFormatting) != 0 {
		return fmt.Errorf("table conditional formatting is not representable by exploration")
	}
	return nil
}

func (c reverseConverter) reverseKPI(value document.DashboardVisual, outputs map[string]string, spec exploration.ExplorationSpec, base exploration.ExplorationVisualizationConfigBase) (*exploration.ExplorationVisualizationConfig, error) {
	presentation, ok := value.Presentation.Value.(*document.KPIDashboardPresentation)
	if !ok || presentation == nil {
		return nil, fmt.Errorf("KPI visual requires KPI presentation")
	}
	if presentation.DashboardPresentationBase.ConditionalFormatting != nil && len(*presentation.DashboardPresentationBase.ConditionalFormatting) != 0 {
		return nil, fmt.Errorf("KPI conditional formatting is not representable by exploration")
	}
	if len(spec.Metrics) == 0 {
		return nil, fmt.Errorf("KPI requires a selected metric")
	}
	result := &exploration.KPIExplorationVisualization{ExplorationVisualizationConfigBase: base, Kind: "kpi", Value: exploration.ExplorationVisualizationFieldRef{Field: fieldOutput(spec.Metrics[0].Field, spec.Metrics[0].Alias, outputs)}}
	if presentation.Comparison != nil {
		ref, err := c.kpiValueRef(*presentation.Comparison, outputs)
		if err != nil {
			return nil, err
		}
		result.Comparison = &ref
	}
	if presentation.Goal != nil {
		ref, err := c.kpiValueRef(*presentation.Goal, outputs)
		if err != nil {
			return nil, err
		}
		result.Goal = &ref
	}
	if presentation.Trend != nil {
		category, err := c.fieldRef(presentation.Trend.Category, outputs)
		if err != nil {
			return nil, err
		}
		trendValue, err := c.fieldRef(presentation.Trend.Value, outputs)
		if err != nil {
			return nil, err
		}
		if presentation.Trend.Dataset != "" && presentation.Trend.Dataset != "primary" {
			return nil, fmt.Errorf("KPI trend dataset %q is not representable", presentation.Trend.Dataset)
		}
		result.Trend = &exploration.ExplorationKPITrend{Category: category, Value: trendValue}
	}
	p := &exploration.ExplorationKPIPresentation{Mode: cloneMapped(presentation.Mode, func(v visualizationir.VisualizationKPIMode) exploration.ExplorationVisualizationKPIMode {
		return exploration.ExplorationVisualizationKPIMode(v)
	}), Delta: cloneMapped(presentation.Delta, func(v visualizationir.VisualizationKPIDeltaMode) exploration.ExplorationVisualizationKPIDeltaMode {
		return exploration.ExplorationVisualizationKPIDeltaMode(v)
	}), FavorableDirection: cloneMapped(presentation.FavorableDirection, func(v visualizationir.VisualizationKPIDirection) exploration.ExplorationVisualizationKPIDirection {
		return exploration.ExplorationVisualizationKPIDirection(v)
	}), MissingComparison: cloneMapped(presentation.MissingComparison, func(v visualizationir.VisualizationKPIMissingComparison) exploration.ExplorationVisualizationKPIMissingComparison {
		return exploration.ExplorationVisualizationKPIMissingComparison(v)
	}), DisplayUnits: cloneMapped(presentation.DisplayUnits, func(v visualizationir.VisualizationDisplayUnits) exploration.ExplorationVisualizationDisplayUnits {
		return exploration.ExplorationVisualizationDisplayUnits(v)
	}), Note: cloneString(presentation.Note), Tone: cloneMapped(presentation.Tone, func(v visualizationir.VisualizationTone) exploration.ExplorationVisualizationTone {
		return exploration.ExplorationVisualizationTone(v)
	})}
	if presentation.Ranges != nil {
		converted := make([]exploration.ExplorationVisualizationKPIQualitativeRange, len(*presentation.Ranges))
		for i, item := range *presentation.Ranges {
			converted[i] = exploration.ExplorationVisualizationKPIQualitativeRange{Minimum: cloneFloat64(item.Minimum), Maximum: cloneFloat64(item.Maximum), Label: item.Label, Tone: exploration.ExplorationVisualizationTone(item.Tone)}
		}
		p.Ranges = &converted
	}
	if presentation.Thresholds != nil {
		converted := make([]exploration.ExplorationVisualizationThreshold, len(*presentation.Thresholds))
		for i, item := range *presentation.Thresholds {
			converted[i] = exploration.ExplorationVisualizationThreshold{Value: item.Value, Tone: exploration.ExplorationVisualizationTone(item.Tone)}
		}
		p.Thresholds = &converted
	}
	if p.Mode != nil || p.Delta != nil || p.FavorableDirection != nil || p.MissingComparison != nil || p.DisplayUnits != nil || p.Note != nil || p.Tone != nil || p.Ranges != nil || p.Thresholds != nil {
		result.Presentation = p
	}
	return &exploration.ExplorationVisualizationConfig{Value: result}, nil
}

func (c reverseConverter) kpiValueRef(value document.DashboardKPIValueBinding, outputs map[string]string) (exploration.ExplorationVisualizationFieldRef, error) {
	if value.Dataset != "" && value.Dataset != "primary" {
		return exploration.ExplorationVisualizationFieldRef{}, fmt.Errorf("KPI binding dataset %q is not representable", value.Dataset)
	}
	if value.Reducer != nil {
		return exploration.ExplorationVisualizationFieldRef{}, fmt.Errorf("KPI binding reducer is not representable by exploration")
	}
	ref, err := c.fieldRef(value.Field, outputs)
	if err != nil {
		return ref, err
	}
	if value.Label != "" && value.Label != value.Field {
		return exploration.ExplorationVisualizationFieldRef{}, fmt.Errorf("KPI binding label is not representable")
	}
	return ref, nil
}

func (c reverseConverter) reversePoint(value document.DashboardVisual, outputs map[string]string, spec exploration.ExplorationSpec, base exploration.ExplorationVisualizationConfigBase) (*exploration.ExplorationVisualizationConfig, error) {
	presentation, ok := value.Presentation.Value.(*document.PointDashboardPresentation)
	if !ok || presentation == nil {
		return nil, fmt.Errorf("scatter visual requires point presentation")
	}
	if presentation.DashboardPresentationBase.ConditionalFormatting != nil && len(*presentation.DashboardPresentationBase.ConditionalFormatting) != 0 || presentation.Labels != nil || presentation.Label != nil || presentation.Tooltip != nil || presentation.ColorScale != nil || presentation.SizeScale != nil || presentation.Overplot != nil || presentation.Brush != nil || presentation.Axes != nil || presentation.ReferenceLines != nil || presentation.ReferenceBands != nil || presentation.EventAnnotations != nil {
		return nil, fmt.Errorf("dashboard point renderer options are not representable by exploration")
	}
	x, err := c.fieldRef(presentation.X, outputs)
	if err != nil {
		return nil, err
	}
	y, err := c.fieldRef(presentation.Y, outputs)
	if err != nil {
		return nil, err
	}
	result := &exploration.PointExplorationVisualization{ExplorationVisualizationConfigBase: base, Kind: "point", Mark: "point", X: x, Y: y}
	if presentation.Size != nil {
		ref, e := c.fieldRef(*presentation.Size, outputs)
		if e != nil {
			return nil, e
		}
		result.Size = &ref
	}
	if presentation.Color != nil {
		ref, e := c.fieldRef(*presentation.Color, outputs)
		if e != nil {
			return nil, e
		}
		result.Color = &ref
	}
	if presentation.Series != nil {
		return nil, fmt.Errorf("dashboard point series is not representable by exploration")
	}
	if len(presentation.Identity) > 0 {
		refs := make([]exploration.ExplorationVisualizationFieldRef, 0, len(presentation.Identity))
		for _, field := range presentation.Identity {
			ref, e := c.fieldRef(field, outputs)
			if e != nil {
				return nil, e
			}
			refs = append(refs, ref)
		}
		result.Identity = &refs
	}
	applyBasePresentation(&result.ExplorationVisualizationConfigBase, presentation.Legend, nil, nil, nil)
	return &exploration.ExplorationVisualizationConfig{Value: result}, nil
}

func (c reverseConverter) reverseProportional(value document.DashboardVisual, outputs map[string]string, spec exploration.ExplorationSpec, base exploration.ExplorationVisualizationConfigBase) (*exploration.ExplorationVisualizationConfig, error) {
	presentation, ok := value.Presentation.Value.(*document.ProportionalDashboardPresentation)
	if !ok || presentation == nil {
		return nil, fmt.Errorf("%s visual requires proportional presentation", value.Type)
	}
	if presentation.DashboardPresentationBase.ConditionalFormatting != nil && len(*presentation.DashboardPresentationBase.ConditionalFormatting) != 0 || presentation.Labels != nil || presentation.Rose != nil || presentation.CenterLabel != nil || presentation.LabelPosition != nil || presentation.InnerRadius != nil || presentation.OuterRadius != nil || presentation.Align != nil || presentation.Sort != nil {
		return nil, fmt.Errorf("dashboard proportional renderer options are not representable by exploration")
	}
	if len(spec.Dimensions) == 0 || len(spec.Metrics) == 0 {
		return nil, fmt.Errorf("proportional visual requires a dimension and metric")
	}
	category := exploration.ExplorationVisualizationFieldRef{Field: fieldOutput(spec.Dimensions[0].Field, spec.Dimensions[0].Alias, outputs)}
	amount := exploration.ExplorationVisualizationFieldRef{Field: fieldOutput(spec.Metrics[0].Field, spec.Metrics[0].Alias, outputs)}
	result := &exploration.ProportionalExplorationVisualization{ExplorationVisualizationConfigBase: base, Kind: "proportional", Mark: exploration.ExplorationVisualizationProportionalMark(value.Type), Category: category, Value: amount}
	applyBasePresentation(&result.ExplorationVisualizationConfigBase, presentation.Legend, presentation.DisplayUnits, presentation.Orientation, nil)
	return &exploration.ExplorationVisualizationConfig{Value: result}, nil
}

func (c reverseConverter) reversePolar(value document.DashboardVisual, outputs map[string]string, spec exploration.ExplorationSpec, base exploration.ExplorationVisualizationConfigBase) (*exploration.ExplorationVisualizationConfig, error) {
	presentation, ok := value.Presentation.Value.(*document.PolarDashboardPresentation)
	if !ok || presentation == nil {
		return nil, fmt.Errorf("%s visual requires polar presentation", value.Type)
	}
	if presentation.DashboardPresentationBase.ConditionalFormatting != nil && len(*presentation.DashboardPresentationBase.ConditionalFormatting) != 0 || presentation.Labels != nil || presentation.Minimum != nil || presentation.Maximum != nil || presentation.Target != nil || presentation.ShowPointer != nil || presentation.Area != nil || presentation.ProgressWidth != nil || presentation.Thresholds != nil {
		return nil, fmt.Errorf("dashboard polar renderer options are not representable by exploration")
	}
	if len(spec.Metrics) == 0 {
		return nil, fmt.Errorf("polar visual requires a metric")
	}
	result := &exploration.PolarExplorationVisualization{ExplorationVisualizationConfigBase: base, Kind: "polar", Mark: exploration.ExplorationVisualizationPolarMark(value.Type), Value: exploration.ExplorationVisualizationFieldRef{Field: fieldOutput(spec.Metrics[0].Field, spec.Metrics[0].Alias, outputs)}}
	if len(spec.Dimensions) > 0 {
		ref := exploration.ExplorationVisualizationFieldRef{Field: fieldOutput(spec.Dimensions[0].Field, spec.Dimensions[0].Alias, outputs)}
		result.Category = &ref
	}
	applyBasePresentation(&result.ExplorationVisualizationConfigBase, presentation.Legend, presentation.DisplayUnits, nil, nil)
	return &exploration.ExplorationVisualizationConfig{Value: result}, nil
}
