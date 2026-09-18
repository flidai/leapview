package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

const maxAPIGenAuthorizationBody = 1 << 20

func resolvedResource(rawID string, kind projectgraph.Kind) []access.ResourceRef {
	id, err := projectgraph.NewResourceID(strings.TrimSpace(rawID))
	if err != nil {
		return nil
	}
	resource, err := access.NewResourceRef(id, kind)
	if err != nil {
		return nil
	}
	return []access.ResourceRef{resource}
}

func pathResourceResolver(parameter string, kind projectgraph.Kind) accessmodule.APIGenResourceResolver {
	return func(r *http.Request, _ projectgraph.ResourceID) []access.ResourceRef {
		return resolvedResource(chi.URLParam(r, parameter), kind)
	}
}

// pipelineResourceResolver supports both Pipeline routes with a path target
// and commands such as createRefreshRun whose trusted target is carried in
// the generated request body. It restores the exact bytes so authorization
// cannot consume or rewrite the handler's command payload.
func pipelineResourceResolver(r *http.Request, _ projectgraph.ResourceID) []access.ResourceRef {
	if pathID := chi.URLParam(r, "pipeline"); strings.TrimSpace(pathID) != "" {
		return resolvedResource(pathID, projectgraph.KindPipeline)
	}
	if r == nil || r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAPIGenAuthorizationBody+1))
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil || len(body) > maxAPIGenAuthorizationBody {
		return nil
	}
	var payload struct {
		PipelineID string `json:"pipelineId"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	return resolvedResource(payload.PipelineID, projectgraph.KindPipeline)
}
