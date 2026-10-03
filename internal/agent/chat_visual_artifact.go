package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/document"
)

// ChatVisualArtifact is the authored source for a successfully completed
// query_visual tool call. It is loaded from persisted conversation messages;
// rendered visualization envelopes are deliberately not used as source.
type ChatVisualArtifact struct {
	ArtifactID      string
	ToolCallID      string
	SemanticModelID string
	Visual          document.DashboardVisual
	Filters         []document.DashboardFilter
}

// ConversationVisualArtifact resolves one completed visual artifact back to
// its original query_visual arguments. Conversation and message ownership are
// checked through the persisted repository before any source is returned.
func (s *Service) ConversationVisualArtifact(ctx context.Context, scope Scope, conversationID, artifactID string) (ChatVisualArtifact, error) {
	if s == nil || s.repo == nil || strings.TrimSpace(scope.PrincipalID) == "" {
		return ChatVisualArtifact{}, ErrNotFound
	}
	conversationID = strings.TrimSpace(conversationID)
	artifactID = strings.TrimSpace(artifactID)
	if conversationID == "" || artifactID == "" {
		return ChatVisualArtifact{}, fmt.Errorf("conversation and artifact are required")
	}
	if _, err := s.repo.GetConversation(ctx, scope.PrincipalID, conversationID); err != nil {
		return ChatVisualArtifact{}, err
	}
	messages, err := s.repo.ListMessages(ctx, scope.PrincipalID, conversationID)
	if err != nil {
		return ChatVisualArtifact{}, err
	}
	type visualCallSource struct {
		callID       string
		outputPartID string
		source       ChatVisualArtifact
		valid        bool
	}
	callsByID := make(map[string]visualCallSource)
	callsByOutputPartID := make(map[string]visualCallSource)
	var selected ChatVisualArtifact
	selectedValid := false
	for _, message := range activeMessageProjection(messages) {
		if message.Role == MessageRoleAssistant {
			for _, call := range toolCallsFromContentJSON(message.ContentJSON) {
				if call.ID == "" {
					continue
				}
				candidate := visualCallSource{callID: call.ID, outputPartID: call.OutputPartID}
				if call.Name == "query_visual" {
					var input struct {
						SemanticModelID string                     `json:"semanticModelId"`
						Visual          document.DashboardVisual   `json:"visual"`
						Filters         []document.DashboardFilter `json:"filters"`
					}
					decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
					decoder.DisallowUnknownFields()
					if err := decoder.Decode(&input); err == nil && strings.TrimSpace(input.SemanticModelID) != "" && input.Visual.Query.Value != nil {
						candidate.source = ChatVisualArtifact{
							ToolCallID: call.ID, SemanticModelID: strings.TrimSpace(input.SemanticModelID),
							Visual: input.Visual, Filters: append([]document.DashboardFilter(nil), input.Filters...),
						}
						candidate.valid = true
					}
				}
				callsByID[call.ID] = candidate
				if call.OutputPartID != "" {
					callsByOutputPartID[call.OutputPartID] = candidate
				}
			}
			continue
		}
		if message.Role != MessageRoleTool || message.ToolName != "query_visual" || message.IsError {
			continue
		}
		artifact, _ := toolArtifact(message.ContentJSON)
		if artifact == nil || artifact.ID != artifactID {
			continue
		}

		candidate, ok := callsByID[message.ToolCallID]
		outputPartID := outputPartFromContentJSON(message.ContentJSON).ID
		if outputPartID != "" {
			if exact, found := callsByOutputPartID[outputPartID]; found {
				candidate, ok = exact, true
			} else if candidate.outputPartID != "" {
				ok = false
			}
		}
		if ok && candidate.callID != message.ToolCallID {
			ok = false
		}
		selectedValid = ok && candidate.valid
		if selectedValid {
			selected = candidate.source
			selected.ArtifactID = artifact.ID
		}
	}
	if selectedValid {
		return selected, nil
	}
	return ChatVisualArtifact{}, ErrNotFound
}
