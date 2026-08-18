package authoring

import (
	"fmt"
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
	value.Spec.Pages[pageIndex].Components = append(value.Spec.Pages[pageIndex].Components, document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: componentID, Type: "visual", Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 12, RowSpan: 4}}, Type: "visual", Visual: visualID}})
	return nil
}

func canonicalVisualTypeSupported(value document.DashboardVisualType) bool {
	switch value {
	case document.DashboardVisualTypeLine, document.DashboardVisualTypeArea, document.DashboardVisualTypeBar, document.DashboardVisualTypeColumn, document.DashboardVisualTypePie, document.DashboardVisualTypeDonut, document.DashboardVisualTypeScatter, document.DashboardVisualTypeFunnel, document.DashboardVisualTypeTreemap, document.DashboardVisualTypeGauge, document.DashboardVisualTypeHeatmap, document.DashboardVisualTypeSankey, document.DashboardVisualTypeGraph, document.DashboardVisualTypeMap, document.DashboardVisualTypeCandlestick, document.DashboardVisualTypeBoxplot, document.DashboardVisualTypeCombo, document.DashboardVisualTypeWaterfall, document.DashboardVisualTypeHistogram, document.DashboardVisualTypeRadar, document.DashboardVisualTypeTree, document.DashboardVisualTypeSunburst, document.DashboardVisualTypeKpi, document.DashboardVisualTypeTable, document.DashboardVisualTypeMatrix, document.DashboardVisualTypePivot:
		return true
	default:
		return false
	}
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
	case document.DashboardVisualTypeTable, document.DashboardVisualTypeMatrix:
		query.Value = &document.RecordsDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "records"}, Type: "records", Dataset: "pending_dataset", Fields: []document.DashboardRecordFieldSelection{}}
		presentation.Value = &document.TableDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "table"}, Type: "table", RowHeight: 32, ShowHeader: true, Striped: false}
	case document.DashboardVisualTypePivot:
		query.Value = &document.PivotDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "pivot"}, Type: "pivot", Rows: []document.DashboardDimensionSelection{}, Columns: []document.DashboardDimensionSelection{}, Metrics: []document.DashboardMetricSelection{}}
		presentation.Value = &document.TableDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "table"}, Type: "table", RowHeight: 32, ShowHeader: true, Striped: false}
	case document.DashboardVisualTypeKpi:
		query.Value = &document.AggregateDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{}, Metrics: []document.DashboardMetricSelection{}}
		presentation.Value = &document.KPIDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "kpi"}, Type: "kpi"}
	case document.DashboardVisualTypePie, document.DashboardVisualTypeDonut, document.DashboardVisualTypeFunnel:
		query.Value = &document.AggregateDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{}, Metrics: []document.DashboardMetricSelection{}}
		presentation.Value = &document.ProportionalDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "proportional"}, Type: "proportional"}
	case document.DashboardVisualTypeTreemap, document.DashboardVisualTypeTree, document.DashboardVisualTypeSunburst:
		query.Value = &document.AggregateDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{}, Metrics: []document.DashboardMetricSelection{}}
		presentation.Value = &document.HierarchyDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "hierarchy"}, Type: "hierarchy"}
	case document.DashboardVisualTypeRadar:
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
		if strings.TrimSpace(query.Dataset) == "pending_dataset" && strings.TrimSpace(patch.ResolvedTable) != "" {
			query.Dataset = strings.TrimSpace(patch.ResolvedTable)
		}
		ref := patch.FieldID
		for _, existing := range query.Fields {
			id, _ := canonicalRecordSelection(existing)
			if id == ref {
				return nil
			}
		}
		query.Fields = append(query.Fields, document.DashboardRecordFieldSelection{String: &ref})
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
