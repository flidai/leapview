package module

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

type semanticAttributeResolutionRepository struct {
	access.Repository
	resolution access.SemanticAttributeResolution
	registry   access.SemanticAttributeRegistrySnapshot
	subject    access.SubjectRef
}

func (r *semanticAttributeResolutionRepository) ResolveSemanticAttributes(_ context.Context, subject access.SubjectRef) (access.SemanticAttributeResolution, error) {
	r.subject = subject
	return r.resolution, nil
}

func (r *semanticAttributeResolutionRepository) SemanticAttributeRegistry(context.Context) (access.SemanticAttributeRegistrySnapshot, error) {
	return r.registry, nil
}

func TestResolveSemanticAttributesUsesAuthenticatedPrincipalContext(t *testing.T) {
	repository := &semanticAttributeResolutionRepository{resolution: access.SemanticAttributeResolution{Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-1"}}}
	m, err := newSurface(surfaceConfig{Repository: func() (access.Repository, error) { return repository, nil }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithPrincipal(context.Background(), Principal{ID: "principal-1"})
	if _, err := m.ResolveSemanticAttributes(ctx); err != nil {
		t.Fatal(err)
	}
	if repository.subject != (access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-1"}) {
		t.Fatalf("resolved subject = %#v, want authenticated principal", repository.subject)
	}
}

func TestResolveSemanticAttributesRejectsCrossSubjectRepositoryResponse(t *testing.T) {
	repository := &semanticAttributeResolutionRepository{resolution: access.SemanticAttributeResolution{Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-2"}}}
	m, err := newSurface(surfaceConfig{Repository: func() (access.Repository, error) { return repository, nil }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithPrincipal(context.Background(), Principal{ID: "principal-1"})
	if _, err := m.ResolveSemanticAttributes(ctx); err == nil {
		t.Fatal("cross-subject repository response was accepted")
	}
	if repository.subject != (access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-1"}) {
		t.Fatalf("repository received subject = %#v, want authenticated principal", repository.subject)
	}
}

func TestResolveSemanticAttributesRejectsUnauthenticatedAndDevBypassContexts(t *testing.T) {
	repository := &semanticAttributeResolutionRepository{}
	m, err := newSurface(surfaceConfig{Repository: func() (access.Repository, error) { return repository, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ResolveSemanticAttributes(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("missing principal error = %v, want unauthorized", err)
	}
	if _, err := m.ResolveSemanticAttributes(WithPrincipal(context.Background(), LocalDeveloperPrincipal())); !errors.Is(err, ErrForbidden) {
		t.Fatalf("development bypass error = %v, want forbidden", err)
	}
	if repository.subject != (access.SubjectRef{}) {
		t.Fatalf("repository received subject for rejected context: %#v", repository.subject)
	}
}

func TestSemanticAttributeRegistryGetterUsesRepositoryReadPort(t *testing.T) {
	want := access.SemanticAttributeRegistrySnapshot{State: access.SemanticAttributeRegistryState{Profile: "semantic-value/v1", Revision: 4}}
	repository := &semanticAttributeResolutionRepository{registry: want}
	m, err := newSurface(surfaceConfig{Repository: func() (access.Repository, error) { return repository, nil }})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.SemanticAttributeRegistry(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("registry = %#v, want %#v", got, want)
	}
}
