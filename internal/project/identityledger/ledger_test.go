package identityledger

import (
	"errors"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestNormalizeResourcesRejectsCandidateWideCollision(t *testing.T) {
	_, err := NormalizeResources([]Resource{
		{AuthoredID: "orders", Kind: projectgraph.KindSource},
		{AuthoredID: "orders", Kind: projectgraph.KindModel},
	})
	if !errors.Is(err, ErrDuplicateAuthoredID) {
		t.Fatalf("error = %v, want duplicate authored ID", err)
	}
}

func TestNormalizeResourcesSortsAndAdmitsExactlySixKinds(t *testing.T) {
	resources, err := NormalizeResources([]Resource{
		{AuthoredID: "z", Kind: projectgraph.KindDashboard},
		{AuthoredID: "a", Kind: projectgraph.KindConnection},
		{AuthoredID: "b", Kind: projectgraph.KindSource},
		{AuthoredID: "c", Kind: projectgraph.KindModel},
		{AuthoredID: "d", Kind: projectgraph.KindSemanticModel},
		{AuthoredID: "e", Kind: projectgraph.KindPipeline},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resources[0].AuthoredID != "a" || resources[len(resources)-1].AuthoredID != "z" {
		t.Fatalf("resources not sorted: %#v", resources)
	}
	if _, err := NormalizeResources([]Resource{{AuthoredID: "project", Kind: projectgraph.KindProject}}); err == nil {
		t.Fatal("public Project kind unexpectedly admitted")
	}
}
