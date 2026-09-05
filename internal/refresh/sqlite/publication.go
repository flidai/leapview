package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/transaction"
	materializedb "github.com/flidai/leapview/internal/refresh/internal/db"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

// PublicationUnitOfWork owns the fenced refresh completion transaction after
// canonical delivery has committed its immutable generation.
type PublicationUnitOfWork struct {
	db                  *sql.DB
	applyAccessSnapshot func(context.Context, transaction.Transaction, string) error
}

func NewPublicationUnitOfWork(database *sql.DB, applyAccessSnapshot func(context.Context, transaction.Transaction, string) error) *PublicationUnitOfWork {
	return &PublicationUnitOfWork{db: database, applyAccessSnapshot: applyAccessSnapshot}
}

// CompleteCanonicalRefresh commits the refresh run/job tree after the sealed
// delivery lifecycle has already published the new immutable catalog. It owns
// only refresh workflow state; delivery remains the sole serving-root writer.
func (u *PublicationUnitOfWork) CompleteCanonicalRefresh(ctx context.Context, job refreshrun.JobRecord, result refreshrun.CanonicalRefreshResult) error {
	if u == nil || u.db == nil {
		return fmt.Errorf("refresh publication database is required")
	}
	if err := job.Validate(); err != nil || job.LeaseOwner == "" || job.LeaseRevision <= 0 || strings.TrimSpace(result.PlanID) == "" || strings.TrimSpace(result.ServingStateID) == "" || result.SnapshotID <= 0 {
		return refreshrun.ErrLeaseLost
	}
	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := materializedb.New(tx)
	publication, err := canonicalDeliveryPublication(ctx, tx, job, result)
	if err != nil {
		return err
	}
	if !publication.found {
		return fmt.Errorf("canonical refresh publication evidence is missing")
	}
	complete, err := canonicalWorkflowComplete(ctx, tx, job)
	if err != nil {
		return err
	}
	if complete {
		if publication.found {
			if err := persistCanonicalDataVersion(ctx, tx, job, publication); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	active, err := q.RefreshPublicationFenceActive(ctx, materializedb.RefreshPublicationFenceActiveParams{
		RunID: job.RunID, ProjectID: job.Identity.ProjectID.String(), GenerationID: job.Identity.GenerationID,
		Environment: job.Identity.Environment, TargetRevision: job.TargetRevision,
		LeaseOwner: job.LeaseOwner, LeaseRevision: job.LeaseRevision,
	})
	if err != nil {
		return err
	}
	expectedRuns, err := q.CountRefreshPublicationTreeRuns(ctx, materializedb.CountRefreshPublicationTreeRunsParams{
		RunID: job.RunID, ProjectID: job.Identity.ProjectID.String(), GenerationID: job.Identity.GenerationID, Environment: job.Identity.Environment,
	})
	if err != nil {
		return err
	}
	expectedJobs, err := q.CountRefreshPublicationTreeJobs(ctx, materializedb.CountRefreshPublicationTreeJobsParams{
		RunID: job.RunID, ProjectID: job.Identity.ProjectID.String(), GenerationID: job.Identity.GenerationID, Environment: job.Identity.Environment,
	})
	if err != nil {
		return err
	}
	if expectedRuns < 1 || expectedJobs < 1 {
		return refreshrun.ErrLeaseLost
	}
	if active {
		completedRuns, err := q.CompleteRefreshPublicationRun(ctx, materializedb.CompleteRefreshPublicationRunParams{
			RunID: job.RunID, ProjectID: job.Identity.ProjectID.String(), GenerationID: job.Identity.GenerationID,
			Environment: job.Identity.Environment, TargetRevision: job.TargetRevision,
			LeaseOwner: job.LeaseOwner, LeaseRevision: job.LeaseRevision,
		})
		if err != nil {
			return err
		}
		if completedRuns != expectedRuns {
			return refreshrun.ErrLeaseLost
		}
		completedJobs, err := q.CompleteRefreshPublicationJob(ctx, materializedb.CompleteRefreshPublicationJobParams{
			RunID: job.RunID, ProjectID: job.Identity.ProjectID.String(), GenerationID: job.Identity.GenerationID,
			Environment: job.Identity.Environment, LeaseOwner: job.LeaseOwner, LeaseRevision: job.LeaseRevision,
		})
		if err != nil {
			return err
		}
		if completedJobs != expectedJobs {
			return refreshrun.ErrLeaseLost
		}
	} else if err := completeCanonicalWorkflowWithoutLease(ctx, tx, job); err != nil {
		return err
	}
	if err := persistCanonicalDataVersion(ctx, tx, job, publication); err != nil {
		return err
	}
	return tx.Commit()
}

type canonicalDeliveryPublicationEvidence struct {
	found       bool
	generation  string
	snapshotID  int64
	completedAt time.Time
}

// canonicalDeliveryPublication finds the committed delivery rooted at the
// serving generation captured by the refresh job. The delivery tables are
// read-only here: delivery remains the sole writer of the serving root.
func canonicalDeliveryPublication(ctx context.Context, tx *sql.Tx, job refreshrun.JobRecord, result refreshrun.CanonicalRefreshResult) (canonicalDeliveryPublicationEvidence, error) {
	var evidence canonicalDeliveryPublicationEvidence
	var completed string
	err := tx.QueryRowContext(ctx, `
SELECT g.serving_state_id, attempt.qualified_snapshot_id, COALESCE(p.completed_at, '')
FROM delivery_publications p
JOIN delivery_plans plan ON plan.id = p.plan_id
JOIN delivery_generations base ON base.id = p.expected_base_generation_id
JOIN delivery_generations g ON g.id = p.generation_id
JOIN delivery_build_attempts attempt
  ON attempt.plan_id = p.plan_id AND attempt.candidate_id = p.candidate_id AND attempt.status = 'sealed'
WHERE p.project_id = ? AND p.environment = ? AND p.status = 'committed'
  AND p.plan_id = ? AND g.serving_state_id = ?
  AND attempt.qualified_snapshot_id = ?
  AND plan.operation_kind = 'restatement'
  AND base.serving_state_id = ?
LIMIT 1`, job.Identity.ProjectID.String(), job.Identity.Environment, result.PlanID, result.ServingStateID, result.SnapshotID, job.Identity.GenerationID).Scan(&evidence.generation, &evidence.snapshotID, &completed)
	if err == sql.ErrNoRows {
		return evidence, nil
	}
	if err != nil {
		return evidence, err
	}
	evidence.found = evidence.generation != ""
	if completed != "" {
		parsed, parseErr := parsePublicationTime(completed)
		if parseErr != nil {
			return canonicalDeliveryPublicationEvidence{}, parseErr
		}
		evidence.completedAt = parsed
	}
	if evidence.completedAt.IsZero() {
		evidence.completedAt = time.Now().UTC()
	}
	return evidence, nil
}

func parsePublicationTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid canonical publication completion time %q", value)
}

func canonicalWorkflowComplete(ctx context.Context, tx *sql.Tx, job refreshrun.JobRecord) (bool, error) {
	var totalRuns, incompleteRuns, failedRuns, totalJobs, incompleteJobs, failedJobs int64
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*), COUNT(*) FILTER (WHERE runs.status <> 'succeeded'), COUNT(*) FILTER (WHERE runs.status IN ('failed', 'cancelled', 'superseded'))
FROM refresh_job_runs runs JOIN refresh_jobs jobs ON jobs.id = runs.job_id
WHERE jobs.project_id = ? AND jobs.generation_id = ? AND runs.environment = ?
  AND (runs.id = ? OR runs.parent_run_id = ?)`, job.Identity.ProjectID.String(), job.Identity.GenerationID, job.Identity.Environment, job.RunID, job.RunID).Scan(&totalRuns, &incompleteRuns, &failedRuns); err != nil {
		return false, err
	}
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*), COUNT(*) FILTER (WHERE jobs.status <> 'succeeded'), COUNT(*) FILTER (WHERE jobs.status IN ('failed', 'cancelled', 'superseded'))
FROM refresh_jobs jobs JOIN refresh_job_runs runs ON runs.job_id = jobs.id
WHERE jobs.project_id = ? AND jobs.generation_id = ? AND runs.environment = ?
  AND (runs.id = ? OR runs.parent_run_id = ?)`, job.Identity.ProjectID.String(), job.Identity.GenerationID, job.Identity.Environment, job.RunID, job.RunID).Scan(&totalJobs, &incompleteJobs, &failedJobs); err != nil {
		return false, err
	}
	if failedRuns > 0 || failedJobs > 0 {
		return false, refreshrun.ErrLeaseLost
	}
	return totalRuns > 0 && totalJobs > 0 && incompleteRuns == 0 && incompleteJobs == 0, nil
}

func completeCanonicalWorkflowWithoutLease(ctx context.Context, tx *sql.Tx, job refreshrun.JobRecord) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE refresh_job_runs SET status = 'succeeded', finished_at = CURRENT_TIMESTAMP, error = ''
WHERE (id = ? OR parent_run_id = ?) AND environment = ? AND target_revision = ? AND status IN ('prepared', 'queued', 'running')
  AND job_id IN (SELECT id FROM refresh_jobs WHERE project_id = ? AND generation_id = ?)`, job.RunID, job.RunID, job.Identity.Environment, job.TargetRevision, job.Identity.ProjectID.String(), job.Identity.GenerationID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE refresh_jobs SET status = 'succeeded', updated_at = CURRENT_TIMESTAMP, finished_at = CURRENT_TIMESTAMP,
    lease_owner = '', lease_expires_at = NULL, last_error = ''
WHERE id IN (SELECT job_id FROM refresh_job_runs WHERE id = ? OR parent_run_id = ?)
  AND project_id = ? AND generation_id = ? AND status IN ('queued', 'running')`, job.RunID, job.RunID, job.Identity.ProjectID.String(), job.Identity.GenerationID); err != nil {
		return err
	}
	complete, err := canonicalWorkflowComplete(ctx, tx, job)
	if err != nil {
		return err
	}
	if !complete {
		return refreshrun.ErrLeaseLost
	}
	return nil
}

func persistCanonicalDataVersion(ctx context.Context, tx *sql.Tx, job refreshrun.JobRecord, publication canonicalDeliveryPublicationEvidence) error {
	if !publication.found {
		return nil
	}
	if publication.snapshotID <= 0 {
		return fmt.Errorf("canonical publication %s has no durable DuckLake snapshot", publication.generation)
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO semantic_model_data_versions (
  project_id, environment, semantic_model_id, snapshot_id, generation_id, refreshed_at, source, pipeline_id, run_id
) VALUES (?, ?, ?, ?, ?, ?, 'refresh', NULLIF(?, ''), NULLIF(?, ''))
ON CONFLICT (project_id, environment, semantic_model_id, generation_id) DO UPDATE SET
  snapshot_id = excluded.snapshot_id, refreshed_at = excluded.refreshed_at, source = excluded.source,
  pipeline_id = excluded.pipeline_id, run_id = excluded.run_id`, job.Identity.ProjectID.String(), job.Identity.Environment, job.SemanticModelID.String(), publication.snapshotID, publication.generation, publication.completedAt.UTC().Format(time.RFC3339Nano), job.PipelineID.String(), job.RunID)
	return err
}
