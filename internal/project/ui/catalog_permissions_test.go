package ui

import (
	"html"
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
			node := CatalogPageForCatalogsWithOptions([]catalog.Catalog{projectCatalog}, CatalogListOptions{
				CanCreateDraft:                test.canCreate,
				CreateDashboardModels:         []CatalogDashboardModelOption{{ID: "semantic:sales", Title: "Sales"}},
				CreateDashboardIdempotencyKey: "request-1",
			}, "csrf-1", nil)
			var rendered strings.Builder
			if err := node.Render(&rendered); err != nil {
				t.Fatal(err)
			}
			body := html.UnescapeString(rendered.String())
			got := strings.Contains(body, `create-draft-href="/dashboards/new"`)
			if got != test.wantLink {
				t.Fatalf("create draft link present=%v, want %v: %s", got, test.wantLink, body)
			}
			for _, attribute := range []string{`create-draft-models="[{"id":"semantic:sales","title":"Sales"}]"`, `create-draft-csrf-token="csrf-1"`, `create-draft-idempotency-key="request-1"`} {
				present := strings.Contains(body, attribute)
				if present != test.wantLink {
					t.Fatalf("create draft attribute %q present=%v, want %v: %s", attribute, present, test.wantLink, body)
				}
			}
		})
	}
}
