package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	jobpolicy "github.com/flidai/leapview/internal/platform/jobs"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	if err := jobspostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
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

func TestRepositoryConfiguredRejectsTypedNilDBTX(t *testing.T) {
	var db *typedNilDBTX
	if refresh := New(db); refresh.Configured() {
		t.Fatal("refresh repository accepted a typed-nil DBTX")
	}
}

type typedNilDBTX struct{}

func (*typedNilDBTX) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (*typedNilDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }

func (*typedNilDBTX) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func seedRefreshJob(t *testing.T, db *pgxpool.Pool, id, runID, project, environment, principal string) {
	t.Helper()
	if _, err := jobspostgres.New(db).Enqueue(t.Context(), jobs.EnqueueInput{ID: id, Kind: "refresh_pipeline", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: principal, PartitionKey: "refresh:" + project + ":" + environment, ResourceKind: "refresh_run", ResourceID: runID, EstimatedMemoryBytes: 1, Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresRefreshSchemaRollbackAndRoleBoundary(t *testing.T) {
	h := postgrestest.Start(t)
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "refresh_runtime_password", Login: true})
	backup := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup", Password: "refresh_backup_password", Login: true})
	readonly := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly", Password: "refresh_readonly_password", Login: true})
	maintenance := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Password: "refresh_maintenance_password", Login: true})
	db := h.NewDatabase(t, "refresh_schema_rollback_test")
	h.GrantDatabase(t, db.Name, runtime, "CONNECT")
	h.GrantDatabase(t, db.Name, backup, "CONNECT")
	h.GrantDatabase(t, db.Name, readonly, "CONNECT")
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
	if err := jobspostgres.ApplySchema(t.Context(), tx); err != nil {
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
	if err := jobspostgres.ApplySchema(t.Context(), tx); err != nil {
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
	if _, err := user.Exec(t.Context(), `SELECT refresh.fail_child_runs('missing-run','test')`); err != nil {
		t.Fatalf("runtime role cannot execute guarded child transition: %v", err)
	}
	if _, err := user.Exec(t.Context(), `SELECT refresh.complete_child_runs('missing-run')`); err != nil {
		t.Fatalf("runtime role cannot execute guarded child completion: %v", err)
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
	if _, err := maint.Exec(t.Context(), `SELECT refresh.fail_child_runs('missing-run','test')`); err == nil {
		t.Fatal("maintenance role unexpectedly received child transition EXECUTE")
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
	if _, err := reader.Exec(t.Context(), `SELECT refresh.complete_child_runs('missing-run')`); err == nil {
		t.Fatal("backup role unexpectedly received child transition EXECUTE")
	}
	readOnlyConn, err := pgxpool.New(t.Context(), db.URL(readonly))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(readOnlyConn.Close)
	if _, err := readOnlyConn.Exec(t.Context(), `SELECT refresh.fail_child_runs('missing-run','test')`); err == nil {
		t.Fatal("readonly role unexpectedly received child transition EXECUTE")
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

	seedRefreshJob(t, admin, "job-run-1", "run_1", "project_sales", "prod", "principal")
	_, err = r.CreateRun(t.Context(), RunInput{RunID: "run_1", ProjectID: "project_sales", Environment: "prod", GenerationID: "generation_1", PipelineID: "pipeline_sales", SemanticModelID: "sales", TargetType: "refresh_pipeline", TargetID: "pipeline_sales", TriggerType: "manual", InvocationSource: "manual", PlanDigest: "sha256:" + strings.Repeat("c", 64), ArtifactDigest: "sha256:" + strings.Repeat("a", 64), PrincipalID: "principal", JobID: "job-run-1"})
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

func TestPostgresRefreshStandaloneRootRequiresCanonicalJob(t *testing.T) {
	_, admin := refreshTestDB(t)
	r := New(admin)
	ctx := t.Context()
	digest := "sha256:" + strings.Repeat("a", 64)
	input := RunInput{RunID: "standalone-root-authority", ProjectID: "standalone-project", Environment: "prod", GenerationID: "generation-1", PipelineID: "pipeline", SemanticModelID: "semantic", TargetType: "refresh_pipeline", TargetID: "pipeline", TriggerType: "manual", InvocationSource: "manual", PlanDigest: digest, ArtifactDigest: digest, PrincipalID: "principal"}
	if _, err := r.CreateRun(ctx, input); err == nil {
		t.Fatal("standalone root admission unexpectedly succeeded")
	}
	var count int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM refresh.run WHERE run_id=$1`, input.RunID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("standalone root survived failed commit: count=%d", count)
	}

	input.RunID = "standalone-tree-root"
	child := input
	child.RunID = "standalone-tree-child"
	child.ParentRunID = input.RunID
	child.TargetType = "model_table"
	child.TargetID = "model"
	child.TriggerType = "dependency"
	child.InvocationSource = "dependency"
	if _, _, err := r.CreateRunTreeWithSupersedeHook(ctx, input, []RunInput{child}, "", "", 0, nil, nil); err == nil {
		t.Fatal("standalone tree admission unexpectedly succeeded")
	}
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM refresh.run WHERE run_id IN ($1,$2)`, input.RunID, child.RunID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("standalone tree survived failed commit: count=%d", count)
	}
}

func TestPostgresRefreshRootJobPairingAndParentScopeGuards(t *testing.T) {
	_, admin := refreshTestDB(t)
	r := New(admin)
	ctx := t.Context()
	digest := "sha256:" + strings.Repeat("a", 64)
	base := RunInput{ProjectID: "pairing-project", Environment: "prod", GenerationID: "generation-pairing", PipelineID: "pipeline-pairing", SemanticModelID: "semantic-pairing", TargetType: "refresh_pipeline", TargetID: "pipeline-pairing", TriggerType: "manual", InvocationSource: "manual", PlanDigest: digest, ArtifactDigest: digest, PrincipalID: "pairing-principal"}
	cases := []struct {
		name string
		job  jobs.EnqueueInput
	}{
		{name: "kind", job: jobs.EnqueueInput{ID: "pairing-wrong-kind", Kind: "other", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: base.PrincipalID, PartitionKey: "refresh:" + base.ProjectID + ":" + base.Environment, ResourceKind: "refresh_run", ResourceID: "pairing-wrong-kind", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)}},
		{name: "workload", job: jobs.EnqueueInput{ID: "pairing-wrong-workload", Kind: "refresh_pipeline", WorkloadClass: jobpolicy.WorkloadClassControl, PrincipalID: base.PrincipalID, PartitionKey: "refresh:" + base.ProjectID + ":" + base.Environment, ResourceKind: "refresh_run", ResourceID: "pairing-wrong-workload", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)}},
		{name: "resource-kind", job: jobs.EnqueueInput{ID: "pairing-wrong-resource-kind", Kind: "refresh_pipeline", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: base.PrincipalID, PartitionKey: "refresh:" + base.ProjectID + ":" + base.Environment, ResourceKind: "other", ResourceID: "pairing-wrong-resource-kind", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)}},
		{name: "resource-id", job: jobs.EnqueueInput{ID: "pairing-wrong-resource-id", Kind: "refresh_pipeline", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: base.PrincipalID, PartitionKey: "refresh:" + base.ProjectID + ":" + base.Environment, ResourceKind: "refresh_run", ResourceID: "different-run", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)}},
		{name: "partition", job: jobs.EnqueueInput{ID: "pairing-wrong-partition", Kind: "refresh_pipeline", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: base.PrincipalID, PartitionKey: "refresh:other:prod", ResourceKind: "refresh_run", ResourceID: "pairing-wrong-partition", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)}},
		{name: "principal", job: jobs.EnqueueInput{ID: "pairing-wrong-principal", Kind: "refresh_pipeline", WorkloadClass: jobpolicy.WorkloadClassBackground, PrincipalID: "other-principal", PartitionKey: "refresh:" + base.ProjectID + ":" + base.Environment, ResourceKind: "refresh_run", ResourceID: "pairing-wrong-principal", EstimatedMemoryBytes: 1, Payload: []byte(`{}`)}},
	}
	jobsRepo := jobspostgres.New(admin)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := jobsRepo.Enqueue(ctx, tc.job); err != nil {
				t.Fatal(err)
			}
			input := base
			input.RunID = "run-" + tc.name
			input.JobID = tc.job.ID
			if _, err := r.CreateRun(ctx, input); err == nil {
				t.Fatal("run with mismatched canonical job unexpectedly committed")
			}
		})
	}

	parent := base
	parent.RunID = "parent-scope-root"
	parent.JobID = "parent-scope-job"
	seedRefreshJob(t, admin, parent.JobID, parent.RunID, parent.ProjectID, parent.Environment, parent.PrincipalID)
	if _, err := r.CreateRun(ctx, parent); err != nil {
		t.Fatal(err)
	}
	child := base
	child.RunID = "orphan-child"
	child.ParentRunID = "missing-parent"
	child.JobID = ""
	child.TargetType = "model_table"
	child.TargetID = "child"
	child.TriggerType = "dependency"
	child.InvocationSource = "dependency"
	if _, err := r.CreateRun(ctx, child); err == nil {
		t.Fatal("child with missing parent unexpectedly committed")
	}
	child.RunID = "cross-scope-child"
	child.ParentRunID = parent.RunID
	child.ProjectID = "other-project"
	if _, err := r.CreateRun(ctx, child); err == nil {
		t.Fatal("child with cross-scope parent unexpectedly committed")
	}
	child.RunID = "self-parent-child"
	child.ParentRunID = child.RunID
	child.ProjectID = parent.ProjectID
	if _, err := r.CreateRun(ctx, child); err == nil {
		t.Fatal("self-parented child unexpectedly committed")
	}
}

func TestPostgresRefreshExactJobAttachmentReplayAfterQueueAdvance(t *testing.T) {
	_, admin := refreshTestDB(t)
	r := New(admin)
	jobsRepo := jobspostgres.New(admin)
	ctx := t.Context()
	digest := "sha256:" + strings.Repeat("a", 64)
	seedRefreshJob(t, admin, "advanced-replay-job", "advanced-replay-run", "advanced-project", "prod", "advanced-principal")
	input := RunInput{RunID: "advanced-replay-run", ProjectID: "advanced-project", Environment: "prod", GenerationID: "generation", PipelineID: "pipeline", SemanticModelID: "semantic", TargetType: "refresh_pipeline", TargetID: "pipeline", TriggerType: "manual", InvocationSource: "manual", PlanDigest: digest, ArtifactDigest: digest, PrincipalID: "advanced-principal", JobID: "advanced-replay-job"}
	if _, err := r.CreateRun(ctx, input); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := jobsRepo.ClaimByID(ctx, input.JobID, jobpolicy.WorkloadClassBackground, "advanced-replay-worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("advance canonical queue job: ok=%v err=%v", ok, err)
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.AttachJobTx(ctx, tx, input.RunID, input.JobID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("exact advanced attachment replay: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if claimed.Status != jobs.StatusRunning {
		t.Fatalf("advanced queue status=%q, want running", claimed.Status)
	}
}

func TestPostgresRefreshConcurrentAdmissionSerializesEmptyTargetSet(t *testing.T) {
	_, admin := refreshTestDB(t)
	r := New(admin)
	ctx := t.Context()
	base := RunInput{
		ProjectID: "project_admission", Environment: "prod", GenerationID: "generation_1",
		PipelineID: "pipeline_admission", SemanticModelID: "semantic_admission",
		TargetType: "refresh_pipeline", TargetID: "pipeline_admission",
		TriggerType: "manual", InvocationSource: "manual",
		PlanDigest: "sha256:" + strings.Repeat("a", 64), ArtifactDigest: "sha256:" + strings.Repeat("b", 64), PrincipalID: "principal", TargetRevision: 1,
	}
	start := make(chan struct{})
	type result struct {
		run Run
		err error
	}
	results := make(chan result, 2)
	for _, id := range []string{"run-admission-a", "run-admission-b"} {
		id := id
		seedRefreshJob(t, admin, "job-admission-"+id, id, base.ProjectID, base.Environment, base.PrincipalID)
		go func() {
			<-start
			in := base
			in.RunID = id
			in.JobID = "job-admission-" + id
			run, err := r.CreateRun(ctx, in)
			results <- result{run: run, err: err}
		}()
	}
	close(start)
	var successes, conflicts int
	for range 2 {
		got := <-results
		if got.err == nil {
			successes++
		} else if errors.Is(got.err, ErrConflict) {
			conflicts++
		} else {
			t.Fatalf("concurrent admission error = %v", got.err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent admission successes=%d conflicts=%d, want one each", successes, conflicts)
	}
}

func TestPostgresRefreshChildTransitionFunctionSerializesConcurrentCallers(t *testing.T) {
	_, admin := refreshTestDB(t)
	r := New(admin)
	ctx := t.Context()
	digest := "sha256:" + strings.Repeat("e", 64)
	rootID, childID, jobID := "function-root", "function-child", "function-job"
	seedRefreshJob(t, admin, jobID, rootID, "function-project", "prod", "function-principal")
	root := RunInput{RunID: rootID, ProjectID: "function-project", Environment: "prod", GenerationID: "function-generation", PipelineID: "function-pipeline", SemanticModelID: "function-semantic", TargetType: "refresh_pipeline", TargetID: "function-pipeline", TriggerType: "manual", InvocationSource: "manual", PlanDigest: digest, ArtifactDigest: digest, PrincipalID: "function-principal", JobID: jobID}
	child := RunInput{RunID: childID, ProjectID: root.ProjectID, Environment: root.Environment, GenerationID: root.GenerationID, PipelineID: root.PipelineID, SemanticModelID: root.SemanticModelID, TargetType: "model_table", TargetID: "function-model", TriggerType: "dependency", InvocationSource: "dependency", PlanDigest: digest, ArtifactDigest: digest, PrincipalID: root.PrincipalID, ParentRunID: rootID}
	if _, _, err := r.CreateRunTreeWithSupersedeHook(ctx, root, []RunInput{child}, "", "", 0, nil, nil); err != nil {
		t.Fatal(err)
	}
	type result struct {
		affected int64
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			tx, err := admin.Begin(ctx)
			if err != nil {
				results <- result{err: err}
				return
			}
			var affected int64
			err = tx.QueryRow(ctx, `SELECT refresh.fail_child_runs($1,$2)`, rootID, "concurrent child failure").Scan(&affected)
			if err == nil {
				err = tx.Commit(ctx)
			} else {
				_ = tx.Rollback(ctx)
			}
			results <- result{affected: affected, err: err}
		}()
	}
	close(start)
	var affected []int64
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		affected = append(affected, got.affected)
	}
	if !((affected[0] == 1 && affected[1] == 0) || (affected[0] == 0 && affected[1] == 1)) {
		t.Fatalf("concurrent child function counts=%v, want one update and one no-op", affected)
	}
	var status string
	if err := admin.QueryRow(ctx, `SELECT status FROM refresh.run WHERE run_id=$1`, childID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("child status=%q, want failed", status)
	}
}

func TestPostgresScheduledReplaceRequiresJobsSupersessionHook(t *testing.T) {
	_, admin := refreshTestDB(t)
	r := New(admin)
	digest := "sha256:" + strings.Repeat("a", 64)
	first := RunInput{RunID: "replace-first", ProjectID: "replace-project", Environment: "prod", GenerationID: "generation-replace", PipelineID: "pipeline-replace", SemanticModelID: "semantic-replace", TargetType: "refresh_pipeline", TargetID: "pipeline-replace", TriggerType: "schedule", InvocationSource: "schedule", MatchingScheduleIDs: []string{"daily"}, ConcurrencyPolicy: "Replace", NominalTime: time.Now().UTC().Truncate(time.Microsecond), PlanDigest: digest, ArtifactDigest: digest, PrincipalID: "principal-replace", JobID: "job-replace-first"}
	seedRefreshJob(t, admin, first.JobID, first.RunID, first.ProjectID, first.Environment, first.PrincipalID)
	if _, err := r.CreateRun(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.RunID = "replace-second"
	second.NominalTime = time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	tx, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateRunTxWithSupersedeHook(t.Context(), tx, second, nil); err == nil {
		_ = tx.Rollback(t.Context())
		t.Fatal("scheduled replacement committed without jobs supersession hook")
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	stored, err := r.LookupRun(t.Context(), first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "queued" {
		t.Fatalf("first run status=%q, supersession was not rolled back", stored.Status)
	}
}

func TestPostgresOccurrenceTerminalReconcileRejectsContradiction(t *testing.T) {
	_, admin := refreshTestDB(t)
	r := New(admin)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	digest := "sha256:" + strings.Repeat("a", 64)
	if _, err := r.PutSchedule(ctx, ScheduleInput{ProjectID: "project_occurrence_reconcile", Environment: "prod", PipelineID: "pipeline_occurrence_reconcile", ScheduleID: "daily", SemanticModelID: "semantic_occurrence_reconcile", GenerationID: "generation_occurrence_reconcile", ArtifactDigest: digest, Cron: "* * * * *", Timezone: "UTC", ConcurrencyPolicy: "Forbid", StartingDeadline: time.Hour, ScheduleDigest: "sha256:" + strings.Repeat("b", 64), NextRunAt: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	claimed, err := r.ClaimDue(ctx, Scope{ProjectID: "project_occurrence_reconcile", Environment: "prod", GenerationID: "generation_occurrence_reconcile"}, now, "scheduler-reconcile", time.Minute, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed occurrence=%#v err=%v", claimed, err)
	}
	o := claimed[0]
	seedRefreshJob(t, admin, "job-occurrence-reconcile", "occurrence-reconcile-run", o.ProjectID, o.Environment, "principal:occurrence-reconcile")
	root, _, err := r.CreateRunTreeWithSupersedeHook(ctx, RunInput{RunID: "occurrence-reconcile-run", ProjectID: o.ProjectID, Environment: o.Environment, GenerationID: o.GenerationID, PipelineID: o.PipelineID, SemanticModelID: o.SemanticModelID, TargetType: "refresh_pipeline", TargetID: o.PipelineID, TriggerType: "schedule", InvocationSource: "schedule", MatchingScheduleIDs: o.MatchingScheduleIDs, ScheduleRevisionID: o.ScheduleRevisionID, OccurrenceID: o.OccurrenceID, NominalTime: o.NominalTime, ConcurrencyPolicy: "Forbid", PlanDigest: digest, ArtifactDigest: digest, PrincipalID: "principal:occurrence-reconcile"}, nil, o.OccurrenceID, o.LeaseOwner, o.FenceGeneration, func(context.Context, Tx, Run) (string, error) { return "job-occurrence-reconcile", nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := r.ClaimAttempt(ctx, root.RunID, "worker-occurrence-reconcile", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CompleteAttempt(ctx, root.RunID, attempt.OwnerID, attempt.FenceGeneration, json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ReconcileOccurrenceTerminalTx(ctx, tx, root.RunID, "succeeded", json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err = admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = r.ReconcileOccurrenceTerminalTx(ctx, tx, root.RunID, "failed", json.RawMessage(`{"error":"contradiction"}`))
	_ = tx.Rollback(ctx)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched terminal reconciliation error=%v, want conflict", err)
	}
}

func TestPostgresOccurrenceGuardRejectsTamperedLifecycle(t *testing.T) {
	_, admin := refreshTestDB(t)
	r := New(admin)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	digest := "sha256:" + strings.Repeat("c", 64)
	if _, err := r.PutSchedule(ctx, ScheduleInput{ProjectID: "project_occurrence_guard", Environment: "prod", PipelineID: "pipeline_occurrence_guard", ScheduleID: "daily", SemanticModelID: "semantic_occurrence_guard", GenerationID: "generation_occurrence_guard", ArtifactDigest: digest, Cron: "* * * * *", Timezone: "UTC", ConcurrencyPolicy: "Forbid", StartingDeadline: time.Hour, ScheduleDigest: "sha256:" + strings.Repeat("d", 64), NextRunAt: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	claimed, err := r.ClaimDue(ctx, Scope{ProjectID: "project_occurrence_guard", Environment: "prod", GenerationID: "generation_occurrence_guard"}, now, "scheduler-guard", time.Minute, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed occurrence=%#v err=%v", claimed, err)
	}
	o := claimed[0]
	if _, err := admin.Exec(ctx, `UPDATE refresh.schedule_occurrence SET finished_at=clock_timestamp() WHERE occurrence_id=$1`, o.OccurrenceID); err == nil {
		t.Fatal("claimed occurrence accepted finished timestamp")
	}
	if _, err := admin.Exec(ctx, `UPDATE refresh.schedule_occurrence SET status='succeeded' WHERE occurrence_id=$1`, o.OccurrenceID); err == nil {
		t.Fatal("claimed occurrence accepted terminal transition without run binding")
	}
	if err := r.ReleaseOccurrence(ctx, o); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE refresh.schedule_occurrence SET status='skipped' WHERE occurrence_id=$1`, o.OccurrenceID); err == nil {
		t.Fatal("pending occurrence accepted terminal transition without evidence")
	}
	if _, err := admin.Exec(ctx, `UPDATE refresh.schedule_occurrence SET status='skipped',outcome='{"reason":"test"}'::jsonb,finished_at=clock_timestamp() WHERE occurrence_id=$1`, o.OccurrenceID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE refresh.schedule_occurrence SET outcome='{"reason":"tampered"}'::jsonb WHERE occurrence_id=$1`, o.OccurrenceID); err == nil {
		t.Fatal("terminal occurrence accepted outcome mutation")
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
	seedRefreshJob(t, admin, "job-version-run", "version-run", "p", "prod", "principal")
	pubRun, err := r.CreateRun(ctx, RunInput{RunID: "version-run", ProjectID: "p", Environment: "prod", GenerationID: "g1", PipelineID: "pipe", SemanticModelID: "m", TargetType: "refresh_pipeline", TargetID: "pipe", TriggerType: "manual", InvocationSource: "manual", PlanDigest: "sha256:" + strings.Repeat("5", 64), ArtifactDigest: digestA, PrincipalID: "principal", JobID: "job-version-run"})
	if err != nil {
		t.Fatal(err)
	}
	pubAttempt, err := r.ClaimAttempt(ctx, pubRun.RunID, "publisher", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	pubInput := PublicationInput{PublicationID: "version-publication", RunID: pubRun.RunID, BaseGenerationID: "g1", ResultGenerationID: "g1", ExpectedTargetRevision: 1, ResultTargetRevision: 2, PlanDigest: pubRun.PlanDigest, ArtifactDigest: digestA, PhysicalPoolID: "pool", CatalogID: "catalog", OwnerID: "publisher", FenceGeneration: pubAttempt.FenceGeneration, Evidence: []byte(`{"linked":true}`)}
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
	seedRefreshJob(t, admin, "job-recovery-run", "recovery-run", "p", "prod", "principal")
	if _, err := r.CreateRun(ctx, RunInput{RunID: "recovery-run", ProjectID: "p", Environment: "prod", GenerationID: "g1", PipelineID: "pipe", SemanticModelID: "m", TargetType: "refresh_pipeline", TargetID: "recovery-pipe", TriggerType: "manual", InvocationSource: "manual", PlanDigest: "sha256:" + strings.Repeat("4", 64), ArtifactDigest: digestA, PrincipalID: "principal", JobID: "job-recovery-run"}); err != nil {
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
	seedRefreshJob(t, admin, "job-pub-run", "pub-run", "p", "prod", "principal")
	if _, err := r.CreateRun(ctx, RunInput{RunID: "pub-run", ProjectID: "p", Environment: "prod", GenerationID: "g", PipelineID: "pipe", SemanticModelID: "m", TargetType: "refresh_pipeline", TargetID: "pipe", TriggerType: "manual", InvocationSource: "manual", PlanDigest: digestP, ArtifactDigest: digestA, PrincipalID: "principal", JobID: "job-pub-run"}); err != nil {
		t.Fatal(err)
	}
	attempt, err := r.ClaimAttempt(ctx, "pub-run", "worker", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	p := PublicationInput{PublicationID: "publication-1", RunID: "pub-run", BaseGenerationID: "g", ResultGenerationID: "g", ExpectedTargetRevision: 1, ResultTargetRevision: 2, PlanDigest: digestP, ArtifactDigest: digestA, PhysicalPoolID: "pool", CatalogID: "catalog", OwnerID: "worker", FenceGeneration: attempt.FenceGeneration, Evidence: []byte(`{"linked":true}`)}
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

func TestPostgresRefreshConcurrentLinkReplayAfterCommit(t *testing.T) {
	_, admin := refreshTestDB(t)
	r := New(admin)
	ctx := t.Context()
	digest := "sha256:" + strings.Repeat("a", 64)
	seedRefreshJob(t, admin, "job-concurrent-link", "concurrent-link-run", "p", "prod", "principal")
	if _, err := r.CreateRun(ctx, RunInput{RunID: "concurrent-link-run", ProjectID: "p", Environment: "prod", GenerationID: "g", PipelineID: "pipe", SemanticModelID: "m", TargetType: "refresh_pipeline", TargetID: "pipe", TriggerType: "manual", InvocationSource: "manual", PlanDigest: digest, ArtifactDigest: digest, PrincipalID: "principal", JobID: "job-concurrent-link"}); err != nil {
		t.Fatal(err)
	}
	attempt, err := r.ClaimAttempt(ctx, "concurrent-link-run", "worker", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	input := PublicationInput{PublicationID: "concurrent-link-publication", RunID: "concurrent-link-run", BaseGenerationID: "g", ResultGenerationID: "result-g", ExpectedTargetRevision: 1, ResultTargetRevision: 2, PlanDigest: digest, ArtifactDigest: digest, PhysicalPoolID: "pool", CatalogID: "catalog", OwnerID: "worker", FenceGeneration: attempt.FenceGeneration, Evidence: []byte(`{"linked":true}`)}
	tx1, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx1.Rollback(ctx)
	if _, err := r.LinkPublicationTx(ctx, tx1, input); err != nil {
		t.Fatal(err)
	}
	tx2, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx2.Rollback(ctx)
	started := make(chan struct{})
	result := make(chan struct {
		publication Publication
		err         error
	}, 1)
	go func() {
		close(started)
		publication, linkErr := r.LinkPublicationTx(ctx, tx2, input)
		result <- struct {
			publication Publication
			err         error
		}{publication, linkErr}
	}()
	<-started
	// The second transaction has reached the insert path and is waiting on
	// tx1's uncommitted deterministic publication row.  Commit tx1 as the
	// canonical owner; tx2 must replay the now-committed row exactly.
	time.Sleep(50 * time.Millisecond)
	if err := r.CommitPublicationTx(ctx, tx1, input.PublicationID, input.RunID, input.OwnerID, input.FenceGeneration, 42, input.Evidence, input.PhysicalPoolID, input.CatalogID); err != nil {
		t.Fatal(err)
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := <-result
	if got.err != nil {
		t.Fatalf("concurrent exact link replay: %v", got.err)
	}
	if got.publication.State != "committed" || got.publication.SnapshotID != 42 {
		t.Fatalf("concurrent replay publication = %#v", got.publication)
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresRefreshRunMayPublishTxLocksWorkerFence(t *testing.T) {
	_, admin := refreshTestDB(t)
	r := New(admin)
	ctx := t.Context()
	digest := "sha256:" + strings.Repeat("a", 64)
	seedRefreshJob(t, admin, "job-fence-lock", "fence-lock-run", "p", "prod", "principal")
	if _, err := r.CreateRun(ctx, RunInput{RunID: "fence-lock-run", ProjectID: "p", Environment: "prod", GenerationID: "g", PipelineID: "pipe", SemanticModelID: "m", TargetType: "refresh_pipeline", TargetID: "pipe", TriggerType: "manual", InvocationSource: "manual", PlanDigest: digest, ArtifactDigest: digest, PrincipalID: "principal", JobID: "job-fence-lock"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ClaimAttempt(ctx, "fence-lock-run", "worker-a", 1, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	tx1, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx1.Rollback(ctx)
	allowed, err := r.RunMayPublishTx(ctx, tx1, "fence-lock-run", "worker-a", 1)
	if err != nil || !allowed {
		t.Fatalf("RunMayPublishTx() = %v, %v; want live fence", allowed, err)
	}
	tx2, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx2.Rollback(ctx)
	takeover := make(chan error, 1)
	go func() {
		_, claimErr := r.ClaimAttemptTx(ctx, tx2, "fence-lock-run", "worker-b", 2, time.Minute)
		takeover <- claimErr
	}()
	// The lease expires while tx1 still owns the row lock. A takeover must
	// remain blocked until the publishing transaction releases that lock.
	time.Sleep(80 * time.Millisecond)
	select {
	case claimErr := <-takeover:
		t.Fatalf("takeover completed while publication fence lock held: %v", claimErr)
	default:
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case claimErr := <-takeover:
		if claimErr != nil {
			t.Fatalf("takeover after fence release: %v", claimErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("takeover remained blocked after publication fence release")
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
	seedRefreshJob(t, admin, "job-guard-run", "guard-run", "p", "prod", "principal")
	if _, err := r.CreateRun(ctx, RunInput{RunID: "guard-run", ProjectID: "p", Environment: "prod", GenerationID: "g", PipelineID: "pipe", SemanticModelID: "m", TargetType: "refresh_pipeline", TargetID: "guard-target", TriggerType: "manual", InvocationSource: "manual", PlanDigest: digest, ArtifactDigest: digest, PrincipalID: "principal", JobID: "job-guard-run"}); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO refresh.attempt(run_id,attempt_number,fence_generation,owner_id,lease_expires_at,status,evidence) VALUES ('guard-run',1,1,'forged',clock_timestamp()+interval '1 minute','succeeded','{"forged":true}')`); err == nil {
		t.Fatal("forged terminal attempt INSERT unexpectedly succeeded")
	}
	if _, err := admin.Exec(ctx, `UPDATE refresh.run SET status='running' WHERE run_id='guard-run'`); err == nil {
		t.Fatal("unfenced queued run transition unexpectedly succeeded")
	}
	if _, err := admin.Exec(ctx, `INSERT INTO refresh.publication_link(publication_id,run_id,base_generation_id,result_generation_id,plan_digest,artifact_digest,physical_pool_id,catalog_id,expected_target_revision,result_target_revision,fence_generation,owner_id,evidence) VALUES ('forged-pub','guard-run','g','g',$1,$1,'pool','catalog',1,2,1,'owner','{"linked":true}')`, digest); err == nil {
		t.Fatal("fabricated publication INSERT unexpectedly succeeded")
	}
	if _, err := admin.Exec(ctx, `INSERT INTO refresh.recovery_state(run_id,state,reconciliation_fence,owner_id,evidence) VALUES ('guard-run','reconciled',1,'owner','{}')`); err == nil {
		t.Fatal("empty fenced recovery INSERT unexpectedly succeeded")
	}
	if _, err := admin.Exec(ctx, `INSERT INTO refresh.data_version(project_id,environment,semantic_model_id,generation_id,snapshot_id,source,physical_pool_id,catalog_id,run_id,lease_owner,lease_revision) VALUES ('p','prod','m','g',1,'refresh','pool','catalog','guard-run','owner',1)`); err == nil {
		t.Fatal("unpublished data-version INSERT unexpectedly succeeded")
	}
	seedRefreshJob(t, admin, "job-recovery-guard", "recovery-guard", "p", "prod", "principal")
	if _, err := r.CreateRun(ctx, RunInput{RunID: "recovery-guard", ProjectID: "p", Environment: "prod", GenerationID: "g", PipelineID: "pipe", SemanticModelID: "m", TargetType: "model_table", TargetID: "recovery-guard", TriggerType: "manual", InvocationSource: "manual", PlanDigest: digest, ArtifactDigest: digest, PrincipalID: "principal", JobID: "job-recovery-guard"}); err != nil {
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
		seedRefreshJob(t, admin, "job-"+id, id, "p", "prod", "principal")
		if _, err := r.CreateRun(ctx, RunInput{RunID: id, ProjectID: "p", Environment: "prod", GenerationID: "g", PipelineID: "pipe", SemanticModelID: "m", TargetType: "model_table", TargetID: id, TriggerType: "manual", InvocationSource: "manual", PlanDigest: digest, ArtifactDigest: digest, PrincipalID: "principal", JobID: "job-" + id}); err != nil {
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
