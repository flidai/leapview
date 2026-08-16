package sqlite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/flidai/leapview/internal/platform/jobs"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	platformdb "github.com/flidai/leapview/internal/refresh/internal/db"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
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
		if normalized.TriggerType != refreshrun.TriggerSchedule || occurrence.Identity != normalized.Identity ||
			normalized.TargetID != occurrence.PipelineID || occurrence.ArtifactDigest == "" || occurrence.ScheduledAt.IsZero() {
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
			ProjectID: normalized.Identity.ProjectID.String(), GenerationID: normalized.Identity.GenerationID, Environment: normalized.Identity.Environment,
			TargetType: normalized.TargetType, TargetID: normalized.TargetID.String(),
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
	groupIDsJSON, err := json.Marshal(normalized.GroupIDs)
	if err != nil {
		return refreshrun.RunRecord{}, fmt.Errorf("encode refresh group ids: %w", err)
	}
	if err := q.CreateRefreshJob(ctx, platformdb.CreateRefreshJobParams{
		ID: jobID, ProjectID: normalized.Identity.ProjectID.String(), GenerationID: normalized.Identity.GenerationID,
		SemanticModelID: normalized.SemanticModelID.String(), PipelineID: normalized.PipelineID.String(), PrincipalID: normalized.PrincipalID,
		GroupIdsJson: string(groupIDsJSON), EstimatedMemoryBytes: normalized.EstimatedMemoryBytes,
		Kind: normalized.JobKind, PayloadJson: normalized.PayloadJSON, Status: refreshrun.RunStatusQueued,
	}); err != nil {
		return refreshrun.RunRecord{}, err
	}
	if err := q.CreateRefreshJobRun(ctx, platformdb.CreateRefreshJobRunParams{
		ID: runID, JobID: jobID, PrincipalID: normalized.PrincipalID, Environment: normalized.Identity.Environment, TargetType: normalized.TargetType,
		TargetID: normalized.TargetID.String(), TriggerType: normalized.TriggerType, ParentRunID: normalized.ParentRunID,
		RetryOf: normalized.RetryOf, Status: refreshrun.RunStatusQueued, TargetRevision: normalized.TargetRevision,
	}); err != nil {
		return refreshrun.RunRecord{}, err
	}
	if occurrence != nil {
		result, err := q.AttachRefreshPipelineRun(ctx, platformdb.AttachRefreshPipelineRunParams{
			RunID: sql.NullString{String: runID, Valid: true}, ProjectID: occurrence.Identity.ProjectID.String(),
			Environment: occurrence.Identity.Environment, PipelineID: occurrence.PipelineID.String(), GenerationID: occurrence.Identity.GenerationID,
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
			Id: runID, ProjectId: normalized.Identity.ProjectID.String(),
			PipelineId: normalized.TargetID.String(), SemanticModel: normalized.SemanticModelID.String(), Trigger: normalized.TriggerType,
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
	return r.GetRun(ctx, normalized.Identity, runID)
}

func (r *SQLRunRepository) ClaimNextExecutableJob(ctx context.Context, identity projectgraph.ServingIdentity, owner string, lease time.Duration) (refreshrun.JobRecord, bool, error) {
	candidates, err := r.ListExecutableJobs(ctx, identity, 1)
	if err != nil || len(candidates) == 0 {
		return refreshrun.JobRecord{}, false, err
	}
	return r.ClaimExecutableJob(ctx, candidates[0], owner, lease)
}

func (r *SQLRunRepository) ListExecutableJobs(ctx context.Context, identity projectgraph.ServingIdentity, limit int) ([]refreshrun.JobRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("refresh run database is required")
	}
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 16
	}
	rows, err := r.q.ListExecutableRefreshJobHeads(ctx, platformdb.ListExecutableRefreshJobHeadsParams{
		ResultLimit: int64(limit), RefreshPipelineKind: refreshrun.JobKindRefreshPipeline,
		ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, Environment: identity.Environment, QueuedStatus: refreshrun.RunStatusQueued,
		RunQueuedStatus: refreshrun.RunStatusQueued, RunningStatus: refreshrun.RunStatusRunning,
	})
	if err != nil {
		return nil, err
	}
	jobs := make([]refreshrun.JobRecord, 0, len(rows))
	for _, row := range rows {
		rowIdentity, identityErr := projectgraph.NewServingIdentity(projectgraph.ResourceID(row.ProjectID), row.Environment, row.GenerationID)
		if identityErr != nil || rowIdentity != identity {
			return nil, fmt.Errorf("invalid persisted refresh job serving identity for %s", row.ID)
		}
		pipelineID := projectgraph.ResourceID(row.PipelineID)
		var groupIDs []string
		if err := json.Unmarshal([]byte(row.GroupIdsJson), &groupIDs); err != nil {
			return nil, fmt.Errorf("invalid persisted refresh job group ids for %s: %w", row.ID, err)
		}
		canonicalGroups, marshalErr := json.Marshal(groupIDs)
		if marshalErr != nil || string(canonicalGroups) != row.GroupIdsJson || groupIDs == nil {
			return nil, fmt.Errorf("invalid persisted refresh job group ids for %s", row.ID)
		}
		if err := refreshrun.ValidateGroupIDs(groupIDs); err != nil {
			return nil, fmt.Errorf("invalid persisted refresh job group ids for %s: %w", row.ID, err)
		}
		jobs = append(jobs, refreshrun.JobRecord{
			ID: row.ID, Identity: rowIdentity, PipelineID: pipelineID, SemanticModelID: projectgraph.ResourceID(row.SemanticModelID), PrincipalID: row.PrincipalID,
			GroupIDs: groupIDs, EstimatedMemoryBytes: row.EstimatedMemoryBytes,
			Kind: row.Kind, PayloadJSON: row.PayloadJson, RunID: row.RunID, TargetType: row.TargetType,
			TargetID: projectgraph.ResourceID(row.TargetID), TargetRevision: row.TargetRevision, TriggerType: row.TriggerType, AttemptCount: int(row.AttemptCount),
			LeaseOwner: row.LeaseOwner, LeaseRevision: row.LeaseRevision,
		})
		if err := jobs[len(jobs)-1].Validate(); err != nil {
			return nil, fmt.Errorf("invalid persisted refresh job %s: %w", row.ID, err)
		}
	}
	return jobs, nil
}

func (r *SQLRunRepository) ClaimExecutableJob(ctx context.Context, job refreshrun.JobRecord, owner string, lease time.Duration) (refreshrun.JobRecord, bool, error) {
	if r == nil || r.db == nil {
		return refreshrun.JobRecord{}, false, fmt.Errorf("refresh run database is required")
	}
	if owner == "" || owner != strings.TrimSpace(owner) {
		return refreshrun.JobRecord{}, false, fmt.Errorf("lease owner is required")
	}
	if err := job.Validate(); err != nil {
		return refreshrun.JobRecord{}, false, err
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
		ID: job.ID, ProjectID: job.Identity.ProjectID.String(), GenerationID: job.Identity.GenerationID,
		QueuedStatus: refreshrun.RunStatusQueued, PreviousRunningStatus: refreshrun.RunStatusRunning,
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
	if err := q.MarkRefreshJobRunClaimed(ctx, platformdb.MarkRefreshJobRunClaimedParams{
		Status: refreshrun.RunStatusRunning, ID: job.RunID, ProjectID: job.Identity.ProjectID.String(), GenerationID: job.Identity.GenerationID, Environment: job.Identity.Environment,
	}); err != nil {
		return refreshrun.JobRecord{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return refreshrun.JobRecord{}, false, err
	}
	job.AttemptCount++
	job.LeaseOwner = owner
	job.LeaseRevision++
	return job, true, nil
}

func (r *SQLRunRepository) RenewJobLease(ctx context.Context, job refreshrun.JobRecord, lease time.Duration) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("refresh run database is required")
	}
	if err := job.Validate(); err != nil {
		return err
	}
	changed, err := r.q.RenewRefreshJobLease(ctx, platformdb.RenewRefreshJobLeaseParams{
		LeaseModifier: sqliteLeaseModifier(lease), ID: job.ID,
		LeaseOwner: job.LeaseOwner, ProjectID: job.Identity.ProjectID.String(), GenerationID: job.Identity.GenerationID,
		LeaseRevision: job.LeaseRevision, Status: refreshrun.RunStatusRunning,
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
		RunID: job.RunID, ProjectID: job.Identity.ProjectID.String(), GenerationID: job.Identity.GenerationID, Environment: job.Identity.Environment,
		LeaseOwner: job.LeaseOwner, LeaseRevision: job.LeaseRevision,
	})
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	if changed != 1 {
		return refreshrun.RunRecord{}, refreshrun.ErrLeaseLost
	}
	return r.GetRun(ctx, job.Identity, job.RunID)
}

func (r *SQLRunRepository) RunMayPublish(ctx context.Context, job refreshrun.JobRecord) (bool, error) {
	allowed, err := r.q.RefreshRunMayPublish(ctx, platformdb.RefreshRunMayPublishParams{
		RunID: job.RunID, ProjectID: job.Identity.ProjectID.String(), GenerationID: job.Identity.GenerationID, Environment: job.Identity.Environment, TargetRevision: job.TargetRevision,
		LeaseOwner: job.LeaseOwner, LeaseRevision: job.LeaseRevision,
	})
	return allowed == 1, err
}

func (r *SQLRunRepository) JobQueueStats(ctx context.Context, identity projectgraph.ServingIdentity) (refreshrun.JobQueueStats, error) {
	if r == nil || r.db == nil {
		return refreshrun.JobQueueStats{}, fmt.Errorf("refresh run database is required")
	}
	if err := identity.Validate(); err != nil {
		return refreshrun.JobQueueStats{}, err
	}
	row, err := r.q.GetRefreshJobQueueStats(ctx, platformdb.GetRefreshJobQueueStatsParams{
		QueuedStatus: refreshrun.RunStatusQueued, RunningStatus: refreshrun.RunStatusRunning,
		StaleRunningStatus: refreshrun.RunStatusRunning, RefreshPipelineKind: refreshrun.JobKindRefreshPipeline,
		ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, Environment: identity.Environment,
	})
	if err != nil {
		return refreshrun.JobQueueStats{}, err
	}
	return refreshrun.JobQueueStats{QueuedJobs: int(row.QueuedJobs), RunningJobs: int(row.RunningJobs), StaleLeasedJobs: int(row.StaleLeasedJobs)}, nil
}

func (r *SQLRunRepository) GetRun(ctx context.Context, identity projectgraph.ServingIdentity, runID string) (refreshrun.RunRecord, error) {
	if err := identity.Validate(); err != nil {
		return refreshrun.RunRecord{}, err
	}
	if runID == "" || runID != strings.TrimSpace(runID) {
		return refreshrun.RunRecord{}, fmt.Errorf("run id is required")
	}
	row, err := r.q.GetMaterializationRun(ctx, platformdb.GetMaterializationRunParams{RunID: runID, ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, Environment: identity.Environment})
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	return materializationRunFromGetRow(row)
}

func (r *SQLRunRepository) ListRuns(ctx context.Context, identity projectgraph.ServingIdentity, page refreshrun.RunPage) ([]refreshrun.RunRecord, error) {
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	limit := runPageLimit(page)
	cursor := runPageCursor{}
	after := strings.TrimSpace(page.After)
	if after != "" {
		resolved, ok, err := r.runPageCursor(ctx, identity, "", "", after)
		if err != nil {
			return nil, err
		}
		if !ok {
			return []refreshrun.RunRecord{}, nil
		}
		cursor = resolved
	}
	rows, err := r.q.ListMaterializationRuns(ctx, platformdb.ListMaterializationRunsParams{
		ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, Environment: identity.Environment, CursorCreatedAt: cursor.CreatedAt, CursorSequence: cursor.Sequence, Limit: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]refreshrun.RunRecord, 0, len(rows))
	for _, row := range rows {
		run, mapErr := materializationRunFromListRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		out = append(out, run)
	}
	return out, nil
}

func (r *SQLRunRepository) ListTargetRuns(ctx context.Context, identity projectgraph.ServingIdentity, targetType string, targetID projectgraph.ResourceID, page refreshrun.RunPage) ([]refreshrun.RunRecord, error) {
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	if targetType != refreshrun.TargetModelTable && targetType != refreshrun.TargetRefreshPipeline {
		return nil, fmt.Errorf("target type is required")
	}
	if err := targetID.Validate(); err != nil {
		return nil, err
	}
	limit := runPageLimit(page)
	cursor := runPageCursor{}
	after := strings.TrimSpace(page.After)
	if after != "" {
		resolved, ok, err := r.runPageCursor(ctx, identity, targetType, targetID.String(), after)
		if err != nil {
			return nil, err
		}
		if !ok {
			return []refreshrun.RunRecord{}, nil
		}
		cursor = resolved
	}
	rows, err := r.q.ListTargetMaterializationRuns(ctx, platformdb.ListTargetMaterializationRunsParams{
		ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, Environment: identity.Environment, TargetType: targetType, TargetID: targetID.String(),
		CursorCreatedAt: cursor.CreatedAt, CursorSequence: cursor.Sequence, Limit: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]refreshrun.RunRecord, 0, len(rows))
	for _, row := range rows {
		run, mapErr := materializationRunFromTargetListRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		out = append(out, run)
	}
	return out, nil
}

func (r *SQLRunRepository) ListChildRuns(ctx context.Context, identity projectgraph.ServingIdentity, parentRunID string) ([]refreshrun.RunRecord, error) {
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	if parentRunID == "" || parentRunID != strings.TrimSpace(parentRunID) {
		return nil, fmt.Errorf("parent run id is required")
	}
	rows, err := r.q.ListChildMaterializationRuns(ctx, platformdb.ListChildMaterializationRunsParams{
		ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, Environment: identity.Environment, ParentRunID: sql.NullString{String: parentRunID, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	out := make([]refreshrun.RunRecord, 0, len(rows))
	for _, row := range rows {
		run, mapErr := materializationRunFromChildRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		out = append(out, run)
	}
	return out, nil
}

func (r *SQLRunRepository) LatestTargetRun(ctx context.Context, identity projectgraph.ServingIdentity, targetType string, targetID projectgraph.ResourceID) (refreshrun.RunRecord, bool, error) {
	runs, err := r.ListTargetRuns(ctx, identity, targetType, targetID, refreshrun.RunPage{Limit: 1})
	if err != nil {
		return refreshrun.RunRecord{}, false, err
	}
	if len(runs) == 0 {
		return refreshrun.RunRecord{}, false, nil
	}
	return runs[0], true, nil
}

func (r *SQLRunRepository) LatestSuccessfulTargetRun(ctx context.Context, identity projectgraph.ServingIdentity, targetType string, targetID projectgraph.ResourceID) (refreshrun.RunRecord, bool, error) {
	if err := identity.Validate(); err != nil {
		return refreshrun.RunRecord{}, false, err
	}
	if targetType != refreshrun.TargetModelTable && targetType != refreshrun.TargetRefreshPipeline {
		return refreshrun.RunRecord{}, false, fmt.Errorf("target type is required")
	}
	if err := targetID.Validate(); err != nil {
		return refreshrun.RunRecord{}, false, err
	}
	row, err := r.q.LatestSuccessfulMaterializationRun(ctx, platformdb.LatestSuccessfulMaterializationRunParams{
		ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, Environment: identity.Environment,
		TargetType: targetType, TargetID: targetID.String(), Status: refreshrun.RunStatusSucceeded,
	})
	if err == sql.ErrNoRows {
		return refreshrun.RunRecord{}, false, nil
	}
	if err != nil {
		return refreshrun.RunRecord{}, false, err
	}
	run, mapErr := materializationRunFromLatestRow(row)
	if mapErr != nil {
		return refreshrun.RunRecord{}, false, mapErr
	}
	return run, true, nil
}

func (r *SQLRunRepository) MarkRunRunning(ctx context.Context, identity projectgraph.ServingIdentity, runID string) (refreshrun.RunRecord, error) {
	return r.markRun(ctx, identity, runID, refreshrun.RunStatusRunning, "")
}

func (r *SQLRunRepository) MarkRunSucceeded(ctx context.Context, identity projectgraph.ServingIdentity, runID string) (refreshrun.RunRecord, error) {
	return r.markRun(ctx, identity, runID, refreshrun.RunStatusSucceeded, "")
}

func (r *SQLRunRepository) MarkRunFailed(ctx context.Context, identity projectgraph.ServingIdentity, runID, message string) (refreshrun.RunRecord, error) {
	return r.markRun(ctx, identity, runID, refreshrun.RunStatusFailed, message)
}

func (r *SQLRunRepository) MarkRunSucceededClaimed(ctx context.Context, job refreshrun.JobRecord) (refreshrun.RunRecord, error) {
	return r.markRunClaimed(ctx, job, refreshrun.RunStatusSucceeded, "")
}

func (r *SQLRunRepository) MarkRunFailedClaimed(ctx context.Context, job refreshrun.JobRecord, message string) (refreshrun.RunRecord, error) {
	if err := r.MarkRunTreeFailedClaimed(ctx, job, message); err != nil {
		return refreshrun.RunRecord{}, err
	}
	return r.GetRun(ctx, job.Identity, job.RunID)
}

func (r *SQLRunRepository) MarkRunTreeFailedClaimed(ctx context.Context, job refreshrun.JobRecord, message string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("refresh run database is required")
	}
	if err := job.Validate(); err != nil || job.LeaseOwner == "" || job.LeaseRevision <= 0 {
		return refreshrun.ErrLeaseLost
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	args := platformdb.MarkRefreshRunTreeFailedClaimedParams{RunID: job.RunID, ProjectID: job.Identity.ProjectID.String(), GenerationID: job.Identity.GenerationID, Environment: job.Identity.Environment, LeaseOwner: job.LeaseOwner, LeaseRevision: job.LeaseRevision, ErrorMessage: message}
	expectedRuns, err := q.CountRefreshRunTreeClaimed(ctx, platformdb.CountRefreshRunTreeClaimedParams{
		RunID: job.RunID, ProjectID: job.Identity.ProjectID.String(), GenerationID: job.Identity.GenerationID, Environment: job.Identity.Environment,
	})
	if err != nil {
		return err
	}
	expectedJobs, err := q.CountRefreshJobTreeClaimed(ctx, platformdb.CountRefreshJobTreeClaimedParams{
		RunID: job.RunID, ProjectID: job.Identity.ProjectID.String(), GenerationID: job.Identity.GenerationID, Environment: job.Identity.Environment,
	})
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
	jobs, err := q.CompleteRefreshJobTreeFailedClaimed(ctx, platformdb.CompleteRefreshJobTreeFailedClaimedParams{RunID: job.RunID, ProjectID: job.Identity.ProjectID.String(), GenerationID: job.Identity.GenerationID, Environment: job.Identity.Environment, LeaseOwner: job.LeaseOwner, LeaseRevision: job.LeaseRevision, ErrorMessage: message})
	if err != nil {
		return err
	}
	if jobs != expectedJobs {
		return refreshrun.ErrLeaseLost
	}
	return tx.Commit()
}

func (r *SQLRunRepository) CancelRun(ctx context.Context, identity projectgraph.ServingIdentity, runID string) (refreshrun.RunRecord, error) {
	if err := identity.Validate(); err != nil || runID == "" || runID != strings.TrimSpace(runID) {
		return refreshrun.RunRecord{}, fmt.Errorf("serving identity and canonical run id are required")
	}
	prior, err := r.GetRun(ctx, identity, runID)
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	if prior.ParentRunID != "" || prior.TargetType != refreshrun.TargetRefreshPipeline || prior.Identity.GenerationID == "" {
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
		QueuedStatus: refreshrun.RunStatusQueued, ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, Environment: identity.Environment,
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
		if _, getErr := r.GetRun(ctx, identity, runID); getErr != nil {
			return refreshrun.RunRecord{}, getErr
		}
		return refreshrun.RunRecord{}, refreshrun.ErrRunNotCancellable
	}
	if err := q.CancelQueuedChildMaterializationRuns(ctx, platformdb.CancelQueuedChildMaterializationRunsParams{
		CancelledStatus: refreshrun.RunStatusCancelled, ParentRunID: sql.NullString{String: runID, Valid: true},
		QueuedStatus: refreshrun.RunStatusQueued, ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, Environment: identity.Environment,
	}); err != nil {
		return refreshrun.RunRecord{}, err
	}
	if err := q.CancelQueuedChildRefreshJobs(ctx, platformdb.CancelQueuedChildRefreshJobsParams{
		CancelledStatus: refreshrun.RunStatusCancelled, ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, Environment: identity.Environment,
		QueuedStatus: refreshrun.RunStatusQueued, ParentRunID: sql.NullString{String: runID, Valid: true},
	}); err != nil {
		return refreshrun.RunRecord{}, err
	}
	if err := q.CancelQueuedRefreshJobForRun(ctx, platformdb.CancelQueuedRefreshJobForRunParams{
		CancelledStatus: refreshrun.RunStatusCancelled, RunID: runID,
		ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, Environment: identity.Environment, QueuedStatus: refreshrun.RunStatusQueued,
	}); err != nil {
		return refreshrun.RunRecord{}, err
	}
	failed, err := q.FailCancelledRefreshCandidate(ctx, prior.Identity.GenerationID)
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
	return r.GetRun(ctx, identity, runID)
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

func (r *SQLRunRepository) markRun(ctx context.Context, identity projectgraph.ServingIdentity, runID, status, message string) (refreshrun.RunRecord, error) {
	if err := identity.Validate(); err != nil || runID == "" || runID != strings.TrimSpace(runID) {
		return refreshrun.RunRecord{}, fmt.Errorf("serving identity and canonical run id are required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	params := platformdb.MarkMaterializationRunActiveParams{Status: status, ErrorMessage: message, RunID: runID, ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, Environment: identity.Environment}
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
		err = q.CompleteRefreshJobSucceeded(ctx, platformdb.CompleteRefreshJobSucceededParams{RunID: runID, ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, Environment: identity.Environment})
	case refreshrun.RunStatusFailed:
		err = q.CompleteRefreshJobFailed(ctx, platformdb.CompleteRefreshJobFailedParams{ErrorMessage: message, RunID: runID, ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, Environment: identity.Environment})
	default:
		err = q.UpdateRefreshJobForActiveRun(ctx, platformdb.UpdateRefreshJobForActiveRunParams{NewStatus: status, RunID: runID, ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, Environment: identity.Environment})
	}
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return refreshrun.RunRecord{}, err
	}
	return r.GetRun(ctx, identity, runID)
}

func (r *SQLRunRepository) markRunClaimed(ctx context.Context, job refreshrun.JobRecord, status, message string) (refreshrun.RunRecord, error) {
	if r == nil || r.db == nil {
		return refreshrun.RunRecord{}, fmt.Errorf("refresh run database is required")
	}
	if err := job.Validate(); err != nil || job.LeaseOwner == "" || job.LeaseRevision <= 0 {
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
		RunID: job.RunID, ProjectID: job.Identity.ProjectID.String(), GenerationID: job.Identity.GenerationID, Environment: job.Identity.Environment,
		LeaseOwner: job.LeaseOwner, LeaseRevision: job.LeaseRevision,
	}
	var affected int64
	if status == refreshrun.RunStatusSucceeded {
		affected, err = q.MarkRefreshRunSucceededClaimed(ctx, params)
	} else {
		affected, err = q.MarkRefreshRunFailedClaimed(ctx, platformdb.MarkRefreshRunFailedClaimedParams{
			RunID: job.RunID, ProjectID: job.Identity.ProjectID.String(), GenerationID: job.Identity.GenerationID, Environment: job.Identity.Environment,
			LeaseOwner: job.LeaseOwner, LeaseRevision: job.LeaseRevision, ErrorMessage: message,
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
			RunID: job.RunID, ProjectID: job.Identity.ProjectID.String(), GenerationID: job.Identity.GenerationID, Environment: job.Identity.Environment,
			LeaseOwner: job.LeaseOwner, LeaseRevision: job.LeaseRevision,
		})
	} else {
		affected, err = q.CompleteRefreshJobFailedClaimed(ctx, platformdb.CompleteRefreshJobFailedClaimedParams{
			RunID: job.RunID, ProjectID: job.Identity.ProjectID.String(), GenerationID: job.Identity.GenerationID, Environment: job.Identity.Environment,
			LeaseOwner: job.LeaseOwner, LeaseRevision: job.LeaseRevision, ErrorMessage: message,
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
	return r.GetRun(ctx, job.Identity, job.RunID)
}

type materializationRunDBRow struct {
	ID                   string
	ProjectID            string
	Environment          string
	GenerationID         string
	SemanticModelID      string
	PipelineID           string
	PrincipalID          sql.NullString
	PrincipalDisplayName string
	TargetType           string
	TargetID             string
	TargetRevision       int64
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

func materializationRunFromGetRow(row platformdb.GetMaterializationRunRow) (refreshrun.RunRecord, error) {
	return materializationRunFromDB(materializationRunDBRow{
		ID: row.ID, ProjectID: row.ProjectID, Environment: row.Environment, GenerationID: row.GenerationID, SemanticModelID: row.SemanticModelID, PipelineID: row.PipelineID,
		PrincipalID: row.PrincipalID, PrincipalDisplayName: row.PrincipalDisplayName, TargetType: row.TargetType,
		TargetID: row.TargetID, TargetRevision: row.TargetRevision, TriggerType: row.TriggerType, ParentRunID: row.ParentRunID, RetryOf: row.RetryOf, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, Error: row.Error,
	})
}

func materializationRunFromChildRow(row platformdb.ListChildMaterializationRunsRow) (refreshrun.RunRecord, error) {
	return materializationRunFromDB(materializationRunDBRow{
		ID: row.ID, ProjectID: row.ProjectID, Environment: row.Environment, GenerationID: row.GenerationID, SemanticModelID: row.SemanticModelID, PipelineID: row.PipelineID,
		PrincipalID: row.PrincipalID, PrincipalDisplayName: row.PrincipalDisplayName, TargetType: row.TargetType,
		TargetID: row.TargetID, TargetRevision: row.TargetRevision, TriggerType: row.TriggerType, ParentRunID: row.ParentRunID, RetryOf: row.RetryOf, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, Error: row.Error,
	})
}

func materializationRunFromLatestRow(row platformdb.LatestSuccessfulMaterializationRunRow) (refreshrun.RunRecord, error) {
	return materializationRunFromDB(materializationRunDBRow{
		ID: row.ID, ProjectID: row.ProjectID, Environment: row.Environment, GenerationID: row.GenerationID, SemanticModelID: row.SemanticModelID, PipelineID: row.PipelineID,
		PrincipalID: row.PrincipalID, PrincipalDisplayName: row.PrincipalDisplayName, TargetType: row.TargetType,
		TargetID: row.TargetID, TargetRevision: row.TargetRevision, TriggerType: row.TriggerType, ParentRunID: row.ParentRunID, RetryOf: row.RetryOf, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, Error: row.Error,
	})
}

func materializationRunFromListRow(row platformdb.ListMaterializationRunsRow) (refreshrun.RunRecord, error) {
	return materializationRunFromDB(materializationRunDBRow{
		ID: row.ID, ProjectID: row.ProjectID, Environment: row.Environment, GenerationID: row.GenerationID, SemanticModelID: row.SemanticModelID, PipelineID: row.PipelineID,
		PrincipalID: row.PrincipalID, PrincipalDisplayName: row.PrincipalDisplayName, TargetType: row.TargetType,
		TargetID: row.TargetID, TargetRevision: row.TargetRevision, TriggerType: row.TriggerType, ParentRunID: row.ParentRunID, RetryOf: row.RetryOf, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, Error: row.Error,
	})
}

func materializationRunFromTargetListRow(row platformdb.ListTargetMaterializationRunsRow) (refreshrun.RunRecord, error) {
	return materializationRunFromDB(materializationRunDBRow{
		ID: row.ID, ProjectID: row.ProjectID, Environment: row.Environment, GenerationID: row.GenerationID, SemanticModelID: row.SemanticModelID, PipelineID: row.PipelineID,
		PrincipalID: row.PrincipalID, PrincipalDisplayName: row.PrincipalDisplayName, TargetType: row.TargetType,
		TargetID: row.TargetID, TargetRevision: row.TargetRevision, TriggerType: row.TriggerType, ParentRunID: row.ParentRunID, RetryOf: row.RetryOf, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, Error: row.Error,
	})
}

func materializationRunFromDB(row materializationRunDBRow) (refreshrun.RunRecord, error) {
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(row.ProjectID), row.Environment, row.GenerationID)
	if err != nil {
		return refreshrun.RunRecord{}, fmt.Errorf("invalid refresh run serving identity: %w", err)
	}
	run := refreshrun.RunRecord{
		ID: row.ID, Identity: identity, SemanticModelID: projectgraph.ResourceID(row.SemanticModelID), PipelineID: projectgraph.ResourceID(row.PipelineID),
		PrincipalID: row.PrincipalID.String, PrincipalDisplayName: row.PrincipalDisplayName, TargetType: row.TargetType,
		TargetID: projectgraph.ResourceID(row.TargetID), TargetRevision: row.TargetRevision, TriggerType: row.TriggerType, ParentRunID: row.ParentRunID.String, RetryOf: row.RetryOf.String, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt.String, Error: row.Error,
	}
	if run.Status == refreshrun.RunStatusQueued {
		run.StartedAt = ""
	}
	if err := validateMappedRun(run); err != nil {
		return refreshrun.RunRecord{}, err
	}
	return run, nil
}

func validateMappedRun(run refreshrun.RunRecord) error {
	if err := validateStoredID(run.ID, "run id", true); err != nil {
		return err
	}
	if err := run.Identity.Validate(); err != nil {
		return err
	}
	if err := run.SemanticModelID.Validate(); err != nil {
		return fmt.Errorf("invalid refresh run semantic model id: %w", err)
	}
	if err := run.PipelineID.Validate(); err != nil {
		return fmt.Errorf("invalid refresh run pipeline id: %w", err)
	}
	if err := run.TargetID.Validate(); err != nil {
		return fmt.Errorf("invalid refresh run target id: %w", err)
	}
	if run.TargetType != refreshrun.TargetModelTable && run.TargetType != refreshrun.TargetRefreshPipeline {
		return fmt.Errorf("unsupported refresh target type %q", run.TargetType)
	}
	if run.TriggerType != refreshrun.TriggerDependency && run.TriggerType != refreshrun.TriggerManual && run.TriggerType != refreshrun.TriggerSchedule && run.TriggerType != refreshrun.TriggerRetry {
		return fmt.Errorf("unsupported refresh trigger type %q", run.TriggerType)
	}
	switch run.Status {
	case refreshrun.RunStatusQueued, refreshrun.RunStatusRunning, refreshrun.RunStatusPrepared, refreshrun.RunStatusSucceeded, refreshrun.RunStatusFailed, refreshrun.RunStatusCancelled, refreshrun.RunStatusSuperseded:
	default:
		return fmt.Errorf("unsupported refresh run status %q", run.Status)
	}
	if run.TargetType == refreshrun.TargetRefreshPipeline {
		if run.PipelineID != run.TargetID {
			return fmt.Errorf("refresh pipeline run pipeline id does not match target id")
		}
	} else if run.PipelineID == "" {
		return fmt.Errorf("model-table run pipeline id is required")
	}
	if err := validateStoredID(run.PrincipalID, "principal id", false); err != nil {
		return err
	}
	if err := validateStoredID(run.ParentRunID, "parent run id", false); err != nil {
		return err
	}
	if err := validateStoredID(run.RetryOf, "retry of", false); err != nil {
		return err
	}
	if run.TargetRevision < 0 {
		return fmt.Errorf("refresh target revision must not be negative")
	}
	return nil
}

func validateStoredID(value, name string, required bool) error {
	if !required && value == "" {
		return nil
	}
	if value == "" || value != strings.TrimSpace(value) || len(value) > 256 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("refresh %s is not canonical", name)
	}
	return nil
}

type runPageCursor struct {
	CreatedAt string
	Sequence  int64
}

func (r *SQLRunRepository) runPageCursor(ctx context.Context, identity projectgraph.ServingIdentity, targetType, targetID, runID string) (runPageCursor, bool, error) {
	row, err := r.q.GetMaterializationRunCursor(ctx, platformdb.GetMaterializationRunCursorParams{
		RunID: runID, ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID, Environment: identity.Environment, TargetType: targetType, TargetID: targetID,
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
	Identity             projectgraph.ServingIdentity
	SemanticModelID      projectgraph.ResourceID
	PipelineID           projectgraph.ResourceID
	PrincipalID          string
	TargetType           string
	TargetID             projectgraph.ResourceID
	TargetRevision       int64
	TriggerType          string
	ParentRunID          string
	RetryOf              string
	JobKind              string
	PayloadJSON          string
	GroupIDs             []string
	EstimatedMemoryBytes int64
}

func normalizeRunInput(input refreshrun.RunInput) (normalizedRunInput, error) {
	if err := input.Validate(); err != nil {
		return normalizedRunInput{}, err
	}
	payloadJSON := strings.TrimSpace(input.PayloadJSON)
	if payloadJSON == "" {
		payloadJSON = "{}"
	}
	if input.ParentRunID == "" {
		if input.TargetType != refreshrun.TargetRefreshPipeline || input.JobKind != refreshrun.JobKindRefreshPipeline {
			return normalizedRunInput{}, fmt.Errorf("root refresh runs must target a refresh pipeline")
		}
		if input.TriggerType == refreshrun.TriggerDependency {
			return normalizedRunInput{}, fmt.Errorf("root refresh runs cannot use dependency trigger")
		}
	} else {
		if input.TargetType != refreshrun.TargetModelTable || input.TriggerType != refreshrun.TriggerDependency || input.JobKind != refreshrun.JobKindChildRun {
			return normalizedRunInput{}, fmt.Errorf("child refresh tasks must be model-table dependencies")
		}
		if input.RetryOf != "" {
			return normalizedRunInput{}, fmt.Errorf("child refresh tasks cannot be retries")
		}
	}
	if input.RetryOf != "" && input.TriggerType != refreshrun.TriggerRetry {
		return normalizedRunInput{}, fmt.Errorf("retry refresh runs must use retry trigger")
	}
	if input.TriggerType == refreshrun.TriggerRetry && input.RetryOf == "" {
		return normalizedRunInput{}, fmt.Errorf("retry trigger requires retry_of")
	}
	return normalizedRunInput{
		Identity: input.Identity, SemanticModelID: input.SemanticModelID, PipelineID: input.PipelineID,
		PrincipalID: input.PrincipalID, GroupIDs: append([]string(nil), input.GroupIDs...), EstimatedMemoryBytes: input.EstimatedMemoryBytes,
		TargetType: input.TargetType, TargetID: input.TargetID,
		TargetRevision: input.TargetRevision, TriggerType: input.TriggerType, ParentRunID: input.ParentRunID,
		RetryOf: input.RetryOf, JobKind: input.JobKind, PayloadJSON: payloadJSON,
	}, nil
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
