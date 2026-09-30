package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPostgresScheduledOccurrenceDeniedByExternalRootAcrossPipelines(t *testing.T) {
	_, admin := refreshTestDB(t)
	r := New(admin)
	ctx := t.Context()
	const project, environment, generation = "project_external_admission", "prod", "generation_external_admission"
	digest := "sha256:" + strings.Repeat("a", 64)
	now := time.Now().UTC().Truncate(time.Minute)
	if _, err := r.PutSchedule(ctx, ScheduleInput{
		ProjectID: project, Environment: environment, PipelineID: "pipeline_scheduled",
		ScheduleID: "every-minute", SemanticModelID: "semantic_scheduled", GenerationID: generation,
		ArtifactDigest: digest, Cron: "* * * * *", Timezone: "UTC", ConcurrencyPolicy: "Forbid",
		StartingDeadline: time.Hour, ScheduleDigest: digest, NextRunAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := r.ClaimDue(ctx, Scope{ProjectID: project, Environment: environment, GenerationID: generation}, now, "scheduler-worker", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed occurrences = %d, want 1", len(claimed))
	}
	occurrence := claimed[0]
	if denied, err := r.DenyScheduledOccurrenceForExternalActiveRoot(ctx, occurrence); err != nil || denied {
		t.Fatalf("deny without active root = (%v, %v), want (false, nil)", denied, err)
	}

	seedRefreshJob(t, admin, "job_external_pipeline", "run_external_pipeline", project, environment, "principal:external")
	if _, err := r.CreateRun(ctx, RunInput{
		RunID: "run_external_pipeline", ProjectID: project, Environment: environment, GenerationID: generation,
		PipelineID: "pipeline_manual", SemanticModelID: "semantic_manual", TargetType: "refresh_pipeline",
		TargetID: "pipeline_manual", TriggerType: "manual", InvocationSource: "manual",
		PlanDigest: digest, ArtifactDigest: digest, PrincipalID: "principal:external", JobID: "job_external_pipeline",
	}); err != nil {
		t.Fatal(err)
	}
	denied, err := r.DenyScheduledOccurrenceForExternalActiveRoot(ctx, occurrence)
	if err != nil {
		t.Fatal(err)
	}
	if !denied {
		t.Fatal("external active root on another pipeline did not deny scheduled occurrence")
	}
	stored, err := r.GetOccurrence(ctx, occurrence.OccurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	var outcome struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(stored.Outcome, &outcome); err != nil {
		t.Fatal(err)
	}
	if stored.Status != "skipped" || outcome.Reason != "admission_denied_external_active" {
		t.Fatalf("stored occurrence status=%q outcome=%s, want terminal external-active denial", stored.Status, stored.Outcome)
	}
	if err := r.ReleaseOccurrence(ctx, occurrence); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("release after terminal denial = %v, want stale-fence error", err)
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
