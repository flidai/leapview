package compiler

import (
	"fmt"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

func lowerSemanticDimensions(values *map[string]projectcontracts.SemanticDimension) map[string]semanticmodel.SemanticDimensionSpec {
	if values == nil {
		return nil
	}
	result := make(map[string]semanticmodel.SemanticDimensionSpec, len(*values))
	for name, value := range *values {
		bindings := make(map[string]semanticmodel.DimensionBinding, len(value.Bindings))
		for dataset, binding := range value.Bindings {
			bindings[dataset] = semanticmodel.DimensionBinding{Field: binding.Field, Path: optionalStrings(binding.Path)}
		}
		dimension := semanticmodel.SemanticDimensionSpec{
			Label: optionalString(value.Label), Description: optionalString(value.Description), AIContext: lowerAIContext(value.AiContext),
			Datatype: semanticmodel.LogicalDataType(optionalString(value.Datatype)), Bindings: bindings,
		}
		if value.Time != nil {
			dimension.Time = &semanticmodel.TimeSemanticsSpec{
				NativeGrain: value.Time.NativeGrain, Grains: append([]string(nil), value.Time.Grains...),
				Calendar: optionalString(value.Time.Calendar), Timezone: optionalString(value.Time.Timezone),
			}
		}
		result[name] = dimension
	}
	return result
}

func lowerLocalSemanticDimensions(datasets map[string]projectcontracts.SemanticDataset, target map[string]semanticmodel.SemanticDimensionSpec) (map[string]semanticmodel.SemanticDimensionSpec, error) {
	if target == nil {
		target = map[string]semanticmodel.SemanticDimensionSpec{}
	}
	owners := map[string]string{}
	for _, datasetName := range sortedMapKeys(datasets) {
		dataset := datasets[datasetName]
		if dataset.Dimensions == nil {
			continue
		}
		for _, name := range sortedMapKeys(*dataset.Dimensions) {
			if owner, exists := owners[name]; exists {
				return nil, fmt.Errorf("datasets.%s.dimensions.%s conflicts with datasets.%s.dimensions.%s", datasetName, name, owner, name)
			}
			if _, exists := target[name]; exists {
				return nil, fmt.Errorf("datasets.%s.dimensions.%s conflicts with spec.dimensions.%s", datasetName, name, name)
			}
			owners[name] = datasetName
			value := (*dataset.Dimensions)[name]
			field := optionalString(value.Field)
			if field == "" {
				field = name
			}
			result := semanticmodel.SemanticDimensionSpec{
				Label: optionalString(value.Label), Description: optionalString(value.Description), AIContext: lowerAIContext(value.AiContext),
				Datatype: semanticmodel.LogicalDataType(optionalString(value.Datatype)),
				Bindings: map[string]semanticmodel.DimensionBinding{datasetName: {Field: datasetName + "." + field}},
			}
			if value.Time != nil {
				result.Time = &semanticmodel.TimeSemanticsSpec{NativeGrain: value.Time.NativeGrain, Grains: append([]string(nil), value.Time.Grains...), Calendar: optionalString(value.Time.Calendar), Timezone: optionalString(value.Time.Timezone)}
			}
			target[name] = result
		}
	}
	return target, nil
}

func lowerSemanticMetrics(values *map[string]projectcontracts.SemanticMetric, datasets map[string]projectcontracts.SemanticDataset) (map[string]semanticmodel.SemanticMetricSpec, error) {
	result := make(map[string]semanticmodel.SemanticMetricSpec)
	if values != nil {
		for name, value := range *values {
			metric := semanticmodel.SemanticMetricSpec{}
			switch variant := value.Value.(type) {
			case *projectcontracts.SemanticMetricDerivedVariant:
				metric.Type, metric.Expression = variant.Type, variant.Expression
				lowerSemanticMetricCommon(&metric, variant.Label, variant.Description, variant.AiContext, variant.Unit, variant.Format, variant.Hidden)
			case *projectcontracts.SemanticMetricRatioVariant:
				metric.Type, metric.Numerator, metric.Denominator = variant.Type, variant.Numerator, variant.Denominator
				lowerSemanticMetricCommon(&metric, variant.Label, variant.Description, variant.AiContext, variant.Unit, variant.Format, variant.Hidden)
			case nil:
				return nil, fmt.Errorf("metric %q variant is required", name)
			default:
				return nil, fmt.Errorf("metric %q has unsupported variant %T", name, value.Value)
			}
			result[name] = metric
		}
	}
	owners := map[string]string{}
	for _, datasetName := range sortedMapKeys(datasets) {
		dataset := datasets[datasetName]
		if dataset.Metrics == nil {
			continue
		}
		for _, name := range sortedMapKeys(*dataset.Metrics) {
			if owner, exists := owners[name]; exists {
				return nil, fmt.Errorf("datasets.%s.metrics.%s conflicts with datasets.%s.metrics.%s", datasetName, name, owner, name)
			}
			if _, exists := result[name]; exists {
				return nil, fmt.Errorf("datasets.%s.metrics.%s conflicts with spec.metrics.%s", datasetName, name, name)
			}
			owners[name] = datasetName
			value := (*dataset.Metrics)[name]
			field := optionalString(value.Field)
			if field == "" {
				field = name
			}
			metric := semanticmodel.SemanticMetricSpec{Type: "aggregate", Dataset: datasetName, Aggregation: value.Agg, Input: &semanticmodel.MetricInput{Field: datasetName + "." + field}, Where: optionalStrings(value.Where), Empty: optionalString(value.Empty), TimeDimension: optionalString(value.TimeDimension)}
			if metric.TimeDimension == "" {
				metric.TimeDimension = optionalString(dataset.DefaultTimeDimension)
			}
			lowerSemanticMetricCommon(&metric, value.Label, value.Description, value.AiContext, value.Unit, value.Format, value.Hidden)
			result[name] = metric
		}
	}
	return result, nil
}

func lowerSemanticMetricCommon(metric *semanticmodel.SemanticMetricSpec, label, description *string, aiContext *projectcontracts.AIContext, unit, format *string, hidden *bool) {
	metric.Label = optionalString(label)
	metric.Description = optionalString(description)
	metric.AIContext = lowerAIContext(aiContext)
	metric.Unit = optionalString(unit)
	metric.Format = optionalString(format)
	if hidden != nil {
		metric.Hidden = *hidden
	}
}
