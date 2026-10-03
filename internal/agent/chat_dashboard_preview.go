package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	agenttools "github.com/flidai/leapview/internal/agent/tools"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

// Preview focus supplements a composed draft. Only a persisted successful query
// supplies source; the browser supplies an artifact identity, never chart rows.
func (s *Service) promptDashboardPreviewContextItems(ctx context.Context, input PromptInput, messages []Message) ([]agentcore.ContextItem, error) {
	items, err := promptDashboardDraftContextItems(input.Scope, input.Context, messages, input.EditMessageID)
	if err != nil || input.Context == nil || strings.TrimSpace(input.Context.PreviewArtifactID) == "" {
		return items, err
	}
	if input.Context.normalized().Surface != "chat" || input.Scope.BuilderDashboardID != "" || input.Scope.BuilderDraftID != "" {
		return nil, fmt.Errorf("visual preview requires chat context")
	}
	artifactID := strings.TrimSpace(input.Context.PreviewArtifactID)
	artifact, err := s.ConversationVisualArtifact(ctx, input.Scope, input.ConversationID, artifactID)
	if err != nil {
		return nil, err
	}
	// Editing an earlier user message cannot attach a visual from its future.
	active := activeMessageProjection(messages)
	if input.EditMessageID != "" {
		for index, message := range active {
			if message.Role == MessageRoleUser && message.ID == input.EditMessageID {
				active = active[:index]
				break
			}
		}
	}
	if _, err := conversationVisualArtifactFromMessages(active, artifactID); err != nil {
		return nil, err
	}
	visual, err := agenttools.NormalizeChatVisualDefinition(artifact.Visual)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(artifactID))
	visualID := "preview_" + hex.EncodeToString(digest[:8])
	title := "Visual preview"
	if visual.Title != nil && strings.TrimSpace(*visual.Title) != "" {
		title = strings.TrimSpace(*visual.Title)
	}
	source := agenttools.ComposeChatDashboardInput{Title: title, SemanticModelID: artifact.SemanticModelID, Visuals: []agenttools.ChatDashboardVisualInput{{ID: visualID, Visual: visual, Filters: &artifact.Filters}}}
	// Reuse the complete-source bounds enforced for composed dashboard context.
	if _, err := agenttools.ChatDashboardDraftPromptContextItems(artifact.ToolCallID, source); err != nil {
		return nil, err
	}
	items = append(items, agentcore.ContextItem{Key: "leapview_dashboard_preview", Value: map[string]any{
		"artifactId": artifactID, "visualId": visualID, "semanticModelId": artifact.SemanticModelID,
		"visual": visual, "filters": artifact.Filters,
		"focus": "This is the selected visual preview for this turn. Apply requested visual edits to this canonical source. Use compose_chat_dashboard to show an updated preview. If a dashboard draft is also present, preserve its complete snapshot and existing visual IDs; do not silently replace it with this visual. Ask if the requested destination is ambiguous.",
	}})
	return items, nil
}
