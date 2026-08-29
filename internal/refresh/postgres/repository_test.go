package postgres

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/jackc/pgx/v5/pgxpool"
)

func refreshTestDB(t *testing.T) (*postgrestest.Database, *pgxpool.Pool) {
	t.Helper()
	h := postgrestest.Start(t)
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "refresh_runtime_password", Login: true})
	maintenance := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Password: "refresh_maintenance_password", Login: true})
	db := h.NewDatabase(t, "refresh_authority_test")
	h.GrantDatabase(t, db.Name, runtime, "CONNECT")
	h.GrantDatabase(t, db.Name, maintenance, "CONNECT")
	admin, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	tx, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return db, admin
}

func TestPostgresRefreshSchemaRollbackAndRoleBoundary(t *testing.T) {
	h := postgrestest.Start(t)
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "refresh_runtime_password", Login: true})
	backup := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup", Password: "refresh_backup_password", Login: true})
	maintenance := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Password: "refresh_maintenance_password", Login: true})
	db := h.NewDatabase(t, "refresh_schema_rollback_test")
	h.GrantDatabase(t, db.Name, runtime, "CONNECT")
	h.GrantDatabase(t, db.Name, backup, "CONNECT")
	h.GrantDatabase(t, db.Name, maintenance, "CONNECT")
	admin, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	tx, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := admin.QueryRow(t.Context(), `SELECT to_regclass('refresh.run') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("schema objects survived rollback")
	}
	tx, err = admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	user, err := pgxpool.New(t.Context(), db.URL(runtime))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(user.Close)
	if _, err := user.Exec(t.Context(), `DELETE FROM refresh.run`); err == nil {
		t.Fatal("runtime role unexpectedly received direct DELETE")
	}
	if _, err := user.Exec(t.Context(), `SELECT refresh.maintenance(1)`); err == nil {
		t.Fatal("runtime role unexpectedly received maintenance EXECUTE")
	}
	maint, err := pgxpool.New(t.Context(), db.URL(maintenance))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(maint.Close)
	var maintenanceCount int64
	if err := maint.QueryRow(t.Context(), `SELECT refresh.maintenance(1)`).Scan(&maintenanceCount); err != nil {
		t.Fatalf("maintenance role EXECUTE grant: %v", err)
	}
	if maintenanceCount != 0 {
		t.Fatalf("empty maintenance count = %d, want zero", maintenanceCount)
	}
	reader, err := pgxpool.New(t.Context(), db.URL(backup))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	var count int
	if err := reader.QueryRow(t.Context(), `SELECT count(*) FROM refresh.run`).Scan(&count); err != nil {
		t.Fatalf("backup SELECT grant: %v", err)
	}
}

func TestPostgresRefreshConcurrentOccurrenceClaimAndFence(t *testing.T) {
	_, admin := refreshTestDB(t)
	r := New(admin)
	now := time.Now().UTC()
	_, err := r.PutSchedule(t.Context(), ScheduleInput{ProjectID: "project_sales", Environment: "prod", PipelineID: "pipeline_sales", ScheduleID: "morning", SemanticModelID: "sales", GenerationID: "generation_1", ArtifactDigest: "sha256:" + strings.Repeat("a", 64), Cron: "0 6 * * *", Timezone: "UTC", ConcurrencyPolicy: "Forbid", StartingDeadline: time.Minute, ScheduleDigest: "sha256:" + strings.Repeat("b", 64), NextRunAt: now.Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	results := make([][]Occurrence, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = r.ClaimDue(t.Context(), Scope{ProjectID: "project_sales", Environment: "prod"}, now, "worker-"+string(rune('a'+i)), time.Minute, 1)
		}(i)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if len(results[0])+len(results[1]) != 1 {
		t.Fatalf("concurrent claims = %d, want one", len(results[0])+len(results[1]))
	}

	_, err = r.CreateRun(t.Context(), RunInput{RunID: "run_1", ProjectID: "project_sales", Environment: "prod", GenerationID: "generation_1", PipelineID: "pipeline_sales", SemanticModelID: "sales", TargetType: "refresh_pipeline", TargetID: "pipeline_sales", TriggerType: "manual", InvocationSource: "manual", PlanDigest: "sha256:" + strings.Repeat("c", 64), ArtifactDigest: "sha256:" + strings.Repeat("a", 64), PrincipalID: "principal"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.ClaimAttempt(t.Context(), "run_1", "worker-a", 1, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	// Let the short lease expire before exercising takeover; direct runtime
	// updates cannot fabricate an expired running lease.
	time.Sleep(25 * time.Millisecond)
	second, err := r.ClaimAttempt(t.Context(), "run_1", "worker-b", 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.FenceGeneration <= first.FenceGeneration {
		t.Fatal("fence did not advance")
	}
	if err := r.CompleteAttempt(t.Context(), "run_1", "worker-a", first.FenceGeneration, nil); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale completion = %v", err)
	}
}

func TestPostgresRefreshScheduleCatchupRetryAndDeadline(t *testing.T) {
	_, admin := refreshTestDB(t)
	r := New(admin)
	ctx := t.Context()
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	digestA := "sha256:" + strings.Repeat("a", 64)
	if _, err := r.PutSchedule(ctx, ScheduleInput{ProjectID: "p", Environment: "prod", PipelineID: "pipe", ScheduleID: "a", SemanticModelID: "m", GenerationID: "g1", ArtifactDigest: digestA, Cron: "*/5 * * * *", Timezone: "UTC", ConcurrencyPolicy: "Forbid", ScheduleDigest: "sha256:" + strings.Repeat("1", 64), NextRunAt: now.Add(-20 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PutSchedule(ctx, ScheduleInput{ProjectID: "p", Environment: "prod", PipelineID: "pipe", ScheduleID: "b", SemanticModelID: "m", GenerationID: "g1", ArtifactDigest: digestA, Cron: "*/5 * * * *", Timezone: "UTC", ConcurrencyPolicy: "Forbid", ScheduleDigest: "sha256:" + strings.Repeat("2", 64), NextRunAt: now.Add(-20 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	claimed, err := r.ClaimDue(ctx, Scope{ProjectID: "p", Environment: "prod", GenerationID: "g1"}, now, "scheduler", time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || !claimed[0].NominalTime.Equal(now) || len(claimed[0].MatchingScheduleIDs) != 2 {
		t.Fatalf("coalesced catch-up = %#v", claimed)
	}
	if err := r.ReleaseOccurrence(ctx, claimed[0]); err != nil {
		t.Fatal(err)
	}
	retry, err := r.ClaimDue(ctx, Scope{ProjectID: "p", Environment: "prod", GenerationID: "g1"}, now, "scheduler", time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(retry) != 1 || !retry[0].NominalTime.Equal(now) {
		t.Fatalf("retry occurrence = %#v", retry)
	}
	// Data-version admission is tied to the exact committed physical
	// publication and its current run fence.
	pubRun, err := r.CreateRun(ctx, RunInput{RunID: "version-run", ProjectID: "p", Environment: "prod", GenerationID: "g1", PipelineID: "pipe", SemanticModelID: "m", TargetType: "refresh_pipeline", TargetID: "pipe", TriggerType: "manual", InvocationSource: "manual", PlanDigest: "sha256:" + strings.Repeat("5", 64), ArtifactDigest: digestA, PrincipalID: "principal"})
	if err != nil {
		t.Fatal(err)
	}
	pubAttempt, err := r.ClaimAttempt(ctx, pubRun.RunID, "publisher", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	pubInput := PublicationInput{PublicationID: "version-publication", RunID: pubRun.RunID, GenerationID: "g1", PlanDigest: pubRun.PlanDigest, ArtifactDigest: digestA, PhysicalPoolID: "pool", CatalogID: "catalog", OwnerID: "publisher", FenceGeneration: pubAttempt.FenceGeneration, Evidence: []byte(`{"linked":true}`)}
	if _, err := r.LinkPublication(ctx, pubInput); err != nil {
		t.Fatal(err)
	}
	if err := r.CommitPublication(ctx, pubInput.PublicationID, pubInput.RunID, pubInput.OwnerID, pubInput.FenceGeneration, 1, []byte(`{"committed":true}`), pubInput.PhysicalPoolID, pubInput.CatalogID); err != nil {
		t.Fatal(err)
	}
	v := DataVersion{ProjectID: "p", Environment: "prod", SemanticModelID: "m", GenerationID: "g1", SnapshotID: 1, Source: "refresh", PhysicalPoolID: "pool", CatalogID: "catalog", RunID: pubRun.RunID, LeaseOwner: "publisher", LeaseRevision: 1}
	if err := r.SaveDataVersion(ctx, v); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveDataVersion(ctx, v); err != nil {
		t.Fatal("equal data-version replay:", err)
	}
	v.SnapshotID = 2
	if err := r.SaveDataVersion(ctx, v); !errors.Is(err, ErrConflict) {
		t.Fatalf("equal-fence data-version conflict=%v", err)
	}
	v.LeaseRevision = 2
	if err := r.SaveDataVersion(ctx, v); err == nil {
		t.Fatal("forged greater-fence data-version unexpectedly accepted")
	}
	if _, err := r.CreateRun(ctx, RunInput{RunID: "recovery-run", ProjectID: "p", Environment: "prod", GenerationID: "g1", PipelineID: "pipe", SemanticModelID: "m", TargetType: "refresh_pipeline", TargetID: "recovery-pipe", TriggerType: "manual", InvocationSource: "manual", PlanDigest: "sha256:" + strings.Repeat("4", 64), ArtifactDigest: digestA, PrincipalID: "principal"}); err != nil {
		t.Fatal(err)
	}
	if attempt, err := r.ClaimAttempt(ctx, "recovery-run", "reconciler", 1, time.Minute); err != nil {
		t.Fatal(err)
	} else if err := r.FailAttempt(ctx, "recovery-run", "reconciler", attempt.FenceGeneration, "uncertain", []byte(`{"error":"uncertain"}`)); err != nil {
		t.Fatal(err)
	}
	rec := RecoveryInput{RunID: "recovery-run", OwnerID: "reconciler", FenceGeneration: 1, Lease: time.Minute, State: "reconciled", ExactExternalIdentity: "attempt-1", Evidence: []byte(`{"snapshot":1}`)}
	if _, err := r.RecordRecovery(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RecordRecovery(ctx, rec); err != nil {
		t.Fatal("equal recovery replay:", err)
	}
	rec.Evidence = []byte(`{"snapshot":2}`)
	if _, err := r.RecordRecovery(ctx, rec); !errors.Is(err, ErrConflict) {
		t.Fatalf("equal recovery conflict=%v", err)
	}
	rec.FenceGeneration = 0
	if _, err := r.RecordRecovery(ctx, rec); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale recovery error=%v, want ErrStaleFence", err)
	}

	deadline := now.Add(-24 * time.Hour)
	if _, err := r.PutSchedule(ctx, ScheduleInput{ProjectID: "p", Environment: "prod", PipelineID: "late", ScheduleID: "daily", SemanticModelID: "m", GenerationID: "g1", ArtifactDigest: digestA, Cron: "0 0 * * *", Timezone: "UTC", ConcurrencyPolicy: "Forbid", StartingDeadline: time.Minute, ScheduleDigest: "sha256:" + strings.Repeat("3", 64), NextRunAt: deadline}); err != nil {
		t.Fatal(err)
	}
	if got, err := r.ClaimDue(ctx, Scope{ProjectID: "p", Environment: "prod", GenerationID: "g1"}, now, "scheduler", time.Minute, 10); err != nil {
		t.Fatal(err)
	} else if len(got) != 0 {
		t.Fatalf("expired schedule claimed = %#v", got)
	}
}

func TestPostgresRefreshScheduleDSTUsesArgoCronSemantics(t *testing.T) {
	s, err := refreshschedule.ParseSchedule("30 2 * * *", "America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-03-08 02:30 local does not exist; Next must skip to Monday's
	// 02:30 (06:30 UTC) rather than fabricating an instant in the gap.
	next := s.Next(time.Date(2026, 3, 8, 6, 0, 0, 0, time.UTC))
	want := time.Date(2026, 3, 9, 6, 30, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("DST next=%s want %s", next, want)
	}
}

func TestPostgresRefreshScheduleClosePreservesNextRun(t *testing.T) {
	_, admin := refreshTestDB(t)
	r := New(admin)
	ctx := t.Context()
	digest := "sha256:" + strings.Repeat("a", 64)
	first, err := r.PutSchedule(ctx, ScheduleInput{ProjectID: "p", Environment: "prod", PipelineID: "close-pipe", ScheduleID: "close", SemanticModelID: "m", GenerationID: "g", ArtifactDigest: digest, Cron: "0 * * * *", Timezone: "UTC", ConcurrencyPolicy: "Forbid", ScheduleDigest: "sha256:" + strings.Repeat("1", 64), NextRunAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.PutSchedule(ctx, ScheduleInput{ProjectID: "p", Environment: "prod", PipelineID: "close-pipe", ScheduleID: "close", SemanticModelID: "m", GenerationID: "g", ArtifactDigest: digest, Cron: "0 * * * *", Timezone: "UTC", ConcurrencyPolicy: "Forbid", ScheduleDigest: "sha256:" + strings.Repeat("2", 64), NextRunAt: first.NextRunAt.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var next time.Time
	if err := admin.QueryRow(ctx, `SELECT next_run_at FROM refresh.schedule_revision WHERE schedule_revision_id=$1`, first.ScheduleRevisionID).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if !next.Equal(first.NextRunAt) {
		t.Fatalf("closed schedule next_run_at changed: got %s want %s", next, first.NextRunAt)
	}
	if _, err := admin.Exec(ctx, `UPDATE refresh.schedule_revision SET next_run_at=next_run_at+interval '1 hour' WHERE schedule_revision_id=$1`, first.ScheduleRevisionID); err == nil {
		t.Fatal("closed schedule next_run_at mutation unexpectedly succeeded")
	}
}

func TestPostgresRefreshPublicationPhysicalFence(t *testing.T) {
	_, admin := refreshTestDB(t)
	r := New(admin)
	ctx := t.Context()
	digestA := "sha256:" + strings.Repeat("a", 64)
	digestP := "sha256:" + strings.Repeat("b", 64)
	if _, err := r.CreateRun(ctx, RunInput{RunID: "pub-run", ProjectID: "p", Environment: "prod", GenerationID: "g", PipelineID: "pipe", SemanticModelID: "m", TargetType: "refresh_pipeline", TargetID: "pipe", TriggerType: "manual", InvocationSource: "manual", PlanDigest: digestP, ArtifactDigest: digestA, PrincipalID: "principal"}); err != nil {
		t.Fatal(err)
	}
	attempt, err := r.ClaimAttempt(ctx, "pub-run", "worker", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	p := PublicationInput{PublicationID: "publication-1", RunID: "pub-run", GenerationID: "g", PlanDigest: digestP, ArtifactDigest: digestA, PhysicalPoolID: "pool", CatalogID: "catalog", OwnerID: "worker", FenceGeneration: attempt.FenceGeneration, Evidence: []byte(`{"linked":true}`)}
	if _, err := r.LinkPublication(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := r.CommitPublication(ctx, p.PublicationID, p.RunID, p.OwnerID, p.FenceGeneration, 42, []byte(`{"ok":true}`), "other-pool", "catalog"); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("wrong physical tuple commit=%v", err)
	}
	if err := r.CommitPublication(ctx, p.PublicationID, p.RunID, p.OwnerID, p.FenceGeneration, 42, []byte(`{"ok":true}`), p.PhysicalPoolID, p.CatalogID); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresRefreshDirectLifecycleGuardsAndMaintenanceBudget(t *testing.T) {
	db, admin := refreshTestDB(t)
	maintenance, err := pgxpool.New(t.Context(), db.URL(postgrestest.Role{Name: "leapview_control_maintenance", Password: "refresh_maintenance_password", Login: true}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(maintenance.Close)
	r := New(admin)
	ctx := t.Context()
	digest := "sha256:" + strings.Repeat("a", 64)
	if _, err := r.CreateRun(ctx, RunInput{RunID: "guard-run", ProjectID: "p", Environment: "prod", GenerationID: "g", PipelineID: "pipe", SemanticModelID: "m", TargetType: "refresh_pipeline", TargetID: "guard-target", TriggerType: "manual", InvocationSource: "manual", PlanDigest: digest, ArtifactDigest: digest, PrincipalID: "principal"}); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO refresh.attempt(run_id,attempt_number,fence_generation,owner_id,lease_expires_at,status,evidence) VALUES ('guard-run',1,1,'forged',clock_timestamp()+interval '1 minute','succeeded','{"forged":true}')`); err == nil {
		t.Fatal("forged terminal attempt INSERT unexpectedly succeeded")
	}
	if _, err := admin.Exec(ctx, `UPDATE refresh.run SET status='running' WHERE run_id='guard-run'`); err == nil {
		t.Fatal("unfenced queued run transition unexpectedly succeeded")
	}
	if _, _, err := r.ReserveOperation(ctx, OperationInput{ProjectID: "p", Environment: "prod", IdempotencyKey: "guard-op", RequestDigest: digest, OperationType: "refresh", OwnerID: "owner", Lease: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE refresh.operation SET owner_id='forged' WHERE project_id='p' AND idempotency_key='guard-op'`); err == nil {
		t.Fatal("same-fence operation owner mutation unexpectedly succeeded")
	}
	if _, err := admin.Exec(ctx, `INSERT INTO refresh.publication_link(publication_id,run_id,generation_id,plan_digest,artifact_digest,physical_pool_id,catalog_id,fence_generation,owner_id,evidence) VALUES ('forged-pub','guard-run','g', $1,$1,'pool','catalog',1,'owner','{"linked":true}')`, digest); err == nil {
		t.Fatal("fabricated publication INSERT unexpectedly succeeded")
	}
	if _, err := admin.Exec(ctx, `INSERT INTO refresh.recovery_state(run_id,state,reconciliation_fence,owner_id,evidence) VALUES ('guard-run','reconciled',1,'owner','{}')`); err == nil {
		t.Fatal("empty fenced recovery INSERT unexpectedly succeeded")
	}
	if _, err := admin.Exec(ctx, `INSERT INTO refresh.data_version(project_id,environment,semantic_model_id,generation_id,snapshot_id,source,physical_pool_id,catalog_id,run_id,lease_owner,lease_revision) VALUES ('p','prod','m','g',1,'refresh','pool','catalog','guard-run','owner',1)`); err == nil {
		t.Fatal("unpublished data-version INSERT unexpectedly succeeded")
	}
	if _, err := r.CreateRun(ctx, RunInput{RunID: "recovery-guard", ProjectID: "p", Environment: "prod", GenerationID: "g", PipelineID: "pipe", SemanticModelID: "m", TargetType: "model_table", TargetID: "recovery-guard", TriggerType: "manual", InvocationSource: "manual", PlanDigest: digest, ArtifactDigest: digest, PrincipalID: "principal"}); err != nil {
		t.Fatal(err)
	}
	if attempt, err := r.ClaimAttempt(ctx, "recovery-guard", "recovery-owner", 1, time.Minute); err != nil {
		t.Fatal(err)
	} else if err := r.FailAttempt(ctx, "recovery-guard", "recovery-owner", attempt.FenceGeneration, "uncertain", []byte(`{"error":"uncertain"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RecordRecovery(ctx, RecoveryInput{RunID: "recovery-guard", OwnerID: "recovery-owner", Lease: time.Minute, FenceGeneration: 1, State: "reconciled", ExactExternalIdentity: "external-1", Evidence: []byte(`{"checked":true}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE refresh.recovery_state SET reconciliation_fence=2,owner_id='forged',lease_expires_at=clock_timestamp()+interval '1 minute' WHERE run_id='recovery-guard'`); err == nil {
		t.Fatal("live-owner recovery takeover unexpectedly succeeded")
	}

	for _, id := range []string{"maintenance-a", "maintenance-b"} {
		if _, err := r.CreateRun(ctx, RunInput{RunID: id, ProjectID: "p", Environment: "prod", GenerationID: "g", PipelineID: "pipe", SemanticModelID: "m", TargetType: "model_table", TargetID: id, TriggerType: "manual", InvocationSource: "manual", PlanDigest: digest, ArtifactDigest: digest, PrincipalID: "principal"}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.ClaimAttempt(ctx, id, "worker-"+id, 1, 100*time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.Exec(ctx, `UPDATE refresh.run SET status='prepared' WHERE run_id='maintenance-a'`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	var n int64
	err = maintenance.QueryRow(ctx, `SELECT refresh.maintenance(1)`).Scan(&n)
	if err != nil || n != 1 {
		t.Fatalf("maintenance first budget = %d, %v; want one affected row", n, err)
	}
	err = maintenance.QueryRow(ctx, `SELECT refresh.maintenance(10)`).Scan(&n)
	if err != nil || n < 3 {
		t.Fatalf("maintenance remainder = %d, %v; want remaining attempt plus runs", n, err)
	}
}
