package module

import (
	"context"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/agent/ui"
)

func (m *Module) generateConversationTitleAsync(scope agent.Scope, conversationID, clientID string) {
	if m.service == nil {
		m.clearChatTitlePending(conversationID)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := m.service.GenerateConversationTitle(ctx, scope, conversationID); err != nil && m.logger != nil {
			m.logger.DebugContext(ctx, "agent title generation failed", "conversation_id", conversationID, "error", err)
		}
		m.clearChatTitlePending(conversationID)
		if m.broker != nil {
			conversations := m.chatConversations(ctx, scope)
			m.broker.Publish(ChatStreamID(scope, clientID), ui.ChatConversationsPatch(conversations, ""))
			m.broker.Publish(ChatConversationStreamID(scope, clientID, conversationID), ui.ChatConversationsPatch(conversations, conversationID))
		}
	}()
}

// queueMissingChatTitle repairs old one-turn chats that missed the async title job.
func (m *Module) queueMissingChatTitle(ctx context.Context, scope agent.Scope, conversationID, clientID string) {
	if m.service == nil || m.isChatTitlePending(conversationID) {
		return
	}
	ok, err := m.service.ConversationNeedsGeneratedTitle(ctx, scope, conversationID)
	if err != nil || !ok {
		return
	}
	m.markChatTitlePending(conversationID)
	m.generateConversationTitleAsync(scope, conversationID, clientID)
}

func (m *Module) markChatTitlePending(conversationID string) {
	if conversationID == "" {
		return
	}
	m.chatTitleMu.Lock()
	defer m.chatTitleMu.Unlock()
	if m.pendingChatTitles == nil {
		m.pendingChatTitles = map[string]struct{}{}
	}
	m.pendingChatTitles[conversationID] = struct{}{}
}

func (m *Module) clearChatTitlePending(conversationID string) {
	if conversationID == "" {
		return
	}
	m.chatTitleMu.Lock()
	defer m.chatTitleMu.Unlock()
	delete(m.pendingChatTitles, conversationID)
}

func (m *Module) isChatTitlePending(conversationID string) bool {
	m.chatTitleMu.Lock()
	defer m.chatTitleMu.Unlock()
	_, ok := m.pendingChatTitles[conversationID]
	return ok
}

func ChatStreamID(scope agent.Scope, clientID string) string {
	if strings.TrimSpace(clientID) == "" {
		clientID = "default"
	}
	return "chat:" + clientID + ":" + scope.PrincipalID
}

// ChatConversationStreamID scopes worker updates to the conversation page
// currently open in a browser. The global ChatStreamID remains available for
// list and new-chat views.
func ChatConversationStreamID(scope agent.Scope, clientID, conversationID string) string {
	return ChatStreamID(scope, clientID) + ":conversation:" + strings.TrimSpace(conversationID)
}
