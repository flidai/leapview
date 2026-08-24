package sqlite

import (
	"fmt"
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
	for _, generation := range []struct {
		id, environment string
	}{
		{id: "generation_dev_a", environment: "dev"},
		{id: "generation_a", environment: "prod"},
	} {
		if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO serving_states (id, project_id, environment, status) VALUES (?, 'project_sales', ?, 'active')`, generation.id, generation.environment); err != nil {
			t.Fatalf("insert serving generation: %v", err)
		}
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO serving_states (id, project_id, environment, status) VALUES ('generation_b', 'project_sales', 'prod', 'validated')`); err != nil {
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
	seedRefreshGenerations(t, store)
	repo := NewRepository(store.SQLDB())
	schedule, err := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	schedule.ID = "daily"
	deployedAt := time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC)
	if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{
		Identity: testIdentity("prod", "generation_a"), ArtifactDigest: testArtifactDigest,
		Pipelines: []refreshschedule.Definition{{ID: "pipeline_sales_refresh", Name: "sales-refresh", SemanticModelID: "semantic_sales", Timezone: "UTC", StartingDeadlineSeconds: 4 * 60 * 60, ConcurrencyPolicy: refreshschedule.ConcurrencyForbid, Schedules: []refreshschedule.Schedule{schedule}}},
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

func TestRepositoryReconcileCarriesCronCursorAcrossGenerationArtifactAndScheduleRename(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedRefreshGenerations(t, store)
	repo := NewRepository(store.SQLDB())

	oldSchedule, err := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	oldSchedule.ID = "daily"
	deployedAt := time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC)
	if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{
		Identity: testIdentity("prod", "generation_a"), ArtifactDigest: validDigest("a"), Now: deployedAt,
		Pipelines: []refreshschedule.Definition{{
			ID: "pipeline_sales_refresh", SemanticModelID: "semantic_sales", Timezone: "UTC",
			StartingDeadlineSeconds: 7200, ConcurrencyPolicy: refreshschedule.ConcurrencyForbid,
			Schedules: []refreshschedule.Schedule{oldSchedule},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	renamedSchedule, err := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	renamedSchedule.ID = "every weekday at 06:00"
	restartedAt := time.Date(2026, 7, 18, 6, 30, 0, 0, time.UTC)
	if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{
		Identity: testIdentity("prod", "generation_b"), ArtifactDigest: validDigest("b"), Now: restartedAt,
		Pipelines: []refreshschedule.Definition{{
			ID: "pipeline_sales_refresh", SemanticModelID: "semantic_sales", Timezone: "UTC",
			StartingDeadlineSeconds: 7200, ConcurrencyPolicy: refreshschedule.ConcurrencyForbid,
			Schedules: []refreshschedule.Schedule{renamedSchedule},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	due, err := repo.ClaimDue(t.Context(), testIdentity("prod", "generation_b"), restartedAt)
	if err != nil {
		t.Fatal(err)
	}
	wantNominal := time.Date(2026, 7, 18, 6, 0, 0, 0, time.UTC)
	if len(due) != 1 || !due[0].ScheduledAt.Equal(wantNominal) || strings.Join(due[0].MatchingScheduleIDs, ",") != renamedSchedule.ID || due[0].ArtifactDigest != validDigest("b") {
		t.Fatalf("generation recovery occurrence = %#v, want nominal %s with renamed evidence and new artifact", due, wantNominal)
	}
}

func TestRepositoryClaimDueDoesNotAdvanceAnotherGeneration(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedRefreshGenerations(t, store)
	repo := NewRepository(store.SQLDB())
	schedule, _ := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	schedule.ID = "daily"
	deployedAt := time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		identity projectgraph.ServingIdentity
		pipeline refreshschedule.Definition
	}{
		{identity: testIdentity("prod", "generation_a"), pipeline: refreshschedule.Definition{ID: "pipeline_sales_refresh_a", SemanticModelID: "semantic_sales_a"}},
		{identity: testIdentity("prod", "generation_b"), pipeline: refreshschedule.Definition{ID: "pipeline_sales_refresh_b", SemanticModelID: "semantic_sales_b"}},
	} {
		if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{
			Identity: item.identity, ArtifactDigest: validDigest("a"), Now: deployedAt,
			Pipelines: []refreshschedule.Definition{{ID: item.pipeline.ID, SemanticModelID: item.pipeline.SemanticModelID, Timezone: "UTC", StartingDeadlineSeconds: 7200, ConcurrencyPolicy: refreshschedule.ConcurrencyForbid, Schedules: []refreshschedule.Schedule{schedule}}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	due, err := repo.ClaimDue(t.Context(), testIdentity("prod", "generation_a"), time.Date(2026, 7, 18, 7, 0, 0, 0, time.UTC))
	if err != nil || len(due) != 1 || due[0].Identity.GenerationID != "generation_a" {
		t.Fatalf("generation A ClaimDue() = %#v, %v", due, err)
	}
	generationBNext, ok, err := repo.NextRun(t.Context(), testIdentity("prod", "generation_b"), "pipeline_sales_refresh_b")
	if err != nil || !ok || !generationBNext.Equal(time.Date(2026, 7, 18, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("generation B next run = %s, found=%v, err=%v", generationBNext, ok, err)
	}
}

func TestRepositoryCoalescesOverlappingScheduleEntries(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedRefreshGenerations(t, store)
	morning, _ := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	later, _ := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	morning.ID = "morning"
	later.ID = "later"
	repo := NewRepository(store.SQLDB())
	if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{
		Identity: testIdentity("prod", "generation_a"), ArtifactDigest: testArtifactDigest, Now: time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC),
		Pipelines: []refreshschedule.Definition{{ID: "pipeline_sales_refresh", SemanticModelID: "semantic_sales", Timezone: "UTC", StartingDeadlineSeconds: 7200, ConcurrencyPolicy: refreshschedule.ConcurrencyForbid, Schedules: []refreshschedule.Schedule{morning, later}}},
	}); err != nil {
		t.Fatal(err)
	}
	due, err := repo.ClaimDue(t.Context(), testIdentity("prod", "generation_a"), time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || !due[0].ScheduledAt.Equal(time.Date(2026, 7, 18, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("due = %#v, want one overlapping occurrence", due)
	}
	if got, want := strings.Join(due[0].MatchingScheduleIDs, ","), "later,morning"; got != want {
		t.Fatalf("matching schedule IDs = %q, want %q", got, want)
	}
}

func TestRepositoryReleaseOccurrenceMakesQueueFailureRetryable(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedRefreshGenerations(t, store)
	schedule, _ := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	schedule.ID = "daily"
	unrelated, _ := refreshschedule.ParseSchedule("0 8 * * *", "UTC")
	unrelated.ID = "morning-report"
	repo := NewRepository(store.SQLDB())
	if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{
		Identity: testIdentity("prod", "generation_a"), ArtifactDigest: testArtifactDigest, Now: time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC),
		Pipelines: []refreshschedule.Definition{{ID: "pipeline_sales_refresh", SemanticModelID: "semantic_sales", Timezone: "UTC", StartingDeadlineSeconds: 7200, ConcurrencyPolicy: refreshschedule.ConcurrencyForbid, Schedules: []refreshschedule.Schedule{schedule, unrelated}}},
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
	var status, outcome string
	if err := store.SQLDB().QueryRowContext(t.Context(), `
SELECT status, outcome
FROM refresh_pipeline_occurrences
WHERE project_id = 'project_sales' AND environment = 'prod'
  AND pipeline_id = 'pipeline_sales_refresh'
  AND scheduled_at = ?`, first[0].ScheduledAt.UTC().Format(time.RFC3339Nano)).Scan(&status, &outcome); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || outcome != "dispatch_failed" {
		t.Fatalf("released occurrence status=%q outcome=%q, want pending/dispatch_failed", status, outcome)
	}
	var unrelatedCursor string
	if err := store.SQLDB().QueryRowContext(t.Context(), `
SELECT next_run_at FROM refresh_pipeline_schedules
WHERE project_id = 'project_sales' AND environment = 'prod'
  AND pipeline_id = 'pipeline_sales_refresh' AND trigger_id = 'morning-report'`).Scan(&unrelatedCursor); err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC).Format(time.RFC3339Nano); unrelatedCursor != want {
		t.Fatalf("unrelated schedule cursor = %q, want %q", unrelatedCursor, want)
	}
	// A dispatcher outage can last past newer nominal ticks. The exact pending
	// occurrence still owns the retry; catch-up must not advance over it.
	delayedRetry := now.Add(25 * time.Hour)
	second, err := repo.ClaimDue(t.Context(), testIdentity("prod", "generation_a"), delayedRetry)
	if err != nil || len(second) != 1 {
		t.Fatalf("second ClaimDue() = %#v, %v, want retry", second, err)
	}
	if second[0].ArtifactDigest != testArtifactDigest || !second[0].ScheduledAt.Equal(first[0].ScheduledAt) {
		t.Fatalf("retry occurrence = %#v, want %#v", second[0], first[0])
	}
	if got := strings.Join(second[0].MatchingScheduleIDs, ","); got != "daily" {
		t.Fatalf("retry matching schedule IDs = %q, want exact stored evidence", got)
	}
}

func TestRepositoryRecoversAbandonedOccurrenceClaim(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedRefreshGenerations(t, store)
	schedule, _ := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	schedule.ID = "daily"
	repo := NewRepository(store.SQLDB())
	if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{
		Identity: testIdentity("prod", "generation_a"), ArtifactDigest: testArtifactDigest, Now: time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC),
		Pipelines: []refreshschedule.Definition{{ID: "pipeline_sales_refresh", SemanticModelID: "semantic_sales", Timezone: "UTC", StartingDeadlineSeconds: 7200, ConcurrencyPolicy: refreshschedule.ConcurrencyForbid, Schedules: []refreshschedule.Schedule{schedule}}},
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
	seedRefreshGenerations(t, store)
	schedule, _ := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	schedule.ID = "daily"
	repo := NewRepository(store.SQLDB())
	if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{
		Identity: testIdentity("prod", "generation_a"), ArtifactDigest: testArtifactDigest, Now: time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC),
		Pipelines: []refreshschedule.Definition{{ID: "pipeline_sales_refresh", SemanticModelID: "semantic_sales", Timezone: "UTC", StartingDeadlineSeconds: 7200, ConcurrencyPolicy: refreshschedule.ConcurrencyForbid, Schedules: []refreshschedule.Schedule{schedule}}},
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
	seedRefreshGenerations(t, store)
	repo := NewRepository(store.SQLDB())
	schedule, _ := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	schedule.ID = "daily"
	now := time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC)
	input := refreshschedule.ReconcileInput{Identity: testIdentity("prod", "generation_a"), ArtifactDigest: testArtifactDigest, Pipelines: []refreshschedule.Definition{{ID: "pipeline_sales_refresh", SemanticModelID: "semantic_sales", Timezone: "UTC", ConcurrencyPolicy: refreshschedule.ConcurrencyForbid, Schedules: []refreshschedule.Schedule{schedule}}}, Now: now}
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
	schedule.ID = "daily"
	now := time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		identity projectgraph.ServingIdentity
		pipeline refreshschedule.Definition
		digest   string
	}{
		{testIdentity("prod", "generation_a"), refreshschedule.Definition{ID: "pipeline_a", SemanticModelID: "semantic_a", Timezone: "UTC", StartingDeadlineSeconds: 7200, ConcurrencyPolicy: refreshschedule.ConcurrencyForbid, Schedules: []refreshschedule.Schedule{schedule}}, validDigest("a")},
		{testIdentity("prod", "generation_b"), refreshschedule.Definition{ID: "pipeline_b", SemanticModelID: "semantic_b", Timezone: "UTC", StartingDeadlineSeconds: 7200, ConcurrencyPolicy: refreshschedule.ConcurrencyForbid, Schedules: []refreshschedule.Schedule{schedule}}, validDigest("b")},
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
	// A forged generation-A occurrence cannot release B's row.
	forged := dueB[0]
	forged.Identity = testIdentity("prod", "generation_a")
	if err := repo.ReleaseOccurrence(t.Context(), forged); err == nil {
		t.Fatal("ReleaseOccurrence forged generation = nil, want isolation error")
	}
	if err := repo.SaveDataVersion(t.Context(), refreshschedule.DataVersion{
		Identity: testIdentity("prod", "generation_b"), SemanticModelID: "semantic_b", SnapshotID: 7,
		RefreshedAt: now, Source: refreshschedule.DataVersionSourceRefresh, PipelineID: "pipeline_b",
	}); err != nil {
		t.Fatal(err)
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
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO serving_states (id, project_id, environment, status) VALUES ('generation_a', 'project_sales', 'prod', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO refresh_jobs (id, project_id, generation_id, semantic_model_id, pipeline_id, principal_id, group_ids_json, estimated_memory_bytes, kind, status) VALUES ('job_1', 'project_sales', 'generation_a', 'semantic_sales', 'pipeline_sales_refresh', 'system:refresh', '[]', 67108864, 'refresh_pipeline', 'succeeded');
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

func TestRepositoryOccurrenceIdentityExcludesGeneration(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedRefreshGenerations(t, store)
	repo := NewRepository(store.SQLDB())
	schedule, err := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	schedule.ID = "daily"
	now := time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC)
	for _, generation := range []string{"generation_a", "generation_b"} {
		if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{
			Identity: testIdentity("prod", generation), ArtifactDigest: testArtifactDigest, Now: now,
			Pipelines: []refreshschedule.Definition{{
				ID: "pipeline_sales_refresh", SemanticModelID: "semantic_sales", Timezone: "UTC", StartingDeadlineSeconds: 7200, ConcurrencyPolicy: refreshschedule.ConcurrencyForbid,
				Schedules: []refreshschedule.Schedule{schedule},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := repo.ClaimDue(t.Context(), testIdentity("prod", "generation_a"), now.Add(2*time.Hour))
	if err != nil || len(first) != 1 {
		t.Fatalf("generation A ClaimDue() = %#v, %v", first, err)
	}
	second, err := repo.ClaimDue(t.Context(), testIdentity("prod", "generation_b"), now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("generation B ClaimDue() = %#v, want logical occurrence dedupe", second)
	}
	var rows int
	var capturedGeneration string
	if err := store.SQLDB().QueryRowContext(t.Context(), `
SELECT COUNT(*), MAX(generation_id)
FROM refresh_pipeline_occurrences
WHERE project_id = 'project_sales' AND environment = 'prod'
  AND pipeline_id = 'pipeline_sales_refresh'
  AND scheduled_at = '2026-07-18T06:00:00Z'`).Scan(&rows, &capturedGeneration); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || capturedGeneration != "generation_a" {
		t.Fatalf("occurrence rows=%d generation=%q, want one generation_a row", rows, capturedGeneration)
	}
}

func TestRepositoryClaimDueOrdersNominalThenPipeline(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedRefreshGenerations(t, store)
	repo := NewRepository(store.SQLDB())
	firstTrigger, _ := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	firstTrigger.ID = "z-trigger"
	secondTrigger, _ := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	secondTrigger.ID = "a-trigger"
	now := time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC)
	if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{
		Identity: testIdentity("prod", "generation_a"), ArtifactDigest: testArtifactDigest, Now: now,
		Pipelines: []refreshschedule.Definition{
			{ID: "pipeline_z", SemanticModelID: "semantic_z", Timezone: "UTC", StartingDeadlineSeconds: 7200, ConcurrencyPolicy: refreshschedule.ConcurrencyForbid, Schedules: []refreshschedule.Schedule{firstTrigger}},
			{ID: "pipeline_a", SemanticModelID: "semantic_a", Timezone: "UTC", StartingDeadlineSeconds: 7200, ConcurrencyPolicy: refreshschedule.ConcurrencyForbid, Schedules: []refreshschedule.Schedule{secondTrigger}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	due, err := repo.ClaimDue(t.Context(), testIdentity("prod", "generation_a"), now.Add(2*time.Hour))
	if err != nil || len(due) != 2 {
		t.Fatalf("ClaimDue() = %#v, %v", due, err)
	}
	if due[0].PipelineID != "pipeline_a" || due[1].PipelineID != "pipeline_z" {
		t.Fatalf("ClaimDue order = %q, %q; want pipeline-ID tie break", due[0].PipelineID, due[1].PipelineID)
	}
	if !due[0].ScheduledAt.Equal(due[1].ScheduledAt) {
		t.Fatalf("nominal times differ: %s vs %s", due[0].ScheduledAt, due[1].ScheduledAt)
	}
}

func TestRepositoryDeadlineZeroStillDispatchesNormalControllerTick(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedRefreshGenerations(t, store)
	repo := NewRepository(store.SQLDB())
	schedule, _ := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	schedule.ID = "skip-missed"
	deployedAt := time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC)
	if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{
		Identity: testIdentity("prod", "generation_a"), ArtifactDigest: testArtifactDigest, Now: deployedAt,
		Pipelines: []refreshschedule.Definition{{ID: "pipeline_sales_refresh", SemanticModelID: "semantic_sales", Timezone: "UTC", ConcurrencyPolicy: refreshschedule.ConcurrencyForbid, Schedules: []refreshschedule.Schedule{schedule}}},
	}); err != nil {
		t.Fatal(err)
	}
	due, err := repo.ClaimDue(t.Context(), testIdentity("prod", "generation_a"), time.Date(2026, 7, 18, 6, 0, 30, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("ClaimDue() = %#v, want one normal tick occurrence", due)
	}
	next, ok, err := repo.NextRun(t.Context(), testIdentity("prod", "generation_a"), "pipeline_sales_refresh")
	if err != nil || !ok || !next.Equal(time.Date(2026, 7, 19, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("next run=%s found=%v err=%v, want next day", next, ok, err)
	}
}

func TestRepositoryClaimDueSkipsOccurrenceOutsideDeadlineWithoutReconcile(t *testing.T) {
	for _, deadline := range []int64{0, 1800} {
		t.Run(fmt.Sprintf("deadline_%d", deadline), func(t *testing.T) {
			store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			seedRefreshGenerations(t, store)
			repo := NewRepository(store.SQLDB())
			schedule, _ := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
			schedule.ID = "morning"
			deployedAt := time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC)
			if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{
				Identity: testIdentity("prod", "generation_a"), ArtifactDigest: testArtifactDigest, Now: deployedAt,
				Pipelines: []refreshschedule.Definition{{
					ID: "pipeline_sales_refresh", SemanticModelID: "semantic_sales", Timezone: "UTC",
					StartingDeadlineSeconds: deadline, ConcurrencyPolicy: refreshschedule.ConcurrencyForbid,
					Schedules: []refreshschedule.Schedule{schedule},
				}},
			}); err != nil {
				t.Fatal(err)
			}
			due, err := repo.ClaimDue(t.Context(), testIdentity("prod", "generation_a"), time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC))
			if err != nil {
				t.Fatal(err)
			}
			if len(due) != 0 {
				t.Fatalf("ClaimDue() = %#v, want expired occurrence skipped", due)
			}
			next, ok, err := repo.NextRun(t.Context(), testIdentity("prod", "generation_a"), "pipeline_sales_refresh")
			if err != nil || !ok || !next.Equal(time.Date(2026, 7, 19, 6, 0, 0, 0, time.UTC)) {
				t.Fatalf("next run=%s found=%v err=%v, want next scheduled day", next, ok, err)
			}
		})
	}
}

func TestRepositoryPositiveDeadlineRecoversOnePipelineOccurrence(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedRefreshGenerations(t, store)
	first, _ := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
	first.ID = "morning"
	second, _ := refreshschedule.ParseSchedule("0 7 * * *", "UTC")
	second.ID = "later"
	deployedAt := time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC)
	repo := NewRepository(store.SQLDB())
	if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{
		Identity: testIdentity("prod", "generation_a"), ArtifactDigest: testArtifactDigest, Now: deployedAt,
		Pipelines: []refreshschedule.Definition{{ID: "pipeline_sales_refresh", SemanticModelID: "semantic_sales", Timezone: "UTC", StartingDeadlineSeconds: 7200, ConcurrencyPolicy: refreshschedule.ConcurrencyReplace, Schedules: []refreshschedule.Schedule{first, second}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{
		Identity: testIdentity("prod", "generation_a"), ArtifactDigest: testArtifactDigest, Now: time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC),
		Pipelines: []refreshschedule.Definition{{ID: "pipeline_sales_refresh", SemanticModelID: "semantic_sales", Timezone: "UTC", StartingDeadlineSeconds: 7200, ConcurrencyPolicy: refreshschedule.ConcurrencyReplace, Schedules: []refreshschedule.Schedule{first, second}}},
	}); err != nil {
		t.Fatal(err)
	}
	due, err := repo.ClaimDue(t.Context(), testIdentity("prod", "generation_a"), time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || !due[0].ScheduledAt.Equal(time.Date(2026, 7, 18, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("due = %#v, want one latest deadline recovery", due)
	}
}

func TestRepositoryReconcileSkipsMissedOccurrenceAtOrOutsideDeadline(t *testing.T) {
	for _, deadline := range []int64{0, 1800} {
		t.Run(fmt.Sprintf("deadline_%d", deadline), func(t *testing.T) {
			store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			seedRefreshGenerations(t, store)
			scheduled, _ := refreshschedule.ParseSchedule("0 6 * * *", "UTC")
			scheduled.ID = "morning"
			deployedAt := time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC)
			repo := NewRepository(store.SQLDB())
			definition := refreshschedule.Definition{
				ID: "pipeline_sales_refresh", SemanticModelID: "semantic_sales", Timezone: "UTC",
				StartingDeadlineSeconds: deadline, ConcurrencyPolicy: refreshschedule.ConcurrencyForbid,
				Schedules: []refreshschedule.Schedule{scheduled},
			}
			if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{
				Identity: testIdentity("prod", "generation_a"), ArtifactDigest: testArtifactDigest,
				Now: deployedAt, Pipelines: []refreshschedule.Definition{definition},
			}); err != nil {
				t.Fatal(err)
			}
			restartedAt := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
			if err := repo.Reconcile(t.Context(), refreshschedule.ReconcileInput{
				Identity: testIdentity("prod", "generation_a"), ArtifactDigest: testArtifactDigest,
				Now: restartedAt, Pipelines: []refreshschedule.Definition{definition},
			}); err != nil {
				t.Fatal(err)
			}
			due, err := repo.ClaimDue(t.Context(), testIdentity("prod", "generation_a"), restartedAt)
			if err != nil {
				t.Fatal(err)
			}
			if len(due) != 0 {
				t.Fatalf("ClaimDue() = %#v, want missed occurrence skipped", due)
			}
			next, ok, err := repo.NextRun(t.Context(), testIdentity("prod", "generation_a"), definition.ID)
			if err != nil || !ok || !next.Equal(time.Date(2026, 7, 19, 6, 0, 0, 0, time.UTC)) {
				t.Fatalf("next run=%s found=%v err=%v, want next scheduled day", next, ok, err)
			}
		})
	}
}
