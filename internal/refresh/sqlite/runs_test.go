package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/platform"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	jobsqlite "github.com/flidai/leapview/internal/platform/jobs/sqlite"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/pkg/jobs"
)

var testRunIdentity = projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "dev", GenerationID: "generation_a"}

func testSQLitePipelinePlan(identity projectgraph.ServingIdentity, pipelineID, semanticModelID string) *deployment.PipelinePlan {
	plan, err := deployment.NewPipelinePlan(deployment.PipelinePlan{
		ID: "pipeline_plan_test", PipelineID: pipelineID, ProjectID: identity.ProjectID.String(), Environment: identity.Environment, SemanticModelID: semanticModelID, ServingGenerationID: identity.GenerationID,
		ArtifactDigest:       "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SelectionDigest:      "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		MaterializationScope: []string{"model_orders"},
	})
	if err != nil {
		panic(err)
	}
	return &plan
}

func testSQLiteRootRunInput(identity projectgraph.ServingIdentity, pipelineID, semanticModelID, principalID string) refreshrun.RunInput {
	plan := testSQLitePipelinePlan(identity, pipelineID, semanticModelID)
	payload, _ := json.Marshal(map[string]any{"pipelinePlan": plan})
	return refreshrun.RunInput{
		Identity: identity, SemanticModelID: projectgraph.ResourceID(semanticModelID), PipelineID: projectgraph.ResourceID(pipelineID), PipelinePlan: plan,
		PrincipalID: principalID, GroupIDs: []string{}, EstimatedMemoryBytes: 67108864,
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: projectgraph.ResourceID(pipelineID), TriggerType: refreshrun.TriggerManual,
		JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: string(payload),
	}
}

func TestSQLRunRepositoryRecordsInitialLifecycleInRunTransaction(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO serving_states (id, project_id, environment, status) VALUES ('generation_a', 'project_sales', 'dev', 'validated');`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO principals (id, email, display_name) VALUES ('user:test', 'test@example.test', 'Test')`); err != nil {
		t.Fatal(err)
	}
	events := jobsqlite.NewRepository(store.SQLDB())
	repository := NewSQLRunRepositoryWithWorkflow(store.SQLDB(), events, RunWorkflowConfig{
		ResourceKind: "refresh", InitialEvent: "refresh.queued", InitialState: "queued",
	})
	run, err := repository.CreateRun(t.Context(), testSQLiteRootRunInput(testRunIdentity, "pipeline_daily", "semantic_sales", "user:test"))
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := events.ListEvents(t.Context(), "refresh", run.ID, 0, 10)
	if err != nil || len(recorded) != 1 || recorded[0].EventType != "refresh.queued" {
		t.Fatalf("initial events = %#v, %v", recorded, err)
	}
}

func TestSQLRunRepositoryAdmissionCollisionMatrix(t *testing.T) {
	t.Run("external active denies scheduled without supersession", func(t *testing.T) {
		store, repo, _ := seedRefreshJob(t, refreshrun.RunStatusRunning, "+5 minutes")
		at := time.Date(2026, 8, 20, 6, 0, 0, 0, time.UTC)
		insertClaimedOccurrence(t, store, testRunIdentity, "pipeline_daily", at, []string{"daily"})
		input := testSQLiteRootRunInput(testRunIdentity, "pipeline_daily", "semantic_sales", "user:test")
		input.TriggerType, input.InvocationSource = refreshrun.TriggerSchedule, refreshrun.TriggerSchedule
		input.TriggerID, input.NominalTime = "", at.Format(time.RFC3339Nano)
		input.MatchingScheduleIDs, input.ConcurrencyPolicy = []string{"daily"}, "Replace"
		occurrence := refreshschedule.Occurrence{Identity: testRunIdentity, PipelineID: "pipeline_daily", MatchingScheduleIDs: []string{"daily"}, ArtifactDigest: testSQLitePipelinePlan(testRunIdentity, "pipeline_daily", "semantic_sales").ArtifactDigest, ScheduledAt: at, Timezone: "UTC"}
		if _, err := repo.CreateScheduledRun(t.Context(), input, occurrence); !errors.Is(err, refreshrun.ErrAdmissionDeniedExternalActive) {
			t.Fatalf("scheduled admission error = %v, want admission_denied_external_active", err)
		}
		assertRefreshStatuses(t, store, refreshrun.RunStatusRunning, refreshrun.RunStatusRunning)
		var outcome string
		if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT outcome FROM refresh_pipeline_occurrences WHERE project_id=? AND environment=? AND pipeline_id=? AND scheduled_at=?`, testRunIdentity.ProjectID.String(), testRunIdentity.Environment, "pipeline_daily", at.Format(time.RFC3339Nano)).Scan(&outcome); err != nil {
			t.Fatal(err)
		}
		if outcome != refreshrun.AdmissionDeniedExternalActive {
			t.Fatalf("occurrence outcome = %q", outcome)
		}
	})

	t.Run("active invocation rejects incoming manual", func(t *testing.T) {
		_, repo, _ := seedRefreshJob(t, refreshrun.RunStatusRunning, "+5 minutes")
		input := testSQLiteRootRunInput(testRunIdentity, "pipeline_daily", "semantic_sales", "user:other")
		input.InvocationSource, input.TriggerID = refreshrun.TriggerManual, ""
		if _, err := repo.CreateRun(t.Context(), input); !errors.Is(err, refreshrun.ErrInvocationConflict) {
			t.Fatalf("manual admission error = %v, want conflict", err)
		}
	})

	t.Run("scheduled forbid skips scheduled overlap", func(t *testing.T) {
		store, repo, _ := seedRefreshJob(t, refreshrun.RunStatusRunning, "+5 minutes")
		if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE refresh_job_runs SET trigger_type='schedule' WHERE id='run_1'`); err != nil {
			t.Fatal(err)
		}
		at := time.Date(2026, 8, 20, 7, 0, 0, 0, time.UTC)
		insertClaimedOccurrence(t, store, testRunIdentity, "pipeline_daily", at, []string{"daily"})
		input := testSQLiteRootRunInput(testRunIdentity, "pipeline_daily", "semantic_sales", "user:test")
		input.TriggerType, input.InvocationSource = refreshrun.TriggerSchedule, refreshrun.TriggerSchedule
		input.TriggerID, input.NominalTime = "", at.Format(time.RFC3339Nano)
		input.MatchingScheduleIDs, input.ConcurrencyPolicy = []string{"daily"}, "Forbid"
		occurrence := refreshschedule.Occurrence{Identity: testRunIdentity, PipelineID: "pipeline_daily", MatchingScheduleIDs: []string{"daily"}, ArtifactDigest: testSQLitePipelinePlan(testRunIdentity, "pipeline_daily", "semantic_sales").ArtifactDigest, ScheduledAt: at, Timezone: "UTC"}
		run, err := repo.CreateScheduledRun(t.Context(), input, occurrence)
		if err != nil || run.Status != refreshrun.RunStatusSkipped {
			t.Fatalf("forbid overlap run=%#v err=%v", run, err)
		}
	})

	t.Run("scheduled replace supersedes only scheduled", func(t *testing.T) {
		store, repo, _ := seedRefreshJob(t, refreshrun.RunStatusRunning, "+5 minutes")
		if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE refresh_job_runs SET trigger_type='schedule' WHERE id='run_1'`); err != nil {
			t.Fatal(err)
		}
		priorAt := time.Date(2026, 8, 20, 7, 0, 0, 0, time.UTC)
		insertClaimedOccurrence(t, store, testRunIdentity, "pipeline_daily", priorAt, []string{"daily"})
		if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE refresh_pipeline_occurrences SET run_id='run_1', status='attached', outcome='admitted' WHERE project_id=? AND environment=? AND pipeline_id=? AND scheduled_at=?`, testRunIdentity.ProjectID.String(), testRunIdentity.Environment, "pipeline_daily", priorAt.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		at := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
		insertClaimedOccurrence(t, store, testRunIdentity, "pipeline_daily", at, []string{"daily"})
		input := testSQLiteRootRunInput(testRunIdentity, "pipeline_daily", "semantic_sales", "user:test")
		input.TriggerType, input.InvocationSource = refreshrun.TriggerSchedule, refreshrun.TriggerSchedule
		input.TriggerID, input.NominalTime = "", at.Format(time.RFC3339Nano)
		input.MatchingScheduleIDs, input.ConcurrencyPolicy = []string{"daily"}, "Replace"
		occurrence := refreshschedule.Occurrence{Identity: testRunIdentity, PipelineID: "pipeline_daily", MatchingScheduleIDs: []string{"daily"}, ArtifactDigest: testSQLitePipelinePlan(testRunIdentity, "pipeline_daily", "semantic_sales").ArtifactDigest, ScheduledAt: at, Timezone: "UTC"}
		run, err := repo.CreateScheduledRun(t.Context(), input, occurrence)
		if err != nil || run.Status != refreshrun.RunStatusQueued {
			t.Fatalf("replace run=%#v err=%v", run, err)
		}
		var prior string
		if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT status FROM refresh_job_runs WHERE id='run_1'`).Scan(&prior); err != nil {
			t.Fatal(err)
		}
		if prior != refreshrun.RunStatusSuperseded {
			t.Fatalf("prior scheduled status = %q", prior)
		}
		var occurrenceStatus, occurrenceOutcome string
		if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT status, outcome FROM refresh_pipeline_occurrences WHERE project_id=? AND environment=? AND pipeline_id=? AND scheduled_at=?`, testRunIdentity.ProjectID.String(), testRunIdentity.Environment, "pipeline_daily", priorAt.Format(time.RFC3339Nano)).Scan(&occurrenceStatus, &occurrenceOutcome); err != nil {
			t.Fatal(err)
		}
		if occurrenceStatus != "superseded" || occurrenceOutcome != "superseded" {
			t.Fatalf("prior occurrence status/outcome = %q/%q", occurrenceStatus, occurrenceOutcome)
		}
	})
}

func TestSQLRunRepositoryPersistsInvocationSourceIndependentlyFromTriggerType(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO serving_states (id, project_id, environment, status) VALUES ('generation_a', 'project_sales', 'dev', 'validated');
INSERT INTO principals (id, email, display_name) VALUES ('user:test', 'test@example.test', 'Test');`); err != nil {
		t.Fatal(err)
	}
	repository := NewSQLRunRepository(store.SQLDB())
	input := testSQLiteRootRunInput(testRunIdentity, "pipeline_daily", "semantic_sales", "user:test")
	input.TriggerType = refreshrun.TriggerManual
	input.InvocationSource = "backfill"
	run, err := repository.CreateRun(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if run.TriggerType != refreshrun.TriggerManual || run.InvocationSource != "backfill" {
		t.Fatalf("persisted invocation trigger/source = %q/%q", run.TriggerType, run.InvocationSource)
	}
	claimed, ok, err := repository.ClaimNextExecutableJob(t.Context(), testRunIdentity, "worker-1", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim persisted invocation ok=%v err=%v", ok, err)
	}
	if claimed.TriggerType != refreshrun.TriggerManual || claimed.InvocationSource != "backfill" {
		t.Fatalf("claimed invocation trigger/source = %q/%q", claimed.TriggerType, claimed.InvocationSource)
	}
}

func insertClaimedOccurrence(t *testing.T, store *platform.Store, identity projectgraph.ServingIdentity, pipeline string, at time.Time, ids []string) {
	t.Helper()
	encoded, _ := json.Marshal(ids)
	_, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO refresh_pipeline_occurrences (project_id, environment, pipeline_id, generation_id, artifact_digest, scheduled_at, timezone, matching_schedule_ids_json, status, outcome, claimed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'claimed', 'pending', CURRENT_TIMESTAMP)`, identity.ProjectID.String(), identity.Environment, pipeline, identity.GenerationID, testSQLitePipelinePlan(identity, pipeline, "semantic_sales").ArtifactDigest, at.Format(time.RFC3339Nano), "UTC", string(encoded))
	if err != nil {
		t.Fatal(err)
	}
}

func TestSQLRunRepositoryRollsBackRunWhenInitialLifecycleFails(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO serving_states (id, project_id, environment, status) VALUES ('generation_a', 'project_sales', 'dev', 'validated');`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO principals (id, email, display_name) VALUES ('user:test', 'test@example.test', 'Test')`); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("initial lifecycle unavailable")
	repository := NewSQLRunRepositoryWithWorkflow(store.SQLDB(), jobplatform.WorkflowRecorderFunc(func(context.Context, transaction.Transaction, jobs.WorkflowIntent) error {
		return injected
	}), RunWorkflowConfig{ResourceKind: "refresh", InitialEvent: "refresh.queued", InitialState: "queued"})
	_, err = repository.CreateRun(t.Context(), testSQLiteRootRunInput(testRunIdentity, "pipeline_daily", "semantic_sales", "user:test"))
	if !errors.Is(err, injected) {
		t.Fatalf("CreateRun() error = %v, want injected failure", err)
	}
	var count int
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM refresh_job_runs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("refresh run count = %d, %v", count, err)
	}
}

func TestSQLRunRepositoryAcceptsActiveLeaseFence(t *testing.T) {
	store, repo, job := seedRefreshJob(t, refreshrun.RunStatusRunning, "+5 minutes")

	if err := repo.RenewJobLease(t.Context(), job, time.Minute); err != nil {
		t.Fatalf("RenewJobLease() error = %v", err)
	}
	if _, err := repo.MarkRunPrepared(t.Context(), job); err != nil {
		t.Fatalf("MarkRunPrepared() error = %v", err)
	}
	allowed, err := repo.RunMayPublish(t.Context(), job)
	if err != nil {
		t.Fatalf("RunMayPublish() error = %v", err)
	}
	if !allowed {
		t.Fatal("RunMayPublish() = false for active lease")
	}
	assertRefreshStatuses(t, store, refreshrun.RunStatusRunning, "prepared")
}

func TestSQLRunRepositoryRejectsExpiredLeaseFence(t *testing.T) {
	t.Run("renew", func(t *testing.T) {
		store, repo, job := seedExpiredRefreshJob(t, refreshrun.RunStatusRunning)

		err := repo.RenewJobLease(t.Context(), job, time.Minute)
		if !errors.Is(err, refreshrun.ErrLeaseLost) {
			t.Fatalf("RenewJobLease() error = %v, want ErrLeaseLost", err)
		}
		assertRefreshStatuses(t, store, refreshrun.RunStatusRunning, refreshrun.RunStatusRunning)
	})

	t.Run("prepare", func(t *testing.T) {
		store, repo, job := seedExpiredRefreshJob(t, refreshrun.RunStatusRunning)

		_, err := repo.MarkRunPrepared(t.Context(), job)
		if !errors.Is(err, refreshrun.ErrLeaseLost) {
			t.Fatalf("MarkRunPrepared() error = %v, want ErrLeaseLost", err)
		}
		assertRefreshStatuses(t, store, refreshrun.RunStatusRunning, refreshrun.RunStatusRunning)
	})

	t.Run("publish eligibility", func(t *testing.T) {
		store, repo, job := seedExpiredRefreshJob(t, "prepared")

		allowed, err := repo.RunMayPublish(t.Context(), job)
		if err != nil {
			t.Fatalf("RunMayPublish() error = %v", err)
		}
		if allowed {
			t.Fatal("RunMayPublish() = true for expired lease")
		}
		assertRefreshStatuses(t, store, refreshrun.RunStatusRunning, "prepared")
	})
	t.Run("terminal transition", func(t *testing.T) {
		store, repo, job := seedExpiredRefreshJob(t, refreshrun.RunStatusRunning)
		if _, err := repo.MarkRunSucceededClaimed(t.Context(), job); !errors.Is(err, refreshrun.ErrLeaseLost) {
			t.Fatalf("terminal success error = %v, want ErrLeaseLost", err)
		}
		assertRefreshStatuses(t, store, refreshrun.RunStatusRunning, refreshrun.RunStatusRunning)
	})
}

func TestSQLRunRepositoryFencesTerminalTransitionsAcrossReclaim(t *testing.T) {
	t.Run("stale success cannot clear reclaimed lease", func(t *testing.T) {
		store, repo, job := seedRefreshJob(t, refreshrun.RunStatusRunning, "+5 minutes")
		if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE refresh_jobs SET lease_expires_at = datetime('now', '-1 second') WHERE id = ?`, job.ID); err != nil {
			t.Fatal(err)
		}
		reclaimed, ok, err := repo.ClaimNextExecutableJob(t.Context(), testRunIdentity, "worker-2", time.Minute)
		if err != nil || !ok {
			t.Fatalf("reclaim ok=%v err=%v", ok, err)
		}
		if _, err := repo.MarkRunSucceededClaimed(t.Context(), job); !errors.Is(err, refreshrun.ErrLeaseLost) {
			t.Fatalf("stale success error = %v, want ErrLeaseLost", err)
		}
		assertRefreshStatuses(t, store, refreshrun.RunStatusRunning, refreshrun.RunStatusRunning)
		if _, err := repo.MarkRunSucceededClaimed(t.Context(), reclaimed); err != nil {
			t.Fatalf("current success error = %v", err)
		}
		assertRefreshStatuses(t, store, refreshrun.RunStatusSucceeded, refreshrun.RunStatusSucceeded)
	})

	t.Run("stale failure cannot overwrite reclaimed attempt", func(t *testing.T) {
		store, repo, job := seedRefreshJob(t, refreshrun.RunStatusRunning, "+5 minutes")
		if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE refresh_jobs SET lease_expires_at = datetime('now', '-1 second') WHERE id = ?`, job.ID); err != nil {
			t.Fatal(err)
		}
		reclaimed, ok, err := repo.ClaimNextExecutableJob(t.Context(), testRunIdentity, "worker-2", time.Minute)
		if err != nil || !ok {
			t.Fatalf("reclaim ok=%v err=%v", ok, err)
		}
		if _, err := repo.MarkRunFailedClaimed(t.Context(), job, "stale failure"); !errors.Is(err, refreshrun.ErrLeaseLost) {
			t.Fatalf("stale failure error = %v, want ErrLeaseLost", err)
		}
		assertRefreshStatuses(t, store, refreshrun.RunStatusRunning, refreshrun.RunStatusRunning)
		if _, err := repo.MarkRunFailedClaimed(t.Context(), reclaimed, "current failure"); err != nil {
			t.Fatalf("current failure error = %v", err)
		}
		assertRefreshStatuses(t, store, refreshrun.RunStatusFailed, refreshrun.RunStatusFailed)
	})
}

func TestSQLRunRepositoryFailsClaimedRunTreeAtomically(t *testing.T) {
	store, repo, job := seedRefreshJob(t, refreshrun.RunStatusRunning, "+5 minutes")
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO refresh_jobs (id, project_id, generation_id, semantic_model_id, pipeline_id, principal_id, group_ids_json, estimated_memory_bytes, kind, status) VALUES ('child_job', 'project_sales', 'generation_a', 'semantic_sales', 'pipeline_daily', 'system:refresh', '[]', 67108864, 'child_run', 'queued');
INSERT INTO refresh_job_runs (id, job_id, principal_id, environment, target_type, target_id, target_revision, trigger_type, parent_run_id, status, created_sequence)
VALUES ('child_run', 'child_job', 'user:test', 'dev', 'model_table', 'table_orders', 1, 'dependency', 'run_1', 'running', 2);`); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkRunTreeFailedClaimed(t.Context(), job, "pipeline failed"); err != nil {
		t.Fatalf("tree failure = %v", err)
	}
	var rootStatus, childStatus, childJobStatus string
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT status FROM refresh_job_runs WHERE id='run_1'`).Scan(&rootStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT status FROM refresh_job_runs WHERE id='child_run'`).Scan(&childStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT status FROM refresh_jobs WHERE id='child_job'`).Scan(&childJobStatus); err != nil {
		t.Fatal(err)
	}
	if rootStatus != refreshrun.RunStatusFailed || childStatus != refreshrun.RunStatusFailed || childJobStatus != refreshrun.RunStatusFailed {
		t.Fatalf("tree statuses = %q/%q/%q, want failed", rootStatus, childStatus, childJobStatus)
	}
}

func TestSQLRunRepositoryRejectsIneligibleChildTree(t *testing.T) {
	store, repo, job := seedRefreshJob(t, refreshrun.RunStatusRunning, "+5 minutes")
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO refresh_jobs (id, project_id, generation_id, semantic_model_id, pipeline_id, principal_id, group_ids_json, estimated_memory_bytes, kind, status) VALUES ('child_job', 'project_sales', 'generation_a', 'semantic_sales', 'pipeline_daily', 'system:refresh', '[]', 67108864, 'child_run', 'queued');
INSERT INTO refresh_job_runs (id, job_id, principal_id, environment, target_type, target_id, target_revision, trigger_type, parent_run_id, status, created_sequence)
VALUES ('child_run', 'child_job', 'user:test', 'dev', 'model_table', 'table_orders', 1, 'dependency', 'run_1', 'succeeded', 2);`); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkRunTreeFailedClaimed(t.Context(), job, "pipeline failed"); !errors.Is(err, refreshrun.ErrLeaseLost) {
		t.Fatalf("tree failure = %v, want ErrLeaseLost", err)
	}
	assertRefreshStatuses(t, store, refreshrun.RunStatusRunning, refreshrun.RunStatusRunning)
}

func TestSQLRunRepositoryIsolatesSameTargetAcrossServingGenerations(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO serving_states (id, project_id, environment, status) VALUES
  ('generation_a', 'project_sales', 'dev', 'validated'),
  ('generation_b', 'project_sales', 'dev', 'validated');`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO principals (id, email, display_name) VALUES ('user:test', 'test@example.test', 'Test')`); err != nil {
		t.Fatal(err)
	}
	repository := NewSQLRunRepository(store.SQLDB())
	identityB := projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "dev", GenerationID: "generation_b"}
	input := func(identity projectgraph.ServingIdentity, pipeline, semanticModel string) refreshrun.RunInput {
		return testSQLiteRootRunInput(identity, pipeline, semanticModel, "user:test")
	}
	runA, err := repository.CreateRun(t.Context(), input(testRunIdentity, "pipeline_daily", "semantic_sales"))
	if err != nil {
		t.Fatal(err)
	}
	conflicting := input(identityB, "pipeline_daily", "semantic_sales")
	if _, err := repository.CreateRun(t.Context(), conflicting); !errors.Is(err, refreshrun.ErrInvocationConflict) {
		t.Fatalf("same target across generations error = %v, want invocation conflict", err)
	}
	scheduledInput := func(nominal string) refreshrun.RunInput {
		value := input(identityB, "pipeline_other", "semantic_other")
		value.TriggerType = refreshrun.TriggerSchedule
		value.InvocationSource = refreshrun.TriggerSchedule
		value.MatchingScheduleIDs = []string{"daily"}
		value.NominalTime = nominal
		value.ConcurrencyPolicy = refreshschedule.ConcurrencyReplace
		return value
	}
	runB, err := repository.CreateRun(t.Context(), scheduledInput("2026-08-20T06:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if runA.TargetRevision != 1 || runB.TargetRevision != 1 || runA.PipelineID != "pipeline_daily" || runB.PipelineID != "pipeline_other" {
		t.Fatalf("cross-generation revisions/pipeline = %d/%d %q/%q, want both 1 and distinct targets", runA.TargetRevision, runB.TargetRevision, runA.PipelineID, runB.PipelineID)
	}
	claimedA, ok, err := repository.ClaimNextExecutableJob(t.Context(), testRunIdentity, "worker-a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim generation A ok=%v err=%v", ok, err)
	}
	if _, err := repository.MarkRunPrepared(t.Context(), claimedA); err != nil {
		t.Fatal(err)
	}
	claimedB, ok, err := repository.ClaimNextExecutableJob(t.Context(), identityB, "worker-b", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim generation B ok=%v err=%v", ok, err)
	}
	if _, err := repository.MarkRunPrepared(t.Context(), claimedB); err != nil {
		t.Fatal(err)
	}
	newB, err := repository.CreateRun(t.Context(), scheduledInput("2026-08-21T06:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if newB.TargetRevision != 2 {
		t.Fatalf("generation B target revision = %d, want 2", newB.TargetRevision)
	}
	readScope, err := refreshrun.ReadScopeForIdentity(testRunIdentity)
	if err != nil {
		t.Fatal(err)
	}
	unchangedA, err := repository.GetRun(t.Context(), readScope, runA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchangedA.Status != refreshrun.RunStatusPrepared {
		t.Fatalf("generation A status = %q, want prepared after generation B supersession", unchangedA.Status)
	}
	allowedA, err := repository.RunMayPublish(t.Context(), claimedA)
	if err != nil || !allowedA {
		t.Fatalf("generation A publication fence allowed=%v err=%v, want true", allowedA, err)
	}
	allowedB, err := repository.RunMayPublish(t.Context(), claimedB)
	if err != nil {
		t.Fatal(err)
	}
	if allowedB {
		t.Fatal("generation B superseded publication fence = true, want false")
	}
}

func TestSQLRunRepositoryRunRemainsVisibleAfterGenerationActivation(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO serving_states (id, project_id, environment, status) VALUES
  ('generation_a', 'project_sales', 'dev', 'active'),
  ('generation_b', 'project_sales', 'dev', 'validated');`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO principals (id, email, display_name) VALUES ('user:test', 'test@example.test', 'Test')`); err != nil {
		t.Fatal(err)
	}
	repository := NewSQLRunRepository(store.SQLDB())
	run, err := repository.CreateRun(t.Context(), testSQLiteRootRunInput(testRunIdentity, "pipeline_daily", "semantic_sales", "user:test"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE serving_states SET status = CASE id WHEN 'generation_a' THEN 'superseded' ELSE 'active' END`); err != nil {
		t.Fatal(err)
	}
	activeIdentity := projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "dev", GenerationID: "generation_b"}
	scope, err := refreshrun.ReadScopeForIdentity(activeIdentity)
	if err != nil {
		t.Fatal(err)
	}
	visible, err := repository.GetRun(t.Context(), scope, run.ID)
	if err != nil {
		t.Fatalf("GetRun() after activation: %v", err)
	}
	if visible.ID != run.ID || visible.Identity != testRunIdentity {
		t.Fatalf("visible run = %#v, want originating identity %v", visible, testRunIdentity)
	}
	listed, err := repository.ListRuns(t.Context(), scope, refreshrun.RunPage{Limit: 10})
	if err != nil {
		t.Fatalf("ListRuns() after activation: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != run.ID {
		t.Fatalf("listed runs = %#v, want originating run", listed)
	}
}

func TestSQLRunRepositoryListsExecutableJobsAcrossGenerationsWithinReadScope(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO serving_states (id, project_id, environment, status) VALUES
  ('generation_a', 'project_sales', 'dev', 'active'),
  ('generation_b', 'project_sales', 'dev', 'validated'),
  ('generation_other', 'project_other', 'dev', 'validated'),
  ('generation_prod', 'project_sales', 'prod', 'active');
INSERT INTO principals (id, email, display_name) VALUES
  ('user:a', 'a@example.test', 'A'),
  ('user:b', 'b@example.test', 'B'),
  ('user:c', 'c@example.test', 'C'),
  ('user:d', 'd@example.test', 'D');`); err != nil {
		t.Fatal(err)
	}
	repository := NewSQLRunRepository(store.SQLDB())
	identities := []projectgraph.ServingIdentity{
		testRunIdentity,
		{ProjectID: "project_sales", Environment: "dev", GenerationID: "generation_b"},
		{ProjectID: "project_other", Environment: "dev", GenerationID: "generation_other"},
		{ProjectID: "project_sales", Environment: "prod", GenerationID: "generation_prod"},
	}
	for index, identity := range identities {
		if _, err := repository.CreateRun(t.Context(), testSQLiteRootRunInput(identity, "pipeline_"+string(rune('a'+index)), "semantic_"+string(rune('a'+index)), "user:"+string(rune('a'+index)))); err != nil {
			t.Fatal(err)
		}
	}
	scope, err := refreshrun.ReadScopeForIdentity(testRunIdentity)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := repository.ListExecutableJobs(t.Context(), scope, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("jobs in project/dev scope = %#v, want both serving generations only", jobs)
	}
	for _, job := range jobs {
		if job.Identity.ProjectID != testRunIdentity.ProjectID || job.Identity.Environment != testRunIdentity.Environment {
			t.Fatalf("out-of-scope executable job = %#v", job)
		}
	}
}

func TestMaterializationRunMappingRejectsCorruptPersistedIdentity(t *testing.T) {
	base := materializationRunDBRow{
		ID: "run_1", ProjectID: "project_sales", Environment: "dev", GenerationID: "generation_a",
		SemanticModelID: "semantic_sales", PipelineID: "pipeline_daily", TargetType: refreshrun.TargetRefreshPipeline,
		TargetID: "pipeline_daily", TargetRevision: 1, TriggerType: refreshrun.TriggerManual,
		InvocationSource: refreshrun.TriggerManual,
		PlanDigest:       "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", MaterializationScopeJSON: `["model_orders"]`,
		Status: refreshrun.RunStatusQueued,
	}
	if _, err := materializationRunFromDB(base); err != nil {
		t.Fatalf("valid persisted run rejected: %v", err)
	}
	for name, mutate := range map[string]func(*materializationRunDBRow){
		"generation alias":     func(row *materializationRunDBRow) { row.GenerationID = " generation_a" },
		"semantic model alias": func(row *materializationRunDBRow) { row.SemanticModelID = " semantic_sales" },
		"target alias":         func(row *materializationRunDBRow) { row.TargetID = " pipeline_daily" },
		"status enum":          func(row *materializationRunDBRow) { row.Status = "unknown" },
	} {
		t.Run(name, func(t *testing.T) {
			row := base
			mutate(&row)
			if _, err := materializationRunFromDB(row); err == nil {
				t.Fatal("corrupt persisted run accepted")
			}
		})
	}
}

func seedExpiredRefreshJob(t *testing.T, runStatus string) (*platform.Store, *SQLRunRepository, refreshrun.JobRecord) {
	t.Helper()
	return seedRefreshJob(t, runStatus, "-1 second")
}

func seedRefreshJob(t *testing.T, runStatus, leaseOffset string) (*platform.Store, *SQLRunRepository, refreshrun.JobRecord) {
	t.Helper()
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO serving_states (id, project_id, environment, status) VALUES ('generation_a', 'project_sales', 'dev', 'validated');
INSERT INTO principals (id, email, display_name) VALUES ('user:test', 'test@example.test', 'Test');`); err != nil {
		t.Fatalf("seed refresh job dependencies: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO refresh_jobs (
  id, project_id, generation_id, semantic_model_id, pipeline_id, principal_id, group_ids_json, estimated_memory_bytes, kind, status, lease_owner, lease_revision
) VALUES (
  'job_1', 'project_sales', 'generation_a', 'semantic_sales', 'pipeline_daily', 'user:test', '[]', 67108864, 'refresh_pipeline', 'running', 'worker-1', 1
);
INSERT INTO refresh_job_runs (
  id, job_id, principal_id, environment, target_type, target_id, target_revision, trigger_type, status, created_sequence
) VALUES (
  'run_1', 'job_1', 'user:test', 'dev', 'refresh_pipeline', 'pipeline_daily', 1, 'manual', ?, 1
);`, runStatus); err != nil {
		t.Fatalf("seed refresh job: %v", err)
	}
	plan := testSQLitePipelinePlan(testRunIdentity, "pipeline_daily", "semantic_sales")
	payload, _ := json.Marshal(map[string]any{"pipelinePlan": plan})
	scope, _ := json.Marshal(plan.MaterializationScope)
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE refresh_jobs SET payload_json = ? WHERE id = 'job_1';`, string(payload)); err != nil {
		t.Fatalf("seed refresh plan payload: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE refresh_job_runs SET project_id = 'project_sales', trigger_id = 'manual', invocation_source = 'manual', plan_digest = ?, materialization_scope_json = ? WHERE id = 'run_1';`, plan.Digest, string(scope)); err != nil {
		t.Fatalf("seed refresh run plan: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(),
		`UPDATE refresh_jobs SET lease_expires_at = datetime('now', ?) WHERE id = 'job_1'`,
		leaseOffset,
	); err != nil {
		t.Fatalf("set refresh job lease: %v", err)
	}
	job := refreshrun.JobRecord{
		ID: "job_1", Identity: testRunIdentity, SemanticModelID: "semantic_sales", PipelineID: "pipeline_daily", PipelinePlan: plan, PrincipalID: "user:test", EstimatedMemoryBytes: 67108864,
		Kind: refreshrun.JobKindRefreshPipeline, RunID: "run_1",
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline_daily", TargetRevision: 1,
		TriggerType: refreshrun.TriggerManual, LeaseOwner: "worker-1", LeaseRevision: 1,
	}
	return store, NewSQLRunRepository(store.SQLDB()), job
}

func assertRefreshStatuses(t *testing.T, store *platform.Store, wantJob, wantRun string) {
	t.Helper()
	var jobStatus, runStatus string
	if err := store.SQLDB().QueryRowContext(t.Context(), `
SELECT j.status, r.status
FROM refresh_jobs j
JOIN refresh_job_runs r ON r.job_id = j.id
WHERE j.id = 'job_1'`).Scan(&jobStatus, &runStatus); err != nil {
		t.Fatalf("read refresh statuses: %v", err)
	}
	if jobStatus != wantJob || runStatus != wantRun {
		t.Fatalf("refresh statuses = %q/%q, want %q/%q", jobStatus, runStatus, wantJob, wantRun)
	}
}
