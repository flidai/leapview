package module

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringapplication "github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/builderview"
	"github.com/flidai/leapview/internal/dashboard/document"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func (m *Module) ResolveTurnContext(r *http.Request, scope agent.Scope, candidate agent.TurnContext) (agent.TurnContext, error) {
	if len(candidate.References) > agent.MaxTurnReferences {
		return agent.TurnContext{}, fmt.Errorf("at most %d references can be attached", agent.MaxTurnReferences)
	}
	if _, err := m.activeProjectID(r.Context()); err != nil {
		return agent.TurnContext{}, err
	}
	switch strings.ToLower(strings.TrimSpace(candidate.Surface)) {
	case "dashboard":
		return m.resolveDashboardTurnContext(r.Context(), scope, candidate)
	case "dashboard_builder":
		return m.resolveBuilderTurnContext(r.Context(), scope, candidate)
	case "data":
		return m.resolveDataTurnContext(r.Context(), scope, candidate)
	case "chat", "builder":
		projectID, _ := m.activeProjectID(r.Context())
		scope.ProjectID = projectID
		references, err := m.resolveCatalogTurnReferences(r.Context(), scope, candidate.References, true)
		if err != nil {
			return agent.TurnContext{}, err
		}
		resolved := agent.TurnContext{Surface: strings.ToLower(strings.TrimSpace(candidate.Surface)), References: references}
		// Chat stays attached to its conversation while edits target the active
		// builder page. Resolve the selection against the authorized draft;
		// client-provided page names and model IDs are never authoritative.
		dashboardID := strings.TrimSpace(candidate.DashboardID)
		pageID := strings.TrimSpace(candidate.PageID)
		for _, reference := range references {
			if reference.Reference.Kind != "dashboard" || reference.Reference.ID != dashboardID || pageID == "" {
				continue
			}
			if m.dashboardAuthoring == nil || scope.Credential.Restricted {
				return agent.TurnContext{}, errors.New("dashboard draft context is unavailable")
			}
			draft, err := m.dashboardAuthoring.Draft(r.Context(), authoringapplication.DraftRequest{ProjectID: projectgraph.ResourceID(projectID), ActorID: scope.PrincipalID, DashboardID: authoring.DashboardID(dashboardID)})
			if err != nil {
				return agent.TurnContext{}, errors.New("dashboard draft is unknown or unauthorized")
			}
			resolved.DashboardID = dashboardID
			resolved.DashboardTitle = draft.Lifecycle.Title
			resolved.ModelID = draft.Lifecycle.SemanticModel.String()
			if draft.Lifecycle.Draft == nil {
				return agent.TurnContext{}, errors.New("dashboard has no active draft")
			}
			resolved.DraftID = draft.Lifecycle.Draft.ID.String()
			resolved.DraftRevision = &agent.DraftRevision{RevisionID: draft.Revision.ID.String(), Number: int64(draft.Revision.Number), ContentHash: draft.Revision.ContentHash}
			return withChatDraftPage(resolved, draft.Revision.Document, pageID)
		}
		if resolved.Surface == "builder" {
			return agent.TurnContext{}, errors.New("select a dashboard page before asking the builder agent to add visuals")
		}
		return resolved, nil
	default:
		return agent.TurnContext{}, errors.New("unsupported agent context surface")
	}
}

// Called only after the draft facade has authorized the actor and dashboard.
func withChatDraftPage(resolved agent.TurnContext, doc document.DashboardDocument, pageID string) (agent.TurnContext, error) {
	for _, page := range doc.Spec.Pages {
		if page.ID != pageID {
			continue
		}
		resolved.PageID, resolved.PageTitle = page.ID, page.Title
		for i := range resolved.References {
			reference := &resolved.References[i]
			if reference.Reference.Kind == "dashboard" && reference.Reference.ID == resolved.DashboardID {
				reference.PageID = page.ID
				reference.Context = append(reference.Context, fmt.Sprintf("Active builder page: %s (%s). Add new visuals to this page unless the user explicitly selects another destination.", page.Title, page.ID))
			}
		}
		return resolved, nil
	}
	return agent.TurnContext{}, fmt.Errorf("dashboard page %q no longer exists; select another page before asking the agent to edit", pageID)
}

// resolveCatalogTurnReferences reloads reference metadata through principal and
// credential authorization. Private draft references are only available to the
// existing chat/builder surfaces that explicitly opt in.
func (m *Module) resolveCatalogTurnReferences(ctx context.Context, scope agent.Scope, candidates []agent.TurnReference, allowPrivateDraft bool) ([]agent.TurnReference, error) {
	if m.catalog == nil {
		return nil, errors.New("catalog is not configured")
	}
	if strings.TrimSpace(scope.PrincipalID) == "" {
		return nil, errors.New("catalog principal is unavailable")
	}
	projectID := scope.ProjectID
	catalogScope := ToolsScope(scope)
	catalogScope.ProjectID = projectID
	references := make([]agent.TurnReference, 0, len(candidates))
	for _, reference := range candidates {
		kind, err := projectgraph.ParseKind(strings.TrimSpace(reference.Reference.Kind))
		if err != nil {
			continue
		}
		id, err := projectgraph.NewResourceID(strings.TrimSpace(reference.Reference.ID))
		if err != nil {
			continue
		}
		item, err := (credentialCatalog{base: m.catalog}).Get(ctx, catalogScope, agenttools.CatalogGetRequest{
			Ref: agenttools.CatalogRef{ID: id.String(), Kind: agenttools.CatalogType(kind)},
		})
		if err != nil {
			// Private drafts are not part of the active serving graph.
			// Resolve browser references through the edit-authorized draft
			// facade; never reuse client-provided names or model context.
			if allowPrivateDraft && kind == projectgraph.KindDashboard && m.dashboardAuthoring != nil && !scope.Credential.Restricted {
				draft, draftErr := m.dashboardAuthoring.Draft(ctx, authoringapplication.DraftRequest{
					ProjectID: projectgraph.ResourceID(projectID), ActorID: scope.PrincipalID, DashboardID: authoring.DashboardID(id.String()),
				})
				if draftErr == nil {
					references = append(references, agent.TurnReference{
						Reference: agent.TurnReferenceKey{Kind: string(projectgraph.KindDashboard), ID: id.String()},
						Name:      draft.Lifecycle.Title, Resource: agent.TurnReferenceResource{ID: projectID, Name: projectID},
						DashboardID: id.String(), ModelID: draft.Lifecycle.SemanticModel.String(),
						Href:    "/dashboards/" + url.PathEscape(id.String()) + "/edit",
						Context: []string{"Private dashboard draft. Use get_dashboard_draft or read_dashboard_source to inspect the current revision before editing."},
					})
					continue
				}
			}
			return nil, errors.New("referenced catalog resource is unknown or unauthorized")
		}
		references = append(references, TurnReferenceFromCatalog(item.Item, projectID))
	}
	return references, nil
}

func (m *Module) resolveBuilderTurnContext(ctx context.Context, scope agent.Scope, candidate agent.TurnContext) (agent.TurnContext, error) {
	projectID, err := m.activeProjectID(ctx)
	if err != nil {
		return agent.TurnContext{}, err
	}
	if m.dashboardAuthoring == nil || strings.TrimSpace(scope.PrincipalID) == "" {
		return agent.TurnContext{}, errors.New("dashboard authoring is unavailable")
	}
	dashboardID := strings.TrimSpace(candidate.DashboardID)
	draftID := strings.TrimSpace(candidate.DraftID)
	if dashboardID == "" || draftID == "" {
		return agent.TurnContext{}, errors.New("builder context requires dashboard and draft")
	}
	if !CredentialAllowsResource(contextModuleScope(scope, projectID), projectgraph.ResourceID(dashboardID), projectgraph.KindDashboard, access.CapabilityResourceEdit) {
		return agent.TurnContext{}, errors.New("credential cannot edit this dashboard")
	}
	builder, err := m.dashboardAuthoring.Builder(ctx, builderview.Request{
		ProjectID: projectgraph.ResourceID(projectID), ActorID: scope.PrincipalID,
		DashboardID: authoring.DashboardID(dashboardID), SelectedPageID: strings.TrimSpace(candidate.PageID),
	})
	if err != nil {
		return agent.TurnContext{}, err
	}
	scope.ProjectID = projectID
	return m.resolvedBuilderContextWithReferences(ctx, scope, candidate, builder)
}

// The authoring facade has already authorized this builder signal. Validate
// the active destination before resolving separately authorized attachments.
func (m *Module) resolvedBuilderContextWithReferences(ctx context.Context, scope agent.Scope, candidate agent.TurnContext, builder uisignals.DashboardBuilderSignal) (agent.TurnContext, error) {
	resolved, err := resolvedBuilderTurnContext(candidate, builder)
	if err != nil {
		return agent.TurnContext{}, err
	}
	resolved.References, err = m.resolveEmbeddedTurnReferences(ctx, scope, candidate.References, nil)
	if err != nil {
		return agent.TurnContext{}, err
	}
	return resolved, nil
}

// Embedded surfaces retain their authorized page or draft as the destination.
// Attachments supply read-authorized metadata, never a new edit destination.
func (m *Module) resolveEmbeddedTurnReferences(ctx context.Context, scope agent.Scope, candidates []agent.TurnReference, resolveLocal func([]agent.TurnReference) []agent.TurnReference) ([]agent.TurnReference, error) {
	if len(candidates) > agent.MaxTurnReferences {
		return nil, fmt.Errorf("at most %d references can be attached", agent.MaxTurnReferences)
	}
	references := make([]agent.TurnReference, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		if resourceID := strings.TrimSpace(candidate.Resource.ID); resourceID != "" && resourceID != scope.ProjectID {
			return nil, errors.New("referenced resource belongs to another project")
		}
		var resolved []agent.TurnReference
		if strings.EqualFold(strings.TrimSpace(candidate.Reference.Kind), "visual") {
			if resolveLocal != nil {
				resolved = resolveLocal([]agent.TurnReference{candidate})
			}
		} else {
			var err error
			resolved, err = m.resolveCatalogTurnReferences(ctx, scope, []agent.TurnReference{candidate}, false)
			if err != nil {
				return nil, err
			}
		}
		for _, reference := range resolved {
			key := reference.Reference.Kind + "\x00" + reference.Reference.ID
			if reference.ComponentID != "" {
				key = "visual\x00" + reference.ComponentID
			}
			if !seen[key] {
				seen[key] = true
				references = append(references, reference)
			}
		}
	}
	return references, nil
}

func resolvedBuilderTurnContext(candidate agent.TurnContext, builder uisignals.DashboardBuilderSignal) (agent.TurnContext, error) {
	draftID := strings.TrimSpace(candidate.DraftID)
	if builder.DashboardID != strings.TrimSpace(candidate.DashboardID) || builder.DraftID != draftID {
		return agent.TurnContext{}, errors.New("dashboard draft changed; reload the builder")
	}
	pageID := strings.TrimSpace(candidate.PageID)
	if pageID == "" && builder.SelectedPageID != nil {
		pageID = strings.TrimSpace(*builder.SelectedPageID)
	}
	pageTitle, found := "", false
	for _, page := range builder.Pages {
		if page.ID == pageID {
			pageTitle = page.Title
			found = true
			break
		}
	}
	if pageID == "" || !found {
		return agent.TurnContext{}, errors.New("dashboard draft page is unavailable")
	}
	return agent.TurnContext{
		Surface: "dashboard_builder", DashboardID: builder.DashboardID, DashboardTitle: builder.Title,
		DraftID: builder.DraftID, DraftRevision: &agent.DraftRevision{
			RevisionID: builder.Revision.ID, Number: builder.Revision.Number, ContentHash: builder.Revision.ContentHash,
		},
		PageID: pageID, PageTitle: pageTitle, ModelID: builder.SemanticModel.ID,
	}, nil
}

func (m *Module) resolveDataTurnContext(ctx context.Context, scope agent.Scope, candidate agent.TurnContext) (agent.TurnContext, error) {
	projectID, err := m.activeProjectID(ctx)
	if err != nil {
		return agent.TurnContext{}, err
	}
	if candidate.Exploration == nil {
		return agent.TurnContext{}, errors.New("data context requires an exploration spec")
	}
	explorationSpec, err := candidate.NormalizedDataExploration()
	if err != nil {
		return agent.TurnContext{}, fmt.Errorf("invalid exploration spec: %w", err)
	}
	modelID := strings.TrimSpace(explorationSpec.ModelID)
	datasetID := ""
	if explorationSpec.DatasetID != nil {
		datasetID = strings.TrimSpace(*explorationSpec.DatasetID)
	}
	if modelID == "" {
		return agent.TurnContext{}, errors.New("exploration spec requires semantic model")
	}
	if candidate.ModelID != "" && strings.TrimSpace(candidate.ModelID) != modelID {
		return agent.TurnContext{}, errors.New("top-level modelId does not match exploration spec")
	}
	if candidate.DatasetID != "" && strings.TrimSpace(candidate.DatasetID) != datasetID {
		return agent.TurnContext{}, errors.New("top-level datasetId does not match exploration spec")
	}
	scope.ProjectID = projectID
	resolvedModel, err := m.resolveContextResource(ctx, scope, modelID, projectgraph.KindSemanticModel, access.CapabilityResourceUse)
	if err != nil {
		return agent.TurnContext{}, errors.New("semantic model is unknown or unauthorized")
	}
	if m.dashboardMetrics == nil {
		return agent.TurnContext{}, fmt.Errorf("unknown project %q", projectID)
	}
	metrics, ok := m.dashboardMetrics(projectID)
	if !ok || metrics == nil {
		return agent.TurnContext{}, fmt.Errorf("unknown project %q", projectID)
	}
	model, ok := metrics.SemanticModel(resolvedModel.String())
	if !ok || model == nil {
		return agent.TurnContext{}, fmt.Errorf("unknown semantic model %q", modelID)
	}
	if err := exploration.ValidateAgainstModel(model, explorationSpec); err != nil {
		return agent.TurnContext{}, err
	}
	if err := authorizeSemanticExploration(ctx, metrics, resolvedModel.String(), datasetID, model, explorationSpec); err != nil {
		return agent.TurnContext{}, errors.New("semantic context is unknown or unauthorized")
	}
	references, err := m.resolveDataTurnReferences(ctx, scope, model, resolvedModel.String(), modelID, datasetID, candidate.References)
	if err != nil {
		return agent.TurnContext{}, err
	}
	return agent.TurnContext{
		Surface: "data", ModelID: resolvedModel.String(), DatasetID: datasetID,
		Exploration: explorationSpec, References: references,
	}, nil
}

// Data dataset pins are limited to the exploration already authorized above.
// Other resources use the shared catalog boundary without private-draft access.
func (m *Module) resolveDataTurnReferences(ctx context.Context, scope agent.Scope, model *semanticmodel.Model, resolvedModelID, selectedModelID, datasetID string, candidates []agent.TurnReference) ([]agent.TurnReference, error) {
	var references []agent.TurnReference
	for _, reference := range candidates {
		if resourceID := strings.TrimSpace(reference.Resource.ID); resourceID != "" && resourceID != scope.ProjectID {
			return nil, errors.New("referenced catalog resource is unknown or unauthorized")
		}
		if strings.ToLower(strings.TrimSpace(reference.Reference.Kind)) == "dataset" {
			// This is the exact ID emitted by dataExplorerAgentSuggestions.
			if datasetID == "" || strings.TrimSpace(reference.Reference.ID) != selectedModelID+"/"+datasetID {
				return nil, errors.New("dataset reference does not match the current exploration")
			}
			dataset := model.Datasets[datasetID]
			name := dataset.DisplayName
			if strings.TrimSpace(name) == "" {
				name = datasetID
			}
			references = append(references, agent.TurnReference{
				Reference: agent.TurnReferenceKey{Kind: "dataset", ID: resolvedModelID + "/" + datasetID},
				Name:      name, Description: dataset.Description, ModelID: resolvedModelID, DatasetID: datasetID,
				Resource:  agent.TurnReferenceResource{ID: scope.ProjectID, Name: scope.ProjectID},
				Hierarchy: []string{scope.ProjectID, resolvedModelID}, Context: []string{"active_project_generation"},
				Href: "/explore?mode=explore&semanticModel=" + url.QueryEscape(resolvedModelID) + "&dataset=" + url.QueryEscape(datasetID),
			})
			continue
		}
		resolved, err := m.resolveCatalogTurnReferences(ctx, scope, []agent.TurnReference{reference}, false)
		if err != nil {
			return nil, err
		}
		references = append(references, resolved...)
	}
	return references, nil
}

func (m *Module) resolveDashboardTurnContext(ctx context.Context, scope agent.Scope, candidate agent.TurnContext) (agent.TurnContext, error) {
	projectID, err := m.activeProjectID(ctx)
	if err != nil {
		return agent.TurnContext{}, err
	}
	dashboardID := strings.TrimSpace(candidate.DashboardID)
	pageID := strings.TrimSpace(candidate.PageID)
	if dashboardID == "" || pageID == "" {
		return agent.TurnContext{}, errors.New("dashboard context requires dashboard and page")
	}
	scope.ProjectID = projectID
	resolvedDashboard, err := m.resolveContextResource(ctx, scope, dashboardID, projectgraph.KindDashboard, access.CapabilityResourceRead)
	if err != nil {
		return agent.TurnContext{}, errors.New("dashboard is unknown or unauthorized")
	}
	if m.dashboardMetrics == nil {
		return agent.TurnContext{}, fmt.Errorf("unknown project %q", projectID)
	}
	metrics, ok := m.dashboardMetrics(projectID)
	if !ok || metrics == nil {
		return agent.TurnContext{}, fmt.Errorf("unknown project %q", projectID)
	}
	if metrics.Resolver() == nil {
		return agent.TurnContext{}, fmt.Errorf("unknown dashboard %q", dashboardID)
	}
	resolved, err := metrics.Resolver().Resolve(resolvedDashboard)
	if err != nil {
		return agent.TurnContext{}, fmt.Errorf("unknown dashboard %q", dashboardID)
	}
	report := resolved.Definition
	var page dashboard.Page
	for _, current := range metrics.Pages(resolvedDashboard.String()) {
		if current.ID == pageID {
			page = current
			break
		}
	}
	if page.ID == "" {
		return agent.TurnContext{}, fmt.Errorf("unknown dashboard page %q", pageID)
	}
	filters, err := dashboardFiltersFromTurnContext(candidate.Filters)
	if err != nil {
		return agent.TurnContext{}, err
	}
	filters = report.NormalizeFiltersForPage(page.ID, filters).WithDefaults()
	filterMap, err := turnContextFilters(filters)
	if err != nil {
		return agent.TurnContext{}, err
	}
	references, err := m.resolveEmbeddedTurnReferences(ctx, scope, candidate.References, func(candidates []agent.TurnReference) []agent.TurnReference {
		return ResolveDashboardTurnReferences(candidates, DashboardTurnReferenceContext{
			Resource:    agent.TurnReferenceResource{ID: projectID, Name: projectID},
			DashboardID: report.ID, DashboardTitle: report.Title, Page: page,
		}, report.Visualizations)
	})
	if err != nil {
		return agent.TurnContext{}, err
	}
	return agent.TurnContext{
		Surface:        "dashboard",
		DashboardID:    report.ID,
		DashboardTitle: report.Title,
		PageID:         page.ID,
		PageTitle:      page.Title,
		ModelID:        metrics.ModelIDForDashboard(report.ID),
		Generation:     candidate.Generation,
		Filters:        filterMap,
		References:     references,
	}, nil
}

func (m *Module) activeProjectID(ctx context.Context) (string, error) {
	if m == nil {
		return "", errors.New("active project runtime is required")
	}
	projectID := m.projectID
	if m.projectIDResolver != nil {
		resolved, err := m.projectIDResolver(ctx)
		if err != nil {
			return "", err
		}
		projectID = resolved
	}
	if err := projectID.Validate(); err != nil {
		return "", fmt.Errorf("active project runtime is required: %w", err)
	}
	return projectID.String(), nil
}

func (m *Module) resolveContextResource(ctx context.Context, scope agent.Scope, raw string, kind projectgraph.Kind, capability access.Capability) (projectgraph.ResourceID, error) {
	id, err := projectgraph.NewResourceID(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if m.resolveResource == nil {
		return "", errors.New("authorized project catalog is not configured")
	}
	projectID, err := m.activeProjectID(ctx)
	if err != nil {
		return "", err
	}
	return m.resolveResource(ctx, contextModuleScope(scope, projectID), id, kind, capability)
}

func contextModuleScope(scope agent.Scope, projectID string) Scope {
	return Scope{
		ProjectID: projectID, PrincipalID: scope.PrincipalID, GroupIDs: append([]string(nil), scope.GroupIDs...), ConversationID: scope.ConversationID,
		DevAuthBypass: scope.DevAuthBypass,
		Credential: CredentialScope{
			ProjectID:         scope.Credential.ProjectID,
			Capabilities:      append([]string(nil), scope.Credential.Capabilities...),
			PermissionProfile: scope.Credential.PermissionProfile,
			Permissions:       clonePermissionPairs(scope.Credential.Permissions),
			Restricted:        scope.Credential.Restricted,
		},
	}
}

func dashboardFiltersFromTurnContext(raw map[string]any) (dashboard.Filters, error) {
	if raw == nil {
		return dashboard.Filters{}.WithDefaults(), nil
	}
	if _, ok := raw["revision"]; !ok {
		return dashboard.Filters{}, errors.New("invalid dashboard filter state: revision is required")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return dashboard.Filters{}, fmt.Errorf("encode dashboard filter state: %w", err)
	}
	var state dashboardfilter.State
	if err := json.Unmarshal(encoded, &state); err != nil {
		return dashboard.Filters{}, fmt.Errorf("invalid dashboard filter state: %w", err)
	}
	return dashboard.Filters{CompiledState: &state}.WithDefaults(), nil
}

func turnContextFilters(filters dashboard.Filters) (map[string]any, error) {
	state := dashboardfilter.State{
		AppliedControls: map[string]dashboardfilter.AppliedState{},
		DraftControls:   map[string]dashboardfilter.Expression{},
		DirtyBindings:   []string{},
	}
	if filters.CompiledState != nil {
		state = dashboardfilter.CloneState(*filters.CompiledState)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("encode normalized dashboard filter state: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, fmt.Errorf("decode normalized dashboard filter state: %w", err)
	}
	return out, nil
}

type DashboardTurnReferenceContext struct {
	Resource       agent.TurnReferenceResource
	DashboardID    string
	DashboardTitle string
	Page           dashboard.Page
}

func ResolveDashboardTurnReferences(candidates []agent.TurnReference, context DashboardTurnReferenceContext, visualizations map[string]visualizationdefinition.Definition) []agent.TurnReference {
	resolved := make([]agent.TurnReference, 0, min(len(candidates), agent.MaxTurnReferences))
	seen := map[string]struct{}{}
	href := "/dashboards/" + url.PathEscape(context.DashboardID) + "/pages/" + url.PathEscape(context.Page.ID)
	location := agent.TurnReferenceLocation{
		DashboardID: context.DashboardID, DashboardName: context.DashboardTitle,
		PageID: context.Page.ID, PageName: context.Page.Title, Href: href,
	}
	for _, candidate := range candidates {
		if len(resolved) == agent.MaxTurnReferences {
			break
		}
		if strings.ToLower(strings.TrimSpace(candidate.Reference.Kind)) != "visual" {
			continue
		}
		candidateResourceID := strings.TrimSpace(candidate.Resource.ID)
		contextResourceID := strings.TrimSpace(context.Resource.ID)
		if candidateResourceID != "" && candidateResourceID != contextResourceID {
			continue
		}
		visualID := lastAgentContextReferencePart(candidate.Reference.ID)
		if visualID == "" || candidate.Reference.ID != context.DashboardID+"."+visualID {
			continue
		}
		for _, component := range context.Page.Visuals {
			if component.Visual != visualID {
				continue
			}
			title, visualType, ok := resolvedVisualMetadata(component, visualID, visualizations)
			if !ok {
				break
			}
			if _, exists := seen[component.ID]; exists {
				break
			}
			seen[component.ID] = struct{}{}
			resolved = append(resolved, agent.TurnReference{
				Reference:   candidate.Reference,
				Name:        title,
				Resource:    context.Resource,
				Hierarchy:   []string{context.Resource.Name, context.DashboardTitle, context.Page.Title},
				Href:        href,
				Locations:   []agent.TurnReferenceLocation{location},
				Context:     []string{"current_page", "current_dashboard"},
				ComponentID: component.ID,
				VisualID:    visualID,
				VisualType:  visualType,
			})
			break
		}
	}
	return resolved
}

func lastAgentContextReferencePart(value string) string {
	if index := strings.LastIndex(value, "."); index >= 0 {
		return value[index+1:]
	}
	return value
}

func resolvedVisualMetadata(component dashboard.PageVisual, visualID string, visualizations map[string]visualizationdefinition.Definition) (string, string, bool) {
	if component.Visual != visualID {
		return "", "", false
	}
	visual, ok := visualizations[visualID]
	if !ok {
		return "", "", false
	}
	base, err := visualizationir.SpecificationBase(visual.Spec)
	if err != nil {
		return "", "", false
	}
	title := strings.TrimSpace(component.Title)
	if title == "" {
		title = strings.TrimSpace(base.Title)
	}
	if title == "" {
		title = visualID
	}
	visualType := base.Kind
	switch spec := visual.Spec.Value.(type) {
	case *visualizationir.CartesianVisualizationSpec:
		visualType = string(spec.Mark)
	case *visualizationir.PointVisualizationSpec:
		visualType = "scatter"
	case *visualizationir.ProportionalVisualizationSpec:
		visualType = string(spec.Mark)
	case *visualizationir.HierarchyVisualizationSpec:
		visualType = string(spec.Mark)
	case *visualizationir.PolarVisualizationSpec:
		visualType = string(spec.Mark)
	}
	return title, strings.TrimSpace(visualType), true
}
