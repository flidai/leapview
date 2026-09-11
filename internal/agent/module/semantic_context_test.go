package module

import (
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"testing"
)

func TestSemanticContextMissingAuthorityFailsClosed(t *testing.T) {
	model := &semanticmodel.Model{}
	if err := authorizeSemanticExploration(t.Context(), nil, "sales", "orders", model, nil); err != nil {
		t.Fatal(err)
	}
	model.AccessPolicy.Datasets = map[string]semanticmodel.SemanticDatasetAccessSpec{"orders": {RequiredAccessGrants: []string{"staff"}}}
	if err := authorizeSemanticExploration(t.Context(), nil, "sales", "orders", model, nil); err == nil {
		t.Fatal("protected agent context accepted without authority")
	}
	if err := authorizeSemanticExploration(t.Context(), nil, "sales", "orders", nil, nil); err == nil {
		t.Fatal("unknown model accepted as public")
	}
}
