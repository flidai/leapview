package catalog

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestSemanticCatalogDiscoveryAndDirectReferenceShareGate(t *testing.T) {
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "semantic_sales", Kind: projectgraph.KindSemanticModel, Name: "sales"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project_demo", "development", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	lease := testLease{snapshot: snapshot}
	for _, test := range []struct {
		name       string
		visibility SemanticModelVisibility
		wantCount  int
		wantError  bool
	}{
		{name: "missing metadata", wantError: true},
		{name: "protected denied", visibility: func(context.Context, Lease, string, projectgraph.ResourceID) (bool, error) { return false, nil }},
		{name: "allowed", wantCount: 1, visibility: func(_ context.Context, got Lease, principal string, id projectgraph.ResourceID) (bool, error) {
			if got.Identity() != identity || id != "semantic_sales" || principal != "dev" {
				t.Fatal("visibility did not use exact catalog lease")
			}
			return true, nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, err := NewService(testLeases{lease: lease}, testSubjects{}, WithSemanticModelVisibility(test.visibility))
			if err != nil {
				t.Fatal(err)
			}
			page, err := service.Search(t.Context(), SearchRequest{PrincipalID: "dev", DevAuthBypass: true, Query: "sales"})
			if (err != nil) != test.wantError || len(page.Items) != test.wantCount {
				t.Fatalf("search = %#v, %v", page, err)
			}
			page, err = service.List(t.Context(), ListRequest{PrincipalID: "dev", DevAuthBypass: true})
			if (err != nil) != test.wantError || len(page.Items) != test.wantCount {
				t.Fatalf("list = %#v, %v", page, err)
			}
			_, err = service.Resolve(t.Context(), "dev", Ref{ID: "semantic_sales", Kind: projectgraph.KindSemanticModel}, access.CapabilityResourceRead, true)
			if test.wantError && !errors.Is(err, ErrUnavailable) {
				t.Fatalf("resolve = %v", err)
			}
			if !test.wantError && test.wantCount == 0 && !errors.Is(err, ErrNotFound) {
				t.Fatalf("denied resolve = %v", err)
			}
			if test.wantCount == 1 && err != nil {
				t.Fatal(err)
			}
		})
	}
}
