package authoring

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	dashboardmodel "github.com/flidai/leapview/internal/dashboard"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	"github.com/flidai/leapview/internal/project/graph"
)

// ApplyEdit applies one validated authoring edit to an immutable revision.
//
// The reducer is deliberately pure: it does not persist, publish, deploy,
// allocate identifiers, or read the clock. nextRevisionID, nextNumber, and
// nextCreatedAt are supplied by the caller so the resulting revision is fully
// deterministic.
func ApplyEdit(lifecycle DashboardLifecycle, current Revision, command Command, nextRevisionID RevisionID, nextNumber uint64, nextCreatedAt time.Time) (DashboardLifecycle, Revision, error) {
	if err := lifecycle.Validate(); err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}
	if err := current.Validate(); err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}
	if current.DashboardID != lifecycle.ID {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: current revision belongs to dashboard %q", ErrInvalidAuthoring, current.DashboardID)
	}
	if lifecycle.SemanticModel != current.Document.SemanticModel {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: lifecycle semantic model does not match current revision", ErrInvalidAuthoring)
	}
	if lifecycle.Draft == nil {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: lifecycle has no draft", ErrInvalidAuthoring)
	}
	if !sameRevisionToken(lifecycle.Draft.Revision, current.Token()) {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: lifecycle draft does not select current revision", ErrStaleRevision)
	}
	if err := command.Validate(); err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}
	if command.DashboardID != lifecycle.ID || command.DashboardID != current.DashboardID {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: command belongs to dashboard %q", ErrInvalidAuthoring, command.DashboardID)
	}
	if command.DraftID != lifecycle.Draft.ID {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: command belongs to draft %q", ErrInvalidAuthoring, command.DraftID)
	}
	if !sameRevisionToken(command.ExpectedRevision, current.Token()) {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: expected revision does not match current revision", ErrStaleRevision)
	}
	if nextNumber != current.Number+1 {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: next revision number must be current number + 1", ErrInvalidAuthoring)
	}

	payload, _ := command.payloadValue()
	switch payload.(type) {
	case *PublishPayload, *ArchivePayload:
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: lifecycle operations are not edit commands", ErrInvalidPayload)
	}

	document, err := current.Document.Clone()
	if err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}
	if err := applyPayload(&document, payload); err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}
	if err := document.ValidateDraftStructure(); err != nil {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: resulting dashboard: %v", ErrInvalidPayload, err)
	}

	// ContentHash is an expected-result assertion, not a second concurrency
	// token. It is checked only after the edit has been applied.
	resultHash, err := DashboardContentHash(document)
	if err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}
	if command.ContentHash != "" && command.ContentHash != resultHash {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: expected resulting content hash %q, got %q", ErrConflict, command.ContentHash, resultHash)
	}

	provenance := command.Provenance.Clone()
	revision, err := NewRevision(nextRevisionID, current.DashboardID, nextNumber, nextCreatedAt, document, provenance)
	if err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}

	nextLifecycle, err := cloneValueAs(lifecycle, "lifecycle")
	if err != nil {
		return DashboardLifecycle{}, Revision{}, err
	}
	// Title lives in the authored document and in the lifecycle identity. Slug
	// and visibility are lifecycle-only and therefore remain unchanged unless
	// MetadataPatch explicitly changed them.
	nextLifecycle.Title = revision.Document.Title
	nextLifecycle.SemanticModel = revision.Document.SemanticModel
	if metadata, ok := payload.(*MetadataPatch); ok {
		if metadata.Slug != nil {
			if strings.TrimSpace(*metadata.Slug) == "" {
				return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: slug cannot be cleared", ErrInvalidPayload)
			}
			nextLifecycle.Slug = *metadata.Slug
		}
		if metadata.Visibility != nil {
			nextLifecycle.Visibility = *metadata.Visibility
		}
	}
	if visibility, ok := payload.(*SetVisibilityPayload); ok {
		nextLifecycle.Visibility = visibility.Visibility
	}
	draftProvenance := provenance.Clone()
	nextLifecycle.Draft = &Draft{
		ID:          lifecycle.Draft.ID,
		DashboardID: nextLifecycle.ID,
		Revision:    revision.Token(),
		Provenance:  draftProvenance,
	}
	if err := nextLifecycle.Validate(); err != nil {
		return DashboardLifecycle{}, Revision{}, fmt.Errorf("%w: resulting lifecycle: %v", ErrInvalidAuthoring, err)
	}
	return nextLifecycle, revision, nil
}

func applyPayload(document *Dashboard, payload authoringPayload) error {
	switch value := payload.(type) {
	case *MetadataPatch:
		return applyMetadata(document, *value)
	case *SetVisibilityPayload:
		// Lifecycle visibility is applied by ApplyEdit after the document clone;
		// keeping this payload a no-op here still gives it a transactional
		// revision and prevents accidental document rewrites.
		return nil
	case *AddPagePayload:
		return addPage(document, *value)
	case *AddVisualPayload:
		return addVisual(document, *value)
	case *AssignFieldPayload:
		return assignField(document, *value)
	case *UpsertPagePayload:
		page, err := cloneValueAs(value.Page, "upsert page")
		if err != nil {
			return err
		}
		for index := range document.Pages {
			if document.Pages[index].ID == page.ID {
				document.Pages[index] = page
				return nil
			}
		}
		document.Pages = append(document.Pages, page)
		return nil
	case *RemovePagePayload:
		return removePage(document, value.PageID)
	case *UpsertVisualPayload:
		visual, err := cloneValueAs(value.Visual, "upsert visual")
		if err != nil {
			return err
		}
		if document.Visuals == nil {
			document.Visuals = make(map[string]AuthoringVisualization)
		}
		document.Visuals[value.VisualID] = visual
		return nil
	case *RemoveVisualPayload:
		return removeVisual(document, value.VisualID)
	case *SetLayoutPayload:
		return applyLayout(document, *value)
	case *SetFiltersPayload:
		return applyFilters(document, *value)
	case *SetInteractionPayload:
		return applyInteraction(document, *value)
	default:
		return fmt.Errorf("%w: unsupported payload %T", ErrInvalidPayload, payload)
	}
}

func addPage(document *Dashboard, payload AddPagePayload) error {
	pageID := strings.TrimSpace(payload.PageID)
	if pageID == "" {
		pageID = nextBuilderID("page", len(document.Pages)+1, func(candidate string) bool {
			for _, page := range document.Pages {
				if page.ID == candidate {
					return true
				}
			}
			return false
		})
	}
	for _, page := range document.Pages {
		if page.ID == pageID {
			return fmt.Errorf("%w: page %q already exists", ErrConflict, pageID)
		}
	}
	title := strings.TrimSpace(payload.Title)
	if title == "" {
		title = builderTitle(pageID)
		if title == "" {
			title = "Page"
		}
	}
	document.Pages = append(document.Pages, dashboardmodel.Page{
		ID: pageID, Title: title,
		Canvas: dashboardmodel.PageCanvas{Width: 1366, Height: 940},
		Grid:   dashboardmodel.PageGrid{Columns: 12, RowHeight: 48, Gap: 16},
	}.WithDefaults())
	return nil
}

func addVisual(document *Dashboard, payload AddVisualPayload) error {
	pageIndex := -1
	for index := range document.Pages {
		if document.Pages[index].ID == payload.PageID {
			pageIndex = index
			break
		}
	}
	if pageIndex < 0 {
		return fmt.Errorf("%w: page %q", ErrNotFound, payload.PageID)
	}
	visualID := strings.TrimSpace(payload.VisualID)
	if visualID == "" {
		visualID = nextCanonicalBuilderID("visual", len(document.Visuals)+1, func(candidate string) bool {
			_, exists := document.Visuals[candidate]
			return exists
		})
	}
	if _, exists := document.Visuals[visualID]; exists {
		return fmt.Errorf("%w: visual %q already exists", ErrConflict, visualID)
	}
	componentID := strings.TrimSpace(payload.ComponentID)
	if componentID == "" {
		componentID = visualID + "_tile"
		if !canonicalIdentifierPattern.MatchString(componentID) {
			componentID = nextCanonicalBuilderID("component", len(document.Pages[pageIndex].Visuals)+1, func(candidate string) bool {
				for _, component := range document.Pages[pageIndex].Visuals {
					if component.ID == candidate {
						return true
					}
				}
				return false
			})
		}
	}
	for _, page := range document.Pages {
		for _, component := range page.Visuals {
			if component.ID == componentID {
				return fmt.Errorf("%w: component %q already exists", ErrConflict, componentID)
			}
		}
	}
	title := strings.TrimSpace(payload.Title)
	if title == "" {
		title = builderTitle(visualID)
		if title == "" {
			title = "Visual"
		}
	}
	capability, _ := VisualizationCapabilityForType(payload.Type)
	var visual AuthoringVisualization
	if capability.Kind == "grid" {
		visual = TabularVisualization(payload.Type, TableVisual{Title: title})
	} else {
		visual = ChartVisualization(Visual{Title: title, Type: payload.Type})
	}
	if document.Visuals == nil {
		document.Visuals = make(map[string]AuthoringVisualization)
	}
	document.Visuals[visualID] = visual
	page := &document.Pages[pageIndex]
	page.Visuals = append(page.Visuals, dashboardmodel.PageVisual{ID: componentID, Kind: "visual", Visual: visualID, Placement: defaultBuilderPlacement(page)})
	document.Pages[pageIndex] = *page
	return nil
}

func defaultBuilderPlacement(page *dashboardmodel.Page) dashboardmodel.PagePlacement {
	pageValue := page.WithDefaults()
	span := minInt(6, pageValue.Grid.Columns)
	if span < 1 {
		span = 1
	}
	// Scan row-major so repeated adds have a stable, non-overlapping location.
	for row := 1; row <= 1<<20; row++ {
		for col := 1; col+span-1 <= pageValue.Grid.Columns; col++ {
			candidate := dashboardmodel.PagePlacement{Col: col, Row: row, ColSpan: span, RowSpan: 4}
			occupied := false
			for _, component := range pageValue.Visuals {
				if component.Placement.IsZero() {
					continue
				}
				if placementsOverlap(candidate, component.Placement) {
					occupied = true
					break
				}
			}
			if !occupied {
				return candidate
			}
		}
	}
	return dashboardmodel.PagePlacement{Col: 1, Row: 1, ColSpan: span, RowSpan: 4}
}

func placementsOverlap(left, right dashboardmodel.PagePlacement) bool {
	return left.Col < right.Col+right.ColSpan && right.Col < left.Col+left.ColSpan &&
		left.Row < right.Row+right.RowSpan && right.Row < left.Row+left.RowSpan
}

func assignField(document *Dashboard, payload AssignFieldPayload) error {
	pageIndex, componentIndex := -1, -1
	for index := range document.Pages {
		if document.Pages[index].ID != payload.PageID {
			continue
		}
		pageIndex = index
		for component := range document.Pages[index].Visuals {
			if document.Pages[index].Visuals[component].ID == payload.VisualID {
				componentIndex = component
				break
			}
		}
		break
	}
	if pageIndex < 0 {
		return fmt.Errorf("%w: page %q", ErrNotFound, payload.PageID)
	}
	if componentIndex < 0 {
		return fmt.Errorf("%w: visual component %q on page %q", ErrNotFound, payload.VisualID, payload.PageID)
	}
	component := document.Pages[pageIndex].Visuals[componentIndex]
	if component.Kind != "visual" || strings.TrimSpace(component.Visual) == "" {
		return fmt.Errorf("%w: component %q is not a visual", ErrInvalidPayload, payload.VisualID)
	}
	visual, ok := document.Visuals[component.Visual]
	if !ok {
		return fmt.Errorf("%w: visual %q", ErrNotFound, component.Visual)
	}
	ref := FieldRef{Field: strings.TrimSpace(payload.FieldID), Alias: fieldAlias(payload.FieldID)}
	if visual.Chart != nil {
		switch payload.Role {
		case FieldRoleMeasure:
			visual.Chart.Query.Measures = appendUniqueFieldRef(visual.Chart.Query.Measures, ref)
		case FieldRoleDimension, FieldRoleDetail:
			visual.Chart.Query.Dimensions = appendUniqueFieldRef(visual.Chart.Query.Dimensions, ref)
		}
	} else if visual.Tabular != nil {
		// A new table visual has no query table until its first governed field is
		// assigned. The application resolves this table from the active semantic
		// model and carries it as non-transport metadata; never infer it from an
		// unvalidated client payload here. Existing table targets remain stable so
		// related-table dimensions can be assigned without changing the fact.
		if strings.TrimSpace(visual.Tabular.Query.Table) == "" && strings.TrimSpace(payload.ResolvedTable) != "" {
			visual.Tabular.Query.Table = strings.TrimSpace(payload.ResolvedTable)
		}
		switch payload.Role {
		case FieldRoleMeasure:
			visual.Tabular.Query.Measures = appendUniqueFieldRef(visual.Tabular.Query.Measures, ref)
		case FieldRoleDimension:
			visual.Tabular.Query.Columns = appendUniqueFieldRef(visual.Tabular.Query.Columns, ref)
		case FieldRoleDetail:
			visual.Tabular.Query.Fields = appendUniqueString(visual.Tabular.Query.Fields, ref.Field)
		}
	} else {
		return fmt.Errorf("%w: visual %q has no authored variant", ErrInvalidPayload, component.Visual)
	}
	document.Visuals[component.Visual] = visual
	return nil
}

func appendUniqueFieldRef(values []FieldRef, value FieldRef) []FieldRef {
	for _, item := range values {
		if item.Field == value.Field {
			return values
		}
	}
	return append(values, value)
}

func appendUniqueString(values []string, value string) []string {
	for _, item := range values {
		if item == value {
			return values
		}
	}
	return append(values, value)
}

func fieldAlias(value string) string {
	parts := strings.Split(strings.TrimSpace(value), ".")
	return parts[len(parts)-1]
}

func builderTitle(value string) string {
	value = strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(value), "-", " "), "_", " ")
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func nextBuilderID(prefix string, start int, exists func(string) bool) string {
	if start < 1 {
		start = 1
	}
	for index := start; ; index++ {
		candidate := fmt.Sprintf("%s-%d", prefix, index)
		if !exists(candidate) {
			return candidate
		}
	}
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

func minInt(left, right int) int {
	if right <= 0 || left < right {
		return left
	}
	return right
}

func applyMetadata(document *Dashboard, patch MetadataPatch) error {
	if patch.Title != nil {
		document.Title = *patch.Title
	}
	if patch.Description != nil {
		document.Description = *patch.Description
	}
	if patch.SemanticModel != nil {
		modelID, err := graph.NewResourceID(*patch.SemanticModel)
		if err != nil {
			return fmt.Errorf("%w: semantic model: %v", ErrInvalidPayload, err)
		}
		document.SemanticModel = modelID
	}
	if patch.Appearance != nil {
		if patch.Appearance.Icon != nil {
			icon := *patch.Appearance.Icon
			document.Appearance.Icon = &icon
		}
		if patch.Appearance.Color != nil {
			color := *patch.Appearance.Color
			document.Appearance.Color = &color
		}
	}
	// Slug and visibility do not live in Dashboard. They are applied by
	// ApplyEdit after the document mutation, where lifecycle identity is built.
	return nil
}

func removePage(document *Dashboard, pageID string) error {
	for index := range document.Pages {
		if document.Pages[index].ID == pageID {
			if len(document.Pages) <= 1 {
				return fmt.Errorf("%w: cannot remove the last page", ErrInvalidPayload)
			}
			document.Pages = append(document.Pages[:index], document.Pages[index+1:]...)
			return nil
		}
	}
	return fmt.Errorf("%w: page %q", ErrNotFound, pageID)
}

func removeVisual(document *Dashboard, visualID string) error {
	if _, exists := document.Visuals[visualID]; !exists {
		return fmt.Errorf("%w: visual %q", ErrNotFound, visualID)
	}
	for _, page := range document.Pages {
		for _, component := range page.Visuals {
			if component.Visual == visualID {
				return fmt.Errorf("%w: visual %q is referenced by page %q component %q", ErrConflict, visualID, page.ID, component.ID)
			}
		}
	}
	delete(document.Visuals, visualID)
	return nil
}

func applyLayout(document *Dashboard, payload SetLayoutPayload) error {
	pageIndex := -1
	for index := range document.Pages {
		if document.Pages[index].ID == payload.PageID {
			pageIndex = index
			break
		}
	}
	if pageIndex < 0 {
		return fmt.Errorf("%w: page %q", ErrNotFound, payload.PageID)
	}
	page := &document.Pages[pageIndex]
	components := make(map[string]struct{}, len(page.Visuals))
	for _, component := range page.Visuals {
		if strings.TrimSpace(component.ID) == "" {
			return fmt.Errorf("%w: page %q has component without id", ErrInvalidPayload, page.ID)
		}
		if _, exists := components[component.ID]; exists {
			return fmt.Errorf("%w: page %q has duplicate component %q", ErrInvalidPayload, page.ID, component.ID)
		}
		components[component.ID] = struct{}{}
	}
	if payload.Canvas != nil {
		page.Canvas = *payload.Canvas
	}
	if payload.Grid != nil {
		page.Grid = *payload.Grid
	}
	if payload.Placements != nil {
		for componentID := range payload.Placements {
			if _, exists := components[componentID]; !exists {
				return fmt.Errorf("%w: page %q has unknown component %q", ErrInvalidPayload, page.ID, componentID)
			}
		}
		// A non-nil map replaces the entire authored placement set. Missing
		// components are explicitly cleared; compiled geometry is untouched.
		// Empty placement is a valid draft state while a builder is composing a
		// page; strict placement validation remains a compiler concern.
		for index := range page.Visuals {
			placement, exists := payload.Placements[page.Visuals[index].ID]
			if !exists {
				placement = dashboardmodel.PagePlacement{}
			}
			page.Visuals[index].Placement = placement
		}
	}
	return nil
}

func applyFilters(document *Dashboard, payload SetFiltersPayload) error {
	if payload.Clear {
		document.FilterDefinitions = map[string]dashboardfilter.Definition{}
		document.FilterBindings = map[string]dashboardfilter.Binding{}
		document.FilterApplication = dashboardfilter.ApplicationPolicy{}.WithDefaults()
		return nil
	}
	document.FilterDefinitions = make(map[string]dashboardfilter.Definition, len(payload.Definitions))
	for id, definition := range payload.Definitions {
		cloned, err := cloneValueAs(definition, "filter definition")
		if err != nil {
			return err
		}
		document.FilterDefinitions[id] = cloned
	}
	document.FilterBindings = make(map[string]dashboardfilter.Binding, len(payload.Bindings))
	for id, binding := range payload.Bindings {
		cloned, err := cloneValueAs(binding, "filter binding")
		if err != nil {
			return err
		}
		document.FilterBindings[id] = cloned
	}
	if payload.Application == nil {
		document.FilterApplication = dashboardfilter.ApplicationPolicy{}.WithDefaults()
	} else {
		document.FilterApplication = payload.Application.WithDefaults()
	}
	return nil
}

func applyInteraction(document *Dashboard, payload SetInteractionPayload) error {
	visualID, err := resolveInteractionVisual(document, payload.PageID, payload.VisualID)
	if err != nil {
		return err
	}
	visual := document.Visuals[visualID]
	interaction := Interaction{}
	if !payload.Clear {
		interaction, err = cloneValueAs(*payload.Interaction, "interaction")
		if err != nil {
			return err
		}
	}
	if visual.Chart != nil {
		visual.Chart.Interaction = interaction
	} else if visual.Tabular != nil {
		visual.Tabular.Interaction = interaction
	} else {
		return fmt.Errorf("%w: visual %q has no authored variant", ErrInvalidPayload, visualID)
	}
	document.Visuals[visualID] = visual
	return nil
}

func resolveInteractionVisual(document *Dashboard, pageID, visualID string) (string, error) {
	if visualID != "" {
		if _, exists := document.Visuals[visualID]; exists {
			if pageID == "" {
				return visualID, nil
			}
			for _, page := range document.Pages {
				if page.ID != pageID {
					continue
				}
				for _, component := range page.Visuals {
					if component.Visual == visualID || component.ID == visualID {
						return visualID, nil
					}
				}
				return "", fmt.Errorf("%w: page %q does not reference visual %q", ErrConflict, pageID, visualID)
			}
			return "", fmt.Errorf("%w: page %q", ErrNotFound, pageID)
		}
		// A page component ID is accepted when PageID is supplied; the authored
		// visualization it points at remains the interaction target.
		if pageID != "" {
			for _, page := range document.Pages {
				if page.ID != pageID {
					continue
				}
				for _, component := range page.Visuals {
					if component.ID == visualID && component.Visual != "" {
						if _, exists := document.Visuals[component.Visual]; exists {
							return component.Visual, nil
						}
					}
				}
				return "", fmt.Errorf("%w: visual component %q", ErrNotFound, visualID)
			}
		}
		return "", fmt.Errorf("%w: visual %q", ErrNotFound, visualID)
	}

	// A page-only target is unambiguous only when that page contains exactly
	// one visual component. This keeps the command deterministic and prevents a
	// broad interaction edit from silently touching several visual definitions.
	for _, page := range document.Pages {
		if page.ID != pageID {
			continue
		}
		var target string
		for _, component := range page.Visuals {
			if component.Kind != "visual" || component.Visual == "" {
				continue
			}
			if target != "" {
				return "", fmt.Errorf("%w: page %q contains multiple visual targets", ErrInvalidPayload, pageID)
			}
			target = component.Visual
		}
		if target == "" {
			return "", fmt.Errorf("%w: page %q has no visual target", ErrNotFound, pageID)
		}
		return target, nil
	}
	return "", fmt.Errorf("%w: page %q", ErrNotFound, pageID)
}

func sameRevisionToken(left, right RevisionToken) bool {
	return left.RevisionID == right.RevisionID && left.Number == right.Number && left.ContentHash == right.ContentHash
}

func cloneValueAs[T any](value T, path string) (T, error) {
	cloned, err := cloneValue(reflect.ValueOf(value), path)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("%w: clone %s: %v", ErrInvalidAuthoring, path, err)
	}
	return cloned.Interface().(T), nil
}
