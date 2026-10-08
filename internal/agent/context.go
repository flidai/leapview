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
	builderTurnContextSurface   = "dashboard_builder"
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
	DraftID        string                       `json:"draftId,omitempty"`
	DraftRevision  *DraftRevision               `json:"draftRevision,omitempty"`
	PageID         string                       `json:"pageId,omitempty"`
	PageTitle      string                       `json:"pageTitle,omitempty"`
	ModelID        string                       `json:"modelId,omitempty"`
	DatasetID      string                       `json:"datasetId,omitempty"`
	Exploration    *exploration.ExplorationSpec `json:"exploration,omitempty"`
	Generation     int64                        `json:"generation,omitempty"`
	Filters        map[string]any               `json:"filters,omitempty"`
	References     []TurnReference              `json:"references,omitempty"`
}

// DraftRevision is server-resolved concurrency evidence for authoring tools.
type DraftRevision struct {
	RevisionID  string `json:"revisionId"`
	Number      int64  `json:"number"`
	ContentHash string `json:"contentHash"`
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
	// The browser context includes presentation metadata alongside the turn
	// inputs. Accept it without letting it override MaxTurnReferences or enter
	// the server-resolved context sent to the model.
	var decoded struct {
		turnContext
		ReferenceLimit *int32 `json:"referenceLimit,omitempty"`
	}
	if err := strictjson.DecodeWithOptions(data, &decoded, turnContextJSONOptions); err != nil {
		return err
	}
	*c = TurnContext(decoded.turnContext)
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
	c.DraftID = strings.TrimSpace(c.DraftID)
	if c.DraftRevision != nil {
		c.DraftRevision.RevisionID = strings.TrimSpace(c.DraftRevision.RevisionID)
		c.DraftRevision.ContentHash = strings.TrimSpace(c.DraftRevision.ContentHash)
	}
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
	if normalized.Surface != dashboardTurnContextSurface && normalized.Surface != builderTurnContextSurface && normalized.Surface != dataTurnContextSurface && ((normalized.Surface != "chat" && normalized.Surface != "builder") || len(normalized.References) == 0) {
		return nil
	}
	items := []agentcore.ContextItem{{Key: "leapview_context", Value: normalized}}
	if normalized.Surface == builderTurnContextSurface {
		items = append(items, agentcore.ContextItem{Key: "leapview_builder_v1_policy", Value: "Edit only this open dashboard draft. Creating, forking, deleting, publishing, archiving, or changing visibility is not available through the agent. Ask the user to create a dashboard and select its semantic model in the UI first. A tool preview query can be blocked independently of the live Builder preview; do not claim the Builder chart failed unless its own status confirms that."})
	}
	return items
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

// Product behavior belongs in trusted system instructions, not in reference
// labels inside the untrusted external context message. The condition uses the
// current turn's server-resolved surface, including after a durable resume.
func withBuilderAuthoringGuidance(prompt string) string {
	return prompt + "\n\n" + `When the current turn's leapview_context surface is builder, the user is working in a dashboard builder. A request to create or show a new chart means add it directly to the context dashboardId and pageId draft. Use get_dashboard_draft, add_dashboard_visual, and assign_dashboard_field with authoritative field IDs and the latest returned revision. Complete the required chart fields. Do not stop at query_visual or ask the user to click Add to Dashboard or Preview. The builder refreshes and exposes the new visual's details card. Respect an explicit preview-only request, another destination, or a request for no changes. Questions about existing visuals do not authorize creating duplicates. For surface chat, create read-only visual previews unless the user explicitly asks to edit a dashboard. These rules do not grant access beyond the authorized tools.

For a request to build a complete dashboard, produce the complete private draft, not a collection of chat-only visual previews. First resolve the referenced semantic model and inspect its authoritative fields. Ask for a data source only if none can be identified. Create a dashboard draft, read_dashboard_source, then use edit_dashboard_source to add the fully bound visual definitions, useful filters, and page layout together in one atomic edit. Avoid a separate model round trip for every chart and every field. Use the canonical source and documented schema; do not guess field IDs or invent measures. Compose a compact layout: key metrics first, a wider trend when relevant, and complementary comparisons side by side. Omit chart types that the data cannot support. Preserve existing content when extending a dashboard. Finish with preview_dashboard_draft for the intended page and correct validation errors before calling the result ready. The app opens a successfully previewed new dashboard beside the same conversation. Do not publish automatically. Model and query latency are real; do not promise a one-second completion.

After creating visuals or authoring a dashboard, start the final answer with one short paragraph stating the result, destination page when applicable, and any important limitations. Put the visual inventory, filter explanation, layout, and validation notes in following paragraphs; the chat shows these behind View details. Keep the opening summary concise, avoid repeating tool progress, and include full configuration or source only when requested.`
}
