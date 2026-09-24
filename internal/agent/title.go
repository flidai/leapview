package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	agentcore "github.com/flidai/leapview/pkg/agent"
)

const (
	titleReserveOutputTokens = 64
	maxGeneratedTitleRunes   = 50
)

const modelRequestPurposeTitle agentcore.ModelRequestPurpose = "title_generation"

var thinkBlockPattern = regexp.MustCompile(`(?is)<think>.*?</think>\s*`)

func (s *Service) ConversationNeedsGeneratedTitle(ctx context.Context, scope Scope, conversationID string) (bool, error) {
	conversation, err := s.repo.GetConversation(ctx, scope.PrincipalID, conversationID)
	if err != nil {
		return false, err
	}
	if !isDefaultConversationTitle(conversation.Title) {
		return false, nil
	}
	_, ok, err := firstUserPromptForTitle(ctx, s.repo, scope, conversationID)
	return ok, err
}

// GenerateConversationTitle is best-effort metadata: failures never block the chat turn.
func (s *Service) GenerateConversationTitle(ctx context.Context, scope Scope, conversationID string) (Conversation, error) {
	runtime := s.runtimeSnapshot()
	if runtime == nil || !runtime.enabled || !runtime.config.Enabled() || runtime.model == nil {
		return Conversation{}, ErrDisabled
	}
	if s.repo == nil {
		return Conversation{}, fmt.Errorf("agent store is required")
	}
	conversation, err := s.repo.GetConversation(ctx, scope.PrincipalID, conversationID)
	if err != nil {
		return Conversation{}, err
	}
	if !isDefaultConversationTitle(conversation.Title) {
		return conversation, nil
	}
	firstPrompt, ok, err := firstUserPromptForTitle(ctx, s.repo, scope, conversationID)
	if err != nil {
		return conversation, err
	}
	if !ok {
		return conversation, nil
	}

	resp, err := runtime.model.Complete(ctx, agentcore.ModelRequest{
		Purpose: modelRequestPurposeTitle,
		Messages: []agentcore.Message{
			{Role: agentcore.RoleSystem, Content: titleSystemPrompt()},
			{Role: agentcore.RoleUser, Content: "Generate a title for this conversation:\n" + firstPrompt},
		},
		Tools:  nil,
		Limits: agentcore.Limits{ReserveOutputTokens: titleReserveOutputTokens},
	}, nil)
	title := fallbackConversationTitle(firstPrompt)
	if err != nil {
		slog.DebugContext(ctx, "agent title generation model call failed", "conversation_id", conversationID, "error", err)
	} else if generated := cleanGeneratedTitle(resp.Content); generated != "" {
		title = generated
	} else {
		slog.DebugContext(ctx, "agent title generation returned empty content, using fallback", "conversation_id", conversationID)
	}
	if title == "" {
		return conversation, nil
	}

	latest, err := s.repo.GetConversation(ctx, scope.PrincipalID, conversationID)
	if err != nil {
		return conversation, err
	}
	if !isDefaultConversationTitle(latest.Title) {
		return latest, nil
	}
	updated, err := s.repo.UpdateDefaultConversationTitle(ctx, scope.PrincipalID, conversationID, title)
	if errors.Is(err, ErrNotFound) {
		return latest, nil
	}
	if err != nil {
		return latest, err
	}
	return updated, nil
}

func firstUserPromptForTitle(ctx context.Context, repo Repository, scope Scope, conversationID string) (string, bool, error) {
	messages, err := repo.ListMessages(ctx, scope.PrincipalID, conversationID)
	if err != nil {
		return "", false, err
	}
	userCount := 0
	firstPrompt := ""
	for _, message := range messages {
		if message.Role != MessageRoleUser {
			continue
		}
		userCount++
		if firstPrompt == "" {
			firstPrompt = strings.TrimSpace(message.ContentText)
		}
	}
	if userCount != 1 || firstPrompt == "" {
		return "", false, nil
	}
	return firstPrompt, true, nil
}

func isDefaultConversationTitle(title string) bool {
	return strings.TrimSpace(title) == ConversationDefaultTitle
}

func cleanGeneratedTitle(text string) string {
	text = thinkBlockPattern.ReplaceAllString(text, "")
	for _, line := range strings.Split(text, "\n") {
		title := strings.TrimSpace(line)
		if title == "" {
			continue
		}
		title = strings.TrimSpace(strings.Trim(title, "\"'`*_# \t\r\n"))
		title = strings.TrimRight(title, ".!?:;")
		title = strings.TrimSpace(title)
		if title == "" {
			continue
		}
		runes := []rune(title)
		if len(runes) > maxGeneratedTitleRunes {
			title = strings.TrimSpace(string(runes[:maxGeneratedTitleRunes]))
		}
		return title
	}
	return ""
}

func fallbackConversationTitle(prompt string) string {
	title := cleanGeneratedTitle(prompt)
	if title == "" {
		return ""
	}
	runes := []rune(title)
	runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
	return string(runes)
}

func titleSystemPrompt() string {
	return `You are a conversation title generator. Output only one title.

<task>
Generate a brief title that helps the user find this chat later.

Follow all rules in <rules>.
Use the <examples> so you know what a good title looks like.
Your output must be:
- A single line
- 50 characters or fewer
- No explanations
</task>

<rules>
- Use the same language as the user message you are summarizing
- Title must be grammatically correct and read naturally
- Never include tool names, tool calls, model names, or agent internals
- Focus on the main BI topic or question the user needs to retrieve
- Preserve exact dashboard names, metric names, model names, IDs, HTTP codes, and numbers
- Do not answer the user's question
- Do not use markdown, quotes, or trailing punctuation
- Do not say you cannot generate a title or complain about the input
- Always output something meaningful, even if the input is minimal
- If the user message is short or conversational, create a useful neutral title such as Greeting, Quick check-in, or Intro message
</rules>

<examples>
"what dashboards do we have available?" -> Available dashboards
"show me revenue by month" -> Monthly revenue
"describe executive-sales" -> Executive Sales dashboard
"what metrics are in Orders Metrics?" -> Orders Metrics overview
"why is delivery time so high?" -> Delivery time investigation
"list semantic models" -> Semantic models
"compare order count and freight value" -> Orders and freight comparison
</examples>`
}
