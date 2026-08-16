package sqlite

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

const testArtifactDigest = "sha256:" + "a" + "000000000000000000000000000000000000000000000000000000000000000"

func testIdentity(environment, generation string) projectgraph.ServingIdentity {
	return projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: environment, GenerationID: generation}
}

func seedRefreshGenerations(t *testing.T, store *platform.Store) {
	t.Helper()
	for _, environment := range []string{"dev", "prod"} {
		if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO serving_states (id, project_id, environment, status) VALUES (?, 'project_sales', ?, 'active')`, "generation_a", environment); err != nil {
			t.Fatalf("insert serving generation: %v", err)
		}
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO serving_states (id, project_id, environment, status) VALUES ('generation_b', 'project_sales', 'prod', 'active')`); err != nil {
		t.Fatalf("insert serving generation b: %v", err)
	}
}

func validDigest(suffix string) string { return "sha256:" + strings.Repeat(suffix, 64) }

func TestRepositoryReconcileAndClaimDueCoalescesCatchUp(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO workspaces (id, title) VALUES ('sales', 'Sales')`); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	seedRefreshGenerations(t, store)
	repo := NewRepository(store.SQLDB())
	schedule, err := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	deployedAt := time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC)
	if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{
		Identity: testIdentity("prod", "generation_a"), ArtifactDigest: testArtifactDigest,
		Pipelines: []refreshschedule.Definition{{ID: "pipeline_sales_refresh", Name: "sales-refresh", SemanticModelID: "semantic_sales", Schedules: []refreshschedule.Schedule{schedule}}},
		Now:       deployedAt,
	}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	next, ok, err := repo.NextRun(t.Context(), testIdentity("prod", "generation_a"), "pipeline_sales_refresh")
	if err != nil || !ok || !next.Equal(time.Date(2026, 7, 18, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("NextRun() = %s, %v, %v", next, ok, err)
	}

	due, err := repo.ClaimDue(t.Context(), testIdentity("prod", "generation_a"), time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ClaimDue() error = %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("due = %#v, want one catch-up occurrence", due)
	}
	if due[0].PipelineID != "pipeline_sales_refresh" || due[0].SemanticModelID != "semantic_sales" {
		t.Fatalf("occurrence = %#v", due[0])
	}
	if due[0].ArtifactDigest != testArtifactDigest {
		t.Fatalf("artifact digest = %q, want canonical digest", due[0].ArtifactDigest)
	}
	if !due[0].ScheduledAt.Equal(time.Date(2026, 7, 20, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("scheduled at = %s", due[0].ScheduledAt)
	}

	again, err := repo.ClaimDue(t.Context(), testIdentity("prod", "generation_a"), time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("second ClaimDue() error = %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second due = %#v, want none", again)
	}
}

func TestRepositoryClaimDueDoesNotAdvanceAnotherEnvironment(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO workspaces (id, title) VALUES ('sales', 'Sales')`); err != nil {
		t.Fatal(err)
	}
	seedRefreshGenerations(t, store)
	repo := NewRepository(store.SQLDB())
	schedule, _ := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	deployedAt := time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC)
	for _, environment := range []string{"dev", "prod"} {
		if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{
			Identity: testIdentity(environment, "generation_a"), ArtifactDigest: validDigest("a"), Now: deployedAt,
			Pipelines: []refreshschedule.Definition{{ID: "pipeline_sales_refresh", SemanticModelID: "semantic_sales", Schedules: []refreshschedule.Schedule{schedule}}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	due, err := repo.ClaimDue(t.Context(), testIdentity("dev", "generation_a"), time.Date(2026, 7, 18, 7, 0, 0, 0, time.UTC))
	if err != nil || len(due) != 1 || due[0].Identity.Environment != "dev" {
		t.Fatalf("dev ClaimDue() = %#v, %v", due, err)
	}
	prodNext, ok, err := repo.NextRun(t.Context(), testIdentity("prod", "generation_a"), "pipeline_sales_refresh")
	if err != nil || !ok || !prodNext.Equal(time.Date(2026, 7, 18, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("prod next run = %s, found=%v, err=%v", prodNext, ok, err)
	}
}

func TestRepositoryCoalescesSimultaneouslyDueScheduleEntries(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO workspaces (id, title) VALUES ('sales', 'Sales')`); err != nil {
		t.Fatal(err)
	}
	seedRefreshGenerations(t, store)
	morning, _ := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	later, _ := refreshschedule.ParseSchedule("0 7 * * *", "UTC")
	repo := NewRepository(store.SQLDB())
	if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{
		Identity: testIdentity("prod", "generation_a"), ArtifactDigest: testArtifactDigest, Now: time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC),
		Pipelines: []refreshschedule.Definition{{ID: "pipeline_sales_refresh", SemanticModelID: "semantic_sales", Schedules: []refreshschedule.Schedule{morning, later}}},
	}); err != nil {
		t.Fatal(err)
	}
	due, err := repo.ClaimDue(t.Context(), testIdentity("prod", "generation_a"), time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || !due[0].ScheduledAt.Equal(time.Date(2026, 7, 18, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("due = %#v, want one coalesced latest occurrence", due)
	}
}

func TestRepositoryReleaseOccurrenceMakesQueueFailureRetryable(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO workspaces (id, title) VALUES ('sales', 'Sales')`); err != nil {
		t.Fatal(err)
	}
	seedRefreshGenerations(t, store)
	schedule, _ := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	repo := NewRepository(store.SQLDB())
	if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{
		Identity: testIdentity("prod", "generation_a"), ArtifactDigest: testArtifactDigest, Now: time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC),
		Pipelines: []refreshschedule.Definition{{ID: "pipeline_sales_refresh", SemanticModelID: "semantic_sales", Schedules: []refreshschedule.Schedule{schedule}}},
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 18, 7, 0, 0, 0, time.UTC)
	first, err := repo.ClaimDue(t.Context(), testIdentity("prod", "generation_a"), now)
	if err != nil || len(first) != 1 {
		t.Fatalf("first ClaimDue() = %#v, %v", first, err)
	}
	if err := repo.ReleaseOccurrence(t.Context(), first[0]); err != nil {
		t.Fatal(err)
	}
	second, err := repo.ClaimDue(t.Context(), testIdentity("prod", "generation_a"), now)
	if err != nil || len(second) != 1 {
		t.Fatalf("second ClaimDue() = %#v, %v, want retry", second, err)
	}
	if second[0].ArtifactDigest != testArtifactDigest || !second[0].ScheduledAt.Equal(first[0].ScheduledAt) {
		t.Fatalf("retry occurrence = %#v, want %#v", second[0], first[0])
	}
}

func TestRepositoryRecoversAbandonedOccurrenceClaim(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO workspaces (id, title) VALUES ('sales', 'Sales')`); err != nil {
		t.Fatal(err)
	}
	seedRefreshGenerations(t, store)
	schedule, _ := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	repo := NewRepository(store.SQLDB())
	if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{
		Identity: testIdentity("prod", "generation_a"), ArtifactDigest: testArtifactDigest, Now: time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC),
		Pipelines: []refreshschedule.Definition{{ID: "pipeline_sales_refresh", SemanticModelID: "semantic_sales", Schedules: []refreshschedule.Schedule{schedule}}},
	}); err != nil {
		t.Fatal(err)
	}
	first, err := repo.ClaimDue(t.Context(), testIdentity("prod", "generation_a"), time.Date(2026, 7, 18, 7, 0, 0, 0, time.UTC))
	if err != nil || len(first) != 1 {
		t.Fatalf("first ClaimDue() = %#v, %v", first, err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `
UPDATE refresh_pipeline_occurrences SET claimed_at = '2026-07-18T06:00:00Z'
WHERE project_id = 'project_sales' AND environment = 'prod' AND generation_id = 'generation_a' AND pipeline_id = 'pipeline_sales_refresh'`); err != nil {
		t.Fatal(err)
	}
	recovered, err := repo.ClaimDue(t.Context(), testIdentity("prod", "generation_a"), time.Date(2026, 7, 18, 7, 10, 0, 0, time.UTC))
	if err != nil || len(recovered) != 1 {
		t.Fatalf("recovered ClaimDue() = %#v, %v", recovered, err)
	}
	if !recovered[0].ScheduledAt.Equal(first[0].ScheduledAt) || recovered[0].ArtifactDigest != first[0].ArtifactDigest {
		t.Fatalf("recovered occurrence = %#v, want %#v", recovered[0], first[0])
	}
}

func TestRepositoryClaimDueDeduplicatesConcurrentDispatchers(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO workspaces (id, title) VALUES ('sales', 'Sales')`); err != nil {
		t.Fatal(err)
	}
	seedRefreshGenerations(t, store)
	schedule, _ := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	repo := NewRepository(store.SQLDB())
	if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{
		Identity: testIdentity("prod", "generation_a"), ArtifactDigest: testArtifactDigest, Now: time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC),
		Pipelines: []refreshschedule.Definition{{ID: "pipeline_sales_refresh", SemanticModelID: "semantic_sales", Schedules: []refreshschedule.Schedule{schedule}}},
	}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan []refreshschedule.Occurrence, 2)
	errors := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			due, err := repo.ClaimDue(t.Context(), testIdentity("prod", "generation_a"), time.Date(2026, 7, 18, 7, 0, 0, 0, time.UTC))
			results <- due
			errors <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errors)
	claimed := 0
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	for due := range results {
		claimed += len(due)
	}
	if claimed != 1 {
		t.Fatalf("claimed occurrences = %d, want exactly one", claimed)
	}
}

func TestRepositoryReconcileRemovesSupersededSchedules(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO workspaces (id, title) VALUES ('sales', 'Sales')`); err != nil {
		t.Fatal(err)
	}
	seedRefreshGenerations(t, store)
	repo := NewRepository(store.SQLDB())
	schedule, _ := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	now := time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC)
	input := refreshschedule.ReconcileInput{Identity: testIdentity("prod", "generation_a"), ArtifactDigest: testArtifactDigest, Pipelines: []refreshschedule.Definition{{ID: "pipeline_sales_refresh", SemanticModelID: "semantic_sales", Schedules: []refreshschedule.Schedule{schedule}}}, Now: now}
	if err := repo.Reconcile(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	input.ArtifactDigest = validDigest("b")
	input.Pipelines = nil
	if err := repo.Reconcile(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	due, err := repo.ClaimDue(t.Context(), testIdentity("prod", "generation_a"), now.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("due = %#v, want removed schedule to be ineligible", due)
	}
}

func TestRepositoryGenerationIsolationAcrossScheduleOperations(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedRefreshGenerations(t, store)
	repo := NewRepository(store.SQLDB())
	schedule, _ := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	now := time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		identity projectgraph.ServingIdentity
		pipeline refreshschedule.Definition
		digest   string
	}{
		{testIdentity("prod", "generation_a"), refreshschedule.Definition{ID: "pipeline_a", SemanticModelID: "semantic_a", Schedules: []refreshschedule.Schedule{schedule}}, validDigest("a")},
		{testIdentity("prod", "generation_b"), refreshschedule.Definition{ID: "pipeline_b", SemanticModelID: "semantic_b", Schedules: []refreshschedule.Schedule{schedule}}, validDigest("b")},
	} {
		if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{Identity: item.identity, ArtifactDigest: item.digest, Pipelines: []refreshschedule.Definition{item.pipeline}, Now: now}); err != nil {
			t.Fatal(err)
		}
	}
	// Reconcile generation A must not delete generation B's schedule.
	if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{Identity: testIdentity("prod", "generation_a"), ArtifactDigest: validDigest("c"), Now: now}); err != nil {
		t.Fatal(err)
	}
	if due, err := repo.ClaimDue(t.Context(), testIdentity("prod", "generation_a"), now.Add(2*time.Hour)); err != nil || len(due) != 0 {
		t.Fatalf("generation A due = %#v, %v; want none", due, err)
	}
	dueB, err := repo.ClaimDue(t.Context(), testIdentity("prod", "generation_b"), now.Add(2*time.Hour))
	if err != nil || len(dueB) != 1 || dueB[0].Identity.GenerationID != "generation_b" {
		t.Fatalf("generation B due = %#v, %v; want one generation-B occurrence", dueB, err)
	}
	// A forged generation-A occurrence cannot attach or release B's row.
	forged := dueB[0]
	forged.Identity = testIdentity("prod", "generation_a")
	if err := repo.AttachRun(t.Context(), forged, "run_forged"); err == nil {
		t.Fatal("AttachRun forged generation = nil, want isolation error")
	}
	if err := repo.ReleaseOccurrence(t.Context(), forged); err == nil {
		t.Fatal("ReleaseOccurrence forged generation = nil, want isolation error")
	}
	if _, ok, err := repo.DataVersion(t.Context(), testIdentity("prod", "generation_a"), "semantic_b"); err != nil || ok {
		t.Fatalf("generation A read of generation B data version = found=%v err=%v", ok, err)
	}
}

func TestRepositorySemanticModelDataVersion(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO workspaces (id, title) VALUES ('sales', 'Sales')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO serving_states (id, project_id, environment, status) VALUES ('generation_a', 'project_sales', 'prod', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO refresh_jobs (id, project_id, generation_id, semantic_model_id, kind, status) VALUES ('job_1', 'project_sales', 'generation_a', 'semantic_sales', 'refresh_pipeline', 'succeeded');
INSERT INTO refresh_job_runs (id, job_id, environment, target_type, target_id, trigger_type, status) VALUES ('run_1', 'job_1', 'prod', 'refresh_pipeline', 'sales.sales-refresh', 'manual', 'succeeded');
`); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(store.SQLDB())
	want := refreshschedule.DataVersion{
		Identity: testIdentity("prod", "generation_a"), SemanticModelID: "semantic_sales", SnapshotID: 42,
		RefreshedAt: time.Date(2026, 7, 18, 6, 1, 0, 0, time.UTC),
		Source:      refreshschedule.DataVersionSourceRefresh, PipelineID: "pipeline_sales_refresh", RunID: "run_1",
	}
	if err := repo.SaveDataVersion(t.Context(), want); err != nil {
		t.Fatalf("SaveDataVersion() error = %v", err)
	}
	got, ok, err := repo.DataVersion(t.Context(), testIdentity("prod", "generation_a"), "semantic_sales")
	if err != nil || !ok {
		t.Fatalf("DataVersion() = %#v, %v, %v", got, ok, err)
	}
	if got != want {
		t.Fatalf("DataVersion() = %#v, want %#v", got, want)
	}
}
