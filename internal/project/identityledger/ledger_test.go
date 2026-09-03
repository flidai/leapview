package identityledger

import (
	"errors"
	"reflect"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestCandidateFromGraphIncludesAuthoredKindsAndExcludesProject(t *testing.T) {
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project_demo", Kind: projectgraph.KindProject, Name: "demo"},
		{ID: "connection_warehouse", Kind: projectgraph.KindConnection, Name: "warehouse"},
		{ID: "source_orders", Kind: projectgraph.KindSource, Name: "source-orders"},
		{ID: "model_orders", Kind: projectgraph.KindModel, Name: "model-orders"},
		{ID: "semantic_orders", Kind: projectgraph.KindSemanticModel, Name: "semantic-orders"},
		{ID: "pipeline_orders", Kind: projectgraph.KindPipeline, Name: "pipeline-orders"},
		{ID: "dashboard_orders", Kind: projectgraph.KindDashboard, Name: "dashboard-orders"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	candidate, err := CandidateFromGraph("instance-1", "bundle-1", "bundle-0", "actor-1", graph)
	if err != nil {
		t.Fatal(err)
	}
	want := []Resource{
		{AuthoredID: "connection_warehouse", Kind: projectgraph.KindConnection},
		{AuthoredID: "dashboard_orders", Kind: projectgraph.KindDashboard},
		{AuthoredID: "model_orders", Kind: projectgraph.KindModel},
		{AuthoredID: "pipeline_orders", Kind: projectgraph.KindPipeline},
		{AuthoredID: "semantic_orders", Kind: projectgraph.KindSemanticModel},
		{AuthoredID: "source_orders", Kind: projectgraph.KindSource},
	}
	if !reflect.DeepEqual(candidate.Resources, want) {
		t.Fatalf("candidate resources = %#v, want %#v", candidate.Resources, want)
	}
}

func TestCandidateFromGraphRejectsZeroGraph(t *testing.T) {
	_, err := CandidateFromGraph("instance-1", "bundle-1", "", "actor-1", projectgraph.ProjectGraph{})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v, want invalid identity ledger input", err)
	}
}

func TestCandidateFromGraphValidatesCandidateInputs(t *testing.T) {
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project_demo", Kind: projectgraph.KindProject, Name: "demo"},
		{ID: "source_orders", Kind: projectgraph.KindSource, Name: "orders"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]struct {
		instanceID string
		bundleID   string
		actorID    string
	}{
		"missing instance": {bundleID: "bundle-1", actorID: "actor-1"},
		"missing bundle":   {instanceID: "instance-1", actorID: "actor-1"},
		"missing actor":    {instanceID: "instance-1", bundleID: "bundle-1"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := CandidateFromGraph(input.instanceID, input.bundleID, "", input.actorID, graph)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error = %v, want invalid identity ledger input", err)
			}
		})
	}
}

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
