package ui

import "testing"

func TestCatalogListPatchIncludesSearchQuery(t *testing.T) {
	patch := CatalogListPatchForCatalogs(nil, CatalogListOptions{Query: "operations"})
	page, ok := patch["page"].(map[string]any)
	if !ok {
		t.Fatalf("page patch = %T", patch["page"])
	}
	query, ok := page["listQuery"].(*string)
	if !ok || *query != "operations" {
		t.Fatalf("list query = %v, want operations", page["listQuery"])
	}
}
