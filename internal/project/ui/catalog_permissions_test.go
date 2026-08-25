package ui

import (
	"strings"
	"testing"

	catalog "github.com/flidai/leapview/internal/project/navigation"
)

func TestCatalogCreateDraftAffordanceFollowsPermission(t *testing.T) {
	projectCatalog := catalog.Catalog{Project: catalog.Project{ID: "sales", Title: "Sales"}}
	for _, test := range []struct {
		name      string
		canCreate bool
		wantLink  bool
	}{
		{name: "editor", canCreate: true, wantLink: true},
		{name: "read only", canCreate: false, wantLink: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := CatalogPageForCatalogsWithOptions([]catalog.Catalog{projectCatalog}, CatalogListOptions{CanCreateDraft: test.canCreate}, "", nil)
			var rendered strings.Builder
			if err := node.Render(&rendered); err != nil {
				t.Fatal(err)
			}
			got := strings.Contains(rendered.String(), `create-draft-href="/dashboards/new"`)
			if got != test.wantLink {
				t.Fatalf("create draft link present=%v, want %v: %s", got, test.wantLink, rendered.String())
			}
		})
	}
}
