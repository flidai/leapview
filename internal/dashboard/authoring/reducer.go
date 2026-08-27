package authoring

import (
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
	case *PublishPayload, *ArchivePayload:
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
	case *RemovePagePayload:
		for index, page := range value.Spec.Pages {
			if page.ID == patch.PageID {
				if len(value.Spec.Pages) <= 1 {
					return fmt.Errorf("%w: cannot remove the last page", ErrInvalidPayload)
				}
				value.Spec.Pages = append(value.Spec.Pages[:index], value.Spec.Pages[index+1:]...)
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
		if _, ok := value.Spec.Visuals[patch.VisualID]; !ok {
			return fmt.Errorf("%w: visual %q", ErrNotFound, patch.VisualID)
		}
		for pageIndex := range value.Spec.Pages {
			components := value.Spec.Pages[pageIndex].Components
			for _, component := range components {
				visual, ok := component.Value.(*document.VisualDashboardPageComponent)
				if ok && visual.Visual == patch.VisualID {
					base, _ := component.Base()
					return fmt.Errorf("%w: visual %q is referenced by page %q component %q", ErrConflict, patch.VisualID, value.Spec.Pages[pageIndex].ID, base.ID)
				}
			}
		}
		delete(value.Spec.Visuals, patch.VisualID)
		return nil
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
		return nil
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
	default:
		return fmt.Errorf("%w: unsupported payload %T", ErrInvalidPayload, payload)
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
	placement := nextCanonicalVisualPlacement(*value, pageIndex)
	value.Spec.Pages[pageIndex].Components = append(value.Spec.Pages[pageIndex].Components, document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: componentID, Type: "visual", Placement: placement}, Type: "visual", Visual: visualID}})
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

func nextCanonicalVisualPlacement(value document.DashboardDocument, pageIndex int) document.DashboardPlacement {
	const (
		defaultColumnSpan int32 = 12
		rowSpan           int32 = 4
	)
	columns := defaultColumnSpan
	if value.Spec.Layout != nil && value.Spec.Layout.Columns > 0 {
		columns = value.Spec.Layout.Columns
	}
	page := value.Spec.Pages[pageIndex]
	if page.Layout != nil && page.Layout.Columns != nil && *page.Layout.Columns > 0 {
		columns = *page.Layout.Columns
	}
	columnSpan := minPositive(columns, defaultColumnSpan)
	row := int32(1)
	for {
		candidate := document.DashboardPlacement{Column: 1, Row: row, ColumnSpan: columnSpan, RowSpan: rowSpan}
		moved := false
		for _, component := range page.Components {
			base, err := component.Base()
			if err != nil || base == nil {
				continue
			}
			if placementsOverlap(candidate, base.Placement) {
				bottom := base.Placement.Row + maxPositive(base.Placement.RowSpan, 1)
				if bottom > row {
					row = bottom
				}
				moved = true
				break
			}
		}
		if !moved {
			return candidate
		}
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
	return document.DashboardVisual{Type: visualType, Title: &title, Query: query, Presentation: presentation}
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
