package explorationadapter

import (
	"fmt"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func (c converter) presentation() (string, document.DashboardPresentation, error) {
	if c.spec.Pivot != nil {
		if len(c.spec.Dimensions) != 0 || len(c.spec.Metrics) != 0 {
			return "", document.DashboardPresentation{}, fmt.Errorf("pivot exploration cannot carry top-level dimensions or metrics")
		}
		if err := c.validatePivotVisualization(); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		if err := c.validateBase("pivot", false, false, false, false); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		kind := "pivot"
		if c.spec.Visualization != nil {
			requested, err := c.spec.Visualization.Kind()
			if err != nil {
				return "", document.DashboardPresentation{}, err
			}
			if requested != "pivot" && requested != "matrix" {
				return "", document.DashboardPresentation{}, fmt.Errorf("visualization %q cannot render a pivot query", requested)
			}
			kind = requested
		}
		return kind, document.DashboardPresentation{Value: c.tablePresentation()}, nil
	}
	if c.spec.Visualization == nil || c.spec.Visualization.Value == nil {
		return "table", document.DashboardPresentation{Value: c.tablePresentation()}, nil
	}
	switch value := c.spec.Visualization.Value.(type) {
	case *exploration.CartesianExplorationVisualization:
		if value == nil {
			return "", document.DashboardPresentation{}, fmt.Errorf("cartesian visualization is nil")
		}
		kind := string(value.Mark)
		if kind == "" {
			return "", document.DashboardPresentation{}, fmt.Errorf("cartesian mark is required")
		}
		if !validCartesianMark(value.Mark) {
			return "", document.DashboardPresentation{}, fmt.Errorf("unsupported cartesian mark %q", value.Mark)
		}
		if err := c.validateChartRefs(value.X, derefRefs(value.Y), value.Series); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		if err := c.validateCartesianChannels(value); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		presentation := &document.CartesianDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "cartesian"}, Type: "cartesian", Smooth: cloneBool(value.Smooth), ShowSymbols: cloneBool(value.ShowSymbols), Orientation: dashboardOrientation(value.Orientation), Stacking: dashboardStacking(value.Stacking)}
		if base, err := c.visualizationBase(); err != nil {
			return "", document.DashboardPresentation{}, err
		} else if base != nil {
			presentation.Legend = dashboardLegend(base.Legend)
			presentation.DisplayUnits = dashboardDisplayUnits(base.DisplayUnits)
		}
		return kind, document.DashboardPresentation{Value: presentation}, nil
	case *exploration.TableExplorationVisualization:
		if value == nil {
			return "", document.DashboardPresentation{}, fmt.Errorf("table visualization is nil")
		}
		if err := c.validateBase("table", false, false, false, false); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		if err := c.validateChartRefs(nil, value.Columns, nil); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		if err := c.validateTableColumns(value.Columns); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		return "table", document.DashboardPresentation{Value: c.tablePresentation()}, nil
	case *exploration.KPIExplorationVisualization:
		if value == nil {
			return "", document.DashboardPresentation{}, fmt.Errorf("KPI visualization is nil")
		}
		if err := c.validateBase("KPI", false, true, false, false); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		if err := c.validateChartRefs(&value.Value, nil, nil); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		if err := c.validateKPIChannels(value); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		if err := validateKPIPresentation(value.Presentation); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		p := &document.KPIDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "kpi"}, Type: "kpi"}
		if value.Presentation != nil {
			p.Mode = cloneMapped(value.Presentation.Mode, func(value exploration.ExplorationVisualizationKPIMode) visualizationir.VisualizationKPIMode {
				return visualizationir.VisualizationKPIMode(value)
			})
			p.Delta = cloneMapped(value.Presentation.Delta, func(value exploration.ExplorationVisualizationKPIDeltaMode) visualizationir.VisualizationKPIDeltaMode {
				return visualizationir.VisualizationKPIDeltaMode(value)
			})
			p.FavorableDirection = cloneMapped(value.Presentation.FavorableDirection, func(value exploration.ExplorationVisualizationKPIDirection) visualizationir.VisualizationKPIDirection {
				return visualizationir.VisualizationKPIDirection(value)
			})
			p.MissingComparison = cloneMapped(value.Presentation.MissingComparison, func(value exploration.ExplorationVisualizationKPIMissingComparison) visualizationir.VisualizationKPIMissingComparison {
				return visualizationir.VisualizationKPIMissingComparison(value)
			})
			p.Ranges = cloneKPIRanges(value.Presentation.Ranges)
			p.Thresholds = cloneThresholds(value.Presentation.Thresholds)
			p.DisplayUnits = cloneMapped(value.Presentation.DisplayUnits, func(value exploration.ExplorationVisualizationDisplayUnits) visualizationir.VisualizationDisplayUnits {
				return visualizationir.VisualizationDisplayUnits(value)
			})
			p.Note = cloneString(value.Presentation.Note)
			p.Tone = cloneMapped(value.Presentation.Tone, func(value exploration.ExplorationVisualizationTone) visualizationir.VisualizationTone {
				return visualizationir.VisualizationTone(value)
			})
		}
		if base, err := c.visualizationBase(); err != nil {
			return "", document.DashboardPresentation{}, err
		} else if base != nil && base.DisplayUnits != nil && p.DisplayUnits == nil {
			p.DisplayUnits = dashboardDisplayUnits(base.DisplayUnits)
		}
		if value.Comparison != nil {
			binding, err := c.kpiValueBinding(*value.Comparison)
			if err != nil {
				return "", document.DashboardPresentation{}, err
			}
			p.Comparison = binding
		}
		if value.Goal != nil {
			binding, err := c.kpiValueBinding(*value.Goal)
			if err != nil {
				return "", document.DashboardPresentation{}, err
			}
			p.Goal = binding
		}
		if value.Trend != nil {
			category, err := c.visualFieldOutput(value.Trend.Category)
			if err != nil {
				return "", document.DashboardPresentation{}, err
			}
			trendValue, err := c.visualFieldOutput(value.Trend.Value)
			if err != nil {
				return "", document.DashboardPresentation{}, err
			}
			p.Trend = &document.DashboardKPITrendBinding{Dataset: "primary", Category: category, Value: trendValue}
		}
		return "kpi", document.DashboardPresentation{Value: p}, nil
	case *exploration.MatrixExplorationVisualization:
		if value == nil {
			return "", document.DashboardPresentation{}, fmt.Errorf("matrix visualization is nil")
		}
		if err := c.validateBase("matrix", false, false, false, false); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		refs := append([]exploration.ExplorationVisualizationFieldRef{}, value.Rows...)
		refs = append(refs, value.Columns...)
		refs = append(refs, value.Metrics...)
		if err := c.validateChartRefs(nil, refs, nil); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		if err := c.validatePivotVisualizationRefs(value.Rows, value.Columns, value.Metrics); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		return "matrix", document.DashboardPresentation{Value: c.tablePresentation()}, nil
	case *exploration.PivotExplorationVisualization:
		if value == nil {
			return "", document.DashboardPresentation{}, fmt.Errorf("pivot visualization is nil")
		}
		if err := c.validateBase("pivot", false, false, false, false); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		refs := append([]exploration.ExplorationVisualizationFieldRef{}, value.Rows...)
		refs = append(refs, value.Columns...)
		refs = append(refs, value.Metrics...)
		if err := c.validateChartRefs(nil, refs, nil); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		if err := c.validatePivotVisualizationRefs(value.Rows, value.Columns, value.Metrics); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		return "pivot", document.DashboardPresentation{Value: c.tablePresentation()}, nil
	case *exploration.ProportionalExplorationVisualization:
		if value == nil {
			return "", document.DashboardPresentation{}, fmt.Errorf("proportional visualization is nil")
		}
		if !validProportionalMark(value.Mark) {
			return "", document.DashboardPresentation{}, fmt.Errorf("unsupported proportional mark %q", value.Mark)
		}
		if err := c.validateBase("proportional", true, true, true, false); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		if value.Series != nil {
			return "", document.DashboardPresentation{}, fmt.Errorf("proportional series is not representable by a dashboard presentation")
		}
		if err := c.validateChartRefs(&value.Category, []exploration.ExplorationVisualizationFieldRef{value.Value}, value.Series); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		if err := c.validateProportionalChannels(value); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		p := &document.ProportionalDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "proportional"}, Type: "proportional"}
		if base, err := c.visualizationBase(); err != nil {
			return "", document.DashboardPresentation{}, err
		} else if base != nil {
			p.Legend = dashboardLegend(base.Legend)
			p.DisplayUnits = dashboardDisplayUnits(base.DisplayUnits)
			p.Orientation = dashboardOrientation(base.Orientation)
		}
		return string(value.Mark), document.DashboardPresentation{Value: p}, nil
	case *exploration.PolarExplorationVisualization:
		if value == nil {
			return "", document.DashboardPresentation{}, fmt.Errorf("polar visualization is nil")
		}
		if !validPolarMark(value.Mark) {
			return "", document.DashboardPresentation{}, fmt.Errorf("unsupported polar mark %q", value.Mark)
		}
		if err := c.validateBase("polar", true, true, false, false); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		if value.Series != nil {
			return "", document.DashboardPresentation{}, fmt.Errorf("polar series selection is not representable by a dashboard presentation")
		}
		if err := c.validateChartRefs(value.Category, []exploration.ExplorationVisualizationFieldRef{value.Value}, nil); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		if err := c.validatePolarChannels(value); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		p := &document.PolarDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "polar"}, Type: "polar"}
		if base, err := c.visualizationBase(); err != nil {
			return "", document.DashboardPresentation{}, err
		} else if base != nil {
			p.Legend = dashboardLegend(base.Legend)
			p.DisplayUnits = dashboardDisplayUnits(base.DisplayUnits)
		}
		return string(value.Mark), document.DashboardPresentation{Value: p}, nil
	case *exploration.PointExplorationVisualization:
		if value == nil {
			return "", document.DashboardPresentation{}, fmt.Errorf("point visualization is nil")
		}
		if value.Mark != "point" {
			return "", document.DashboardPresentation{}, fmt.Errorf("unsupported point mark %q", value.Mark)
		}
		if err := c.validateBase("point", true, false, false, false); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		refs := []exploration.ExplorationVisualizationFieldRef{value.X, value.Y}
		if value.Size != nil {
			refs = append(refs, *value.Size)
		}
		if value.Color != nil {
			refs = append(refs, *value.Color)
		}
		if value.Identity != nil {
			refs = append(refs, (*value.Identity)...)
		}
		if err := c.validateChartRefs(nil, refs, nil); err != nil {
			return "", document.DashboardPresentation{}, err
		}
		if value.Identity == nil || len(*value.Identity) == 0 {
			return "", document.DashboardPresentation{}, fmt.Errorf("point identity is required by the dashboard visual contract")
		}
		x, err := c.visualFieldOutput(value.X)
		if err != nil {
			return "", document.DashboardPresentation{}, err
		}
		y, err := c.visualFieldOutput(value.Y)
		if err != nil {
			return "", document.DashboardPresentation{}, err
		}
		identity := make([]string, len(*value.Identity))
		for index, ref := range *value.Identity {
			identity[index], err = c.visualFieldOutput(ref)
			if err != nil {
				return "", document.DashboardPresentation{}, err
			}
		}
		p := &document.PointDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "point"}, Type: "point", X: x, Y: y, Identity: identity}
		if value.Size != nil {
			mapped, mapErr := c.visualFieldOutput(*value.Size)
			err = mapErr
			if err != nil {
				return "", document.DashboardPresentation{}, err
			}
			p.Size = cloneString(&mapped)
		}
		if value.Color != nil {
			mapped, mapErr := c.visualFieldOutput(*value.Color)
			err = mapErr
			if err != nil {
				return "", document.DashboardPresentation{}, err
			}
			p.Color = cloneString(&mapped)
		}
		if base, baseErr := c.visualizationBase(); baseErr != nil {
			return "", document.DashboardPresentation{}, baseErr
		} else if base != nil {
			p.Legend = dashboardLegend(base.Legend)
		}
		return "scatter", document.DashboardPresentation{Value: p}, nil
	case *exploration.HierarchyExplorationVisualization:
		return "", document.DashboardPresentation{}, fmt.Errorf("hierarchy visualization conversion is unsupported by the dashboard document contract")
	case *exploration.GeographicExplorationVisualization:
		return "", document.DashboardPresentation{}, fmt.Errorf("geographic visualization conversion is unsupported by the dashboard document contract")
	default:
		return "", document.DashboardPresentation{}, fmt.Errorf("unsupported exploration visualization %T", value)
	}
}

func (c converter) validateChartRefs(single *exploration.ExplorationVisualizationFieldRef, many []exploration.ExplorationVisualizationFieldRef, series *exploration.ExplorationVisualizationFieldRef) error {
	refs := append([]exploration.ExplorationVisualizationFieldRef(nil), many...)
	if single != nil {
		refs = append(refs, *single)
	}
	if series != nil {
		refs = append(refs, *series)
	}
	for _, ref := range refs {
		if _, err := c.visualFieldOutput(ref); err != nil {
			return err
		}
		if ref.Format != nil {
			return fmt.Errorf("visualization field %q format cannot be represented by a dashboard query", ref.Field)
		}
	}
	return nil
}

// validateBase rejects visualization options for which the dashboard
// presentation has no equivalent. A copied visual must not look valid while
// silently losing one of these options.
func (c converter) validateBase(kind string, allowLegend, allowDisplayUnits, allowOrientation, allowStacking bool) error {
	base, err := c.visualizationBase()
	if err != nil {
		return err
	}
	if base == nil {
		return nil
	}
	if base.Legend != nil {
		if !validExplorationLegend(*base.Legend) {
			return fmt.Errorf("%s has unsupported legend position %q", kind, *base.Legend)
		}
		if !allowLegend {
			return fmt.Errorf("%s legend is not representable by the dashboard presentation", kind)
		}
	}
	if base.DisplayUnits != nil {
		if !validDisplayUnits(*base.DisplayUnits) {
			return fmt.Errorf("%s has unsupported display units %q", kind, *base.DisplayUnits)
		}
		if !allowDisplayUnits {
			return fmt.Errorf("%s display units are not representable by the dashboard presentation", kind)
		}
	}
	if base.Orientation != nil {
		if *base.Orientation != exploration.ExplorationVisualizationOrientationHorizontal && *base.Orientation != exploration.ExplorationVisualizationOrientationVertical {
			return fmt.Errorf("%s has unsupported orientation %q", kind, *base.Orientation)
		}
		if !allowOrientation {
			return fmt.Errorf("%s orientation is not representable by the dashboard presentation", kind)
		}
	}
	if base.Stacking != nil {
		if !validStacking(*base.Stacking) {
			return fmt.Errorf("%s has unsupported stacking mode %q", kind, *base.Stacking)
		}
		if !allowStacking {
			return fmt.Errorf("%s stacking is not representable by the dashboard presentation", kind)
		}
	}
	return nil
}

func (c converter) visualizationBase() (*exploration.ExplorationVisualizationConfigBase, error) {
	if c.spec.Visualization == nil || c.spec.Visualization.Value == nil {
		return nil, nil
	}
	base, err := c.spec.Visualization.Base()
	if err != nil {
		return nil, err
	}
	return base, nil
}

func (c converter) validateCartesianChannels(value *exploration.CartesianExplorationVisualization) error {
	dimensions, metrics, err := c.querySelections()
	if err != nil {
		return err
	}
	if value.X != nil {
		output, err := c.visualFieldOutput(*value.X)
		if err != nil {
			return err
		}
		if len(dimensions) > 0 {
			if output != dimensionOutput(dimensions[0]) {
				return fmt.Errorf("cartesian x selection %q cannot be represented; dashboard charts use the first dimension %q", output, dimensionOutput(dimensions[0]))
			}
		} else if len(metrics) == 0 || output != metricOutput(metrics[0]) {
			want := "the first metric"
			if len(metrics) > 0 {
				want = fmt.Sprintf("the first metric %q", metricOutput(metrics[0]))
			}
			return fmt.Errorf("cartesian x selection %q cannot be represented; dashboard charts use %s", output, want)
		}
	}
	if value.Y != nil {
		if len(*value.Y) != len(metrics) {
			return fmt.Errorf("cartesian y selection has %d fields; dashboard charts use all %d selected metrics", len(*value.Y), len(metrics))
		}
		for index, ref := range *value.Y {
			output, err := c.visualFieldOutput(ref)
			if err != nil {
				return err
			}
			if output != metricOutput(metrics[index]) {
				return fmt.Errorf("cartesian y selection %q cannot be represented at index %d; dashboard chart metric is %q", output, index, metricOutput(metrics[index]))
			}
		}
	}
	if value.Series != nil {
		kind := string(value.Mark)
		switch kind {
		case "line", "area", "bar", "column":
		default:
			return fmt.Errorf("cartesian series selection is not representable for %s visuals", kind)
		}
		if len(dimensions) != 2 {
			return fmt.Errorf("cartesian series selection requires exactly two selected dimensions")
		}
		output, err := c.visualFieldOutput(*value.Series)
		if err != nil {
			return err
		}
		if output != dimensionOutput(dimensions[1]) {
			return fmt.Errorf("cartesian series selection %q cannot be represented; dashboard charts use the second dimension %q", output, dimensionOutput(dimensions[1]))
		}
	}
	return nil
}

func (c converter) validateTableColumns(columns []exploration.ExplorationVisualizationFieldRef) error {
	if len(columns) == 0 {
		return nil
	}
	dimensions, metrics, err := c.querySelections()
	if err != nil {
		return err
	}
	want := make([]string, 0, len(dimensions)+len(metrics))
	for _, value := range dimensions {
		want = append(want, dimensionOutput(value))
	}
	for _, value := range metrics {
		want = append(want, metricOutput(value))
	}
	if len(columns) != len(want) {
		return fmt.Errorf("table columns have %d fields; dashboard table uses all %d query fields", len(columns), len(want))
	}
	for index, ref := range columns {
		if ref.Format != nil {
			return fmt.Errorf("table column %q format cannot be represented by a dashboard presentation", ref.Field)
		}
		output, err := c.visualFieldOutput(ref)
		if err != nil {
			return err
		}
		if output != want[index] {
			return fmt.Errorf("table column %q cannot be represented at index %d; dashboard table column is %q", output, index, want[index])
		}
	}
	return nil
}

func (c converter) validateTableConfigColumns(columns []exploration.ExplorationTableColumn) error {
	refs := make([]exploration.ExplorationVisualizationFieldRef, len(columns))
	for index, column := range columns {
		if column.Label != nil || column.Width != nil {
			return fmt.Errorf("table column %q label/width is not representable by a dashboard presentation", column.Field)
		}
		refs[index] = exploration.ExplorationVisualizationFieldRef{Field: column.Field, Format: column.Format}
	}
	return c.validateTableColumns(refs)
}

func (c converter) validateKPIChannels(value *exploration.KPIExplorationVisualization) error {
	_, metrics, err := c.querySelections()
	if err != nil {
		return err
	}
	if len(metrics) == 0 {
		return fmt.Errorf("KPI requires a selected metric")
	}
	want, err := c.visualFieldOutput(value.Value)
	if err != nil {
		return err
	}
	if want != metricOutput(metrics[0]) {
		return fmt.Errorf("KPI value %q cannot be represented; dashboard KPI uses metric %q", want, metricOutput(metrics[0]))
	}
	for _, ref := range []*exploration.ExplorationVisualizationFieldRef{value.Comparison, value.Goal} {
		if ref != nil {
			if ref.Format != nil {
				return fmt.Errorf("KPI field %q format cannot be represented by a dashboard presentation", ref.Field)
			}
			if _, err := c.visualFieldOutput(*ref); err != nil {
				return err
			}
		}
	}
	if value.Trend != nil {
		if value.Trend.Category.Format != nil {
			return fmt.Errorf("KPI trend category %q format cannot be represented by a dashboard presentation", value.Trend.Category.Field)
		}
		if value.Trend.Value.Format != nil {
			return fmt.Errorf("KPI trend value %q format cannot be represented by a dashboard presentation", value.Trend.Value.Field)
		}
		if _, err := c.visualFieldOutput(value.Trend.Category); err != nil {
			return err
		}
		if _, err := c.visualFieldOutput(value.Trend.Value); err != nil {
			return err
		}
	}
	return nil
}

func (c converter) validateProportionalChannels(value *exploration.ProportionalExplorationVisualization) error {
	dimensions, metrics, err := c.querySelections()
	if err != nil {
		return err
	}
	if len(dimensions) == 0 {
		return fmt.Errorf("proportional category requires a selected dimension")
	}
	category, err := c.visualFieldOutput(value.Category)
	if err != nil {
		return err
	}
	if category != dimensionOutput(dimensions[0]) {
		return fmt.Errorf("proportional category %q cannot be represented; dashboard charts use dimension %q", category, dimensionOutput(dimensions[0]))
	}
	if len(metrics) == 0 {
		return fmt.Errorf("proportional value requires a selected metric")
	}
	amount, err := c.visualFieldOutput(value.Value)
	if err != nil {
		return err
	}
	if amount != metricOutput(metrics[0]) {
		return fmt.Errorf("proportional value %q cannot be represented; dashboard charts use metric %q", amount, metricOutput(metrics[0]))
	}
	return nil
}

func (c converter) validatePolarChannels(value *exploration.PolarExplorationVisualization) error {
	dimensions, metrics, err := c.querySelections()
	if err != nil {
		return err
	}
	if value.Category != nil {
		if len(dimensions) == 0 {
			return fmt.Errorf("polar category requires a selected dimension")
		}
		category, err := c.visualFieldOutput(*value.Category)
		if err != nil {
			return err
		}
		if category != dimensionOutput(dimensions[0]) {
			return fmt.Errorf("polar category %q cannot be represented; dashboard charts use dimension %q", category, dimensionOutput(dimensions[0]))
		}
	}
	if len(metrics) == 0 {
		return fmt.Errorf("polar value requires a selected metric")
	}
	amount, err := c.visualFieldOutput(value.Value)
	if err != nil {
		return err
	}
	if amount != metricOutput(metrics[0]) {
		return fmt.Errorf("polar value %q cannot be represented; dashboard charts use metric %q", amount, metricOutput(metrics[0]))
	}
	return nil
}

func (c converter) kpiValueBinding(value exploration.ExplorationVisualizationFieldRef) (*document.DashboardKPIValueBinding, error) {
	field, err := c.visualFieldOutput(value)
	if err != nil {
		return nil, err
	}
	return &document.DashboardKPIValueBinding{Dataset: "primary", Field: field, Label: field}, nil
}

func (c converter) visualFieldOutput(value exploration.ExplorationVisualizationFieldRef) (string, error) {
	dimensions, metrics, err := c.querySelections()
	if err != nil {
		return "", fmt.Errorf("visualization field %q: %w", value.Field, err)
	}
	field := strings.TrimSpace(value.Field)
	if field == "" {
		return "", fmt.Errorf("visualization field is required")
	}
	if output, ok := rawSelectedFieldOutput(field, dimensions, metrics); ok {
		return output, nil
	}
	return c.selectedFieldOutput(field, "visualization", dimensions, metrics)
}

func (c converter) selectedFieldOutput(field, role string, dimensions []document.DashboardDimensionSelection, metrics []document.DashboardMetricSelection) (string, error) {
	field = strings.TrimSpace(field)
	if output, ok := rawSelectedFieldOutput(field, dimensions, metrics); ok {
		return output, nil
	}
	resolved, err := c.resolve(field, role)
	if err != nil {
		return "", err
	}
	for _, selection := range dimensions {
		if name, ok := dimensionName(selection); ok && name == resolved {
			return dimensionOutput(selection), nil
		}
	}
	for _, selection := range metrics {
		if name, ok := metricName(selection); ok && name == resolved {
			return metricOutput(selection), nil
		}
	}
	return "", fmt.Errorf("%s field %q is not selected by the exploration", role, field)
}

func rawSelectedFieldOutput(field string, dimensions []document.DashboardDimensionSelection, metrics []document.DashboardMetricSelection) (string, bool) {
	for _, selection := range dimensions {
		if name, ok := dimensionName(selection); ok && (name == field || dimensionOutput(selection) == field) {
			return dimensionOutput(selection), true
		}
	}
	for _, selection := range metrics {
		if name, ok := metricName(selection); ok && (name == field || metricOutput(selection) == field) {
			return metricOutput(selection), true
		}
	}
	return "", false
}

func (c converter) validatePivotVisualization() error {
	if c.spec.Visualization == nil || c.spec.Visualization.Value == nil {
		return nil
	}
	switch value := c.spec.Visualization.Value.(type) {
	case *exploration.PivotExplorationVisualization:
		if value == nil {
			return fmt.Errorf("pivot visualization is nil")
		}
		return c.validatePivotVisualizationRefs(value.Rows, value.Columns, value.Metrics)
	case *exploration.MatrixExplorationVisualization:
		if value == nil {
			return fmt.Errorf("matrix visualization is nil")
		}
		return c.validatePivotVisualizationRefs(value.Rows, value.Columns, value.Metrics)
	default:
		return fmt.Errorf("visualization %T cannot render a pivot query", value)
	}
}

func (c converter) validatePivotVisualizationRefs(rows, columns, metrics []exploration.ExplorationVisualizationFieldRef) error {
	if c.spec.Pivot == nil {
		return fmt.Errorf("pivot visualization requires pivot query configuration")
	}
	wantRows := make([]exploration.ExplorationDimensionRef, len(c.spec.Pivot.Rows))
	copy(wantRows, c.spec.Pivot.Rows)
	wantColumns := make([]exploration.ExplorationDimensionRef, len(c.spec.Pivot.Columns))
	copy(wantColumns, c.spec.Pivot.Columns)
	if err := c.matchVisualizationRefs("pivot rows", rows, wantRows); err != nil {
		return err
	}
	if err := c.matchVisualizationRefs("pivot columns", columns, wantColumns); err != nil {
		return err
	}
	if len(metrics) != len(c.spec.Pivot.Metrics) {
		return fmt.Errorf("pivot metrics cannot be represented: got %d fields, want %d", len(metrics), len(c.spec.Pivot.Metrics))
	}
	for index, ref := range metrics {
		if ref.Format != nil {
			return fmt.Errorf("pivot metric %q format cannot be represented by a dashboard query", ref.Field)
		}
		output, err := c.visualFieldOutput(ref)
		if err != nil {
			return err
		}
		wantSelection, resolveErr := c.metric(c.spec.Pivot.Metrics[index])
		if resolveErr != nil {
			return resolveErr
		}
		if output != metricOutput(wantSelection) {
			return fmt.Errorf("pivot metric %q cannot be represented at index %d", output, index)
		}
	}
	return nil
}

func (c converter) matchVisualizationRefs(kind string, refs []exploration.ExplorationVisualizationFieldRef, want []exploration.ExplorationDimensionRef) error {
	if len(refs) != len(want) {
		return fmt.Errorf("%s cannot be represented: got %d fields, want %d", kind, len(refs), len(want))
	}
	for index, ref := range refs {
		if ref.Format != nil {
			return fmt.Errorf("%s field %q format cannot be represented by a dashboard query", kind, ref.Field)
		}
		output, err := c.visualFieldOutput(ref)
		if err != nil {
			return err
		}
		wantValue := applyTimeDimension(want[index], c.spec.Time)
		wantSelection, resolveErr := c.dimension(wantValue)
		if resolveErr != nil {
			return resolveErr
		}
		if output != dimensionOutput(wantSelection) {
			return fmt.Errorf("%s field %q cannot be represented at index %d", kind, output, index)
		}
	}
	return nil
}

func (c converter) tablePresentation() *document.TableDashboardPresentation {
	rowHeight := int32(32)
	showHeader := true
	striped := false
	if c.spec.Table != nil {
		if c.spec.Table.Density != nil {
			if *c.spec.Table.Density == exploration.ExplorationTableDensityCompact {
				rowHeight = 24
			} else if *c.spec.Table.Density == exploration.ExplorationTableDensityComfortable {
				rowHeight = 40
			}
		}
		if c.spec.Table.RowHeight != nil {
			rowHeight = *c.spec.Table.RowHeight
		}
		if c.spec.Table.ShowHeader != nil {
			showHeader = *c.spec.Table.ShowHeader
		}
		if c.spec.Table.Striped != nil {
			striped = *c.spec.Table.Striped
		}
	}
	return &document.TableDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "table"}, Type: "table", RowHeight: rowHeight, ShowHeader: showHeader, Striped: striped}
}
