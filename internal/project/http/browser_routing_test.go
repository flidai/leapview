package http

import (
	"context"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestRequestedAssetSectionSupportsFixedDashboardRoutes(t *testing.T) {
	for _, section := range []string{"details", "definition", "versions", "lineage"} {
		request := httptest.NewRequest(stdhttp.MethodGet, "/dashboards/dashboard:executive-sales/"+section, nil)
		if got := requestedAssetSection(request); got != section {
			t.Fatalf("section = %q, want %q", got, section)
		}
	}
}

func TestBoundProjectUsesActiveProjectResolver(t *testing.T) {
	want := projectgraph.ResourceID("project:active")
	h := &BrowserHandler{ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return want, nil }}
	got, err := h.boundProject(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("project ID = %q, want %q", got, want)
	}
}
