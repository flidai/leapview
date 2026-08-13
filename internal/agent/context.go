package agent

import (
	"strings"

	agentcore "github.com/flidai/leapview/pkg/agent"
)

const (
	dashboardTurnContextSurface = "dashboard"
	dataTurnContextSurface      = "data"
)

const MaxTurnReferences = 12

// TurnContext is server-resolved product context for one user turn. It is
// deliberately separate from Scope: Scope controls authorization, while this
// value describes the dashboard state the user is asking about.
type TurnContext struct {
	Surface        string           `json:"surface"`
	WorkspaceID    string           `json:"workspaceId,omitempty"`
	DashboardID    string           `json:"dashboardId,omitempty"`
	DashboardTitle string           `json:"dashboardTitle,omitempty"`
	PageID         string           `json:"pageId,omitempty"`
	PageTitle      string           `json:"pageTitle,omitempty"`
	ModelID        string           `json:"modelId,omitempty"`
	DatasetID      string           `json:"datasetId,omitempty"`
	Exploration    *DataExploration `json:"exploration,omitempty"`
	Generation     int64            `json:"generation,omitempty"`
	Filters        map[string]any   `json:"filters,omitempty"`
	References     []TurnReference  `json:"references,omitempty"`
}

type DataExploration struct {
	Dimensions []string                `json:"dimensions"`
	Measures   []string                `json:"measures"`
	Filters    []DataExplorationFilter `json:"filters"`
	Sort       []DataExplorationSort   `json:"sort"`
	Time       *DataExplorationTime    `json:"time,omitempty"`
	Limit      int64                   `json:"limit"`
}

type DataExplorationFilter struct {
	Field    string   `json:"field"`
	Operator string   `json:"operator"`
	Values   []string `json:"values"`
	Fact     string   `json:"fact,omitempty"`
}

type DataExplorationSort struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}

type DataExplorationTime struct {
	Field string `json:"field"`
	Grain string `json:"grain"`
	Alias string `json:"alias,omitempty"`
}

type TurnReference struct {
	Reference   TurnReferenceKey        `json:"reference"`
	Name        string                  `json:"name,omitempty"`
	Description string                  `json:"description,omitempty"`
	Workspace   TurnReferenceWorkspace  `json:"workspace"`
	Hierarchy   []string                `json:"hierarchy,omitempty"`
	Href        string                  `json:"href,omitempty"`
	Locations   []TurnReferenceLocation `json:"locations,omitempty"`
	Context     []string                `json:"context,omitempty"`

	// The fields below are derived server-side and enrich model context. They
	// are never trusted when supplied by a client.
	ComponentID string `json:"componentId,omitempty"`
	VisualID    string `json:"visualId,omitempty"`
	VisualType  string `json:"visualType,omitempty"`
	DashboardID string `json:"dashboardId,omitempty"`
	PageID      string `json:"pageId,omitempty"`
	TableID     string `json:"tableId,omitempty"`
	FilterID    string `json:"filterId,omitempty"`
	ModelID     string `json:"modelId,omitempty"`
	DatasetID   string `json:"datasetId,omitempty"`
	FieldID     string `json:"fieldId,omitempty"`
	AssetID     string `json:"assetId,omitempty"`
}

type TurnReferenceKey struct {
	WorkspaceID string `json:"workspaceId"`
	Type        string `json:"type"`
	ID          string `json:"id"`
}

type TurnReferenceWorkspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type TurnReferenceLocation struct {
	DashboardID   string `json:"dashboardId,omitempty"`
	DashboardName string `json:"dashboardName,omitempty"`
	PageID        string `json:"pageId,omitempty"`
	PageName      string `json:"pageName,omitempty"`
	Href          string `json:"href"`
}

func (c TurnContext) normalized() TurnContext {
	c.Surface = strings.ToLower(strings.TrimSpace(c.Surface))
	c.WorkspaceID = strings.TrimSpace(c.WorkspaceID)
	c.DashboardID = strings.TrimSpace(c.DashboardID)
	c.DashboardTitle = strings.TrimSpace(c.DashboardTitle)
	c.PageID = strings.TrimSpace(c.PageID)
	c.PageTitle = strings.TrimSpace(c.PageTitle)
	c.ModelID = strings.TrimSpace(c.ModelID)
	c.DatasetID = strings.TrimSpace(c.DatasetID)
	if c.Exploration != nil {
		c.Exploration = normalizeDataExploration(*c.Exploration)
	}
	refs := make([]TurnReference, 0, len(c.References))
	seen := map[string]struct{}{}
	for _, ref := range c.References {
		ref.Reference.Type = strings.ToLower(strings.TrimSpace(ref.Reference.Type))
		ref.Reference.ID = strings.TrimSpace(ref.Reference.ID)
		ref.Reference.WorkspaceID = strings.TrimSpace(ref.Reference.WorkspaceID)
		ref.Name = strings.TrimSpace(ref.Name)
		ref.Description = strings.TrimSpace(ref.Description)
		ref.Workspace.ID = strings.TrimSpace(ref.Workspace.ID)
		ref.Workspace.Name = strings.TrimSpace(ref.Workspace.Name)
		hierarchy := make([]string, 0, len(ref.Hierarchy))
		for _, part := range ref.Hierarchy {
			if part = strings.TrimSpace(part); part != "" {
				hierarchy = append(hierarchy, part)
			}
		}
		ref.Hierarchy = hierarchy
		ref.Href = strings.TrimSpace(ref.Href)
		ref.ComponentID = strings.TrimSpace(ref.ComponentID)
		ref.VisualID = strings.TrimSpace(ref.VisualID)
		ref.VisualType = strings.ToLower(strings.TrimSpace(ref.VisualType))
		ref.DashboardID = strings.TrimSpace(ref.DashboardID)
		ref.PageID = strings.TrimSpace(ref.PageID)
		ref.TableID = strings.TrimSpace(ref.TableID)
		ref.FilterID = strings.TrimSpace(ref.FilterID)
		ref.ModelID = strings.TrimSpace(ref.ModelID)
		ref.DatasetID = strings.TrimSpace(ref.DatasetID)
		ref.FieldID = strings.TrimSpace(ref.FieldID)
		ref.AssetID = strings.TrimSpace(ref.AssetID)
		if ref.Reference.Type == "" || ref.Reference.ID == "" || ref.Reference.WorkspaceID == "" {
			continue
		}
		key := ref.Reference.WorkspaceID + ":" + ref.Reference.Type + ":" + ref.Reference.ID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		refs = append(refs, ref)
	}
	c.References = refs
	return c
}

// NormalizedDataExploration returns a bounded, canonical copy suitable for a
// trusted turn context after the caller validates its semantic members.
func (c TurnContext) NormalizedDataExploration() *DataExploration {
	return c.normalized().Exploration
}

func turnContextItems(context *TurnContext) []agentcore.ContextItem {
	if context == nil {
		return nil
	}
	normalized := context.normalized()
	if normalized.Surface != dashboardTurnContextSurface && normalized.Surface != dataTurnContextSurface && (normalized.Surface != "chat" || len(normalized.References) == 0) {
		return nil
	}
	return []agentcore.ContextItem{{Key: "leapview_context", Value: normalized}}
}

func normalizeDataExploration(value DataExploration) *DataExploration {
	value.Dimensions = normalizedStrings(value.Dimensions, 64)
	value.Measures = normalizedStrings(value.Measures, 64)
	if value.Limit <= 0 {
		value.Limit = 100
	} else if value.Limit > 1000 {
		value.Limit = 1000
	}
	filters := make([]DataExplorationFilter, 0, min(len(value.Filters), 32))
	for _, filter := range value.Filters {
		filter.Field = strings.TrimSpace(filter.Field)
		filter.Operator = strings.ToLower(strings.TrimSpace(filter.Operator))
		filter.Fact = strings.TrimSpace(filter.Fact)
		filter.Values = normalizedStrings(filter.Values, 100)
		if filter.Field != "" && filter.Operator != "" && len(filters) < 32 {
			filters = append(filters, filter)
		}
	}
	value.Filters = filters
	sorts := make([]DataExplorationSort, 0, min(len(value.Sort), 8))
	for _, sort := range value.Sort {
		sort.Field = strings.TrimSpace(sort.Field)
		sort.Direction = strings.ToLower(strings.TrimSpace(sort.Direction))
		if sort.Field != "" && len(sorts) < 8 {
			sorts = append(sorts, sort)
		}
	}
	value.Sort = sorts
	if value.Time != nil {
		value.Time.Field = strings.TrimSpace(value.Time.Field)
		value.Time.Grain = strings.ToLower(strings.TrimSpace(value.Time.Grain))
		value.Time.Alias = strings.TrimSpace(value.Time.Alias)
		if value.Time.Field == "" {
			value.Time = nil
		}
	}
	return &value
}

func normalizedStrings(values []string, limit int) []string {
	result := make([]string, 0, min(len(values), limit))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) == limit {
			break
		}
	}
	return result
}
