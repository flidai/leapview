package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringsqlite "github.com/flidai/leapview/internal/dashboard/authoring/sqlite"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/project/graph"
)

type canonicalRevalidationFixture struct {
	store      *platform.Store
	repository *authoringsqlite.Repository
	revision   authoring.Revision
	lifecycle  authoring.DashboardLifecycle
	generation authoring.RevalidationGeneration
	deps       []graph.ResourceID
	prior      authoring.CompiledRevision
}

func newCanonicalRevalidationFixture(t *testing.T) canonicalRevalidationFixture {
	t.Helper()
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "revalidation.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES ('owner', 'owner@example.test', 'Owner')`); err != nil {
		t.Fatal(err)
	}
	repository := authoringsqlite.NewRepository(store.SQLDB())
	input, revision := canonicalSQLiteInput(t, "project:sales", "dashboard:revalidate", "revision-1", authoring.CreateOperation{})
	if _, err := repository.Create(ctx, input); err != nil {
		t.Fatal(err)
	}
	priorIdentity := servingIdentity(t, "project:sales", "test", "generation-1")
	definition := dashboarddefinition.Definition{ID: "dashboard:revalidate", Title: "Sales", SemanticModel: "model:sales"}
	prior, err := authoring.NewCompiledRevision("project:sales", "dashboard:revalidate", revision.Token(), definition, priorIdentity, time.Date(2026, 8, 18, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Publish(ctx, authoring.PublishInput{ProjectID: "project:sales", DashboardID: "dashboard:revalidate", ExpectedDraftRevision: revision.Token(), Published: authoring.Published{Revision: revision.Token(), Compilation: prior.Token(), PublishedAt: prior.CompiledAt, Provenance: revision.Provenance}, Compilation: prior, Evidence: authoring.CommandEvidence{ID: "publish-revalidation", Fingerprint: "publish-revalidation", Action: authoring.AuthorizationActionPublish, Provenance: revision.Provenance, OccurredAt: prior.CompiledAt}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO serving_states (id, project_id, environment, status) VALUES ('generation-2', 'project:sales', 'test', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO project_active_serving_states (project_id, environment, generation_id) VALUES ('project:sales', 'test', 'generation-2')`); err != nil {
		t.Fatal(err)
	}
	projectGraph, err := graph.NewProjectGraph([]graph.Resource{{ID: "project:sales", Kind: graph.KindProject, Name: "sales-project"}, {ID: "dashboard:revalidate", Kind: graph.KindDashboard, Name: "revalidate-dashboard"}, {ID: "model:sales", Kind: graph.KindSemanticModel, Name: "sales-model"}}, []graph.Edge{{From: "dashboard:revalidate", To: "model:sales"}})
	if err != nil {
		t.Fatal(err)
	}
	identity := servingIdentity(t, "project:sales", "test", "generation-2")
	authorization, err := accesssnapshot.NewAuthorizationSnapshot(identity, projectGraph, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	generation := authoring.RevalidationGeneration{Identity: identity, Graph: projectGraph, Authorization: authorization, ChangedIDs: []graph.ResourceID{"model:sales"}}
	lifecycle, err := repository.Get(ctx, "project:sales", "dashboard:revalidate")
	if err != nil {
		t.Fatal(err)
	}
	return canonicalRevalidationFixture{store: store, repository: repository, revision: revision, lifecycle: lifecycle, generation: generation, deps: projectGraph.Dependencies("dashboard:revalidate"), prior: prior}
}

func canonicalRevalidationCommit(t *testing.T, fixture canonicalRevalidationFixture, attempt string, lifecycle authoring.DashboardLifecycle, prior, compiled authoring.CompiledRevision) authoring.RevalidationCommit {
	t.Helper()
	return authoring.RevalidationCommit{AttemptID: attempt, Generation: fixture.generation, Dashboard: lifecycle, AuthoredRevision: fixture.revision, PriorCompilation: prior.Token(), Compilation: compiled, DependencyIDs: fixture.deps, AttemptedAt: compiled.CompiledAt}
}

func TestCanonicalSQLiteRevalidationCASPreservesEvidenceAndClearsFailures(t *testing.T) {
	ctx := context.Background()
	fixture := newCanonicalRevalidationFixture(t)
	definition := fixture.prior.Definition
	compiled, err := authoring.NewCompiledRevision("project:sales", "dashboard:revalidate", fixture.revision.Token(), definition, fixture.generation.Identity, time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	commit := canonicalRevalidationCommit(t, fixture, "018f4f2e-0000-7000-8000-000000000001", fixture.lifecycle, fixture.prior, compiled)
	if err := fixture.repository.CommitRevalidation(ctx, commit); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repository.CommitRevalidation(ctx, commit); !errors.Is(err, authoring.ErrRevalidationConflict) {
		t.Fatalf("replayed CAS error = %v", err)
	}
	retained, err := fixture.repository.GetPublishedCompilation(ctx, "project:sales", "dashboard:revalidate")
	if err != nil || retained.SemanticIdentity != fixture.generation.Identity {
		t.Fatalf("published evidence = %#v err=%v", retained, err)
	}
	current, err := fixture.repository.Get(ctx, "project:sales", "dashboard:revalidate")
	if err != nil {
		t.Fatal(err)
	}
	failure := authoring.RevalidationFailure{Identity: fixture.generation.Identity, DependencyIDs: fixture.deps, Code: "INVALID_DEPENDENCY", Message: "model changed", FailedAt: time.Date(2026, 8, 18, 13, 0, 0, 0, time.UTC)}
	if err := fixture.repository.RecordRevalidationFailure(ctx, authoring.RevalidationFailureInput{AttemptID: "018f4f2e-0000-7000-8000-000000000002", Generation: fixture.generation, Dashboard: current, AuthoredRevision: fixture.revision, PriorCompilation: compiled.Token(), DependencyIDs: fixture.deps, Failure: failure}); err != nil {
		t.Fatal(err)
	}
	failed, err := fixture.repository.Get(ctx, "project:sales", "dashboard:revalidate")
	if err != nil || failed.Revalidation == nil || failed.Revalidation.Code != failure.Code {
		t.Fatalf("failure projection = %#v err=%v", failed.Revalidation, err)
	}
	retryCompiled, err := authoring.NewCompiledRevision("project:sales", "dashboard:revalidate", fixture.revision.Token(), definition, fixture.generation.Identity, time.Date(2026, 8, 18, 14, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	current, err = fixture.repository.Get(ctx, "project:sales", "dashboard:revalidate")
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.repository.CommitRevalidation(ctx, canonicalRevalidationCommit(t, fixture, "018f4f2e-0000-7000-8000-000000000003", current, compiled, retryCompiled)); err != nil {
		t.Fatal(err)
	}
	cleared, err := fixture.repository.Get(ctx, "project:sales", "dashboard:revalidate")
	if err != nil || cleared.Revalidation != nil {
		t.Fatalf("failure was not cleared = %#v err=%v", cleared.Revalidation, err)
	}
}

func TestCanonicalSQLiteRevalidationConcurrentCASAdvancesOnce(t *testing.T) {
	ctx := context.Background()
	fixture := newCanonicalRevalidationFixture(t)
	compiled, err := authoring.NewCompiledRevision("project:sales", "dashboard:revalidate", fixture.revision.Token(), fixture.prior.Definition, fixture.generation.Identity, time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	commits := []authoring.RevalidationCommit{
		canonicalRevalidationCommit(t, fixture, "018f4f2e-0000-7000-8000-000000000004", fixture.lifecycle, fixture.prior, compiled),
		canonicalRevalidationCommit(t, fixture, "018f4f2e-0000-7000-8000-000000000005", fixture.lifecycle, fixture.prior, compiled),
	}
	results := make(chan error, len(commits))
	var group sync.WaitGroup
	for _, commit := range commits {
		group.Add(1)
		go func(input authoring.RevalidationCommit) {
			defer group.Done()
			results <- fixture.repository.CommitRevalidation(ctx, input)
		}(commit)
	}
	group.Wait()
	close(results)
	var success, conflict int
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, authoring.ErrRevalidationConflict):
			conflict++
		default:
			t.Fatalf("concurrent CAS error = %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("concurrent CAS success=%d conflict=%d", success, conflict)
	}
}
