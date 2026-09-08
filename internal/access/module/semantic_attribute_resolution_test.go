package module

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

type semanticAttributeResolutionTestRepository struct {
	access.Repository
	resolution access.SemanticAttributeResolution
	err        error
	subject    access.SubjectRef
	calls      int
}

func (r *semanticAttributeResolutionTestRepository) ResolveSemanticAttributes(_ context.Context, subject access.SubjectRef) (access.SemanticAttributeResolution, error) {
	r.subject = subject
	r.calls++
	if r.err != nil {
		return access.SemanticAttributeResolution{}, r.err
	}
	return r.resolution, nil
}

func newSemanticAttributeResolutionTestModule(t *testing.T, repository access.Repository) *Module {
	t.Helper()
	m, err := newSurface(surfaceConfig{Repository: func() (access.Repository, error) { return repository, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestResolveSemanticAttributesBindsAuthenticatedPrincipal(t *testing.T) {
	repository := &semanticAttributeResolutionTestRepository{resolution: access.SemanticAttributeResolution{
		Subject:  access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-1"},
		Subjects: []access.SubjectRef{{Kind: access.SubjectKindPrincipal, ID: "principal-1"}},
	}}
	m := newSemanticAttributeResolutionTestModule(t, repository)
	ctx := WithPrincipal(context.Background(), Principal{ID: "principal-1", Kind: access.PrincipalKindUser})
	resolution, err := m.ResolveSemanticAttributes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Subject != repository.subject || repository.subject != (access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-1"}) {
		t.Fatalf("resolved subject = %#v, want authenticated principal", resolution.Subject)
	}
	if repository.calls != 1 {
		t.Fatalf("resolution calls = %d, want one", repository.calls)
	}
}

func TestResolveSemanticAttributesRejectsUnauthenticatedBypassAndNonPrincipal(t *testing.T) {
	repository := &semanticAttributeResolutionTestRepository{}
	m := newSemanticAttributeResolutionTestModule(t, repository)
	tests := []struct {
		name string
		ctx  context.Context
		want error
	}{
		{name: "missing", ctx: context.Background(), want: ErrUnauthorized},
		{name: "development bypass", ctx: WithPrincipal(context.Background(), LocalDeveloperPrincipal()), want: ErrForbidden},
		{name: "publication", ctx: WithPrincipal(context.Background(), Principal{ID: "publication-1", Kind: access.PrincipalKindDashboardPublication}), want: ErrForbidden},
		{name: "group", ctx: WithPrincipal(context.Background(), Principal{ID: "group-1", Kind: access.PrincipalKindGroup}), want: ErrForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := m.ResolveSemanticAttributes(test.ctx)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	if repository.calls != 0 {
		t.Fatalf("rejected contexts invoked resolution %d times", repository.calls)
	}
}

func TestResolveSemanticAttributesRejectsCrossSubjectResponse(t *testing.T) {
	repository := &semanticAttributeResolutionTestRepository{resolution: access.SemanticAttributeResolution{
		Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-2"},
	}}
	m := newSemanticAttributeResolutionTestModule(t, repository)
	ctx := WithPrincipal(context.Background(), Principal{ID: "principal-1", Kind: access.PrincipalKindUser})
	if _, err := m.ResolveSemanticAttributes(ctx); err == nil {
		t.Fatal("cross-subject resolution was accepted")
	}

	repository.resolution = access.SemanticAttributeResolution{
		Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-1"},
		Subjects: []access.SubjectRef{
			{Kind: access.SubjectKindPrincipal, ID: "principal-1"},
			{Kind: access.SubjectKindPrincipal, ID: "principal-2"},
		},
	}
	if _, err := m.ResolveSemanticAttributes(ctx); err == nil {
		t.Fatal("cross-subject principal closure was accepted")
	}
}

func TestResolveSemanticAttributesFailsClosedWithoutCapability(t *testing.T) {
	m := newSemanticAttributeResolutionTestModule(t, &struct{ access.Repository }{})
	ctx := WithPrincipal(context.Background(), Principal{ID: "principal-1", Kind: access.PrincipalKindUser})
	if _, err := m.ResolveSemanticAttributes(ctx); err == nil {
		t.Fatal("repository without resolution capability was accepted")
	}
}
