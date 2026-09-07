package authz

import (
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

// authorizationFieldIsMetric preserves the source collection's member kind
// for count-only authorization projections. Legacy untyped fields are
// resolved only when the name is unambiguous; qualified physical fields are
// dimensions and therefore participate in the same ambiguity check when a
// model also exposes a same-named metric.
func authorizationFieldIsMetric(model *semanticmodel.Model, field dataquery.Field) (bool, error) {
	if model == nil {
		return false, fmt.Errorf("semantic model is required for authorization field %q", field.Field)
	}
	name := strings.TrimSpace(field.Field)
	if name == "" {
		return false, fmt.Errorf("semantic authorization projection contains an empty field")
	}
	switch strings.ToLower(strings.TrimSpace(field.Kind)) {
	case dataquery.FieldKindMetric:
		return true, nil
	case dataquery.FieldKindDimension:
		return false, nil
	case "":
		_, metricKnown := model.Metrics[name]
		_, dimensionKnown := model.Dimensions[name]
		if !dimensionKnown {
			if _, err := model.ResolveDimension(name); err == nil {
				dimensionKnown = true
			}
		}
		if metricKnown && dimensionKnown {
			return false, fmt.Errorf("semantic authorization projection field %q is ambiguous between metric and dimension", name)
		}
		return metricKnown, nil
	default:
		return false, fmt.Errorf("semantic authorization projection field %q has unsupported kind %q", name, field.Kind)
	}
}
