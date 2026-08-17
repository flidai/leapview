package authoring

import (
	"fmt"
	"strings"
)

// ValidateDraftStructure validates the identity and referential structure of
// an authored draft document without requiring a compiler-ready visualization.
//
// Drafts are allowed to carry empty visual collections, empty page placement,
// and incomplete query or presentation details. Those semantic diagnostics are
// intentionally deferred to strict ValidateContract and the dashboard
// compiler. This boundary only protects the document from losing its stable
// identity or becoming internally dangling/corrupt while it is edited.
func (d *Dashboard) ValidateDraftStructure() error {
	if d == nil {
		return fmt.Errorf("dashboard draft is nil")
	}
	if !d.ID.Valid() || strings.TrimSpace(d.Title) == "" {
		return fmt.Errorf("dashboard draft requires id and title")
	}
	if strings.TrimSpace(d.SemanticModel.String()) == "" {
		return fmt.Errorf("dashboard draft %q requires semantic_model", d.ID)
	}
	if len(d.Pages) == 0 {
		return fmt.Errorf("dashboard draft %q requires at least one page", d.ID)
	}

	if err := d.validateDraftVisuals(); err != nil {
		return err
	}
	return d.validateDraftPages()
}

func (d *Dashboard) validateDraftVisuals() error {
	for id, authored := range d.Visuals {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("dashboard draft visual id is required")
		}
		if (authored.Chart == nil) == (authored.Tabular == nil) {
			return fmt.Errorf("visual %q must contain exactly one authoring variant", id)
		}
		if strings.TrimSpace(authored.Type) == "" {
			return fmt.Errorf("visual %q requires type", id)
		}
		if authored.Chart != nil {
			if strings.TrimSpace(authored.Chart.Type) == "" {
				return fmt.Errorf("visual %q requires chart type", id)
			}
			if authored.Chart.Type != authored.Type {
				return fmt.Errorf("visual %q chart type %q does not match visualization type %q", id, authored.Chart.Type, authored.Type)
			}
			if capability, ok := VisualizationCapabilityForType(authored.Type); ok && capability.Kind == "grid" {
				return fmt.Errorf("visual %q tabular type %q requires tabular variant", id, authored.Type)
			}
			if err := d.validateDraftInteraction(id, authored.Chart.Interaction); err != nil {
				return err
			}
			continue
		}
		if authored.Type != "table" && authored.Type != "matrix" && authored.Type != "pivot" {
			return fmt.Errorf("visual %q has unsupported tabular type %q", id, authored.Type)
		}
		if err := d.validateDraftInteraction(id, authored.Tabular.Interaction); err != nil {
			return err
		}
	}
	return nil
}

func (d *Dashboard) validateDraftInteraction(visualID string, interaction Interaction) error {
	validateTargets := func(kind string, targets []string) error {
		for _, target := range targets {
			if strings.TrimSpace(target) == "" {
				return fmt.Errorf("visual %q %s has empty target", visualID, kind)
			}
			if _, ok := d.Visuals[target]; !ok {
				return fmt.Errorf("visual %q %s references unknown target %q", visualID, kind, target)
			}
		}
		return nil
	}
	for _, item := range []struct {
		kind      string
		selection SelectionInteraction
	}{
		{kind: "point_selection", selection: interaction.PointSelection},
		{kind: "row_selection", selection: interaction.RowSelection},
	} {
		selection := item.selection
		if err := validateTargets(item.kind, append(append(slicesClone(selection.Targets), selection.HighlightTargets...), selection.NoneTargets...)); err != nil {
			return err
		}
	}
	return validateTargets("spatial_selection", append(append(slicesClone(interaction.SpatialSelection.Targets), interaction.SpatialSelection.HighlightTargets...), interaction.SpatialSelection.NoneTargets...))
}

func (d *Dashboard) validateDraftPages() error {
	seenPages := make(map[string]struct{}, len(d.Pages))
	for index, page := range d.Pages {
		if strings.TrimSpace(page.ID) == "" {
			return fmt.Errorf("page %d requires id", index)
		}
		if _, exists := seenPages[page.ID]; exists {
			return fmt.Errorf("duplicate page id %q", page.ID)
		}
		seenPages[page.ID] = struct{}{}

		page = page.WithDefaults()
		seenComponents := make(map[string]struct{}, len(page.Visuals))
		for _, component := range page.Visuals {
			if strings.TrimSpace(component.ID) == "" {
				return fmt.Errorf("page %q has a visual missing id", page.ID)
			}
			if _, exists := seenComponents[component.ID]; exists {
				return fmt.Errorf("page %q has duplicate component %q", page.ID, component.ID)
			}
			seenComponents[component.ID] = struct{}{}
			if !component.Placement.IsZero() {
				if err := validatePlacement(page, component); err != nil {
					return err
				}
			}

			switch component.Kind {
			case "header":
				if component.Visual != "" || component.Binding.ID != "" {
					return fmt.Errorf("page %q header %q must not reference a visual or filter binding", page.ID, component.ID)
				}
			case "slicer":
				if component.Visual != "" {
					return fmt.Errorf("page %q slicer %q must not reference a visual", page.ID, component.ID)
				}
				if component.Binding.ID == "" || !d.bindingReferenceExists(page.ID, component.Binding) {
					return fmt.Errorf("page %q slicer %q references unknown filter binding %s/%s", page.ID, component.ID, component.Binding.Scope, component.Binding.ID)
				}
			case "visual":
				if component.Visual == "" {
					return fmt.Errorf("page %q visual %q requires visual", page.ID, component.ID)
				}
				if _, ok := d.Visuals[component.Visual]; !ok {
					return fmt.Errorf("page %q references unknown visual %q", page.ID, component.Visual)
				}
				if component.Binding.ID != "" {
					return fmt.Errorf("page %q visual %q must not reference a filter binding", page.ID, component.ID)
				}
			default:
				return fmt.Errorf("page %q visual %q has unsupported kind %q", page.ID, component.ID, component.Kind)
			}
		}
		for id := range page.FilterBindings {
			if strings.TrimSpace(id) == "" {
				return fmt.Errorf("page %q filter binding id is required", page.ID)
			}
		}
	}
	for id := range d.FilterBindings {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("dashboard filter binding id is required")
		}
	}
	return nil
}

// slicesClone is deliberately local to this structural validator so it does
// not introduce a dependency on a newer standard-library slices package in
// the authoring contract's public surface.
func slicesClone(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}
