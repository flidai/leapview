// Package builderview exposes the governed, bounded read projection used by
// the dashboard builder. It intentionally returns signal-shaped data rather
// than an authored document or compiled runtime envelope.
package builderview

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	dashboarddocument "github.com/flidai/leapview/internal/dashboard/document"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	"github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

const (
	maxPages              = 128
	maxVisuals            = 1024
	maxFilterComponents   = 1024
	maxInteractionTargets = 1024
	maxTables             = 256
	maxFields             = 1024
	maxSlots              = 64
	maxDiagnostics        = 128
)

// Request identifies one project-scoped draft builder projection. Empty
// selections use the first deterministic page/visual fallback.
type Request struct {
	ProjectID        graph.ResourceID
	ActorID          string
	DashboardID      authoring.DashboardID
	SelectedPageID   string
	SelectedVisualID string
}

// Runtime is the only active-generation capability needed by the builder.
// SemanticModelProjection must return a detached model projection.
type Runtime interface {
	projectruntime.Runtime
	SemanticModelProjection(graph.ResourceID) (*semanticmodel.Model, bool)
}

// Options wires the read-side builder dependencies.
type Options struct {
	Provider   projectruntime.Provider
	Repository authoring.Repository
	Authorizer authoringservice.Authorizer
}

// Service builds one bounded builder projection per request.
type Service struct {
	provider   projectruntime.Provider
	repository authoring.Repository
	authorizer authoringservice.Authorizer
}

func NewService(options Options) (*Service, error) {
	if options.Provider == nil {
		return nil, fmt.Errorf("dashboard builder runtime provider is required")
	}
	if options.Repository == nil {
		return nil, fmt.Errorf("dashboard builder authoring repository is required")
	}
	if options.Authorizer == nil {
		return nil, fmt.Errorf("dashboard builder authorizer is required")
	}
	return &Service{provider: options.Provider, repository: options.Repository, authorizer: options.Authorizer}, nil
}

// Build authorizes EDIT before reading the exact draft revision. The active
// runtime lease is acquired once, after authoring identity checks, and is
// released on every path after acquisition.
func (s *Service) Build(ctx context.Context, request Request) (uisignals.DashboardBuilderSignal, error) {
	if s == nil || s.provider == nil || s.repository == nil || s.authorizer == nil {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("dashboard builder service is not configured")
	}
	projectID := request.ProjectID
	actorID := strings.TrimSpace(request.ActorID)
	if err := projectID.Validate(); err != nil {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("project ID is invalid: %w", err)
	}
	if actorID == "" {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("project and actor are required")
	}
	if err := authoring.ValidateDashboardID(request.DashboardID); err != nil {
		return uisignals.DashboardBuilderSignal{}, err
	}
	if err := ctx.Err(); err != nil {
		return uisignals.DashboardBuilderSignal{}, err
	}

	// Lifecycle identity is metadata, but the draft pointer and its document
	// remain undisclosed until this exact dashboard EDIT decision succeeds.
	lifecycle, err := s.repository.Get(ctx, projectID, request.DashboardID)
	if err != nil {
		return uisignals.DashboardBuilderSignal{}, err
	}
	if lifecycle.ProjectID != projectID || lifecycle.ID != request.DashboardID {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("dashboard builder lifecycle identity does not match request")
	}
	canEdit, err := s.authorize(ctx, actorID, projectID, lifecycle, authoring.AuthorizationActionEdit)
	if err != nil {
		return uisignals.DashboardBuilderSignal{}, err
	}
	if !canEdit {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("%w: dashboard edit is forbidden", access.ErrForbidden)
	}
	if lifecycle.Status == authoring.LifecycleStatusArchived {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("%w: dashboard is archived", authoring.ErrNotFound)
	}
	if lifecycle.Status != authoring.LifecycleStatusDraft && lifecycle.Status != authoring.LifecycleStatusPublished {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("%w: unsupported lifecycle status %q", authoring.ErrNotFound, lifecycle.Status)
	}
	if lifecycle.Draft == nil {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("%w: dashboard has no draft", authoring.ErrNotFound)
	}
	if lifecycle.Draft.DashboardID != lifecycle.ID {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("dashboard builder draft identity does not match lifecycle")
	}
	if err := lifecycle.Draft.Revision.ValidateComplete(); err != nil {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("validate current draft pointer: %w", err)
	}
	if err := lifecycle.Validate(); err != nil {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("validate dashboard lifecycle: %w", err)
	}
	capabilities, err := s.capabilities(ctx, actorID, projectID, lifecycle, canEdit)
	if err != nil {
		return uisignals.DashboardBuilderSignal{}, err
	}

	revision, err := s.repository.GetRevision(ctx, projectID, request.DashboardID, lifecycle.Draft.Revision.RevisionID)
	if err != nil {
		return uisignals.DashboardBuilderSignal{}, err
	}
	if err := revision.Validate(); err != nil {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("validate dashboard draft revision: %w", err)
	}
	if revision.Number > uint64(1<<63-1) {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("dashboard builder revision number exceeds signal range")
	}
	if revision.DashboardID != request.DashboardID || !sameRevision(revision.Token(), lifecycle.Draft.Revision) {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("dashboard builder draft revision does not match lifecycle pointer")
	}
	if revision.Document.Metadata.ID != request.DashboardID.String() {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("dashboard builder document identity does not match request")
	}
	if revision.Document.Spec.SemanticModel != lifecycle.SemanticModel.String() {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("dashboard builder semantic model does not match lifecycle")
	}

	lease, err := s.provider.Acquire(ctx)
	if err != nil {
		return uisignals.DashboardBuilderSignal{}, err
	}
	if lease == nil {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("dashboard builder runtime lease is empty")
	}
	defer lease.Release()
	identity := lease.Identity()
	if err := identity.Validate(); err != nil {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("dashboard builder serving identity does not match project: %w", err)
	}
	if identity.ProjectID != projectID {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("dashboard builder serving identity project %q does not match %q", identity.ProjectID, projectID)
	}
	if lease.Runtime() == nil {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("dashboard builder runtime is empty")
	}
	active, ok := lease.Runtime().(Runtime)
	if !ok || active == nil {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("active runtime does not provide semantic model projection")
	}
	model, ok := active.SemanticModelProjection(lifecycle.SemanticModel)
	if !ok || model == nil {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("semantic model %q is unavailable in active runtime", lifecycle.SemanticModel)
	}

	return project(request, lifecycle, revision, model, capabilities)
}

func (s *Service) authorize(ctx context.Context, actorID string, projectID graph.ResourceID, lifecycle authoring.DashboardLifecycle, action authoring.AuthorizationAction) (bool, error) {
	err := s.authorizer.Authorize(ctx, authoringservice.AuthorizationRequest{
		ActorID: actorID, ProjectID: projectID, DashboardID: lifecycle.ID,
		OwnerPrincipalID: lifecycle.OwnerPrincipalID, SemanticModel: lifecycle.SemanticModel,
		Target: authoringservice.AuthorizationTargetAuthoredDashboard, Visibility: lifecycle.Visibility, Action: action,
	})
	if err == nil {
		return true, nil
	}
	if errors.Is(err, access.ErrForbidden) {
		return false, nil
	}
	return false, err
}

func (s *Service) capabilities(ctx context.Context, actorID string, projectID graph.ResourceID, lifecycle authoring.DashboardLifecycle, canEdit bool) (uisignals.DashboardBuilderCapabilitiesSignal, error) {
	publish, err := s.authorize(ctx, actorID, projectID, lifecycle, authoring.AuthorizationActionPublish)
	if err != nil {
		return uisignals.DashboardBuilderCapabilitiesSignal{}, err
	}
	export, err := s.authorize(ctx, actorID, projectID, lifecycle, authoring.AuthorizationActionView)
	if err != nil {
		return uisignals.DashboardBuilderCapabilitiesSignal{}, err
	}
	archive, err := s.authorize(ctx, actorID, projectID, lifecycle, authoring.AuthorizationActionArchive)
	if err != nil {
		return uisignals.DashboardBuilderCapabilitiesSignal{}, err
	}
	return uisignals.DashboardBuilderCapabilitiesSignal{
		CanEdit: canEdit, CanShare: canEdit, CanPublish: publish, CanArchive: archive, CanPreview: canEdit,
		CanExport: export, CanAddPage: canEdit, CanAddVisual: canEdit,
	}, nil
}

func project(request Request, lifecycle authoring.DashboardLifecycle, revision authoring.Revision, model *semanticmodel.Model, capabilities uisignals.DashboardBuilderCapabilitiesSignal) (uisignals.DashboardBuilderSignal, error) {
	pages, diagnostics, selectedPageID, selectedVisualID, err := projectPages(revision.Document, request.SelectedPageID, request.SelectedVisualID)
	if err != nil {
		return uisignals.DashboardBuilderSignal{}, err
	}
	semantic, err := projectSemanticModel(lifecycle.SemanticModel.String(), model)
	if err != nil {
		return uisignals.DashboardBuilderSignal{}, err
	}
	if len(diagnostics) > maxDiagnostics {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("dashboard builder diagnostics exceed bounded limit")
	}

	token := revision.Token()
	draftID := ""
	if lifecycle.Draft != nil {
		draftID = lifecycle.Draft.ID.String()
	}
	dirty := lifecycle.Published == nil || !sameRevision(token, lifecycle.Published.Revision)
	sourceEvidence, err := sourceEvidenceSignalChecked(revision.Provenance)
	if err != nil {
		return uisignals.DashboardBuilderSignal{}, err
	}
	revisionValue, err := revisionSignalChecked(token)
	if err != nil {
		return uisignals.DashboardBuilderSignal{}, err
	}
	signal := uisignals.DashboardBuilderSignal{
		DashboardID: lifecycle.ID.String(), DraftID: draftID,
		Revision: revisionValue, Title: lifecycle.Title, Lifecycle: string(lifecycle.Status), Visibility: string(lifecycle.Visibility),
		HasUnpublishedChanges: dirty, Origin: originSignal(revision.Provenance), SourceEvidence: sourceEvidence,
		SemanticModel: semantic, VisualCatalog: projectVisualCatalog(), Filters: projectFilters(revision.Document), Pages: pages, Capabilities: capabilities, Diagnostics: diagnostics,
		Preview: uisignals.DashboardBuilderPreviewStateSignal{Active: false, Mode: "draft", Loading: false},
		Save:    uisignals.DashboardBuilderSaveStateSignal{State: saveState(dirty), LastSavedAt: optionalTime(revision.CreatedAt)},
	}
	if selectedPageID != "" {
		signal.SelectedPageID = &selectedPageID
	}
	if selectedVisualID != "" {
		signal.SelectedVisualID = &selectedVisualID
	}
	return signal, nil
}

func projectFilters(authored dashboarddocument.DashboardDocument) []uisignals.DashboardBuilderFilterSignal {
	filters := authored.Spec.Filters
	bindingsByFilter := make(map[string][]uisignals.DashboardBuilderFilterBindingSignal, len(filters))
	for _, page := range authored.Spec.Pages {
		if page.FilterBindings == nil {
			continue
		}
		for _, binding := range *page.FilterBindings {
			pageID := page.ID
			projected := uisignals.DashboardBuilderFilterBindingSignal{ID: binding.ID, Scope: "page", PageID: &pageID, Targets: []string{}}
			if binding.Targets != nil {
				projected.Targets = append(projected.Targets, (*binding.Targets)...)
			}
			bindingsByFilter[binding.Filter] = append(bindingsByFilter[binding.Filter], projected)
		}
	}
	result := make([]uisignals.DashboardBuilderFilterSignal, 0, len(filters))
	for _, filter := range filters {
		controlType := ""
		if projectedType, err := filter.Control.Type(); err == nil {
			controlType = projectedType
		}
		projected := uisignals.DashboardBuilderFilterSignal{
			ID: filter.ID, Label: filter.Label, Dimension: filter.Dimension, ControlType: controlType,
			Required:       filter.Required != nil && *filter.Required,
			ReaderEditable: filter.ReaderEditable == nil || *filter.ReaderEditable,
			Targets:        []string{},
			Bindings:       append([]uisignals.DashboardBuilderFilterBindingSignal(nil), bindingsByFilter[filter.ID]...),
		}
		if len(projected.Bindings) == 0 {
			reportTargets := []string{}
			if filter.Targets != nil {
				reportTargets = append(reportTargets, (*filter.Targets)...)
			}
			projected.Bindings = []uisignals.DashboardBuilderFilterBindingSignal{{ID: filter.ID, Scope: "report", Targets: reportTargets}}
		}
		sort.SliceStable(projected.Bindings, func(i, j int) bool {
			leftPage, rightPage := "", ""
			if projected.Bindings[i].PageID != nil {
				leftPage = *projected.Bindings[i].PageID
			}
			if projected.Bindings[j].PageID != nil {
				rightPage = *projected.Bindings[j].PageID
			}
			if leftPage == rightPage {
				return projected.Bindings[i].ID < projected.Bindings[j].ID
			}
			return leftPage < rightPage
		})
		if filter.Description != nil {
			projected.Description = filter.Description
		}
		if filter.Targets != nil {
			projected.Targets = append(projected.Targets, (*filter.Targets)...)
		}
		if filter.URLParameter != nil {
			projected.URLParameter = filter.URLParameter
		}
		result = append(result, projected)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func projectPages(authored dashboarddocument.DashboardDocument, requestedPageID, requestedVisualID string) ([]uisignals.DashboardBuilderPageSignal, []uisignals.DashboardBuilderDiagnosticSignal, string, string, error) {
	if len(authored.Spec.Pages) > maxPages {
		return nil, nil, "", "", fmt.Errorf("dashboard builder pages exceed bounded limit")
	}
	pages := append([]dashboarddocument.DashboardPage(nil), authored.Spec.Pages...)
	result := make([]uisignals.DashboardBuilderPageSignal, 0, len(pages))
	diagnostics := make([]uisignals.DashboardBuilderDiagnosticSignal, 0)
	visualTotal := 0
	filterComponentTotal := 0
	filterDefinitions := make(map[string]dashboarddocument.DashboardFilter, len(authored.Spec.Filters))
	for _, filter := range authored.Spec.Filters {
		filterDefinitions[filter.ID] = filter
	}
	for _, page := range pages {
		components := append([]dashboarddocument.DashboardPageComponent(nil), page.Components...)
		sort.SliceStable(components, func(i, j int) bool {
			left, _ := components[i].Base()
			right, _ := components[j].Base()
			if left == nil || right == nil {
				return i < j
			}
			return left.ID < right.ID
		})
		visuals := make([]uisignals.DashboardBuilderVisualSignal, 0, len(components))
		filterComponents := make([]uisignals.DashboardBuilderFilterComponentSignal, 0)
		seenVisualIDs := make(map[string]struct{}, len(components))
		for _, component := range components {
			if filterComponent, ok := component.Value.(*dashboarddocument.FilterDashboardPageComponent); ok {
				filterComponentTotal++
				if filterComponentTotal > maxFilterComponents {
					return nil, nil, "", "", fmt.Errorf("dashboard builder filter components exceed bounded limit")
				}
				base, err := component.Base()
				if err != nil {
					return nil, nil, "", "", err
				}
				definition, exists := filterDefinitions[filterComponent.Filter]
				if !exists {
					diagnostics = append(diagnostics, diagnostic("error", "FILTER_MISSING", fmt.Sprintf("Filter %q is missing from the authored document.", filterComponent.Filter), filterComponent.Filter))
					continue
				}
				controlType, err := definition.Control.Type()
				if err != nil {
					return nil, nil, "", "", err
				}
				placement := dashboard.PagePlacement{Col: int(base.Placement.Column), Row: int(base.Placement.Row), ColSpan: int(base.Placement.ColumnSpan), RowSpan: int(base.Placement.RowSpan)}
				filterComponents = append(filterComponents, uisignals.DashboardBuilderFilterComponentSignal{
					ID:          base.ID,
					FilterID:    filterComponent.Filter,
					Label:       display(definition.Label, definition.ID),
					ControlType: controlType,
					Placement:   uisignals.DashboardPagePlacementFromDashboard(placement),
				})
				continue
			}
			visualComponent, ok := component.Value.(*dashboarddocument.VisualDashboardPageComponent)
			if !ok || strings.TrimSpace(visualComponent.Visual) == "" {
				continue
			}
			base, err := component.Base()
			if err != nil {
				return nil, nil, "", "", err
			}
			visualTotal++
			if visualTotal > maxVisuals {
				return nil, nil, "", "", fmt.Errorf("dashboard builder visuals exceed bounded limit")
			}
			authoredVisual, ok := authored.Spec.Visuals[visualComponent.Visual]
			if !ok {
				diagnostics = append(diagnostics, diagnostic("error", "VISUAL_MISSING", fmt.Sprintf("Visual %q is missing from the authored document.", visualComponent.Visual), visualComponent.Visual))
				continue
			}
			visual, err := projectCanonicalVisual(base, visualComponent, authoredVisual)
			if err != nil {
				return nil, nil, "", "", err
			}
			// A visualization definition may be placed more than once on a
			// page. The builder edits placements, not definitions, so the
			// signal identity must be the component identity. This also keeps
			// selection deterministic when two placements reference one
			// authored visualization.
			visual.ID = base.ID
			if strings.TrimSpace(visual.ID) == "" {
				visual.ID = visualComponent.Visual
			}
			if _, exists := seenVisualIDs[visual.ID]; exists {
				// Draft validation rejects duplicate component IDs, but retain a
				// deterministic fallback for a defensive projection of malformed
				// input rather than returning duplicate signal identities.
				for suffix := 2; ; suffix++ {
					candidate := fmt.Sprintf("%s@%d", visual.ID, suffix)
					if _, used := seenVisualIDs[candidate]; !used {
						visual.ID = candidate
						break
					}
				}
			}
			seenVisualIDs[visual.ID] = struct{}{}
			visuals = append(visuals, visual)
		}
		result = append(result, uisignals.DashboardBuilderPageSignal{ID: page.ID, Title: display(page.Title, page.ID), Grid: projectCanonicalPageGrid(authored.Spec, page), Visuals: visuals, FilterComponents: filterComponents})
	}
	selectedPageID := choosePage(result, strings.TrimSpace(requestedPageID))
	selectedVisualID := chooseVisual(result, selectedPageID, strings.TrimSpace(requestedVisualID))
	return result, diagnostics, selectedPageID, selectedVisualID, nil
}

func projectCanonicalPageGrid(spec dashboarddocument.DashboardSpec, page dashboarddocument.DashboardPage) uisignals.DashboardPageGrid {
	columns, rowHeight, gap, padding := int64(12), int64(48), int64(16), int64(16)
	if spec.Layout != nil {
		columns, rowHeight, gap, padding = int64(spec.Layout.Columns), int64(spec.Layout.RowHeight), int64(spec.Layout.Gap), int64(spec.Layout.Padding)
	}
	if page.Layout != nil {
		if page.Layout.Columns != nil {
			columns = int64(*page.Layout.Columns)
		}
		if page.Layout.RowHeight != nil {
			rowHeight = int64(*page.Layout.RowHeight)
		}
		if page.Layout.Gap != nil {
			gap = int64(*page.Layout.Gap)
		}
		if page.Layout.Padding != nil {
			padding = int64(*page.Layout.Padding)
		}
	}
	return uisignals.DashboardPageGrid{Columns: columns, RowHeight: rowHeight, Gap: gap, Padding: padding}
}

func projectCanonicalVisual(base *dashboarddocument.DashboardPageComponentBase, component *dashboarddocument.VisualDashboardPageComponent, authored dashboarddocument.DashboardVisual) (uisignals.DashboardBuilderVisualSignal, error) {
	if base == nil || component == nil {
		return uisignals.DashboardBuilderVisualSignal{}, fmt.Errorf("visual component is required")
	}
	title := ""
	if authored.Title != nil {
		title = *authored.Title
	}
	slots, err := canonicalSlots(authored.Query)
	if err != nil {
		return uisignals.DashboardBuilderVisualSignal{}, err
	}
	placement := dashboard.PagePlacement{Col: int(base.Placement.Column), Row: int(base.Placement.Row), ColSpan: int(base.Placement.ColumnSpan), RowSpan: int(base.Placement.RowSpan)}
	titleVisible := true
	if authored.TitleVisible != nil {
		titleVisible = *authored.TitleVisible
	}
	legendVisible, labelsVisible, axisVisible := canonicalVisualFormatVisibility(authored)
	formatOptions, err := authoring.CanonicalVisualFormatOptions(authored)
	if err != nil {
		return uisignals.DashboardBuilderVisualSignal{}, fmt.Errorf("project visual format options: %w", err)
	}
	interaction, err := projectCanonicalInteraction(authored)
	if err != nil {
		return uisignals.DashboardBuilderVisualSignal{}, fmt.Errorf("project visual interaction: %w", err)
	}
	var datasetID *string
	if records, ok := authored.Query.Value.(*dashboarddocument.RecordsDashboardQuery); ok {
		resolved := strings.TrimSpace(records.Dataset)
		if resolved != "" && resolved != "pending_dataset" {
			datasetID = &resolved
		}
	}
	return uisignals.DashboardBuilderVisualSignal{ID: base.ID, VisualID: component.Visual, Title: display(title, component.Visual), TitleVisible: titleVisible, Type: authored.Type, DatasetID: datasetID, LegendVisible: legendVisible, AxisVisible: axisVisible, DataLabelsVisible: labelsVisible, FormatOptions: projectVisualFormatOptions(formatOptions), Placement: uisignals.DashboardPagePlacementFromDashboard(placement), Slots: slots, Filters: []string{}, Interaction: interaction}, nil
}

// projectCanonicalInteraction exposes only the small, closed subset needed by
// the visual builder. It never returns an authored interaction union: spatial,
// multiple, and malformed declarations remain visible as configured but
// non-editable metadata so the browser cannot clobber them.
func projectCanonicalInteraction(authored dashboarddocument.DashboardVisual) (*uisignals.DashboardBuilderInteractionSignal, error) {
	projection := &uisignals.DashboardBuilderInteractionSignal{
		Configured: false, Editable: false, Toggle: true,
		Mappings: []uisignals.DashboardBuilderInteractionMappingSignal{},
		Targets:  []string{}, HighlightTargets: []string{}, NoneTargets: []string{},
	}
	if authored.Interactions == nil || len(*authored.Interactions) == 0 {
		mapping, ok := inferCanonicalInteractionMapping(authored.Query)
		if ok {
			projection.Editable = true
			mode := "single"
			projection.Mode = &mode
			projection.Mappings = []uisignals.DashboardBuilderInteractionMappingSignal{mapping}
			return projection, nil
		}
		// No authored interaction and no stable selection identity: omit the
		// optional projection so the builder can present its unavailable state.
		return nil, nil
	}
	projection.Configured = true
	if len(*authored.Interactions) != 1 {
		message := "Multiple authored interactions are not editable in the builder."
		projection.Message = &message
		return projection, nil
	}
	interaction := (*authored.Interactions)[0]
	selection, ok := interaction.Value.(*dashboarddocument.SelectionDashboardInteraction)
	if !ok || selection == nil {
		message := "Spatial or unsupported authored interactions are not editable in the builder."
		projection.Message = &message
		return projection, nil
	}
	if selection.Mode != dashboarddocument.DashboardSelectionModeSingle && selection.Mode != dashboarddocument.DashboardSelectionModeMultiple {
		message := "Unsupported authored interaction mode is not editable in the builder."
		projection.Message = &message
		return projection, nil
	}
	if len(selection.Mappings) > maxSlots {
		return nil, fmt.Errorf("dashboard builder interaction mappings exceed bounded limit")
	}
	projection.Editable = true
	mode := string(selection.Mode)
	projection.Mode = &mode
	projection.Toggle = selection.Toggle
	projection.Mappings = make([]uisignals.DashboardBuilderInteractionMappingSignal, 0, len(selection.Mappings))
	for _, mapping := range selection.Mappings {
		if safeFieldID(mapping.Field) == "" || safeFieldID(mapping.Value) == "" {
			projection.Editable = false
			message := "This authored interaction contains an unsupported field mapping."
			projection.Message = &message
			projection.Mappings = []uisignals.DashboardBuilderInteractionMappingSignal{}
			return projection, nil
		}
		projected := uisignals.DashboardBuilderInteractionMappingSignal{Field: mapping.Field, Value: mapping.Value}
		if mapping.Dataset != nil {
			dataset := *mapping.Dataset
			projected.Dataset = &dataset
		}
		if mapping.Grain != nil {
			grain := string(*mapping.Grain)
			projected.Grain = &grain
		}
		if mapping.Label != nil {
			label := *mapping.Label
			projected.Label = &label
		}
		projection.Mappings = append(projection.Mappings, projected)
	}
	projection.Targets = appendBoundedInteractionTargets(projection.Targets, selection.Targets)
	projection.HighlightTargets = appendBoundedInteractionTargets(projection.HighlightTargets, selection.HighlightTargets)
	projection.NoneTargets = appendBoundedInteractionTargets(projection.NoneTargets, selection.NoneTargets)
	if len(projection.Targets) > maxInteractionTargets || len(projection.HighlightTargets) > maxInteractionTargets || len(projection.NoneTargets) > maxInteractionTargets {
		return nil, fmt.Errorf("dashboard builder interaction targets exceed bounded visual limit")
	}
	if !interactionTargetsDisjoint(projection.Targets, projection.HighlightTargets, projection.NoneTargets) {
		projection.Editable = false
		message := "This authored interaction contains overlapping target effects."
		projection.Message = &message
	}
	return projection, nil
}

func appendBoundedInteractionTargets(dst []string, targets *[]string) []string {
	if targets == nil {
		return dst
	}
	return append(dst, (*targets)...)
}

func interactionTargetsDisjoint(groups ...[]string) bool {
	seen := make(map[string]struct{})
	for _, group := range groups {
		for _, target := range group {
			if _, exists := seen[target]; exists {
				return false
			}
			seen[target] = struct{}{}
		}
	}
	return true
}

// inferCanonicalInteractionMapping follows the builder's canonical query
// field helpers, choosing the first aggregate dimension, pivot row, or record
// field. Metrics, histograms, and distributions intentionally have no default
// interaction mapping because they do not expose a stable selectable identity.
func inferCanonicalInteractionMapping(query dashboarddocument.DashboardQuery) (uisignals.DashboardBuilderInteractionMappingSignal, bool) {
	var fieldID, label, dataset string
	var grain *string
	switch value := query.Value.(type) {
	case *dashboarddocument.AggregateDashboardQuery:
		if len(value.Dimensions) == 0 {
			return uisignals.DashboardBuilderInteractionMappingSignal{}, false
		}
		fieldID, label = canonicalDimension(value.Dimensions[0])
		if reference := value.Dimensions[0].Reference; reference != nil && reference.Grain != nil {
			value := string(*reference.Grain)
			grain = &value
		}
	case *dashboarddocument.PivotDashboardQuery:
		if len(value.Rows) == 0 {
			return uisignals.DashboardBuilderInteractionMappingSignal{}, false
		}
		fieldID, label = canonicalDimension(value.Rows[0])
		if reference := value.Rows[0].Reference; reference != nil && reference.Grain != nil {
			value := string(*reference.Grain)
			grain = &value
		}
	case *dashboarddocument.RecordsDashboardQuery:
		if len(value.Fields) == 0 {
			return uisignals.DashboardBuilderInteractionMappingSignal{}, false
		}
		fieldID, label = canonicalRecordField(value.Fields[0])
		dataset = strings.TrimSpace(value.Dataset)
	default:
		return uisignals.DashboardBuilderInteractionMappingSignal{}, false
	}
	if safeFieldID(fieldID) == "" {
		return uisignals.DashboardBuilderInteractionMappingSignal{}, false
	}
	mapping := uisignals.DashboardBuilderInteractionMappingSignal{Field: fieldID, Value: fieldID}
	if dataset != "" {
		mapping.Dataset = &dataset
	}
	if grain != nil {
		mapping.Grain = grain
	}
	if safeLabel(label) && label != fieldID {
		mapping.Label = &label
	}
	return mapping, true
}

func projectVisualCatalog() []uisignals.DashboardBuilderVisualTypeSignal {
	catalog := authoring.CanonicalVisualCatalog()
	result := make([]uisignals.DashboardBuilderVisualTypeSignal, 0, len(catalog))
	for _, entry := range catalog {
		roles := make([]uisignals.DashboardBuilderFieldRoleSignal, 0, len(authoring.CanonicalVisualRoles(entry.Type)))
		for _, role := range authoring.CanonicalVisualRoles(entry.Type) {
			roles = append(roles, uisignals.DashboardBuilderFieldRoleSignal(role))
		}
		limits := make([]uisignals.DashboardBuilderVisualRoleLimitSignal, 0, len(authoring.CanonicalVisualRoleLimits(entry.Type)))
		for _, limit := range authoring.CanonicalVisualRoleLimits(entry.Type) {
			limits = append(limits, uisignals.DashboardBuilderVisualRoleLimitSignal{Role: uisignals.DashboardBuilderFieldRoleSignal(limit.Role), Minimum: limit.Minimum, Maximum: limit.Maximum})
		}
		result = append(result, uisignals.DashboardBuilderVisualTypeSignal{Type: entry.Type, Label: entry.Label, Group: entry.Group, ReferenceHref: entry.ReferenceHref, Roles: roles, RoleLimits: limits})
	}
	return result
}

func projectVisualFormatOptions(options []authoring.VisualFormatOption) []uisignals.DashboardBuilderFormatOptionSignal {
	result := make([]uisignals.DashboardBuilderFormatOptionSignal, 0, len(options))
	for _, option := range options {
		choices := make([]uisignals.DashboardBuilderFormatChoiceSignal, 0, len(option.Choices))
		for _, choice := range option.Choices {
			choices = append(choices, uisignals.DashboardBuilderFormatChoiceSignal{Value: choice.Value, Label: choice.Label})
		}
		projected := uisignals.DashboardBuilderFormatOptionSignal{Key: option.Key, Label: option.Label, Section: option.Section, Control: option.Control, Value: option.Value, Choices: choices}
		if option.Placeholder != "" {
			projected.Placeholder = &option.Placeholder
		}
		result = append(result, projected)
	}
	return result
}

func canonicalVisualFormatVisibility(visual dashboarddocument.DashboardVisual) (legendVisible, labelsVisible, axisVisible bool) {
	// Unsupported controls remain false so the browser cannot present a toggle
	// that the selected presentation cannot persist.
	legendVisible, labelsVisible, axisVisible = false, false, true
	if base, err := visual.Presentation.Base(); err == nil {
		if base.AxisVisible != nil {
			axisVisible = *base.AxisVisible
		}
	}
	switch presentation := visual.Presentation.Value.(type) {
	case *dashboarddocument.CartesianDashboardPresentation:
		legendVisible = dashboardLegendVisible(presentation.Legend)
		labelsVisible = dashboardLabelsVisible(presentation.Labels)
	case *dashboarddocument.PointDashboardPresentation:
		legendVisible = dashboardLegendVisible(presentation.Legend)
		labelsVisible = dashboardLabelsVisible(presentation.Labels)
	case *dashboarddocument.ProportionalDashboardPresentation:
		legendVisible = dashboardLegendVisible(presentation.Legend)
		labelsVisible = dashboardLabelsVisible(presentation.Labels)
	case *dashboarddocument.HierarchyDashboardPresentation:
		legendVisible = dashboardLegendVisible(presentation.Legend)
		labelsVisible = dashboardLabelsVisible(presentation.Labels)
	case *dashboarddocument.PolarDashboardPresentation:
		legendVisible = dashboardLegendVisible(presentation.Legend)
		labelsVisible = dashboardLabelsVisible(presentation.Labels)
	case *dashboarddocument.GeographicDashboardPresentation:
		labelsVisible = dashboardLabelsVisible(presentation.Labels)
	}
	return legendVisible, labelsVisible, axisVisible
}

func dashboardLegendVisible(position *dashboarddocument.DashboardLegendPosition) bool {
	return position == nil || *position != dashboarddocument.DashboardLegendPositionNone
}

func dashboardLabelsVisible(policy *dashboarddocument.DashboardLabelPolicy) bool {
	return policy == nil || policy.Density != dashboarddocument.DashboardLabelDensityHidden
}

func canonicalSlots(query dashboarddocument.DashboardQuery) ([]uisignals.DashboardBuilderVisualSlotSignal, error) {
	slots := make([]uisignals.DashboardBuilderVisualSlotSignal, 0)
	switch value := query.Value.(type) {
	case *dashboarddocument.AggregateDashboardQuery:
		for index, field := range value.Dimensions {
			id, label := canonicalDimension(field)
			slots = append(slots, slot(fmt.Sprintf("dimension-%d", index), label, "dimension", id, true))
		}
		for index, field := range value.Metrics {
			id, label := canonicalMetric(field)
			slots = append(slots, slot(fmt.Sprintf("metric-%d", index), label, "metric", id, true))
		}
	case *dashboarddocument.RecordsDashboardQuery:
		for index, field := range value.Fields {
			id, label := canonicalRecordField(field)
			slots = append(slots, slot(fmt.Sprintf("field-%d", index), label, "detail", id, false))
		}
	case *dashboarddocument.PivotDashboardQuery:
		for index, field := range value.Rows {
			id, label := canonicalDimension(field)
			slots = append(slots, slot(fmt.Sprintf("row-%d", index), label, "dimension", id, true))
		}
		for index, field := range value.Columns {
			id, label := canonicalDimension(field)
			slots = append(slots, slot(fmt.Sprintf("column-%d", index), label, "dimension", id, true))
		}
		for index, field := range value.Metrics {
			id, label := canonicalMetric(field)
			slots = append(slots, slot(fmt.Sprintf("metric-%d", index), label, "metric", id, true))
		}
	case *dashboarddocument.HistogramDashboardQuery:
		id, label := canonicalMetric(value.Field)
		slots = append(slots, slot("metric-0", label, "metric", id, true))
	case *dashboarddocument.DistributionDashboardQuery:
		id, label := canonicalMetric(value.Field)
		slots = append(slots, slot("metric-0", label, "metric", id, true))
	}
	return boundSlots(slots)
}

func canonicalDimension(value dashboarddocument.DashboardDimensionSelection) (string, string) {
	if value.String != nil {
		return *value.String, *value.String
	}
	if value.Reference != nil {
		id := value.Reference.Dimension
		if value.Reference.Alias != nil {
			return id, *value.Reference.Alias
		}
		return id, id
	}
	return "", ""
}
func canonicalMetric(value dashboarddocument.DashboardMetricSelection) (string, string) {
	if value.String != nil {
		return *value.String, *value.String
	}
	if value.Reference != nil {
		id := value.Reference.Metric
		if value.Reference.Alias != nil {
			return id, *value.Reference.Alias
		}
		return id, id
	}
	return "", ""
}
func canonicalRecordField(value dashboarddocument.DashboardRecordFieldSelection) (string, string) {
	if value.String != nil {
		return *value.String, *value.String
	}
	if value.Reference != nil {
		id := value.Reference.Field
		if value.Reference.Alias != nil {
			return id, *value.Reference.Alias
		}
		return id, id
	}
	return "", ""
}

func boundSlots(slots []uisignals.DashboardBuilderVisualSlotSignal) ([]uisignals.DashboardBuilderVisualSlotSignal, error) {
	if len(slots) > maxSlots {
		return nil, fmt.Errorf("dashboard builder visual slots exceed bounded limit")
	}
	if slots == nil {
		slots = []uisignals.DashboardBuilderVisualSlotSignal{}
	}
	return slots, nil
}

func slot(id, label, kind, fieldID string, required bool) uisignals.DashboardBuilderVisualSlotSignal {
	fieldID = safeFieldID(fieldID)
	if fieldID == "" {
		label = "Field"
	} else if !safeLabel(label) {
		label = fieldID
	}
	value := uisignals.DashboardBuilderVisualSlotSignal{ID: id, Label: display(label, fieldID), Kind: kind, Required: required}
	if fieldID != "" {
		value.FieldID = &fieldID
	}
	return value
}

// safeFieldID admits only the governed semantic identifier alphabet used by
// AssignFieldPayload. In particular, authored expressions, SQL snippets, and
// renderer-only aliases are never copied into a clickable builder signal.
func safeFieldID(value string) string {
	value = strings.TrimSpace(value)
	if !authoring.ValidGovernedFieldID(value) {
		return ""
	}
	return value
}

func safeLabel(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || strings.ContainsAny(value, "();'\"`") || strings.Contains(value, "--") {
		return false
	}
	for _, keyword := range []string{"select ", " from ", " where ", " join ", "drop ", "union "} {
		if strings.Contains(value, keyword) {
			return false
		}
	}
	return true
}

func projectSemanticModel(modelID string, model *semanticmodel.Model) (uisignals.DashboardBuilderSemanticModelSignal, error) {
	if model == nil {
		return uisignals.DashboardBuilderSemanticModelSignal{}, fmt.Errorf("dashboard builder semantic model is empty")
	}
	tableIDs := make(map[string]struct{}, len(model.Tables))
	for id := range model.Tables {
		tableIDs[id] = struct{}{}
	}
	for _, dimension := range model.Dimensions {
		for table := range dimension.Bindings {
			tableIDs[table] = struct{}{}
		}
	}
	for _, metric := range model.Metrics {
		if strings.TrimSpace(metric.Dataset) != "" {
			tableIDs[metric.Dataset] = struct{}{}
		}
	}
	tables := make([]string, 0, len(tableIDs))
	for id := range tableIDs {
		if strings.TrimSpace(id) != "" {
			tables = append(tables, id)
		}
	}
	sort.Strings(tables)
	if len(tables) > maxTables {
		return uisignals.DashboardBuilderSemanticModelSignal{}, fmt.Errorf("dashboard builder semantic tables exceed bounded limit")
	}
	result := make([]uisignals.DashboardBuilderDatasetSignal, 0, len(tables))
	totalFields := 0
	for _, tableID := range tables {
		fields := make([]uisignals.DashboardBuilderFieldSignal, 0)
		for id, dimension := range model.Dimensions {
			_, ok := dimension.Bindings[tableID]
			if !ok {
				continue
			}
			fieldID := id
			// Semantic dimensions are referenced by their model member IDs in
			// aggregate and pivot query selections. Their physical binding is
			// execution metadata and must not leak into the assign_field payload
			// (e.g. `orders.status` is not accepted where `status` is required).
			if strings.TrimSpace(dimension.Name) != "" {
				fieldID = dimension.Name
			}
			if projected, ok := fieldSignal(fieldID, display(dimension.Label, fieldID), "dimension", uisignals.DashboardBuilderFieldRoleSignalDimension, dimension.Type, dimension.Description); ok {
				projected.DatasetID = uisignals.Pointer(tableID)
				fields = append(fields, projected)
			}
		}
		if table, ok := model.Tables[tableID]; ok {
			for id, dimension := range table.Dimensions {
				fieldID := tableID + "." + id
				if strings.TrimSpace(dimension.Field) != "" {
					fieldID = dimension.Field
				}
				if !containsFieldRole(fields, fieldID, uisignals.DashboardBuilderFieldRoleSignalDetail) {
					if projected, ok := fieldSignal(fieldID, display(dimension.Label, id), "dimension", uisignals.DashboardBuilderFieldRoleSignalDetail, dimension.Type, dimension.Description); ok {
						projected.DatasetID = uisignals.Pointer(tableID)
						fields = append(fields, projected)
					}
				}
			}
		}
		for id, metric := range model.Metrics {
			if strings.TrimSpace(metric.Dataset) != tableID {
				continue
			}
			fieldID := id
			if projected, ok := fieldSignal(fieldID, display(metric.Label, id), "metric", uisignals.DashboardBuilderFieldRoleSignalMetric, "number", metric.Description); ok {
				projected.DatasetID = uisignals.Pointer(tableID)
				fields = append(fields, projected)
			}
		}
		sort.SliceStable(fields, func(i, j int) bool {
			if fields[i].ID == fields[j].ID {
				return fields[i].Kind < fields[j].Kind
			}
			return fields[i].ID < fields[j].ID
		})
		if len(fields) > maxFields {
			return uisignals.DashboardBuilderSemanticModelSignal{}, fmt.Errorf("dashboard builder fields exceed bounded limit")
		}
		totalFields += len(fields)
		if totalFields > maxFields {
			return uisignals.DashboardBuilderSemanticModelSignal{}, fmt.Errorf("dashboard builder fields exceed bounded limit")
		}
		result = append(result, uisignals.DashboardBuilderDatasetSignal{ID: tableID, Title: display(model.Tables[tableID].Description, tableID), Fields: fields})
	}
	if result == nil {
		result = []uisignals.DashboardBuilderDatasetSignal{}
	}
	return uisignals.DashboardBuilderSemanticModelSignal{ID: modelID, Title: display(model.Title, model.Name), Datasets: result}, nil
}

func fieldSignal(id, label, kind string, role uisignals.DashboardBuilderFieldRoleSignal, dataType, description string) (uisignals.DashboardBuilderFieldSignal, bool) {
	id = safeFieldID(id)
	if id == "" {
		return uisignals.DashboardBuilderFieldSignal{}, false
	}
	if !safeLabel(label) {
		label = id
	}
	field := uisignals.DashboardBuilderFieldSignal{ID: id, Label: display(label, id), Kind: kind, Roles: []uisignals.DashboardBuilderFieldRoleSignal{role}, DataType: safeDataType(dataType)}
	if strings.TrimSpace(description) != "" && safeLabel(description) {
		field.Description = &description
	}
	return field, true
}

func safeDataType(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !safeLabel(value) {
		return "unknown"
	}
	return value
}

func containsFieldRole(fields []uisignals.DashboardBuilderFieldSignal, id string, role uisignals.DashboardBuilderFieldRoleSignal) bool {
	for _, field := range fields {
		if field.ID != id {
			continue
		}
		for _, candidate := range field.Roles {
			if candidate == role {
				return true
			}
		}
	}
	return false
}

func originSignal(provenance authoring.Provenance) uisignals.DashboardBuilderOriginSignal {
	kind := string(provenance.Origin)
	label := map[string]string{"file": "Project file", "ui": "UI", "agent": "Agent"}[kind]
	if label == "" {
		label = kind
	}
	result := uisignals.DashboardBuilderOriginSignal{Kind: kind, Label: label}
	if provenance.Source != nil && strings.TrimSpace(provenance.Source.Path) != "" {
		path := strings.TrimSpace(provenance.Source.Path)
		result.SourcePath = &path
	}
	return result
}

func sourceEvidenceSignal(provenance authoring.Provenance) *uisignals.DashboardBuilderSourceEvidenceSignal {
	signal, _ := sourceEvidenceSignalChecked(provenance)
	return signal
}

func sourceEvidenceSignalChecked(provenance authoring.Provenance) (*uisignals.DashboardBuilderSourceEvidenceSignal, error) {
	if provenance.ForkedFrom == nil {
		return nil, nil
	}
	switch provenance.ForkedFrom.Kind {
	case authoring.ForkSourceProject:
		if provenance.ForkedFrom.Project == nil || provenance.ForkedFrom.Project.Identity == (graph.ServingIdentity{}) {
			return nil, nil
		}
		item := &uisignals.DashboardBuilderProjectSourceEvidence{
			Kind: "project", ProjectID: provenance.ForkedFrom.Project.SourceProjectID.String(),
			DashboardID: provenance.ForkedFrom.Project.SourceDashboardID.String(), GenerationID: provenance.ForkedFrom.Project.Identity.GenerationID,
		}
		if path := strings.TrimSpace(provenance.ForkedFrom.Project.Path); path != "" {
			item.Path = &path
		}
		return &uisignals.DashboardBuilderSourceEvidenceSignal{Value: item}, nil
	default:
		return nil, nil
	}
}

func revisionSignal(token authoring.RevisionToken) uisignals.DashboardBuilderRevisionSignal {
	return uisignals.DashboardBuilderRevisionSignal{ID: token.RevisionID.String(), Number: int64(token.Number), ContentHash: token.ContentHash}
}

func revisionSignalChecked(token authoring.RevisionToken) (uisignals.DashboardBuilderRevisionSignal, error) {
	if token.Number > uint64(1<<63-1) {
		return uisignals.DashboardBuilderRevisionSignal{}, fmt.Errorf("dashboard builder revision number exceeds signal range")
	}
	return revisionSignal(token), nil
}

func sameRevision(left, right authoring.RevisionToken) bool {
	return left.RevisionID == right.RevisionID && left.Number == right.Number && left.ContentHash == right.ContentHash
}

func choosePage(pages []uisignals.DashboardBuilderPageSignal, requested string) string {
	if requested != "" {
		for _, page := range pages {
			if page.ID == requested {
				return requested
			}
		}
	}
	if len(pages) > 0 {
		return pages[0].ID
	}
	return ""
}

func chooseVisual(pages []uisignals.DashboardBuilderPageSignal, pageID, requested string) string {
	for _, page := range pages {
		if page.ID != pageID || len(page.Visuals) == 0 {
			continue
		}
		if requested != "" {
			for _, visual := range page.Visuals {
				if visual.ID == requested {
					return requested
				}
			}
		}
		return page.Visuals[0].ID
	}
	return ""
}

func diagnostic(severity, code, message, target string) uisignals.DashboardBuilderDiagnosticSignal {
	result := uisignals.DashboardBuilderDiagnosticSignal{Severity: severity, Code: code, Message: message}
	if strings.TrimSpace(target) != "" {
		target = strings.TrimSpace(target)
		result.TargetID = &target
	}
	return result
}

func saveState(dirty bool) string {
	if dirty {
		return "dirty"
	}
	return "saved"
}

func optionalTime(value interface {
	IsZero() bool
	Format(string) string
}) *string {
	if value == nil || value.IsZero() {
		return nil
	}
	formatted := value.Format("2006-01-02T15:04:05.999999999Z07:00")
	return &formatted
}

func display(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
