package authoring

import (
	"fmt"
	"math"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/document"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func validateAppendExplorationVisualPayload(value AppendExplorationVisualPayload) error {
	for kind, id := range map[string]string{
		"page id": value.PageID, "visual id": value.VisualID, "component id": value.ComponentID,
	} {
		if err := validateCanonicalObjectID(kind, id); err != nil {
			return err
		}
	}
	semanticModel := strings.TrimSpace(value.SemanticModel)
	if semanticModel == "" || semanticModel != value.SemanticModel {
		return fmt.Errorf("%w: append exploration semantic model is required and cannot have surrounding whitespace", ErrInvalidPayload)
	}
	if err := projectgraph.ResourceID(semanticModel).Validate(); err != nil {
		return fmt.Errorf("%w: append exploration semantic model: %v", ErrInvalidPayload, err)
	}
	if value.Placement.Column < 1 || value.Placement.Row < 1 || value.Placement.ColumnSpan < 1 || value.Placement.RowSpan < 1 {
		return fmt.Errorf("%w: append exploration placement must have positive column, row, and spans", ErrInvalidPayload)
	}
	// Placement coordinates are int32 on the wire. Validate the half-open
	// rectangle endpoints before reducer overlap arithmetic so a hostile
	// near-MaxInt32 placement cannot wrap around and evade collision checks.
	if int64(value.Placement.Column)+int64(value.Placement.ColumnSpan) > math.MaxInt32 ||
		int64(value.Placement.Row)+int64(value.Placement.RowSpan) > math.MaxInt32 {
		return fmt.Errorf("%w: append exploration placement exceeds layout coordinate bounds", ErrInvalidPayload)
	}
	if !canonicalVisualTypeSupported(value.Visual.Type) {
		return fmt.Errorf("%w: unsupported appended visual type %q", ErrInvalidPayload, value.Visual.Type)
	}
	if value.Visual.Query.Value == nil || value.Visual.Presentation.Value == nil {
		return fmt.Errorf("%w: appended visual query and presentation are required", ErrInvalidPayload)
	}
	seenFilters := make(map[string]struct{}, len(value.Filters))
	for index, filter := range value.Filters {
		if err := validateCanonicalObjectID(fmt.Sprintf("filter %d id", index), filter.ID); err != nil {
			return err
		}
		if _, exists := seenFilters[filter.ID]; exists {
			return fmt.Errorf("%w: appended filter id %q is duplicated", ErrConflict, filter.ID)
		}
		seenFilters[filter.ID] = struct{}{}
		if filter.Targets == nil || len(*filter.Targets) != 1 || (*filter.Targets)[0] != value.VisualID {
			return fmt.Errorf("%w: appended filter %q must target only visual %q", ErrInvalidPayload, filter.ID, value.VisualID)
		}
	}
	return nil
}

// CandidateAppendExplorationDocument applies the same closed append reducer
// used by ApplyEdit to a detached document. The application seam uses this
// candidate for active-model/compiler validation before persistence.
func CandidateAppendExplorationDocument(current document.DashboardDocument, payload AppendExplorationVisualPayload) (document.DashboardDocument, error) {
	if err := validateAppendExplorationVisualPayload(payload); err != nil {
		return document.DashboardDocument{}, err
	}
	candidate, err := current.Clone()
	if err != nil {
		return document.DashboardDocument{}, err
	}
	if err := appendCanonicalExplorationVisual(&candidate, payload); err != nil {
		return document.DashboardDocument{}, err
	}
	if err := ValidateCanonicalDocument(candidate); err != nil {
		return document.DashboardDocument{}, err
	}
	return candidate, nil
}

func appendCanonicalExplorationVisual(value *document.DashboardDocument, patch AppendExplorationVisualPayload) error {
	if err := validateAppendExplorationVisualPayload(patch); err != nil {
		return err
	}
	pageIndex := -1
	for index, page := range value.Spec.Pages {
		if page.ID == patch.PageID {
			pageIndex = index
			break
		}
	}
	if pageIndex < 0 {
		return fmt.Errorf("%w: page %q", ErrNotFound, patch.PageID)
	}
	if value.Spec.SemanticModel != patch.SemanticModel {
		return fmt.Errorf("%w: appended exploration semantic model %q does not match dashboard semantic model %q", ErrConflict, patch.SemanticModel, value.Spec.SemanticModel)
	}
	if _, exists := value.Spec.Visuals[patch.VisualID]; exists {
		return fmt.Errorf("%w: visual %q already exists", ErrConflict, patch.VisualID)
	}
	for _, page := range value.Spec.Pages {
		for _, component := range page.Components {
			base, err := component.Base()
			if err != nil {
				return err
			}
			if base.ID == patch.ComponentID {
				return fmt.Errorf("%w: component %q already exists", ErrConflict, patch.ComponentID)
			}
		}
	}
	for _, filter := range value.Spec.Filters {
		for _, appended := range patch.Filters {
			if filter.ID == appended.ID {
				return fmt.Errorf("%w: filter %q already exists", ErrConflict, appended.ID)
			}
		}
	}
	page := value.Spec.Pages[pageIndex]
	for _, component := range page.Components {
		base, err := component.Base()
		if err != nil {
			return err
		}
		if placementsOverlap(patch.Placement, base.Placement) {
			return fmt.Errorf("%w: placement for component %q overlaps component %q", ErrConflict, patch.ComponentID, base.ID)
		}
	}
	if value.Spec.Visuals == nil {
		value.Spec.Visuals = make(map[string]document.DashboardVisual)
	}
	value.Spec.Visuals[patch.VisualID] = patch.Visual
	value.Spec.Pages[pageIndex].Components = append(value.Spec.Pages[pageIndex].Components, document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{
		DashboardPageComponentBase: document.DashboardPageComponentBase{ID: patch.ComponentID, Type: "visual", Placement: patch.Placement},
		Type:                       "visual", Visual: patch.VisualID,
	}})
	value.Spec.Filters = append(value.Spec.Filters, patch.Filters...)
	return nil
}
