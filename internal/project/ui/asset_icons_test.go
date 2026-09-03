package ui

import (
	"strings"
	"testing"
)

func renderAssetIcon(t *testing.T, typ string) string {
	t.Helper()
	var output strings.Builder
	if err := assetTypeInlineIcon(typ).Render(&output); err != nil {
		t.Fatalf("render %s icon: %v", typ, err)
	}
	return output.String()
}

func TestAssetIconsUseDistinctSemanticModelSVGIdentity(t *testing.T) {
	data := renderAssetIcon(t, "source")
	model := renderAssetIcon(t, "model")
	semanticModel := renderAssetIcon(t, "semantic_model")

	if data == model || data == semanticModel || model == semanticModel {
		t.Fatalf("asset icons should have distinct SVG identity: source=%q model=%q semantic_model=%q", data, model, semanticModel)
	}
	if !strings.Contains(semanticModel, `<circle cx="12" cy="4.5" r="2.5"/>`) ||
		!strings.Contains(semanticModel, `<path d="M7 12h10"/>`) {
		t.Fatalf("semantic_model icon = %q, want Lucide Waypoints SVG paths", semanticModel)
	}
	if strings.Contains(semanticModel, `<path d="M21 8a2 2 0 00-1-1.73`) {
		t.Fatalf("semantic_model icon still uses Lucide Box SVG: %q", semanticModel)
	}
}
