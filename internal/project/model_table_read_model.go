package project

import (
	"encoding/json"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

// ModelTableAssetReadModel is the typed detail projection for a compiled
// model table. The serving graph deliberately carries only portable resource
// metadata; model-table definitions remain in the active compiled manifest.
// Keeping this projection at the read-model boundary prevents the UI from
// treating a graph resource as if it were the compiled table definition.
type ModelTableAssetReadModel struct {
	Source             string                                   `json:"Source,omitempty"`
	Sources            []string                                 `json:"Sources,omitempty"`
	SourceReads        map[string][]string                      `json:"SourceReads,omitempty"`
	SQL                string                                   `json:"SQL,omitempty"`
	Transform          semanticmodel.Transform                  `json:"Transform,omitempty"`
	Columns            map[string]semanticmodel.ModelColumn     `json:"Columns,omitempty"`
	Entities           map[string]semanticmodel.ModelEntitySpec `json:"Entities,omitempty"`
	GrainEntity        string                                   `json:"GrainEntity,omitempty"`
	Dimensions         map[string]semanticmodel.MetricDimension `json:"Dimensions,omitempty"`
	Description        string                                   `json:"Description,omitempty"`
	Schema             semanticmodel.TableSchema                `json:"Schema,omitempty"`
	SourceDependencies []string                                 `json:"SourceDependencies,omitempty"`
	ModelDependencies  []string                                 `json:"ModelDependencies,omitempty"`
}

// ModelTableAssetPayload converts the validated compiled table into the
// dynamic transport map consumed by the existing project UI signals. The
// conversion is deliberately at this boundary: the table remains strongly
// typed until it is encoded for the browser-facing read model.
func ModelTableAssetPayload(table semanticmodel.Table) map[string]any {
	projection := ModelTableAssetReadModel{
		Source:             table.Source,
		Sources:            append([]string(nil), table.Sources...),
		SourceReads:        cloneStringSliceMap(table.SourceReads),
		SQL:                table.SQL,
		Transform:          table.Transform,
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

func cloneStringSliceMap(input map[string][]string) map[string][]string {
	if input == nil {
		return nil
	}
	output := make(map[string][]string, len(input))
	for key, values := range input {
		output[key] = append([]string(nil), values...)
	}
	return output
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

func cloneModelEntities(input map[string]semanticmodel.ModelEntitySpec) map[string]semanticmodel.ModelEntitySpec {
	if input == nil {
		return nil
	}
	output := make(map[string]semanticmodel.ModelEntitySpec, len(input))
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
