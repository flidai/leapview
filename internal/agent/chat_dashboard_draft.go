package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	agenttools "github.com/flidai/leapview/internal/agent/tools"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

// ChatDashboardDraft is the latest successful, conversation-owned dashboard
// composition. It is reconstructed from immutable tool messages rather than
// stored as an instance dashboard before the user explicitly saves it.
type ChatDashboardDraft struct {
	Revision        string
	ToolCallID      string
	Title           string
	SemanticModelID string
	Visuals         []ChatDashboardDraftVisual
	promptSource    agenttools.ComposeChatDashboardInput
}

type ChatDashboardDraftVisual struct {
	ID         string
	ArtifactID string
	Title      string
	Visual     document.DashboardVisual
	Filters    []document.DashboardFilter
}

type ChatDashboardDraftArtifact struct {
	Revision string
	Title    string
	Visuals  []ChatDashboardDraftVisualArtifact
}

type ChatDashboardDraftVisualArtifact struct {
	ID         string `json:"id"`
	ArtifactID string `json:"artifactId"`
	Title      string `json:"title"`
}

type chatDashboardDraftDisplay struct {
	Type            string                                                      `json:"type"`
	ID              string                                                      `json:"id"`
	Revision        string                                                      `json:"revision"`
	Title           string                                                      `json:"title"`
	SemanticModelID string                                                      `json:"semanticModelId"`
	Visuals         []ChatDashboardDraftVisualArtifact                          `json:"visuals"`
	Summary         string                                                      `json:"summary"`
	Patch           map[string]map[string]visualizationir.VisualizationEnvelope `json:"patch"`
}

type chatDashboardDraftSourceCall struct {
	id           string
	outputPartID string
	input        agenttools.ComposeChatDashboardInput
	valid        bool
}

// ConversationDashboardDraft returns the latest successful composition in
// the active transcript branch. The persisted assistant call contains the
// authored sources; the paired tool result supplies the validated preview
// artifact identities. Ownership is checked before message history is read.
func (s *Service) ConversationDashboardDraft(ctx context.Context, scope Scope, conversationID string) (ChatDashboardDraft, error) {
	if s == nil || s.repo == nil || strings.TrimSpace(scope.PrincipalID) == "" {
		return ChatDashboardDraft{}, ErrNotFound
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return ChatDashboardDraft{}, fmt.Errorf("conversation is required")
	}
	if _, err := s.repo.GetConversation(ctx, scope.PrincipalID, conversationID); err != nil {
		return ChatDashboardDraft{}, err
	}
	messages, err := s.repo.ListMessages(ctx, scope.PrincipalID, conversationID)
	if err != nil {
		return ChatDashboardDraft{}, err
	}
	draft, ok := conversationDashboardDraftFromMessages(activeMessageProjection(messages))
	if !ok {
		return ChatDashboardDraft{}, ErrNotFound
	}
	return draft, nil
}

func dashboardDraftPromptContextItems(messages []Message, editMessageID string) ([]agentcore.ContextItem, error) {
	active := activeMessageProjection(messages)
	if editMessageID = strings.TrimSpace(editMessageID); editMessageID != "" {
		for index, message := range active {
			if message.Role == MessageRoleUser && message.ID == editMessageID {
				active = active[:index]
				break
			}
		}
	}
	draft, ok := conversationDashboardDraftFromMessages(active)
	if !ok {
		return nil, nil
	}
	return agenttools.ChatDashboardDraftPromptContextItems(draft.Revision, draft.promptSource)
}

func promptDashboardDraftContextItems(scope Scope, turnContext *TurnContext, messages []Message, editMessageID string) ([]agentcore.ContextItem, error) {
	if scope.BuilderDashboardID != "" || scope.BuilderDraftID != "" {
		return nil, nil
	}
	if turnContext != nil {
		surface := turnContext.normalized().Surface
		if surface != "" && surface != "chat" {
			return nil, nil
		}
	}
	return dashboardDraftPromptContextItems(messages, editMessageID)
}

func promptContextItems(turnContext *TurnContext, dashboardDraftItems []agentcore.ContextItem) []agentcore.ContextItem {
	items := turnContextItems(turnContext)
	seen := make(map[string]struct{}, len(items)+len(dashboardDraftItems))
	for _, item := range items {
		seen[item.Key] = struct{}{}
	}
	for _, item := range dashboardDraftItems {
		if _, exists := seen[item.Key]; exists {
			continue
		}
		seen[item.Key] = struct{}{}
		items = append(items, item)
	}
	return items
}

// withoutPreviousDashboardDraftContext removes prior source snapshots from a
// transcript before the current active draft is injected. Other external
// context remains available to the model.
func withoutPreviousDashboardDraftContext(messages []agentcore.Message) []agentcore.Message {
	filtered := make([]agentcore.Message, 0, len(messages))
	for _, message := range messages {
		if !isDashboardDraftContextMessage(message) {
			filtered = append(filtered, message)
		}
	}
	return filtered
}

func isDashboardDraftContextMessage(message agentcore.Message) bool {
	if message.Role != agentcore.RoleUser || message.Kind != agentcore.MessageKindExternalContext {
		return false
	}
	content := message.Content
	if !strings.HasPrefix(content, "<external_") {
		return false
	}
	end := strings.Index(content, ">\n")
	if end < 0 {
		return false
	}
	tag := content[1:end]
	key := strings.TrimPrefix(tag, "external_")
	if key != "leapview_dashboard_draft" && !isIndexedDashboardVisualContextKey(key) {
		return false
	}
	return strings.HasSuffix(content, "\n</"+tag+">")
}

func isIndexedDashboardVisualContextKey(key string) bool {
	const prefix = "leapview_dashboard_visual_"
	if !strings.HasPrefix(key, prefix) {
		return false
	}
	index := strings.TrimPrefix(key, prefix)
	return len(index) == 2 && index[0] >= '0' && index[0] <= '9' && index[1] >= '0' && index[1] <= '9'
}

func conversationDashboardDraftFromMessages(messages []Message) (ChatDashboardDraft, bool) {
	callsByID := make(map[string]chatDashboardDraftSourceCall)
	callsByOutputPartID := make(map[string]chatDashboardDraftSourceCall)
	var selected ChatDashboardDraft
	selectedValid := false
	for _, message := range messages {
		if message.Role == MessageRoleAssistant {
			for _, call := range toolCallsFromContentJSON(message.ContentJSON) {
				if call.ID == "" {
					continue
				}
				candidate := chatDashboardDraftSourceCall{id: call.ID, outputPartID: call.OutputPartID}
				if call.Name == agenttools.ComposeChatDashboardToolName {
					input, err := decodeChatDashboardDraftInput(call.Arguments)
					if err == nil {
						candidate.input, candidate.valid = normalizeChatDashboardDraftInput(input)
					}
				}
				callsByID[call.ID] = candidate
				if call.OutputPartID != "" {
					callsByOutputPartID[call.OutputPartID] = candidate
				}
			}
			continue
		}
		if message.Role != MessageRoleTool || message.ToolName != agenttools.ComposeChatDashboardToolName || message.IsError {
			continue
		}
		candidate, ok := callsByID[message.ToolCallID]
		outputPartID := outputPartFromContentJSON(message.ContentJSON).ID
		if candidate.outputPartID != "" || outputPartID != "" {
			if exact, found := callsByOutputPartID[outputPartID]; outputPartID == "" || !found || exact.id != message.ToolCallID {
				ok = false
			} else {
				candidate, ok = exact, true
			}
		}
		if !ok || candidate.id != message.ToolCallID || !candidate.valid {
			continue
		}
		display, err := decodeChatDashboardDraftDisplay(displayContentJSON(message.ContentJSON))
		if err != nil {
			continue
		}
		validated, valid := validateChatDashboardDraftResult(candidate, display)
		if valid {
			selected, selectedValid = validated, true
		}
	}
	return selected, selectedValid
}

func decodeChatDashboardDraftInput(raw json.RawMessage) (agenttools.ComposeChatDashboardInput, error) {
	var input agenttools.ComposeChatDashboardInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return agenttools.ComposeChatDashboardInput{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return agenttools.ComposeChatDashboardInput{}, fmt.Errorf("arguments must contain exactly one JSON object")
	}
	return input, nil
}

func normalizeChatDashboardDraftInput(input agenttools.ComposeChatDashboardInput) (agenttools.ComposeChatDashboardInput, bool) {
	input.Title = strings.TrimSpace(input.Title)
	input.SemanticModelID = strings.TrimSpace(input.SemanticModelID)
	if input.Title == "" || len([]rune(input.Title)) > 255 || input.SemanticModelID == "" || input.Visuals == nil || len(input.Visuals) > 12 {
		return agenttools.ComposeChatDashboardInput{}, false
	}
	seen := make(map[string]struct{}, len(input.Visuals))
	for index := range input.Visuals {
		visual := &input.Visuals[index]
		visual.ID = strings.TrimSpace(visual.ID)
		if visual.ID == "" || len(visual.ID) > 128 || visual.ID[0] != '_' && !isASCIIAlpha(visual.ID[0]) {
			return agenttools.ComposeChatDashboardInput{}, false
		}
		for _, char := range visual.ID {
			if !(char == '_' || char == '-' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9') {
				return agenttools.ComposeChatDashboardInput{}, false
			}
		}
		if _, exists := seen[visual.ID]; exists {
			return agenttools.ComposeChatDashboardInput{}, false
		}
		seen[visual.ID] = struct{}{}
		if visual.Filters != nil && len(*visual.Filters) > 100 {
			return agenttools.ComposeChatDashboardInput{}, false
		}
		normalized, err := agenttools.NormalizeChatVisualDefinition(visual.Visual)
		if err != nil {
			return agenttools.ComposeChatDashboardInput{}, false
		}
		visual.Visual = normalized
	}
	return input, true
}

func isASCIIAlpha(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func decodeChatDashboardDraftDisplay(raw string) (chatDashboardDraftDisplay, error) {
	var display chatDashboardDraftDisplay
	if strings.TrimSpace(raw) == "" {
		return chatDashboardDraftDisplay{}, fmt.Errorf("dashboard draft display content is missing")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&display); err != nil {
		return chatDashboardDraftDisplay{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return chatDashboardDraftDisplay{}, fmt.Errorf("dashboard draft display must contain one JSON object")
	}
	return display, nil
}

func validateChatDashboardDraftResult(call chatDashboardDraftSourceCall, display chatDashboardDraftDisplay) (ChatDashboardDraft, bool) {
	if display.Type != "dashboard_draft" || display.ID != call.id || display.Title != call.input.Title ||
		display.SemanticModelID != call.input.SemanticModelID || display.Revision == "" || len(display.Visuals) != len(call.input.Visuals) {
		return ChatDashboardDraft{}, false
	}
	revision, err := agenttools.ChatDashboardDraftRevision(call.id, call.input)
	if err != nil || display.Revision != revision || len(display.Patch) != 1 {
		return ChatDashboardDraft{}, false
	}
	patch, ok := display.Patch["visuals"]
	if !ok || len(patch) != len(display.Visuals) {
		return ChatDashboardDraft{}, false
	}
	visuals := make([]ChatDashboardDraftVisual, 0, len(call.input.Visuals))
	seenArtifactIDs := make(map[string]struct{}, len(call.input.Visuals))
	for index, source := range call.input.Visuals {
		artifact := display.Visuals[index]
		if artifact.ID != source.ID || strings.TrimSpace(artifact.ArtifactID) == "" || strings.TrimSpace(artifact.Title) == "" {
			return ChatDashboardDraft{}, false
		}
		if _, exists := seenArtifactIDs[artifact.ArtifactID]; exists {
			return ChatDashboardDraft{}, false
		}
		seenArtifactIDs[artifact.ArtifactID] = struct{}{}
		envelope, exists := patch[artifact.ArtifactID]
		if !exists || envelope.VisualID != artifact.ArtifactID || visualizationir.ValidateEnvelope(envelope) != nil {
			return ChatDashboardDraft{}, false
		}
		filters := []document.DashboardFilter(nil)
		if source.Filters != nil {
			filters = append(filters, (*source.Filters)...)
		}
		visuals = append(visuals, ChatDashboardDraftVisual{
			ID: source.ID, ArtifactID: artifact.ArtifactID, Title: artifact.Title,
			Visual: source.Visual, Filters: filters,
		})
	}
	return ChatDashboardDraft{
		Revision: display.Revision, ToolCallID: call.id, Title: display.Title,
		SemanticModelID: display.SemanticModelID, Visuals: visuals, promptSource: call.input,
	}, true
}
