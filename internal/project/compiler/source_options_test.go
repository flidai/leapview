package compiler

import (
	"reflect"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestResolveEffectiveSourceOptionsPrecedence(t *testing.T) {
	got, err := ResolveEffectiveSourceOptions(
		semanticmodel.Source{Format: "csv", Options: map[string]any{"delimiter": ";"}},
		semanticmodel.Connection{ReaderDefaults: map[string]map[string]any{"csv": {"header": true, "delimiter": ","}}},
	)
	if err != nil {
		t.Fatalf("ResolveEffectiveSourceOptions() error = %v", err)
	}
	want := map[string]any{"header": true, "delimiter": ";", "quote": `"`, "escape": `"`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("effective options = %#v, want %#v", got, want)
	}
}
