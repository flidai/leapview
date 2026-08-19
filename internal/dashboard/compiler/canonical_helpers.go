package compiler

import (
	"fmt"

	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func definitionAllowsExpression(definition dashboardfilter.Definition, expression dashboardfilter.Expression) bool {
	if expression.Kind == dashboardfilter.ExpressionUnfiltered {
		return true
	}
	for _, predicate := range definition.Predicates {
		if predicate.Kind != expression.Kind {
			continue
		}
		if expression.Operator == "" {
			return true
		}
		for _, operator := range predicate.Operators {
			if operator == expression.Operator {
				return true
			}
		}
	}
	return false
}

// mutableSpecificationBase returns the canonical IR base for controlled
// interaction completion. It never accepts authoring-layer visualization
// values.
func mutableSpecificationBase(spec visualizationir.VisualizationSpec) (*visualizationir.VisualizationSpecBase, error) {
	switch value := spec.Value.(type) {
	case *visualizationir.CartesianVisualizationSpec:
		if value == nil {
			return nil, fmt.Errorf("unsupported visualization specification %T (nil)", spec.Value)
		}
		return &value.VisualizationSpecBase, nil
	case *visualizationir.PointVisualizationSpec:
		if value == nil {
			return nil, fmt.Errorf("unsupported visualization specification %T (nil)", spec.Value)
		}
		return &value.VisualizationSpecBase, nil
	case *visualizationir.ProportionalVisualizationSpec:
		if value == nil {
			return nil, fmt.Errorf("unsupported visualization specification %T (nil)", spec.Value)
		}
		return &value.VisualizationSpecBase, nil
	case *visualizationir.HierarchyVisualizationSpec:
		if value == nil {
			return nil, fmt.Errorf("unsupported visualization specification %T (nil)", spec.Value)
		}
		return &value.VisualizationSpecBase, nil
	case *visualizationir.PolarVisualizationSpec:
		if value == nil {
			return nil, fmt.Errorf("unsupported visualization specification %T (nil)", spec.Value)
		}
		return &value.VisualizationSpecBase, nil
	case *visualizationir.TableVisualizationSpec:
		if value == nil {
			return nil, fmt.Errorf("unsupported visualization specification %T (nil)", spec.Value)
		}
		return &value.VisualizationSpecBase, nil
	case *visualizationir.MatrixVisualizationSpec:
		if value == nil {
			return nil, fmt.Errorf("unsupported visualization specification %T (nil)", spec.Value)
		}
		return &value.VisualizationSpecBase, nil
	case *visualizationir.PivotVisualizationSpec:
		if value == nil {
			return nil, fmt.Errorf("unsupported visualization specification %T (nil)", spec.Value)
		}
		return &value.VisualizationSpecBase, nil
	case *visualizationir.KPIVisualizationSpec:
		if value == nil {
			return nil, fmt.Errorf("unsupported visualization specification %T (nil)", spec.Value)
		}
		return &value.VisualizationSpecBase, nil
	case *visualizationir.GeographicVisualizationSpec:
		if value == nil {
			return nil, fmt.Errorf("unsupported visualization specification %T (nil)", spec.Value)
		}
		return &value.VisualizationSpecBase, nil
	default:
		return nil, fmt.Errorf("unsupported visualization specification %T", spec.Value)
	}
}
