package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	dashboardmodel "github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/service"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var forkTestTime = time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

func newForkService(t *testing.T, repository *fakeRepository, auth *fakeAuthorizer, compiler *fakeCompiler, dashboardID authoring.DashboardID) *service.Service {
	t.Helper()
	draftID := authoring.DraftID("fork-draft")
	revisionID := authoring.RevisionID("fork-revision")
	svc, err := service.NewService(service.Options{
		Repository: repository, Authorizer: auth, Compiler: compiler,
		Now:            func() time.Time { return forkTestTime },
		NewDashboardID: func() (authoring.DashboardID, error) { return dashboardID, nil },
		NewDraftID:     func() (authoring.DraftID, error) { return draftID, nil },
		NewRevisionID:  func() (authoring.RevisionID, error) { return revisionID, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func publishedSource(t *testing.T, repository *fakeRepository, withNewerDraft bool) (authoring.DashboardLifecycle, authoring.Revision) {
	t.Helper()
	page := dashboardmodel.Page{ID: "overview", Title: "Overview", Canvas: dashboardmodel.PageCanvas{Width: 1366, Height: 940}, Grid: dashboardmodel.PageGrid{Columns: 12, RowHeight: 48, Gap: 16}}.WithDefaults()
	document := authoring.Dashboard{ID: "source", Title: "Published Orders", SemanticModel: "sales", Visuals: map[string]authoring.AuthoringVisualization{}, Pages: []dashboardmodel.Page{page}}
	provenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "publisher"}
	revision, err := authoring.NewRevision("published-revision", "source", 3, forkTestTime, document, provenance)
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := projectgraph.NewServingIdentity("project", "production", "serving-state")
	compiled, err := authoring.NewCompiledRevision("project", "source", revision.Token(), dashboarddefinition.Definition{ID: "source", SemanticModel: "sales", Title: document.Title}, identity, forkTestTime)
	if err != nil {
		t.Fatal(err)
	}
	published := authoring.Published{Revision: revision.Token(), Compilation: compiled.Token(), PublishedAt: forkTestTime, Provenance: provenance}
	lifecycle := authoring.DashboardLifecycle{ProjectID: "project", ID: "source", OwnerPrincipalID: "owner", Slug: "published-orders", Title: document.Title, SemanticModel: "sales", Visibility: authoring.VisibilityOrganization, Status: authoring.LifecycleStatusPublished, Published: &published}
	if withNewerDraft {
		newerDocument := document
		newerDocument.Title = "Newer Draft"
		newer, err := authoring.NewRevision("newer-revision", "source", 4, forkTestTime.Add(time.Minute), newerDocument, provenance)
		if err != nil {
			t.Fatal(err)
		}
		repository.revisions[newer.ID] = newer
		lifecycle.Draft = &authoring.Draft{ID: "source-draft", DashboardID: lifecycle.ID, Revision: newer.Token(), Provenance: provenance}
	}
	if err := lifecycle.Validate(); err != nil {
		t.Fatal(err)
	}
	repository.lifecycle = lifecycle
	repository.revisions[revision.ID] = revision
	return lifecycle, revision
}

func TestForkPublishedRevisionIntoPrivateDraft(t *testing.T) {
	repository, auth, compiler := newFakeRepository(), &fakeAuthorizer{}, &fakeCompiler{}
	source, published := publishedSource(t, repository, true)
	svc := newForkService(t, repository, auth, compiler, "forked")
	result, err := svc.Fork(t.Context(), service.ForkRequest{ProjectID: "project", SourceDashboardID: source.ID, ActorID: "actor", Title: "Forked Orders", Slug: "forked-orders", Origin: authoring.OriginAgent, IdempotencyKey: "fork-published"})
	if err != nil {
		t.Fatal(err)
	}
	if repository.createCalls != 1 || compiler.calls != 0 {
		t.Fatalf("createCalls=%d compilerCalls=%d", repository.createCalls, compiler.calls)
	}
	if len(auth.requests) != 2 || auth.requests[0].Action != authoring.AuthorizationActionView || auth.requests[1].Action != authoring.AuthorizationActionEdit {
		t.Fatalf("authorization order = %#v", auth.requests)
	}
	if auth.requests[0].DashboardID != source.ID || auth.requests[1].DashboardID != result.Lifecycle.ID {
		t.Fatalf("authorization dashboards = %#v", auth.requests)
	}
	if result.Lifecycle.ID == source.ID || result.Lifecycle.Visibility != authoring.VisibilityPrivate || result.Lifecycle.OwnerPrincipalID != "actor" || result.Lifecycle.SemanticModel != source.SemanticModel {
		t.Fatalf("fork lifecycle = %#v", result.Lifecycle)
	}
	if result.Lifecycle.Draft == nil || result.Lifecycle.Draft.ID == "source-draft" || result.Revision.RevisionID == published.ID {
		t.Fatalf("fork identifiers = %#v", result)
	}
	forked := repository.revisions[result.Revision.RevisionID]
	if forked.Document.Title != "Forked Orders" || forked.Document.ID != result.Lifecycle.ID || forked.Document.SemanticModel != published.Document.SemanticModel {
		t.Fatalf("fork document = %#v", forked.Document)
	}
	if forked.Provenance.Origin != authoring.OriginAgent || forked.Provenance.ForkedFrom == nil || forked.Provenance.ForkedFrom.Kind != authoring.ForkSourceInstance || forked.Provenance.ForkedFrom.Instance == nil || forked.Provenance.ForkedFrom.Instance.SourceProjectID != "project" || forked.Provenance.ForkedFrom.Instance.SourceDashboardID != source.ID || forked.Provenance.ForkedFrom.Instance.SourceRevision != published.Token() {
		t.Fatalf("fork provenance = %#v", forked.Provenance)
	}
	if forked.Document.Title == "Newer Draft" {
		t.Fatal("fork copied newer draft instead of exact published revision")
	}
}

func TestForkDeepCopiesDocumentAndProvenance(t *testing.T) {
	repository, auth, compiler := newFakeRepository(), &fakeAuthorizer{}, &fakeCompiler{}
	source, _ := publishedSource(t, repository, false)
	svc := newForkService(t, repository, auth, compiler, "forked")
	result, err := svc.Fork(t.Context(), service.ForkRequest{ProjectID: "project", SourceDashboardID: source.ID, ActorID: "actor", Source: &authoring.SourceMetadata{Metadata: map[string]string{"channel": "ui"}}, IdempotencyKey: "fork-deep-copy"})
	if err != nil {
		t.Fatal(err)
	}
	forked := repository.revisions[result.Revision.RevisionID]
	forked.Document.Pages[0].Title = "mutated"
	forked.Provenance.Source.Metadata["channel"] = "mutated"
	forked.Provenance.ForkedFrom.Instance.SourceRevision.Number = 99
	if repository.revisions["published-revision"].Document.Pages[0].Title == "mutated" {
		t.Fatal("fork document aliases source document")
	}
	if repository.revisions["published-revision"].Provenance.Source != nil {
		t.Fatal("source revision unexpectedly received fork source metadata")
	}
	if result.Lifecycle.Draft.Provenance.ForkedFrom.Instance.SourceRevision.Number == 99 {
		t.Fatal("mutating stored revision provenance changed lifecycle provenance")
	}
	if forked.Provenance.Digest() == sourcePublishedDigest(t, repository) {
		t.Fatal("fork provenance digest did not differ from source")
	}
}

func sourcePublishedDigest(t *testing.T, repository *fakeRepository) string {
	t.Helper()
	return repository.revisions["published-revision"].Provenance.Digest()
}

func TestForkRejectsIncompleteSourcesAndPreservesErrors(t *testing.T) {
	tests := []struct {
		name  string
		setup func(authoring.DashboardLifecycle, *fakeRepository)
		want  error
	}{
		{name: "missing publication", setup: func(l authoring.DashboardLifecycle, r *fakeRepository) {
			l.Status = authoring.LifecycleStatusDraft
			l.Published = nil
			r.lifecycle = l
		}, want: authoring.ErrInvalidAuthoring},
		{name: "archived", setup: func(l authoring.DashboardLifecycle, r *fakeRepository) {
			l.Status = authoring.LifecycleStatusArchived
			r.lifecycle = l
		}, want: authoring.ErrConflict},
		{name: "missing authored source", setup: func(l authoring.DashboardLifecycle, r *fakeRepository) { delete(r.revisions, "published-revision") }, want: authoring.ErrSourceUnavailable},
		{name: "stale published token", setup: func(l authoring.DashboardLifecycle, r *fakeRepository) {
			original := r.revisions["published-revision"]
			document := original.Document
			document.Title = "Retained but different"
			replacement, err := authoring.NewRevision(original.ID, original.DashboardID, original.Number+1, original.CreatedAt.Add(time.Minute), document, original.Provenance)
			if err != nil {
				t.Fatal(err)
			}
			r.revisions[original.ID] = replacement
		}, want: authoring.ErrStaleRevision},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repository, auth, compiler := newFakeRepository(), &fakeAuthorizer{}, &fakeCompiler{}
			lifecycle, _ := publishedSource(t, repository, false)
			tt.setup(lifecycle, repository)
			svc := newForkService(t, repository, auth, compiler, "forked")
			_, err := svc.Fork(t.Context(), service.ForkRequest{ProjectID: "project", SourceDashboardID: "source", ActorID: "actor", IdempotencyKey: "fork-" + tt.name})
			if !errors.Is(err, tt.want) || repository.createCalls != 0 || compiler.calls != 0 {
				t.Fatalf("err=%v createCalls=%d compilerCalls=%d", err, repository.createCalls, compiler.calls)
			}
		})
	}
}

func TestForkAuthorizationAndInputFailures(t *testing.T) {
	t.Run("source view denial", func(t *testing.T) {
		repository, auth, compiler := newFakeRepository(), &fakeAuthorizer{err: errors.New("view denied"), denyAction: authoring.AuthorizationActionView}, &fakeCompiler{}
		lifecycle, _ := publishedSource(t, repository, false)
		svc := newForkService(t, repository, auth, compiler, "forked")
		_, err := svc.Fork(t.Context(), service.ForkRequest{ProjectID: "project", SourceDashboardID: lifecycle.ID, ActorID: "actor", IdempotencyKey: "fork-source-denial"})
		if err == nil || repository.createCalls != 0 || len(auth.requests) != 1 {
			t.Fatalf("err=%v auth=%#v", err, auth.requests)
		}
	})
	t.Run("target edit denial", func(t *testing.T) {
		repository, auth, compiler := newFakeRepository(), &fakeAuthorizer{err: errors.New("edit denied"), denyAction: authoring.AuthorizationActionEdit}, &fakeCompiler{}
		lifecycle, _ := publishedSource(t, repository, false)
		svc := newForkService(t, repository, auth, compiler, "forked")
		_, err := svc.Fork(t.Context(), service.ForkRequest{ProjectID: "project", SourceDashboardID: lifecycle.ID, ActorID: "actor", IdempotencyKey: "fork-target-denial"})
		if err == nil || repository.createCalls != 0 || len(auth.requests) != 2 {
			t.Fatalf("err=%v auth=%#v", err, auth.requests)
		}
	})
	t.Run("target id collision", func(t *testing.T) {
		repository, auth, compiler := newFakeRepository(), &fakeAuthorizer{}, &fakeCompiler{}
		lifecycle, _ := publishedSource(t, repository, false)
		svc := newForkService(t, repository, auth, compiler, lifecycle.ID)
		_, err := svc.Fork(t.Context(), service.ForkRequest{ProjectID: "project", SourceDashboardID: lifecycle.ID, ActorID: "actor", IdempotencyKey: "fork-collision"})
		if !errors.Is(err, authoring.ErrInvalidAuthoring) || len(auth.requests) != 1 || repository.createCalls != 0 {
			t.Fatalf("err=%v auth=%#v createCalls=%d", err, auth.requests, repository.createCalls)
		}
	})
	t.Run("invalid origin and slug", func(t *testing.T) {
		repository, auth, compiler := newFakeRepository(), &fakeAuthorizer{}, &fakeCompiler{}
		lifecycle, _ := publishedSource(t, repository, false)
		svc := newForkService(t, repository, auth, compiler, "forked")
		_, err := svc.Fork(t.Context(), service.ForkRequest{ProjectID: "project", SourceDashboardID: lifecycle.ID, ActorID: "actor", Origin: authoring.Origin("bad"), Slug: "Not valid", IdempotencyKey: "fork-invalid"})
		if !errors.Is(err, authoring.ErrInvalidAuthoring) || repository.createCalls != 0 {
			t.Fatalf("err=%v createCalls=%d", err, repository.createCalls)
		}
	})
}

func TestForkInvalidClock(t *testing.T) {
	repository, auth, compiler := newFakeRepository(), &fakeAuthorizer{}, &fakeCompiler{}
	lifecycle, _ := publishedSource(t, repository, false)
	svc, err := service.NewService(service.Options{
		Repository: repository, Authorizer: auth, Compiler: compiler, Now: func() time.Time { return time.Time{} },
		NewDashboardID: func() (authoring.DashboardID, error) { return "forked", nil }, NewDraftID: func() (authoring.DraftID, error) { return "fork-draft", nil }, NewRevisionID: func() (authoring.RevisionID, error) { return "fork-revision", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Fork(context.Background(), service.ForkRequest{ProjectID: "project", SourceDashboardID: lifecycle.ID, ActorID: "actor", IdempotencyKey: "fork-context"})
	if err == nil || repository.createCalls != 0 {
		t.Fatalf("err=%v createCalls=%d", err, repository.createCalls)
	}
}
