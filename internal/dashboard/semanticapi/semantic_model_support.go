package http

import (
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/api"
)

func semanticModelSummaryDTO(row dashboard.CatalogModel) api.SemanticModelSummary {
	return api.SemanticModelSummary{ID: row.ID.String(), Title: row.Title, Description: row.Description}
}

func SemanticTableProjection(model *semanticmodel.Model, datasetID string, table semanticmodel.Table) api.SemanticDatasetResponse {
	entities := make(map[string]api.SemanticEntitySummary, len(table.Entities))
	for name, entity := range table.Entities {
		entities[name] = api.SemanticEntitySummary{Type: entity.Type, Fields: append([]string(nil), entity.Fields...)}
	}
	return api.SemanticDatasetResponse{
		ID:          datasetID,
		Model:       table.ModelName,
		Description: table.Description,
		GrainEntity: table.GrainEntity,
		Entities:    entities,
		FieldCount:  len(table.Dimensions),
		MetricCount: semanticDatasetMetricCount(model, datasetID),
	}
}

func semanticDatasetMetricCount(model *semanticmodel.Model, datasetID string) int {
	if model == nil {
		return 0
	}
	count := 0
	for _, metric := range model.Metrics {
		if metric.Dataset == datasetID {
			count++
		}
	}
	return count
}

func SemanticDatasetFieldsProjection(model *semanticmodel.Model, datasetID string, table semanticmodel.Table) []api.SemanticFieldResponse {
	out := make([]api.SemanticFieldResponse, 0, len(table.Dimensions)+semanticDatasetMetricCount(model, datasetID))
	for _, fieldID := range sortedMapKeys(table.Dimensions) {
		dimension := table.Dimensions[fieldID]
		out = append(out, api.SemanticFieldResponse{
			ID:          datasetID + "." + fieldID,
			Kind:        "dimension",
			Dataset:     datasetID,
			Name:        fieldID,
			Label:       dimension.Label,
			Description: dimension.Description,
			Type:        dimension.Type,
			Datatype:    semanticDimensionDatatype(dimension),
		})
	}
	for _, metricID := range sortedMapKeys(model.Metrics) {
		metric := model.Metrics[metricID]
		if metric.Dataset != datasetID {
			continue
		}
		out = append(out, semanticMetricFieldDTO(model, metricID, datasetID, metricID, metric))
	}
	return out
}

func SemanticModelFieldsProjection(model *semanticmodel.Model) []api.SemanticFieldResponse {
	out := make([]api.SemanticFieldResponse, 0, len(model.Dimensions)+len(model.Metrics))
	for _, name := range sortedMapKeys(model.Dimensions) {
		dimension := model.Dimensions[name]
		datatype := string(dimension.Datatype)
		if datatype == "" {
			datatype = dimension.Type
		}
		out = append(out, api.SemanticFieldResponse{ID: name, Kind: "dimension", Name: name, Label: dimension.Label, Description: dimension.Description, Type: dimension.Type, Datatype: datatype, Grains: append([]string{}, dimension.Grains...)})
	}
	for _, name := range sortedMapKeys(model.Metrics) {
		metric := model.Metrics[name]
		out = append(out, api.SemanticFieldResponse{ID: name, Kind: "metric", Dataset: metric.Dataset, Name: name, Label: metric.Label, Description: metric.Description, Unit: metric.Unit, Format: metric.Format, Datatype: semanticMetricDatatype(model, metric)})
	}
	return out
}

func semanticMetricFieldDTO(model *semanticmodel.Model, id, datasetID, name string, metric semanticmodel.Metric) api.SemanticFieldResponse {
	return api.SemanticFieldResponse{
		ID:          id,
		Kind:        "metric",
		Dataset:     datasetID,
		Name:        name,
		Label:       metric.Label,
		Description: metric.Description,
		Unit:        metric.Unit,
		Format:      metric.Format,
		Datatype:    semanticMetricDatatype(model, metric),
	}
}

func semanticDimensionDatatype(dimension semanticmodel.MetricDimension) string {
	if dimension.Datatype != "" {
		return string(dimension.Datatype)
	}
	return semanticColumnType(dimension.Type)
}

func semanticMetricDatatype(model *semanticmodel.Model, metric semanticmodel.Metric) string {
	if metric.Aggregation == "count" || metric.Aggregation == "count_distinct" {
		return "integer"
	}
	if metric.Aggregation == "avg" {
		return "decimal"
	}
	if model != nil && metric.Input != nil && metric.Input.Field != "" {
		if dimension, err := model.ResolveDimension(metric.Input.Field); err == nil {
			return semanticDimensionDatatype(dimension)
		}
	}
	return semanticColumnType(semanticMetricTypeFromModel(model, metric))
}

func semanticRelationshipDTO(relationship semanticmodel.Relationship) (api.SemanticRelationshipResponse, error) {
	fromDataset, fromFields, err := semanticmodel.RelationshipEndpoint(relationship, true)
	if err != nil {
		return api.SemanticRelationshipResponse{}, fmt.Errorf("relationship %q from endpoint: %w", relationship.ID, err)
	}
	toDataset, toFields, err := semanticmodel.RelationshipEndpoint(relationship, false)
	if err != nil {
		return api.SemanticRelationshipResponse{}, fmt.Errorf("relationship %q to endpoint: %w", relationship.ID, err)
	}
	return api.SemanticRelationshipResponse{
		ID: relationship.ID, FromDataset: fromDataset, FromFields: fromFields,
		ToDataset: toDataset, ToFields: toFields, Cardinality: relationship.Cardinality, Active: true,
	}, nil
}

func SemanticModelProjection(metrics Metrics, id string) (api.SemanticModelDescriptionResponse, bool) {
	catalog := metrics.Catalog()
	var catalogModel dashboard.CatalogModel
	for _, model := range catalog.Models {
		if model.ID.String() == id {
			catalogModel = model
			break
		}
	}
	if catalogModel.ID == "" {
		return api.SemanticModelDescriptionResponse{}, false
	}
	out := api.SemanticModelDescriptionResponse{
		ID:          catalogModel.ID.String(),
		Title:       catalogModel.Title,
		Description: catalogModel.Description,
		Dashboards:  dashboardsForModel(metrics, id),
	}
	if model := semanticModelForID(metrics, id); model != nil {
		compiled := compiledSemanticModel(metrics, id)
		if compiled == nil {
			return api.SemanticModelDescriptionResponse{}, false
		}
		fieldCount := 0
		for _, datasetID := range compiled.DatasetNames() {
			dataset, _ := compiled.Dataset(datasetID)
			fieldCount += len(dataset.Table().Dimensions)
		}
		out.Counts = &api.SemanticModelCounts{
			Datasets:      len(compiled.DatasetNames()),
			Fields:        fieldCount,
			Dimensions:    len(model.Dimensions),
			Metrics:       len(model.Metrics),
			Filters:       len(model.Filters),
			Relationships: len(model.Relationships),
		}
		datasets := make([]api.SemanticDatasetSummary, 0, len(compiled.DatasetNames()))
		for _, datasetID := range compiled.DatasetNames() {
			dataset, _ := compiled.Dataset(datasetID)
			table := dataset.Table()
			description := firstSemanticNonEmpty(dataset.Description(), table.Description)
			datasets = append(datasets, api.SemanticDatasetSummary{
				ID:          datasetID,
				Model:       dataset.ModelName(),
				Description: description,
				FieldCount:  len(table.Dimensions),
				MetricCount: semanticDatasetMetricCount(model, datasetID),
			})
		}
		sort.SliceStable(datasets, func(i, j int) bool { return datasets[i].ID < datasets[j].ID })
		out.Datasets = datasets
	}
	return out, true
}

func dashboardsForModel(metrics Metrics, modelID string) []api.ModelDashboardUsage {
	out := make([]api.ModelDashboardUsage, 0)
	for _, dashboardSummary := range metrics.Catalog().Dashboards {
		if metrics.Resolver() == nil {
			continue
		}
		resolved, err := metrics.Resolver().Resolve(dashboardSummary.ID)
		if err != nil || (resolved.Definition.SemanticModel != modelID && (resolved.Model == nil || resolved.Model.Name != modelID)) {
			continue
		}
		out = append(out, api.ModelDashboardUsage{
			ID:            resolved.Definition.ID,
			Title:         resolved.Definition.Title,
			SemanticModel: resolved.Definition.SemanticModel,
			Pages:         len(metrics.Pages(resolved.Definition.ID)),
		})
	}
	return out
}

func semanticModelForID(metrics Metrics, modelID string) *semanticmodel.Model {
	if model, ok := metrics.SemanticModel(modelID); ok {
		return model
	}
	for _, dashboardSummary := range metrics.Catalog().Dashboards {
		if metrics.Resolver() == nil {
			continue
		}
		resolved, err := metrics.Resolver().Resolve(dashboardSummary.ID)
		if err == nil && resolved.Model != nil && resolved.Model.Name == modelID {
			return resolved.Model
		}
	}
	return nil
}

// compiledSemanticModel obtains the activation-owned dataset bindings. API
// request paths require an activation planner; authoring models are not
// compiled on demand here.
func compiledSemanticModel(metrics Metrics, modelID string) *semanticquery.CompiledModel {
	if planner, ok := semanticPlanner(metrics, modelID); ok {
		return planner.CompiledModel()
	}
	return nil
}

func semanticModelActivationUnavailable(metrics Metrics, modelID string) bool {
	if metrics == nil || semanticModelForID(metrics, modelID) == nil {
		return false
	}
	planner, available := semanticPlanner(metrics, modelID)
	return !available || planner == nil || planner.CompiledModel() == nil
}

func semanticPlanner(metrics any, modelID string) (*semanticquery.Planner, bool) {
	provider, ok := metrics.(consumerPlannerProvider)
	if !ok {
		return nil, false
	}
	value, available := provider.Planner(modelID)
	if !available {
		return nil, false
	}
	planner, ok := value.(*semanticquery.Planner)
	if !ok || planner == nil {
		return nil, false
	}
	return planner, true
}

func firstSemanticNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
