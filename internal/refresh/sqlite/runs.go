package sqlite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/jobs"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	platformdb "github.com/flidai/leapview/internal/refresh/internal/db"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/internal/servingstate"
)

type SQLRunRepository struct {
	db        *sql.DB
	q         *platformdb.Queries
	workflow  jobs.WorkflowRecorder
	execution RunWorkflowConfig
}

type RunWorkflowConfig struct {
	ResourceKind string
	InitialEvent string
	InitialState string
}

func NewSQLRunRepository(db *sql.DB) *SQLRunRepository {
	return &SQLRunRepository{db: db, q: platformdb.New(db)}
}

func NewSQLRunRepositoryWithWorkflow(db *sql.DB, workflow jobs.WorkflowRecorder, execution RunWorkflowConfig) *SQLRunRepository {
	return &SQLRunRepository{db: db, q: platformdb.New(db), workflow: workflow, execution: execution}
}

func (r *SQLRunRepository) CreateRun(ctx context.Context, input refreshrun.RunInput) (refreshrun.RunRecord, error) {
	return r.createRun(ctx, input, nil)
}

func (r *SQLRunRepository) CreateScheduledRun(ctx context.Context, input refreshrun.RunInput, occurrence refreshschedule.Occurrence) (refreshrun.RunRecord, error) {
	return r.createRun(ctx, input, &occurrence)
}

func (r *SQLRunRepository) createRun(ctx context.Context, input refreshrun.RunInput, occurrence *refreshschedule.Occurrence) (refreshrun.RunRecord, error) {
	if r == nil || r.db == nil {
		return refreshrun.RunRecord{}, fmt.Errorf("refresh run database is required")
	}
	normalized, err := normalizeRunInput(input)
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	if occurrence != nil {
		if normalized.TriggerType != refreshrun.TriggerSchedule || occurrence.WorkspaceID != normalized.WorkspaceID ||
			normalized.TargetID != occurrence.WorkspaceID+"."+occurrence.PipelineID || occurrence.Environment == "" ||
			occurrence.ArtifactDigest == "" || occurrence.ScheduledAt.IsZero() {
			return refreshrun.RunRecord{}, fmt.Errorf("scheduled refresh run does not match its claimed occurrence")
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	if normalized.ParentRunID == "" && normalized.TargetType == refreshrun.TargetRefreshPipeline {
		target := platformdb.SupersedeRefreshTargetJobsParams{
			WorkspaceID: normalized.WorkspaceID, Environment: normalized.Environment,
			TargetType: normalized.TargetType, TargetID: normalized.TargetID,
		}
		if err := q.SupersedeRefreshTargetJobs(ctx, target); err != nil {
			return refreshrun.RunRecord{}, err
		}
		if err := q.SupersedeRefreshTargetRuns(ctx, platformdb.SupersedeRefreshTargetRunsParams(target)); err != nil {
			return refreshrun.RunRecord{}, err
		}
	}
	jobID := newRunID("matjob")
	runID := newRunID("matrun")
	if err := q.CreateRefreshJob(ctx, platformdb.CreateRefreshJobParams{
		ID: jobID, WorkspaceID: normalized.WorkspaceID, ServingStateID: normalized.ServingStateID,
		ModelID: normalized.ModelID, Kind: normalized.JobKind, PayloadJson: normalized.PayloadJSON, Status: refreshrun.RunStatusQueued,
	}); err != nil {
		return refreshrun.RunRecord{}, err
	}
	if err := q.CreateRefreshJobRun(ctx, platformdb.CreateRefreshJobRunParams{
		ID: runID, JobID: jobID, PrincipalID: normalized.PrincipalID, Environment: normalized.Environment, TargetType: normalized.TargetType,
		TargetID: normalized.TargetID, TriggerType: normalized.TriggerType, ParentRunID: normalized.ParentRunID,
		RetryOf: normalized.RetryOf, Status: refreshrun.RunStatusQueued, TargetGeneration: normalized.TargetGeneration,
	}); err != nil {
		return refreshrun.RunRecord{}, err
	}
	if occurrence != nil {
		result, err := q.AttachRefreshPipelineRun(ctx, platformdb.AttachRefreshPipelineRunParams{
			RunID: sql.NullString{String: runID, Valid: true}, WorkspaceID: occurrence.WorkspaceID,
			Environment: occurrence.Environment, PipelineID: occurrence.PipelineID,
			ScheduledAt: occurrence.ScheduledAt.UTC().Format(time.RFC3339Nano),
		})
		if err != nil {
			return refreshrun.RunRecord{}, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return refreshrun.RunRecord{}, err
		}
		if affected != 1 {
			return refreshrun.RunRecord{}, fmt.Errorf("scheduled refresh occurrence is not claimable")
		}
	}
	if normalized.ParentRunID == "" && r.workflow != nil {
		if strings.TrimSpace(r.execution.ResourceKind) == "" || strings.TrimSpace(r.execution.InitialEvent) == "" || strings.TrimSpace(r.execution.InitialState) == "" {
			return refreshrun.RunRecord{}, fmt.Errorf("refresh workflow contract is required")
		}
		dataString, encodeErr := refreshgen.EncodeGenCreateRefreshRunAuditPayload(refreshgen.GenSchemaRefreshQueuedAuditPayload{
			Id: runID, WorkspaceId: normalized.WorkspaceID,
			PipelineId:    strings.TrimPrefix(normalized.TargetID, normalized.WorkspaceID+"."),
			SemanticModel: normalized.ModelID, Trigger: normalized.TriggerType,
			RetryOf: normalized.RetryOf, Status: r.execution.InitialState,
		})
		if encodeErr != nil {
			return refreshrun.RunRecord{}, encodeErr
		}
		data := []byte(dataString)
		if err := r.workflow.RecordWorkflow(ctx, tx, jobs.WorkflowIntent{Event: jobs.EventInput{
			Key: r.execution.InitialEvent, ResourceKind: r.execution.ResourceKind,
			ResourceID: runID, EventType: r.execution.InitialEvent, Data: data,
		}}); err != nil {
			return refreshrun.RunRecord{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return refreshrun.RunRecord{}, err
	}
	return r.GetRun(ctx, normalized.WorkspaceID, runID)
}

func (r *SQLRunRepository) ClaimNextExecutableJob(ctx context.Context, environment, owner string, lease time.Duration) (refreshrun.JobRecord, bool, error) {
	candidates, err := r.ListExecutableJobs(ctx, environment, 1)
	if err != nil || len(candidates) == 0 {
		return refreshrun.JobRecord{}, false, err
	}
	return r.ClaimExecutableJob(ctx, candidates[0], owner, lease)
}

func (r *SQLRunRepository) ListExecutableJobs(ctx context.Context, environment string, limit int) ([]refreshrun.JobRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("refresh run database is required")
	}
	environment = string(servingstate.NormalizeEnvironment(servingstate.Environment(environment)))
	if limit <= 0 {
		limit = 16
	}
	rows, err := r.q.ListExecutableRefreshJobHeads(ctx, platformdb.ListExecutableRefreshJobHeadsParams{
		ResultLimit: int64(limit), RefreshPipelineKind: refreshrun.JobKindRefreshPipeline,
		Environment: environment, QueuedStatus: refreshrun.RunStatusQueued,
		RunQueuedStatus: refreshrun.RunStatusQueued, RunningStatus: refreshrun.RunStatusRunning,
	})
	if err != nil {
		return nil, err
	}
	jobs := make([]refreshrun.JobRecord, 0, len(rows))
	for _, row := range rows {
		jobs = append(jobs, refreshrun.JobRecord{
			ID: row.ID, WorkspaceID: row.WorkspaceID, Environment: row.Environment, ServingStateID: row.ServingStateID, ModelID: row.ModelID,
			Kind: row.Kind, PayloadJSON: row.PayloadJson, RunID: row.RunID, TargetType: row.TargetType,
			TargetID: row.TargetID, TargetGeneration: row.TargetGeneration, TriggerType: row.TriggerType, AttemptCount: int(row.AttemptCount),
			LeaseOwner: row.LeaseOwner, LeaseGeneration: row.LeaseGeneration,
		})
	}
	return jobs, nil
}

func (r *SQLRunRepository) ClaimExecutableJob(ctx context.Context, job refreshrun.JobRecord, owner string, lease time.Duration) (refreshrun.JobRecord, bool, error) {
	if r == nil || r.db == nil {
		return refreshrun.JobRecord{}, false, fmt.Errorf("refresh run database is required")
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return refreshrun.JobRecord{}, false, fmt.Errorf("lease owner is required")
	}
	if strings.TrimSpace(job.ID) == "" || strings.TrimSpace(job.RunID) == "" {
		return refreshrun.JobRecord{}, false, fmt.Errorf("refresh job and run ids are required")
	}
	if lease <= 0 {
		lease = time.Minute
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return refreshrun.JobRecord{}, false, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	leaseExpr := sqliteLeaseModifier(lease)
	result, err := q.ClaimRefreshJob(ctx, platformdb.ClaimRefreshJobParams{
		RunningStatus: refreshrun.RunStatusRunning, LeaseOwner: owner, LeaseModifier: leaseExpr,
		ID: job.ID, QueuedStatus: refreshrun.RunStatusQueued, PreviousRunningStatus: refreshrun.RunStatusRunning,
	})
	if err != nil {
		return refreshrun.JobRecord{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return refreshrun.JobRecord{}, false, err
	}
	if affected == 0 {
		return refreshrun.JobRecord{}, false, nil
	}
	if err := q.MarkRefreshJobRunClaimed(ctx, platformdb.MarkRefreshJobRunClaimedParams{Status: refreshrun.RunStatusRunning, ID: job.RunID}); err != nil {
		return refreshrun.JobRecord{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return refreshrun.JobRecord{}, false, err
	}
	job.AttemptCount++
	job.LeaseOwner = owner
	job.LeaseGeneration++
	return job, true, nil
}

func (r *SQLRunRepository) RenewJobLease(ctx context.Context, job refreshrun.JobRecord, lease time.Duration) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("refresh run database is required")
	}
	changed, err := r.q.RenewRefreshJobLease(ctx, platformdb.RenewRefreshJobLeaseParams{
		LeaseModifier: sqliteLeaseModifier(lease), ID: strings.TrimSpace(job.ID),
		LeaseOwner: strings.TrimSpace(job.LeaseOwner), LeaseGeneration: job.LeaseGeneration, Status: refreshrun.RunStatusRunning,
	})
	if err != nil {
		return err
	}
	if changed != 1 {
		return refreshrun.ErrLeaseLost
	}
	return nil
}

func (r *SQLRunRepository) MarkRunPrepared(ctx context.Context, job refreshrun.JobRecord) (refreshrun.RunRecord, error) {
	changed, err := r.q.MarkRefreshRunPrepared(ctx, platformdb.MarkRefreshRunPreparedParams{
		RunID: job.RunID, WorkspaceID: job.WorkspaceID,
		LeaseOwner: job.LeaseOwner, LeaseGeneration: job.LeaseGeneration,
	})
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	if changed != 1 {
		return refreshrun.RunRecord{}, refreshrun.ErrLeaseLost
	}
	return r.GetRun(ctx, job.WorkspaceID, job.RunID)
}

func (r *SQLRunRepository) RunMayPublish(ctx context.Context, job refreshrun.JobRecord) (bool, error) {
	allowed, err := r.q.RefreshRunMayPublish(ctx, platformdb.RefreshRunMayPublishParams{
		RunID: job.RunID, WorkspaceID: job.WorkspaceID, TargetGeneration: job.TargetGeneration,
		LeaseOwner: job.LeaseOwner, LeaseGeneration: job.LeaseGeneration,
	})
	return allowed == 1, err
}

func (r *SQLRunRepository) JobQueueStats(ctx context.Context, environment string) (refreshrun.JobQueueStats, error) {
	if r == nil || r.db == nil {
		return refreshrun.JobQueueStats{}, fmt.Errorf("refresh run database is required")
	}
	row, err := r.q.GetRefreshJobQueueStats(ctx, platformdb.GetRefreshJobQueueStatsParams{
		QueuedStatus: refreshrun.RunStatusQueued, RunningStatus: refreshrun.RunStatusRunning,
		StaleRunningStatus: refreshrun.RunStatusRunning, RefreshPipelineKind: refreshrun.JobKindRefreshPipeline,
		Environment: string(servingstate.NormalizeEnvironment(servingstate.Environment(environment))),
	})
	if err != nil {
		return refreshrun.JobQueueStats{}, err
	}
	return refreshrun.JobQueueStats{QueuedJobs: int(row.QueuedJobs), RunningJobs: int(row.RunningJobs), StaleLeasedJobs: int(row.StaleLeasedJobs)}, nil
}

func (r *SQLRunRepository) GetRun(ctx context.Context, workspaceID, runID string) (refreshrun.RunRecord, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	runID = strings.TrimSpace(runID)
	if workspaceID == "" {
		return refreshrun.RunRecord{}, fmt.Errorf("workspace id is required")
	}
	if runID == "" {
		return refreshrun.RunRecord{}, fmt.Errorf("run id is required")
	}
	row, err := r.q.GetMaterializationRun(ctx, platformdb.GetMaterializationRunParams{RunID: runID, WorkspaceID: workspaceID})
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	return materializationRunFromGetRow(row), nil
}

func (r *SQLRunRepository) ListRuns(ctx context.Context, workspaceID string, page refreshrun.RunPage) ([]refreshrun.RunRecord, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return nil, fmt.Errorf("workspace id is required")
	}
	limit := runPageLimit(page)
	cursor := runPageCursor{}
	after := strings.TrimSpace(page.After)
	if after != "" {
		resolved, ok, err := r.runPageCursor(ctx, workspaceID, environmentForPage(page), "", "", after)
		if err != nil {
			return nil, err
		}
		if !ok {
			return []refreshrun.RunRecord{}, nil
		}
		cursor = resolved
	}
	rows, err := r.q.ListMaterializationRuns(ctx, platformdb.ListMaterializationRunsParams{
		WorkspaceID: workspaceID, Environment: environmentForPage(page), CursorCreatedAt: cursor.CreatedAt, CursorSequence: cursor.Sequence, Limit: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]refreshrun.RunRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, materializationRunFromListRow(row))
	}
	return out, nil
}

func (r *SQLRunRepository) ListTargetRuns(ctx context.Context, workspaceID, targetType, targetID string, page refreshrun.RunPage) ([]refreshrun.RunRecord, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	targetType = strings.TrimSpace(targetType)
	targetID = strings.TrimSpace(targetID)
	if workspaceID == "" {
		return nil, fmt.Errorf("workspace id is required")
	}
	if targetType == "" {
		return nil, fmt.Errorf("target type is required")
	}
	if targetID == "" {
		return nil, fmt.Errorf("target id is required")
	}
	limit := runPageLimit(page)
	cursor := runPageCursor{}
	after := strings.TrimSpace(page.After)
	if after != "" {
		resolved, ok, err := r.runPageCursor(ctx, workspaceID, environmentForPage(page), targetType, targetID, after)
		if err != nil {
			return nil, err
		}
		if !ok {
			return []refreshrun.RunRecord{}, nil
		}
		cursor = resolved
	}
	rows, err := r.q.ListTargetMaterializationRuns(ctx, platformdb.ListTargetMaterializationRunsParams{
		WorkspaceID: workspaceID, Environment: environmentForPage(page), TargetType: targetType, TargetID: targetID,
		CursorCreatedAt: cursor.CreatedAt, CursorSequence: cursor.Sequence, Limit: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]refreshrun.RunRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, materializationRunFromTargetListRow(row))
	}
	return out, nil
}

func (r *SQLRunRepository) ListChildRuns(ctx context.Context, workspaceID, parentRunID string) ([]refreshrun.RunRecord, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	parentRunID = strings.TrimSpace(parentRunID)
	if workspaceID == "" {
		return nil, fmt.Errorf("workspace id is required")
	}
	if parentRunID == "" {
		return nil, fmt.Errorf("parent run id is required")
	}
	rows, err := r.q.ListChildMaterializationRuns(ctx, platformdb.ListChildMaterializationRunsParams{
		WorkspaceID: workspaceID, ParentRunID: sql.NullString{String: parentRunID, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	out := make([]refreshrun.RunRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, materializationRunFromChildRow(row))
	}
	return out, nil
}

func (r *SQLRunRepository) LatestTargetRun(ctx context.Context, workspaceID, environment, targetType, targetID string) (refreshrun.RunRecord, bool, error) {
	runs, err := r.ListTargetRuns(ctx, workspaceID, targetType, targetID, refreshrun.RunPage{Limit: 1, Environment: environment})
	if err != nil {
		return refreshrun.RunRecord{}, false, err
	}
	if len(runs) == 0 {
		return refreshrun.RunRecord{}, false, nil
	}
	return runs[0], true, nil
}

func (r *SQLRunRepository) LatestSuccessfulTargetRun(ctx context.Context, workspaceID, environment, targetType, targetID string) (refreshrun.RunRecord, bool, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	targetType = strings.TrimSpace(targetType)
	targetID = strings.TrimSpace(targetID)
	if workspaceID == "" {
		return refreshrun.RunRecord{}, false, fmt.Errorf("workspace id is required")
	}
	if targetType == "" {
		return refreshrun.RunRecord{}, false, fmt.Errorf("target type is required")
	}
	if targetID == "" {
		return refreshrun.RunRecord{}, false, fmt.Errorf("target id is required")
	}
	row, err := r.q.LatestSuccessfulMaterializationRun(ctx, platformdb.LatestSuccessfulMaterializationRunParams{
		WorkspaceID: workspaceID, Environment: string(servingstate.NormalizeEnvironment(servingstate.Environment(environment))),
		TargetType: targetType, TargetID: targetID, Status: refreshrun.RunStatusSucceeded,
	})
	if err == sql.ErrNoRows {
		return refreshrun.RunRecord{}, false, nil
	}
	if err != nil {
		return refreshrun.RunRecord{}, false, err
	}
	return materializationRunFromLatestRow(row), true, nil
}

func (r *SQLRunRepository) MarkRunRunning(ctx context.Context, workspaceID, runID string) (refreshrun.RunRecord, error) {
	return r.markRun(ctx, workspaceID, runID, refreshrun.RunStatusRunning, "")
}

func (r *SQLRunRepository) MarkRunSucceeded(ctx context.Context, workspaceID, runID string) (refreshrun.RunRecord, error) {
	return r.markRun(ctx, workspaceID, runID, refreshrun.RunStatusSucceeded, "")
}

func (r *SQLRunRepository) MarkRunFailed(ctx context.Context, workspaceID, runID, message string) (refreshrun.RunRecord, error) {
	return r.markRun(ctx, workspaceID, runID, refreshrun.RunStatusFailed, message)
}

func (r *SQLRunRepository) MarkRunSucceededClaimed(ctx context.Context, job refreshrun.JobRecord) (refreshrun.RunRecord, error) {
	return r.markRunClaimed(ctx, job, refreshrun.RunStatusSucceeded, "")
}

func (r *SQLRunRepository) MarkRunFailedClaimed(ctx context.Context, job refreshrun.JobRecord, message string) (refreshrun.RunRecord, error) {
	if err := r.MarkRunTreeFailedClaimed(ctx, job, message); err != nil {
		return refreshrun.RunRecord{}, err
	}
	return r.GetRun(ctx, job.WorkspaceID, job.RunID)
}

func (r *SQLRunRepository) MarkRunTreeFailedClaimed(ctx context.Context, job refreshrun.JobRecord, message string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("refresh run database is required")
	}
	if strings.TrimSpace(job.WorkspaceID) == "" || strings.TrimSpace(job.RunID) == "" || strings.TrimSpace(job.LeaseOwner) == "" || job.LeaseGeneration <= 0 {
		return refreshrun.ErrLeaseLost
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	args := platformdb.MarkRefreshRunTreeFailedClaimedParams{RunID: job.RunID, WorkspaceID: job.WorkspaceID, LeaseOwner: job.LeaseOwner, LeaseGeneration: job.LeaseGeneration, ErrorMessage: message}
	expectedRuns, err := q.CountRefreshRunTreeClaimed(ctx, job.RunID)
	if err != nil {
		return err
	}
	expectedJobs, err := q.CountRefreshJobTreeClaimed(ctx, job.RunID)
	if err != nil {
		return err
	}
	if expectedRuns < 1 || expectedJobs < 1 {
		return refreshrun.ErrLeaseLost
	}
	changed, err := q.MarkRefreshRunTreeFailedClaimed(ctx, args)
	if err != nil {
		return err
	}
	if changed != expectedRuns {
		return refreshrun.ErrLeaseLost
	}
	jobs, err := q.CompleteRefreshJobTreeFailedClaimed(ctx, platformdb.CompleteRefreshJobTreeFailedClaimedParams{RunID: job.RunID, WorkspaceID: job.WorkspaceID, LeaseOwner: job.LeaseOwner, LeaseGeneration: job.LeaseGeneration, ErrorMessage: message})
	if err != nil {
		return err
	}
	if jobs != expectedJobs {
		return refreshrun.ErrLeaseLost
	}
	return tx.Commit()
}

func (r *SQLRunRepository) CancelRun(ctx context.Context, workspaceID, runID string) (refreshrun.RunRecord, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	runID = strings.TrimSpace(runID)
	if workspaceID == "" || runID == "" {
		return refreshrun.RunRecord{}, fmt.Errorf("workspace id and run id are required")
	}
	prior, err := r.GetRun(ctx, workspaceID, runID)
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	if prior.ParentRunID != "" || prior.TargetType != refreshrun.TargetRefreshPipeline || prior.ServingStateID == "" {
		return refreshrun.RunRecord{}, refreshrun.ErrRunNotCancellable
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	result, err := q.CancelQueuedMaterializationRun(ctx, platformdb.CancelQueuedMaterializationRunParams{
		CancelledStatus: refreshrun.RunStatusCancelled, RunID: runID,
		QueuedStatus: refreshrun.RunStatusQueued, WorkspaceID: workspaceID,
	})
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	if affected == 0 {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return refreshrun.RunRecord{}, rollbackErr
		}
		if _, getErr := r.GetRun(ctx, workspaceID, runID); getErr != nil {
			return refreshrun.RunRecord{}, getErr
		}
		return refreshrun.RunRecord{}, refreshrun.ErrRunNotCancellable
	}
	if err := q.CancelQueuedChildMaterializationRuns(ctx, platformdb.CancelQueuedChildMaterializationRunsParams{
		CancelledStatus: refreshrun.RunStatusCancelled, ParentRunID: sql.NullString{String: runID, Valid: true},
		QueuedStatus: refreshrun.RunStatusQueued, WorkspaceID: workspaceID,
	}); err != nil {
		return refreshrun.RunRecord{}, err
	}
	if err := q.CancelQueuedChildRefreshJobs(ctx, platformdb.CancelQueuedChildRefreshJobsParams{
		CancelledStatus: refreshrun.RunStatusCancelled, WorkspaceID: workspaceID,
		QueuedStatus: refreshrun.RunStatusQueued, ParentRunID: sql.NullString{String: runID, Valid: true},
	}); err != nil {
		return refreshrun.RunRecord{}, err
	}
	if err := q.CancelQueuedRefreshJobForRun(ctx, platformdb.CancelQueuedRefreshJobForRunParams{
		CancelledStatus: refreshrun.RunStatusCancelled, RunID: runID,
		WorkspaceID: workspaceID, QueuedStatus: refreshrun.RunStatusQueued,
	}); err != nil {
		return refreshrun.RunRecord{}, err
	}
	failed, err := q.FailCancelledRefreshCandidate(ctx, prior.ServingStateID)
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	failedCount, err := failed.RowsAffected()
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	if failedCount != 1 {
		return refreshrun.RunRecord{}, fmt.Errorf("refresh candidate is not cancellable")
	}
	if err := tx.Commit(); err != nil {
		return refreshrun.RunRecord{}, err
	}
	return r.GetRun(ctx, workspaceID, runID)
}

func (r *SQLRunRepository) FailRunsForTerminalServingStates(ctx context.Context, environment, message string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("refresh run database is required")
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = "refresh did not complete"
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	if err := q.FailTerminalServingStateRuns(ctx, platformdb.FailTerminalServingStateRunsParams{
		FailedStatus: refreshrun.RunStatusFailed, ErrorMessage: message,
		QueuedStatus: refreshrun.RunStatusQueued, RunningStatus: refreshrun.RunStatusRunning, Environment: environment,
	}); err != nil {
		return err
	}
	if err := q.FailTerminalServingStateJobs(ctx, platformdb.FailTerminalServingStateJobsParams{
		FailedStatus: refreshrun.RunStatusFailed, QueuedStatus: refreshrun.RunStatusQueued, RunningStatus: refreshrun.RunStatusRunning, Environment: environment,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *SQLRunRepository) markRun(ctx context.Context, workspaceID, runID, status, message string) (refreshrun.RunRecord, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	runID = strings.TrimSpace(runID)
	if workspaceID == "" {
		return refreshrun.RunRecord{}, fmt.Errorf("workspace id is required")
	}
	if runID == "" {
		return refreshrun.RunRecord{}, fmt.Errorf("run id is required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	params := platformdb.MarkMaterializationRunActiveParams{Status: status, ErrorMessage: message, RunID: runID, WorkspaceID: workspaceID}
	var result sql.Result
	if status == refreshrun.RunStatusSucceeded || status == refreshrun.RunStatusFailed {
		result, err = q.MarkMaterializationRunTerminal(ctx, platformdb.MarkMaterializationRunTerminalParams(params))
	} else {
		result, err = q.MarkMaterializationRunActive(ctx, params)
	}
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	if affected == 0 {
		return refreshrun.RunRecord{}, sql.ErrNoRows
	}
	switch status {
	case refreshrun.RunStatusSucceeded:
		err = q.CompleteRefreshJobSucceeded(ctx, platformdb.CompleteRefreshJobSucceededParams{RunID: runID, WorkspaceID: workspaceID})
	case refreshrun.RunStatusFailed:
		err = q.CompleteRefreshJobFailed(ctx, platformdb.CompleteRefreshJobFailedParams{ErrorMessage: message, RunID: runID, WorkspaceID: workspaceID})
	default:
		err = q.UpdateRefreshJobForActiveRun(ctx, platformdb.UpdateRefreshJobForActiveRunParams{NewStatus: status, RunID: runID, WorkspaceID: workspaceID})
	}
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return refreshrun.RunRecord{}, err
	}
	return r.GetRun(ctx, workspaceID, runID)
}

func (r *SQLRunRepository) markRunClaimed(ctx context.Context, job refreshrun.JobRecord, status, message string) (refreshrun.RunRecord, error) {
	if r == nil || r.db == nil {
		return refreshrun.RunRecord{}, fmt.Errorf("refresh run database is required")
	}
	if strings.TrimSpace(job.WorkspaceID) == "" || strings.TrimSpace(job.RunID) == "" ||
		strings.TrimSpace(job.LeaseOwner) == "" || job.LeaseGeneration <= 0 {
		return refreshrun.RunRecord{}, refreshrun.ErrLeaseLost
	}
	if status != refreshrun.RunStatusSucceeded && status != refreshrun.RunStatusFailed {
		return refreshrun.RunRecord{}, fmt.Errorf("unsupported claimed terminal run status %q", status)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	params := platformdb.MarkRefreshRunSucceededClaimedParams{
		RunID: job.RunID, WorkspaceID: job.WorkspaceID,
		LeaseOwner: job.LeaseOwner, LeaseGeneration: job.LeaseGeneration,
	}
	var affected int64
	if status == refreshrun.RunStatusSucceeded {
		affected, err = q.MarkRefreshRunSucceededClaimed(ctx, params)
	} else {
		affected, err = q.MarkRefreshRunFailedClaimed(ctx, platformdb.MarkRefreshRunFailedClaimedParams{
			RunID: job.RunID, WorkspaceID: job.WorkspaceID,
			LeaseOwner: job.LeaseOwner, LeaseGeneration: job.LeaseGeneration, ErrorMessage: message,
		})
	}
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	if affected != 1 {
		return refreshrun.RunRecord{}, refreshrun.ErrLeaseLost
	}
	if status == refreshrun.RunStatusSucceeded {
		affected, err = q.CompleteRefreshJobSucceededClaimed(ctx, platformdb.CompleteRefreshJobSucceededClaimedParams{
			RunID: job.RunID, WorkspaceID: job.WorkspaceID,
			LeaseOwner: job.LeaseOwner, LeaseGeneration: job.LeaseGeneration,
		})
	} else {
		affected, err = q.CompleteRefreshJobFailedClaimed(ctx, platformdb.CompleteRefreshJobFailedClaimedParams{
			RunID: job.RunID, WorkspaceID: job.WorkspaceID,
			LeaseOwner: job.LeaseOwner, LeaseGeneration: job.LeaseGeneration, ErrorMessage: message,
		})
	}
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	if affected != 1 {
		return refreshrun.RunRecord{}, refreshrun.ErrLeaseLost
	}
	if err := tx.Commit(); err != nil {
		return refreshrun.RunRecord{}, err
	}
	return r.GetRun(ctx, job.WorkspaceID, job.RunID)
}

type materializationRunDBRow struct {
	ID                   string
	WorkspaceID          string
	Environment          string
	ServingStateID       sql.NullString
	ModelID              string
	PrincipalID          sql.NullString
	PrincipalDisplayName string
	TargetType           string
	TargetID             string
	TargetGeneration     int64
	TriggerType          string
	ParentRunID          sql.NullString
	RetryOf              sql.NullString
	Status               string
	CreatedAt            string
	UpdatedAt            string
	StartedAt            string
	FinishedAt           sql.NullString
	Error                string
}

func materializationRunFromGetRow(row platformdb.GetMaterializationRunRow) refreshrun.RunRecord {
	return materializationRunFromDB(materializationRunDBRow{
		ID: row.ID, WorkspaceID: row.WorkspaceID, Environment: row.Environment, ServingStateID: row.ServingStateID, ModelID: row.ModelID,
		PrincipalID: row.PrincipalID, PrincipalDisplayName: row.PrincipalDisplayName, TargetType: row.TargetType,
		TargetID: row.TargetID, TargetGeneration: row.TargetGeneration, TriggerType: row.TriggerType, ParentRunID: row.ParentRunID, RetryOf: row.RetryOf, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, Error: row.Error,
	})
}

func materializationRunFromChildRow(row platformdb.ListChildMaterializationRunsRow) refreshrun.RunRecord {
	return materializationRunFromDB(materializationRunDBRow{
		ID: row.ID, WorkspaceID: row.WorkspaceID, Environment: row.Environment, ServingStateID: row.ServingStateID, ModelID: row.ModelID,
		PrincipalID: row.PrincipalID, PrincipalDisplayName: row.PrincipalDisplayName, TargetType: row.TargetType,
		TargetID: row.TargetID, TargetGeneration: row.TargetGeneration, TriggerType: row.TriggerType, ParentRunID: row.ParentRunID, RetryOf: row.RetryOf, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, Error: row.Error,
	})
}

func materializationRunFromLatestRow(row platformdb.LatestSuccessfulMaterializationRunRow) refreshrun.RunRecord {
	return materializationRunFromDB(materializationRunDBRow{
		ID: row.ID, WorkspaceID: row.WorkspaceID, Environment: row.Environment, ServingStateID: row.ServingStateID, ModelID: row.ModelID,
		PrincipalID: row.PrincipalID, PrincipalDisplayName: row.PrincipalDisplayName, TargetType: row.TargetType,
		TargetID: row.TargetID, TargetGeneration: row.TargetGeneration, TriggerType: row.TriggerType, ParentRunID: row.ParentRunID, RetryOf: row.RetryOf, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, Error: row.Error,
	})
}

func materializationRunFromListRow(row platformdb.ListMaterializationRunsRow) refreshrun.RunRecord {
	return materializationRunFromDB(materializationRunDBRow{
		ID: row.ID, WorkspaceID: row.WorkspaceID, Environment: row.Environment, ServingStateID: row.ServingStateID, ModelID: row.ModelID,
		PrincipalID: row.PrincipalID, PrincipalDisplayName: row.PrincipalDisplayName, TargetType: row.TargetType,
		TargetID: row.TargetID, TargetGeneration: row.TargetGeneration, TriggerType: row.TriggerType, ParentRunID: row.ParentRunID, RetryOf: row.RetryOf, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, Error: row.Error,
	})
}

func materializationRunFromTargetListRow(row platformdb.ListTargetMaterializationRunsRow) refreshrun.RunRecord {
	return materializationRunFromDB(materializationRunDBRow{
		ID: row.ID, WorkspaceID: row.WorkspaceID, Environment: row.Environment, ServingStateID: row.ServingStateID, ModelID: row.ModelID,
		PrincipalID: row.PrincipalID, PrincipalDisplayName: row.PrincipalDisplayName, TargetType: row.TargetType,
		TargetID: row.TargetID, TargetGeneration: row.TargetGeneration, TriggerType: row.TriggerType, ParentRunID: row.ParentRunID, RetryOf: row.RetryOf, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, Error: row.Error,
	})
}

func materializationRunFromDB(row materializationRunDBRow) refreshrun.RunRecord {
	run := refreshrun.RunRecord{
		ID: row.ID, WorkspaceID: row.WorkspaceID, Environment: row.Environment, ServingStateID: row.ServingStateID.String, ModelID: row.ModelID,
		PrincipalID: row.PrincipalID.String, PrincipalDisplayName: row.PrincipalDisplayName, TargetType: row.TargetType,
		TargetID: row.TargetID, TargetGeneration: row.TargetGeneration, TriggerType: row.TriggerType, ParentRunID: row.ParentRunID.String, RetryOf: row.RetryOf.String, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt.String, Error: row.Error,
	}
	if run.Status == refreshrun.RunStatusQueued {
		run.StartedAt = ""
	}
	return run
}

type runPageCursor struct {
	CreatedAt string
	Sequence  int64
}

func (r *SQLRunRepository) runPageCursor(ctx context.Context, workspaceID, environment, targetType, targetID, runID string) (runPageCursor, bool, error) {
	row, err := r.q.GetMaterializationRunCursor(ctx, platformdb.GetMaterializationRunCursorParams{
		RunID: runID, WorkspaceID: workspaceID, Environment: environment, TargetType: targetType, TargetID: targetID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return runPageCursor{}, false, nil
		}
		return runPageCursor{}, false, err
	}
	return runPageCursor{CreatedAt: row.CreatedAt, Sequence: row.CreatedSequence}, true, nil
}

type normalizedRunInput struct {
	WorkspaceID      string
	Environment      string
	ModelID          string
	ServingStateID   string
	PrincipalID      string
	TargetType       string
	TargetID         string
	TargetGeneration int64
	TriggerType      string
	ParentRunID      string
	RetryOf          string
	JobKind          string
	PayloadJSON      string
}

func normalizeRunInput(input refreshrun.RunInput) (normalizedRunInput, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	environment := string(servingstate.NormalizeEnvironment(servingstate.Environment(strings.TrimSpace(input.Environment))))
	modelID := strings.TrimSpace(input.ModelID)
	servingStateID := strings.TrimSpace(input.ServingStateID)
	principalID := strings.TrimSpace(input.PrincipalID)
	targetType := strings.TrimSpace(input.TargetType)
	targetID := strings.TrimSpace(input.TargetID)
	if input.TargetGeneration < 0 {
		return normalizedRunInput{}, fmt.Errorf("target generation cannot be negative")
	}
	triggerType := strings.TrimSpace(input.TriggerType)
	parentRunID := strings.TrimSpace(input.ParentRunID)
	retryOf := strings.TrimSpace(input.RetryOf)
	jobKind := strings.TrimSpace(input.JobKind)
	payloadJSON := strings.TrimSpace(input.PayloadJSON)
	if workspaceID == "" {
		return normalizedRunInput{}, fmt.Errorf("workspace id is required")
	}
	if modelID == "" {
		return normalizedRunInput{}, fmt.Errorf("model id is required")
	}
	if payloadJSON == "" {
		payloadJSON = "{}"
	}
	if err := validateRunTarget(targetType, targetID); err != nil {
		return normalizedRunInput{}, err
	}
	if err := validateRunTrigger(triggerType); err != nil {
		return normalizedRunInput{}, err
	}
	if err := validateJobKind(jobKind); err != nil {
		return normalizedRunInput{}, err
	}
	if parentRunID == "" {
		if targetType != refreshrun.TargetRefreshPipeline || jobKind != refreshrun.JobKindRefreshPipeline {
			return normalizedRunInput{}, fmt.Errorf("root refresh runs must target a refresh pipeline")
		}
		if triggerType == refreshrun.TriggerDependency {
			return normalizedRunInput{}, fmt.Errorf("root refresh runs cannot use dependency trigger")
		}
		if servingStateID == "" {
			return normalizedRunInput{}, fmt.Errorf("root refresh run serving state id is required")
		}
	} else {
		if targetType != refreshrun.TargetModelTable || triggerType != refreshrun.TriggerDependency || jobKind != refreshrun.JobKindChildRun {
			return normalizedRunInput{}, fmt.Errorf("child refresh tasks must be model-table dependencies")
		}
		if retryOf != "" {
			return normalizedRunInput{}, fmt.Errorf("child refresh tasks cannot be retries")
		}
	}
	if retryOf != "" && triggerType != refreshrun.TriggerRetry {
		return normalizedRunInput{}, fmt.Errorf("retry refresh runs must use retry trigger")
	}
	if triggerType == refreshrun.TriggerRetry && retryOf == "" {
		return normalizedRunInput{}, fmt.Errorf("retry trigger requires retry_of")
	}
	return normalizedRunInput{
		WorkspaceID:      workspaceID,
		Environment:      environment,
		ModelID:          modelID,
		ServingStateID:   servingStateID,
		PrincipalID:      principalID,
		TargetType:       targetType,
		TargetID:         targetID,
		TargetGeneration: input.TargetGeneration,
		TriggerType:      triggerType,
		ParentRunID:      parentRunID,
		RetryOf:          retryOf,
		JobKind:          jobKind,
		PayloadJSON:      payloadJSON,
	}, nil
}

func environmentForPage(page refreshrun.RunPage) string {
	return string(servingstate.NormalizeEnvironment(servingstate.Environment(strings.TrimSpace(page.Environment))))
}

func validateRunTarget(targetType, targetID string) error {
	switch targetType {
	case refreshrun.TargetModelTable, refreshrun.TargetRefreshPipeline:
	default:
		return fmt.Errorf("unsupported materialization target type %q", targetType)
	}
	if targetID == "" {
		return fmt.Errorf("target id is required")
	}
	return nil
}

func validateRunTrigger(triggerType string) error {
	switch triggerType {
	case refreshrun.TriggerDependency, refreshrun.TriggerManual, refreshrun.TriggerSchedule, refreshrun.TriggerRetry:
		return nil
	default:
		return fmt.Errorf("unsupported materialization trigger type %q", triggerType)
	}
}

func validateJobKind(kind string) error {
	switch kind {
	case refreshrun.JobKindRefreshPipeline, refreshrun.JobKindChildRun:
		return nil
	default:
		return fmt.Errorf("unsupported materialization job kind %q", kind)
	}
}

func sqliteLeaseModifier(duration time.Duration) string {
	seconds := int(duration.Seconds())
	if seconds <= 0 {
		seconds = 60
	}
	return fmt.Sprintf("+%d seconds", seconds)
}

func pageRuns(rows []refreshrun.RunRecord, page refreshrun.RunPage) []refreshrun.RunRecord {
	limit := runPageLimit(page)
	start := 0
	after := strings.TrimSpace(page.After)
	if after != "" {
		start = len(rows)
		for i, row := range rows {
			if row.ID == after {
				start = i + 1
				break
			}
		}
	}
	if start >= len(rows) {
		return []refreshrun.RunRecord{}
	}
	end := start + limit
	if end > len(rows) {
		end = len(rows)
	}
	return append([]refreshrun.RunRecord(nil), rows[start:end]...)
}

func runPageLimit(page refreshrun.RunPage) int {
	limit := page.Limit
	if limit <= 0 || limit > 100 {
		return 100
	}
	return limit
}

func newRunID(prefix string) string {
	return prefix + "_" + newRunSecret()[:24]
}

func newRunSecret() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		sum := sha256.Sum256([]byte(time.Now().Format(time.RFC3339Nano)))
		return hex.EncodeToString(sum[:])
	}
	return hex.EncodeToString(b[:])
}
