package http

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestMountRoutesUsesCanonicalResourceAreas(t *testing.T) {
	router := chi.NewRouter()
	(Handler{}).MountRoutes(router, nil)
	want := map[string]bool{
		"GET /explore":                                   true,
		"POST /explore/command":                          true,
		"GET /data":                                      true,
		"POST /data/search":                              true,
		"GET /data/{asset}":                              true,
		"GET /data/{asset}/{section}":                    true,
		"GET /models/{asset}":                            true,
		"GET /models/{asset}/{section}":                  true,
		"GET /semantic-models/{asset}":                   true,
		"GET /semantic-models/{asset}/{section}":         true,
		"GET /pipelines":                                 true,
		"POST /pipelines/command":                        true,
		"GET /pipelines/{asset}":                         true,
		"GET /pipelines/{asset}/{section}":               true,
		"GET /connections":                               true,
		"POST /connections/search":                       true,
		"POST /connections/administration/configuration": true,
		"POST /connections/administration/lifecycle":     true,
		"GET /connections/{asset}":                       true,
		"GET /connections/{asset}/{section}":             true,
	}
	got := map[string]bool{}
	if err := chi.Walk(router, func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		got[method+" "+path] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for route := range want {
		if !got[route] {
			t.Errorf("missing canonical route %s", route)
		}
	}
	for route := range got {
		if route == "" || want[route] {
			continue
		}
		t.Errorf("unexpected route %s", route)
	}
}
