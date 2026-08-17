package project

import (
	"encoding/json"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

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
	Connections       map[string]semanticmodel.Connection          `json:"Connections,omitempty"`
	Sources           map[string]semanticmodel.Source              `json:"Sources,omitempty"`
	Datasets          map[string]semanticmodel.SemanticDatasetSpec `json:"Datasets,omitempty"`
	DatasetDetails    map[string]semanticmodel.Table               `json:"DatasetDetails,omitempty"`
	Relationships     []semanticmodel.Relationship                 `json:"Relationships,omitempty"`
	Filters           map[string]semanticmodel.SemanticFilterSpec  `json:"Filters,omitempty"`
	Dimensions        map[string]semanticmodel.SemanticDimension   `json:"Dimensions,omitempty"`
	Metrics           map[string]semanticmodel.Metric              `json:"Metrics,omitempty"`
}

// SemanticModelAssetPayload converts a validated compiled model into the
// dynamic transport map consumed by the existing project UI signals. The
// conversion is deliberately at this boundary: the model remains strongly
// typed until it is encoded for the browser-facing read model.
func SemanticModelAssetPayload(model *semanticmodel.Model) map[string]any {
	if model == nil {
		return nil
	}
	projection := SemanticModelAssetReadModel{
		Name:              model.Name,
		Title:             model.Title,
		Description:       model.Description,
		AIContext:         model.AIContext,
		DefaultConnection: model.DefaultConnection,
		Connections:       model.Connections,
		Sources:           model.Sources,
		Datasets:          model.Datasets,
		DatasetDetails:    model.Tables,
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
