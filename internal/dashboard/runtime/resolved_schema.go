package runtime

import (
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

type resolvedVisualizationField struct {
	member  string
	metric  bool
	dataset string
}

// resolveDashboardSchemas applies activation-owned semantic types to the
// compiler-produced visual result schemas. Query bindings remain unchanged;
// executable planning and the served frame now consume the same discovered
// Model schema.
func resolveDashboardSchemas(definition dashboarddefinition.Definition, model *semanticmodel.Model) (dashboarddefinition.Definition, error) {
	if model == nil {
		return dashboarddefinition.Definition{}, fmt.Errorf("semantic model is required")
	}
	for visualID, visual := range definition.Visualizations {
		resolved, err := resolveVisualizationSchema(visual, model)
		if err != nil {
			return dashboarddefinition.Definition{}, fmt.Errorf("visualization %q: %w", visualID, err)
		}
		definition.Visualizations[visualID] = resolved
	}
	return definition, nil
}

func resolveVisualizationSchema(definition visualizationdefinition.Definition, model *semanticmodel.Model) (visualizationdefinition.Definition, error) {
	bindings := map[string]map[string]resolvedVisualizationField{}
	addQueryFieldBindings(bindings, definition.Query)
	for datasetID, query := range definition.SecondaryQueries {
		query.DatasetID = datasetID
		addQueryFieldBindings(bindings, query)
	}
	base, err := definition.Spec.Base()
	if err != nil {
		return visualizationdefinition.Definition{}, err
	}
	for datasetIndex := range base.Datasets {
		schema := &base.Datasets[datasetIndex]
		byAlias := bindings[schema.ID]
		for fieldIndex := range schema.Fields {
			field := &schema.Fields[fieldIndex]
			binding, ok := byAlias[field.ID]
			if !ok {
				continue
			}
			datatype, err := resolvedFieldDataType(model, binding)
			if err != nil {
				return visualizationdefinition.Definition{}, fmt.Errorf("dataset %q field %q: %w", schema.ID, field.ID, err)
			}
			field.DataType = visualizationDataType(datatype)
		}
	}
	revision, err := visualizationir.ComputeSpecRevision(definition.Spec)
	if err != nil {
		return visualizationdefinition.Definition{}, err
	}
	definition.SpecRevision = revision.String()
	if err := definition.Validate(); err != nil {
		return visualizationdefinition.Definition{}, err
	}
	return definition, nil
}

func addQueryFieldBindings(result map[string]map[string]resolvedVisualizationField, query visualizationdefinition.QueryBinding) {
	datasetID := query.DatasetID
	if datasetID == "" {
		datasetID = "primary"
	}
	if result[datasetID] == nil {
		result[datasetID] = map[string]resolvedVisualizationField{}
	}
	add := func(fields []visualizationdefinition.FieldBinding, metric bool, dataset string) {
		for _, field := range fields {
			result[datasetID][field.Alias] = resolvedVisualizationField{member: field.FieldID, metric: metric, dataset: dataset}
		}
	}
	switch {
	case query.Aggregate != nil:
		add(query.Aggregate.Dimensions, false, query.Aggregate.TableID)
		if query.Aggregate.Series != nil {
			add([]visualizationdefinition.FieldBinding{*query.Aggregate.Series}, false, query.Aggregate.TableID)
		}
		if query.Aggregate.Time != nil {
			add([]visualizationdefinition.FieldBinding{{FieldID: query.Aggregate.Time.FieldID, Alias: query.Aggregate.Time.Alias}}, false, query.Aggregate.TableID)
		}
		add(query.Aggregate.Metrics, true, query.Aggregate.TableID)
	case query.Detail != nil:
		add(query.Detail.Fields, false, query.Detail.TableID)
	case query.Matrix != nil:
		add(query.Matrix.Rows, false, query.Matrix.TableID)
		add(query.Matrix.Columns, false, query.Matrix.TableID)
		add(query.Matrix.Metrics, true, query.Matrix.TableID)
	case query.Pivot != nil:
		add(query.Pivot.Rows, false, query.Pivot.TableID)
		add(query.Pivot.Columns, false, query.Pivot.TableID)
		add(query.Pivot.Metrics, true, query.Pivot.TableID)
	case query.Spatial != nil:
		add(query.Spatial.Dimensions, false, query.Spatial.TableID)
		if query.Spatial.Series != nil {
			add([]visualizationdefinition.FieldBinding{*query.Spatial.Series}, false, query.Spatial.TableID)
		}
		if query.Spatial.Time != nil {
			add([]visualizationdefinition.FieldBinding{{FieldID: query.Spatial.Time.FieldID, Alias: query.Spatial.Time.Alias}}, false, query.Spatial.TableID)
		}
		add(query.Spatial.Metrics, true, query.Spatial.TableID)
	}
}

func resolvedFieldDataType(model *semanticmodel.Model, field resolvedVisualizationField) (semanticmodel.LogicalDataType, error) {
	if field.metric {
		if _, ok := model.Metrics[field.member]; !ok {
			return "", fmt.Errorf("unknown metric %q", field.member)
		}
		return model.MetricDataType(field.member)
	}
	if dimension, err := model.ResolveSemanticDimension(field.member); err == nil {
		return dimension.Datatype, nil
	}
	member := field.member
	if !strings.Contains(member, ".") && field.dataset != "" {
		table := field.dataset
		if dataset, ok := model.Datasets[field.dataset]; ok && dataset.Model != "" {
			table = dataset.Model
		}
		member = table + "." + member
	}
	dimension, err := model.ResolveDimension(member)
	if err != nil {
		return "", err
	}
	return dimension.Datatype, nil
}

func visualizationDataType(value semanticmodel.LogicalDataType) visualizationir.VisualizationDataType {
	switch value {
	case semanticmodel.DataTypeInteger:
		return visualizationir.VisualizationDataTypeInteger
	case semanticmodel.DataTypeDecimal:
		return visualizationir.VisualizationDataTypeDecimal
	case semanticmodel.DataTypeFloat:
		return visualizationir.VisualizationDataTypeFloat
	case semanticmodel.DataTypeBoolean:
		return visualizationir.VisualizationDataTypeBoolean
	case semanticmodel.DataTypeDate:
		return visualizationir.VisualizationDataTypeDate
	case semanticmodel.DataTypeTime, semanticmodel.DataTypeDateTime, semanticmodel.DataTypeDateTimeTZ:
		return visualizationir.VisualizationDataTypeTemporal
	default:
		return visualizationir.VisualizationDataTypeString
	}
}
