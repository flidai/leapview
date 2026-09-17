package app

import (
	"net/http"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

func pathResourceResolver(parameter string, kind projectgraph.Kind) accessmodule.APIGenResourceResolver {
	return func(r *http.Request, _ projectgraph.ResourceID) []access.ResourceRef {
		id, err := projectgraph.NewResourceID(chi.URLParam(r, parameter))
		if err != nil {
			return nil
		}
		resource, err := access.NewResourceRef(id, kind)
		if err != nil {
			return nil
		}
		return []access.ResourceRef{resource}
	}
}
