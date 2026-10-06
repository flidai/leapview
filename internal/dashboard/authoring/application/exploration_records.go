package application

import (
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard/authoring/explorationadapter"
)

func deriveRecordsExplorationOptions(spec exploration.ExplorationSpec, model *semanticmodel.Model, compiled *semanticquery.CompiledModel, visualID string) (explorationadapter.Options, error) {
	if err := exploration.ValidateAgainstModel(model, &spec); err != nil {
		return explorationadapter.Options{}, err
	}
	// Filters remain semantic dashboard controls. Resolve those independently;
	// record selections need no semantic alias for a proven physical column.
	filters := spec
	filters.Mode = nil
	filters.Dimensions = nil
	filters.Sort = nil
	filters.Visualization = nil
	options, err := deriveExplorationAdapterOptions(filters, model, compiled, visualID)
	if err != nil {
		return explorationadapter.Options{}, err
	}
	options.RecordFields = map[string]string{}
	dataset := *spec.DatasetID
	for _, selection := range spec.Dimensions {
		field := selection.Field
		physical := field
		if _, ok := compiled.SemanticDimension(field); ok {
			binding, bound := compiled.DimensionBinding(field, dataset)
			if !bound {
				return explorationadapter.Options{}, fmt.Errorf("record field %q has no binding on dataset %q", field, dataset)
			}
			physical = binding.Physical.Field
		}
		root, column, qualified := strings.Cut(physical, ".")
		if !qualified || root != dataset || column == "" {
			return explorationadapter.Options{}, fmt.Errorf("record field %q is not a physical field on dataset %q", field, dataset)
		}
		if _, ok := compiled.PhysicalField(physical); !ok {
			return explorationadapter.Options{}, fmt.Errorf("record field %q is not active", physical)
		}
		options.RecordFields[field] = column
	}
	return options, nil
}
