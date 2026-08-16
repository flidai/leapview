package app

import (
	"strings"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestValidTusTransportIDRequiresCanonicalOpaqueToken(t *testing.T) {
	valid := "tus_" + strings.Repeat("a", 64)
	for _, value := range []string{valid, "tus_" + strings.Repeat("A", 64), " tus_" + strings.Repeat("a", 64), "tus_" + strings.Repeat("a", 63)} {
		if got := validTusTransportID(value); got != (value == valid) {
			t.Errorf("validTusTransportID(%q) = %t, want %t", value, got, value == valid)
		}
	}
}

func TestActiveProjectResourceIsExactCanonicalReference(t *testing.T) {
	resources := activeProjectResource(nil, projectgraph.ResourceID("project_demo"))
	if len(resources) != 1 {
		t.Fatalf("resource count = %d, want 1", len(resources))
	}
	if resources[0].ID() != "project_demo" || resources[0].Kind() != projectgraph.KindProject {
		t.Fatalf("resource = %#v, want project_demo/project", resources[0])
	}
	if err := resources[0].Validate(); err != nil {
		t.Fatalf("resource is not canonical: %v", err)
	}
}
