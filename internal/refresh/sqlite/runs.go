package sqlite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/flidai/leapview/internal/access"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectpipelineplan "github.com/flidai/leapview/internal/project/contracts/pipelineplan"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	platformdb "github.com/flidai/leapview/internal/refresh/internal/db"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/pkg/jobs"
)

type SQLRunRepository struct {
	db        *sql.DB
	q         *platformdb.Queries
	workflow  jobplatform.WorkflowRecorder
	audit     access.AuditIntentRecorder
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

func NewSQLRunRepositoryWithWorkflow(db *sql.DB, workflow jobplatform.WorkflowRecorder, execution RunWorkflowConfig) *SQLRunRepository {
	return &SQLRunRepository{db: db, q: platformdb.New(db), workflow: workflow, execution: execution}
}

func NewSQLRunRepositoryWithWorkflowAndAudit(db *sql.DB, workflow jobplatform.WorkflowRecorder, execution RunWorkflowConfig, audit access.AuditIntentRecorder) *SQLRunRepository {
	return &SQLRunRepository{db: db, q: platformdb.New(db), workflow: workflow, execution: execution, audit: audit}
}

func (r *SQLRunRepository) ConfigureAuditIntentRecorder(audit access.AuditIntentRecorder) {
	if r != nil {
		r.audit = audit
	}
}

func (r *SQLRunRepository) recordAuditIntent(ctx context.Context, tx transaction.Transaction, intent *access.AuditIntent, resourceID string) error {
	if intent == nil {
		return nil
	}
	if r.audit == nil {
		return fmt.Errorf("refresh audit intent recorder is required")
	}
	copy := *intent
	resourceID = strings.TrimSpace(resourceID)
	copy.ResourceID = resourceID
	copy.AggregateKey = "refresh_run:" + resourceID
	if strings.Contains(strings.ToLower(copy.Operation), "create") {
		copy.AggregateSequence = 1
	} else {
		copy.AggregateSequence = 2
	}
	hash := sha256.Sum256([]byte(copy.Operation + "\x00" + copy.PrincipalID + "\x00" + copy.ResourceID + "\x00" + copy.RequestID))
	copy.EventID = "sha256:" + hex.EncodeToString(hash[:])
	return r.audit.RecordAuditIntent(ctx, tx, copy)
}

func (r *SQLRunRepository) CreateRun(ctx context.Context, input refreshrun.RunInput) (refreshrun.RunRecord, error) {
	return r.createRun(ctx, input, nil)
}

func (r *SQLRunRepository) CreateScheduledRun(ctx context.Context, input refreshrun.RunInput, occurrence refreshschedule.Occurrence) (refreshrun.RunRecord, error) {
	return r.createRun(ctx, input, &occurrence)
}

// CheckInvocationAdmission is a non-mutating early guard for externally
// initiated invocations. CreateRun repeats the check in its transaction to
// retain the collision fence under concurrent dispatchers.
func (r *SQLRunRepository) CheckInvocationAdmission(ctx context.Context, identity projectgraph.ServingIdentity, pipelineID projectgraph.ResourceID, source string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("refresh run database is required")
	}
	if err := identity.Validate(); err != nil {
		return err
	}
	if err := pipelineID.Validate(); err != nil {
		return err
	}
	if source == refreshrun.TriggerSchedule {
		return nil
	}
	active, err := r.q.RefreshTargetHasActiveRun(ctx, platformdb.RefreshTargetHasActiveRunParams{
		ProjectID: identity.ProjectID.String(), Environment: identity.Environment,
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: pipelineID.String(),
	})
	if err != nil {
		return err
	}
	if active != 0 {
		return refreshrun.ErrInvocationConflict
	}
	return nil
}

// CheckScheduledInvocationAdmission reserves the terminal outcome for a
// schedule occurrence before candidate construction when an external root is
// active. CreateRun repeats this check transactionally for dispatcher races.
func (r *SQLRunRepository) CheckScheduledInvocationAdmission(ctx context.Context, occurrence refreshschedule.Occurrence) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("refresh run database is required")
	}
	if err := occurrence.Identity.Validate(); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	active, err := q.RefreshTargetHasActiveExternalRun(ctx, platformdb.RefreshTargetHasActiveExternalRunParams{
		ProjectID: occurrence.Identity.ProjectID.String(), Environment: occurrence.Identity.Environment,
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: occurrence.PipelineID.String(),
	})
	if err != nil {
		return err
	}
	if active == 0 {
		return tx.Commit()
	}
	affected, err := q.SkipRefreshPipelineOccurrence(ctx, platformdb.SkipRefreshPipelineOccurrenceParams{
		RunID: sql.NullString{}, Outcome: refreshrun.AdmissionDeniedExternalActive, TerminalReason: "external invocation active",
		ProjectID: occurrence.Identity.ProjectID.String(), Environment: occurrence.Identity.Environment,
		PipelineID: occurrence.PipelineID.String(), ScheduledAt: occurrence.ScheduledAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("scheduled refresh occurrence is not claimable")
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return errors.Join(refreshschedule.ErrOccurrenceSkipped, refreshrun.ErrAdmissionDeniedExternalActive)
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
			normalized.TargetID != occurrence.PipelineID ||
			normalized.NominalTime != occurrence.ScheduledAt.UTC().Format(time.RFC3339Nano) || occurrence.ArtifactDigest == "" || occurrence.ScheduledAt.IsZero() {
			return refreshrun.RunRecord{}, fmt.Errorf("scheduled refresh run does not match its claimed occurrence")
		}
		if !sameStrings(normalized.MatchingScheduleIDs, occurrence.MatchingScheduleIDs) {
			return refreshrun.RunRecord{}, fmt.Errorf("scheduled refresh run does not match occurrence schedule evidence")
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	admissionSkipped := false
	rootPipeline := normalized.ParentRunID == "" && normalized.TargetType == refreshrun.TargetRefreshPipeline
	source := normalized.InvocationSource
	if source == "" {
		source = normalized.TriggerType
	}
	if rootPipeline && source != refreshrun.TriggerSchedule {
		activeExternal, err := q.RefreshTargetHasActiveRun(ctx, platformdb.RefreshTargetHasActiveRunParams{
			ProjectID: normalized.Identity.ProjectID.String(), Environment: normalized.Identity.Environment,
			TargetType: normalized.TargetType, TargetID: normalized.TargetID.String(),
		})
		if err != nil {
			return refreshrun.RunRecord{}, err
		}
		if activeExternal != 0 {
			return refreshrun.RunRecord{}, refreshrun.ErrInvocationConflict
		}
	}
	if rootPipeline && source == refreshrun.TriggerSchedule {
		activeExternal, err := q.RefreshTargetHasActiveExternalRun(ctx, platformdb.RefreshTargetHasActiveExternalRunParams{
			ProjectID: normalized.Identity.ProjectID.String(), Environment: normalized.Identity.Environment,
			TargetType: normalized.TargetType, TargetID: normalized.TargetID.String(),
		})
		if err != nil {
			return refreshrun.RunRecord{}, err
		}
		if activeExternal != 0 {
			if occurrence == nil {
				return refreshrun.RunRecord{}, refreshrun.ErrAdmissionDeniedExternalActive
			}
			affected, updateErr := q.SkipRefreshPipelineOccurrence(ctx, platformdb.SkipRefreshPipelineOccurrenceParams{
				RunID: sql.NullString{}, Outcome: refreshrun.AdmissionDeniedExternalActive, TerminalReason: "external invocation active",
				ProjectID: occurrence.Identity.ProjectID.String(), Environment: occurrence.Identity.Environment,
				PipelineID: occurrence.PipelineID.String(), ScheduledAt: occurrence.ScheduledAt.UTC().Format(time.RFC3339Nano),
			})
			if updateErr != nil {
				return refreshrun.RunRecord{}, updateErr
			}
			if affected != 1 {
				return refreshrun.RunRecord{}, fmt.Errorf("scheduled refresh occurrence is not claimable")
			}
			if err := tx.Commit(); err != nil {
				return refreshrun.RunRecord{}, err
			}
			return refreshrun.RunRecord{Status: refreshrun.RunStatusSkipped, Error: refreshrun.AdmissionDeniedExternalActive}, errors.Join(refreshschedule.ErrOccurrenceSkipped, refreshrun.ErrAdmissionDeniedExternalActive)
		}
	}
	policy := normalized.ConcurrencyPolicy
	if rootPipeline && policy == refreshschedule.ConcurrencyForbid {
		active, err := q.RefreshTargetHasActiveRun(ctx, platformdb.RefreshTargetHasActiveRunParams{
			ProjectID: normalized.Identity.ProjectID.String(), Environment: normalized.Identity.Environment,
			TargetType: normalized.TargetType, TargetID: normalized.TargetID.String(),
		})
		if err != nil {
			return refreshrun.RunRecord{}, err
		}
		admissionSkipped = active != 0
	}
	if rootPipeline && source == refreshrun.TriggerSchedule && policy == refreshschedule.ConcurrencyReplace {
		target := platformdb.SupersedeRefreshTargetJobsParams{
			ProjectID: normalized.Identity.ProjectID.String(), Environment: normalized.Identity.Environment,
			TargetType: normalized.TargetType, TargetID: normalized.TargetID.String(),
		}
		if err := q.SupersedeRefreshTargetJobs(ctx, target); err != nil {
			return refreshrun.RunRecord{}, err
		}
		if err := q.SupersedeRefreshTargetOccurrences(ctx, platformdb.SupersedeRefreshTargetOccurrencesParams{
			ProjectID: target.ProjectID, Environment: target.Environment, TargetID: target.TargetID, TargetType: target.TargetType,
		}); err != nil {
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
	matchingScheduleIDsJSON, err := json.Marshal(normalized.MatchingScheduleIDs)
	if err != nil {
		return refreshrun.RunRecord{}, fmt.Errorf("encode refresh schedule evidence: %w", err)
	}
	admissionStatus := refreshrun.RunStatusQueued
	if admissionSkipped {
		admissionStatus = refreshrun.RunStatusSkipped
	}
	if err := q.CreateRefreshJob(ctx, platformdb.CreateRefreshJobParams{
		ID: jobID, ProjectID: normalized.Identity.ProjectID.String(), GenerationID: normalized.Identity.GenerationID,
		SemanticModelID: normalized.SemanticModelID.String(), PipelineID: normalized.PipelineID.String(), PrincipalID: normalized.PrincipalID,
		GroupIdsJson: string(groupIDsJSON), EstimatedMemoryBytes: normalized.EstimatedMemoryBytes,
		Kind: normalized.JobKind, PayloadJson: normalized.PayloadJSON, Status: admissionStatus,
	}); err != nil {
		return refreshrun.RunRecord{}, fmt.Errorf("create refresh job: %w", err)
	}
	if err := q.CreateRefreshJobRun(ctx, platformdb.CreateRefreshJobRunParams{
		ID: runID, JobID: jobID, PrincipalID: normalized.PrincipalID, Environment: normalized.Identity.Environment, TargetType: normalized.TargetType,
		TargetID: normalized.TargetID.String(), TriggerType: normalized.TriggerType, TriggerID: normalized.TriggerID, InvocationSource: normalized.InvocationSource, NominalTime: normalized.NominalTime,
		PlanDigest: normalized.PlanDigest, MaterializationScopeJson: normalized.MaterializationScopeJSON, MatchingScheduleIdsJson: string(matchingScheduleIDsJSON), ParentRunID: normalized.ParentRunID,
		Status: admissionStatus, TargetRevision: normalized.TargetRevision,
	}); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: refresh_job_runs") {
			return refreshrun.RunRecord{}, refreshrun.ErrTargetActive
		}
		return refreshrun.RunRecord{}, fmt.Errorf("create refresh job run: %w", err)
	}
	if occurrence != nil && admissionSkipped {
		affected, updateErr := q.SkipRefreshPipelineOccurrence(ctx, platformdb.SkipRefreshPipelineOccurrenceParams{
			RunID: sql.NullString{}, Outcome: "skipped", TerminalReason: "overlap forbidden", ProjectID: occurrence.Identity.ProjectID.String(),
			Environment: occurrence.Identity.Environment, PipelineID: occurrence.PipelineID.String(), ScheduledAt: occurrence.ScheduledAt.UTC().Format(time.RFC3339Nano),
		})
		if updateErr != nil {
			return refreshrun.RunRecord{}, updateErr
		}
		if affected != 1 {
			return refreshrun.RunRecord{}, fmt.Errorf("scheduled refresh occurrence is not claimable")
		}
	} else if occurrence != nil {
		result, err := q.AttachRefreshPipelineRun(ctx, platformdb.AttachRefreshPipelineRunParams{
			RunID: sql.NullString{String: runID, Valid: true}, ProjectID: occurrence.Identity.ProjectID.String(),
			Environment: occurrence.Identity.Environment, PipelineID: occurrence.PipelineID.String(),
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
			Id:                  runID,
			PipelineId:          normalized.TargetID.String(),
			SemanticModel:       normalized.SemanticModelID.String(),
			InvocationSource:    normalized.InvocationSource,
			MatchingScheduleIds: append([]string{}, normalized.MatchingScheduleIDs...),
			PlanDigest:          normalized.PlanDigest,
			Status:              admissionStatus,
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
	if normalized.ParentRunID == "" {
		intent := normalized.AuditIntent
		if intent != nil {
			copy := *intent
			copy.ResourceID = runID
			if data, encodeErr := refreshgen.EncodeGenCreateRefreshRunAuditPayload(refreshgen.GenSchemaRefreshQueuedAuditPayload{
				Id: runID, PipelineId: normalized.TargetID.String(), SemanticModel: normalized.SemanticModelID.String(),
				InvocationSource: normalized.InvocationSource, MatchingScheduleIds: append([]string{}, normalized.MatchingScheduleIDs...), PlanDigest: normalized.PlanDigest, Status: admissionStatus,
			}); encodeErr != nil {
				return refreshrun.RunRecord{}, encodeErr
			} else {
				copy.MetadataJSON = data
			}
			intent = &copy
		}
		if err := r.recordAuditIntent(ctx, tx, intent, runID); err != nil {
			return refreshrun.RunRecord{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return refreshrun.RunRecord{}, err
	}
	return r.getRunForIdentity(ctx, normalized.Identity, runID)
}

func (r *SQLRunRepository) ClaimNextExecutableJob(ctx context.Context, identity projectgraph.ServingIdentity, owner string, lease time.Duration) (refreshrun.JobRecord, bool, error) {
	if r == nil || r.db == nil {
		return refreshrun.JobRecord{}, false, fmt.Errorf("refresh run database is required")
	}
	if err := identity.Validate(); err != nil {
		return refreshrun.JobRecord{}, false, err
	}
	row, err := r.q.NextExecutableRefreshJob(ctx, platformdb.NextExecutableRefreshJobParams{
		RefreshPipelineKind: refreshrun.JobKindRefreshPipeline, ProjectID: identity.ProjectID.String(), GenerationID: identity.GenerationID,
		Environment: identity.Environment, QueuedStatus: refreshrun.RunStatusQueued, RunQueuedStatus: refreshrun.RunStatusQueued, RunningStatus: refreshrun.RunStatusRunning,
	})
	if err == sql.ErrNoRows {
		return refreshrun.JobRecord{}, false, nil
	}
	if err != nil {
		return refreshrun.JobRecord{}, false, err
	}
	rowIdentity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(row.ProjectID), row.Environment, row.GenerationID)
	if err != nil || rowIdentity != identity {
		return refreshrun.JobRecord{}, false, fmt.Errorf("invalid persisted refresh job serving identity for %s", row.ID)
	}
	var groupIDs []string
	if err := json.Unmarshal([]byte(row.GroupIdsJson), &groupIDs); err != nil {
		return refreshrun.JobRecord{}, false, fmt.Errorf("invalid persisted refresh job group ids for %s: %w", row.ID, err)
	}
	canonicalGroups, err := json.Marshal(groupIDs)
	if err != nil || string(canonicalGroups) != row.GroupIdsJson || groupIDs == nil {
		return refreshrun.JobRecord{}, false, fmt.Errorf("invalid persisted refresh job group ids for %s", row.ID)
	}
	if err := refreshrun.ValidateGroupIDs(groupIDs); err != nil {
		return refreshrun.JobRecord{}, false, fmt.Errorf("invalid persisted refresh job group ids for %s: %w", row.ID, err)
	}
	matchingScheduleIDs, err := decodeRunScheduleIDs(row.MatchingScheduleIdsJson)
	if err != nil {
		return refreshrun.JobRecord{}, false, fmt.Errorf("invalid persisted refresh schedule evidence for %s: %w", row.ID, err)
	}
	pipelinePlan, err := decodePersistedPipelinePlan(row.PayloadJson, row.PlanDigest, row.MaterializationScopeJson)
	if err != nil {
		return refreshrun.JobRecord{}, false, fmt.Errorf("invalid persisted refresh job plan for %s: %w", row.ID, err)
	}
	candidate := refreshrun.JobRecord{
		ID: row.ID, Identity: rowIdentity, PipelineID: projectgraph.ResourceID(row.PipelineID), SemanticModelID: projectgraph.ResourceID(row.SemanticModelID), PipelinePlan: pipelinePlan, InvocationSource: row.InvocationSource, MatchingScheduleIDs: matchingScheduleIDs, TriggerID: row.TriggerID, NominalTime: row.NominalTime, PrincipalID: row.PrincipalID,
		GroupIDs: groupIDs, EstimatedMemoryBytes: row.EstimatedMemoryBytes, Kind: row.Kind, PayloadJSON: row.PayloadJson, RunID: row.RunID,
		TargetType: row.TargetType, TargetID: projectgraph.ResourceID(row.TargetID), TargetRevision: row.TargetRevision, TriggerType: row.TriggerType, AttemptCount: int(row.AttemptCount), LeaseOwner: row.LeaseOwner, LeaseRevision: row.LeaseRevision,
	}
	if err := candidate.Validate(); err != nil {
		return refreshrun.JobRecord{}, false, fmt.Errorf("invalid persisted refresh job %s: %w", row.ID, err)
	}
	return r.ClaimExecutableJob(ctx, candidate, owner, lease)
}

func (r *SQLRunRepository) ListExecutableJobs(ctx context.Context, scope refreshrun.ReadScope, limit int) ([]refreshrun.JobRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("refresh run database is required")
	}
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 16
	}
	rows, err := r.q.ListExecutableRefreshJobHeads(ctx, platformdb.ListExecutableRefreshJobHeadsParams{
		ResultLimit: int64(limit), RefreshPipelineKind: refreshrun.JobKindRefreshPipeline,
		ProjectID: scope.ProjectID.String(), Environment: scope.Environment, QueuedStatus: refreshrun.RunStatusQueued,
		RunQueuedStatus: refreshrun.RunStatusQueued, RunningStatus: refreshrun.RunStatusRunning,
	})
	if err != nil {
		return nil, err
	}
	jobs := make([]refreshrun.JobRecord, 0, len(rows))
	for _, row := range rows {
		rowIdentity, identityErr := projectgraph.NewServingIdentity(projectgraph.ResourceID(row.ProjectID), row.Environment, row.GenerationID)
		if identityErr != nil {
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
		matchingScheduleIDs, decodeErr := decodeRunScheduleIDs(row.MatchingScheduleIdsJson)
		if decodeErr != nil {
			return nil, fmt.Errorf("invalid persisted refresh schedule evidence for %s: %w", row.ID, decodeErr)
		}
		pipelinePlan, planErr := decodePersistedPipelinePlan(row.PayloadJson, row.PlanDigest, row.MaterializationScopeJson)
		if planErr != nil {
			return nil, fmt.Errorf("invalid persisted refresh job plan for %s: %w", row.ID, planErr)
		}
		jobs = append(jobs, refreshrun.JobRecord{
			ID: row.ID, Identity: rowIdentity, PipelineID: pipelineID, SemanticModelID: projectgraph.ResourceID(row.SemanticModelID), PipelinePlan: pipelinePlan, InvocationSource: row.InvocationSource, MatchingScheduleIDs: matchingScheduleIDs, TriggerID: row.TriggerID, NominalTime: row.NominalTime, PrincipalID: row.PrincipalID,
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

func decodePersistedPipelinePlan(payloadJSON, planDigest, scopeJSON string) (*projectpipelineplan.Plan, error) {
	var payload struct {
		PipelinePlan *projectpipelineplan.Plan `json:"pipelinePlan"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return nil, err
	}
	if payload.PipelinePlan == nil {
		return nil, fmt.Errorf("pipeline plan is missing")
	}
	if err := payload.PipelinePlan.Validate(); err != nil {
		return nil, err
	}
	if payload.PipelinePlan.Digest != planDigest {
		return nil, fmt.Errorf("pipeline plan digest evidence does not match payload")
	}
	encodedScope, err := json.Marshal(payload.PipelinePlan.MaterializationScope)
	if err != nil {
		return nil, err
	}
	if string(encodedScope) != scopeJSON {
		return nil, fmt.Errorf("pipeline materialization scope evidence does not match payload")
	}
	return payload.PipelinePlan, nil
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
	return r.getRunForIdentity(ctx, job.Identity, job.RunID)
}

func (r *SQLRunRepository) RunMayPublish(ctx context.Context, job refreshrun.JobRecord) (bool, error) {
	allowed, err := r.q.RefreshRunMayPublish(ctx, platformdb.RefreshRunMayPublishParams{
		RunID: job.RunID, ProjectID: job.Identity.ProjectID.String(), GenerationID: job.Identity.GenerationID, Environment: job.Identity.Environment, TargetRevision: job.TargetRevision,
		LeaseOwner: job.LeaseOwner, LeaseRevision: job.LeaseRevision,
	})
	return allowed == 1, err
}

func (r *SQLRunRepository) JobQueueStats(ctx context.Context, scope refreshrun.ReadScope) (refreshrun.JobQueueStats, error) {
	if r == nil || r.db == nil {
		return refreshrun.JobQueueStats{}, fmt.Errorf("refresh run database is required")
	}
	if err := scope.Validate(); err != nil {
		return refreshrun.JobQueueStats{}, err
	}
	row, err := r.q.GetRefreshJobQueueStats(ctx, platformdb.GetRefreshJobQueueStatsParams{
		QueuedStatus: refreshrun.RunStatusQueued, RunningStatus: refreshrun.RunStatusRunning,
		StaleRunningStatus: refreshrun.RunStatusRunning, RefreshPipelineKind: refreshrun.JobKindRefreshPipeline,
		ProjectID: scope.ProjectID.String(), Environment: scope.Environment,
	})
	if err != nil {
		return refreshrun.JobQueueStats{}, err
	}
	return refreshrun.JobQueueStats{QueuedJobs: int(row.QueuedJobs), RunningJobs: int(row.RunningJobs), StaleLeasedJobs: int(row.StaleLeasedJobs)}, nil
}

func (r *SQLRunRepository) GetRun(ctx context.Context, scope refreshrun.ReadScope, runID string) (refreshrun.RunRecord, error) {
	if err := scope.Validate(); err != nil {
		return refreshrun.RunRecord{}, err
	}
	if runID == "" || runID != strings.TrimSpace(runID) {
		return refreshrun.RunRecord{}, fmt.Errorf("run id is required")
	}
	row, err := r.q.GetMaterializationRun(ctx, platformdb.GetMaterializationRunParams{RunID: runID, ProjectID: scope.ProjectID.String(), Environment: scope.Environment})
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	return materializationRunFromGetRow(row)
}

func (r *SQLRunRepository) getRunForIdentity(ctx context.Context, identity projectgraph.ServingIdentity, runID string) (refreshrun.RunRecord, error) {
	scope, err := refreshrun.ReadScopeForIdentity(identity)
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	return r.GetRun(ctx, scope, runID)
}

func (r *SQLRunRepository) ListRuns(ctx context.Context, scope refreshrun.ReadScope, page refreshrun.RunPage) ([]refreshrun.RunRecord, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	limit := runPageLimit(page)
	cursor := runPageCursor{}
	after := strings.TrimSpace(page.After)
	if after != "" {
		resolved, ok, err := r.runPageCursor(ctx, scope, "", "", after)
		if err != nil {
			return nil, err
		}
		if !ok {
			return []refreshrun.RunRecord{}, nil
		}
		cursor = resolved
	}
	rows, err := r.q.ListMaterializationRuns(ctx, platformdb.ListMaterializationRunsParams{
		ProjectID: scope.ProjectID.String(), Environment: scope.Environment, CursorCreatedAt: cursor.CreatedAt, CursorSequence: cursor.Sequence, Limit: int64(limit),
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

func (r *SQLRunRepository) ListTargetRuns(ctx context.Context, scope refreshrun.ReadScope, targetType string, targetID projectgraph.ResourceID, page refreshrun.RunPage) ([]refreshrun.RunRecord, error) {
	if err := scope.Validate(); err != nil {
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
		resolved, ok, err := r.runPageCursor(ctx, scope, targetType, targetID.String(), after)
		if err != nil {
			return nil, err
		}
		if !ok {
			return []refreshrun.RunRecord{}, nil
		}
		cursor = resolved
	}
	rows, err := r.q.ListTargetMaterializationRuns(ctx, platformdb.ListTargetMaterializationRunsParams{
		ProjectID: scope.ProjectID.String(), Environment: scope.Environment, TargetType: targetType, TargetID: targetID.String(),
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

func (r *SQLRunRepository) ListChildRuns(ctx context.Context, scope refreshrun.ReadScope, parentRunID string) ([]refreshrun.RunRecord, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if parentRunID == "" || parentRunID != strings.TrimSpace(parentRunID) {
		return nil, fmt.Errorf("parent run id is required")
	}
	rows, err := r.q.ListChildMaterializationRuns(ctx, platformdb.ListChildMaterializationRunsParams{
		ProjectID: scope.ProjectID.String(), Environment: scope.Environment, ParentRunID: sql.NullString{String: parentRunID, Valid: true},
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

func (r *SQLRunRepository) LatestTargetRun(ctx context.Context, scope refreshrun.ReadScope, targetType string, targetID projectgraph.ResourceID) (refreshrun.RunRecord, bool, error) {
	runs, err := r.ListTargetRuns(ctx, scope, targetType, targetID, refreshrun.RunPage{Limit: 1})
	if err != nil {
		return refreshrun.RunRecord{}, false, err
	}
	if len(runs) == 0 {
		return refreshrun.RunRecord{}, false, nil
	}
	return runs[0], true, nil
}

func (r *SQLRunRepository) LatestSuccessfulTargetRun(ctx context.Context, scope refreshrun.ReadScope, targetType string, targetID projectgraph.ResourceID) (refreshrun.RunRecord, bool, error) {
	if err := scope.Validate(); err != nil {
		return refreshrun.RunRecord{}, false, err
	}
	if targetType != refreshrun.TargetModelTable && targetType != refreshrun.TargetRefreshPipeline {
		return refreshrun.RunRecord{}, false, fmt.Errorf("target type is required")
	}
	if err := targetID.Validate(); err != nil {
		return refreshrun.RunRecord{}, false, err
	}
	row, err := r.q.LatestSuccessfulMaterializationRun(ctx, platformdb.LatestSuccessfulMaterializationRunParams{
		ProjectID: scope.ProjectID.String(), Environment: scope.Environment,
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
	return r.getRunForIdentity(ctx, job.Identity, job.RunID)
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

func (r *SQLRunRepository) MarkRunTreeSupersededClaimed(ctx context.Context, job refreshrun.JobRecord, message string) error {
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
	if _, err := q.SupersedeRefreshPipelineOccurrenceClaimed(ctx, platformdb.SupersedeRefreshPipelineOccurrenceClaimedParams{
		RunID: job.RunID, ProjectID: job.Identity.ProjectID.String(), GenerationID: job.Identity.GenerationID,
		Environment: job.Identity.Environment, LeaseOwner: job.LeaseOwner, LeaseRevision: job.LeaseRevision, ErrorMessage: message,
	}); err != nil {
		return err
	}
	changedRuns, err := q.MarkRefreshRunTreeSupersededClaimed(ctx, platformdb.MarkRefreshRunTreeSupersededClaimedParams{
		RunID: job.RunID, ProjectID: job.Identity.ProjectID.String(), GenerationID: job.Identity.GenerationID,
		Environment: job.Identity.Environment, LeaseOwner: job.LeaseOwner, LeaseRevision: job.LeaseRevision, ErrorMessage: message,
	})
	if err != nil {
		return err
	}
	if changedRuns != expectedRuns {
		return refreshrun.ErrLeaseLost
	}
	changedJobs, err := q.CompleteRefreshJobTreeSupersededClaimed(ctx, platformdb.CompleteRefreshJobTreeSupersededClaimedParams{
		RunID: job.RunID, ProjectID: job.Identity.ProjectID.String(), GenerationID: job.Identity.GenerationID,
		Environment: job.Identity.Environment, LeaseOwner: job.LeaseOwner, LeaseRevision: job.LeaseRevision, ErrorMessage: message,
	})
	if err != nil {
		return err
	}
	if changedJobs != expectedJobs {
		return refreshrun.ErrLeaseLost
	}
	return tx.Commit()
}

func (r *SQLRunRepository) CancelRun(ctx context.Context, identity projectgraph.ServingIdentity, runID string) (refreshrun.RunRecord, error) {
	return r.cancelRun(ctx, identity, runID, nil)
}

// CancelRunWithAudit commits the queued cancellation and its durable audit
// intent in the same SQLite transaction.
func (r *SQLRunRepository) CancelRunWithAudit(ctx context.Context, identity projectgraph.ServingIdentity, runID string, intent *access.AuditIntent) (refreshrun.RunRecord, error) {
	return r.cancelRun(ctx, identity, runID, intent)
}

func (r *SQLRunRepository) cancelRun(ctx context.Context, identity projectgraph.ServingIdentity, runID string, explicit *access.AuditIntent) (refreshrun.RunRecord, error) {
	if err := identity.Validate(); err != nil || runID == "" || runID != strings.TrimSpace(runID) {
		return refreshrun.RunRecord{}, fmt.Errorf("serving identity and canonical run id are required")
	}
	scope, err := refreshrun.ReadScopeForIdentity(identity)
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	prior, err := r.GetRun(ctx, scope, runID)
	if err != nil {
		return refreshrun.RunRecord{}, err
	}
	if prior.Identity != identity {
		return refreshrun.RunRecord{}, sql.ErrNoRows
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
		if _, getErr := r.GetRun(ctx, scope, runID); getErr != nil {
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
	intent := explicit
	if intent == nil {
		if fromContext, ok := refreshrun.AuditIntentFromContext(ctx); ok {
			intent = &fromContext
		}
	}
	if intent != nil {
		copy := *intent
		copy.ResourceID = runID
		if data, encodeErr := refreshgen.EncodeGenCancelRefreshRunAuditPayload(refreshgen.GenSchemaRefreshCancelledAuditPayload{
			Id: runID, PipelineId: prior.PipelineID.String(), Status: prior.Status, InvocationSource: prior.InvocationSource,
			MatchingScheduleIds: append([]string{}, prior.MatchingScheduleIDs...),
		}); encodeErr != nil {
			return refreshrun.RunRecord{}, encodeErr
		} else {
			copy.MetadataJSON = data
		}
		intent = &copy
	}
	if err := r.recordAuditIntent(ctx, tx, intent, runID); err != nil {
		return refreshrun.RunRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return refreshrun.RunRecord{}, err
	}
	return r.getRunForIdentity(ctx, identity, runID)
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
	return r.getRunForIdentity(ctx, identity, runID)
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
	return r.getRunForIdentity(ctx, job.Identity, job.RunID)
}

type materializationRunDBRow struct {
	ID                       string
	ProjectID                string
	Environment              string
	GenerationID             string
	SemanticModelID          string
	PipelineID               string
	PrincipalID              sql.NullString
	PrincipalDisplayName     string
	TargetType               string
	TargetID                 string
	TargetRevision           int64
	TriggerType              string
	TriggerID                string
	InvocationSource         string
	NominalTime              string
	PlanDigest               string
	MaterializationScopeJSON string
	MatchingScheduleIDsJSON  string
	ParentRunID              sql.NullString
	Status                   string
	CreatedAt                string
	UpdatedAt                string
	StartedAt                string
	FinishedAt               sql.NullString
	Error                    string
}

func materializationRunFromGetRow(row platformdb.GetMaterializationRunRow) (refreshrun.RunRecord, error) {
	return materializationRunFromDB(materializationRunDBRow{
		ID: row.ID, ProjectID: row.ProjectID, Environment: row.Environment, GenerationID: row.GenerationID, SemanticModelID: row.SemanticModelID, PipelineID: row.PipelineID,
		PrincipalID: row.PrincipalID, PrincipalDisplayName: row.PrincipalDisplayName, TargetType: row.TargetType,
		TargetID: row.TargetID, TargetRevision: row.TargetRevision, TriggerType: row.TriggerType, TriggerID: row.TriggerID, InvocationSource: row.InvocationSource, NominalTime: row.NominalTime, PlanDigest: row.PlanDigest, MaterializationScopeJSON: row.MaterializationScopeJson, MatchingScheduleIDsJSON: row.MatchingScheduleIdsJson, ParentRunID: row.ParentRunID, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, Error: row.Error,
	})
}

func materializationRunFromChildRow(row platformdb.ListChildMaterializationRunsRow) (refreshrun.RunRecord, error) {
	return materializationRunFromDB(materializationRunDBRow{
		ID: row.ID, ProjectID: row.ProjectID, Environment: row.Environment, GenerationID: row.GenerationID, SemanticModelID: row.SemanticModelID, PipelineID: row.PipelineID,
		PrincipalID: row.PrincipalID, PrincipalDisplayName: row.PrincipalDisplayName, TargetType: row.TargetType,
		TargetID: row.TargetID, TargetRevision: row.TargetRevision, TriggerType: row.TriggerType, TriggerID: row.TriggerID, InvocationSource: row.InvocationSource, NominalTime: row.NominalTime, PlanDigest: row.PlanDigest, MaterializationScopeJSON: row.MaterializationScopeJson, MatchingScheduleIDsJSON: row.MatchingScheduleIdsJson, ParentRunID: row.ParentRunID, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, Error: row.Error,
	})
}

func materializationRunFromLatestRow(row platformdb.LatestSuccessfulMaterializationRunRow) (refreshrun.RunRecord, error) {
	return materializationRunFromDB(materializationRunDBRow{
		ID: row.ID, ProjectID: row.ProjectID, Environment: row.Environment, GenerationID: row.GenerationID, SemanticModelID: row.SemanticModelID, PipelineID: row.PipelineID,
		PrincipalID: row.PrincipalID, PrincipalDisplayName: row.PrincipalDisplayName, TargetType: row.TargetType,
		TargetID: row.TargetID, TargetRevision: row.TargetRevision, TriggerType: row.TriggerType, TriggerID: row.TriggerID, InvocationSource: row.InvocationSource, NominalTime: row.NominalTime, PlanDigest: row.PlanDigest, MaterializationScopeJSON: row.MaterializationScopeJson, MatchingScheduleIDsJSON: row.MatchingScheduleIdsJson, ParentRunID: row.ParentRunID, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, Error: row.Error,
	})
}

func materializationRunFromListRow(row platformdb.ListMaterializationRunsRow) (refreshrun.RunRecord, error) {
	return materializationRunFromDB(materializationRunDBRow{
		ID: row.ID, ProjectID: row.ProjectID, Environment: row.Environment, GenerationID: row.GenerationID, SemanticModelID: row.SemanticModelID, PipelineID: row.PipelineID,
		PrincipalID: row.PrincipalID, PrincipalDisplayName: row.PrincipalDisplayName, TargetType: row.TargetType,
		TargetID: row.TargetID, TargetRevision: row.TargetRevision, TriggerType: row.TriggerType, TriggerID: row.TriggerID, InvocationSource: row.InvocationSource, NominalTime: row.NominalTime, PlanDigest: row.PlanDigest, MaterializationScopeJSON: row.MaterializationScopeJson, MatchingScheduleIDsJSON: row.MatchingScheduleIdsJson, ParentRunID: row.ParentRunID, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, Error: row.Error,
	})
}

func materializationRunFromTargetListRow(row platformdb.ListTargetMaterializationRunsRow) (refreshrun.RunRecord, error) {
	return materializationRunFromDB(materializationRunDBRow{
		ID: row.ID, ProjectID: row.ProjectID, Environment: row.Environment, GenerationID: row.GenerationID, SemanticModelID: row.SemanticModelID, PipelineID: row.PipelineID,
		PrincipalID: row.PrincipalID, PrincipalDisplayName: row.PrincipalDisplayName, TargetType: row.TargetType,
		TargetID: row.TargetID, TargetRevision: row.TargetRevision, TriggerType: row.TriggerType, TriggerID: row.TriggerID, InvocationSource: row.InvocationSource, NominalTime: row.NominalTime, PlanDigest: row.PlanDigest, MaterializationScopeJSON: row.MaterializationScopeJson, MatchingScheduleIDsJSON: row.MatchingScheduleIdsJson, ParentRunID: row.ParentRunID, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, Error: row.Error,
	})
}

func materializationRunFromDB(row materializationRunDBRow) (refreshrun.RunRecord, error) {
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(row.ProjectID), row.Environment, row.GenerationID)
	if err != nil {
		return refreshrun.RunRecord{}, fmt.Errorf("invalid refresh run serving identity: %w", err)
	}
	var scope []string
	if err := json.Unmarshal([]byte(row.MaterializationScopeJSON), &scope); err != nil || scope == nil {
		return refreshrun.RunRecord{}, fmt.Errorf("invalid refresh run materialization scope")
	}
	matchingScheduleIDs, err := decodeRunScheduleIDs(row.MatchingScheduleIDsJSON)
	if err != nil {
		return refreshrun.RunRecord{}, fmt.Errorf("invalid refresh run schedule evidence: %w", err)
	}
	run := refreshrun.RunRecord{
		ID: row.ID, Identity: identity, SemanticModelID: projectgraph.ResourceID(row.SemanticModelID), PipelineID: projectgraph.ResourceID(row.PipelineID),
		InvocationSource: row.InvocationSource, MatchingScheduleIDs: matchingScheduleIDs, PrincipalID: row.PrincipalID.String, PrincipalDisplayName: row.PrincipalDisplayName, TargetType: row.TargetType,
		TargetID: projectgraph.ResourceID(row.TargetID), TargetRevision: row.TargetRevision, TriggerType: row.TriggerType, TriggerID: row.TriggerID, NominalTime: row.NominalTime, PlanDigest: row.PlanDigest, MaterializationScope: scope, ParentRunID: row.ParentRunID.String, Status: row.Status,
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
	if run.TriggerType != refreshrun.TriggerDependency && run.TriggerType != refreshrun.TriggerManual && run.TriggerType != refreshrun.TriggerSchedule {
		return fmt.Errorf("unsupported refresh trigger type %q", run.TriggerType)
	}
	if run.InvocationSource != refreshrun.TriggerDependency && run.InvocationSource != refreshrun.TriggerManual && run.InvocationSource != refreshrun.TriggerSchedule && run.InvocationSource != "backfill" && run.InvocationSource != "external" {
		return fmt.Errorf("unsupported refresh invocation source %q", run.InvocationSource)
	}
	if err := validateRunScheduleIDs(run.MatchingScheduleIDs); err != nil {
		return err
	}
	switch run.Status {
	case refreshrun.RunStatusQueued, refreshrun.RunStatusRunning, refreshrun.RunStatusPrepared, refreshrun.RunStatusSucceeded, refreshrun.RunStatusFailed, refreshrun.RunStatusCancelled, refreshrun.RunStatusSuperseded, refreshrun.RunStatusSkipped:
	default:
		return fmt.Errorf("unsupported refresh run status %q", run.Status)
	}
	if run.TargetType == refreshrun.TargetRefreshPipeline {
		if run.PipelineID != run.TargetID {
			return fmt.Errorf("refresh pipeline run pipeline id does not match target id")
		}
		if err := validateStoredID(run.TriggerID, "trigger id", false); err != nil {
			return err
		}
		if run.PlanDigest == "" || len(run.MaterializationScope) == 0 {
			return fmt.Errorf("refresh pipeline run plan evidence is required")
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

func (r *SQLRunRepository) runPageCursor(ctx context.Context, scope refreshrun.ReadScope, targetType, targetID, runID string) (runPageCursor, bool, error) {
	row, err := r.q.GetMaterializationRunCursor(ctx, platformdb.GetMaterializationRunCursorParams{
		RunID: runID, ProjectID: scope.ProjectID.String(), Environment: scope.Environment, TargetType: targetType, TargetID: targetID,
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
	Identity                 projectgraph.ServingIdentity
	SemanticModelID          projectgraph.ResourceID
	PipelineID               projectgraph.ResourceID
	PrincipalID              string
	TargetType               string
	TargetID                 projectgraph.ResourceID
	TargetRevision           int64
	TriggerType              string
	TriggerID                string
	InvocationSource         string
	MatchingScheduleIDs      []string
	NominalTime              string
	ConcurrencyPolicy        string
	PlanDigest               string
	MaterializationScopeJSON string
	ParentRunID              string
	JobKind                  string
	PayloadJSON              string
	GroupIDs                 []string
	EstimatedMemoryBytes     int64
	AuditIntent              *access.AuditIntent
}

func normalizeRunInput(input refreshrun.RunInput) (normalizedRunInput, error) {
	// InvocationSource is the durable contract, while TriggerType remains the
	// storage discriminator. Accept source-only callers for the trigger-shaped
	// sources and materialize the discriminator before validation/persistence.
	if input.TriggerType == "" {
		switch input.InvocationSource {
		case refreshrun.TriggerManual, refreshrun.TriggerSchedule:
			input.TriggerType = input.InvocationSource
		}
	}
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
	}
	invocationSource := input.InvocationSource
	if invocationSource == "" {
		invocationSource = input.TriggerType
	}
	materializationScopeJSON := "[]"
	planDigest := ""
	if input.PipelinePlan != nil {
		encoded, err := json.Marshal(input.PipelinePlan.MaterializationScope)
		if err != nil {
			return normalizedRunInput{}, fmt.Errorf("encode refresh materialization scope: %w", err)
		}
		materializationScopeJSON = string(encoded)
		planDigest = input.PipelinePlan.Digest
	}
	return normalizedRunInput{
		Identity: input.Identity, SemanticModelID: input.SemanticModelID, PipelineID: input.PipelineID,
		PrincipalID: input.PrincipalID, GroupIDs: append([]string{}, input.GroupIDs...), EstimatedMemoryBytes: input.EstimatedMemoryBytes,
		TargetType: input.TargetType, TargetID: input.TargetID,
		TargetRevision: input.TargetRevision, TriggerType: input.TriggerType, TriggerID: input.TriggerID, InvocationSource: invocationSource, MatchingScheduleIDs: canonicalRunScheduleIDs(input.MatchingScheduleIDs), NominalTime: input.NominalTime, ConcurrencyPolicy: input.ConcurrencyPolicy,
		PlanDigest: planDigest, MaterializationScopeJSON: materializationScopeJSON, ParentRunID: input.ParentRunID,
		JobKind: input.JobKind, PayloadJSON: payloadJSON,
		AuditIntent: input.AuditIntent,
	}, nil
}

func decodeRunScheduleIDs(value string) ([]string, error) {
	var ids []string
	if strings.TrimSpace(value) == "" {
		return []string{}, nil
	}
	if err := json.Unmarshal([]byte(value), &ids); err != nil || ids == nil {
		if err == nil {
			err = fmt.Errorf("schedule evidence must be an array")
		}
		return nil, err
	}
	canonical := canonicalRunScheduleIDs(ids)
	if !sameStrings(ids, canonical) {
		return nil, fmt.Errorf("schedule evidence is not canonically sorted")
	}
	if err := validateRunScheduleIDs(ids); err != nil {
		return nil, err
	}
	return ids, nil
}

func canonicalRunScheduleIDs(ids []string) []string {
	result := append([]string{}, ids...)
	sort.Strings(result)
	return result
}

func validateRunScheduleIDs(ids []string) error {
	previous := ""
	for _, id := range ids {
		if id == "" || id != strings.TrimSpace(id) {
			return fmt.Errorf("matching schedule id is not canonical")
		}
		if previous != "" && id <= previous {
			return fmt.Errorf("matching schedule ids must be sorted canonically")
		}
		previous = id
	}
	return nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
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
