package ui

import (
	"testing"
)

func TestModelTableSQL_FallbackExtractionPath(t *testing.T) {
	// 1. Verify old structure (Model.SQL or Definition.SQL)
	oldMeta := map[string]any{
		"Definition": map[string]any{
			"SQL": "SELECT * FROM old_table",
		},
	}
	if got := modelTableSQL(oldMeta); got != "SELECT * FROM old_table" {
		t.Errorf("expected 'SELECT * FROM old_table', got %q", got)
	}

	// 2. Verify new ADR-0010 AuthoredModel structure
	newMeta := map[string]any{
		"AuthoredModel": map[string]any{
			"Query": map[string]any{
				"Code": "SELECT * FROM new_table",
			},
		},
	}
	if got := modelTableSQL(newMeta); got != "SELECT * FROM new_table" {
		t.Errorf("expected 'SELECT * FROM new_table', got %q", got)
	}

	// 3. Verify empty struct
	if got := modelTableSQL(map[string]any{}); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}
