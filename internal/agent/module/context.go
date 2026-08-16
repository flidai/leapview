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
	"github.com/flidai/leapview/internal/dashboard"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func (m *Module) ResolveTurnContext(r *http.Request, scope agent.Scope, candidate agent.TurnContext) (agent.TurnContext, error) {
	if len(candidate.References) > agent.MaxTurnReferences {
		return agent.TurnContext{}, fmt.Errorf("at most %d references can be attached", agent.MaxTurnReferences)
	}
	if _, err := m.activeProjectID(); err != nil {
		return agent.TurnContext{}, err
	}
	switch strings.ToLower(strings.TrimSpace(candidate.Surface)) {
	case "dashboard":
		return m.resolveDashboardTurnContext(r.Context(), scope, candidate)
	case "data":
		return m.resolveDataTurnContext(r.Context(), scope, candidate)
	case "chat":
		if m.catalog == nil {
			return agent.TurnContext{}, errors.New("catalog is not configured")
		}
		if strings.TrimSpace(scope.PrincipalID) == "" {
			return agent.TurnContext{}, errors.New("catalog principal is unavailable")
		}
		projectID, _ := m.activeProjectID()
		scope.ProjectID = projectID
		references := make([]agent.TurnReference, 0, len(candidate.References))
		for _, reference := range candidate.References {
			kind, err := projectgraph.ParseKind(strings.TrimSpace(reference.Reference.Kind))
			if err != nil {
				continue
			}
			id, err := projectgraph.NewResourceID(strings.TrimSpace(reference.Reference.ID))
			if err != nil {
				continue
			}
			item, err := m.catalog.Get(r.Context(), agenttools.Scope{ProjectID: projectID, PrincipalID: scope.PrincipalID}, agenttools.CatalogGetRequest{
				Ref: agenttools.CatalogRef{ID: id.String(), Kind: agenttools.CatalogType(kind)},
			})
			if err != nil {
				return agent.TurnContext{}, errors.New("referenced catalog resource is unknown or unauthorized")
			}
			references = append(references, TurnReferenceFromCatalog(item.Item, projectID))
		}
		return agent.TurnContext{Surface: "chat", References: references}, nil
	default:
		return agent.TurnContext{}, errors.New("unsupported agent context surface")
	}
}

func (m *Module) resolveDataTurnContext(ctx context.Context, scope agent.Scope, candidate agent.TurnContext) (agent.TurnContext, error) {
	projectID, err := m.activeProjectID()
	if err != nil {
		return agent.TurnContext{}, err
	}
	modelID := strings.TrimSpace(candidate.ModelID)
	datasetID := strings.TrimSpace(candidate.DatasetID)
	if modelID == "" || datasetID == "" {
		return agent.TurnContext{}, errors.New("data context requires semantic model and dataset")
	}
	scope.ProjectID = projectID
	if !contextCredentialAllowsCapability(scope, access.CapabilityResourceUse) {
		return agent.TurnContext{}, errors.New("credential cannot view this data")
	}
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
	if _, ok := model.Tables[datasetID]; !ok {
		return agent.TurnContext{}, fmt.Errorf("unknown dataset %q", datasetID)
	}
	exploration := candidate.NormalizedDataExploration()
	if err := validateDataExploration(model, exploration); err != nil {
		return agent.TurnContext{}, err
	}
	return agent.TurnContext{
		Surface: "data", ModelID: resolvedModel.String(), DatasetID: datasetID,
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
	projectID, err := m.activeProjectID()
	if err != nil {
		return agent.TurnContext{}, err
	}
	dashboardID := strings.TrimSpace(candidate.DashboardID)
	pageID := strings.TrimSpace(candidate.PageID)
	if dashboardID == "" || pageID == "" {
		return agent.TurnContext{}, errors.New("dashboard context requires dashboard and page")
	}
	scope.ProjectID = projectID
	if !contextCredentialAllowsCapability(scope, access.CapabilityResourceRead) {
		return agent.TurnContext{}, errors.New("credential cannot view this dashboard")
	}
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
	return agent.TurnContext{
		Surface:        "dashboard",
		DashboardID:    report.ID,
		DashboardTitle: report.Title,
		PageID:         page.ID,
		PageTitle:      page.Title,
		ModelID:        metrics.ModelIDForDashboard(report.ID),
		Generation:     candidate.Generation,
		Filters:        filterMap,
		References: ResolveDashboardTurnReferences(candidate.References, DashboardTurnReferenceContext{
			Resource:    agent.TurnReferenceResource{ID: projectID, Name: projectID},
			DashboardID: report.ID, DashboardTitle: report.Title, Page: page,
		}, report.Visualizations),
	}, nil
}

func (m *Module) activeProjectID() (string, error) {
	if m == nil || m.projectID == "" {
		return "", errors.New("active project runtime is required")
	}
	return m.projectID.String(), nil
}

func (m *Module) resolveContextResource(ctx context.Context, scope agent.Scope, raw string, kind projectgraph.Kind, capability access.Capability) (projectgraph.ResourceID, error) {
	id, err := projectgraph.NewResourceID(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if m.resolveResource == nil {
		return "", errors.New("authorized project catalog is not configured")
	}
	projectID, err := m.activeProjectID()
	if err != nil {
		return "", err
	}
	return m.resolveResource(ctx, agenttools.Scope{
		ProjectID: projectID, PrincipalID: scope.PrincipalID, ConversationID: scope.ConversationID,
		DevAuthBypass: scope.DevAuthBypass,
		Credential:    agenttools.CredentialScope{ProjectID: scope.Credential.ProjectID, Capabilities: append([]string(nil), scope.Credential.Capabilities...), Restricted: scope.Credential.Restricted},
	}, id, kind, capability)
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
		if strings.TrimSpace(candidate.Resource.ID) != context.Resource.ID {
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

func contextCredentialAllowsCapability(scope agent.Scope, capability access.Capability) bool {
	if !scope.Credential.Restricted || scope.Credential.Capabilities == nil {
		return true
	}
	for _, allowed := range scope.Credential.Capabilities {
		if strings.EqualFold(strings.TrimSpace(allowed), string(capability)) {
			return true
		}
	}
	return false
}
