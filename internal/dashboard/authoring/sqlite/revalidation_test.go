package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	dashboardmodel "github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringsqlite "github.com/flidai/leapview/internal/dashboard/authoring/sqlite"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/platform"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestRevalidationSQLiteCASAndFailureEvidence(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "revalidation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES (?, ?, ?)`, "owner", "owner@example.test", "Owner"); err != nil {
		t.Fatal(err)
	}
	repo := authoringsqlite.NewRepository(store.SQLDB())
	revision, lifecycle := revalidationLifecycle(t)
	if _, err := repo.Create(ctx, authoring.CreateInput{ProjectID: "project", Lifecycle: lifecycle, Revision: revision}); err != nil {
		t.Fatal(err)
	}
	oldIdentity := mustRevalidationIdentity(t, "project", "prod", "generation-1")
	oldCompiled, err := authoring.NewCompiledRevision("project", "dashboard", revision.Token(), dashboarddefinition.Definition{ID: "dashboard", Title: "Dashboard", SemanticModel: "semantic", Pages: revision.Document.Pages}, oldIdentity, time.Date(2026, 8, 15, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Publish(ctx, authoring.PublishInput{ProjectID: "project", DashboardID: "dashboard", ExpectedDraftRevision: revision.Token(), Published: authoring.Published{Revision: revision.Token(), Compilation: oldCompiled.Token(), PublishedAt: oldCompiled.CompiledAt, Provenance: revision.Provenance}, Compilation: oldCompiled, Evidence: publishEvidence("publish", oldCompiled.CompiledAt)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO serving_states (id, project_id, environment, status) VALUES (?, ?, ?, 'active')`, "generation-2", "project", "prod"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO project_active_serving_states (project_id, environment, generation_id) VALUES (?, ?, ?)`, "project", "prod", "generation-2"); err != nil {
		t.Fatal(err)
	}
	generation, graph := revalidationGeneration(t)
	current, err := repo.Get(ctx, "project", "dashboard")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := authoring.NewCompiledRevision("project", "dashboard", revision.Token(), oldCompiled.Definition, generation.Identity, time.Date(2026, 8, 16, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	commit := authoring.RevalidationCommit{AttemptID: "attempt_00000000000000000000000000000001", Generation: generation, Dashboard: current, AuthoredRevision: revision, PriorCompilation: current.Published.Compilation, Compilation: compiled, DependencyIDs: graph.Dependencies("dashboard"), AttemptedAt: compiled.CompiledAt}
	if err := repo.CommitRevalidation(ctx, commit); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetPublishedCompilation(ctx, "project", "dashboard")
	if err != nil || got.SemanticIdentity != generation.Identity || got.SemanticModelID != "semantic" {
		t.Fatalf("compiled=%#v err=%v", got, err)
	}
	if err := repo.CommitRevalidation(ctx, commit); !errors.Is(err, authoring.ErrRevalidationConflict) {
		t.Fatalf("second CAS = %v, want conflict", err)
	}
	failed := authoring.RevalidationFailure{Identity: generation.Identity, DependencyIDs: graph.Dependencies("dashboard"), Code: "INVALID_DEPENDENCY", Message: "semantic model column removed", FailedAt: time.Date(2026, 8, 16, 2, 0, 0, 0, time.UTC)}
	if err := repo.RecordRevalidationFailure(ctx, authoring.RevalidationFailureInput{AttemptID: "attempt_00000000000000000000000000000002", Generation: generation, Dashboard: current, AuthoredRevision: revision, PriorCompilation: compiled.Token(), DependencyIDs: failed.DependencyIDs, Failure: failed}); err != nil {
		t.Fatalf("retry failure = %v", err)
	}
	// A failed retry occupies its own immutable attempt key; the published
	// evidence remains intact while actionable failure state is retained.
	if got, err := repo.GetPublishedCompilation(ctx, "project", "dashboard"); err != nil || got.SemanticIdentity != generation.Identity {
		t.Fatalf("published evidence after failure attempt=%#v err=%v", got, err)
	}
	failedLifecycle, err := repo.Get(ctx, "project", "dashboard")
	if err != nil || failedLifecycle.Revalidation == nil || failedLifecycle.Revalidation.Code != "INVALID_DEPENDENCY" {
		t.Fatalf("retry failure state=%#v err=%v", failedLifecycle.Revalidation, err)
	}
	// A subsequent successful retry in the same generation gets another
	// immutable attempt row and clears the failure projection without changing
	// the authored revision or serving identity contract.
	retryCompiled, err := authoring.NewCompiledRevision("project", "dashboard", revision.Token(), compiled.Definition, generation.Identity, time.Date(2026, 8, 16, 2, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	retryLifecycle, err := repo.Get(ctx, "project", "dashboard")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CommitRevalidation(ctx, authoring.RevalidationCommit{AttemptID: "attempt_00000000000000000000000000000003", Generation: generation, Dashboard: retryLifecycle, AuthoredRevision: revision, PriorCompilation: retryLifecycle.Published.Compilation, Compilation: retryCompiled, DependencyIDs: graph.Dependencies("dashboard"), AttemptedAt: retryCompiled.CompiledAt}); err != nil {
		t.Fatalf("successful retry = %v", err)
	}
	cleared, err := repo.Get(ctx, "project", "dashboard")
	if err != nil || cleared.Revalidation != nil {
		t.Fatalf("newer success did not clear failure: %#v err=%v", cleared.Revalidation, err)
	}
	// A database failure after the immutable insert boundary rolls the whole
	// transaction back: neither a new compiled row nor a pointer advance may
	// leak into the published projection.
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO serving_states (id, project_id, environment, status) VALUES (?, ?, ?, 'validated')`, "generation-3", "project", "prod"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE project_active_serving_states SET generation_id = ? WHERE project_id = ? AND environment = ?`, "generation-3", "project", "prod"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE serving_states SET status = 'inactive' WHERE id = ?`, "generation-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE serving_states SET status = 'active' WHERE id = ?`, "generation-3"); err != nil {
		t.Fatal(err)
	}
	nextGeneration := generation
	nextGeneration.Identity = mustRevalidationIdentity(t, "project", "prod", "generation-3")
	nextGeneration.Authorization, err = accesssnapshot.NewAuthorizationSnapshot(nextGeneration.Identity, graph, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	nextCompiled, err := authoring.NewCompiledRevision("project", "dashboard", revision.Token(), compiled.Definition, nextGeneration.Identity, time.Date(2026, 8, 16, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	nextCurrent, err := repo.Get(ctx, "project", "dashboard")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `CREATE TRIGGER fail_revalidation BEFORE INSERT ON dashboard_authoring_revalidation_attempts WHEN NEW.generation_id = 'generation-3' BEGIN SELECT RAISE(ABORT, 'forced revalidation failure'); END`); err != nil {
		t.Fatal(err)
	}
	rollbackErr := repo.CommitRevalidation(ctx, authoring.RevalidationCommit{AttemptID: "attempt_00000000000000000000000000000004", Generation: nextGeneration, Dashboard: nextCurrent, AuthoredRevision: revision, PriorCompilation: nextCurrent.Published.Compilation, Compilation: nextCompiled, DependencyIDs: graph.Dependencies("dashboard"), AttemptedAt: nextCompiled.CompiledAt})
	if _, err := store.SQLDB().ExecContext(ctx, `DROP TRIGGER fail_revalidation`); err != nil {
		t.Fatal(err)
	}
	if rollbackErr == nil {
		t.Fatal("forced revalidation failure unexpectedly succeeded")
	}
	if got, err := repo.GetPublishedCompilation(ctx, "project", "dashboard"); err != nil || got.SemanticIdentity != generation.Identity {
		t.Fatalf("rollback changed published evidence=%#v err=%v", got, err)
	}
	var attempts int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM dashboard_authoring_revalidation_attempts WHERE generation_id = ?`, "generation-3").Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("rollback left attempt rows=%d err=%v", attempts, err)
	}
	failure := authoring.RevalidationFailure{Identity: nextGeneration.Identity, DependencyIDs: graph.Dependencies("dashboard"), Code: "INVALID_DEPENDENCY", Message: "semantic model removed", FailedAt: time.Date(2026, 8, 16, 4, 0, 0, 0, time.UTC)}
	if err := repo.RecordRevalidationFailure(ctx, authoring.RevalidationFailureInput{AttemptID: "attempt_00000000000000000000000000000005", Generation: nextGeneration, Dashboard: nextCurrent, AuthoredRevision: revision, PriorCompilation: nextCurrent.Published.Compilation, DependencyIDs: failure.DependencyIDs, Failure: failure}); err != nil {
		t.Fatal(err)
	}
	failedLifecycle, err = repo.Get(ctx, "project", "dashboard")
	if err != nil || failedLifecycle.Revalidation == nil || failedLifecycle.Revalidation.Code != "INVALID_DEPENDENCY" {
		t.Fatalf("failure state=%#v err=%v", failedLifecycle.Revalidation, err)
	}
	if got, err := repo.GetPublishedCompilation(ctx, "project", "dashboard"); err != nil || got.SemanticIdentity != generation.Identity {
		t.Fatalf("failure state changed prior evidence=%#v err=%v", got, err)
	}
}

func TestRevalidationSQLiteConcurrentCASOnlyOneAdvances(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "concurrent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES (?, ?, ?)`, "owner", "owner@example.test", "Owner"); err != nil {
		t.Fatal(err)
	}
	repo := authoringsqlite.NewRepository(store.SQLDB())
	revision, lifecycle := revalidationLifecycle(t)
	if _, err := repo.Create(ctx, authoring.CreateInput{ProjectID: "project", Lifecycle: lifecycle, Revision: revision}); err != nil {
		t.Fatal(err)
	}
	oldIdentity := mustRevalidationIdentity(t, "project", "prod", "generation-1")
	old, _ := authoring.NewCompiledRevision("project", "dashboard", revision.Token(), dashboarddefinition.Definition{ID: "dashboard", Title: "Dashboard", SemanticModel: "semantic", Pages: revision.Document.Pages}, oldIdentity, time.Date(2026, 8, 15, 1, 0, 0, 0, time.UTC))
	if _, err := repo.Publish(ctx, authoring.PublishInput{ProjectID: "project", DashboardID: "dashboard", ExpectedDraftRevision: revision.Token(), Published: authoring.Published{Revision: revision.Token(), Compilation: old.Token(), PublishedAt: old.CompiledAt, Provenance: revision.Provenance}, Compilation: old, Evidence: publishEvidence("publish", old.CompiledAt)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO serving_states (id, project_id, environment, status) VALUES (?, ?, ?, 'active')`, "generation-2", "project", "prod"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO project_active_serving_states (project_id, environment, generation_id) VALUES (?, ?, ?)`, "project", "prod", "generation-2"); err != nil {
		t.Fatal(err)
	}
	generation, _ := revalidationGeneration(t)
	current, _ := repo.Get(ctx, "project", "dashboard")
	compiled, _ := authoring.NewCompiledRevision("project", "dashboard", revision.Token(), old.Definition, generation.Identity, time.Date(2026, 8, 16, 1, 0, 0, 0, time.UTC))
	commit := authoring.RevalidationCommit{AttemptID: "attempt_00000000000000000000000000000006", Generation: generation, Dashboard: current, AuthoredRevision: revision, PriorCompilation: current.Published.Compilation, Compilation: compiled, DependencyIDs: generation.Graph.Dependencies("dashboard"), AttemptedAt: compiled.CompiledAt}
	commits := []authoring.RevalidationCommit{commit, commit}
	commits[1].AttemptID = "attempt_00000000000000000000000000000007"
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := range commits {
		wg.Add(1)
		go func(input authoring.RevalidationCommit) {
			defer wg.Done()
			results <- repo.CommitRevalidation(ctx, input)
		}(commits[i])
	}
	wg.Wait()
	close(results)
	var success, conflict int
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, authoring.ErrRevalidationConflict) {
			conflict++
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("concurrent CAS success=%d conflict=%d", success, conflict)
	}
}

func revalidationLifecycle(t *testing.T) (authoring.Revision, authoring.DashboardLifecycle) {
	t.Helper()
	doc := authoring.Dashboard{ID: "dashboard", Title: "Dashboard", SemanticModel: "semantic", Visuals: map[string]authoring.AuthoringVisualization{}, Pages: []dashboardmodel.Page{{ID: "overview"}}}
	prov := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}
	revision, err := authoring.NewRevision("revision", "dashboard", 1, time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC), doc, prov)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := authoring.DashboardLifecycle{ProjectID: "project", ID: "dashboard", OwnerPrincipalID: "owner", Slug: "dashboard", Title: "Dashboard", SemanticModel: "semantic", Visibility: authoring.VisibilityOrganization, Status: authoring.LifecycleStatusDraft, Draft: &authoring.Draft{ID: "draft", DashboardID: "dashboard", Revision: revision.Token(), Provenance: prov}}
	return revision, lifecycle
}

func revalidationGeneration(t *testing.T) (authoring.RevalidationGeneration, projectgraph.ProjectGraph) {
	t.Helper()
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "project", Kind: projectgraph.KindProject, Name: "project"}, {ID: "dashboard", Kind: projectgraph.KindDashboard, Name: "dashboard"}, {ID: "semantic", Kind: projectgraph.KindSemanticModel, Name: "semantic"}}, []projectgraph.Edge{{From: "dashboard", To: "semantic"}})
	if err != nil {
		t.Fatal(err)
	}
	identity := mustRevalidationIdentity(t, "project", "prod", "generation-2")
	auth, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return authoring.RevalidationGeneration{Identity: identity, Graph: graph, Authorization: auth, ChangedIDs: []projectgraph.ResourceID{"semantic"}}, graph
}

func publishEvidence(id string, at time.Time) authoring.CommandEvidence {
	return authoring.CommandEvidence{ID: authoring.CommandID(id), Fingerprint: id + "-fingerprint", Action: authoring.AuthorizationActionPublish, Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}, OccurredAt: at}
}

func mustRevalidationIdentity(t *testing.T, project, environment, generation string) projectgraph.ServingIdentity {
	t.Helper()
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(project), environment, generation)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}
