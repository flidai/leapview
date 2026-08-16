package app

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

func testRevalidationArtifact(t *testing.T, metadata projectgraph.Metadata, provenance projectgraph.Provenance, model *semanticmodel.Model) projectbundle.CompiledProjectArtifact {
	t.Helper()
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project", Kind: projectgraph.KindProject, Name: "project"},
		{ID: "semantic_model:orders", Kind: projectgraph.KindSemanticModel, Name: "orders", Metadata: metadata, Provenance: provenance},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return projectbundle.CompiledProjectArtifact{
		ProjectID: graph.ProjectID(), Graph: graph,
		Manifest: projectmanifest.Project{ID: "project", SemanticModels: map[string]*semanticmodel.Model{"semantic_model:orders": model}},
	}
}

func TestDiffCompiledArtifactsDetectsSemanticDefinitionChange(t *testing.T) {
	prior := testRevalidationArtifact(t, projectgraph.Metadata{}, projectgraph.Provenance{}, &semanticmodel.Model{Name: "orders"})
	currentModel := &semanticmodel.Model{Name: "orders", Dimensions: map[string]semanticmodel.SemanticDimension{"order_date": {Type: "date"}}}
	current := testRevalidationArtifact(t, projectgraph.Metadata{}, projectgraph.Provenance{}, currentModel)
	if got := diffCompiledArtifacts(prior, current); len(got) != 1 || got[0] != "semantic_model:orders" {
		t.Fatalf("semantic definition diff = %v, want semantic_model:orders", got)
	}
}

func TestDiffCompiledArtifactsIgnoresGraphMetadataOnlyChange(t *testing.T) {
	prior := testRevalidationArtifact(t, projectgraph.Metadata{}, projectgraph.Provenance{}, &semanticmodel.Model{Name: "orders"})
	current := testRevalidationArtifact(t,
		projectgraph.Metadata{Domain: "finance", DisplayName: "Orders dashboard", Owner: "owner-2", Tags: []string{"new"}},
		projectgraph.Provenance{Path: "reports/orders.yaml", Origin: "imported"},
		&semanticmodel.Model{Name: "orders"},
	)
	if got := diffCompiledArtifacts(prior, current); len(got) != 0 {
		t.Fatalf("metadata-only diff = %v, want no changed resources", got)
	}
}
