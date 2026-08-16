package module

import (
	"errors"
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/agent/ui"
	productsearch "github.com/flidai/leapview/internal/workspace/search"
)

var referenceTypes = []productsearch.Type{
	productsearch.TypeVisual,
	productsearch.TypeDashboard,
	productsearch.TypePage,
	productsearch.TypeMeasure,
	productsearch.TypeSemanticModel,
}

func (m *Module) IsReferenceType(typ productsearch.Type) bool {
	switch typ {
	case productsearch.TypeVisual, productsearch.TypeDashboard, productsearch.TypePage,
		productsearch.TypeMeasure, productsearch.TypeSemanticModel:
		return true
	default:
		return false
	}
}

func (m *Module) SearchReferences(r *http.Request, turnContext agent.TurnContext, query string, limit int) ([]ui.AgentReferenceSignal, error) {
	if m == nil || m.search == nil {
		return nil, errors.New("search is not configured")
	}
	subject, ok := m.search.SearchSubject(r)
	if !ok {
		return nil, errors.New("search principal is unavailable")
	}
	environment := ""
	if m.environment != nil {
		environment = m.environment(r)
	}
	page, err := m.search.Search(r.Context(), subject, productsearch.Query{
		Text: strings.TrimSpace(query), Environment: environment, Limit: limit,
		AllowedTypes: referenceTypes,
		Context: productsearch.SearchContext{
			WorkspaceID: strings.TrimSpace(turnContext.ProjectID),
			DashboardID: strings.TrimSpace(turnContext.DashboardID),
			PageID:      strings.TrimSpace(turnContext.PageID),
		},
	})
	if err != nil {
		return nil, err
	}
	out := make([]ui.AgentReferenceSignal, 0, len(page.Items))
	for _, result := range page.Items {
		out = append(out, ReferenceSignal(result))
	}
	return out, nil
}

func ReferenceSignal(result productsearch.Result) ui.AgentReferenceSignal {
	locations := make([]ui.AgentReferenceLocationSignal, 0, len(result.Locations))
	for _, location := range result.Locations {
		locations = append(locations, ui.AgentReferenceLocationSignal{
			DashboardID: ui.Optional(location.DashboardID), DashboardName: ui.Optional(location.DashboardName),
			PageID: ui.Optional(location.PageID), PageName: ui.Optional(location.PageName), Href: location.Href,
		})
	}
	contextTags := make([]string, 0, len(result.Context))
	for _, tag := range result.Context {
		contextTags = append(contextTags, string(tag))
	}
	return ui.AgentReferenceSignal{
		Reference: ui.AgentReferenceKeySignal{Kind: string(result.Reference.Type), ID: result.Reference.ID},
		Name:      result.Name, Description: ui.Optional(result.Description),
		VisualType: ui.Optional(result.VisualType),
		Workspace:  ui.AgentReferenceWorkspaceSignal{ID: result.Workspace.ID, Name: result.Workspace.Name},
		Hierarchy:  referenceHierarchy(result),
		Href:       result.Href, Locations: locations, Context: contextTags,
	}
}

func (m *Module) TurnReference(result productsearch.Result) agent.TurnReference {
	locations := make([]agent.TurnReferenceLocation, 0, len(result.Locations))
	for _, location := range result.Locations {
		locations = append(locations, agent.TurnReferenceLocation{
			DashboardID: location.DashboardID, DashboardName: location.DashboardName,
			PageID: location.PageID, PageName: location.PageName, Href: location.Href,
		})
	}
	contextTags := make([]string, 0, len(result.Context))
	for _, tag := range result.Context {
		contextTags = append(contextTags, string(tag))
	}
	reference := agent.TurnReference{
		Reference: agent.TurnReferenceKey{Kind: string(result.Reference.Type), ID: result.Reference.ID},
		Name:      result.Name, Description: result.Description, VisualType: result.VisualType,
		Resource:  agent.TurnReferenceResource{ID: result.Workspace.ID, Name: result.Workspace.Name},
		Hierarchy: referenceHierarchy(result), Href: result.Href, Locations: locations, Context: contextTags,
	}
	parts := strings.Split(result.Reference.ID, ".")
	switch result.Reference.Type {
	case productsearch.TypeVisual:
		reference.VisualID = lastReferencePart(result.Reference.ID)
	case productsearch.TypeFilter:
		reference.FilterID = lastReferencePart(result.Reference.ID)
	case productsearch.TypeSemanticModel:
		reference.ModelID = result.Reference.ID
	case productsearch.TypeSemanticTable:
		if len(parts) > 0 {
			reference.ModelID = parts[0]
		}
		reference.DatasetID = lastReferencePart(result.Reference.ID)
	case productsearch.TypeField:
		if len(parts) > 0 {
			reference.ModelID = parts[0]
		}
		if len(parts) > 1 {
			reference.DatasetID = parts[len(parts)-2]
		}
		reference.FieldID = lastReferencePart(result.Reference.ID)
	case productsearch.TypeMeasure:
		if len(parts) > 0 {
			reference.ModelID = parts[0]
		}
		reference.FieldID = lastReferencePart(result.Reference.ID)
	}
	return reference
}

func referenceHierarchy(result productsearch.Result) []string {
	hierarchy := make([]string, 0, 3)
	if name := strings.TrimSpace(result.Workspace.Name); name != "" {
		hierarchy = append(hierarchy, name)
	}
	appendName := func(name string) {
		name = strings.TrimSpace(name)
		if name != "" && (len(hierarchy) == 0 || hierarchy[len(hierarchy)-1] != name) {
			hierarchy = append(hierarchy, name)
		}
	}
	switch result.Reference.Type {
	case productsearch.TypeVisual:
		if len(result.Locations) > 0 {
			appendName(result.Locations[0].DashboardName)
			appendName(result.Locations[0].PageName)
		}
	case productsearch.TypePage:
		if len(result.Locations) > 0 {
			appendName(result.Locations[0].DashboardName)
		}
	case productsearch.TypeMeasure:
		for _, ancestor := range result.Hierarchy {
			if ancestor.Type == productsearch.TypeSemanticModel {
				appendName(ancestor.Name)
			}
		}
	}
	return hierarchy
}

func lastReferencePart(value string) string {
	if index := strings.LastIndex(value, "."); index >= 0 {
		return value[index+1:]
	}
	return value
}
