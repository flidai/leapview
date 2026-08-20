package project

import (
	"encoding/json"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

// ModelTableAssetReadModel is the typed detail projection for a model table.
// The serving graph deliberately carries only portable resource metadata;
// runtime execution and the detached authored definition are paired by the
// active project read model before this transport map is emitted. Keeping the
// conversion here prevents the UI from treating target-bound execution as
// authored source.
type ModelTableAssetReadModel struct {
	Definition         semanticmodel.ExecutionDefinition         `json:"Definition,omitempty"`
	Configuration      string                                    `json:"Configuration,omitempty"`
	Columns            map[string]semanticmodel.ModelColumn      `json:"Columns,omitempty"`
	Entities           map[string]semanticmodel.EntityDefinition `json:"Entities,omitempty"`
	GrainEntity        string                                    `json:"GrainEntity,omitempty"`
	Dimensions         map[string]semanticmodel.MetricDimension  `json:"Dimensions,omitempty"`
	Description        string                                    `json:"Description,omitempty"`
	Schema             semanticmodel.TableSchema                 `json:"Schema,omitempty"`
	SourceDependencies []string                                  `json:"SourceDependencies,omitempty"`
	ModelDependencies  []string                                  `json:"ModelDependencies,omitempty"`
}

// ModelTableAssetPayload converts the validated compiled table into the
// dynamic transport map consumed by the existing project UI signals. The
// conversion is deliberately at this boundary: the table remains strongly
// typed until it is encoded for the browser-facing read model.
func ModelTableAssetPayload(table semanticmodel.Table) map[string]any {
	return ModelTableAssetPayloadWithAuthoredDefinition(table, nil)
}

// ModelTableAssetPayloadWithAuthoredDefinition builds the browser detail
// projection from the runtime table metadata while using the detached
// authored definition, when available, for the source/SQL shown to users.
// Runtime execution is never changed: only the transport read model's
// Definition field is selected from the non-secret authored union.
func ModelTableAssetPayloadWithAuthoredDefinition(table semanticmodel.Table, authored *projectmanifest.AuthoredModelDefinition) map[string]any {
	return ModelTableAssetPayloadWithAuthoredSource(table, authored, "")
}

// ModelTableAssetPayloadWithAuthoredSource adds the exact validated model
// document retained by the compiler. Model resources contain no resolved
// connection credentials; preserving their source lets every model detail,
// including direct-source models, show its configuration as authored.
func ModelTableAssetPayloadWithAuthoredSource(table semanticmodel.Table, authored *projectmanifest.AuthoredModelDefinition, configuration string) map[string]any {
	definition := table.Execution
	if authored != nil {
		switch authored.Type {
		case "direct":
			if authored.Source != "" {
				definition = semanticmodel.ExecutionDefinition{Source: authored.Source}
			}
		case "sql":
			if authored.SQL != "" {
				definition = semanticmodel.ExecutionDefinition{SQL: authored.SQL}
			}
		}
	}
	projection := ModelTableAssetReadModel{
		Definition:         definition,
		Configuration:      configuration,
		Columns:            cloneModelColumns(table.Columns),
		Entities:           cloneModelEntities(table.Entities),
		GrainEntity:        table.GrainEntity,
		Dimensions:         cloneMetricDimensions(table.Dimensions),
		Description:        table.Description,
		Schema:             table.Schema,
		SourceDependencies: append([]string(nil), table.SourceDependencies...),
		ModelDependencies:  append([]string(nil), table.ModelDependencies...),
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

func cloneModelColumns(input map[string]semanticmodel.ModelColumn) map[string]semanticmodel.ModelColumn {
	if input == nil {
		return nil
	}
	output := make(map[string]semanticmodel.ModelColumn, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneModelEntities(input map[string]semanticmodel.EntityDefinition) map[string]semanticmodel.EntityDefinition {
	if input == nil {
		return nil
	}
	output := make(map[string]semanticmodel.EntityDefinition, len(input))
	for key, value := range input {
		value.Fields = append([]string(nil), value.Fields...)
		output[key] = value
	}
	return output
}

func cloneMetricDimensions(input map[string]semanticmodel.MetricDimension) map[string]semanticmodel.MetricDimension {
	if input == nil {
		return nil
	}
	output := make(map[string]semanticmodel.MetricDimension, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
