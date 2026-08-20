package project

import (
	"encoding/json"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

type SemanticModelConnectionReadModel struct {
	Kind           string                           `json:"Kind,omitempty"`
	Description    string                           `json:"Description,omitempty"`
	Access         semanticmodel.ConnectionAccess   `json:"Access,omitempty"`
	ReaderDefaults *projectcontracts.ReaderDefaults `json:"ReaderDefaults,omitempty"`
}

// SemanticModelAssetReadModel is the typed detail projection used by the
// project browser. The serving graph intentionally carries only portable
// resource metadata; semantic model definitions remain in the validated
// serving-generation manifest. Keeping this projection separate from the
// graph payload prevents presentation code from decoding a graph resource as
// if it were a compiled semantic model.
type SemanticModelAssetReadModel struct {
	Name              string                                       `json:"Name,omitempty"`
	Title             string                                       `json:"Title,omitempty"`
	Description       string                                       `json:"Description,omitempty"`
	AIContext         *semanticmodel.AIContext                     `json:"AIContext,omitempty"`
	DefaultConnection string                                       `json:"DefaultConnection,omitempty"`
	Connections       map[string]SemanticModelConnectionReadModel  `json:"Connections,omitempty"`
	Sources           map[string]semanticmodel.Source              `json:"Sources,omitempty"`
	Datasets          map[string]semanticmodel.SemanticDatasetSpec `json:"Datasets,omitempty"`
	DatasetDetails    map[string]semanticmodel.Table               `json:"DatasetDetails,omitempty"`
	Relationships     []semanticmodel.Relationship                 `json:"Relationships,omitempty"`
	Filters           map[string]semanticmodel.SemanticFilterSpec  `json:"Filters,omitempty"`
	Dimensions        map[string]semanticmodel.SemanticDimension   `json:"Dimensions,omitempty"`
	Metrics           map[string]semanticmodel.Metric              `json:"Metrics,omitempty"`
}

// SemanticModelAssetPayload converts a validated semantic model and its
// activation-owned compiled dataset bindings into the dynamic transport map
// consumed by the existing project UI signals. Serving callers must provide
// the compiled model retained by the active generation; this boundary never
// compiles authored definitions on request.
func SemanticModelAssetPayload(model *semanticmodel.Model, compiled *semanticquery.CompiledModel) map[string]any {
	if model == nil || compiled == nil || len(compiled.DatasetNames()) == 0 || len(model.Datasets) != len(compiled.DatasetNames()) {
		return nil
	}
	datasets := make(map[string]semanticmodel.SemanticDatasetSpec, len(compiled.DatasetNames()))
	datasetDetails := make(map[string]semanticmodel.Table, len(compiled.DatasetNames()))
	for _, alias := range compiled.DatasetNames() {
		dataset, ok := compiled.Dataset(alias)
		spec, specOK := model.Datasets[alias]
		table, tableOK := model.Tables[alias]
		if !ok || !specOK || !tableOK || strings.TrimSpace(spec.Model) == "" || dataset.ModelName() != strings.TrimSpace(spec.Model) || table.ModelName != dataset.ModelName() {
			return nil
		}
		datasets[alias] = semanticmodel.SemanticDatasetSpec{Model: dataset.ModelName(), DefaultTimeDimension: dataset.DefaultTimeDimension(), DisplayName: dataset.DisplayName(), Description: dataset.Description()}
		datasetDetails[alias] = dataset.Table()
	}
	projection := SemanticModelAssetReadModel{
		Name:              model.Name,
		Title:             model.Title,
		Description:       model.Description,
		AIContext:         model.AIContext,
		DefaultConnection: model.DefaultConnection,
		Connections:       semanticModelConnections(model.Connections),
		Sources:           model.Sources,
		Datasets:          datasets,
		DatasetDetails:    datasetDetails,
		Relationships:     model.Relationships,
		Filters:           model.Filters,
		Dimensions:        model.Dimensions,
		Metrics:           model.Metrics,
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil
	}
	return payload
}

func semanticModelConnections(input map[string]semanticmodel.Connection) map[string]SemanticModelConnectionReadModel {
	output := make(map[string]SemanticModelConnectionReadModel, len(input))
	for name, connection := range input {
		output[name] = SemanticModelConnectionReadModel{
			Kind: connection.Kind, Description: connection.Description,
			Access: connection.Access, ReaderDefaults: connection.ReaderDefaults,
		}
	}
	return output
}
