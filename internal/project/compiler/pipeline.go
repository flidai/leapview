package compiler

import (
	"fmt"
	"os"
	"strings"

	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	configschema "github.com/flidai/leapview/internal/project/schema"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

// LoadRefreshPipeline is the canonical authored Pipeline boundary. The
// generated TypeSpec DTO owns the public shape; this function explicitly
// lowers it into the runtime scheduler definition.
func LoadRefreshPipeline(path string) (refreshschedule.Definition, error) {
	if strings.TrimSpace(path) == "" {
		return refreshschedule.Definition{}, fmt.Errorf("pipeline path is required")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return refreshschedule.Definition{}, err
	}
	var authored projectcontracts.PipelineDocument
	if err := configschema.DecodeResource(configschema.KindPipeline, path, content, &authored); err != nil {
		return refreshschedule.Definition{}, err
	}
	return lowerRefreshPipeline(authored)
}

func lowerRefreshPipeline(authored projectcontracts.PipelineDocument) (refreshschedule.Definition, error) {
	if authored.Kind != projectcontracts.PipelineResourceKindPipeline {
		return refreshschedule.Definition{}, fmt.Errorf("pipeline kind %q is invalid", authored.Kind)
	}
	selection, err := lowerPipelineSelection(authored.Spec.Selection)
	if err != nil {
		return refreshschedule.Definition{}, fmt.Errorf("spec.selection: %w", err)
	}
	definition := refreshschedule.Definition{
		ID:              projectgraph.ResourceID(authored.Metadata.ID),
		Name:            authored.Metadata.Name,
		SemanticModelID: projectgraph.ResourceID(selection),
		Overlap:         string(authored.Spec.RunPolicy.Overlap),
	}
	for index, trigger := range authored.Spec.Triggers {
		switch variant := trigger.Value.(type) {
		case *projectcontracts.ManualPipelineTrigger:
			definition.ManualTriggers = append(definition.ManualTriggers, variant.ID)
		case *projectcontracts.SchedulePipelineTrigger:
			schedule, err := refreshschedule.ParseSchedule(variant.Cron, variant.Timezone)
			if err != nil {
				return refreshschedule.Definition{}, fmt.Errorf("spec.triggers[%d]: %w", index, err)
			}
			schedule.ID = variant.ID
			schedule.MissedOccurrences = string(variant.MissedOccurrences)
			definition.Schedules = append(definition.Schedules, schedule)
		default:
			return refreshschedule.Definition{}, fmt.Errorf("spec.triggers[%d]: trigger variant is required", index)
		}
	}
	if err := definition.Validate(); err != nil {
		return refreshschedule.Definition{}, err
	}
	return definition, nil
}

func lowerPipelineSelection(value projectcontracts.PipelineSelection) (string, error) {
	switch variant := value.Value.(type) {
	case *projectcontracts.SemanticModelPipelineSelection:
		if strings.TrimSpace(variant.SemanticModel) == "" {
			return "", fmt.Errorf("semanticModel is required")
		}
		return variant.SemanticModel, nil
	case nil:
		return "", fmt.Errorf("selection variant is required")
	default:
		return "", fmt.Errorf("unsupported selection variant %T", value.Value)
	}
}
