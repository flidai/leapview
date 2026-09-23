package http

import (
	"testing"

	projectview "github.com/flidai/leapview/internal/project"
)

func TestPipelineSemanticModelTitleResolvesAuthoredAssetDisplayName(t *testing.T) {
	assets := []projectview.DevelopAssetView{
		{ID: "semantic-model:sales", Key: "sales", Title: "Revenue", Type: string(projectview.AssetTypeSemanticModel)},
		{ID: "model:sales", Key: "sales", Title: "Sales transformation", Type: string(projectview.AssetTypeModel)},
	}
	for _, reference := range []string{"semantic-model:sales", "sales"} {
		if got := pipelineSemanticModelTitle(reference, assets); got != "Revenue" {
			t.Errorf("title for %q = %q, want authored semantic-model title", reference, got)
		}
	}
	if got := pipelineSemanticModelTitle("semantic-model:missing", assets); got != "" {
		t.Fatalf("unknown semantic model title = %q, want empty fallback", got)
	}
}
