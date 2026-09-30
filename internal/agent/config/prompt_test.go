package config

import (
	"strings"
	"testing"
)

func TestDefaultSystemPromptUsesCompleteDataBeforeExploration(t *testing.T) {
	for _, want := range []string{
		"catalog_get includes a bounded active definition in details.metadata.definition",
		"When query results and available definitions cover the user's requested metrics, dates, and comparisons",
		"answer without exporting a dashboard or searching documentation",
		"search documentation only when that definition is absent",
		"When a user asks to build a dashboard in main chat, use compose_chat_dashboard",
		"use it with its ordered external_leapview_dashboard_visual_NN source items as the latest successful draft baseline",
		"Set a clear human-readable visual.title for each chart",
		"The live draft remains a conversation artifact until the user explicitly chooses Save",
		"Dashboard Builder tools can edit only the exact dashboardId and draftId supplied in dashboard_builder context",
	} {
		if !strings.Contains(DefaultSystemPrompt, want) {
			t.Fatalf("DefaultSystemPrompt does not contain %q", want)
		}
	}
}

func TestDefaultSystemPromptExplainsVisualValues(t *testing.T) {
	for _, want := range []string{
		"separate Markdown bullet items",
		"include the exact plotted values in a Markdown table",
		"Preserve the returned row order",
		"Use dataCompleteness to distinguish complete, truncated, limit_reached, empty, and unavailable",
		"State clearly when overall completeness is limit_reached",
		"do not invent values or calculations",
	} {
		if !strings.Contains(DefaultSystemPrompt, want) {
			t.Fatalf("DefaultSystemPrompt does not contain %q", want)
		}
	}
	if strings.Contains(DefaultSystemPrompt, "End with at most one short sentence") {
		t.Fatal("DefaultSystemPrompt still suppresses the visual explanation")
	}
}
