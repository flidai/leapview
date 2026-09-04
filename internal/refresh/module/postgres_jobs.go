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

func isNilPostgresCapability(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

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

// ClaimRiverJob binds River's exact attempt to the refresh capability state.
// River has already selected and locked the operational row; this method only
// establishes the refresh-owned attempt and validates the durable payload in
// the caller-visible product history.
func (a *PostgresJobsAdapter) ClaimRiverJob(ctx context.Context, job jobs.Job, lease time.Duration) (refreshrun.JobRecord, error) {
	if a == nil || a.Jobs == nil || a.Refresh == nil {
		return refreshrun.JobRecord{}, errors.New("canonical PostgreSQL jobs and refresh repositories are required")
	}
	if job.ResourceKind != refreshJobResourceKind || job.ResourceID == "" || job.Attempts < 1 {
		return refreshrun.JobRecord{}, errors.New("River refresh job identity is invalid")
	}
	owner := strings.TrimSpace(job.LeaseOwner)
	if owner == "" {
		owner = "river"
	}
	var mapped refreshrun.JobRecord
	var terminalErr error
	err := a.Refresh.InTx(ctx, func(tx refreshpostgres.Tx) error {
		run, err := a.Refresh.LookupRunTx(ctx, tx, job.ResourceID)
		if err != nil {
			return err
		}
		if run.JobID != job.ID {
			return errors.New("River refresh job does not match its run")
		}
		if _, err := a.Refresh.ClaimAttemptTx(ctx, tx, run.RunID, owner, int64(job.Attempts), lease); err != nil {
			return err
		}
		run, err = a.Refresh.LookupRunTx(ctx, tx, job.ResourceID)
		if err != nil {
			return err
		}
		job.LeaseOwner = owner
		job.LeaseGeneration = int64(job.Attempts)
		mapped, err = a.jobRecord(run, job)
		if err == nil {
			return nil
		}
		// Payload decoding happens only after River has selected the exact job.
		// Close the refresh tree, product history, and River row in this one
		// transaction; never reintroduce a separate poison-queue scanner.
		problem := []byte(`{"code":"REFRESH_POISON_PAYLOAD"}`)
		if failErr := a.Refresh.FailRunTreeTx(ctx, tx, run.RunID, owner, int64(job.Attempts), "refresh job payload rejected", problem); failErr != nil {
			return failErr
		}
		if failErr := a.Jobs.FailTx(ctx, tx, job.ID, jobs.Fence{Owner: owner, Generation: int64(job.Attempts)}, problem); failErr != nil {
			return failErr
		}
		terminalErr = errors.New("refresh job payload rejected")
		return nil
	})
	if err == nil && terminalErr != nil {
		err = terminalErr
	}
	return mapped, err
}

// PostgresQueueLifecycle owns the product-history transitions composed with
// refresh capability transactions. River alone owns operational claiming.
type PostgresQueueLifecycle interface {
	CompleteJobTx(context.Context, refreshpostgres.Tx, refreshrun.JobRecord) error
	FailJobTx(context.Context, refreshpostgres.Tx, refreshrun.JobRecord, string) error
	CancelClaimedJobTx(context.Context, refreshpostgres.Tx, refreshrun.JobRecord) error
	CancelJobTx(context.Context, refreshpostgres.Tx, string) error
	SupersedeJobsTx(context.Context, refreshpostgres.Tx, []string) error
}

// PostgresJobHistory exposes the exact product row needed to validate a
// capability-owned publication transaction.
type PostgresJobHistory interface {
	GetJobTx(context.Context, refreshpostgres.Tx, string) (jobs.Job, error)
}

// PostgresJobsAuthority is the complete canonical jobs surface required by
// refresh persistence. A single value supplies reads, enqueue, lifecycle, and
// history so callers cannot accidentally split authorities.
type PostgresJobsAuthority interface {
	PostgresQueueWriter
	PostgresQueueLifecycle
	PostgresJobHistory
	ClaimRiverJob(context.Context, jobs.Job, time.Duration) (refreshrun.JobRecord, error)
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
