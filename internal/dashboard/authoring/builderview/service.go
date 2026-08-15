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
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	"github.com/flidai/leapview/internal/runtimehost"
)

const (
	maxPages       = 128
	maxVisuals     = 1024
	maxTables      = 256
	maxFields      = 1024
	maxSlots       = 64
	maxDiagnostics = 128
)

// Request identifies one workspace-scoped draft builder projection. Empty
// selections use the first deterministic page/visual fallback.
type Request struct {
	WorkspaceID      string
	ActorID          string
	DashboardID      authoring.DashboardID
	SelectedPageID   string
	SelectedVisualID string
}

// Runtime is the only active-generation capability needed by the builder.
// SemanticModelProjection must return a detached model projection.
type Runtime interface {
	runtimehost.Runtime
	SemanticModelProjection(string) (*semanticmodel.Model, bool)
}

// Options wires the read-side builder dependencies.
type Options struct {
	Provider   runtimehost.Provider
	Repository authoring.Repository
	Authorizer authoringservice.Authorizer
}

// Service builds one bounded builder projection per request.
type Service struct {
	provider   runtimehost.Provider
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
	workspaceID := strings.TrimSpace(request.WorkspaceID)
	actorID := strings.TrimSpace(request.ActorID)
	if workspaceID == "" || actorID == "" {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("workspace and actor are required")
	}
	if err := request.DashboardID.Validate(); err != nil {
		return uisignals.DashboardBuilderSignal{}, err
	}
	if err := ctx.Err(); err != nil {
		return uisignals.DashboardBuilderSignal{}, err
	}

	// Lifecycle identity is metadata, but the draft pointer and its document
	// remain undisclosed until this exact dashboard EDIT decision succeeds.
	lifecycle, err := s.repository.Get(ctx, workspaceID, request.DashboardID)
	if err != nil {
		return uisignals.DashboardBuilderSignal{}, err
	}
	if lifecycle.WorkspaceID != workspaceID || lifecycle.ID != request.DashboardID {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("dashboard builder lifecycle identity does not match request")
	}
	canEdit, err := s.authorize(ctx, actorID, workspaceID, lifecycle, authoring.AuthorizationActionEdit)
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
	capabilities, err := s.capabilities(ctx, actorID, workspaceID, lifecycle, canEdit)
	if err != nil {
		return uisignals.DashboardBuilderSignal{}, err
	}

	revision, err := s.repository.GetRevision(ctx, workspaceID, request.DashboardID, lifecycle.Draft.Revision.RevisionID)
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
	if revision.Document.ID != request.DashboardID.String() {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("dashboard builder document identity does not match request")
	}
	if strings.TrimSpace(revision.Document.SemanticModel) != strings.TrimSpace(lifecycle.SemanticModel) {
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
	if strings.TrimSpace(string(lease.ServingStateID())) == "" {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("dashboard builder serving-state identity is empty")
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
	if strings.TrimSpace(model.Name) != strings.TrimSpace(lifecycle.SemanticModel) {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("semantic model projection identity does not match lifecycle")
	}

	return project(request, lifecycle, revision, model, capabilities)
}

func (s *Service) authorize(ctx context.Context, actorID, workspaceID string, lifecycle authoring.DashboardLifecycle, action authoring.AuthorizationAction) (bool, error) {
	err := s.authorizer.Authorize(ctx, authoringservice.AuthorizationRequest{
		ActorID: actorID, WorkspaceID: workspaceID, DashboardID: lifecycle.ID,
		OwnerPrincipalID: lifecycle.OwnerPrincipalID, SemanticModel: lifecycle.SemanticModel, Action: action,
	})
	if err == nil {
		return true, nil
	}
	if errors.Is(err, access.ErrForbidden) {
		return false, nil
	}
	return false, err
}

func (s *Service) capabilities(ctx context.Context, actorID, workspaceID string, lifecycle authoring.DashboardLifecycle, canEdit bool) (uisignals.DashboardBuilderCapabilitiesSignal, error) {
	publish, err := s.authorize(ctx, actorID, workspaceID, lifecycle, authoring.AuthorizationActionPublish)
	if err != nil {
		return uisignals.DashboardBuilderCapabilitiesSignal{}, err
	}
	export, err := s.authorize(ctx, actorID, workspaceID, lifecycle, authoring.AuthorizationActionView)
	if err != nil {
		return uisignals.DashboardBuilderCapabilitiesSignal{}, err
	}
	return uisignals.DashboardBuilderCapabilitiesSignal{
		CanEdit: canEdit, CanShare: canEdit, CanPublish: publish, CanPreview: canEdit,
		CanExport: export, CanAddPage: canEdit, CanAddVisual: canEdit,
	}, nil
}

func project(request Request, lifecycle authoring.DashboardLifecycle, revision authoring.Revision, model *semanticmodel.Model, capabilities uisignals.DashboardBuilderCapabilitiesSignal) (uisignals.DashboardBuilderSignal, error) {
	pages, diagnostics, selectedPageID, selectedVisualID, err := projectPages(revision.Document, request.SelectedPageID, request.SelectedVisualID)
	if err != nil {
		return uisignals.DashboardBuilderSignal{}, err
	}
	semantic, err := projectSemanticModel(model)
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
		WorkspaceID: lifecycle.WorkspaceID, DashboardID: lifecycle.ID.String(), DraftID: draftID,
		Revision: revisionValue, Title: lifecycle.Title, Lifecycle: string(lifecycle.Status), Visibility: string(lifecycle.Visibility),
		HasUnpublishedChanges: dirty, Origin: originSignal(revision.Provenance), SourceEvidence: sourceEvidence,
		SemanticModel: semantic, Pages: pages, Capabilities: capabilities, Diagnostics: diagnostics,
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

func projectPages(document authoring.Dashboard, requestedPageID, requestedVisualID string) ([]uisignals.DashboardBuilderPageSignal, []uisignals.DashboardBuilderDiagnosticSignal, string, string, error) {
	if len(document.Pages) > maxPages {
		return nil, nil, "", "", fmt.Errorf("dashboard builder pages exceed bounded limit")
	}
	pages := append([]dashboard.Page(nil), document.Pages...)
	sort.SliceStable(pages, func(i, j int) bool { return pages[i].ID < pages[j].ID })
	result := make([]uisignals.DashboardBuilderPageSignal, 0, len(pages))
	diagnostics := make([]uisignals.DashboardBuilderDiagnosticSignal, 0)
	visualTotal := 0
	for _, page := range pages {
		page = page.WithDefaults()
		components := append([]dashboard.PageVisual(nil), page.Visuals...)
		sort.SliceStable(components, func(i, j int) bool {
			if components[i].Visual == components[j].Visual {
				return components[i].ID < components[j].ID
			}
			return components[i].Visual < components[j].Visual
		})
		visuals := make([]uisignals.DashboardBuilderVisualSignal, 0, len(components))
		seenVisualIDs := make(map[string]struct{}, len(components))
		for _, component := range components {
			if component.Kind != "visual" || strings.TrimSpace(component.Visual) == "" {
				continue
			}
			visualTotal++
			if visualTotal > maxVisuals {
				return nil, nil, "", "", fmt.Errorf("dashboard builder visuals exceed bounded limit")
			}
			authored, ok := document.Visuals[component.Visual]
			if !ok {
				diagnostics = append(diagnostics, diagnostic("error", "VISUAL_MISSING", fmt.Sprintf("Visual %q is missing from the authored document.", component.Visual), component.Visual))
				continue
			}
			visual, err := projectVisual(component, authored)
			if err != nil {
				return nil, nil, "", "", err
			}
			// A visualization definition may be placed more than once on a
			// page. The builder edits placements, not definitions, so the
			// signal identity must be the component identity. This also keeps
			// selection deterministic when two placements reference one
			// authored visualization.
			visual.ID = component.ID
			if strings.TrimSpace(visual.ID) == "" {
				visual.ID = component.Visual
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
		result = append(result, uisignals.DashboardBuilderPageSignal{ID: page.ID, Title: display(page.Title, page.ID), Canvas: uisignals.DashboardPageCanvasFromDashboard(page.Canvas), Grid: uisignals.DashboardPageGridFromDashboard(page.Grid), Visuals: visuals})
	}
	selectedPageID := choosePage(result, strings.TrimSpace(requestedPageID))
	selectedVisualID := chooseVisual(result, selectedPageID, strings.TrimSpace(requestedVisualID))
	return result, diagnostics, selectedPageID, selectedVisualID, nil
}

func projectVisual(component dashboard.PageVisual, authored authoring.AuthoringVisualization) (uisignals.DashboardBuilderVisualSignal, error) {
	visualType := strings.TrimSpace(authored.Type)
	title := strings.TrimSpace(component.Title)
	if authored.Chart != nil {
		if title == "" {
			title = authored.Chart.Title
		}
		slots, err := chartSlots(authored.Chart.Query)
		if err != nil {
			return uisignals.DashboardBuilderVisualSignal{}, err
		}
		return uisignals.DashboardBuilderVisualSignal{ID: component.Visual, VisualID: component.Visual, Title: display(title, component.Visual), Type: visualType, Placement: uisignals.DashboardPagePlacementFromDashboard(component.Placement), Slots: slots, Filters: []string{}}, nil
	}
	if authored.Tabular != nil {
		if title == "" {
			title = authored.Tabular.Title
		}
		slots, err := tableSlots(authored.Tabular.Query)
		if err != nil {
			return uisignals.DashboardBuilderVisualSignal{}, err
		}
		return uisignals.DashboardBuilderVisualSignal{ID: component.Visual, VisualID: component.Visual, Title: display(title, component.Visual), Type: visualType, Placement: uisignals.DashboardPagePlacementFromDashboard(component.Placement), Slots: slots, Filters: []string{}}, nil
	}
	return uisignals.DashboardBuilderVisualSignal{}, fmt.Errorf("visual %q has no authoring variant", component.Visual)
}

func chartSlots(query authoring.VisualQuery) ([]uisignals.DashboardBuilderVisualSlotSignal, error) {
	slots := make([]uisignals.DashboardBuilderVisualSlotSignal, 0, len(query.Dimensions)+len(query.Measures)+2)
	for index, field := range query.Dimensions {
		slots = append(slots, slot(fmt.Sprintf("dimension-%d", index), display(field.Alias, field.Field), "dimension", field.Field, true))
	}
	if query.Series.Field != "" {
		slots = append(slots, slot("series", display(query.Series.Alias, query.Series.Field), "category", query.Series.Field, false))
	}
	if query.Time.Field != "" {
		slots = append(slots, slot("time", display(query.Time.Alias, query.Time.Field), "category", query.Time.Field, false))
	}
	for index, field := range query.Measures {
		slots = append(slots, slot(fmt.Sprintf("measure-%d", index), display(field.Alias, field.Field), "measure", field.Field, true))
	}
	return boundSlots(slots)
}

func tableSlots(query authoring.TableQuery) ([]uisignals.DashboardBuilderVisualSlotSignal, error) {
	slots := make([]uisignals.DashboardBuilderVisualSlotSignal, 0, len(query.Columns)+len(query.Rows)+len(query.Measures))
	for index, field := range query.Columns {
		slots = append(slots, slot(fmt.Sprintf("column-%d", index), display(field.Alias, field.Field), "dimension", field.Field, true))
	}
	for index, field := range query.Rows {
		slots = append(slots, slot(fmt.Sprintf("row-%d", index), display(field.Alias, field.Field), "detail", field.Field, false))
	}
	for index, field := range query.Measures {
		slots = append(slots, slot(fmt.Sprintf("measure-%d", index), display(field.Alias, field.Field), "measure", field.Field, true))
	}
	for index, field := range query.Fields {
		slots = append(slots, slot(fmt.Sprintf("field-%d", index), field, "detail", field, false))
	}
	return boundSlots(slots)
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

func projectSemanticModel(model *semanticmodel.Model) (uisignals.DashboardBuilderSemanticModelSignal, error) {
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
	for _, measure := range model.Measures {
		if strings.TrimSpace(measure.Fact) != "" {
			tableIDs[measure.Fact] = struct{}{}
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
	result := make([]uisignals.DashboardBuilderTableSignal, 0, len(tables))
	totalFields := 0
	for _, tableID := range tables {
		fields := make([]uisignals.DashboardBuilderFieldSignal, 0)
		for id, dimension := range model.Dimensions {
			binding, ok := dimension.Bindings[tableID]
			if !ok {
				continue
			}
			fieldID := id
			if strings.TrimSpace(binding.Field) != "" {
				fieldID = binding.Field
			} else if strings.TrimSpace(dimension.Name) != "" {
				fieldID = dimension.Name
			}
			if projected, ok := fieldSignal(fieldID, display(dimension.Label, fieldID), "dimension", dimension.Type, dimension.Description); ok {
				fields = append(fields, projected)
			}
		}
		if table, ok := model.Tables[tableID]; ok {
			for id, dimension := range table.Dimensions {
				fieldID := tableID + "." + id
				if strings.TrimSpace(dimension.Field) != "" {
					fieldID = dimension.Field
				}
				if !containsField(fields, fieldID) {
					if projected, ok := fieldSignal(fieldID, display(dimension.Label, id), "dimension", dimension.Type, dimension.Description); ok {
						fields = append(fields, projected)
					}
				}
			}
		}
		for id, measure := range model.Measures {
			if strings.TrimSpace(measure.Fact) != tableID {
				continue
			}
			fieldID := id
			if strings.TrimSpace(measure.Field) != "" {
				fieldID = measure.Field
			}
			if projected, ok := fieldSignal(fieldID, display(measure.Label, id), "measure", "number", measure.Description); ok {
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
		result = append(result, uisignals.DashboardBuilderTableSignal{ID: tableID, Title: display(model.Tables[tableID].Description, tableID), Fields: fields})
	}
	if result == nil {
		result = []uisignals.DashboardBuilderTableSignal{}
	}
	return uisignals.DashboardBuilderSemanticModelSignal{ID: model.Name, Title: display(model.Title, model.Name), Tables: result}, nil
}

func fieldSignal(id, label, kind, dataType, description string) (uisignals.DashboardBuilderFieldSignal, bool) {
	id = safeFieldID(id)
	if id == "" {
		return uisignals.DashboardBuilderFieldSignal{}, false
	}
	if !safeLabel(label) {
		label = id
	}
	field := uisignals.DashboardBuilderFieldSignal{ID: id, Label: display(label, id), Kind: kind, DataType: safeDataType(dataType)}
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

func containsField(fields []uisignals.DashboardBuilderFieldSignal, id string) bool {
	for _, field := range fields {
		if field.ID == id {
			return true
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
	case authoring.ForkSourceWorkspace:
		if provenance.ForkedFrom.Workspace == nil {
			return nil, nil
		}
		revision, err := revisionSignalChecked(provenance.ForkedFrom.Workspace.SourceRevision)
		if err != nil {
			return nil, fmt.Errorf("dashboard builder source workspace revision: %w", err)
		}
		item := &uisignals.DashboardBuilderWorkspaceSourceEvidence{
			Kind: "workspace", WorkspaceID: provenance.ForkedFrom.Workspace.SourceWorkspaceID,
			DashboardID: provenance.ForkedFrom.Workspace.SourceDashboardID.String(), Revision: revision,
		}
		return &uisignals.DashboardBuilderSourceEvidenceSignal{Value: item}, nil
	case authoring.ForkSourceProject:
		if provenance.ForkedFrom.Project == nil || strings.TrimSpace(provenance.ForkedFrom.Project.ServingStateID) == "" {
			return nil, nil
		}
		item := &uisignals.DashboardBuilderProjectSourceEvidence{
			Kind: "project", WorkspaceID: provenance.ForkedFrom.Project.SourceWorkspaceID,
			DashboardID: provenance.ForkedFrom.Project.SourceDashboardID.String(), ServingStateID: provenance.ForkedFrom.Project.ServingStateID,
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
