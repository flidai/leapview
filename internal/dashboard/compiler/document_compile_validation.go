package compiler

import (
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
)

func lowerCanonicalVisualSeries(query *LoweredDashboardQuery, visualType document.DashboardVisualType) error {
	if query == nil || query.Binding.Aggregate == nil {
		return nil
	}
	switch visualType {
	case document.DashboardVisualTypeLine, document.DashboardVisualTypeArea, document.DashboardVisualTypeBar, document.DashboardVisualTypeColumn, document.DashboardVisualTypeRadar:
	default:
		return nil
	}
	dimensions := query.Binding.Aggregate.Dimensions
	if len(dimensions) <= 1 {
		return nil
	}
	if len(dimensions) != 2 {
		return fmt.Errorf("%s supports at most one category-series dimension", visualType)
	}
	series := dimensions[1]
	query.Binding.Aggregate.Dimensions = append([]visualizationdefinition.FieldBinding(nil), dimensions[:1]...)
	query.Binding.Aggregate.Series = &series
	query.Binding.ResultShape = visualizationdefinition.ResultCategorySeriesValue
	return nil
}

func adjustCanonicalResultShape(binding *visualizationdefinition.QueryBinding, visualType document.DashboardVisualType) {
	if binding == nil {
		return
	}
	switch visualType {
	case document.DashboardVisualTypeHeatmap:
		binding.ResultShape = visualizationdefinition.ResultMatrixCells
	case document.DashboardVisualTypeHistogram:
		binding.ResultShape = visualizationdefinition.ResultHistogramBins
	case document.DashboardVisualTypeBoxplot:
		binding.ResultShape = visualizationdefinition.ResultDistribution
	case document.DashboardVisualTypeScatter:
		binding.ResultShape = visualizationdefinition.ResultPoints
	case document.DashboardVisualTypeGraph, document.DashboardVisualTypeSankey:
		binding.ResultShape = visualizationdefinition.ResultGraphEdges
	case document.DashboardVisualTypeTreemap, document.DashboardVisualTypeTree, document.DashboardVisualTypeSunburst:
		binding.ResultShape = visualizationdefinition.ResultHierarchyNodes
	case document.DashboardVisualTypeWaterfall:
		binding.ResultShape = visualizationdefinition.ResultCategoryDelta
	case document.DashboardVisualTypeCandlestick:
		binding.ResultShape = visualizationdefinition.ResultOHLC
	case document.DashboardVisualTypeCombo:
		binding.ResultShape = visualizationdefinition.ResultCategoryMultiMeasure
	}
}

func validateDerivedResultAliases(query LoweredDashboardQuery, visualType document.DashboardVisualType) error {
	reserved := map[string]struct{}{}
	switch visualType {
	case document.DashboardVisualTypeTreemap, document.DashboardVisualTypeTree, document.DashboardVisualTypeSunburst:
		reserved = map[string]struct{}{"node": {}, "parent": {}}
	case document.DashboardVisualTypeGraph, document.DashboardVisualTypeSankey:
		return nil
	case document.DashboardVisualTypeWaterfall:
		reserved = map[string]struct{}{"start": {}, "end": {}, "positive": {}}
	default:
		return nil
	}
	for _, field := range query.ResultFrame {
		if _, exists := reserved[field.Name]; exists {
			return fmt.Errorf("result field %q is reserved for derived %s output", field.Name, visualType)
		}
	}
	return nil
}

func validateCanonicalVisualResultReferences(visual document.DashboardVisual, query LoweredDashboardQuery) error {
	refs := make([]string, 0)
	if visual.Calculations != nil {
		for _, calculation := range *visual.Calculations {
			refs = append(refs, calculation.Source)
			if calculation.Parent != nil {
				refs = append(refs, *calculation.Parent)
			}
			if calculation.PartitionBy != nil {
				refs = append(refs, (*calculation.PartitionBy)...)
			}
			if calculation.OrderBy != nil {
				for _, order := range *calculation.OrderBy {
					refs = append(refs, order.Field)
				}
			}
			if calculation.Lookup != nil {
				refs = append(refs, calculation.Lookup.Field)
			}
		}
	}
	if visual.Interactions != nil {
		for _, interaction := range *visual.Interactions {
			switch value := interaction.Value.(type) {
			case *document.SelectionDashboardInteraction:
				for _, mapping := range value.Mappings {
					refs = append(refs, mapping.Field)
					if mapping.Label != nil {
						refs = append(refs, *mapping.Label)
					}
				}
			case *document.SpatialSelectionDashboardInteraction:
				refs = append(refs, value.Latitude.Source, value.Longitude.Source)
			}
		}
	}
	for _, ref := range refs {
		if strings.TrimSpace(ref) == "" {
			continue
		}
		if err := query.ValidateResultReference(ref); err != nil {
			return err
		}
	}
	return nil
}

func validateCanonicalInteractionKinds(visual document.DashboardVisual) error {
	if visual.Interactions == nil {
		return nil
	}
	for index, interaction := range *visual.Interactions {
		kind, err := interaction.Type()
		if err != nil {
			return fmt.Errorf("interaction %d: %w", index, err)
		}
		if kind == "spatialSelection" && visual.Type != document.DashboardVisualTypeMap {
			return fmt.Errorf("interaction %d spatialSelection is only supported on map visuals", index)
		}
	}
	return nil
}

func valueOrString(value *string, fallback string) string {
	if value != nil && strings.TrimSpace(*value) != "" {
		return *value
	}
	return fallback
}
