package app

import (
	"context"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestRuntimeServicesResolveProjectIDTracksActivation(t *testing.T) {
	var active projectgraph.ResourceID
	runtime := &runtimeServices{
		projectID: projectgraph.ResourceID("project:startup"),
		projectIDResolver: func(context.Context) (projectgraph.ResourceID, error) {
			return active, nil
		},
	}
	if _, err := runtime.resolveProjectID(t.Context()); err == nil {
		t.Fatal("unbound runtime unexpectedly resolved a project")
	}

	active = projectgraph.ResourceID("project:activated")
	got, err := runtime.resolveProjectID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != active {
		t.Fatalf("resolved project = %q, want %q", got, active)
	}
}
