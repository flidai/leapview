package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func manualIntentInput(project, environment, pipeline, target, principal, key string) ManualIntentInput {
	return ManualIntentInput{
		ProjectID: project, Environment: environment, PipelineID: pipeline, TargetID: target,
		PrincipalID: principal, SourceDigest: "sha256:" + strings.Repeat("a", 64),
		IdempotencyKey: key, RequestDigest: "sha256:" + strings.Repeat("b", 64),
		AuditIntentJSON: json.RawMessage(`{"eventId":"manual-create-event","action":"pipeline.run"}`),
	}
}

func TestManualIntentIdempotentReplayKeepsAcceptedSourceAndIDs(t *testing.T) {
	_, db := refreshTestDB(t)
	r := New(db)
	ctx := t.Context()
	in := manualIntentInput("intent-idem-project", "dev", "pipeline_a", "instance_a", "principal_a", "request-1")
	var auditCalls int
	first, created, err := r.CreateManualIntentWithAudit(ctx, in, func(ctx context.Context, tx Tx, intent ManualIntent) error {
		auditCalls++
		var visible string
		if err := tx.QueryRow(ctx, `SELECT status FROM refresh.manual_intent WHERE intent_id=$1`, intent.IntentID).Scan(&visible); err != nil {
			return err
		}
		if visible != ManualIntentWaiting {
			return fmt.Errorf("audit hook observed status %q", visible)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created || auditCalls != 1 {
		t.Fatalf("first create: created=%v auditCalls=%d", created, auditCalls)
	}
	for label, value := range map[string]string{"intent": first.IntentID, "reserved run": first.ReservedRunID} {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed.Version() != 7 {
			t.Fatalf("%s id %q is not UUIDv7: %v", label, value, err)
		}
	}
	replay := in
	replay.SourceDigest = "sha256:" + strings.Repeat("c", 64)
	replay.TargetID = "instance_after_deploy"
	replay.IntentID, replay.ReservedRunID = "", ""
	replayed, created, err := r.CreateManualIntent(ctx, replay)
	if err != nil {
		t.Fatal(err)
	}
	if created || replayed.IntentID != first.IntentID || replayed.ReservedRunID != first.ReservedRunID || replayed.SourceDigest != first.SourceDigest || replayed.TargetID != first.TargetID {
		t.Fatalf("replay changed accepted intent: created=%v got=%+v want=%+v", created, replayed, first)
	}
	if auditCalls != 1 {
		t.Fatalf("replay repeated audit hook %d times", auditCalls)
	}
	listed, err := r.ListManualIntents(ctx, Scope{ProjectID: in.ProjectID, Environment: in.Environment}, "instance_a", 10)
	if err != nil || len(listed) != 1 || listed[0].IntentID != first.IntentID {
		t.Fatalf("target-scoped queue list = %+v err=%v", listed, err)
	}
	listed, err = r.ListManualIntents(ctx, Scope{ProjectID: in.ProjectID, Environment: in.Environment}, "instance_other", 10)
	if err != nil || len(listed) != 0 {
		t.Fatalf("foreign target queue list = %+v err=%v", listed, err)
	}
	conflict := replay
	conflict.RequestDigest = "sha256:" + strings.Repeat("d", 64)
	if _, _, err := r.CreateManualIntent(ctx, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting replay error = %v, want ErrConflict", err)
	}
	read, err := r.GetManualIntent(ctx, Scope{ProjectID: in.ProjectID, Environment: in.Environment}, first.IntentID)
	if err != nil || read.IntentID != first.IntentID || string(read.AuditIntentJSON) != string(first.AuditIntentJSON) {
		t.Fatalf("readback = %+v, err=%v", read, err)
	}
	if _, err := r.GetManualIntent(ctx, Scope{ProjectID: in.ProjectID, Environment: "other"}, first.IntentID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign-scope get error = %v, want not found", err)
	}
	byKey, err := r.GetManualIntentByIdempotency(ctx, Scope{ProjectID: in.ProjectID, Environment: in.Environment}, in.PrincipalID, in.IdempotencyKey)
	if err != nil || byKey.ReservedRunID != first.ReservedRunID {
		t.Fatalf("idempotency lookup = %+v err=%v", byKey, err)
	}
	if _, err := r.GetManualIntentByIdempotency(ctx, Scope{ProjectID: in.ProjectID, Environment: "other"}, in.PrincipalID, in.IdempotencyKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign-scope idempotency lookup error = %v, want not found", err)
	}
}

func TestManualIntentFIFOClaimLeaseRecoveryAndReservedRunAttach(t *testing.T) {
	_, db := refreshTestDB(t)
	r := New(db)
	ctx := t.Context()
	scope := Scope{ProjectID: "intent-queue-project", Environment: "dev"}
	first, created, err := r.CreateManualIntent(ctx, manualIntentInput(scope.ProjectID, scope.Environment, "pipeline_a", "instance_a", "principal_a", "first"))
	if err != nil || !created {
		t.Fatalf("create first intent: created=%v err=%v", created, err)
	}
	second, created, err := r.CreateManualIntent(ctx, manualIntentInput(scope.ProjectID, scope.Environment, "pipeline_b", "instance_a", "principal_b", "second"))
	if err != nil || !created {
		t.Fatalf("create second intent: created=%v err=%v", created, err)
	}
	claimed, ok, err := r.ClaimNextManualIntent(ctx, scope, "dispatcher-a", 80*time.Millisecond)
	if err != nil || !ok || claimed.IntentID != first.IntentID || claimed.FenceGeneration != 1 {
		t.Fatalf("first claim = intent %q fence %d ok=%v err=%v", claimed.IntentID, claimed.FenceGeneration, ok, err)
	}
	if _, ok, err := r.ClaimNextManualIntent(ctx, scope, "dispatcher-b", time.Minute); err != nil || ok {
		t.Fatalf("active claim was not exclusive: ok=%v err=%v", ok, err)
	}
	seedTestRefreshRoot(t, r, db, scope, claimed.PipelineID, claimed.ReservedRunID, "job-reserved-first", claimed.PrincipalID)
	time.Sleep(100 * time.Millisecond)
	recovered, ok, err := r.ClaimNextManualIntent(ctx, scope, "dispatcher-b", time.Minute)
	if err != nil || !ok || recovered.IntentID != first.IntentID || recovered.FenceGeneration != claimed.FenceGeneration+1 {
		t.Fatalf("expired claim recovery = intent %q fence %d ok=%v err=%v", recovered.IntentID, recovered.FenceGeneration, ok, err)
	}
	if _, err := r.CancelManualIntent(ctx, scope, recovered.IntentID); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancel after reserved root insert = %v, want ErrConflict", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.AttachManualIntentTx(ctx, tx, recovered.IntentID, recovered.LeaseOwner, recovered.FenceGeneration, recovered.ReservedRunID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("attach reserved run: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	attached, err := r.GetManualIntent(ctx, scope, first.IntentID)
	if err != nil || attached.Status != ManualIntentAttached || attached.AttachedRunID != first.ReservedRunID {
		t.Fatalf("attached readback = %+v err=%v", attached, err)
	}
	if _, ok, err := r.ClaimNextManualIntent(ctx, scope, "dispatcher-c", time.Minute); err != nil || ok {
		t.Fatalf("active reserved run did not gate next intent: ok=%v err=%v", ok, err)
	}
	if _, err := r.CancelRun(ctx, scope, recovered.ReservedRunID); err != nil {
		t.Fatal(err)
	}
	next, ok, err := r.ClaimNextManualIntent(ctx, scope, "dispatcher-c", time.Minute)
	if err != nil || !ok || next.IntentID != second.IntentID {
		t.Fatalf("next FIFO claim = %q ok=%v err=%v", next.IntentID, ok, err)
	}
	releaseTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ReleaseManualIntentTx(ctx, releaseTx, next.IntentID, next.LeaseOwner, next.FenceGeneration); err != nil {
		_ = releaseTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := releaseTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	reclaimed, ok, err := r.ClaimNextManualIntent(ctx, scope, "dispatcher-d", time.Minute)
	if err != nil || !ok || reclaimed.IntentID != second.IntentID || reclaimed.FenceGeneration != next.FenceGeneration+1 {
		t.Fatalf("released FIFO re-claim = %q fence=%d ok=%v err=%v", reclaimed.IntentID, reclaimed.FenceGeneration, ok, err)
	}
	if _, err := r.CancelManualIntent(ctx, scope, reclaimed.IntentID); err != nil {
		t.Fatal(err)
	}
}

func TestManualIntentQueueGateSerializesScopeAndCancellation(t *testing.T) {
	_, db := refreshTestDB(t)
	r := New(db)
	ctx := t.Context()
	scope := Scope{ProjectID: "intent-gate-project", Environment: "dev"}
	intent, _, err := r.CreateManualIntent(ctx, manualIntentInput(scope.ProjectID, scope.Environment, "pipeline_b", "instance_a", "principal_b", "queued"))
	if err != nil {
		t.Fatal(err)
	}
	seedTestRefreshRoot(t, r, db, scope, "pipeline_a", "active-run-a", "job-active-a", "principal_a")
	if _, ok, err := r.ClaimNextManualIntent(ctx, scope, "dispatcher", time.Minute); err != nil || ok {
		t.Fatalf("claim passed an active root from another pipeline: ok=%v err=%v", ok, err)
	}
	if _, err := r.CancelRun(ctx, scope, "active-run-a"); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := r.ClaimNextManualIntent(ctx, scope, "dispatcher", time.Minute)
	if err != nil || !ok || claimed.IntentID != intent.IntentID {
		t.Fatalf("claim after root finished = %q ok=%v err=%v", claimed.IntentID, ok, err)
	}
	cancelled, err := r.CancelManualIntent(ctx, scope, intent.IntentID)
	if err != nil || cancelled.Status != ManualIntentCancelled {
		t.Fatalf("cancel claim = status %q err=%v", cancelled.Status, err)
	}
	if _, ok, err := r.ClaimNextManualIntent(ctx, scope, "dispatcher-2", time.Minute); err != nil || ok {
		t.Fatalf("cancelled intent remained claimable: ok=%v err=%v", ok, err)
	}
}

func TestManualIntentRecentStaleListIsSeparateAndBounded(t *testing.T) {
	_, db := refreshTestDB(t)
	r := New(db)
	ctx := t.Context()
	scope := Scope{ProjectID: "intent-stale-project", Environment: "dev"}
	stale, _, err := r.CreateManualIntent(ctx, manualIntentInput(scope.ProjectID, scope.Environment, "pipeline_a", "instance_a", "principal_a", "stale"))
	if err != nil {
		t.Fatal(err)
	}
	waiting, _, err := r.CreateManualIntent(ctx, manualIntentInput(scope.ProjectID, scope.Environment, "pipeline_b", "instance_b", "principal_b", "waiting"))
	if err != nil {
		t.Fatal(err)
	}
	claim, ok, err := r.ClaimNextManualIntent(ctx, scope, "dispatcher", time.Minute)
	if err != nil || !ok || claim.IntentID != stale.IntentID {
		t.Fatalf("claim for stale transition = %q ok=%v err=%v", claim.IntentID, ok, err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.MarkManualIntentStaleTx(ctx, tx, claim.IntentID, claim.LeaseOwner, claim.FenceGeneration, "Pipeline definition changed while waiting; start a new request"); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	live, err := r.ListManualIntents(ctx, scope, "", 10)
	if err != nil || len(live) != 1 || live[0].IntentID != waiting.IntentID {
		t.Fatalf("live intent list = %+v err=%v", live, err)
	}
	recent, err := r.ListRecentStaleManualIntents(ctx, scope, "instance_a", 10)
	if err != nil || len(recent) != 1 || recent[0].IntentID != stale.IntentID || recent[0].Status != ManualIntentStale {
		t.Fatalf("recent stale list = %+v err=%v", recent, err)
	}
	recent, err = r.ListRecentStaleManualIntents(ctx, scope, "instance_b", 10)
	if err != nil || len(recent) != 0 {
		t.Fatalf("foreign target stale list = %+v err=%v", recent, err)
	}
	if _, err := r.ListRecentStaleManualIntents(ctx, scope, "", MaxPageSize+1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unbounded stale history request error = %v, want ErrInvalid", err)
	}
}

func TestRootRunAdmissionIsExclusiveAcrossPipelinesInScope(t *testing.T) {
	_, db := refreshTestDB(t)
	r := New(db)
	ctx := t.Context()
	scope := Scope{ProjectID: "intent-run-gate-project", Environment: "dev"}
	seedTestRefreshRoot(t, r, db, scope, "pipeline_a", "run-a", "job-a", "principal-a")
	seedRefreshJob(t, db, "job-b", "run-b", scope.ProjectID, scope.Environment, "principal-b")
	_, err := r.CreateRun(ctx, RunInput{
		RunID: "run-b", ProjectID: scope.ProjectID, Environment: scope.Environment, GenerationID: "generation_1",
		PipelineID: "pipeline_b", SemanticModelID: "model_b", TargetType: "refresh_pipeline", TargetID: "pipeline_b",
		TriggerType: "manual", InvocationSource: "manual", PlanDigest: "sha256:" + strings.Repeat("c", 64),
		ArtifactDigest: "sha256:" + strings.Repeat("d", 64), PrincipalID: "principal-b", JobID: "job-b",
	})
	if err == nil {
		t.Fatal("second pipeline root run was admitted while the scope target was active")
	}
	if _, err := r.CancelRun(ctx, scope, "run-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateRun(ctx, RunInput{
		RunID: "run-b", ProjectID: scope.ProjectID, Environment: scope.Environment, GenerationID: "generation_1",
		PipelineID: "pipeline_b", SemanticModelID: "model_b", TargetType: "refresh_pipeline", TargetID: "pipeline_b",
		TriggerType: "manual", InvocationSource: "manual", PlanDigest: "sha256:" + strings.Repeat("c", 64),
		ArtifactDigest: "sha256:" + strings.Repeat("d", 64), PrincipalID: "principal-b", JobID: "job-b",
	}); err != nil {
		t.Fatalf("root after prior run terminalized: %v", err)
	}
}

func TestManualIntentWaitingQueueBound(t *testing.T) {
	_, db := refreshTestDB(t)
	r := New(db)
	ctx := t.Context()
	scope := Scope{ProjectID: "intent-bound-project", Environment: "dev"}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxWaitingManualIntents; i++ {
		in := manualIntentInput(scope.ProjectID, scope.Environment, "pipeline", "instance", "principal", fmt.Sprintf("key-%03d", i))
		if _, created, err := r.CreateManualIntentTx(ctx, tx, in); err != nil || !created {
			_ = tx.Rollback(ctx)
			t.Fatalf("create waiting intent %d: created=%v err=%v", i, created, err)
		}
	}
	if _, _, err := r.CreateManualIntentTx(ctx, tx, manualIntentInput(scope.ProjectID, scope.Environment, "pipeline", "instance", "principal", "overflow")); !errors.Is(err, ErrManualIntentQueueFull) {
		_ = tx.Rollback(ctx)
		t.Fatalf("over-bound create error = %v, want ErrManualIntentQueueFull", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}

func seedTestRefreshRoot(t *testing.T, r *Repository, db *pgxpool.Pool, scope Scope, pipelineID, runID, jobID, principalID string) {
	t.Helper()
	seedRefreshJob(t, db, jobID, runID, scope.ProjectID, scope.Environment, principalID)
	_, err := r.CreateRun(t.Context(), RunInput{
		RunID: runID, ProjectID: scope.ProjectID, Environment: scope.Environment, GenerationID: "generation_1",
		PipelineID: pipelineID, SemanticModelID: "model_" + pipelineID, TargetType: "refresh_pipeline", TargetID: pipelineID,
		TriggerType: "manual", InvocationSource: "manual", PlanDigest: "sha256:" + strings.Repeat("c", 64),
		ArtifactDigest: "sha256:" + strings.Repeat("d", 64), PrincipalID: principalID, JobID: jobID,
	})
	if err != nil {
		t.Fatal(err)
	}
}
