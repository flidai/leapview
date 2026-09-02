package application

import (
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/authoring"
)

func TestApplyExactSourceEditsMatchesOneOriginalRangePerEdit(t *testing.T) {
	source := "title: Sales\npages:\n  - id: overview\n    title: Overview\n"
	got, err := applyExactSourceEdits(source, []SourceEdit{
		{OldText: "title: Sales", NewText: "title: Revenue"},
		{OldText: "    title: Overview", NewText: "    title: Executive overview"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "title: Revenue\npages:\n  - id: overview\n    title: Executive overview\n"
	if got != want {
		t.Fatalf("edited source = %q, want %q", got, want)
	}
}

func TestApplyExactSourceEditsRejectsAmbiguousMissingOverlappingAndNoopEdits(t *testing.T) {
	source := "one one two"
	for name, edits := range map[string][]SourceEdit{
		"empty":     {},
		"ambiguous": {{OldText: "one", NewText: "first"}},
		"missing":   {{OldText: "three", NewText: "third"}},
		"overlap":   {{OldText: "one one", NewText: "first"}, {OldText: "one two", NewText: "second"}},
		"noop":      {{OldText: "two", NewText: "two"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := applyExactSourceEdits(source, edits); !errors.Is(err, authoring.ErrInvalidPayload) {
				t.Fatalf("error = %v, want invalid payload", err)
			}
		})
	}
}
