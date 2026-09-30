package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent/contracts"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

const (
	ComposeChatDashboardToolName       = "compose_chat_dashboard"
	maxChatDashboardVisuals            = 12
	maxChatDashboardDraftPayloadBytes  = 768 << 10
	maxChatDashboardDraftContextBytes  = 48 << 10
	maxChatDashboardVisualContextBytes = 15 << 10
)

var chatDashboardVisualIDPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,127}$`)

// ConversationAuthorizeFunc verifies that the authenticated principal owns
// the server-selected conversation that will retain the draft artifact.
type ConversationAuthorizeFunc func(context.Context, Scope) error

// DashboardComposeProvider creates a complete, private-in-conversation
// dashboard snapshot. It stores no project resource; Save is a separate
// authenticated UI command that creates the durable dashboard draft.
type DashboardComposeProvider struct {
	Visual                VisualProvider
	AuthorizeConversation ConversationAuthorizeFunc
}

type ComposeChatDashboardInput = contracts.ComposeChatDashboardInput
type ChatDashboardVisualInput = contracts.ChatDashboardVisualInput
type ComposeChatDashboardResult = contracts.ComposeChatDashboardResult
type ChatDashboardVisualArtifact = contracts.ChatDashboardVisualArtifact

type chatDashboardDraftDisplay struct {
	Type            string                                                      `json:"type"`
	ID              string                                                      `json:"id"`
	Revision        string                                                      `json:"revision"`
	Title           string                                                      `json:"title"`
	SemanticModelID string                                                      `json:"semanticModelId"`
	Visuals         []ChatDashboardVisualArtifact                               `json:"visuals"`
	Summary         string                                                      `json:"summary"`
	Patch           map[string]map[string]visualizationir.VisualizationEnvelope `json:"patch"`
}

type chatDashboardDraftContextManifest struct {
	Revision        string   `json:"revision"`
	Title           string   `json:"title"`
	SemanticModelID string   `json:"semanticModelId"`
	VisualIDs       []string `json:"visualIds"`
}

type chatDashboardVisualContextSource struct {
	ID      string                     `json:"id"`
	Visual  document.DashboardVisual   `json:"visual"`
	Filters []document.DashboardFilter `json:"filters,omitempty"`
}

func (p DashboardComposeProvider) Definitions(scope Scope) []agentcore.ToolDefinition {
	if strings.TrimSpace(scope.PrincipalID) == "" || strings.TrimSpace(scope.ConversationID) == "" || p.AuthorizeConversation == nil {
		return nil
	}
	return []agentcore.ToolDefinition{p.definition(scope)}
}

func (p DashboardComposeProvider) contractDefinitions() []agentcore.ToolDefinition {
	return []agentcore.ToolDefinition{p.definition(Scope{})}
}

func (p DashboardComposeProvider) definition(scope Scope) agentcore.ToolDefinition {
	return agentcore.ToolDefinition{
		Name:         ComposeChatDashboardToolName,
		Description:  "Compose or update the complete dashboard draft in this conversation. Provide every visual in its final order on each call, preserving existing IDs and canonical visual definitions when editing. Each visual is queried from the authorized semantic model; inline data and arbitrary expressions are not accepted. A successful result replaces the chat draft snapshot. Saving it as a dashboard requires the user's explicit Save action.",
		InputSchema:  json.RawMessage(contracts.ComposeChatDashboardInputSchemaJSON),
		OutputSchema: json.RawMessage(contracts.ComposeChatDashboardResultSchemaJSON),
		Effect:       "read",
		Tags:         []string{"dashboard", "compose", "visualization"},
		Handler: agentcore.ToolHandlerFunc(func(ctx context.Context, call agentcore.ToolCall) (agentcore.ToolResult, error) {
			return p.Run(ctx, scope, call), nil
		}),
	}
}

func (p DashboardComposeProvider) Run(ctx context.Context, scope Scope, call agentcore.ToolCall) agentcore.ToolResult {
	if strings.TrimSpace(scope.PrincipalID) == "" || strings.TrimSpace(scope.ConversationID) == "" || p.AuthorizeConversation == nil {
		return ToolError("authentication_required", "dashboard composition requires an authenticated chat conversation")
	}
	if err := p.AuthorizeConversation(ctx, scope); err != nil {
		return ToolError("not_found", "chat conversation not found")
	}
	if strings.TrimSpace(call.ID) == "" {
		return ToolError("invalid_arguments", "tool call ID is required")
	}
	if len(call.Arguments) == 0 || len(call.Arguments) > maxChatDashboardDraftPayloadBytes {
		return ToolError("invalid_arguments", fmt.Sprintf("dashboard draft source must be at most %d bytes", maxChatDashboardDraftPayloadBytes))
	}
	input, err := decodeComposeChatDashboardInput(call.Arguments)
	if err != nil {
		return ToolError("invalid_arguments", err.Error())
	}
	if p.Visual.Resolve == nil || p.Visual.SemanticModel == nil || p.Visual.QueryDefinition == nil {
		return ToolError("catalog_unavailable", "governed semantic visualization runtime is not configured")
	}
	input.Title = strings.TrimSpace(input.Title)
	input.SemanticModelID = strings.TrimSpace(input.SemanticModelID)
	if input.Title == "" {
		return ToolError("invalid_arguments", "title is required")
	}
	if utf8.RuneCountInString(input.Title) > 255 {
		return ToolError("invalid_arguments", "title must be at most 255 characters")
	}
	if input.SemanticModelID == "" {
		return ToolError("invalid_arguments", "semanticModelId is required")
	}
	if input.Visuals == nil || len(input.Visuals) > maxChatDashboardVisuals {
		return ToolError("invalid_arguments", fmt.Sprintf("visuals must contain at most %d items", maxChatDashboardVisuals))
	}
	modelID, err := projectgraph.NewResourceID(input.SemanticModelID)
	if err != nil {
		return ToolError("invalid_arguments", "semanticModelId is invalid")
	}
	resolvedModel, err := p.Visual.Resolve(ctx, scope, modelID, projectgraph.KindSemanticModel, access.CapabilityResourceUse)
	if err != nil || resolvedModel != modelID {
		return ToolError("catalog_not_found", "semantic model is unknown or unauthorized")
	}
	if model, ok := p.Visual.SemanticModel(scope.ProjectID, resolvedModel.String()); !ok || model == nil {
		return ToolError("catalog_not_found", "semantic model is unknown or unauthorized")
	}
	seen := make(map[string]struct{}, len(input.Visuals))
	for index := range input.Visuals {
		visual := &input.Visuals[index]
		visual.ID = strings.TrimSpace(visual.ID)
		if !chatDashboardVisualIDPattern.MatchString(visual.ID) {
			return ToolError("invalid_arguments", fmt.Sprintf("visuals[%d].id is invalid", index))
		}
		if _, exists := seen[visual.ID]; exists {
			return ToolError("invalid_arguments", fmt.Sprintf("visual id %q appears more than once", visual.ID))
		}
		seen[visual.ID] = struct{}{}
		if visual.Filters != nil && len(*visual.Filters) > 100 {
			return ToolError("invalid_arguments", fmt.Sprintf("visuals[%d].filters must contain at most 100 items", index))
		}
		if visual.Visual.Title == nil || strings.TrimSpace(*visual.Visual.Title) == "" {
			return ToolError("invalid_arguments", fmt.Sprintf("visuals[%d].visual.title is required; provide a human-readable chart title", index))
		}
		normalized, err := NormalizeChatVisualDefinition(visual.Visual)
		if err != nil {
			return ToolError("invalid_arguments", fmt.Sprintf("visuals[%d].visual is invalid: %v", index, err))
		}
		visual.Visual = normalized
	}
	revision, err := validateChatDashboardDraftContextSize(call.ID, input)
	if err != nil {
		return ToolError("invalid_arguments", err.Error())
	}
	artifacts := make([]ChatDashboardVisualArtifact, 0, len(input.Visuals))
	envelopes := make(map[string]visualizationir.VisualizationEnvelope, len(input.Visuals))
	for index := range input.Visuals {
		visual := &input.Visuals[index]
		visualInput := agentVisualInput{SemanticModelID: input.SemanticModelID, Visual: visual.Visual}
		if visual.Filters != nil {
			visualInput.Filters = append([]document.DashboardFilter(nil), (*visual.Filters)...)
		}
		arguments, err := json.Marshal(visualInput)
		if err != nil {
			return ToolError("invalid_arguments", "could not encode a visual definition")
		}
		result := p.Visual.Run(ctx, scope, agentcore.ToolCall{ID: chatDashboardVisualCallID(call.ID, visual.ID), Arguments: arguments})
		if result.IsError {
			return dashboardVisualFailure(index, result)
		}
		compact, ok := result.Content.(contracts.QueryVisualResult)
		if !ok || !compact.Ok || strings.TrimSpace(compact.ID) == "" || strings.TrimSpace(compact.Title) == "" {
			return ToolError("query_visual_failed", fmt.Sprintf("visuals[%d] did not produce a valid governed visualization", index))
		}
		display, ok := result.DisplayContent.(agentVisualResult)
		if !ok {
			return ToolError("query_visual_failed", fmt.Sprintf("visuals[%d] did not produce a preview", index))
		}
		envelope, ok := display.Patch["visuals"][compact.ID]
		if !ok || envelope.VisualID != compact.ID || visualizationir.ValidateEnvelope(envelope) != nil {
			return ToolError("query_visual_failed", fmt.Sprintf("visuals[%d] did not produce a valid preview", index))
		}
		artifacts = append(artifacts, ChatDashboardVisualArtifact{ID: visual.ID, ArtifactID: compact.ID, Title: compact.Title})
		envelopes[compact.ID] = envelope
	}

	summary := fmt.Sprintf("Composed %q with %d visuals.", input.Title, len(artifacts))
	content := ComposeChatDashboardResult{
		ID: call.ID, Revision: revision, Title: input.Title, SemanticModelID: input.SemanticModelID,
		Visuals: artifacts, Summary: summary,
	}
	display := chatDashboardDraftDisplay{
		Type: "dashboard_draft", ID: call.ID, Revision: revision, Title: input.Title,
		SemanticModelID: input.SemanticModelID, Visuals: artifacts, Summary: summary,
		Patch: map[string]map[string]visualizationir.VisualizationEnvelope{"visuals": envelopes},
	}
	encoded, err := json.Marshal(display)
	if err != nil || len(encoded) > maxChatDashboardDraftPayloadBytes {
		return ToolError("query_visual_failed", "dashboard preview is too large to retain in chat; reduce visual row budgets or remove visuals")
	}
	return agentcore.ToolResult{Content: content, DisplayContent: display}
}

func decodeComposeChatDashboardInput(raw []byte) (ComposeChatDashboardInput, error) {
	var input ComposeChatDashboardInput
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return ComposeChatDashboardInput{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ComposeChatDashboardInput{}, fmt.Errorf("arguments must contain exactly one JSON object")
	}
	return input, nil
}

func dashboardVisualFailure(index int, result agentcore.ToolResult) agentcore.ToolResult {
	if message := dashboardComposeToolErrorMessage(result); message != "" {
		return ToolError("query_visual_failed", fmt.Sprintf("visuals[%d] could not be composed: %s", index, message))
	}
	return ToolError("query_visual_failed", fmt.Sprintf("visuals[%d] could not be composed", index))
}

func chatDashboardVisualCallID(parentID, visualID string) string {
	sum := sha256.Sum256([]byte(parentID + "\x00" + visualID))
	return "compose_" + hex.EncodeToString(sum[:16])
}

func ChatDashboardDraftRevision(toolCallID string, input ComposeChatDashboardInput) (string, error) {
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID == "" {
		return "", fmt.Errorf("tool call ID is required")
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(toolCallID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(encoded)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func validateChatDashboardDraftContextSize(toolCallID string, input ComposeChatDashboardInput) (string, error) {
	revision, err := ChatDashboardDraftRevision(toolCallID, input)
	if err != nil {
		return "", fmt.Errorf("could not fingerprint the dashboard draft")
	}
	if _, err := ChatDashboardDraftPromptContextItems(revision, input); err != nil {
		return "", err
	}
	return revision, nil
}

// ChatDashboardDraftPromptContextItems builds and bounds the exact untrusted
// source items used to restore a saved conversation draft on later turns.
func ChatDashboardDraftPromptContextItems(revision string, input ComposeChatDashboardInput) ([]agentcore.ContextItem, error) {
	revision = strings.TrimSpace(revision)
	if revision == "" {
		return nil, fmt.Errorf("dashboard draft revision is required for prompt context")
	}
	manifest := chatDashboardDraftContextManifest{
		Revision: revision, Title: input.Title, SemanticModelID: input.SemanticModelID,
		VisualIDs: make([]string, 0, len(input.Visuals)),
	}
	for _, visual := range input.Visuals {
		manifest.VisualIDs = append(manifest.VisualIDs, visual.ID)
	}
	items := make([]agentcore.ContextItem, 0, len(input.Visuals)+1)
	items = append(items, agentcore.ContextItem{Key: "leapview_dashboard_draft", Value: manifest})
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("dashboard draft cannot be encoded for prompt context")
	}
	if len(manifestJSON) > maxChatDashboardVisualContextBytes {
		return nil, fmt.Errorf("dashboard draft metadata is too large to preserve in later chat turns")
	}
	contextBytes := len(manifestJSON)
	for _, visual := range input.Visuals {
		filters := []document.DashboardFilter(nil)
		if visual.Filters != nil {
			filters = append(filters, (*visual.Filters)...)
		}
		encoded, err := json.Marshal(chatDashboardVisualContextSource{ID: visual.ID, Visual: visual.Visual, Filters: filters})
		if err != nil {
			return nil, fmt.Errorf("dashboard visual %q cannot be encoded for prompt context", visual.ID)
		}
		if len(encoded) > maxChatDashboardVisualContextBytes {
			return nil, fmt.Errorf("visual %q source is too large to preserve in later chat turns; reduce its definition or filters", visual.ID)
		}
		contextBytes += len(encoded)
		items = append(items, agentcore.ContextItem{
			Key: fmt.Sprintf("leapview_dashboard_visual_%02d", len(items)-1),
			Value: chatDashboardVisualContextSource{ID: visual.ID, Visual: visual.Visual, Filters: filters},
		})
	}
	if contextBytes > maxChatDashboardDraftContextBytes {
		return nil, fmt.Errorf("dashboard source exceeds the %d KiB limit for conversational updates", maxChatDashboardDraftContextBytes>>10)
	}
	return items, nil
}

func dashboardComposeToolErrorMessage(result agentcore.ToolResult) string {
	if content, ok := result.Content.(map[string]any); ok {
		if raw, ok := content["error"].(map[string]any); ok {
			if message, ok := raw["message"].(string); ok {
				return strings.TrimSpace(message)
			}
		}
	}
	encoded, err := json.Marshal(result.Content)
	if err != nil || len(encoded) > 512 {
		return ""
	}
	return string(encoded)
}
