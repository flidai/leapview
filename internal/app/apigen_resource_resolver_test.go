package app

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

func TestPipelineResourceResolverReadsAndRestoresCommandBody(t *testing.T) {
	body := `{"pipelineId":"pipeline:orders"}`
	request := httptest.NewRequest("POST", "/api/v1/projects/project:test/refresh-runs", strings.NewReader(body))
	resources := pipelineResourceResolver(request, projectgraph.ResourceID("project:test"))
	if len(resources) != 1 || resources[0].ID() != "pipeline:orders" || resources[0].Kind() != projectgraph.KindPipeline {
		t.Fatalf("resolved resources = %#v", resources)
	}
	restored, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != body {
		t.Fatalf("restored body = %q, want %q", restored, body)
	}
}

func TestPipelineResourceResolverPrefersPathTarget(t *testing.T) {
	request := httptest.NewRequest("POST", "/pipelines/pipeline:path", strings.NewReader(`{"pipelineId":"pipeline:body"}`))
	route := chi.NewRouteContext()
	route.URLParams.Add("pipeline", "pipeline:path")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	resources := pipelineResourceResolver(request, projectgraph.ResourceID("project:test"))
	if len(resources) != 1 || resources[0].ID() != "pipeline:path" {
		t.Fatalf("resolved resources = %#v", resources)
	}
}

func TestPipelineResourceResolverRejectsMalformedOrOversizedBody(t *testing.T) {
	for _, body := range []string{`{"pipelineId":`, strings.Repeat("x", maxAPIGenAuthorizationBody+1)} {
		request := httptest.NewRequest("POST", "/api/v1/projects/project:test/refresh-runs", strings.NewReader(body))
		if resources := pipelineResourceResolver(request, projectgraph.ResourceID("project:test")); len(resources) != 0 {
			t.Fatalf("body length %d resolved resources %#v", len(body), resources)
		}
	}
}
