package http

import (
	stdhttp "net/http"
	"reflect"
	"sort"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestMountAuthenticatedRegistersCanonicalSurfacesOnly(t *testing.T) {
	router := chi.NewRouter()
	h := &BrowserHandler{Authenticate: func(next stdhttp.Handler) stdhttp.Handler { return next }}
	h.MountAuthenticated(router)

	var got []string
	if err := chi.Walk(router, func(method, route string, _ stdhttp.Handler, _ ...func(stdhttp.Handler) stdhttp.Handler) error {
		got = append(got, method+" "+route)
		return nil
	}); err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	sort.Strings(got)
	want := []string{"GET /", "GET /catalog/search", "GET /connections", "GET /connections/search", "GET /connections/{asset}/{section}", "GET /dashboards", "GET /dashboards/search", "GET /dashboards/{asset}/definition", "GET /dashboards/{asset}/details", "GET /dashboards/{asset}/lineage", "GET /dashboards/{asset}/versions", "GET /dashboards/{dashboard}/pages/{page}/components/{component}/explore", "GET /explore", "GET /explore/export", "GET /explore/saved/{exploration}", "POST /explore/command", "POST /explore/saved/command", "GET /models", "GET /models/search", "GET /models/{asset}/{section}", "POST /models/{asset}/data/command", "GET /pipelines", "GET /pipelines/{asset}/{section}", "POST /pipelines/command", "GET /search", "GET /semantic-models", "GET /semantic-models/search", "GET /semantic-models/{asset}/{section}", "POST /semantic-models/{asset}/data/command", "GET /sources", "GET /sources/search", "GET /sources/{asset}/{section}", "POST /connections/administration/configuration", "POST /connections/administration/lifecycle", "POST /dashboards/{asset}/appearance"}
	want = append(want, "GET /explore/dashboard-targets", "GET /explore/dashboard-target/{dashboard}", "POST /explore/add-to-dashboard")
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("routes = %v, want %v", got, want)
	}
	for _, legacy := range []string{"/data", "/data/{asset}/{section}", "/data/search", "/workspaces", "/workspaces/{workspace}", "/admin/workspaces"} {
		for _, route := range got {
			if route == "GET "+legacy || route == "POST "+legacy {
				t.Fatalf("legacy route %q was mounted", legacy)
			}
		}
	}
}
