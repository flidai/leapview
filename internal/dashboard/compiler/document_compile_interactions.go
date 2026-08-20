package compiler

import (
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func canonicalConditionalFormatting(presentation document.DashboardPresentation, query LoweredDashboardQuery) (*[]visualizationir.VisualizationConditionalFormat, error) {
	if presentation.Value == nil {
		return nil, nil
	}
	base, err := presentation.Base()
	if err != nil {
		return nil, err
	}
	if base.ConditionalFormatting == nil {
		return nil, nil
	}
	result := make([]visualizationir.VisualizationConditionalFormat, 0, len(*base.ConditionalFormatting))
	for index, authored := range *base.ConditionalFormatting {
		field, err := canonicalResultRef(query, "primary", authored.Field)
		if err != nil {
			return nil, fmt.Errorf("entry %d field: %w", index, err)
		}
		compiled := visualizationir.VisualizationConditionalFormat{ID: authored.ID, Target: authored.Target, Field: field}
		switch rule := authored.Rule.Value.(type) {
		case *document.DashboardGradientConditionalRule:
			if rule == nil {
				return nil, fmt.Errorf("entry %d gradient rule is nil", index)
			}
			if !finiteDashboardFloat(rule.Minimum) || !finiteDashboardFloat(rule.Maximum) || rule.Minimum >= rule.Maximum {
				return nil, fmt.Errorf("entry %d gradient minimum must be finite and less than maximum", index)
			}
			compiled.Rule.Value = &visualizationir.GradientVisualizationConditionalRule{
				VisualizationConditionalRuleBase: visualizationir.VisualizationConditionalRuleBase{Kind: "gradient"},
				Kind:                             "gradient", Minimum: rule.Minimum, Maximum: rule.Maximum,
				Low: canonicalConditionalStyle(rule.Low), High: canonicalConditionalStyle(rule.High), NullStyle: canonicalConditionalStyle(rule.NullStyle),
			}
		case *document.DashboardRulesConditionalRule:
			if rule == nil {
				return nil, fmt.Errorf("entry %d rules rule is nil", index)
			}
			thresholds := make([]visualizationir.VisualizationConditionalThreshold, len(rule.Rules))
			for thresholdIndex, threshold := range rule.Rules {
				if !finiteDashboardFloat(threshold.Value) {
					return nil, fmt.Errorf("entry %d rule %d value must be finite", index, thresholdIndex)
				}
				thresholds[thresholdIndex] = visualizationir.VisualizationConditionalThreshold{Operator: threshold.Operator, Value: threshold.Value, Style: canonicalConditionalStyle(threshold.Style)}
			}
			compiled.Rule.Value = &visualizationir.RulesVisualizationConditionalRule{
				VisualizationConditionalRuleBase: visualizationir.VisualizationConditionalRuleBase{Kind: "rules"},
				Kind:                             "rules", Rules: thresholds, NullStyle: canonicalConditionalStyle(rule.NullStyle), DefaultStyle: canonicalConditionalStyle(rule.DefaultStyle),
			}
		case *document.DashboardFieldConditionalRule:
			if rule == nil {
				return nil, fmt.Errorf("entry %d field rule is nil", index)
			}
			source, err := canonicalResultRef(query, "primary", rule.Source)
			if err != nil {
				return nil, fmt.Errorf("entry %d source: %w", index, err)
			}
			values := make(map[string]visualizationir.VisualizationConditionalStyle, len(rule.Values))
			for value, style := range rule.Values {
				values[value] = canonicalConditionalStyle(style)
			}
			compiled.Rule.Value = &visualizationir.FieldVisualizationConditionalRule{
				VisualizationConditionalRuleBase: visualizationir.VisualizationConditionalRuleBase{Kind: "field"},
				Kind:                             "field", Source: source, Values: values, NullStyle: canonicalConditionalStyle(rule.NullStyle), DefaultStyle: canonicalConditionalStyle(rule.DefaultStyle),
			}
		default:
			return nil, fmt.Errorf("entry %d has unsupported rule %T", index, authored.Rule.Value)
		}
		result = append(result, compiled)
	}
	return &result, nil
}

func canonicalConditionalStyle(authored document.DashboardConditionalStyle) visualizationir.VisualizationConditionalStyle {
	return visualizationir.VisualizationConditionalStyle{Color: authored.Color, Icon: authored.Icon}
}

func canonicalSpatialInteractions(values *[]document.DashboardInteraction, query LoweredDashboardQuery) ([]visualizationir.VisualizationSpatialSelectionInteraction, error) {
	if values == nil {
		return []visualizationir.VisualizationSpatialSelectionInteraction{}, nil
	}
	result := make([]visualizationir.VisualizationSpatialSelectionInteraction, 0)
	for index, interaction := range *values {
		kind, err := interaction.Type()
		if err != nil {
			return nil, fmt.Errorf("interaction %d: %w", index, err)
		}
		if kind != "spatialSelection" {
			continue
		}
		value, ok := interaction.Value.(*document.SpatialSelectionDashboardInteraction)
		if !ok || value == nil {
			return nil, fmt.Errorf("interaction %d spatial selection variant is required", index)
		}
		lat, err := canonicalResultRef(query, "primary", value.Latitude.Source)
		if err != nil {
			return nil, fmt.Errorf("interaction %d latitude source: %w", index, err)
		}
		lon, err := canonicalResultRef(query, "primary", value.Longitude.Source)
		if err != nil {
			return nil, fmt.Errorf("interaction %d longitude source: %w", index, err)
		}
		if strings.TrimSpace(value.Latitude.Field) == "" || strings.TrimSpace(value.Longitude.Field) == "" {
			return nil, fmt.Errorf("interaction %d spatial target fields are required", index)
		}
		gestures := make([]visualizationir.VisualizationSpatialSelectionGesture, len(value.Gestures))
		for gestureIndex, gesture := range value.Gestures {
			gestures[gestureIndex] = visualizationir.VisualizationSpatialSelectionGesture(gesture)
		}
		targets := make([]visualizationir.VisualizationInteractionTarget, 0)
		appendTargets := func(ids *[]string, effect visualizationir.VisualizationInteractionEffect) {
			if ids == nil {
				return
			}
			for _, id := range *ids {
				targets = append(targets, visualizationir.VisualizationInteractionTarget{VisualID: id, Effect: effect})
			}
		}
		appendTargets(value.Targets, visualizationir.VisualizationInteractionEffectFilter)
		appendTargets(value.HighlightTargets, visualizationir.VisualizationInteractionEffectHighlight)
		appendTargets(value.NoneTargets, visualizationir.VisualizationInteractionEffectNone)
		result = append(result, visualizationir.VisualizationSpatialSelectionInteraction{
			ID: fmt.Sprintf("spatial-interaction-%d", index), Gestures: gestures,
			Latitude:  visualizationir.VisualizationSpatialFieldMapping{Source: lat, TargetFieldID: value.Latitude.Field, TargetDatasetID: value.Latitude.Dataset},
			Longitude: visualizationir.VisualizationSpatialFieldMapping{Source: lon, TargetFieldID: value.Longitude.Field, TargetDatasetID: value.Longitude.Dataset},
			Targets:   targets,
		})
	}
	return result, nil
}

func canonicalInteractions(values *[]document.DashboardInteraction, query LoweredDashboardQuery) ([]visualizationir.VisualizationInteraction, error) {
	if values == nil {
		return []visualizationir.VisualizationInteraction{}, nil
	}
	result := make([]visualizationir.VisualizationInteraction, 0, len(*values))
	for index := range *values {
		interaction := (*values)[index]
		kind, err := interaction.Type()
		if err != nil {
			return nil, fmt.Errorf("interaction %d: %w", index, err)
		}
		if kind == "spatialSelection" {
			continue
		}
		compiled := visualizationir.VisualizationInteraction{ID: fmt.Sprintf("interaction-%d", index), Kind: visualizationir.VisualizationInteractionKindSelect, Mappings: []visualizationir.VisualizationInteractionMapping{}, Targets: []visualizationir.VisualizationInteractionTarget{}, Mode: visualizationir.VisualizationSelectionModeSingle}
		if kind == "selection" {
			value := interaction.Value.(*document.SelectionDashboardInteraction)
			compiled.Mode = visualizationir.VisualizationSelectionMode(value.Mode)
			compiled.RequiresStableIdentity = value.Toggle
			for mappingIndex, mapping := range value.Mappings {
				// `field` is the emitted source/result field. `value` is the
				// semantic/physical target identity and `dataset` qualifies that
				// target scope; never resolve the source through the target dataset.
				source, refErr := canonicalResultRef(query, "primary", mapping.Field)
				if refErr != nil {
					return nil, fmt.Errorf("interaction %d mapping %d: %w", index, mappingIndex, refErr)
				}
				if strings.TrimSpace(mapping.Value) == "" {
					return nil, fmt.Errorf("interaction %d mapping %d target value is required", index, mappingIndex)
				}
				label := (*visualizationir.VisualizationFieldRef)(nil)
				if mapping.Label != nil {
					labelRef, labelErr := canonicalResultRef(query, "primary", *mapping.Label)
					if labelErr != nil {
						return nil, fmt.Errorf("interaction %d label %d: %w", index, mappingIndex, labelErr)
					}
					label = &labelRef
				}
				targetDataset := (*string)(nil)
				if mapping.Dataset != nil {
					targetDataset = mapping.Dataset
				}
				grain := (*string)(nil)
				if mapping.Grain != nil {
					value := string(*mapping.Grain)
					grain = &value
				}
				compiled.Mappings = append(compiled.Mappings, visualizationir.VisualizationInteractionMapping{Source: source, TargetFieldID: mapping.Value, TargetDatasetID: targetDataset, Grain: grain, Label: label})
			}
			targets := []string(nil)
			if value.Targets != nil {
				targets = append(targets, (*value.Targets)...)
			}
			for _, target := range targets {
				compiled.Targets = append(compiled.Targets, visualizationir.VisualizationInteractionTarget{VisualID: target, Effect: visualizationir.VisualizationInteractionEffectFilter})
			}
			if value.HighlightTargets != nil {
				for _, target := range *value.HighlightTargets {
					compiled.Targets = append(compiled.Targets, visualizationir.VisualizationInteractionTarget{VisualID: target, Effect: visualizationir.VisualizationInteractionEffectHighlight})
				}
			}
			if value.NoneTargets != nil {
				for _, target := range *value.NoneTargets {
					compiled.Targets = append(compiled.Targets, visualizationir.VisualizationInteractionTarget{VisualID: target, Effect: visualizationir.VisualizationInteractionEffectNone})
				}
			}
		} else {
			value := interaction.Value.(*document.SpatialSelectionDashboardInteraction)
			lat, latErr := canonicalResultRef(query, "primary", value.Latitude.Source)
			if latErr != nil {
				return nil, fmt.Errorf("interaction %d latitude source: %w", index, latErr)
			}
			lon, lonErr := canonicalResultRef(query, "primary", value.Longitude.Source)
			if lonErr != nil {
				return nil, fmt.Errorf("interaction %d longitude source: %w", index, lonErr)
			}
			if strings.TrimSpace(value.Latitude.Field) == "" || strings.TrimSpace(value.Longitude.Field) == "" {
				return nil, fmt.Errorf("interaction %d spatial target fields are required", index)
			}
			compiled.Mappings = append(compiled.Mappings,
				visualizationir.VisualizationInteractionMapping{Source: lat, TargetFieldID: value.Latitude.Field, TargetDatasetID: value.Latitude.Dataset},
				visualizationir.VisualizationInteractionMapping{Source: lon, TargetFieldID: value.Longitude.Field, TargetDatasetID: value.Longitude.Dataset})
			appendTargets := func(targets *[]string, effect visualizationir.VisualizationInteractionEffect) {
				if targets == nil {
					return
				}
				for _, target := range *targets {
					compiled.Targets = append(compiled.Targets, visualizationir.VisualizationInteractionTarget{VisualID: target, Effect: effect})
				}
			}
			appendTargets(value.Targets, visualizationir.VisualizationInteractionEffectFilter)
			appendTargets(value.HighlightTargets, visualizationir.VisualizationInteractionEffectHighlight)
			appendTargets(value.NoneTargets, visualizationir.VisualizationInteractionEffectNone)
		}
		result = append(result, compiled)
	}
	return result, nil
}

func promoteSelectionIdentityFields(base *visualizationir.VisualizationSpecBase) error {
	// Selection datum refs and renderer projection use the compiled mapping
	// sources as their identity tuple. Preserve that invariant even when the
	// authored source is otherwise a dimension or metric.
	for _, interaction := range base.Interactions {
		for _, mapping := range interaction.Mappings {
			datasetIndex := -1
			for index := range base.Datasets {
				if base.Datasets[index].ID == mapping.Source.Dataset {
					datasetIndex = index
					break
				}
			}
			if datasetIndex < 0 {
				return fmt.Errorf("interaction %q references unknown dataset %q", interaction.ID, mapping.Source.Dataset)
			}
			fieldIndex := -1
			for index := range base.Datasets[datasetIndex].Fields {
				if base.Datasets[datasetIndex].Fields[index].ID == mapping.Source.Field {
					fieldIndex = index
					break
				}
			}
			if fieldIndex < 0 {
				return fmt.Errorf("interaction %q references unknown field %q in dataset %q", interaction.ID, mapping.Source.Field, mapping.Source.Dataset)
			}
			base.Datasets[datasetIndex].Fields[fieldIndex].Role = visualizationir.VisualizationFieldRoleIdentity
		}
	}
	return nil
}
