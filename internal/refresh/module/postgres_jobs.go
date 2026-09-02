package module

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	jobpolicy "github.com/flidai/leapview/internal/platform/jobs"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	projectpipelineplan "github.com/flidai/leapview/internal/project/contracts/pipelineplan"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/pkg/jobs"
)

const refreshJobResourceKind = "refresh_run"

// PostgresJobsAdapter is the concrete bridge between the refresh module and
// the canonical platform jobs PostgreSQL repository.  It owns no database
// handles: both repositories are capability adapters over their respective
// caller-provided pgx pools, while enqueue and terminal transitions receive
// the refresh authority's transaction directly.
type PostgresJobsAdapter struct {
	Jobs    *jobspostgres.Repository
	Refresh *refreshpostgres.Repository
}

func NewPostgresJobsAdapter(jobsRepository *jobspostgres.Repository, refreshRepository *refreshpostgres.Repository) *PostgresJobsAdapter {
	return &PostgresJobsAdapter{Jobs: jobsRepository, Refresh: refreshRepository}
}

// postgresQueueAuthority is the provenance-bearing native queue contract.
// The unexported marker prevents another package from presenting an arbitrary
// queue implementation as the canonical PostgreSQL authority. Test wrappers
// that embed PostgresJobsAdapter retain the marker and provenance methods.
type postgresQueueAuthority interface {
	PostgresQueueRecovery
	Configured() bool
	MatchesRefreshRepository(*refreshpostgres.Repository) bool
	postgresQueueAuthorityMarker()
}

// postgresQueueAuthorityMarker marks the concrete adapter as the canonical
// platform-jobs bridge. It intentionally has no runtime behavior.
func (*PostgresJobsAdapter) postgresQueueAuthorityMarker() {}

// Configured reports whether both sibling repositories are initialized and
// backed by the same native database handle. A refresh repository pointer
// alone is not enough: callers could otherwise pair it with a jobs repository
// connected to a different PostgreSQL database.
func (a *PostgresJobsAdapter) Configured() bool {
	return a != nil && a.Jobs != nil && a.Refresh != nil &&
		a.Jobs.Configured() && a.Refresh.Configured() &&
		sameNativeDB(a.Jobs.DB(), a.Refresh.DB())
}

// MatchesRefreshRepository verifies both object provenance and the underlying
// database identity against the canonical refresh authority.
func (a *PostgresJobsAdapter) MatchesRefreshRepository(refresh *refreshpostgres.Repository) bool {
	return a != nil && refresh != nil && a.Refresh == refresh && a.Configured()
}

func sameNativeDB(left, right any) bool {
	if left == nil || right == nil {
		return false
	}
	lv, rv := reflect.ValueOf(left), reflect.ValueOf(right)
	if lv.Type() != rv.Type() {
		return false
	}
	switch lv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		if lv.IsNil() || rv.IsNil() {
			return false
		}
		return lv.Pointer() == rv.Pointer()
	default:
		if lv.Type().Comparable() {
			return lv.Interface() == rv.Interface()
		}
		return false
	}
}

var _ PostgresQueue = (*PostgresJobsAdapter)(nil)
var _ PostgresQueueWriter = (*PostgresJobsAdapter)(nil)
var _ PostgresQueueLifecycle = (*PostgresJobsAdapter)(nil)

// refreshJobPayload is the durable queue envelope.  The refresh authority
// stores only immutable run identity; execution requires the complete plan,
// so it travels in the canonical jobs payload and is validated on dequeue.
type refreshJobPayload struct {
	PipelinePlan *projectpipelineplan.Plan `json:"pipelinePlan"`
	Input        json.RawMessage           `json:"input"`
}

// EnqueueRefreshTx writes one canonical platform job through the transaction
// that inserted the refresh run.  The deterministic id makes retries after an
// ambiguous commit exact replays rather than duplicate work.
func (a *PostgresJobsAdapter) EnqueueRefreshTx(ctx context.Context, tx refreshpostgres.Tx, input refreshrun.RunInput, runID string) (string, error) {
	if a == nil || a.Jobs == nil {
		return "", errors.New("canonical PostgreSQL jobs repository is required")
	}
	if tx == nil || runID == "" {
		return "", errors.New("refresh enqueue transaction and run id are required")
	}
	if err := input.Validate(); err != nil {
		return "", err
	}
	if input.PipelinePlan == nil {
		return "", errors.New("refresh pipeline plan is required")
	}
	rawInput := json.RawMessage(strings.TrimSpace(input.PayloadJSON))
	if len(rawInput) == 0 {
		rawInput = json.RawMessage(`{}`)
	}
	if !json.Valid(rawInput) {
		return "", errors.New("refresh job payload must be valid JSON")
	}
	payload, err := json.Marshal(refreshJobPayload{PipelinePlan: input.PipelinePlan, Input: rawInput})
	if err != nil {
		return "", fmt.Errorf("encode refresh job payload: %w", err)
	}
	jobID := "refresh-job-" + runID
	_, err = a.Jobs.EnqueueTx(ctx, tx, jobs.EnqueueInput{
		ID:                   jobID,
		Kind:                 input.JobKind,
		WorkloadClass:        jobpolicy.WorkloadClassBackground,
		PrincipalID:          input.PrincipalID,
		GroupIDs:             append([]string(nil), input.GroupIDs...),
		PartitionKey:         "refresh:" + input.Identity.ProjectID.String() + ":" + input.Identity.Environment,
		ResourceKind:         refreshJobResourceKind,
		ResourceID:           runID,
		EstimatedMemoryBytes: input.EstimatedMemoryBytes,
		Payload:              payload,
	})
	if err != nil {
		return "", err
	}
	return jobID, nil
}

// AppendRefreshQueuedEventTx records the initial refresh lifecycle event in
// the canonical jobs event log through the admission transaction. The caller
// invokes this only after a fresh operation reservation, so keyed replays do
// not append duplicate events.
func (a *PostgresJobsAdapter) AppendRefreshQueuedEventTx(ctx context.Context, tx refreshpostgres.Tx, runID, payload, eventType string) error {
	if a == nil || a.Jobs == nil {
		return errors.New("canonical PostgreSQL jobs repository is required")
	}
	if tx == nil || runID == "" || eventType == "" {
		return errors.New("refresh event transaction, run id and event type are required")
	}
	_, err := a.Jobs.AppendEventTx(ctx, tx, "refresh", runID, eventType, []byte(payload))
	return err
}

func (a *PostgresJobsAdapter) ListExecutableJobs(ctx context.Context, scope refreshrun.ReadScope, limit int) ([]refreshrun.JobRecord, error) {
	if a == nil || a.Jobs == nil || a.Refresh == nil {
		return nil, errors.New("canonical PostgreSQL jobs and refresh repositories are required")
	}
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > refreshpostgres.MaxPageSize {
		return nil, errors.New("refresh job page limit is outside bound")
	}
	out := make([]refreshrun.JobRecord, 0, limit)
	afterCreated, afterID := time.Time{}, ""
	for len(out) < limit {
		candidates, err := a.Jobs.CandidatesByResourceKindAfter(ctx, jobpolicy.WorkloadClassBackground, refreshJobResourceKind, refreshpostgres.MaxPageSize, afterCreated, afterID)
		if err != nil {
			return nil, err
		}
		if len(candidates) == 0 {
			break
		}
		for _, candidate := range candidates {
			if len(out) == limit {
				break
			}
			run, err := a.Refresh.LookupRun(ctx, candidate.ResourceID)
			if err != nil {
				if errors.Is(err, refreshpostgres.ErrNotFound) {
					continue
				}
				return nil, err
			}
			if run.ProjectID != scope.ProjectID.String() || run.Environment != scope.Environment || run.JobID != candidate.ID {
				continue
			}
			mapped, err := a.jobRecord(run, candidate)
			if err != nil {
				if poisonErr := a.quarantineQueuedPayload(ctx, candidate, run, err); poisonErr != nil {
					return nil, poisonErr
				}
				continue
			}
			out = append(out, mapped)
		}
		if len(candidates) < refreshpostgres.MaxPageSize {
			break
		}
		last := candidates[len(candidates)-1]
		var cursorErr error
		afterCreated, cursorErr = parseJobTimestamp(last.CreatedAt)
		if cursorErr != nil {
			return nil, fmt.Errorf("refresh job candidate cursor is invalid: %w", cursorErr)
		}
		afterID = last.ID
	}
	return out, nil
}

func (a *PostgresJobsAdapter) ClaimExecutableJob(ctx context.Context, candidate refreshrun.JobRecord, owner string, lease time.Duration) (refreshrun.JobRecord, bool, error) {
	if a == nil || a.Jobs == nil || a.Refresh == nil {
		return refreshrun.JobRecord{}, false, errors.New("canonical PostgreSQL jobs and refresh repositories are required")
	}
	if err := candidate.Validate(); err != nil {
		return refreshrun.JobRecord{}, false, err
	}
	var mapped refreshrun.JobRecord
	claimed := false
	terminalized := false
	err := a.Refresh.InTx(ctx, func(tx refreshpostgres.Tx) error {
		job, ok, err := a.Jobs.ClaimByIDTx(ctx, tx, candidate.ID, jobpolicy.WorkloadClassBackground, owner, lease)
		if err != nil {
			return err
		}
		if !ok {
			return errClaimUnavailable
		}
		claimed = true
		run, err := a.Refresh.GetRunTx(ctx, tx, refreshpostgres.Scope{ProjectID: candidate.Identity.ProjectID.String(), Environment: candidate.Identity.Environment}, candidate.RunID)
		if err != nil {
			if !errors.Is(err, refreshpostgres.ErrNotFound) {
				return err
			}
			// The job is already claimed in this transaction. There is no linked
			// refresh attempt to fence, so fail the exact job before rollback.
			if failErr := a.Jobs.FailTx(ctx, tx, job.ID, jobs.Fence{Owner: job.LeaseOwner, Generation: job.LeaseGeneration}, []byte(`{"code":"REFRESH_RUN_MISSING"}`)); failErr != nil {
				return failErr
			}
			terminalized = true
			return nil
		}
		if run.JobID != job.ID || run.ProjectID != candidate.Identity.ProjectID.String() || run.Environment != candidate.Identity.Environment {
			if failErr := a.Jobs.FailTx(ctx, tx, job.ID, jobs.Fence{Owner: job.LeaseOwner, Generation: job.LeaseGeneration}, []byte(`{"code":"REFRESH_JOB_IDENTITY_MISMATCH"}`)); failErr != nil {
				return failErr
			}
			if failErr := a.Refresh.FailRunTerminalEvidenceTx(ctx, tx, run.RunID, "refresh job identity rejected", []byte(`{"code":"REFRESH_JOB_IDENTITY_MISMATCH"}`)); failErr != nil {
				return failErr
			}
			terminalized = true
			return nil
		}
		if _, err := a.Refresh.ClaimAttemptTx(ctx, tx, run.RunID, job.LeaseOwner, job.LeaseGeneration, lease); err != nil {
			return err
		}
		run, err = a.Refresh.GetRunTx(ctx, tx, refreshpostgres.Scope{ProjectID: candidate.Identity.ProjectID.String(), Environment: candidate.Identity.Environment}, candidate.RunID)
		if err != nil {
			return err
		}
		mapped, err = a.jobRecord(run, job)
		if err != nil {
			// Decoder errors may contain payload-controlled text (or implementation
			// details). Close both authorities with a bounded stable code.
			problem := []byte(`{"code":"REFRESH_POISON_PAYLOAD"}`)
			if failErr := a.Refresh.FailRunTreeTx(ctx, tx, run.RunID, job.LeaseOwner, job.LeaseGeneration, "refresh job payload rejected", problem); failErr != nil {
				return failErr
			}
			return a.Jobs.FailTx(ctx, tx, job.ID, jobs.Fence{Owner: job.LeaseOwner, Generation: job.LeaseGeneration}, problem)
		}
		return nil
	})
	if errors.Is(err, errClaimUnavailable) {
		return refreshrun.JobRecord{}, false, nil
	}
	if err != nil {
		return refreshrun.JobRecord{}, false, err
	}
	if terminalized {
		return refreshrun.JobRecord{}, false, nil
	}
	return mapped, claimed, nil
}

var errClaimUnavailable = errors.New("refresh job claim unavailable")

func (a *PostgresJobsAdapter) quarantineQueuedPayload(ctx context.Context, candidate jobs.Job, run refreshpostgres.Run, cause error) error {
	if a == nil || a.Jobs == nil || a.Refresh == nil {
		return errors.New("canonical PostgreSQL jobs and refresh repositories are required")
	}
	if run.JobID != candidate.ID {
		return errors.New("poison refresh job no longer matches its run")
	}
	_ = cause // decoder details are intentionally not persisted or reflected to callers
	problem := []byte(`{"code":"REFRESH_POISON_PAYLOAD"}`)
	return a.Refresh.InTx(ctx, func(tx refreshpostgres.Tx) error {
		runChanged, err := a.Refresh.QuarantineQueuedRunTx(ctx, tx, run.RunID, candidate.ID)
		if err != nil {
			return err
		}
		jobChanged, err := a.Jobs.QuarantineQueuedTx(ctx, tx, candidate.ID, problem)
		if err != nil {
			return err
		}
		if !runChanged && !jobChanged {
			return nil // another worker owns or already terminalized this item
		}
		if runChanged != jobChanged {
			return errors.New("poison refresh run and job terminalization diverged")
		}
		return nil
	})
}

func (a *PostgresJobsAdapter) RenewJobLease(ctx context.Context, job refreshrun.JobRecord, lease time.Duration) error {
	if a == nil || a.Jobs == nil || a.Refresh == nil {
		return errors.New("canonical PostgreSQL jobs and refresh repositories are required")
	}
	if err := job.Validate(); err != nil {
		return err
	}
	return a.Refresh.InTx(ctx, func(tx refreshpostgres.Tx) error {
		fence := jobs.Fence{Owner: job.LeaseOwner, Generation: job.LeaseRevision}
		if err := a.Jobs.RenewTx(ctx, tx, job.ID, fence, lease); err != nil {
			return err
		}
		return a.Refresh.HeartbeatAttemptTx(ctx, tx, job.RunID, job.LeaseOwner, job.LeaseRevision, lease)
	})
}

func (a *PostgresJobsAdapter) JobQueueStats(ctx context.Context, scope refreshrun.ReadScope) (refreshrun.JobQueueStats, error) {
	if a == nil || a.Jobs == nil || a.Refresh == nil {
		return refreshrun.JobQueueStats{}, errors.New("canonical PostgreSQL jobs and refresh repositories are required")
	}
	if err := scope.Validate(); err != nil {
		return refreshrun.JobQueueStats{}, err
	}
	var stats refreshrun.JobQueueStats
	afterCreated, afterID := time.Time{}, ""
	for {
		candidates, err := a.Jobs.CandidatesByResourceKindAfter(ctx, jobpolicy.WorkloadClassBackground, refreshJobResourceKind, refreshpostgres.MaxPageSize, afterCreated, afterID)
		if err != nil {
			return refreshrun.JobQueueStats{}, err
		}
		if len(candidates) == 0 {
			break
		}
		for _, candidate := range candidates {
			run, runErr := a.Refresh.LookupRun(ctx, candidate.ResourceID)
			if runErr != nil {
				if errors.Is(runErr, refreshpostgres.ErrNotFound) {
					continue
				}
				return refreshrun.JobQueueStats{}, runErr
			}
			if run.ProjectID != scope.ProjectID.String() || run.Environment != scope.Environment || run.JobID != candidate.ID {
				continue
			}
			switch candidate.Status {
			case jobs.StatusQueued:
				stats.QueuedJobs++
			case jobs.StatusRunning:
				stats.RunningJobs++
				expiry, parseErr := parseJobTimestamp(candidate.LeaseExpiresAt)
				if parseErr != nil {
					return refreshrun.JobQueueStats{}, fmt.Errorf("refresh job lease timestamp is invalid: %w", parseErr)
				}
				if !expiry.After(time.Now().UTC()) {
					stats.StaleLeasedJobs++
				}
			}
		}
		if len(candidates) < refreshpostgres.MaxPageSize {
			break
		}
		last := candidates[len(candidates)-1]
		var cursorErr error
		afterCreated, cursorErr = parseJobTimestamp(last.CreatedAt)
		if cursorErr != nil {
			return refreshrun.JobQueueStats{}, fmt.Errorf("refresh job stats cursor is invalid: %w", cursorErr)
		}
		afterID = last.ID
	}
	return stats, nil
}

// PostgresQueueLifecycle is kept separate from PostgresQueue so read/claim
// adapters remain useful to diagnostics, while production workers can require
// atomic terminal transitions with refresh.attempt.
type PostgresQueueLifecycle interface {
	CompleteJobTx(context.Context, refreshpostgres.Tx, refreshrun.JobRecord) error
	FailJobTx(context.Context, refreshpostgres.Tx, refreshrun.JobRecord, string) error
	CancelClaimedJobTx(context.Context, refreshpostgres.Tx, refreshrun.JobRecord) error
	CancelJobTx(context.Context, refreshpostgres.Tx, string) error
	SupersedeJobsTx(context.Context, refreshpostgres.Tx, []string) error
}

// PostgresQueueRecovery extends lifecycle transitions with the read/repair
// operations required by startup reconciliation. Keeping these optional on a
// separate interface avoids widening ordinary worker test doubles.
type PostgresQueueRecovery interface {
	PostgresQueueLifecycle
	GetJobTx(context.Context, refreshpostgres.Tx, string) (jobs.Job, error)
	LatestAttemptTx(context.Context, refreshpostgres.Tx, string, int64, int64) (jobspostgres.Attempt, bool, error)
	ActiveRefreshJobsTx(context.Context, refreshpostgres.Tx, time.Time, string, int) ([]jobs.Job, error)
	ReconcileTerminalTx(context.Context, refreshpostgres.Tx, string, jobs.Status) error
}

// PostgresJobsAuthority is the complete canonical jobs surface required by
// refresh persistence. A single value supplies reads, enqueue, lifecycle, and
// recovery so callers cannot accidentally split authorities.
type PostgresJobsAuthority interface {
	PostgresQueue
	PostgresQueueWriter
	PostgresQueueLifecycle
	PostgresQueueRecovery
}

func (a *PostgresJobsAdapter) CompleteJobTx(ctx context.Context, tx refreshpostgres.Tx, job refreshrun.JobRecord) error {
	if a == nil || a.Jobs == nil {
		return errors.New("canonical PostgreSQL jobs repository is required")
	}
	return a.Jobs.CompleteTx(ctx, tx, job.ID, jobs.Fence{Owner: job.LeaseOwner, Generation: job.LeaseRevision})
}

func (a *PostgresJobsAdapter) FailJobTx(ctx context.Context, tx refreshpostgres.Tx, job refreshrun.JobRecord, message string) error {
	if a == nil || a.Jobs == nil {
		return errors.New("canonical PostgreSQL jobs repository is required")
	}
	// Worker failures are not durable free-form diagnostics: they may include
	// SQL, payload fragments, or credentials. Persist only a bounded stable
	// classifier and keep detailed context in process logs.
	_ = message
	problem := []byte(`{"code":"REFRESH_FAILED"}`)
	return a.Jobs.FailTx(ctx, tx, job.ID, jobs.Fence{Owner: job.LeaseOwner, Generation: job.LeaseRevision}, problem)
}

func (a *PostgresJobsAdapter) CancelJobTx(ctx context.Context, tx refreshpostgres.Tx, jobID string) error {
	if a == nil || a.Jobs == nil {
		return errors.New("canonical PostgreSQL jobs repository is required")
	}
	return a.Jobs.CancelTx(ctx, tx, jobID)
}

func (a *PostgresJobsAdapter) CancelClaimedJobTx(ctx context.Context, tx refreshpostgres.Tx, job refreshrun.JobRecord) error {
	if a == nil || a.Jobs == nil || tx == nil {
		return errors.New("canonical PostgreSQL jobs repository and transaction are required")
	}
	return a.Jobs.CancelClaimedTx(ctx, tx, job.ID, jobs.Fence{Owner: job.LeaseOwner, Generation: job.LeaseRevision})
}

func (a *PostgresJobsAdapter) SupersedeJobsTx(ctx context.Context, tx refreshpostgres.Tx, jobIDs []string) error {
	if a == nil || a.Jobs == nil || tx == nil {
		return errors.New("canonical PostgreSQL jobs repository and transaction are required")
	}
	return a.Jobs.SupersedeTx(ctx, tx, jobIDs)
}

func (a *PostgresJobsAdapter) GetJobTx(ctx context.Context, tx refreshpostgres.Tx, id string) (jobs.Job, error) {
	if a == nil || a.Jobs == nil {
		return jobs.Job{}, errors.New("canonical PostgreSQL jobs repository is required")
	}
	return a.Jobs.GetTx(ctx, tx, id)
}

func (a *PostgresJobsAdapter) LatestAttemptTx(ctx context.Context, tx refreshpostgres.Tx, id string, attemptNumber, fencingGeneration int64) (jobspostgres.Attempt, bool, error) {
	if a == nil || a.Jobs == nil {
		return jobspostgres.Attempt{}, false, errors.New("canonical PostgreSQL jobs repository is required")
	}
	return a.Jobs.LatestAttemptTx(ctx, tx, id, attemptNumber, fencingGeneration)
}

func (a *PostgresJobsAdapter) ActiveRefreshJobsTx(ctx context.Context, tx refreshpostgres.Tx, afterCreated time.Time, afterID string, limit int) ([]jobs.Job, error) {
	if a == nil || a.Jobs == nil {
		return nil, errors.New("canonical PostgreSQL jobs repository is required")
	}
	return a.Jobs.ActiveRefreshJobsTx(ctx, tx, afterCreated, afterID, limit)
}

func (a *PostgresJobsAdapter) ReconcileTerminalTx(ctx context.Context, tx refreshpostgres.Tx, id string, desired jobs.Status) error {
	if a == nil || a.Jobs == nil {
		return errors.New("canonical PostgreSQL jobs repository is required")
	}
	return a.Jobs.ReconcileTerminalTx(ctx, tx, id, desired)
}

func (a *PostgresJobsAdapter) jobRecord(run refreshpostgres.Run, job jobs.Job) (refreshrun.JobRecord, error) {
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(run.ProjectID), run.Environment, run.GenerationID)
	if err != nil {
		return refreshrun.JobRecord{}, err
	}
	plan, inputPayload, err := decodeRefreshPayload(job.Payload)
	if err != nil {
		return refreshrun.JobRecord{}, err
	}
	record := refreshrun.JobRecord{
		ID: job.ID, Identity: identity, SemanticModelID: projectgraph.ResourceID(run.SemanticModelID), PipelineID: projectgraph.ResourceID(run.PipelineID),
		PipelinePlan: plan, InvocationSource: run.InvocationSource, MatchingScheduleIDs: append([]string(nil), run.MatchingScheduleIDs...), TriggerID: run.TriggerID,
		NominalTime: formatNominal(run.NominalTime), PrincipalID: run.PrincipalID, GroupIDs: append([]string(nil), job.GroupIDs...), Kind: job.Kind,
		PayloadJSON: inputPayload, EstimatedMemoryBytes: job.EstimatedMemoryBytes, RunID: run.RunID, TargetType: run.TargetType, TargetID: projectgraph.ResourceID(run.TargetID),
		TargetRevision: run.TargetRevision, TriggerType: run.TriggerType, AttemptCount: job.Attempts, LeaseOwner: job.LeaseOwner, LeaseRevision: job.LeaseGeneration,
	}
	if err := record.Validate(); err != nil {
		return refreshrun.JobRecord{}, err
	}
	return record, nil
}

func decodeRefreshPayload(raw []byte) (*projectpipelineplan.Plan, string, error) {
	var envelope refreshJobPayload
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.PipelinePlan == nil {
		return nil, "", errors.New("refresh job payload lacks pipeline plan")
	}
	if err := envelope.PipelinePlan.Validate(); err != nil {
		return nil, "", err
	}
	input := strings.TrimSpace(string(envelope.Input))
	if input == "" {
		input = "{}"
	}
	return envelope.PipelinePlan, input, nil
}

func formatNominal(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseJobTimestamp(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid job timestamp %q", raw)
}
