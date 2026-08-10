package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/example/apigen-example/internal/api/gen"
)

// NewRouter builds the example HTTP router with strict generated routes.
func NewRouter() http.Handler {
	router := chi.NewRouter()
	router.Get("/openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		spec, err := gen.GetEmbeddedOpenAPISpec()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(spec)
	})
	server := NewServer()
	gen.RegisterAPIGenStrictRoutes(router, server, transportErrorResponder{})
	return router
}

type transportErrorResponder struct{}

func (transportErrorResponder) RespondTransportError(_ context.Context, w http.ResponseWriter, _ *http.Request, failure gen.GenTransportError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(failure.StatusCode)
	_ = json.NewEncoder(w).Encode(gen.Error{Code: failure.Code, Message: failure.PublicDetail})
}
