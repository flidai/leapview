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
	} {
		if !strings.Contains(DefaultSystemPrompt, want) {
			t.Fatalf("DefaultSystemPrompt does not contain %q", want)
		}
	}
}
