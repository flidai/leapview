package http

import (
	"context"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestProjectIDForRequestUsesActiveProjectResolver(t *testing.T) {
	want := projectgraph.ResourceID("project:active")
	h := Handler{ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return want, nil }}
	got, err := h.projectIDForRequest(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("project ID = %q, want %q", got, want)
	}
}

func TestProjectIDForRequestRequiresResolver(t *testing.T) {
	if _, err := (Handler{}).projectIDForRequest(t.Context()); err == nil {
		t.Fatal("expected missing active project resolver error")
	}
}
