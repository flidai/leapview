package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	agentcore "github.com/flidai/leapview/pkg/agent"
	"github.com/flidai/leapview/pkg/strictjson"
)

const (
	dashboardTurnContextSurface = "dashboard"
	dataTurnContextSurface      = "data"
)

const MaxTurnReferences = 12

const (
	turnContextMaxBytes = int64(1 << 20)
	turnContextMaxDepth = 32
)

var turnContextJSONOptions = strictjson.Options{MaxBytes: turnContextMaxBytes, MaxDepth: turnContextMaxDepth}

// TurnContext is server-resolved product context for one user turn. It is
// deliberately separate from Scope: Scope controls authorization, while this
// value describes the dashboard state the user is asking about.
type TurnContext struct {
	Surface        string                       `json:"surface"`
	DashboardID    string                       `json:"dashboardId,omitempty"`
	DashboardTitle string                       `json:"dashboardTitle,omitempty"`
	PageID         string                       `json:"pageId,omitempty"`
	PageTitle      string                       `json:"pageTitle,omitempty"`
	ModelID        string                       `json:"modelId,omitempty"`
	DatasetID      string                       `json:"datasetId,omitempty"`
	Exploration    *exploration.ExplorationSpec `json:"exploration,omitempty"`
	Generation     int64                        `json:"generation,omitempty"`
	Filters        map[string]any               `json:"filters,omitempty"`
	References     []TurnReference              `json:"references,omitempty"`
}

// UnmarshalJSON rejects the former client-selectable project field instead
// of silently ignoring it. Context is always rebound to the active serving
// project by the agent module; accepting projectId here would create a
// compatibility path that lets callers select a different project.
func (c *TurnContext) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := strictjson.DecodeWithOptions(data, &fields, turnContextJSONOptions); err != nil {
		return err
	}
	for key := range fields {
		if strings.EqualFold(key, "projectId") {
			return errors.New("projectId is server-bound and must not be supplied")
		}
	}
	type turnContext TurnContext
	var decoded turnContext
	if err := strictjson.DecodeWithOptions(data, &decoded, turnContextJSONOptions); err != nil {
		return err
	}
	*c = TurnContext(decoded)
	return nil
}

type TurnReference struct {
	Reference   TurnReferenceKey        `json:"reference"`
	Name        string                  `json:"name,omitempty"`
	Description string                  `json:"description,omitempty"`
	Resource    TurnReferenceResource   `json:"resource"`
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
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type TurnReferenceResource struct {
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
	c.DashboardID = strings.TrimSpace(c.DashboardID)
	c.DashboardTitle = strings.TrimSpace(c.DashboardTitle)
	c.PageID = strings.TrimSpace(c.PageID)
	c.PageTitle = strings.TrimSpace(c.PageTitle)
	c.ModelID = strings.TrimSpace(c.ModelID)
	c.DatasetID = strings.TrimSpace(c.DatasetID)
	refs := make([]TurnReference, 0, len(c.References))
	seen := map[string]struct{}{}
	for _, ref := range c.References {
		ref.Reference.Kind = strings.ToLower(strings.TrimSpace(ref.Reference.Kind))
		ref.Reference.ID = strings.TrimSpace(ref.Reference.ID)
		ref.Name = strings.TrimSpace(ref.Name)
		ref.Description = strings.TrimSpace(ref.Description)
		ref.Resource.ID = strings.TrimSpace(ref.Resource.ID)
		ref.Resource.Name = strings.TrimSpace(ref.Resource.Name)
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
		if ref.Reference.Kind == "" || ref.Reference.ID == "" {
			continue
		}
		key := ref.Reference.Kind + ":" + ref.Reference.ID
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
// trusted turn context after the caller validates its semantic members. The
// copy is made through the generated contract codec so malformed union
// discriminators and unsupported variants cannot be silently accepted.
func (c TurnContext) NormalizedDataExploration() (*exploration.ExplorationSpec, error) {
	return normalizeDataExploration(c.Exploration)
}

func turnContextItems(context *TurnContext) []agentcore.ContextItem {
	if context == nil {
		return nil
	}
	normalized := context.normalized()
	if normalized.Exploration != nil {
		canonical, err := normalizeDataExploration(normalized.Exploration)
		if err != nil {
			return nil
		}
		normalized.Exploration = canonical
	}
	if normalized.Surface != dashboardTurnContextSurface && normalized.Surface != dataTurnContextSurface && (normalized.Surface != "chat" || len(normalized.References) == 0) {
		return nil
	}
	return []agentcore.ContextItem{{Key: "leapview_context", Value: normalized}}
}

func normalizeDataExploration(value *exploration.ExplorationSpec) (*exploration.ExplorationSpec, error) {
	if value == nil {
		return nil, nil
	}
	if err := exploration.ValidateShape(value); err != nil {
		return nil, err
	}

	// Marshal/Unmarshal performs a complete generated-contract round trip. In
	// addition to making the result independent of caller-owned slices and
	// pointers, this invokes every generated union decoder and therefore
	// rejects unknown or malformed discriminators without dropping fields.
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode exploration spec: %w", err)
	}
	var canonical exploration.ExplorationSpec
	if err := strictjson.DecodeWithOptions(encoded, &canonical, turnContextJSONOptions); err != nil {
		return nil, fmt.Errorf("decode exploration spec: %w", err)
	}
	if err := exploration.ValidateShape(&canonical); err != nil {
		return nil, err
	}
	return &canonical, nil
}
