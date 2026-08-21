package compiler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
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
	selection, timezone, deadline, policy, schedules, err := lowerPipelineSpec(authored.Spec)
	if err != nil {
		return refreshschedule.Definition{}, fmt.Errorf("spec: %w", err)
	}
	definition := refreshschedule.Definition{
		ID:                      projectgraph.ResourceID(authored.Metadata.ID),
		Name:                    authored.Metadata.Name,
		SemanticModelID:         projectgraph.ResourceID(selection),
		SelectionDigest:         authoredPipelineSelectionDigest(selection),
		Timezone:                timezone,
		StartingDeadlineSeconds: deadline,
		ConcurrencyPolicy:       policy,
		Schedules:               schedules,
	}
	if err := definition.Validate(); err != nil {
		return refreshschedule.Definition{}, err
	}
	return definition, nil
}

func authoredPipelineSelectionDigest(semanticModel string) string {
	encoded, _ := json.Marshal(struct {
		SemanticModel string `json:"semanticModel"`
	}{SemanticModel: semanticModel})
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func lowerPipelineSpec(spec projectcontracts.PipelineSpec) (string, string, int64, string, []refreshschedule.Schedule, error) {
	lowerSelection := func(selection projectcontracts.PipelineSelection) (string, error) {
		if strings.TrimSpace(selection.SemanticModel) == "" {
			return "", fmt.Errorf("selection.semanticModel is required")
		}
		return selection.SemanticModel, nil
	}
	switch variant := spec.Value.(type) {
	case *projectcontracts.ManualPipelineSpec:
		selection, err := lowerSelection(variant.Selection)
		return selection, "", 0, "", nil, err
	case *projectcontracts.ScheduledPipelineSpec:
		selection, err := lowerSelection(variant.Selection)
		if err != nil {
			return "", "", 0, "", nil, err
		}
		if len(variant.Schedules) == 0 {
			return "", "", 0, "", nil, fmt.Errorf("schedules must contain at least one schedule")
		}
		type authoredSchedule struct {
			id         string
			expression string
		}
		items := make([]authoredSchedule, 0, len(variant.Schedules))
		for id, expression := range variant.Schedules {
			canonical := strings.Join(strings.Fields(expression), " ")
			items = append(items, authoredSchedule{id: id, expression: canonical})
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].expression != items[j].expression {
				return items[i].expression < items[j].expression
			}
			return items[i].id < items[j].id
		})
		schedules := make([]refreshschedule.Schedule, 0, len(items))
		for _, item := range items {
			schedule, parseErr := refreshschedule.ParseSchedule(item.expression, variant.Timezone)
			if parseErr != nil {
				return "", "", 0, "", nil, fmt.Errorf("schedules[%q]: %w", item.id, parseErr)
			}
			schedule.ID = item.id
			schedules = append(schedules, schedule)
		}
		return selection, variant.Timezone, variant.StartingDeadlineSeconds, string(variant.ConcurrencyPolicy), schedules, nil
	case nil:
		return "", "", 0, "", nil, fmt.Errorf("spec variant is required")
	default:
		return "", "", 0, "", nil, fmt.Errorf("unsupported spec variant %T", spec.Value)
	}
}
