package authoring

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/project/graph"
)

// ApplyEdit applies one validated authoring edit to an immutable canonical
// DashboardDocument revision.  The reducer deliberately does not project
// through the retired dashboard authoring object: every persisted revision is
// cloned from and validated as the generated DTO.
func ApplyEdit(lifecycle DashboardLifecycle, current Revision, command Command, nextRevisionID RevisionID, nextNumber uint64, nextCreatedAt time.Time) (DashboardLifecycle, Revision, error) {
	if err := lifecycle.Validate(); err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}
	if err := current.Validate(); err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}
	if current.DashboardID != lifecycle.ID || lifecycle.Draft == nil || !sameRevisionToken(lifecycle.Draft.Revision, current.Token()) {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: lifecycle does not select current revision", ErrStaleRevision)
	}
	if err := command.Validate(); err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}
	if command.DashboardID != lifecycle.ID || command.DraftID != lifecycle.Draft.ID || !sameRevisionToken(command.ExpectedRevision, current.Token()) {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: command does not select current revision", ErrStaleRevision)
	}
	if nextNumber != current.Number+1 {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: next revision number must be current number + 1", ErrInvalidAuthoring)
	}
	payload, _ := command.payloadValue()
	switch payload.(type) {
	case *PublishPayload, *ArchivePayload, *RestoreRevisionPayload:
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: lifecycle operations are not edit commands", ErrInvalidPayload)
	}
	authored, err := current.Document.Clone()
	if err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}
	if err := applyCanonicalPayload(&authored, payload); err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}
	if err := ValidateCanonicalDocument(authored); err != nil {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: resulting dashboard: %v", ErrInvalidPayload, err)
	}
	hash, err := DashboardContentHash(authored)
	if err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}
	if command.ContentHash != "" && command.ContentHash != hash {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: expected resulting content hash %q, got %q", ErrConflict, command.ContentHash, hash)
	}
	revision, err := NewRevision(nextRevisionID, current.DashboardID, nextNumber, nextCreatedAt, authored, command.Provenance)
	if err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}
	next := lifecycle
	next.Title = canonicalDocumentTitle(authored)
	next.SemanticModel = resourceID(authored.Spec.SemanticModel)
	if metadata, ok := payload.(*MetadataPatch); ok {
		if metadata.Slug != nil {
			next.Slug = strings.TrimSpace(*metadata.Slug)
			if next.Slug == "" {
				return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: slug cannot be cleared", ErrInvalidPayload)
			}
		}
		if metadata.Visibility != nil {
			next.Visibility = *metadata.Visibility
		}
	}
	if visibility, ok := payload.(*SetVisibilityPayload); ok {
		next.Visibility = visibility.Visibility
	}
	next.Draft = &Draft{ID: lifecycle.Draft.ID, DashboardID: lifecycle.ID, Revision: revision.Token(), Provenance: command.Provenance.Clone()}
	if err := next.Validate(); err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}
	return next, revision, nil
}

// ApplyRevisionRestore appends a new draft revision whose document is cloned
// from an exact retained revision. This gives interactive clients undo/redo
// without rewriting history or weakening optimistic concurrency.
func ApplyRevisionRestore(lifecycle DashboardLifecycle, current, target Revision, command Command, nextRevisionID RevisionID, nextNumber uint64, nextCreatedAt time.Time) (DashboardLifecycle, Revision, error) {
	if err := lifecycle.Validate(); err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}
	if err := current.Validate(); err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}
	if err := target.Validate(); err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}
	if err := command.Validate(); err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}
	if current.DashboardID != lifecycle.ID || lifecycle.Draft == nil || !sameRevisionToken(lifecycle.Draft.Revision, current.Token()) {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: lifecycle does not select current revision", ErrStaleRevision)
	}
	if command.DashboardID != lifecycle.ID || command.DraftID != lifecycle.Draft.ID || !sameRevisionToken(command.ExpectedRevision, current.Token()) {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: command does not select current revision", ErrStaleRevision)
	}
	if command.RestoreRevision == nil || !sameRevisionToken(command.RestoreRevision.TargetRevision, target.Token()) {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: restore command does not select target revision", ErrStaleRevision)
	}
	if target.DashboardID != current.DashboardID || target.Number >= current.Number {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: restore target must be an earlier revision of this dashboard", ErrInvalidPayload)
	}
	if nextNumber != current.Number+1 {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: next revision number must be current number + 1", ErrInvalidAuthoring)
	}
	authored, err := target.Document.Clone()
	if err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}
	if err := ValidateCanonicalDocument(authored); err != nil {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: restored dashboard: %v", ErrInvalidPayload, err)
	}
	if command.ContentHash != "" && command.ContentHash != target.ContentHash {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: expected resulting content hash %q, got %q", ErrConflict, command.ContentHash, target.ContentHash)
	}
	revision, err := NewRevision(nextRevisionID, current.DashboardID, nextNumber, nextCreatedAt, authored, command.Provenance)
	if err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}
	next := lifecycle
	next.Title = canonicalDocumentTitle(authored)
	next.SemanticModel = resourceID(authored.Spec.SemanticModel)
	next.Draft = &Draft{ID: lifecycle.Draft.ID, DashboardID: lifecycle.ID, Revision: revision.Token(), Provenance: command.Provenance.Clone()}
	if err := next.Validate(); err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}
	return next, revision, nil
}

func applyCanonicalPayload(value *document.DashboardDocument, payload authoringPayload) error {
	switch patch := payload.(type) {
	case *MetadataPatch:
		if patch.Title != nil {
			title := strings.TrimSpace(*patch.Title)
			if title == "" {
				return fmt.Errorf("%w: title cannot be empty", ErrInvalidPayload)
			}
			value.Metadata.DisplayName = &title
		}
		if patch.Description != nil {
			value.Metadata.Description = patch.Description
		}
		if patch.SemanticModel != nil {
			value.Spec.SemanticModel = strings.TrimSpace(*patch.SemanticModel)
		}
		if patch.Appearance != nil {
			value.Spec.Appearance = patch.Appearance
		}
		return nil
	case *SetVisibilityPayload:
		return nil
	case *AddPagePayload:
		if len(value.Spec.Pages) >= maxAuthoringPages {
			return fmt.Errorf("%w: dashboard builder pages exceed bounded limit", ErrInvalidPayload)
		}
		id := strings.TrimSpace(patch.PageID)
		if id == "" {
			id = nextCanonicalBuilderID("page", len(value.Spec.Pages)+1, func(candidate string) bool {
				for _, page := range value.Spec.Pages {
					if page.ID == candidate {
						return true
					}
				}
				return false
			})
		}
		for _, page := range value.Spec.Pages {
			if page.ID == id {
				return fmt.Errorf("%w: page %q already exists", ErrConflict, id)
			}
		}
		title := strings.TrimSpace(patch.Title)
		if title == "" {
			title = id
		}
		value.Spec.Pages = append(value.Spec.Pages, document.DashboardPage{ID: id, Title: title, Components: []document.DashboardPageComponent{}})
		return nil
	case *RenamePagePayload:
		return renameCanonicalPage(value, *patch)
	case *DuplicatePagePayload:
		return duplicateCanonicalPage(value, *patch)
	case *MovePagePayload:
		return moveCanonicalPage(value, *patch)
	case *UpdatePageLayoutPayload:
		return updateCanonicalPageLayout(value, *patch)
	case *RemovePagePayload:
		for index, page := range value.Spec.Pages {
			if page.ID == patch.PageID {
				if len(value.Spec.Pages) <= 1 {
					return fmt.Errorf("%w: cannot remove the last page", ErrInvalidPayload)
				}
				removedVisuals := canonicalRemovedPageVisuals(page)
				removedBindings := map[string]struct{}{}
				if page.FilterBindings != nil {
					for _, binding := range *page.FilterBindings {
						removedBindings[binding.Filter] = struct{}{}
					}
				}
				value.Spec.Pages = append(value.Spec.Pages[:index], value.Spec.Pages[index+1:]...)
				for _, removed := range removedVisuals {
					if !canonicalVisualReferenced(value, removed.definitionID) {
						delete(value.Spec.Visuals, removed.definitionID)
					}
				}
				pruneCanonicalFilterTargetsAfterVisualRemoval(value, page.ID, removedVisuals, removedBindings)
				return nil
			}
		}
		return fmt.Errorf("%w: page %q", ErrNotFound, patch.PageID)
	case *AddVisualPayload:
		return addCanonicalVisual(value, *patch)
	case *SetPlacementsPayload:
		return setCanonicalPlacements(value, *patch)
	case *AssignFieldPayload:
		return assignCanonicalField(value, *patch)
	case *SetVisualTypePayload:
		return setCanonicalVisualType(value, *patch)
	case *RenameVisualPayload:
		return renameCanonicalVisual(value, *patch)
	case *DuplicateVisualPayload:
		return duplicateCanonicalVisual(value, *patch)
	case *UpdateVisualFormatPayload:
		return updateCanonicalVisualFormat(value, *patch)
	case *RemoveFieldPayload:
		return removeCanonicalField(value, *patch)
	case *MoveFieldPayload:
		return moveCanonicalField(value, *patch)
	case *UpsertPagePayload:
		for index := range value.Spec.Pages {
			if value.Spec.Pages[index].ID == patch.Page.ID {
				value.Spec.Pages[index] = patch.Page
				return nil
			}
		}
		value.Spec.Pages = append(value.Spec.Pages, patch.Page)
		return nil
	case *UpsertVisualPayload:
		if value.Spec.Visuals == nil {
			value.Spec.Visuals = map[string]document.DashboardVisual{}
		}
		value.Spec.Visuals[patch.VisualID] = patch.Visual
		return nil
	case *RemoveVisualPayload:
		return removeCanonicalVisual(value, *patch)
	case *SetLayoutPayload:
		for index := range value.Spec.Pages {
			if value.Spec.Pages[index].ID == patch.PageID {
				value.Spec.Pages[index].Layout = patch.Layout
				return nil
			}
		}
		return fmt.Errorf("%w: page %q", ErrNotFound, patch.PageID)
	case *SetFiltersPayload:
		if patch.Clear {
			value.Spec.Filters = []document.DashboardFilter{}
		} else {
			value.Spec.Filters = append([]document.DashboardFilter(nil), patch.Filters...)
		}
		pruneCanonicalFilterComponents(value)
		return nil
	case *AddFilterPayload:
		return addCanonicalFilter(value, *patch)
	case *UpdateFilterPayload:
		return updateCanonicalFilter(value, *patch)
	case *SetFilterTargetsPayload:
		return setCanonicalFilterTargets(value, *patch)
	case *SetFilterScopePayload:
		return setCanonicalFilterScope(value, *patch)
	case *RemoveFilterPayload:
		return removeCanonicalFilter(value, *patch)
	case *AddFilterComponentPayload:
		return addCanonicalFilterComponent(value, *patch)
	case *RemoveFilterComponentPayload:
		return removeCanonicalFilterComponent(value, *patch)
	case *SetInteractionPayload:
		visualID, err := resolveCanonicalInteractionVisual(*value, patch.PageID, patch.VisualID)
		if err != nil {
			return err
		}
		visual := value.Spec.Visuals[visualID]
		if patch.Clear || patch.Interaction == nil {
			visual.Interactions = nil
		} else {
			interactions := []document.DashboardInteraction{*patch.Interaction}
			visual.Interactions = &interactions
		}
		value.Spec.Visuals[visualID] = visual
		return nil
	case *SetInteractionTargetPayload:
		return setCanonicalInteractionTarget(value, *patch)
	default:
		return fmt.Errorf("%w: unsupported payload %T", ErrInvalidPayload, payload)
	}
}

func renameCanonicalPage(value *document.DashboardDocument, patch RenamePagePayload) error {
	for index := range value.Spec.Pages {
		if value.Spec.Pages[index].ID != patch.PageID {
			continue
		}
		title := strings.TrimSpace(patch.Title)
		value.Spec.Pages[index].Title = title
		return nil
	}
	return fmt.Errorf("%w: page %q", ErrNotFound, patch.PageID)
}

func duplicateCanonicalPage(value *document.DashboardDocument, patch DuplicatePagePayload) error {
	sourceIndex := -1
	for index := range value.Spec.Pages {
		if value.Spec.Pages[index].ID == patch.PageID {
			sourceIndex = index
			break
		}
	}
	if sourceIndex < 0 {
		return fmt.Errorf("%w: page %q", ErrNotFound, patch.PageID)
	}
	if len(value.Spec.Pages) >= maxAuthoringPages {
		return fmt.Errorf("%w: dashboard builder pages exceed bounded limit", ErrInvalidPayload)
	}
	visualCount := canonicalVisualComponentCount(*value)
	sourceVisualCount := canonicalPageVisualComponentCount(value.Spec.Pages[sourceIndex])
	if sourceVisualCount > maxAuthoringVisualComponents-visualCount {
		return fmt.Errorf("%w: dashboard builder visuals exceed bounded limit", ErrInvalidPayload)
	}
	filterComponentCount := canonicalFilterComponentCount(*value)
	sourceFilterComponentCount := canonicalPageFilterComponentCount(value.Spec.Pages[sourceIndex])
	if sourceFilterComponentCount > maxAuthoringFilterComponents-filterComponentCount {
		return fmt.Errorf("%w: dashboard builder filter components exceed bounded limit", ErrInvalidPayload)
	}

	newPageID := strings.TrimSpace(patch.NewPageID)
	if newPageID == "" {
		newPageID = nextCanonicalBuilderID("page", len(value.Spec.Pages)+1, func(candidate string) bool {
			for _, page := range value.Spec.Pages {
				if page.ID == candidate {
					return true
				}
			}
			return false
		})
	}
	for _, page := range value.Spec.Pages {
		if page.ID == newPageID {
			return fmt.Errorf("%w: page %q already exists", ErrConflict, newPageID)
		}
	}

	// JSON round-tripping is the canonical deep-copy boundary for generated
	// document unions and pointer-owned nested values (filter bindings,
	// component variants, and visual definitions).
	encoded, err := json.Marshal(value.Spec.Pages[sourceIndex])
	if err != nil {
		return fmt.Errorf("%w: clone page: %v", ErrInvalidPayload, err)
	}
	var clone document.DashboardPage
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return fmt.Errorf("%w: clone page: %v", ErrInvalidPayload, err)
	}
	clone.ID = newPageID
	if title := strings.TrimSpace(patch.Title); title != "" {
		clone.Title = title
	}

	if value.Spec.Visuals == nil {
		value.Spec.Visuals = make(map[string]document.DashboardVisual)
	}
	clonedVisualIDs := make(map[string]string)
	for index, component := range clone.Components {
		visualComponent, ok := component.Value.(*document.VisualDashboardPageComponent)
		if !ok || strings.TrimSpace(visualComponent.Visual) == "" {
			continue
		}
		sourceVisualID := visualComponent.Visual
		newVisualID, alreadyCloned := clonedVisualIDs[sourceVisualID]
		if !alreadyCloned {
			sourceVisual, exists := value.Spec.Visuals[sourceVisualID]
			if !exists {
				return fmt.Errorf("%w: visual %q", ErrNotFound, sourceVisualID)
			}
			visualEncoded, encodeErr := json.Marshal(sourceVisual)
			if encodeErr != nil {
				return fmt.Errorf("%w: clone visual %q: %v", ErrInvalidPayload, sourceVisualID, encodeErr)
			}
			var visualClone document.DashboardVisual
			if decodeErr := json.Unmarshal(visualEncoded, &visualClone); decodeErr != nil {
				return fmt.Errorf("%w: clone visual %q: %v", ErrInvalidPayload, sourceVisualID, decodeErr)
			}
			newVisualID = nextCanonicalBuilderID("visual", len(value.Spec.Visuals)+1, func(candidate string) bool {
				_, exists := value.Spec.Visuals[candidate]
				return exists
			})
			value.Spec.Visuals[newVisualID] = visualClone
			clonedVisualIDs[sourceVisualID] = newVisualID
		}
		clonedComponent, componentOK := clone.Components[index].Value.(*document.VisualDashboardPageComponent)
		if !componentOK {
			return fmt.Errorf("%w: clone visual component %q", ErrInvalidPayload, visualComponent.Visual)
		}
		clonedComponent.Visual = newVisualID
	}

	// Insert directly after the source while preserving authored page order.
	pages := make([]document.DashboardPage, 0, len(value.Spec.Pages)+1)
	pages = append(pages, value.Spec.Pages[:sourceIndex+1]...)
	pages = append(pages, clone)
	pages = append(pages, value.Spec.Pages[sourceIndex+1:]...)
	value.Spec.Pages = pages
	return nil
}

func canonicalVisualComponentCount(value document.DashboardDocument) int {
	count := 0
	for _, page := range value.Spec.Pages {
		count += canonicalPageVisualComponentCount(page)
	}
	return count
}

func canonicalPageVisualComponentCount(page document.DashboardPage) int {
	count := 0
	for _, component := range page.Components {
		if _, ok := component.Value.(*document.VisualDashboardPageComponent); ok {
			count++
		}
	}
	return count
}

func canonicalFilterComponentCount(value document.DashboardDocument) int {
	count := 0
	for _, page := range value.Spec.Pages {
		count += canonicalPageFilterComponentCount(page)
	}
	return count
}

func canonicalPageFilterComponentCount(page document.DashboardPage) int {
	count := 0
	for _, component := range page.Components {
		if _, ok := component.Value.(*document.FilterDashboardPageComponent); ok {
			count++
		}
	}
	return count
}

func moveCanonicalPage(value *document.DashboardDocument, patch MovePagePayload) error {
	sourceIndex := -1
	for index := range value.Spec.Pages {
		if value.Spec.Pages[index].ID == patch.PageID {
			sourceIndex = index
			break
		}
	}
	if sourceIndex < 0 {
		return fmt.Errorf("%w: page %q", ErrNotFound, patch.PageID)
	}
	if patch.Index < 0 || patch.Index >= len(value.Spec.Pages) {
		return fmt.Errorf("%w: page index %d is outside 0..%d", ErrInvalidPayload, patch.Index, len(value.Spec.Pages)-1)
	}
	if sourceIndex == patch.Index {
		return nil
	}
	page := value.Spec.Pages[sourceIndex]
	pages := append(value.Spec.Pages[:sourceIndex], value.Spec.Pages[sourceIndex+1:]...)
	// The requested index is the final zero-based position. After removal the
	// slice has one fewer item, so index len(pages) intentionally appends.
	if patch.Index == len(pages) {
		pages = append(pages, page)
	} else {
		pages = append(pages, document.DashboardPage{})
		copy(pages[patch.Index+1:], pages[patch.Index:])
		pages[patch.Index] = page
	}
	value.Spec.Pages = pages
	return nil
}

func updateCanonicalPageLayout(value *document.DashboardDocument, patch UpdatePageLayoutPayload) error {
	pageIndex := -1
	for index := range value.Spec.Pages {
		if value.Spec.Pages[index].ID == patch.PageID {
			pageIndex = index
			break
		}
	}
	if pageIndex < 0 {
		return fmt.Errorf("%w: page %q", ErrNotFound, patch.PageID)
	}
	columns := int64(patch.Columns)
	page := &value.Spec.Pages[pageIndex]
	for componentIndex, component := range page.Components {
		base, err := component.Base()
		if err != nil || base == nil {
			if err == nil {
				err = errors.New("component base is empty")
			}
			return fmt.Errorf("%w: page layout component %d: %v", ErrInvalidPayload, componentIndex, err)
		}
		if err := validatePlacementCoordinates(base.Placement); err != nil {
			return fmt.Errorf("%w: page layout component %q: %v", ErrInvalidPayload, base.ID, err)
		}
		columnEnd := int64(base.Placement.Column) + int64(base.Placement.ColumnSpan) - 1
		if int64(base.Placement.Column) > columns || columnEnd > columns {
			return fmt.Errorf("%w: page layout component %q columns %d..%d exceed grid of %d columns", ErrInvalidPayload, base.ID, base.Placement.Column, columnEnd, columns)
		}
	}
	columnsValue, rowHeight, gap, padding := patch.Columns, patch.RowHeight, patch.Gap, patch.Padding
	page.Layout = &document.DashboardLayoutOverride{Columns: &columnsValue, RowHeight: &rowHeight, Gap: &gap, Padding: &padding}
	return nil
}

func addCanonicalFilter(value *document.DashboardDocument, patch AddFilterPayload) error {
	id := strings.TrimSpace(patch.FilterID)
	if id == "" {
		id = nextCanonicalBuilderID("filter", len(value.Spec.Filters)+1, func(candidate string) bool {
			for _, filter := range value.Spec.Filters {
				if filter.ID == candidate {
					return true
				}
			}
			return false
		})
	}
	for _, filter := range value.Spec.Filters {
		if filter.ID == id {
			return fmt.Errorf("%w: filter %q already exists", ErrConflict, id)
		}
	}
	control, err := canonicalBuilderFilterControl(patch.ControlType, patch.Dataset, nil)
	if err != nil {
		return err
	}
	readerEditable := true
	value.Spec.Filters = append(value.Spec.Filters, document.DashboardFilter{ID: id, Label: strings.TrimSpace(patch.Label), Dimension: strings.TrimSpace(patch.Dimension), Control: control, ReaderEditable: &readerEditable})
	return nil
}

func updateCanonicalFilter(value *document.DashboardDocument, patch UpdateFilterPayload) error {
	for index := range value.Spec.Filters {
		filter := &value.Spec.Filters[index]
		if filter.ID != patch.FilterID {
			continue
		}
		control, err := canonicalBuilderFilterControl(patch.ControlType, patch.Dataset, &filter.Control)
		if err != nil {
			return err
		}
		filter.Label = strings.TrimSpace(patch.Label)
		filter.Description = optionalCanonicalString(patch.Description)
		filter.Control = control
		filter.Required = &patch.Required
		filter.ReaderEditable = &patch.ReaderEditable
		filter.URLParameter = optionalCanonicalString(patch.URLParameter)
		return nil
	}
	return fmt.Errorf("%w: filter %q", ErrNotFound, patch.FilterID)
}

// setCanonicalFilterTargets updates only a filter's target policy. A nil
// payload slice clears the authored policy (all semantically compatible
// visuals); explicit targets are copied into the generated document so the
// revision cannot alias transport memory. Canonical compiler validation later
// resolves IDs to page/component consumer keys and checks semantic
// compatibility.
func setCanonicalFilterTargets(value *document.DashboardDocument, patch SetFilterTargetsPayload) error {
	for index := range value.Spec.Filters {
		filter := &value.Spec.Filters[index]
		if filter.ID != patch.FilterID {
			continue
		}
		if patch.Targets == nil {
			filter.Targets = nil
			return nil
		}
		targets := append([]string(nil), patch.Targets...)
		filter.Targets = &targets
		return nil
	}
	return fmt.Errorf("%w: filter %q", ErrNotFound, patch.FilterID)
}

func setCanonicalFilterScope(value *document.DashboardDocument, patch SetFilterScopePayload) error {
	filterFound := false
	for filterIndex := range value.Spec.Filters {
		filter := &value.Spec.Filters[filterIndex]
		if filter.ID == patch.FilterID {
			filterFound = true
			if patch.Scope != "report" || len(patch.Targets) == 0 {
				filter.Targets = nil
			} else {
				targets := append([]string(nil), patch.Targets...)
				filter.Targets = &targets
			}
			break
		}
	}
	if !filterFound {
		return fmt.Errorf("%w: filter %q", ErrNotFound, patch.FilterID)
	}
	if patch.Scope == "report" {
		// Restoring report scope removes every explicit page binding while
		// preserving slicer components, which now resolve to the report binding.
		for pageIndex := range value.Spec.Pages {
			page := &value.Spec.Pages[pageIndex]
			if page.FilterBindings == nil {
				continue
			}
			retained := (*page.FilterBindings)[:0]
			for _, binding := range *page.FilterBindings {
				if binding.Filter != patch.FilterID {
					retained = append(retained, binding)
				}
			}
			if len(retained) == 0 {
				page.FilterBindings = nil
			} else {
				copied := append([]document.DashboardPageFilterBinding(nil), retained...)
				page.FilterBindings = &copied
			}
		}
		return nil
	}
	for pageIndex := range value.Spec.Pages {
		page := &value.Spec.Pages[pageIndex]
		if page.ID != patch.PageID {
			continue
		}
		if len(patch.Targets) > 0 {
			if err := ensureCanonicalPageTargetsHaveIndependentVisuals(value, pageIndex, patch.Targets); err != nil {
				return err
			}
		}
		bindings := []document.DashboardPageFilterBinding{}
		if page.FilterBindings != nil {
			bindings = append(bindings, (*page.FilterBindings)...)
			for bindingIndex := range bindings {
				binding := &bindings[bindingIndex]
				if binding.Filter == patch.FilterID {
					binding.Targets = optionalCanonicalTargets(patch.Targets)
					page.FilterBindings = &bindings
					return nil
				}
			}
		}
		bindings = append(bindings, document.DashboardPageFilterBinding{ID: nextCanonicalPageFilterBindingID(*page, patch.FilterID), Filter: patch.FilterID, Targets: optionalCanonicalTargets(patch.Targets)})
		page.FilterBindings = &bindings
		return nil
	}
	return fmt.Errorf("%w: page %q", ErrNotFound, patch.PageID)
}

func optionalCanonicalTargets(targets []string) *[]string {
	if len(targets) == 0 {
		return nil
	}
	copied := append([]string(nil), targets...)
	return &copied
}

// ensureCanonicalPageTargetsHaveIndependentVisuals preserves true component
// scope in the current runtime, whose query identity is the visual definition
// ID. When one definition is placed more than once on a page, the selected
// component receives an equivalent private definition before it is targeted.
func ensureCanonicalPageTargetsHaveIndependentVisuals(value *document.DashboardDocument, pageIndex int, targets []string) error {
	page := &value.Spec.Pages[pageIndex]
	for _, target := range targets {
		componentIndex := -1
		definitionID := ""
		for index, component := range page.Components {
			base, err := component.Base()
			visual, ok := component.Value.(*document.VisualDashboardPageComponent)
			if err == nil && base != nil && ok && base.ID == target {
				componentIndex, definitionID = index, visual.Visual
				break
			}
		}
		if componentIndex < 0 {
			return fmt.Errorf("%w: visual component %q on page %q", ErrNotFound, target, page.ID)
		}
		placements := 0
		for _, component := range page.Components {
			visual, ok := component.Value.(*document.VisualDashboardPageComponent)
			if ok && visual.Visual == definitionID {
				placements++
			}
		}
		if placements <= 1 {
			continue
		}
		definition, exists := value.Spec.Visuals[definitionID]
		if !exists {
			return fmt.Errorf("%w: visual definition %q", ErrNotFound, definitionID)
		}
		encoded, err := json.Marshal(definition)
		if err != nil {
			return fmt.Errorf("%w: clone scoped visual: %v", ErrInvalidPayload, err)
		}
		var clone document.DashboardVisual
		if err := json.Unmarshal(encoded, &clone); err != nil {
			return fmt.Errorf("%w: clone scoped visual: %v", ErrInvalidPayload, err)
		}
		newID := nextCanonicalBuilderID(definitionID, 2, func(candidate string) bool {
			_, exists := value.Spec.Visuals[candidate]
			return exists
		})
		value.Spec.Visuals[newID] = clone
		page.Components[componentIndex].Value.(*document.VisualDashboardPageComponent).Visual = newID
	}
	return nil
}

func removeCanonicalFilter(value *document.DashboardDocument, patch RemoveFilterPayload) error {
	for index := range value.Spec.Filters {
		if value.Spec.Filters[index].ID == patch.FilterID {
			value.Spec.Filters = append(value.Spec.Filters[:index], value.Spec.Filters[index+1:]...)
			for pageIndex := range value.Spec.Pages {
				if value.Spec.Pages[pageIndex].FilterBindings != nil {
					bindings := (*value.Spec.Pages[pageIndex].FilterBindings)[:0]
					for _, binding := range *value.Spec.Pages[pageIndex].FilterBindings {
						if binding.Filter != patch.FilterID {
							bindings = append(bindings, binding)
						}
					}
					if len(bindings) == 0 {
						value.Spec.Pages[pageIndex].FilterBindings = nil
					} else {
						copied := append([]document.DashboardPageFilterBinding(nil), bindings...)
						value.Spec.Pages[pageIndex].FilterBindings = &copied
					}
				}
				components := value.Spec.Pages[pageIndex].Components[:0]
				for _, component := range value.Spec.Pages[pageIndex].Components {
					filter, ok := component.Value.(*document.FilterDashboardPageComponent)
					if ok && filter.Filter == patch.FilterID {
						continue
					}
					components = append(components, component)
				}
				value.Spec.Pages[pageIndex].Components = components
			}
			return nil
		}
	}
	return fmt.Errorf("%w: filter %q", ErrNotFound, patch.FilterID)
}

func pruneCanonicalFilterComponents(value *document.DashboardDocument) {
	retained := make(map[string]struct{}, len(value.Spec.Filters))
	for _, filter := range value.Spec.Filters {
		retained[filter.ID] = struct{}{}
	}
	for pageIndex := range value.Spec.Pages {
		if value.Spec.Pages[pageIndex].FilterBindings != nil {
			bindings := (*value.Spec.Pages[pageIndex].FilterBindings)[:0]
			for _, binding := range *value.Spec.Pages[pageIndex].FilterBindings {
				if _, ok := retained[binding.Filter]; ok {
					bindings = append(bindings, binding)
				}
			}
			if len(bindings) == 0 {
				value.Spec.Pages[pageIndex].FilterBindings = nil
			} else {
				copied := append([]document.DashboardPageFilterBinding(nil), bindings...)
				value.Spec.Pages[pageIndex].FilterBindings = &copied
			}
		}
		components := value.Spec.Pages[pageIndex].Components[:0]
		for _, component := range value.Spec.Pages[pageIndex].Components {
			filter, ok := component.Value.(*document.FilterDashboardPageComponent)
			if ok {
				if _, exists := retained[filter.Filter]; !exists {
					continue
				}
			}
			components = append(components, component)
		}
		value.Spec.Pages[pageIndex].Components = components
	}
}

func addCanonicalFilterComponent(value *document.DashboardDocument, patch AddFilterComponentPayload) error {
	filterExists := false
	for _, filter := range value.Spec.Filters {
		if filter.ID == patch.FilterID {
			filterExists = true
			break
		}
	}
	if !filterExists {
		return fmt.Errorf("%w: filter %q", ErrNotFound, patch.FilterID)
	}
	pageIndex := -1
	for index := range value.Spec.Pages {
		if value.Spec.Pages[index].ID == patch.PageID {
			pageIndex = index
			break
		}
	}
	if pageIndex < 0 {
		return fmt.Errorf("%w: page %q", ErrNotFound, patch.PageID)
	}
	pageScoped, locallyBound := false, false
	for index := range value.Spec.Pages {
		bindings := value.Spec.Pages[index].FilterBindings
		if bindings == nil {
			continue
		}
		for _, binding := range *bindings {
			if binding.Filter != patch.FilterID {
				continue
			}
			pageScoped = true
			if index == pageIndex {
				locallyBound = true
			}
		}
	}
	if pageScoped && !locallyBound {
		bindings := []document.DashboardPageFilterBinding{}
		if value.Spec.Pages[pageIndex].FilterBindings != nil {
			bindings = append(bindings, (*value.Spec.Pages[pageIndex].FilterBindings)...)
		}
		bindings = append(bindings, document.DashboardPageFilterBinding{ID: nextCanonicalPageFilterBindingID(value.Spec.Pages[pageIndex], patch.FilterID), Filter: patch.FilterID})
		value.Spec.Pages[pageIndex].FilterBindings = &bindings
	}
	for _, component := range value.Spec.Pages[pageIndex].Components {
		filter, ok := component.Value.(*document.FilterDashboardPageComponent)
		if ok && filter.Filter == patch.FilterID {
			return fmt.Errorf("%w: filter %q is already placed on page %q", ErrConflict, patch.FilterID, patch.PageID)
		}
	}
	componentID := strings.TrimSpace(patch.ComponentID)
	if componentID == "" {
		componentID = nextCanonicalBuilderID("filter_component", len(value.Spec.Pages[pageIndex].Components)+1, func(candidate string) bool {
			for _, component := range value.Spec.Pages[pageIndex].Components {
				base, _ := component.Base()
				if base != nil && base.ID == candidate {
					return true
				}
			}
			return false
		})
	}
	for _, component := range value.Spec.Pages[pageIndex].Components {
		base, _ := component.Base()
		if base != nil && base.ID == componentID {
			return fmt.Errorf("%w: component %q already exists", ErrConflict, componentID)
		}
	}
	placement := nextCanonicalComponentPlacement(*value, pageIndex, 3, 2)
	value.Spec.Pages[pageIndex].Components = append(value.Spec.Pages[pageIndex].Components, document.DashboardPageComponent{Value: &document.FilterDashboardPageComponent{
		DashboardPageComponentBase: document.DashboardPageComponentBase{ID: componentID, Type: "filter", Placement: placement}, Type: "filter", Filter: patch.FilterID,
	}})
	return nil
}

func nextCanonicalPageFilterBindingID(page document.DashboardPage, filterID string) string {
	exists := func(candidate string) bool {
		if page.FilterBindings == nil {
			return false
		}
		for _, binding := range *page.FilterBindings {
			if binding.ID == candidate {
				return true
			}
		}
		return false
	}
	if !exists(filterID) {
		return filterID
	}
	return nextCanonicalBuilderID(filterID, 2, exists)
}

func removeCanonicalFilterComponent(value *document.DashboardDocument, patch RemoveFilterComponentPayload) error {
	for pageIndex := range value.Spec.Pages {
		if value.Spec.Pages[pageIndex].ID != patch.PageID {
			continue
		}
		for componentIndex, component := range value.Spec.Pages[pageIndex].Components {
			base, err := component.Base()
			_, isFilter := component.Value.(*document.FilterDashboardPageComponent)
			if err == nil && base != nil && isFilter && base.ID == patch.ComponentID {
				value.Spec.Pages[pageIndex].Components = append(value.Spec.Pages[pageIndex].Components[:componentIndex], value.Spec.Pages[pageIndex].Components[componentIndex+1:]...)
				return nil
			}
		}
		return fmt.Errorf("%w: filter component %q on page %q", ErrNotFound, patch.ComponentID, patch.PageID)
	}
	return fmt.Errorf("%w: page %q", ErrNotFound, patch.PageID)
}

func optionalCanonicalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func canonicalBuilderFilterControl(controlType, dataset string, existing *document.DashboardFilterControl) (document.DashboardFilterControl, error) {
	controlType, dataset = strings.TrimSpace(controlType), strings.TrimSpace(dataset)
	if existing != nil {
		if existingType, err := existing.Type(); err == nil && existingType == controlType {
			return *existing, nil
		}
	}
	distinct := func() *document.DashboardFilterOptions {
		return &document.DashboardFilterOptions{Value: &document.DistinctDashboardFilterOptions{Type: "distinct", Dataset: dataset}}
	}
	switch controlType {
	case "singleSelect":
		return document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: controlType, Options: distinct()}}, nil
	case "multiSelect":
		return document.DashboardFilterControl{Value: &document.MultiSelectDashboardFilterControl{Type: controlType, Options: distinct()}}, nil
	case "text":
		return document.DashboardFilterControl{Value: &document.TextDashboardFilterControl{Type: controlType}}, nil
	case "numericRange":
		return document.DashboardFilterControl{Value: &document.NumericRangeDashboardFilterControl{Type: controlType}}, nil
	case "dateRange":
		return document.DashboardFilterControl{Value: &document.DateRangeDashboardFilterControl{Type: controlType}}, nil
	case "relativePeriod":
		return document.DashboardFilterControl{Value: &document.RelativePeriodDashboardFilterControl{Type: controlType}}, nil
	default:
		return document.DashboardFilterControl{}, fmt.Errorf("%w: unsupported filter control %q", ErrInvalidPayload, controlType)
	}
}

func resolveCanonicalInteractionVisual(value document.DashboardDocument, pageID, visualID string) (string, error) {
	if strings.TrimSpace(visualID) != "" {
		if _, exists := value.Spec.Visuals[visualID]; exists {
			if pageID == "" {
				return visualID, nil
			}
			for _, page := range value.Spec.Pages {
				if page.ID != pageID {
					continue
				}
				for _, component := range page.Components {
					visual, ok := component.Value.(*document.VisualDashboardPageComponent)
					if ok && visual.Visual == visualID {
						return visualID, nil
					}
				}
				return "", fmt.Errorf("%w: page %q does not reference visual %q", ErrConflict, pageID, visualID)
			}
			return "", fmt.Errorf("%w: page %q", ErrNotFound, pageID)
		}
		if pageID != "" {
			for _, page := range value.Spec.Pages {
				if page.ID != pageID {
					continue
				}
				for _, component := range page.Components {
					base, _ := component.Base()
					visual, ok := component.Value.(*document.VisualDashboardPageComponent)
					if ok && base != nil && base.ID == visualID {
						return visual.Visual, nil
					}
				}
				return "", fmt.Errorf("%w: visual component %q", ErrNotFound, visualID)
			}
		}
		return "", fmt.Errorf("%w: visual %q", ErrNotFound, visualID)
	}
	if strings.TrimSpace(pageID) == "" {
		return "", fmt.Errorf("%w: page or visual target is required", ErrInvalidPayload)
	}
	var target string
	for _, page := range value.Spec.Pages {
		if page.ID != pageID {
			continue
		}
		for _, component := range page.Components {
			visual, ok := component.Value.(*document.VisualDashboardPageComponent)
			if !ok || visual.Visual == "" {
				continue
			}
			if target != "" {
				return "", fmt.Errorf("%w: page %q has multiple visual targets", ErrConflict, pageID)
			}
			target = visual.Visual
		}
		if target == "" {
			return "", fmt.Errorf("%w: page %q has no visual targets", ErrNotFound, pageID)
		}
		return target, nil
	}
	return "", fmt.Errorf("%w: page %q", ErrNotFound, pageID)
}

// setCanonicalInteractionTarget updates one target edge while preserving the
// authored selection's exact mode, toggle, mappings, and unrelated targets.
// Both source and target IDs are resolved on the requested page; a visual
// definition ID or a placed component ID is accepted for either side.
func setCanonicalInteractionTarget(value *document.DashboardDocument, patch SetInteractionTargetPayload) error {
	if value == nil {
		return fmt.Errorf("%w: dashboard document is required", ErrInvalidPayload)
	}
	sourceID, err := resolvePageInteractionVisual(*value, patch.PageID, patch.VisualID)
	if err != nil {
		return err
	}
	targetID, err := resolvePageInteractionVisual(*value, patch.PageID, patch.TargetVisualID)
	if err != nil {
		return err
	}
	if sourceID == targetID {
		return fmt.Errorf("%w: interaction source and target cannot be the same visual", ErrInvalidPayload)
	}
	effect := strings.TrimSpace(patch.Effect)
	switch effect {
	case "filter", "highlight", "none":
	default:
		return fmt.Errorf("%w: unsupported interaction target effect %q", ErrInvalidPayload, patch.Effect)
	}
	source, ok := value.Spec.Visuals[sourceID]
	if !ok {
		return fmt.Errorf("%w: visual %q", ErrNotFound, sourceID)
	}
	selection, err := canonicalInteractionSelectionForTarget(source)
	if err != nil {
		return err
	}
	selection.Targets = updateInteractionTargetList(selection.Targets, targetID, effect == "filter")
	selection.HighlightTargets = updateInteractionTargetList(selection.HighlightTargets, targetID, effect == "highlight")
	selection.NoneTargets = updateInteractionTargetList(selection.NoneTargets, targetID, effect == "none")
	if !interactionTargetListsDisjoint(selection.Targets, selection.HighlightTargets, selection.NoneTargets) {
		return fmt.Errorf("%w: interaction target effects overlap", ErrInvalidPayload)
	}
	if interactionTargetCount(selection.Targets) > maxFilterTargets || interactionTargetCount(selection.HighlightTargets) > maxFilterTargets || interactionTargetCount(selection.NoneTargets) > maxFilterTargets {
		return fmt.Errorf("%w: interaction targets exceed bounded visual limit", ErrInvalidPayload)
	}
	interactions := []document.DashboardInteraction{{Value: selection}}
	source.Interactions = &interactions
	value.Spec.Visuals[sourceID] = source
	return nil
}

func interactionTargetCount(value *[]string) int {
	if value == nil {
		return 0
	}
	return len(*value)
}

func interactionTargetListsDisjoint(groups ...*[]string) bool {
	seen := make(map[string]struct{})
	for _, group := range groups {
		if group == nil {
			continue
		}
		for _, target := range *group {
			if _, exists := seen[target]; exists {
				return false
			}
			seen[target] = struct{}{}
		}
	}
	return true
}

func resolvePageInteractionVisual(value document.DashboardDocument, pageID, visualID string) (string, error) {
	pageID, visualID = strings.TrimSpace(pageID), strings.TrimSpace(visualID)
	if pageID == "" || visualID == "" {
		return "", fmt.Errorf("%w: interaction source and target require page and visual ids", ErrInvalidPayload)
	}
	for _, page := range value.Spec.Pages {
		if page.ID != pageID {
			continue
		}
		for _, component := range page.Components {
			visual, ok := component.Value.(*document.VisualDashboardPageComponent)
			if !ok || strings.TrimSpace(visual.Visual) == "" {
				continue
			}
			base, baseErr := component.Base()
			if baseErr != nil {
				return "", baseErr
			}
			if visual.Visual == visualID || (base != nil && base.ID == visualID) {
				if _, exists := value.Spec.Visuals[visual.Visual]; !exists {
					return "", fmt.Errorf("%w: visual %q", ErrNotFound, visual.Visual)
				}
				return visual.Visual, nil
			}
		}
		return "", fmt.Errorf("%w: visual component %q on page %q", ErrNotFound, visualID, pageID)
	}
	return "", fmt.Errorf("%w: page %q", ErrNotFound, pageID)
}

func canonicalInteractionSelectionForTarget(visual document.DashboardVisual) (*document.SelectionDashboardInteraction, error) {
	if visual.Interactions == nil || len(*visual.Interactions) == 0 {
		mapping, ok := inferCanonicalInteractionMapping(visual.Query)
		if !ok {
			return nil, fmt.Errorf("%w: source visual has no inferable dimension or detail mapping", ErrInvalidPayload)
		}
		return &document.SelectionDashboardInteraction{
			DashboardInteractionBase: document.DashboardInteractionBase{Type: "selection"},
			Type:                     "selection", Mode: document.DashboardSelectionModeSingle, Toggle: true,
			Mappings: []document.DashboardInteractionMapping{mapping},
		}, nil
	}
	if len(*visual.Interactions) != 1 {
		return nil, fmt.Errorf("%w: source visual has multiple authored interactions", ErrInvalidPayload)
	}
	selection, ok := (*visual.Interactions)[0].Value.(*document.SelectionDashboardInteraction)
	if !ok || selection == nil {
		return nil, fmt.Errorf("%w: source visual has spatial or unsupported interaction", ErrInvalidPayload)
	}
	if selection.Mode != document.DashboardSelectionModeSingle && selection.Mode != document.DashboardSelectionModeMultiple {
		return nil, fmt.Errorf("%w: source visual has unsupported interaction mode", ErrInvalidPayload)
	}
	clone := *selection
	clone.Mappings = append([]document.DashboardInteractionMapping(nil), selection.Mappings...)
	clone.Targets = cloneInteractionTargets(selection.Targets)
	clone.HighlightTargets = cloneInteractionTargets(selection.HighlightTargets)
	clone.NoneTargets = cloneInteractionTargets(selection.NoneTargets)
	return &clone, nil
}

func cloneInteractionTargets(value *[]string) *[]string {
	if value == nil {
		return nil
	}
	copyValue := append([]string(nil), (*value)...)
	return &copyValue
}

func updateInteractionTargetList(current *[]string, target string, keep bool) *[]string {
	var values []string
	if current != nil {
		values = append([]string(nil), (*current)...)
	}
	found := false
	result := make([]string, 0, len(values)+1)
	for _, candidate := range values {
		if candidate == target {
			if keep && !found {
				result = append(result, candidate)
				found = true
			}
			continue
		}
		result = append(result, candidate)
	}
	if keep && !found {
		result = append(result, target)
	}
	if len(result) == 0 {
		return nil
	}
	return &result
}

func inferCanonicalInteractionMapping(query document.DashboardQuery) (document.DashboardInteractionMapping, bool) {
	var fieldID, label, dataset string
	var grain *document.DashboardTimeGrain
	switch value := query.Value.(type) {
	case *document.AggregateDashboardQuery:
		if len(value.Dimensions) == 0 {
			return document.DashboardInteractionMapping{}, false
		}
		fieldID, label = canonicalDimensionSelection(value.Dimensions[0])
		if reference := value.Dimensions[0].Reference; reference != nil && reference.Grain != nil {
			value := *reference.Grain
			grain = &value
		}
	case *document.PivotDashboardQuery:
		if len(value.Rows) == 0 {
			return document.DashboardInteractionMapping{}, false
		}
		fieldID, label = canonicalDimensionSelection(value.Rows[0])
		if reference := value.Rows[0].Reference; reference != nil && reference.Grain != nil {
			value := *reference.Grain
			grain = &value
		}
	case *document.RecordsDashboardQuery:
		if len(value.Fields) == 0 {
			return document.DashboardInteractionMapping{}, false
		}
		fieldID, label = canonicalRecordSelection(value.Fields[0])
		dataset = strings.TrimSpace(value.Dataset)
	default:
		return document.DashboardInteractionMapping{}, false
	}
	if !ValidGovernedFieldID(fieldID) {
		return document.DashboardInteractionMapping{}, false
	}
	mapping := document.DashboardInteractionMapping{Field: fieldID, Value: fieldID}
	if dataset != "" {
		mapping.Dataset = &dataset
	}
	if grain != nil {
		mapping.Grain = grain
	}
	if strings.TrimSpace(label) != "" && label != fieldID {
		mapping.Label = &label
	}
	return mapping, true
}

func resourceID(value string) graph.ResourceID { return graph.ResourceID(value) }

func addCanonicalVisual(value *document.DashboardDocument, patch AddVisualPayload) error {
	if value.Spec.Visuals == nil {
		value.Spec.Visuals = map[string]document.DashboardVisual{}
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
	if !canonicalVisualTypeSupported(document.DashboardVisualType(strings.TrimSpace(patch.Type))) {
		return fmt.Errorf("%w: unsupported visual type %q", ErrInvalidPayload, patch.Type)
	}
	visualID := strings.TrimSpace(patch.VisualID)
	if visualID == "" {
		visualID = nextCanonicalBuilderID("visual", len(value.Spec.Visuals)+1, func(candidate string) bool { _, ok := value.Spec.Visuals[candidate]; return ok })
	}
	if _, exists := value.Spec.Visuals[visualID]; exists {
		return fmt.Errorf("%w: visual %q already exists", ErrConflict, visualID)
	}
	title := strings.TrimSpace(patch.Title)
	if title == "" {
		title = visualID
	}
	value.Spec.Visuals[visualID] = defaultCanonicalVisual(patch.Type, title)
	componentID := strings.TrimSpace(patch.ComponentID)
	if componentID == "" {
		componentID = nextCanonicalBuilderID("component", len(value.Spec.Pages[pageIndex].Components)+1, func(candidate string) bool {
			for _, component := range value.Spec.Pages[pageIndex].Components {
				base, _ := component.Base()
				if base != nil && base.ID == candidate {
					return true
				}
			}
			return false
		})
	}
	placement := nextCanonicalVisualPlacement(*value, pageIndex, document.DashboardVisualType(strings.TrimSpace(patch.Type)))
	value.Spec.Pages[pageIndex].Components = append(value.Spec.Pages[pageIndex].Components, document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: componentID, Type: "visual", Placement: placement}, Type: "visual", Visual: visualID}})
	if patch.FieldID != "" {
		if !patch.FieldValidated {
			return fmt.Errorf("%w: initial visual field requires governed validation", ErrInvalidPayload)
		}
		return assignCanonicalField(value, AssignFieldPayload{PageID: patch.PageID, VisualID: componentID, FieldID: patch.FieldID, Role: patch.Role, ResolvedTable: patch.ResolvedTable})
	}
	return nil
}

func setCanonicalPlacements(value *document.DashboardDocument, patch SetPlacementsPayload) error {
	pageIndex := -1
	for index := range value.Spec.Pages {
		if value.Spec.Pages[index].ID == patch.PageID {
			pageIndex = index
			break
		}
	}
	if pageIndex < 0 {
		return fmt.Errorf("%w: page %q", ErrNotFound, patch.PageID)
	}
	page := &value.Spec.Pages[pageIndex]
	columns, err := canonicalPlacementColumns(*value, *page)
	if err != nil {
		return err
	}

	placements := make(map[string]document.DashboardPlacement, len(page.Components))
	componentIndexes := make(map[string]int, len(page.Components))
	for index := range page.Components {
		base, err := page.Components[index].Base()
		if err != nil {
			return fmt.Errorf("%w: component %d: %v", ErrInvalidPayload, index, err)
		}
		if _, exists := placements[base.ID]; exists {
			return fmt.Errorf("%w: page %q contains duplicate component %q", ErrInvalidPayload, patch.PageID, base.ID)
		}
		placements[base.ID] = base.Placement
		componentIndexes[base.ID] = index
	}
	for _, update := range patch.Placements {
		if _, exists := placements[update.ComponentID]; !exists {
			return fmt.Errorf("%w: component %q on page %q", ErrNotFound, update.ComponentID, patch.PageID)
		}
		placements[update.ComponentID] = update.Placement
	}

	ids := make([]string, 0, len(placements))
	for id := range placements {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		placement := placements[id]
		if err := validatePlacementCoordinates(placement); err != nil {
			return fmt.Errorf("%w: component %q: %v", ErrInvalidPayload, id, err)
		}
		columnEnd := int64(placement.Column) + int64(placement.ColumnSpan) - 1
		if int64(placement.Column) > columns || columnEnd > columns {
			return fmt.Errorf("%w: component %q columns %d..%d exceed grid of %d columns", ErrInvalidPayload, id, placement.Column, columnEnd, columns)
		}
	}
	for leftIndex, leftID := range ids {
		for _, rightID := range ids[leftIndex+1:] {
			if placementsOverlapCanonical(placements[leftID], placements[rightID]) {
				return fmt.Errorf("%w: components %q and %q overlap", ErrConflict, leftID, rightID)
			}
		}
	}
	for id, placement := range placements {
		index := componentIndexes[id]
		base, err := page.Components[index].Base()
		if err != nil {
			return fmt.Errorf("%w: component %q: %v", ErrInvalidPayload, id, err)
		}
		base.Placement = placement
	}
	return nil
}

func canonicalPlacementColumns(value document.DashboardDocument, page document.DashboardPage) (int64, error) {
	const defaultColumns int64 = 12
	columns := defaultColumns
	if value.Spec.Layout != nil {
		if value.Spec.Layout.Columns <= 0 {
			return 0, fmt.Errorf("%w: dashboard layout columns must be greater than zero", ErrInvalidPayload)
		}
		columns = int64(value.Spec.Layout.Columns)
	}
	if page.Layout != nil && page.Layout.Columns != nil {
		if *page.Layout.Columns <= 0 {
			return 0, fmt.Errorf("%w: page layout columns must be greater than zero", ErrInvalidPayload)
		}
		columns = int64(*page.Layout.Columns)
	}
	return columns, nil
}

func nextCanonicalVisualPlacement(value document.DashboardDocument, pageIndex int, visualType document.DashboardVisualType) document.DashboardPlacement {
	columnSpan, rowSpan := canonicalVisualPlacementSize(visualType)
	return nextCanonicalComponentPlacement(value, pageIndex, columnSpan, rowSpan)
}

func nextCanonicalComponentPlacement(value document.DashboardDocument, pageIndex int, columnSpan, rowSpan int32) document.DashboardPlacement {
	const defaultColumns int32 = 12
	columns := defaultColumns
	if value.Spec.Layout != nil && value.Spec.Layout.Columns > 0 {
		columns = value.Spec.Layout.Columns
	}
	page := value.Spec.Pages[pageIndex]
	if page.Layout != nil && page.Layout.Columns != nil && *page.Layout.Columns > 0 {
		columns = *page.Layout.Columns
	}
	columnSpan = minPositive(columns, columnSpan)
	for row := int32(1); ; row++ {
		for column := int32(1); column <= columns-columnSpan+1; column++ {
			candidate := document.DashboardPlacement{Column: column, Row: row, ColumnSpan: columnSpan, RowSpan: rowSpan}
			available := true
			for _, component := range page.Components {
				base, err := component.Base()
				if err == nil && base != nil && placementsOverlap(candidate, base.Placement) {
					available = false
					break
				}
			}
			if available {
				return candidate
			}
		}
	}
}

func canonicalVisualPlacementSize(visualType document.DashboardVisualType) (columnSpan, rowSpan int32) {
	switch visualType {
	case document.DashboardVisualTypeKpi, document.DashboardVisualTypeGauge:
		return 4, 3
	case document.DashboardVisualTypeTable, document.DashboardVisualTypeMatrix, document.DashboardVisualTypePivot:
		return 6, 5
	default:
		return 6, 4
	}
}

func placementsOverlap(left, right document.DashboardPlacement) bool {
	leftColumnEnd := left.Column + maxPositive(left.ColumnSpan, 1)
	leftRowEnd := left.Row + maxPositive(left.RowSpan, 1)
	rightColumnEnd := right.Column + maxPositive(right.ColumnSpan, 1)
	rightRowEnd := right.Row + maxPositive(right.RowSpan, 1)
	return left.Column < rightColumnEnd && right.Column < leftColumnEnd && left.Row < rightRowEnd && right.Row < leftRowEnd
}

func placementsOverlapCanonical(left, right document.DashboardPlacement) bool {
	leftColumnEnd := int64(left.Column) + int64(left.ColumnSpan) - 1
	leftRowEnd := int64(left.Row) + int64(left.RowSpan) - 1
	rightColumnEnd := int64(right.Column) + int64(right.ColumnSpan) - 1
	rightRowEnd := int64(right.Row) + int64(right.RowSpan) - 1
	return int64(left.Column) <= rightColumnEnd && int64(right.Column) <= leftColumnEnd && int64(left.Row) <= rightRowEnd && int64(right.Row) <= leftRowEnd
}

func maxPositive(value, fallback int32) int32 {
	if value > 0 {
		return value
	}
	return fallback
}

func minPositive(left, right int32) int32 {
	if left <= 0 {
		return right
	}
	if right <= 0 || left < right {
		return left
	}
	return right
}

func canonicalVisualTypeSupported(value document.DashboardVisualType) bool {
	switch value {
	case document.DashboardVisualTypeLine, document.DashboardVisualTypeArea, document.DashboardVisualTypeBar, document.DashboardVisualTypeColumn, document.DashboardVisualTypePie, document.DashboardVisualTypeDonut, document.DashboardVisualTypeScatter, document.DashboardVisualTypeFunnel, document.DashboardVisualTypeTreemap, document.DashboardVisualTypeGauge, document.DashboardVisualTypeHeatmap, document.DashboardVisualTypeSankey, document.DashboardVisualTypeGraph, document.DashboardVisualTypeMap, document.DashboardVisualTypeCandlestick, document.DashboardVisualTypeBoxplot, document.DashboardVisualTypeCombo, document.DashboardVisualTypeWaterfall, document.DashboardVisualTypeHistogram, document.DashboardVisualTypeRadar, document.DashboardVisualTypeTree, document.DashboardVisualTypeSunburst, document.DashboardVisualTypeKpi, document.DashboardVisualTypeTable, document.DashboardVisualTypeMatrix, document.DashboardVisualTypePivot:
		return true
	default:
		return false
	}
}

// CanonicalVisualTypeSupported reports whether a generated dashboard visual
// type is accepted by the authoring reducer. Transport adapters use this same
// predicate so the API and persisted canonical document cannot drift.
func CanonicalVisualTypeSupported(value document.DashboardVisualType) bool {
	return canonicalVisualTypeSupported(value)
}

func defaultCanonicalVisual(kind, title string) document.DashboardVisual {
	visualType := document.DashboardVisualType(kind)
	query := document.DashboardQuery{}
	presentation := document.DashboardPresentation{}
	metric := "pending_metric"
	switch visualType {
	case document.DashboardVisualTypeHistogram:
		query.Value = &document.HistogramDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "histogram"}, Type: "histogram", Field: document.DashboardMetricSelection{String: &metric}, Bins: 10, NullPolicy: document.DashboardHistogramNullPolicyOmit, Approximation: document.DashboardHistogramApproximationExact}
		presentation.Value = &document.CartesianDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "cartesian"}, Type: "cartesian"}
	case document.DashboardVisualTypeBoxplot:
		query.Value = &document.DistributionDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "distribution"}, Type: "distribution", Field: document.DashboardMetricSelection{String: &metric}, Quantiles: []float64{0.25, 0.5, 0.75}, Outliers: document.DashboardDistributionOutlierPolicyOmit, Approximation: document.DashboardHistogramApproximationExact}
		presentation.Value = &document.CartesianDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "cartesian"}, Type: "cartesian"}
	case document.DashboardVisualTypeTable:
		query.Value = &document.RecordsDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "records"}, Type: "records", Dataset: "pending_dataset", Fields: []document.DashboardRecordFieldSelection{}}
		presentation.Value = &document.TableDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "table"}, Type: "table", RowHeight: 32, ShowHeader: true, Striped: false}
	case document.DashboardVisualTypeMatrix, document.DashboardVisualTypePivot:
		query.Value = &document.PivotDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "pivot"}, Type: "pivot", Rows: []document.DashboardDimensionSelection{}, Columns: []document.DashboardDimensionSelection{}, Metrics: []document.DashboardMetricSelection{}}
		presentation.Value = &document.TableDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "table"}, Type: "table", RowHeight: 32, ShowHeader: true, Striped: false}
	case document.DashboardVisualTypeKpi:
		query.Value = &document.AggregateDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{}, Metrics: []document.DashboardMetricSelection{}}
		presentation.Value = &document.KPIDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "kpi"}, Type: "kpi"}
	case document.DashboardVisualTypePie, document.DashboardVisualTypeDonut, document.DashboardVisualTypeFunnel:
		query.Value = &document.AggregateDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{}, Metrics: []document.DashboardMetricSelection{}}
		presentation.Value = &document.ProportionalDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "proportional"}, Type: "proportional"}
	case document.DashboardVisualTypeTreemap, document.DashboardVisualTypeSankey, document.DashboardVisualTypeGraph, document.DashboardVisualTypeTree, document.DashboardVisualTypeSunburst:
		query.Value = &document.AggregateDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{}, Metrics: []document.DashboardMetricSelection{}}
		presentation.Value = &document.HierarchyDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "hierarchy"}, Type: "hierarchy"}
	case document.DashboardVisualTypeGauge, document.DashboardVisualTypeRadar:
		query.Value = &document.AggregateDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{}, Metrics: []document.DashboardMetricSelection{}}
		presentation.Value = &document.PolarDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "polar"}, Type: "polar"}
	case document.DashboardVisualTypeMap:
		query.Value = &document.RecordsDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "records"}, Type: "records", Dataset: "pending_dataset", Fields: []document.DashboardRecordFieldSelection{}}
		presentation.Value = &document.GeographicDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "geographic"}, Type: "geographic"}
	default:
		query.Value = &document.AggregateDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{}, Metrics: []document.DashboardMetricSelection{}}
		presentation.Value = &document.CartesianDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "cartesian"}, Type: "cartesian"}
	}
	if cartesian, ok := presentation.Value.(*document.CartesianDashboardPresentation); ok {
		applyCartesianTypeDefaults(cartesian, visualType)
	}
	return document.DashboardVisual{Type: visualType, Title: &title, Query: query, Presentation: presentation}
}

// resolveCanonicalPageVisual resolves a page component ID (the builder's
// selected visual identity) to its shared visual definition. Definition IDs
// are accepted as a compatibility fallback when the page references one.
func resolveCanonicalPageVisual(value document.DashboardDocument, pageID, visualID string) (string, error) {
	for _, page := range value.Spec.Pages {
		if page.ID != pageID {
			continue
		}
		for _, component := range page.Components {
			visual, ok := component.Value.(*document.VisualDashboardPageComponent)
			if !ok || visual.Visual == "" {
				continue
			}
			base, err := component.Base()
			if err != nil || base == nil {
				continue
			}
			if base.ID == visualID || visual.Visual == visualID {
				if _, exists := value.Spec.Visuals[visual.Visual]; !exists {
					return "", fmt.Errorf("%w: visual %q", ErrNotFound, visual.Visual)
				}
				return visual.Visual, nil
			}
		}
		return "", fmt.Errorf("%w: visual component %q on page %q", ErrNotFound, visualID, pageID)
	}
	return "", fmt.Errorf("%w: page %q", ErrNotFound, pageID)
}

func visualQueryType(query document.DashboardQuery) string {
	typ, _ := query.Type()
	return typ
}

func visualPresentationType(presentation document.DashboardPresentation) string {
	typ, _ := presentation.Type()
	return typ
}

func visualQueryKind(visualType document.DashboardVisualType) string {
	switch visualType {
	case document.DashboardVisualTypeHistogram:
		return "histogram"
	case document.DashboardVisualTypeBoxplot:
		return "distribution"
	case document.DashboardVisualTypeTable, document.DashboardVisualTypeMap:
		return "records"
	case document.DashboardVisualTypeMatrix, document.DashboardVisualTypePivot:
		return "pivot"
	default:
		return "aggregate"
	}
}

func setCanonicalVisualType(value *document.DashboardDocument, patch SetVisualTypePayload) error {
	visualID, err := resolveCanonicalPageVisual(*value, patch.PageID, patch.VisualID)
	if err != nil {
		return err
	}
	visual := value.Spec.Visuals[visualID]
	if visual.Type == patch.Type {
		return nil
	}
	oldQueryType := visualQueryType(visual.Query)
	oldPresentationType := visualPresentationType(visual.Presentation)
	visual.Type = patch.Type
	newDefault := defaultCanonicalVisual(string(patch.Type), visualTitle(visual, visualID))
	// Compatible visual families keep both their bound query and presentation
	// options (bar/column/line/area are all cartesian aggregate visuals).
	if oldQueryType == visualQueryKind(patch.Type) {
		newDefault.Query = visual.Query
	}
	if oldPresentationType == visualPresentationType(newDefault.Presentation) {
		if oldCartesian, oldOK := visual.Presentation.Value.(*document.CartesianDashboardPresentation); oldOK {
			if nextCartesian, nextOK := newDefault.Presentation.Value.(*document.CartesianDashboardPresentation); nextOK {
				mergeCartesianPresentation(nextCartesian, oldCartesian)
				applyCartesianTypeDefaults(nextCartesian, patch.Type)
				newDefault.Presentation.Value = nextCartesian
			} else {
				newDefault.Presentation = visual.Presentation
			}
		} else {
			newDefault.Presentation = visual.Presentation
		}
	} else if oldQueryType == visualQueryKind(patch.Type) {
		// Preserve renderer-neutral formatting controls when only presentation
		// family changes, while letting the target family supply safe defaults.
		if oldBase, baseErr := visual.Presentation.Base(); baseErr == nil {
			if nextBase, nextErr := newDefault.Presentation.Base(); nextErr == nil {
				nextBase.AxisVisible = oldBase.AxisVisible
			}
		}
	}
	newDefault.Title = visual.Title
	newDefault.TitleVisible = visual.TitleVisible
	newDefault.Subtitle = visual.Subtitle
	newDefault.Description = visual.Description
	newDefault.Metadata = visual.Metadata
	newDefault.Accessibility = visual.Accessibility
	newDefault.DataBudget = visual.DataBudget
	newDefault.Calculations = visual.Calculations
	newDefault.Interactions = visual.Interactions
	value.Spec.Visuals[visualID] = newDefault
	return nil
}

func mergeCartesianPresentation(target, source *document.CartesianDashboardPresentation) {
	target.DashboardPresentationBase = source.DashboardPresentationBase
	target.Legend = source.Legend
	target.Labels = source.Labels
	target.Stacking = source.Stacking
	target.ShowSymbols = source.ShowSymbols
	target.Smooth = source.Smooth
	target.Step = source.Step
	target.DataZoom = source.DataZoom
	target.SymbolSize = source.SymbolSize
	target.LabelPosition = source.LabelPosition
	target.DisplayUnits = source.DisplayUnits
	target.Series = source.Series
}

func applyCartesianTypeDefaults(presentation *document.CartesianDashboardPresentation, visualType document.DashboardVisualType) {
	switch visualType {
	case document.DashboardVisualTypeBar:
		orientation := document.DashboardOrientationHorizontal
		presentation.Orientation = &orientation
	case document.DashboardVisualTypeColumn:
		orientation := document.DashboardOrientationVertical
		presentation.Orientation = &orientation
	}
}

func visualTitle(visual document.DashboardVisual, fallback string) string {
	if visual.Title != nil && strings.TrimSpace(*visual.Title) != "" {
		return *visual.Title
	}
	return fallback
}

func renameCanonicalVisual(value *document.DashboardDocument, patch RenameVisualPayload) error {
	visualID, err := resolveCanonicalPageVisual(*value, patch.PageID, patch.VisualID)
	if err != nil {
		return err
	}
	title := strings.TrimSpace(patch.Title)
	visual := value.Spec.Visuals[visualID]
	visual.Title = &title
	value.Spec.Visuals[visualID] = visual
	return nil
}

func duplicateCanonicalVisual(value *document.DashboardDocument, patch DuplicateVisualPayload) error {
	sourceID, err := resolveCanonicalPageVisual(*value, patch.PageID, patch.VisualID)
	if err != nil {
		return err
	}
	pageIndex := -1
	componentIndex := -1
	for index := range value.Spec.Pages {
		if value.Spec.Pages[index].ID != patch.PageID {
			continue
		}
		pageIndex = index
		for componentPosition, component := range value.Spec.Pages[index].Components {
			base, baseErr := component.Base()
			visual, visualOK := component.Value.(*document.VisualDashboardPageComponent)
			if baseErr == nil && base != nil && visualOK && (base.ID == patch.VisualID || visual.Visual == patch.VisualID) {
				componentIndex = componentPosition
				break
			}
		}
		break
	}
	if pageIndex < 0 {
		return fmt.Errorf("%w: page %q", ErrNotFound, patch.PageID)
	}
	if componentIndex < 0 {
		return fmt.Errorf("%w: visual component %q on page %q", ErrNotFound, patch.VisualID, patch.PageID)
	}
	newVisualID := strings.TrimSpace(patch.NewVisualID)
	if newVisualID == "" {
		newVisualID = nextCanonicalBuilderID("visual", len(value.Spec.Visuals)+1, func(candidate string) bool {
			_, exists := value.Spec.Visuals[candidate]
			return exists
		})
	}
	if _, exists := value.Spec.Visuals[newVisualID]; exists {
		return fmt.Errorf("%w: visual %q already exists", ErrConflict, newVisualID)
	}
	newComponentID := strings.TrimSpace(patch.NewComponentID)
	if newComponentID == "" {
		newComponentID = nextCanonicalBuilderID("component", len(value.Spec.Pages[pageIndex].Components)+1, func(candidate string) bool {
			for _, component := range value.Spec.Pages[pageIndex].Components {
				base, _ := component.Base()
				if base != nil && base.ID == candidate {
					return true
				}
			}
			return false
		})
	}
	for _, component := range value.Spec.Pages[pageIndex].Components {
		base, _ := component.Base()
		if base != nil && base.ID == newComponentID {
			return fmt.Errorf("%w: component %q already exists", ErrConflict, newComponentID)
		}
	}
	encoded, err := json.Marshal(value.Spec.Visuals[sourceID])
	if err != nil {
		return fmt.Errorf("%w: clone visual: %v", ErrInvalidPayload, err)
	}
	var clone document.DashboardVisual
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return fmt.Errorf("%w: clone visual: %v", ErrInvalidPayload, err)
	}
	if title := strings.TrimSpace(patch.Title); title != "" {
		clone.Title = &title
	}
	value.Spec.Visuals[newVisualID] = clone
	placement := nextCanonicalVisualPlacement(*value, pageIndex, clone.Type)
	value.Spec.Pages[pageIndex].Components = append(value.Spec.Pages[pageIndex].Components, document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{
		DashboardPageComponentBase: document.DashboardPageComponentBase{ID: newComponentID, Type: "visual", Placement: placement}, Type: "visual", Visual: newVisualID,
	}})
	return nil
}

func removeCanonicalVisual(value *document.DashboardDocument, patch RemoveVisualPayload) error {
	if strings.TrimSpace(patch.PageID) == "" {
		if _, ok := value.Spec.Visuals[patch.VisualID]; !ok {
			return fmt.Errorf("%w: visual %q", ErrNotFound, patch.VisualID)
		}
		for pageIndex := range value.Spec.Pages {
			for _, component := range value.Spec.Pages[pageIndex].Components {
				visual, ok := component.Value.(*document.VisualDashboardPageComponent)
				if ok && visual.Visual == patch.VisualID {
					base, _ := component.Base()
					return fmt.Errorf("%w: visual %q is referenced by page %q component %q", ErrConflict, patch.VisualID, value.Spec.Pages[pageIndex].ID, base.ID)
				}
			}
		}
		delete(value.Spec.Visuals, patch.VisualID)
		return nil
	}
	definitionID, err := resolveCanonicalPageVisual(*value, patch.PageID, patch.VisualID)
	if err != nil {
		return err
	}
	for pageIndex := range value.Spec.Pages {
		if value.Spec.Pages[pageIndex].ID != patch.PageID {
			continue
		}
		components := value.Spec.Pages[pageIndex].Components
		for index, component := range components {
			base, baseErr := component.Base()
			visual, visualOK := component.Value.(*document.VisualDashboardPageComponent)
			if baseErr == nil && base != nil && visualOK && (base.ID == patch.VisualID || visual.Visual == patch.VisualID) {
				value.Spec.Pages[pageIndex].Components = append(components[:index], components[index+1:]...)
				if !canonicalVisualReferenced(value, definitionID) {
					delete(value.Spec.Visuals, definitionID)
				}
				pruneCanonicalFilterTargetsAfterVisualRemoval(value, patch.PageID, []canonicalRemovedVisual{{componentID: base.ID, definitionID: definitionID}}, nil)
				return nil
			}
		}
	}
	return fmt.Errorf("%w: visual component %q", ErrNotFound, patch.VisualID)
}

func canonicalVisualReferenced(value *document.DashboardDocument, visualID string) bool {
	for _, page := range value.Spec.Pages {
		for _, component := range page.Components {
			visual, ok := component.Value.(*document.VisualDashboardPageComponent)
			if ok && visual.Visual == visualID {
				return true
			}
		}
	}
	return false
}

type canonicalRemovedVisual struct {
	componentID  string
	definitionID string
}

func canonicalRemovedPageVisuals(page document.DashboardPage) []canonicalRemovedVisual {
	removed := []canonicalRemovedVisual{}
	for _, component := range page.Components {
		base, err := component.Base()
		visual, ok := component.Value.(*document.VisualDashboardPageComponent)
		if err == nil && base != nil && ok {
			removed = append(removed, canonicalRemovedVisual{componentID: base.ID, definitionID: visual.Visual})
		}
	}
	return removed
}

// pruneCanonicalFilterTargetsAfterVisualRemoval keeps the authored document
// compilable without broadening a visual- or page-scoped filter. A binding
// attached only to deleted content disappears with that content; shared page
// bindings retain their surviving targets.
func pruneCanonicalFilterTargetsAfterVisualRemoval(value *document.DashboardDocument, pageID string, removed []canonicalRemovedVisual, affected map[string]struct{}) {
	if affected == nil {
		affected = map[string]struct{}{}
	}
	componentIDs := make(map[string]struct{}, len(removed))
	qualifiedIDs := make(map[string]struct{}, len(removed))
	unreferencedDefinitions := make(map[string]struct{}, len(removed))
	for _, visual := range removed {
		componentIDs[visual.componentID] = struct{}{}
		qualifiedIDs[pageID+"/"+visual.componentID] = struct{}{}
		if !canonicalVisualReferenced(value, visual.definitionID) {
			unreferencedDefinitions[visual.definitionID] = struct{}{}
		}
	}
	for pageIndex := range value.Spec.Pages {
		page := &value.Spec.Pages[pageIndex]
		if page.ID != pageID || page.FilterBindings == nil {
			continue
		}
		bindings := make([]document.DashboardPageFilterBinding, 0, len(*page.FilterBindings))
		for _, binding := range *page.FilterBindings {
			if binding.Targets == nil {
				bindings = append(bindings, binding)
				continue
			}
			retained := make([]string, 0, len(*binding.Targets))
			for _, target := range *binding.Targets {
				if _, removed := componentIDs[target]; !removed {
					retained = append(retained, target)
				}
			}
			if len(retained) == 0 {
				affected[binding.Filter] = struct{}{}
				continue
			}
			binding.Targets = &retained
			bindings = append(bindings, binding)
		}
		if len(bindings) == 0 {
			page.FilterBindings = nil
		} else {
			page.FilterBindings = &bindings
		}
	}
	for filterIndex := range value.Spec.Filters {
		filter := &value.Spec.Filters[filterIndex]
		if filter.Targets == nil {
			continue
		}
		retained := make([]string, 0, len(*filter.Targets))
		for _, target := range *filter.Targets {
			_, qualifiedRemoved := qualifiedIDs[target]
			_, definitionRemoved := unreferencedDefinitions[target]
			if !qualifiedRemoved && !definitionRemoved {
				retained = append(retained, target)
			}
		}
		if len(retained) == 0 {
			affected[filter.ID] = struct{}{}
		}
		filter.Targets = &retained
	}
	for filterID := range affected {
		if canonicalFilterHasPageBinding(*value, filterID) {
			continue
		}
		for _, filter := range value.Spec.Filters {
			if filter.ID == filterID && (filter.Targets == nil || len(*filter.Targets) == 0) {
				_ = removeCanonicalFilter(value, RemoveFilterPayload{FilterID: filterID})
				break
			}
		}
	}
}

func canonicalFilterHasPageBinding(value document.DashboardDocument, filterID string) bool {
	for _, page := range value.Spec.Pages {
		if page.FilterBindings == nil {
			continue
		}
		for _, binding := range *page.FilterBindings {
			if binding.Filter == filterID {
				return true
			}
		}
	}
	return false
}

func updateCanonicalVisualFormat(value *document.DashboardDocument, patch UpdateVisualFormatPayload) error {
	visualID, err := resolveCanonicalPageVisual(*value, patch.PageID, patch.VisualID)
	if err != nil {
		return err
	}
	visual := value.Spec.Visuals[visualID]
	if patch.Title != nil {
		title := strings.TrimSpace(*patch.Title)
		visual.Title = &title
	}
	if patch.TitleVisible != nil {
		visual.TitleVisible = patch.TitleVisible
	}
	base, baseErr := visual.Presentation.Base()
	if baseErr != nil {
		return fmt.Errorf("%w: visual presentation: %v", ErrInvalidPayload, baseErr)
	}
	if patch.AxisVisible != nil {
		base.AxisVisible = patch.AxisVisible
	}
	if patch.LegendVisible != nil {
		if err := setCanonicalLegendVisibility(&visual.Presentation, *patch.LegendVisible); err != nil {
			return err
		}
	}
	if patch.DataLabelsVisible != nil {
		if err := setCanonicalDataLabelsVisibility(&visual.Presentation, *patch.DataLabelsVisible); err != nil {
			return err
		}
	}
	if patch.FormatKey != "" {
		if patch.FormatValue == nil {
			return fmt.Errorf("%w: visual format option requires a value", ErrInvalidPayload)
		}
		if err := applyCanonicalVisualFormatOption(&visual, strings.TrimSpace(patch.FormatKey), *patch.FormatValue); err != nil {
			return err
		}
	}
	value.Spec.Visuals[visualID] = visual
	return nil
}

func setCanonicalLegendVisibility(presentation *document.DashboardPresentation, visible bool) error {
	switch value := presentation.Value.(type) {
	case *document.CartesianDashboardPresentation:
		return setLegendPosition(&value.Legend, visible)
	case *document.PointDashboardPresentation:
		return setLegendPosition(&value.Legend, visible)
	case *document.ProportionalDashboardPresentation:
		return setLegendPosition(&value.Legend, visible)
	case *document.HierarchyDashboardPresentation:
		return setLegendPosition(&value.Legend, visible)
	case *document.PolarDashboardPresentation:
		return setLegendPosition(&value.Legend, visible)
	default:
		return fmt.Errorf("%w: visual presentation does not support legends", ErrInvalidPayload)
	}
}

func setLegendPosition(position **document.DashboardLegendPosition, visible bool) error {
	if !visible {
		none := document.DashboardLegendPositionNone
		*position = &none
		return nil
	}
	if *position == nil || **position == document.DashboardLegendPositionNone {
		defaultPosition := document.DashboardLegendPositionRight
		*position = &defaultPosition
	}
	return nil
}

func setCanonicalDataLabelsVisibility(presentation *document.DashboardPresentation, visible bool) error {
	set := func(policy **document.DashboardLabelPolicy) {
		if !visible {
			if *policy == nil {
				*policy = &document.DashboardLabelPolicy{}
			}
			(*policy).Density = document.DashboardLabelDensityHidden
			return
		}
		if *policy == nil {
			*policy = &document.DashboardLabelPolicy{Density: document.DashboardLabelDensityAutomatic}
		} else if (*policy).Density == document.DashboardLabelDensityHidden {
			(*policy).Density = document.DashboardLabelDensityAutomatic
		}
	}
	switch value := presentation.Value.(type) {
	case *document.CartesianDashboardPresentation:
		set(&value.Labels)
	case *document.PointDashboardPresentation:
		set(&value.Labels)
	case *document.ProportionalDashboardPresentation:
		set(&value.Labels)
	case *document.HierarchyDashboardPresentation:
		set(&value.Labels)
	case *document.PolarDashboardPresentation:
		set(&value.Labels)
	case *document.GeographicDashboardPresentation:
		set(&value.Labels)
	default:
		return fmt.Errorf("%w: visual presentation does not support data labels", ErrInvalidPayload)
	}
	return nil
}

func removeCanonicalField(value *document.DashboardDocument, patch RemoveFieldPayload) error {
	visualID, err := resolveCanonicalPageVisual(*value, patch.PageID, patch.VisualID)
	if err != nil {
		return err
	}
	visual := value.Spec.Visuals[visualID]
	removed, err := removeFieldFromQuery(&visual.Query, patch.Role, patch.FieldID)
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("%w: field %q in role %q", ErrNotFound, patch.FieldID, patch.Role)
	}
	value.Spec.Visuals[visualID] = visual
	return nil
}

func moveCanonicalField(value *document.DashboardDocument, patch MoveFieldPayload) error {
	visualID, err := resolveCanonicalPageVisual(*value, patch.PageID, patch.VisualID)
	if err != nil {
		return err
	}
	visual := value.Spec.Visuals[visualID]
	targetRole := patch.TargetRole
	if targetRole == "" {
		targetRole = patch.Role
	}
	if targetRole != patch.Role {
		return fmt.Errorf("%w: cross-role field moves are not supported", ErrInvalidPayload)
	}
	selection, sourceIndex, err := takeFieldFromQuery(&visual.Query, patch.Role, patch.FieldID)
	if err != nil {
		return err
	}
	if sourceIndex < 0 {
		return fmt.Errorf("%w: field %q in role %q", ErrNotFound, patch.FieldID, patch.Role)
	}
	if err := insertFieldIntoQuery(&visual.Query, targetRole, selection, sourceIndex, patch.Index, strings.TrimSpace(patch.Direction)); err != nil {
		return err
	}
	value.Spec.Visuals[visualID] = visual
	return nil
}

type canonicalFieldSelection struct {
	Metric    *document.DashboardMetricSelection
	Dimension *document.DashboardDimensionSelection
	Record    *document.DashboardRecordFieldSelection
}

func removeFieldFromQuery(query *document.DashboardQuery, role FieldRole, fieldID string) (bool, error) {
	selection, index, err := takeFieldFromQuery(query, role, fieldID)
	if err != nil {
		return false, err
	}
	return index >= 0 && (selection.Metric != nil || selection.Dimension != nil || selection.Record != nil), nil
}

func takeFieldFromQuery(query *document.DashboardQuery, role FieldRole, fieldID string) (canonicalFieldSelection, int, error) {
	if query == nil || query.Value == nil {
		return canonicalFieldSelection{}, -1, fmt.Errorf("%w: visual query is required", ErrInvalidPayload)
	}
	switch value := query.Value.(type) {
	case *document.AggregateDashboardQuery:
		switch role {
		case FieldRoleMetric:
			for index, field := range value.Metrics {
				id, _ := canonicalMetricSelection(field)
				if id == fieldID {
					value.Metrics = append(value.Metrics[:index], value.Metrics[index+1:]...)
					return canonicalFieldSelection{Metric: &field}, index, nil
				}
			}
		case FieldRoleDimension:
			for index, field := range value.Dimensions {
				id, _ := canonicalDimensionSelection(field)
				if id == fieldID {
					value.Dimensions = append(value.Dimensions[:index], value.Dimensions[index+1:]...)
					return canonicalFieldSelection{Dimension: &field}, index, nil
				}
			}
		case FieldRoleDetail:
			return canonicalFieldSelection{}, -1, fmt.Errorf("%w: aggregate queries do not accept detail fields", ErrInvalidPayload)
		}
	case *document.RecordsDashboardQuery:
		if role != FieldRoleDetail {
			return canonicalFieldSelection{}, -1, fmt.Errorf("%w: records queries accept detail fields", ErrInvalidPayload)
		}
		for index, field := range value.Fields {
			id, _ := canonicalRecordSelection(field)
			if id == fieldID {
				value.Fields = append(value.Fields[:index], value.Fields[index+1:]...)
				return canonicalFieldSelection{Record: &field}, index, nil
			}
		}
	case *document.PivotDashboardQuery:
		switch role {
		case FieldRoleMetric:
			for index, field := range value.Metrics {
				id, _ := canonicalMetricSelection(field)
				if id == fieldID {
					value.Metrics = append(value.Metrics[:index], value.Metrics[index+1:]...)
					return canonicalFieldSelection{Metric: &field}, index, nil
				}
			}
		case FieldRoleDimension:
			for index, field := range value.Rows {
				id, _ := canonicalDimensionSelection(field)
				if id == fieldID {
					value.Rows = append(value.Rows[:index], value.Rows[index+1:]...)
					return canonicalFieldSelection{Dimension: &field}, index, nil
				}
			}
		case FieldRoleDetail:
			return canonicalFieldSelection{}, -1, fmt.Errorf("%w: pivot queries do not accept detail fields", ErrInvalidPayload)
		}
	case *document.HistogramDashboardQuery, *document.DistributionDashboardQuery:
		return canonicalFieldSelection{}, -1, fmt.Errorf("%w: scalar visual field cannot be removed or moved", ErrInvalidPayload)
	default:
		return canonicalFieldSelection{}, -1, fmt.Errorf("%w: visual query does not accept fields", ErrInvalidPayload)
	}
	return canonicalFieldSelection{}, -1, nil
}

func insertFieldIntoQuery(query *document.DashboardQuery, role FieldRole, selection canonicalFieldSelection, sourceIndex int, requestedIndex *int, direction string) error {
	if query == nil || query.Value == nil {
		return fmt.Errorf("%w: visual query is required", ErrInvalidPayload)
	}
	index := sourceIndex
	if requestedIndex != nil {
		index = *requestedIndex
		if direction == "after" {
			index++
		}
	} else if direction == "up" {
		index = sourceIndex - 1
	} else if direction == "down" {
		index = sourceIndex + 1
	}
	if index < 0 {
		index = 0
	}
	switch value := query.Value.(type) {
	case *document.AggregateDashboardQuery:
		if selection.Metric != nil && role == FieldRoleMetric {
			if index > len(value.Metrics) {
				index = len(value.Metrics)
			}
			value.Metrics = append(value.Metrics, document.DashboardMetricSelection{})
			copy(value.Metrics[index+1:], value.Metrics[index:])
			value.Metrics[index] = *selection.Metric
			return nil
		}
		if selection.Dimension != nil && role == FieldRoleDimension {
			if index > len(value.Dimensions) {
				index = len(value.Dimensions)
			}
			value.Dimensions = append(value.Dimensions, document.DashboardDimensionSelection{})
			copy(value.Dimensions[index+1:], value.Dimensions[index:])
			value.Dimensions[index] = *selection.Dimension
			return nil
		}
		if role == FieldRoleMetric && selection.Dimension != nil {
			id, _ := canonicalDimensionSelection(*selection.Dimension)
			value.Metrics = append(value.Metrics, document.DashboardMetricSelection{})
			if index > len(value.Metrics)-1 {
				index = len(value.Metrics) - 1
			}
			copy(value.Metrics[index+1:], value.Metrics[index:])
			value.Metrics[index] = document.DashboardMetricSelection{String: &id}
			return nil
		}
		if role == FieldRoleDimension && selection.Metric != nil {
			id, _ := canonicalMetricSelection(*selection.Metric)
			value.Dimensions = append(value.Dimensions, document.DashboardDimensionSelection{})
			if index > len(value.Dimensions)-1 {
				index = len(value.Dimensions) - 1
			}
			copy(value.Dimensions[index+1:], value.Dimensions[index:])
			value.Dimensions[index] = document.DashboardDimensionSelection{String: &id}
			return nil
		}
	case *document.RecordsDashboardQuery:
		if role == FieldRoleDetail && selection.Record != nil {
			if index > len(value.Fields) {
				index = len(value.Fields)
			}
			value.Fields = append(value.Fields, document.DashboardRecordFieldSelection{})
			copy(value.Fields[index+1:], value.Fields[index:])
			value.Fields[index] = *selection.Record
			return nil
		}
	case *document.PivotDashboardQuery:
		if selection.Metric != nil && role == FieldRoleMetric {
			if index > len(value.Metrics) {
				index = len(value.Metrics)
			}
			value.Metrics = append(value.Metrics, document.DashboardMetricSelection{})
			copy(value.Metrics[index+1:], value.Metrics[index:])
			value.Metrics[index] = *selection.Metric
			return nil
		}
		if selection.Dimension != nil && role == FieldRoleDimension {
			if index > len(value.Rows) {
				index = len(value.Rows)
			}
			value.Rows = append(value.Rows, document.DashboardDimensionSelection{})
			copy(value.Rows[index+1:], value.Rows[index:])
			value.Rows[index] = *selection.Dimension
			return nil
		}
		if role == FieldRoleMetric && selection.Dimension != nil {
			id, _ := canonicalDimensionSelection(*selection.Dimension)
			value.Metrics = append(value.Metrics, document.DashboardMetricSelection{})
			if index > len(value.Metrics)-1 {
				index = len(value.Metrics) - 1
			}
			copy(value.Metrics[index+1:], value.Metrics[index:])
			value.Metrics[index] = document.DashboardMetricSelection{String: &id}
			return nil
		}
		if role == FieldRoleDimension && selection.Metric != nil {
			id, _ := canonicalMetricSelection(*selection.Metric)
			value.Rows = append(value.Rows, document.DashboardDimensionSelection{})
			if index > len(value.Rows)-1 {
				index = len(value.Rows) - 1
			}
			copy(value.Rows[index+1:], value.Rows[index:])
			value.Rows[index] = document.DashboardDimensionSelection{String: &id}
			return nil
		}
	}
	return fmt.Errorf("%w: field role %q is incompatible with visual query", ErrInvalidPayload, role)
}

func assignCanonicalField(value *document.DashboardDocument, patch AssignFieldPayload) error {
	visualID := ""
	pageFound := false
	for _, page := range value.Spec.Pages {
		if page.ID != patch.PageID {
			continue
		}
		pageFound = true
		for _, component := range page.Components {
			visual, ok := component.Value.(*document.VisualDashboardPageComponent)
			if !ok {
				continue
			}
			base, err := component.Base()
			if err != nil {
				continue
			}
			if base.ID == patch.VisualID {
				visualID = visual.Visual
			}
		}
	}
	if !pageFound {
		return fmt.Errorf("%w: page %q", ErrNotFound, patch.PageID)
	}
	if visualID == "" {
		return fmt.Errorf("%w: visual component %q on page %q", ErrNotFound, patch.VisualID, patch.PageID)
	}
	visual, ok := value.Spec.Visuals[visualID]
	if !ok {
		return fmt.Errorf("%w: visual %q", ErrNotFound, visualID)
	}
	switch query := visual.Query.Value.(type) {
	case *document.AggregateDashboardQuery:
		if !ValidSemanticMemberID(patch.FieldID) {
			return fmt.Errorf("%w: aggregate selections require an unqualified semantic member", ErrInvalidPayload)
		}
		switch patch.Role {
		case FieldRoleMetric:
			ref := patch.FieldID
			for _, existing := range query.Metrics {
				id, _ := canonicalMetricSelection(existing)
				if id == ref {
					return nil
				}
			}
			query.Metrics = append(query.Metrics, document.DashboardMetricSelection{String: &ref})
		case FieldRoleDimension:
			ref := patch.FieldID
			for _, existing := range query.Dimensions {
				id, _ := canonicalDimensionSelection(existing)
				if id == ref {
					return nil
				}
			}
			query.Dimensions = append(query.Dimensions, document.DashboardDimensionSelection{String: &ref})
		default:
			return fmt.Errorf("%w: detail fields require records queries", ErrInvalidPayload)
		}
	case *document.RecordsDashboardQuery:
		if patch.Role != FieldRoleDetail {
			return fmt.Errorf("%w: records queries accept detail fields", ErrInvalidPayload)
		}
		if strings.TrimSpace(query.Dataset) == "" {
			return fmt.Errorf("%w: records query dataset is required", ErrInvalidPayload)
		}
		if strings.TrimSpace(query.Dataset) == "pending_dataset" {
			if strings.TrimSpace(patch.ResolvedTable) == "" {
				return fmt.Errorf("%w: governed records field requires a resolved table", ErrInvalidPayload)
			}
			query.Dataset = strings.TrimSpace(patch.ResolvedTable)
		}
		ref := patch.FieldID
		if qualified := strings.SplitN(strings.TrimSpace(patch.FieldID), ".", 2); len(qualified) == 2 {
			if qualified[0] != query.Dataset {
				return fmt.Errorf("%w: records field table %q does not match dataset %q", ErrInvalidPayload, qualified[0], query.Dataset)
			}
			// The governed intent uses a qualified field to resolve and verify
			// its dataset. The canonical records query stores that dataset once
			// and its field selections as schema-valid unqualified members.
			ref = qualified[1]
		}
		for _, existing := range query.Fields {
			id, _ := canonicalRecordSelection(existing)
			if id == ref {
				return nil
			}
		}
		query.Fields = append(query.Fields, document.DashboardRecordFieldSelection{String: &ref})
	case *document.PivotDashboardQuery:
		if !ValidSemanticMemberID(patch.FieldID) {
			return fmt.Errorf("%w: pivot selections require an unqualified semantic member", ErrInvalidPayload)
		}
		ref := patch.FieldID
		switch patch.Role {
		case FieldRoleDimension:
			for _, existing := range query.Rows {
				id, _ := canonicalDimensionSelection(existing)
				if id == ref {
					return nil
				}
			}
			query.Rows = append(query.Rows, document.DashboardDimensionSelection{String: &ref})
		case FieldRoleMetric:
			for _, existing := range query.Metrics {
				id, _ := canonicalMetricSelection(existing)
				if id == ref {
					return nil
				}
			}
			query.Metrics = append(query.Metrics, document.DashboardMetricSelection{String: &ref})
		default:
			return fmt.Errorf("%w: detail fields require records queries", ErrInvalidPayload)
		}
	case *document.HistogramDashboardQuery:
		if patch.Role != FieldRoleMetric || !ValidSemanticMemberID(patch.FieldID) {
			return fmt.Errorf("%w: histogram queries accept semantic metric fields", ErrInvalidPayload)
		}
		id, _ := canonicalMetricSelection(query.Field)
		if id != "" && id != "pending_metric" && id != patch.FieldID {
			return fmt.Errorf("%w: histogram query already has a different metric", ErrConflict)
		}
		ref := patch.FieldID
		query.Field = document.DashboardMetricSelection{String: &ref}
	case *document.DistributionDashboardQuery:
		if patch.Role != FieldRoleMetric || !ValidSemanticMemberID(patch.FieldID) {
			return fmt.Errorf("%w: distribution queries accept semantic metric fields", ErrInvalidPayload)
		}
		id, _ := canonicalMetricSelection(query.Field)
		if id != "" && id != "pending_metric" && id != patch.FieldID {
			return fmt.Errorf("%w: distribution query already has a different metric", ErrConflict)
		}
		ref := patch.FieldID
		query.Field = document.DashboardMetricSelection{String: &ref}
	default:
		return fmt.Errorf("%w: visual query does not accept assigned fields", ErrInvalidPayload)
	}
	value.Spec.Visuals[visualID] = visual
	return nil
}

func canonicalMetricSelection(value document.DashboardMetricSelection) (string, string) {
	if value.String != nil {
		return *value.String, *value.String
	}
	if value.Reference != nil {
		if value.Reference.Alias != nil {
			return value.Reference.Metric, *value.Reference.Alias
		}
		return value.Reference.Metric, value.Reference.Metric
	}
	return "", ""
}

func canonicalDimensionSelection(value document.DashboardDimensionSelection) (string, string) {
	if value.String != nil {
		return *value.String, *value.String
	}
	if value.Reference != nil {
		if value.Reference.Alias != nil {
			return value.Reference.Dimension, *value.Reference.Alias
		}
		return value.Reference.Dimension, value.Reference.Dimension
	}
	return "", ""
}

func canonicalRecordSelection(value document.DashboardRecordFieldSelection) (string, string) {
	if value.String != nil {
		return *value.String, *value.String
	}
	if value.Reference != nil {
		if value.Reference.Alias != nil {
			return value.Reference.Field, *value.Reference.Alias
		}
		return value.Reference.Field, value.Reference.Field
	}
	return "", ""
}

func canonicalDocumentTitle(value document.DashboardDocument) string {
	if value.Metadata.DisplayName != nil {
		return *value.Metadata.DisplayName
	}
	return value.Metadata.Name
}

func nextCanonicalBuilderID(prefix string, start int, exists func(string) bool) string {
	if start < 1 {
		start = 1
	}
	for index := start; ; index++ {
		candidate := fmt.Sprintf("%s_%d", prefix, index)
		if !exists(candidate) {
			return candidate
		}
	}
}

func sameRevisionToken(left, right RevisionToken) bool {
	return left.RevisionID == right.RevisionID && left.Number == right.Number && left.ContentHash == right.ContentHash
}
