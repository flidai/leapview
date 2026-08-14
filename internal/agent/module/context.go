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
	"github.com/flidai/leapview/internal/dashboard"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	productsearch "github.com/flidai/leapview/internal/workspace/search"
)

func (m *Module) ResolveTurnContext(r *http.Request, scope agent.Scope, candidate agent.TurnContext) (agent.TurnContext, error) {
	if len(candidate.References) > agent.MaxTurnReferences {
		return agent.TurnContext{}, fmt.Errorf("at most %d references can be attached", agent.MaxTurnReferences)
	}
	switch strings.ToLower(strings.TrimSpace(candidate.Surface)) {
	case "dashboard":
		return m.resolveDashboardTurnContext(r.Context(), scope, candidate)
	case "data":
		return m.resolveDataTurnContext(r.Context(), scope, candidate)
	case "chat":
		if m.search == nil {
			return agent.TurnContext{}, errors.New("search is not configured")
		}
		defaultWorkspaceID := firstNonEmptyContext(candidate.WorkspaceID, m.defaultWorkspaceID)
		references := make([]productsearch.Reference, 0, len(candidate.References))
		for _, reference := range candidate.References {
			typ := productsearch.Type(strings.ToLower(strings.TrimSpace(reference.Reference.Type)))
			if !m.IsReferenceType(typ) {
				continue
			}
			workspaceID := firstNonEmptyContext(reference.Reference.WorkspaceID, defaultWorkspaceID)
			if workspaceID == "" {
				continue
			}
			workspaceScope := scope
			workspaceScope.WorkspaceID = workspaceID
			if !contextCredentialAllowsPrivilege(workspaceScope, access.PrivilegeViewItem) {
				return agent.TurnContext{}, errors.New("credential cannot view referenced context")
			}
			references = append(references, productsearch.Reference{
				WorkspaceID: workspaceID,
				Type:        typ,
				ID:          reference.Reference.ID,
			})
		}
		subject, ok := m.search.SearchSubject(r)
		if !ok {
			return agent.TurnContext{}, errors.New("search principal is unavailable")
		}
		environment := ""
		if m.environment != nil {
			environment = m.environment(r)
		}
		rows, err := m.search.ResolveSearchReferences(r.Context(), subject, environment, references)
		if err != nil {
			return agent.TurnContext{}, err
		}
		resolved := make([]agent.TurnReference, 0, len(rows))
		resolvedWorkspaceID := ""
		for _, row := range rows {
			resolved = append(resolved, m.TurnReference(row))
			if len(resolved) == 1 {
				resolvedWorkspaceID = row.Reference.WorkspaceID
			} else if resolvedWorkspaceID != row.Reference.WorkspaceID {
				resolvedWorkspaceID = ""
			}
		}
		return agent.TurnContext{Surface: "chat", WorkspaceID: resolvedWorkspaceID, References: resolved}, nil
	default:
		return agent.TurnContext{}, errors.New("unsupported agent context surface")
	}
}

func (m *Module) resolveDataTurnContext(ctx context.Context, scope agent.Scope, candidate agent.TurnContext) (agent.TurnContext, error) {
	workspaceID := strings.TrimSpace(candidate.WorkspaceID)
	modelID := strings.TrimSpace(candidate.ModelID)
	datasetID := strings.TrimSpace(candidate.DatasetID)
	if workspaceID == "" || modelID == "" || datasetID == "" {
		return agent.TurnContext{}, errors.New("data context requires workspace, semantic model, and dataset")
	}
	scope.WorkspaceID = workspaceID
	if !contextCredentialAllowsPrivilege(scope, access.PrivilegeViewItem) {
		return agent.TurnContext{}, errors.New("credential cannot view this data")
	}
	workspaceObject := access.WorkspaceObject(workspaceID)
	modelObject := access.ItemObjectWithParent(access.SecurableSemanticModel, workspaceID, modelID, workspaceObject)
	datasetObject := access.ItemObjectWithParent(access.SecurableDataset, workspaceID, modelID+"/"+datasetID, modelObject)
	if !scope.DevAuthBypass {
		allowed, err := m.authorizeDashboardTurnContext(ctx, scope.PrincipalID, datasetObject, modelObject, workspaceObject)
		if err != nil {
			return agent.TurnContext{}, fmt.Errorf("authorize data context: %w", err)
		}
		if !allowed {
			return agent.TurnContext{}, errors.New("data context is not accessible")
		}
	}
	if m.dashboardMetrics == nil {
		return agent.TurnContext{}, fmt.Errorf("unknown workspace %q", workspaceID)
	}
	metrics, ok := m.dashboardMetrics(workspaceID)
	if !ok || metrics == nil {
		return agent.TurnContext{}, fmt.Errorf("unknown workspace %q", workspaceID)
	}
	model, ok := metrics.SemanticModel(modelID)
	if !ok || model == nil {
		return agent.TurnContext{}, fmt.Errorf("unknown semantic model %q", modelID)
	}
	if _, ok := model.Tables[datasetID]; !ok {
		return agent.TurnContext{}, fmt.Errorf("unknown dataset %q", datasetID)
	}
	exploration := candidate.NormalizedDataExploration()
	if err := validateDataExploration(model, exploration); err != nil {
		return agent.TurnContext{}, err
	}
	return agent.TurnContext{
		Surface: "data", WorkspaceID: workspaceID, ModelID: modelID, DatasetID: datasetID,
		Exploration: exploration,
	}, nil
}

func validateDataExploration(model interface {
	ValidateQueryDimension(string) error
	ValidateAggregateMember(string) error
}, exploration *agent.DataExploration) error {
	if exploration == nil {
		return nil
	}
	for _, dimension := range exploration.Dimensions {
		if err := model.ValidateQueryDimension(dimension); err != nil {
			return fmt.Errorf("invalid exploration dimension %q: %w", dimension, err)
		}
	}
	for _, measure := range exploration.Measures {
		if err := model.ValidateAggregateMember(measure); err != nil {
			return fmt.Errorf("invalid exploration measure %q: %w", measure, err)
		}
	}
	allowedFilterOperators := map[string]bool{
		"equals": true, "in": true, "contains": true, "not_contains": true, "starts_with": true,
		"greater_than_or_equal": true, "less_than": true, "is_null": true, "is_not_null": true,
	}
	for _, filter := range exploration.Filters {
		if err := model.ValidateQueryDimension(filter.Field); err != nil {
			return fmt.Errorf("invalid exploration filter field %q: %w", filter.Field, err)
		}
		if !allowedFilterOperators[filter.Operator] {
			return fmt.Errorf("invalid exploration filter operator %q", filter.Operator)
		}
		valueFree := filter.Operator == "is_null" || filter.Operator == "is_not_null"
		if valueFree != (len(filter.Values) == 0) {
			return fmt.Errorf("invalid values for exploration filter operator %q", filter.Operator)
		}
	}
	if exploration.Time != nil {
		if err := model.ValidateQueryDimension(exploration.Time.Field); err != nil {
			return fmt.Errorf("invalid exploration time field %q: %w", exploration.Time.Field, err)
		}
		if !map[string]bool{"day": true, "week": true, "month": true, "quarter": true, "year": true}[exploration.Time.Grain] {
			return fmt.Errorf("invalid exploration time grain %q", exploration.Time.Grain)
		}
	}
	selected := map[string]struct{}{}
	for _, field := range append(append([]string(nil), exploration.Dimensions...), exploration.Measures...) {
		selected[field] = struct{}{}
	}
	for _, sort := range exploration.Sort {
		if _, ok := selected[sort.Field]; !ok {
			return fmt.Errorf("exploration sort field %q is not selected", sort.Field)
		}
		if sort.Direction != "asc" && sort.Direction != "desc" {
			return fmt.Errorf("invalid exploration sort direction %q", sort.Direction)
		}
	}
	return nil
}

func (m *Module) resolveDashboardTurnContext(ctx context.Context, scope agent.Scope, candidate agent.TurnContext) (agent.TurnContext, error) {
	workspaceID := strings.TrimSpace(candidate.WorkspaceID)
	dashboardID := strings.TrimSpace(candidate.DashboardID)
	pageID := strings.TrimSpace(candidate.PageID)
	if workspaceID == "" || dashboardID == "" || pageID == "" {
		return agent.TurnContext{}, errors.New("dashboard context requires workspace, dashboard, and page")
	}
	scope.WorkspaceID = workspaceID
	if !contextCredentialAllowsPrivilege(scope, access.PrivilegeViewItem) {
		return agent.TurnContext{}, errors.New("credential cannot view this dashboard")
	}
	object := access.ItemObjectWithParent(access.SecurableDashboard, workspaceID, dashboardID, access.WorkspaceObject(workspaceID))
	if !scope.DevAuthBypass {
		allowed, err := m.authorizeDashboardTurnContext(ctx, scope.PrincipalID, object, access.WorkspaceObject(workspaceID))
		if err != nil {
			return agent.TurnContext{}, fmt.Errorf("authorize dashboard context: %w", err)
		}
		if !allowed {
			return agent.TurnContext{}, errors.New("dashboard context is not accessible")
		}
	}
	if m.dashboardMetrics == nil {
		return agent.TurnContext{}, fmt.Errorf("unknown workspace %q", workspaceID)
	}
	metrics, ok := m.dashboardMetrics(workspaceID)
	if !ok || metrics == nil {
		return agent.TurnContext{}, fmt.Errorf("unknown workspace %q", workspaceID)
	}
	report, _, ok := metrics.Report(dashboardID)
	if !ok {
		return agent.TurnContext{}, fmt.Errorf("unknown dashboard %q", dashboardID)
	}
	var page dashboard.Page
	for _, current := range metrics.Pages(dashboardID) {
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
	catalog := metrics.Catalog()
	workspaceName := strings.TrimSpace(catalog.Workspace.Title)
	if workspaceName == "" {
		workspaceName = workspaceID
	}
	return agent.TurnContext{
		Surface:        "dashboard",
		WorkspaceID:    workspaceID,
		DashboardID:    report.ID,
		DashboardTitle: report.Title,
		PageID:         page.ID,
		PageTitle:      page.Title,
		ModelID:        metrics.ModelIDForDashboard(report.ID),
		Generation:     candidate.Generation,
		Filters:        filterMap,
		References: ResolveDashboardTurnReferences(candidate.References, DashboardTurnReferenceContext{
			Workspace:   agent.TurnReferenceWorkspace{ID: workspaceID, Name: workspaceName},
			DashboardID: report.ID, DashboardTitle: report.Title, Page: page,
		}, report.Visualizations),
	}, nil
}

func (m *Module) authorizeDashboardTurnContext(ctx context.Context, principalID string, objects ...access.ObjectRef) (bool, error) {
	if m.skipContextAuthorization {
		return true, nil
	}
	if strings.TrimSpace(principalID) == "" {
		return false, nil
	}
	if m.authorizeAnyObject == nil {
		return false, nil
	}
	return m.authorizeAnyObject(ctx, principalID, access.PrivilegeViewItem, objects)
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
	Workspace      agent.TurnReferenceWorkspace
	DashboardID    string
	DashboardTitle string
	Page           dashboard.Page
}

func ResolveDashboardTurnReferences(candidates []agent.TurnReference, context DashboardTurnReferenceContext, visualizations map[string]visualizationdefinition.Definition) []agent.TurnReference {
	resolved := make([]agent.TurnReference, 0, min(len(candidates), agent.MaxTurnReferences))
	seen := map[string]struct{}{}
	href := "/workspaces/" + url.PathEscape(context.Workspace.ID) + "/dashboards/" + url.PathEscape(context.DashboardID) + "/pages/" + url.PathEscape(context.Page.ID)
	location := agent.TurnReferenceLocation{
		DashboardID: context.DashboardID, DashboardName: context.DashboardTitle,
		PageID: context.Page.ID, PageName: context.Page.Title, Href: href,
	}
	for _, candidate := range candidates {
		if len(resolved) == agent.MaxTurnReferences {
			break
		}
		if strings.ToLower(strings.TrimSpace(candidate.Reference.Type)) != "visual" {
			continue
		}
		if strings.TrimSpace(candidate.Reference.WorkspaceID) != context.Workspace.ID {
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
				Workspace:   context.Workspace,
				Hierarchy:   []string{context.Workspace.Name, context.DashboardTitle, context.Page.Title},
				Href:        href,
				Locations:   []agent.TurnReferenceLocation{location},
				Context:     []string{"current_page", "current_dashboard", "current_workspace"},
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

func firstNonEmptyContext(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func contextCredentialAllowsPrivilege(scope agent.Scope, privilege access.Privilege) bool {
	if !scope.Credential.Restricted {
		return true
	}
	if strings.TrimSpace(scope.Credential.WorkspaceID) != "" &&
		!strings.EqualFold(strings.TrimSpace(scope.Credential.WorkspaceID), strings.TrimSpace(scope.WorkspaceID)) {
		return false
	}
	for _, allowed := range scope.Credential.Privileges {
		if strings.EqualFold(strings.TrimSpace(allowed), string(privilege)) {
			return true
		}
	}
	return false
}
