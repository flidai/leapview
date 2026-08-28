package runtimefactory

import (
	"context"
	"testing"

	manageddataresolver "github.com/flidai/leapview/internal/manageddata/resolver"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type identityManagedDataSource struct {
	identity projectgraph.ServingIdentity
}

func (s *identityManagedDataSource) ResolveManagedData(_ context.Context, identity projectgraph.ServingIdentity) (manageddataresolver.Resolution, error) {
	s.identity = identity
	return manageddataresolver.Resolution{
		RevisionID: "revision",
		Roots:      map[projectgraph.ResourceID]string{"connection:orders": "/managed/orders"},
		Revisions:  map[projectgraph.ResourceID]string{"connection:orders": "sha256:revision"},
	}, nil
}

func TestManagedDataResolverUsesGenerationIdentityProject(t *testing.T) {
	source := &identityManagedDataSource{}
	resolver := NewManagedDataResolver(source)
	identity := projectgraph.ServingIdentity{ProjectID: "project:orders", Environment: "dev", GenerationID: "generation:one"}
	resolution, err := resolver.ResolveManagedDataForIdentity(t.Context(), identity)
	if err != nil {
		t.Fatal(err)
	}
	if source.identity != identity {
		t.Fatalf("managed-data identity = %+v, want %+v", source.identity, identity)
	}
	if resolution.Roots["connection:orders"] != "/managed/orders" {
		t.Fatalf("managed-data roots = %#v", resolution.Roots)
	}
	if resolution.Revisions["connection:orders"] != "sha256:revision" {
		t.Fatalf("managed-data revisions = %#v", resolution.Revisions)
	}
}
