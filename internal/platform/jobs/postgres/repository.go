// Package postgres implements the durable jobs capability on PostgreSQL.
//
// The adapter intentionally accepts pgx's native DBTX shape. It does not
// expose a database/sql compatibility layer; callers that need a transaction
// pass the pgx transaction directly to RecordWorkflow.
package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"

	jobpolicy "github.com/flidai/leapview/internal/platform/jobs"
	jobdb "github.com/flidai/leapview/internal/platform/jobs/postgres/internal/db"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// DBTX is implemented by pgx.Conn, pgx.Tx, pgxpool.Pool and pgxpool.Conn.
// Keeping this interface local lets capability tests use a real pgx pool or a
// narrow recording implementation without introducing a backend abstraction.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// MaintenanceDBTX is the native PostgreSQL surface for the separately
// authenticated maintenance connection. It intentionally mirrors DBTX so a
// pool or caller-owned transaction can invoke only the bounded maintenance
// facade; runtime repositories never retain this capability.
type MaintenanceDBTX interface {
	DBTX
}

// Tx is the native transaction surface accepted by caller-owned workflow
// methods. Requiring commit/rollback prevents a pool or connection from being
// passed accidentally and silently splitting an atomic workflow.
type Tx interface {
	DBTX
	Commit(context.Context) error
	Rollback(context.Context) error
}

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

type Repository struct{ db DBTX }

// Maintenance owns destructive job retention. Keeping this capability out of
// Repository prevents serving-role code from accidentally invoking the prune
// leaf; PostgreSQL role grants remain the enforcement boundary.
type Maintenance struct{ db MaintenanceDBTX }

func queries(db DBTX) *jobdb.Queries { return jobdb.New(db) }

// Attempt is the immutable identity and terminal evidence for one canonical
// job claim.  It is intentionally read through the jobs authority so callers
// cannot infer completion from the parent job row alone.
type Attempt struct {
	JobID                            string
	AttemptNumber, FencingGeneration int64
	Owner, Outcome                   string
	FinishedAt                       *time.Time
	ErrorJSON                        []byte
}

var _ jobs.Repository = (*Repository)(nil)

// DB exposes the configured native PostgreSQL handle to composition-owned
// adapters for authority provenance checks. Mutations must still use the
// caller-owned transaction passed to the Tx methods.
func (r *Repository) DB() DBTX {
	if r == nil {
		return nil
	}
	return r.db
}

// Configured reports whether the repository has a native PostgreSQL query
// authority. Interface values can contain a typed nil pointer, so a plain
// interface comparison is not sufficient for admission.
func (r *Repository) Configured() bool { return r != nil && nativeDBConfigured(r.db) }

func nativeDBConfigured(db DBTX) bool {
	if db == nil {
		return false
	}
	value := reflect.ValueOf(db)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

// MaxAttempts is the bounded retry ceiling for jobs created through this
// adapter. EnqueueInput predates retry policy, so one explicit default is
// persisted rather than permitting unbounded lease reclaim.
const MaxAttempts int64 = 3

// MaxRetryDelay bounds persisted backoff so malformed workers cannot hide a
// job indefinitely in the queue.
const MaxRetryDelay = 24 * time.Hour

// MaxLeaseDuration keeps abandoned claims observable and prevents a malformed
// worker from reserving work for an effectively unbounded interval.
const MaxLeaseDuration = 24 * time.Hour

func NewRepository(db DBTX) *Repository { return &Repository{db: db} }

// New is a concise constructor alias for callers that keep one repository per
// capability package.
func New(db DBTX) *Repository { return NewRepository(db) }

// NewMaintenance constructs the bounded job-retention facade.
func NewMaintenance(db MaintenanceDBTX) *Maintenance { return &Maintenance{db: db} }

// ApplySchema applies the capability-owned DDL on a caller-owned transaction.
// It is useful for clean conformance databases and deliberately performs no
// implicit commit.
func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return fmt.Errorf("schema transaction is required")
	}
	_, err := tx.Exec(ctx, schemaSQL)
	return err
}

func (r *Repository) Enqueue(ctx context.Context, input jobs.EnqueueInput) (jobs.Job, error) {
	if r == nil || r.db == nil {
		return jobs.Job{}, fmt.Errorf("postgres jobs database is required")
	}
	if b, ok := r.db.(beginner); ok {
		if _, alreadyTx := r.db.(pgx.Tx); !alreadyTx {
			tx, err := b.Begin(ctx)
			if err != nil {
				return jobs.Job{}, err
			}
			job, err := r.enqueueTx(ctx, tx, input)
			if err != nil {
				_ = tx.Rollback(ctx)
				return jobs.Job{}, err
			}
			if err := tx.Commit(ctx); err != nil {
				return jobs.Job{}, err
			}
			return job, nil
		}
	}
	return r.enqueueTx(ctx, r.db, input)
}

// EnqueueTx writes one job to a caller-owned transaction.  The transaction is
// deliberately not committed or rolled back by this method, allowing a
// producer to commit its state mutation and durable work atomically.
func (r *Repository) EnqueueTx(ctx context.Context, tx Tx, input jobs.EnqueueInput) (jobs.Job, error) {
	if tx == nil {
		return jobs.Job{}, fmt.Errorf("enqueue transaction is required")
	}
	return r.enqueueTx(ctx, tx, input)
}

func (r *Repository) enqueueTx(ctx context.Context, db DBTX, input jobs.EnqueueInput) (jobs.Job, error) {
	groups, actorErr := jobs.CanonicalActor(input.PrincipalID, input.GroupIDs)
	if !validInput(input, actorErr) {
		return jobs.Job{}, fmt.Errorf("invalid async job")
	}
	canonicalPayload, err := canonicalJSON(input.Payload, 1<<20)
	if err != nil {
		return jobs.Job{}, fmt.Errorf("invalid async job payload: %w", err)
	}
	input.Payload = canonicalPayload
	input.GroupIDs = groups
	digest := jobDigest(input, groups)
	q := queries(db)
	err = q.InsertJob(ctx, jobdb.InsertJobParams{ID: input.ID, Kind: input.Kind, WorkloadClass: input.WorkloadClass, PrincipalID: input.PrincipalID,
		GroupIds: groups, PartitionKey: input.PartitionKey, ResourceKind: input.ResourceKind, ResourceID: input.ResourceID,
		EstimatedMemoryBytes: input.EstimatedMemoryBytes, Payload: input.Payload, RequestDigest: digest})
	if err != nil {
		return jobs.Job{}, err
	}
	var storedDigest string
	storedDigest, err = q.GetRequestDigest(ctx, input.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Job{}, jobs.ErrNotFound
	}
	if err != nil {
		return jobs.Job{}, err
	}
	if storedDigest != digest {
		return jobs.Job{}, jobs.ErrConflict
	}
	row, err := q.GetJob(ctx, input.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Job{}, jobs.ErrNotFound
	}
	if err != nil {
		return jobs.Job{}, err
	}
	return fromGetJob(row)
}

func validInput(input jobs.EnqueueInput, actorErr error) bool {
	return canonicalLiteral(input.ID, 256) && canonicalLiteral(input.Kind, 128) &&
		(input.WorkloadClass == jobpolicy.WorkloadClassBackground || input.WorkloadClass == jobpolicy.WorkloadClassControl) &&
		canonicalLiteral(input.PartitionKey, 512) && canonicalLiteral(input.ResourceKind, 128) && canonicalLiteral(input.ResourceID, 256) &&
		input.EstimatedMemoryBytes > 0 && len(input.Payload) <= 1<<20 && json.Valid(input.Payload) && actorErr == nil
}

func jobDigest(input jobs.EnqueueInput, groups []string) string {
	groupJSON, _ := json.Marshal(groups)
	// The field separators and decimal memory representation make the request
	// identity unambiguous; payload bytes have already been canonicalized.
	sum := sha256.Sum256([]byte(input.Kind + "\x00" + input.WorkloadClass + "\x00" + input.PrincipalID + "\x00" + string(groupJSON) + "\x00" + input.PartitionKey + "\x00" + input.ResourceKind + "\x00" + input.ResourceID + "\x00" + fmt.Sprint(input.EstimatedMemoryBytes) + "\x00" + string(input.Payload)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (r *Repository) Get(ctx context.Context, id string) (jobs.Job, error) {
	if !canonicalLiteral(id, 256) {
		return jobs.Job{}, fmt.Errorf("invalid async job id")
	}
	row, err := queries(r.db).GetJob(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Job{}, jobs.ErrNotFound
	}
	if err != nil {
		return jobs.Job{}, err
	}
	return fromGetJob(row)
}

// GetTx reads a job through a caller-owned transaction so startup
// reconciliation can compare the canonical job row with its refresh link
// without opening a second snapshot.
func (r *Repository) GetTx(ctx context.Context, tx Tx, id string) (jobs.Job, error) {
	if tx == nil || !canonicalLiteral(id, 256) {
		return jobs.Job{}, fmt.Errorf("job transaction and id are required")
	}
	row, err := queries(tx).GetJob(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Job{}, jobs.ErrNotFound
	}
	if err != nil {
		return jobs.Job{}, err
	}
	return fromGetJob(row)
}

// LatestAttemptTx reads the exact attempt identified by the job's persisted
// attempt count and lease generation.  A missing row is reported separately
// so replay/recovery callers can distinguish an incomplete authority state
// from an ordinary not-found error.
func (r *Repository) LatestAttemptTx(ctx context.Context, tx Tx, id string, attemptNumber, fencingGeneration int64) (Attempt, bool, error) {
	if tx == nil || !canonicalLiteral(id, 256) || attemptNumber < 1 || fencingGeneration < 1 {
		return Attempt{}, false, fmt.Errorf("attempt transaction, job id and positive fence are required")
	}
	row, err := queries(tx).GetAttempt(ctx, jobdb.GetAttemptParams{JobID: id, AttemptNumber: attemptNumber, FencingGeneration: fencingGeneration})
	if errors.Is(err, pgx.ErrNoRows) {
		return Attempt{}, false, nil
	}
	if err != nil {
		return Attempt{}, false, err
	}
	return Attempt{JobID: row.JobID, AttemptNumber: row.AttemptNumber, FencingGeneration: row.FencingGeneration, Owner: row.Owner, Outcome: row.Outcome, FinishedAt: nullableTime(row.FinishedAt), ErrorJSON: append([]byte(nil), row.Error...)}, true, nil
}

// ActiveRefreshJobsTx returns only queued/running refresh jobs for startup
// reconciliation. The resource-kind/status index bounds this projection to
// live work; terminal history is never scanned.
func (r *Repository) ActiveRefreshJobsTx(ctx context.Context, tx Tx, afterCreated time.Time, afterID string, limit int) ([]jobs.Job, error) {
	if tx == nil || limit < 1 || limit > 200 || (afterID != "" && !canonicalLiteral(afterID, 256)) {
		return nil, fmt.Errorf("active refresh job projection is invalid")
	}
	rows, err := queries(tx).GetActiveRefreshJobs(ctx, jobdb.GetActiveRefreshJobsParams{ResourceKind: "refresh_run", AfterCreated: pgtype.Timestamptz{Time: nullableRecoveryTime(afterCreated), Valid: true}, AfterID: afterID, PageLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]jobs.Job, 0, limit)
	for _, row := range rows {
		job, scanErr := fromActiveRefreshJob(row)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, job)
	}
	return out, nil
}

func nullableRecoveryTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return value.UTC()
}

func (r *Repository) Candidates(ctx context.Context, workloadClass string, limit int) ([]jobs.Job, error) {
	if !validClass(workloadClass) || limit < 1 || limit > 200 {
		return nil, fmt.Errorf("workload class and candidate limit are required")
	}
	rows, err := queries(r.db).ListCandidates(ctx, jobdb.ListCandidatesParams{WorkloadClass: workloadClass, PageLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	result := make([]jobs.Job, 0, limit)
	for _, row := range rows {
		job, scanErr := fromCandidateJob(row)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, job)
	}
	return result, nil
}

// CandidatesByResourceKind is the bounded fairness projection for one
// capability's queue. It prevents unrelated platform jobs from consuming the
// candidate page and starving refresh work before the caller can inspect its
// scope/resource identity.
func (r *Repository) CandidatesByResourceKind(ctx context.Context, workloadClass, resourceKind string, limit int) ([]jobs.Job, error) {
	return r.CandidatesByResourceKindAfter(ctx, workloadClass, resourceKind, limit, time.Time{}, "")
}

// CandidatesByResourceKindAfter returns one keyset page of canonical
// candidates. Callers that apply a second capability-owned scope filter can
// advance through every page without allowing another project to starve the
// bounded result.
func (r *Repository) CandidatesByResourceKindAfter(ctx context.Context, workloadClass, resourceKind string, limit int, afterCreated time.Time, afterID string) ([]jobs.Job, error) {
	if !validClass(workloadClass) || !canonicalLiteral(resourceKind, 128) || limit < 1 || limit > 200 {
		return nil, fmt.Errorf("workload class, resource kind and candidate limit are required")
	}
	if !afterCreated.IsZero() && !canonicalLiteral(afterID, 256) {
		return nil, fmt.Errorf("candidate cursor id is required")
	}
	var cursor pgtype.Timestamptz
	if !afterCreated.IsZero() {
		cursor = pgtype.Timestamptz{Time: afterCreated.UTC(), Valid: true}
	}
	var cursorID *string
	if afterID != "" {
		cursorID = &afterID
	}
	rows, err := queries(r.db).ListCandidatesByResourceKind(ctx, jobdb.ListCandidatesByResourceKindParams{WorkloadClass: workloadClass, ResourceKind: resourceKind, AfterCreated: cursor, AfterID: cursorID, PageLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	result := make([]jobs.Job, 0, limit)
	for _, row := range rows {
		job, scanErr := fromCandidateResourceJob(row)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, job)
	}
	return result, nil
}

// ClaimByID uses one locking selection and one UPDATE ... RETURNING. SKIP
// LOCKED makes concurrent workers fail closed instead of waiting behind a
// lease owner that is still processing another claim.
func (r *Repository) ClaimByID(ctx context.Context, id, workloadClass, owner string, lease time.Duration) (jobs.Job, bool, error) {
	if !canonicalLiteral(id, 256) || !validClass(workloadClass) || !canonicalLiteral(owner, 256) || lease < time.Microsecond || lease > MaxLeaseDuration {
		return jobs.Job{}, false, fmt.Errorf("job id, workload class, worker owner, and positive lease are required")
	}
	return r.claimByID(ctx, r.db, id, workloadClass, owner, lease)
}

// ClaimByIDTx applies the same fenced claim through a caller-owned
// transaction. Refresh workers use this to claim the platform job and create
// the linked refresh attempt atomically; if either side loses its fence the
// entire claim rolls back and remains retryable.
func (r *Repository) ClaimByIDTx(ctx context.Context, tx Tx, id, workloadClass, owner string, lease time.Duration) (jobs.Job, bool, error) {
	if tx == nil {
		return jobs.Job{}, false, fmt.Errorf("job claim transaction is required")
	}
	if !canonicalLiteral(id, 256) || !validClass(workloadClass) || !canonicalLiteral(owner, 256) || lease < time.Microsecond || lease > MaxLeaseDuration {
		return jobs.Job{}, false, fmt.Errorf("job id, workload class, worker owner, and positive lease are required")
	}
	return r.claimByID(ctx, tx, id, workloadClass, owner, lease)
}

func (r *Repository) claimByID(ctx context.Context, db DBTX, id, workloadClass, owner string, lease time.Duration) (jobs.Job, bool, error) {
	row, err := queries(db).ClaimByID(ctx, jobdb.ClaimByIDParams{ID: id, WorkloadClass: workloadClass, Owner: owner, LeaseMicroseconds: lease.Microseconds()})
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Job{}, false, nil
	}
	if err != nil {
		return jobs.Job{}, false, err
	}
	job, err := fromClaimedJob(row)
	return job, err == nil, err
}

func (r *Repository) Renew(ctx context.Context, id string, fence jobs.Fence, lease time.Duration) error {
	if !validFence(id, fence) || lease < time.Microsecond || lease > MaxLeaseDuration {
		return fmt.Errorf("invalid async job fence")
	}
	return r.renewTx(ctx, r.db, id, fence, lease)
}

// RenewTx renews a job lease through a caller-owned transaction so the
// linked refresh attempt fence can be heartbeated atomically.
func (r *Repository) RenewTx(ctx context.Context, tx Tx, id string, fence jobs.Fence, lease time.Duration) error {
	if tx == nil || !validFence(id, fence) || lease < time.Microsecond || lease > MaxLeaseDuration {
		return fmt.Errorf("invalid async job fence")
	}
	return r.renewTx(ctx, tx, id, fence, lease)
}

func (r *Repository) renewTx(ctx context.Context, db DBTX, id string, fence jobs.Fence, lease time.Duration) error {
	count, err := queries(db).Renew(ctx, jobdb.RenewParams{ID: id, Owner: fence.Owner, LeaseMicroseconds: lease.Microseconds(), Generation: fence.Generation})
	return requireChanged(count, err)
}

func (r *Repository) Complete(ctx context.Context, id string, fence jobs.Fence) error {
	return r.terminal(ctx, id, fence, "succeeded", nil)
}

// CompleteTx applies a successful terminal transition through a caller-owned
// transaction.  Producers that also mutate another capability can therefore
// commit the job outcome and that capability's state atomically.
func (r *Repository) CompleteTx(ctx context.Context, tx Tx, id string, fence jobs.Fence) error {
	if tx == nil {
		return fmt.Errorf("job completion transaction is required")
	}
	return terminalTx(ctx, tx, id, fence, "succeeded", nil)
}

func (r *Repository) Fail(ctx context.Context, id string, fence jobs.Fence, problem []byte) error {
	canonical, err := canonicalJSON(problem, 65536)
	if err != nil {
		return fmt.Errorf("invalid async job failure JSON")
	}
	return r.terminal(ctx, id, fence, "failed", canonical)
}

// FailTx is the transaction-aware counterpart to Fail.
func (r *Repository) FailTx(ctx context.Context, tx Tx, id string, fence jobs.Fence, problem []byte) error {
	if tx == nil {
		return fmt.Errorf("job failure transaction is required")
	}
	canonical, err := canonicalJSON(problem, 65536)
	if err != nil {
		return fmt.Errorf("invalid async job failure JSON")
	}
	return terminalTx(ctx, tx, id, fence, "failed", canonical)
}

func (r *Repository) terminal(ctx context.Context, id string, fence jobs.Fence, outcome string, problem []byte) error {
	if !validFence(id, fence) {
		return fmt.Errorf("invalid async job fence")
	}
	errorJSON := []byte(`{}`)
	if problem != nil {
		errorJSON = problem
	}
	return terminalTx(ctx, r.db, id, fence, outcome, errorJSON)
}

func terminalTx(ctx context.Context, db DBTX, id string, fence jobs.Fence, outcome string, problem []byte) error {
	if !validFence(id, fence) {
		return fmt.Errorf("invalid async job fence")
	}
	errorJSON := []byte(`{}`)
	if problem != nil {
		errorJSON = problem
	}
	count, err := queries(db).Terminal(ctx, jobdb.TerminalParams{ID: id, Owner: fence.Owner, Outcome: outcome, Generation: fence.Generation, Error: errorJSON})
	return requireChanged(count, err)
}

// Retry requeues a claimed job with an explicitly persisted backoff. It is an
// extension of the public repository contract used by retrying workers; the
// existing Fail method remains terminal for callers that do not opt in.
func (r *Repository) Retry(ctx context.Context, id string, fence jobs.Fence, delay time.Duration, problem []byte) error {
	canonical, err := canonicalJSON(problem, 65536)
	if !validFence(id, fence) || delay < 0 || delay > MaxRetryDelay || err != nil {
		return fmt.Errorf("invalid async job retry")
	}
	count, err := queries(r.db).Retry(ctx, jobdb.RetryParams{ID: id, Owner: fence.Owner, Generation: fence.Generation, DelayMicroseconds: delay.Microseconds(), Error: canonical})
	return requireChanged(count, err)
}

func (r *Repository) Cancel(ctx context.Context, id string) error {
	if r == nil || r.db == nil || !canonicalLiteral(id, 256) {
		return fmt.Errorf("invalid async job id")
	}
	// A standalone cancellation must own one transaction because a queued
	// retry has two authoritative rows: the job projection and its latest
	// retrying attempt.  Locking and closing both rows together prevents a
	// replay from leaving a permanently retrying attempt behind.
	if b, ok := r.db.(beginner); ok {
		if _, alreadyTx := r.db.(pgx.Tx); !alreadyTx {
			tx, err := b.Begin(ctx)
			if err != nil {
				return err
			}
			if err := r.cancelTx(ctx, tx, id); err != nil {
				_ = tx.Rollback(ctx)
				return err
			}
			return tx.Commit(ctx)
		}
	}
	return r.cancelTx(ctx, r.db, id)
}

// CancelTx cancels a queued job through a caller-owned transaction. It is
// used when a refresh run and its pending platform job must become terminal
// together (for example, a user cancellation with audit intent).
func (r *Repository) CancelTx(ctx context.Context, tx Tx, id string) error {
	if tx == nil || !canonicalLiteral(id, 256) {
		return fmt.Errorf("job cancellation transaction and id are required")
	}
	return r.cancelTx(ctx, tx, id)
}

// cancelTx closes a queued job and, when it has been retried, its exact
// latest retrying attempt.  The caller owns the transaction and therefore
// controls whether this state transition commits with a domain mutation.
func (r *Repository) cancelTx(ctx context.Context, tx DBTX, id string) error {
	q := queries(tx)
	jobRow, err := q.LockJobForCancel(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.ErrConflict
	} else if err != nil {
		return err
	}
	status, attemptCount, leaseGeneration := jobRow.Status, jobRow.AttemptCount, jobRow.LeaseGeneration
	if status != string(jobs.StatusQueued) {
		return jobs.ErrConflict
	}

	if attemptCount > 0 {
		attemptRow, err := q.LockAttemptForCancel(ctx, jobdb.LockAttemptForCancelParams{JobID: id, AttemptNumber: attemptCount, FencingGeneration: leaseGeneration})
		if errors.Is(err, pgx.ErrNoRows) {
			return jobs.ErrConflict
		} else if err != nil {
			return err
		} else if attemptRow.Outcome != "retrying" || !attemptRow.FinishedAt.Valid || !attemptRow.RetryAt.Valid {
			return jobs.ErrConflict
		}
		result, err := q.CancelAttempt(ctx, jobdb.CancelAttemptParams{JobID: id, AttemptNumber: attemptCount, FencingGeneration: leaseGeneration})
		if err != nil {
			return err
		}
		if err := requireChanged(result.RowsAffected(), nil); err != nil {
			return err
		}
	}
	result, err := q.CancelJob(ctx, id)
	return requireChanged(result.RowsAffected(), err)
}

// ReconcileTerminalTx closes a job to the exact terminal outcome established
// by the authoritative refresh run. Existing terminal rows and their latest
// attempts must already agree; a contradiction is surfaced as ErrConflict
// rather than silently rewritten during startup recovery.
func (r *Repository) ReconcileTerminalTx(ctx context.Context, tx Tx, id string, desired jobs.Status) error {
	if tx == nil || !canonicalLiteral(id, 256) || (desired != jobs.StatusSucceeded && desired != jobs.StatusFailed && desired != jobs.StatusCancelled) {
		return fmt.Errorf("job reconciliation transaction, id and terminal status are required")
	}
	q := queries(tx)
	jobRow, err := q.LockJobReconcile(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.ErrNotFound
	} else if err != nil {
		return err
	}
	current := jobs.Status(jobRow.Status)
	attemptCount, leaseGeneration, leaseOwner, jobError := jobRow.AttemptCount, jobRow.LeaseGeneration, jobRow.LeaseOwner, jobRow.Error
	row, attemptErr := q.LatestAttemptForReconcile(ctx, id)
	attempt := Attempt{JobID: row.JobID, AttemptNumber: row.AttemptNumber, FencingGeneration: row.FencingGeneration, Owner: row.Owner, Outcome: row.Outcome, FinishedAt: nullableTime(row.FinishedAt), ErrorJSON: row.Error}
	var retryAt *time.Time
	if row.RetryAt.Valid {
		t := row.RetryAt.Time
		retryAt = &t
	}
	if attemptErr != nil && !errors.Is(attemptErr, pgx.ErrNoRows) {
		return attemptErr
	}
	foundAttempt := attemptErr == nil
	if attemptCount > 0 && (!foundAttempt || attempt.AttemptNumber != attemptCount || attempt.FencingGeneration != leaseGeneration) {
		return jobs.ErrConflict
	}
	if foundAttempt && attempt.AttemptNumber > attemptCount {
		return jobs.ErrConflict
	}
	if current == jobs.StatusSucceeded || current == jobs.StatusFailed || current == jobs.StatusCancelled {
		if current != desired {
			return jobs.ErrConflict
		}
		if desired == jobs.StatusSucceeded && !jsonEquivalent(jobError, []byte(`{}`)) {
			return jobs.ErrConflict
		}
		if attemptCount > 0 {
			if !foundAttempt || attempt.Outcome != string(desired) || attempt.FinishedAt == nil || retryAt != nil || !jsonEquivalent(attempt.ErrorJSON, jobError) {
				return jobs.ErrConflict
			}
		}
		return nil
	}
	if current != jobs.StatusQueued && current != jobs.StatusRunning {
		return jobs.ErrConflict
	}
	terminalError := []byte(`{"code":"REFRESH_RUN_TERMINAL"}`)
	if desired == jobs.StatusSucceeded {
		// Successful reconciliation is a clean completion, not a recovery
		// failure.  Keep the canonical empty error object expected by the jobs
		// authority's terminal invariant.
		terminalError = []byte(`{}`)
	}
	if current == jobs.StatusQueued {
		if attemptCount > 0 && (attempt.Outcome != "retrying" || attempt.FinishedAt == nil || retryAt == nil || !jsonEquivalent(attempt.ErrorJSON, jobError)) {
			return jobs.ErrConflict
		}
		// A run may be durably failed or cancelled before its queued job is
		// claimed; a successful run, however, must always have an execution
		// attempt that proves completion.
		if attemptCount == 0 && desired == jobs.StatusSucceeded {
			return jobs.ErrConflict
		}
		if attemptCount > 0 {
			result, err := q.CloseRetryingAttempt(ctx, jobdb.CloseRetryingAttemptParams{JobID: id, Outcome: string(desired), Error: terminalError, AttemptNumber: attemptCount, FencingGeneration: leaseGeneration})
			if err != nil {
				return err
			}
			if err := requireChanged(result.RowsAffected(), nil); err != nil {
				return err
			}
		}
	}
	if current == jobs.StatusRunning {
		if !foundAttempt {
			return jobs.ErrConflict
		}
		if attempt.Outcome != "running" && attempt.Outcome != string(desired) {
			return jobs.ErrConflict
		}
		if attempt.Outcome == "running" {
			if attempt.Owner != leaseOwner || attempt.FinishedAt != nil || retryAt != nil {
				return jobs.ErrConflict
			}
			result, err := q.CloseRunningAttempt(ctx, jobdb.CloseRunningAttemptParams{JobID: id, Outcome: string(desired), Error: terminalError, AttemptNumber: attemptCount, FencingGeneration: leaseGeneration})
			if err != nil {
				return err
			}
			if err := requireChanged(result.RowsAffected(), nil); err != nil {
				return err
			}
		} else {
			// A pre-terminalized attempt while the job still says running can
			// only be replayed when both rows already carry the exact recovery
			// evidence this reconciliation would write.  Otherwise accepting the
			// row would hide an owner/fence/error split across the authorities.
			if attempt.Owner != leaseOwner || attempt.FinishedAt == nil || retryAt != nil || !jsonEquivalent(attempt.ErrorJSON, terminalError) || !jsonEquivalent(jobError, terminalError) {
				return jobs.ErrConflict
			}
		}
	}
	result, err := q.ReconcileJob(ctx, jobdb.ReconcileJobParams{ID: id, Status: string(desired), Error: terminalError})
	if err != nil {
		return err
	}
	return requireChanged(result.RowsAffected(), nil)
}

// QuarantineQueuedTx closes one malformed queued job with durable poison
// evidence. A concurrent claimant wins the row lock and returns false so the
// caller can rely on that worker's fenced failure path instead.
func (r *Repository) QuarantineQueuedTx(ctx context.Context, tx Tx, id string, problem []byte) (bool, error) {
	if tx == nil || !canonicalLiteral(id, 256) {
		return false, fmt.Errorf("invalid async poison job")
	}
	canonical, err := canonicalJSON(problem, 65536)
	if err != nil {
		return false, fmt.Errorf("invalid async poison evidence")
	}
	q := queries(tx)
	jobRow, err := q.LockJobQuarantine(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	status, attemptCount, leaseGeneration, existing := jobRow.Status, jobRow.AttemptCount, jobRow.LeaseGeneration, jobRow.Error
	if status != string(jobs.StatusQueued) {
		// Terminal replay is valid only for the exact canonical poison
		// evidence. Checking only the code would bless a tampered problem
		// payload and hide drift between the queue and refresh authorities.
		return status == string(jobs.StatusCancelled) && jsonEquivalent(existing, canonical), nil
	}
	if attemptCount > 0 {
		attemptRow, err := q.LockRetryingAttempt(ctx, jobdb.LockRetryingAttemptParams{JobID: id, AttemptNumber: attemptCount, FencingGeneration: leaseGeneration})
		if errors.Is(err, pgx.ErrNoRows) {
			return false, jobs.ErrConflict
		} else if err != nil {
			return false, err
		} else if attemptRow.Outcome != "retrying" || !attemptRow.FinishedAt.Valid || !attemptRow.RetryAt.Valid {
			return false, jobs.ErrConflict
		}
		result, err := q.QuarantineAttempt(ctx, jobdb.QuarantineAttemptParams{JobID: id, AttemptNumber: attemptCount, FencingGeneration: leaseGeneration, Error: canonical})
		if err != nil {
			return false, err
		}
		if err := requireChanged(result.RowsAffected(), nil); err != nil {
			return false, err
		}
	}
	result, err := q.QuarantineJob(ctx, jobdb.QuarantineJobParams{ID: id, Error: canonical})
	if err != nil {
		return false, err
	}
	if err := requireChanged(result.RowsAffected(), nil); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Repository) CancelClaimed(ctx context.Context, id string, fence jobs.Fence) error {
	if !validFence(id, fence) {
		return fmt.Errorf("invalid async job fence")
	}
	if b, ok := r.db.(beginner); ok {
		if _, alreadyTx := r.db.(pgx.Tx); !alreadyTx {
			tx, err := b.Begin(ctx)
			if err != nil {
				return err
			}
			if err := r.cancelClaimed(ctx, tx, id, fence); err != nil {
				_ = tx.Rollback(ctx)
				return err
			}
			return tx.Commit(ctx)
		}
	}
	return r.cancelClaimed(ctx, r.db, id, fence)
}

// CancelClaimedTx terminalizes a running job and its current attempt through
// a caller-owned transaction. Refresh supersession uses this to close the
// canonical job atomically with its refresh run tree.
func (r *Repository) CancelClaimedTx(ctx context.Context, tx Tx, id string, fence jobs.Fence) error {
	if tx == nil || !validFence(id, fence) {
		return fmt.Errorf("invalid async job fence")
	}
	return r.cancelClaimed(ctx, tx, id, fence)
}

func (r *Repository) cancelClaimed(ctx context.Context, tx DBTX, id string, fence jobs.Fence) error {
	count, err := queries(tx).CancelClaimed(ctx, jobdb.CancelClaimedParams{ID: id, Owner: fence.Owner, Generation: fence.Generation})
	return requireChanged(count, err)
}

// SupersedeTx terminalizes queued and running jobs selected by a refresh
// authority. The jobs capability owns the state-machine SQL; callers provide
// already-authorized job identities and this method performs no cross-schema
// joins.
func (r *Repository) SupersedeTx(ctx context.Context, tx Tx, ids []string) error {
	if tx == nil || len(ids) == 0 {
		return nil
	}
	ordered := append([]string(nil), ids...)
	sort.Strings(ordered)
	for i := 1; i < len(ordered); i++ {
		if ordered[i] == ordered[i-1] {
			return jobs.ErrConflict
		}
	}
	for _, id := range ordered {
		if !canonicalLiteral(id, 256) {
			return fmt.Errorf("invalid async job id")
		}
	}
	for _, id := range ordered {
		q := queries(tx)
		jobRow, err := q.LockJobSupersede(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return jobs.ErrConflict
		} else if err != nil {
			return err
		}
		status, attemptCount, leaseGeneration, leaseOwner := jobs.Status(jobRow.Status), jobRow.AttemptCount, jobRow.LeaseGeneration, jobRow.LeaseOwner
		if status != jobs.StatusQueued && status != jobs.StatusRunning {
			return jobs.ErrConflict
		}
		if attemptCount > 0 {
			attemptRow, attemptErr := q.LockAttemptSupersede(ctx, jobdb.LockAttemptSupersedeParams{JobID: id, AttemptNumber: attemptCount, FencingGeneration: leaseGeneration})
			if errors.Is(attemptErr, pgx.ErrNoRows) {
				return jobs.ErrConflict
			}
			if attemptErr != nil {
				return attemptErr
			}
			if status == jobs.StatusRunning && (attemptRow.Outcome != "running" || attemptRow.Owner != leaseOwner || attemptRow.FinishedAt.Valid || attemptRow.RetryAt.Valid) {
				return jobs.ErrConflict
			}
			if status == jobs.StatusQueued && (attemptRow.Outcome != "retrying" || !attemptRow.FinishedAt.Valid || !attemptRow.RetryAt.Valid) {
				return jobs.ErrConflict
			}
			if attemptRow.Outcome == "running" || attemptRow.Outcome == "retrying" {
				result, updateErr := q.SupersedeAttempt(ctx, jobdb.SupersedeAttemptParams{JobID: id, AttemptNumber: attemptCount, FencingGeneration: leaseGeneration})
				if updateErr != nil {
					return updateErr
				}
				if result.RowsAffected() != 1 {
					return jobs.ErrConflict
				}
			}
		}
		result, err := q.SupersedeJob(ctx, id)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return jobs.ErrConflict
		}
	}
	return nil
}

func (r *Repository) AppendEvent(ctx context.Context, resourceKind, resourceID, eventType string, data []byte) (jobs.Event, error) {
	return r.appendEvent(ctx, r.db, resourceKind, resourceID, eventType, data, "")
}

// AppendEventTx appends an event inside a caller-owned transaction.  This is
// the event-only counterpart to RecordWorkflow for domain transitions that do
// not schedule a follow-up job.
func (r *Repository) AppendEventTx(ctx context.Context, tx Tx, resourceKind, resourceID, eventType string, data []byte) (jobs.Event, error) {
	if tx == nil {
		return jobs.Event{}, fmt.Errorf("event transaction is required")
	}
	return r.appendEvent(ctx, tx, resourceKind, resourceID, eventType, data, "")
}

func (r *Repository) ListEvents(ctx context.Context, resourceKind, resourceID string, after int64, limit int) ([]jobs.Event, error) {
	if !canonicalLiteral(resourceKind, 128) || !canonicalLiteral(resourceID, 256) || after < 0 || limit < 1 || limit > 200 {
		return nil, fmt.Errorf("event limit must be between 1 and 200")
	}
	rows, err := queries(r.db).ListEvents(ctx, jobdb.ListEventsParams{ResourceKind: resourceKind, ResourceID: resourceID, AfterID: after, PageLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	result := make([]jobs.Event, 0, limit)
	for _, row := range rows {
		event, scanErr := fromDBEvent(row)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, event)
	}
	return result, nil
}

// Observe returns bounded queue-health rows without exposing payloads. The
// view is intentionally queryable by readonly roles for stuck/expired,
// retrying and dead-letter operational dashboards.
type Observation struct {
	ID, Kind, WorkloadClass, PrincipalID, Status, Health string
	Attempts, MaxAttempts, RetryCount, ExpiredCount      int64
	LeaseOwner, LeaseExpiresAt, AvailableAt, LastRetryAt string
}

func (r *Repository) Observe(ctx context.Context, workloadClass string, limit int) ([]Observation, error) {
	if !validClass(workloadClass) || limit < 1 || limit > 200 {
		return nil, fmt.Errorf("workload class and observation limit are required")
	}
	rows, err := queries(r.db).Observe(ctx, jobdb.ObserveParams{WorkloadClass: workloadClass, PageLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	result := make([]Observation, 0, limit)
	for _, row := range rows {
		var o Observation
		o.ID, o.Kind, o.WorkloadClass, o.PrincipalID, o.Status, o.Health = row.ID, row.Kind, row.WorkloadClass, row.PrincipalID, row.Status, row.Health
		o.Attempts, o.MaxAttempts, o.LeaseOwner, o.RetryCount, o.ExpiredCount = row.AttemptCount, row.MaxAttempts, row.LeaseOwner, row.RetryCount, row.ExpiredCount
		o.LeaseExpiresAt, o.AvailableAt, o.LastRetryAt = formatPgTimestamp(row.LeaseExpiresAt), formatPgTimestamp(row.AvailableAt), formatPgTimestamp(row.LastRetryAt)
		result = append(result, o)
	}
	return result, nil
}

// RecordWorkflow atomically appends the keyed event and optional follow-up
// job using the caller's pgx transaction. The transaction remains owned by
// the capability making the domain transition.
func (r *Repository) RecordWorkflow(ctx context.Context, tx Tx, intent jobs.WorkflowIntent) error {
	if tx == nil {
		return fmt.Errorf("workflow transaction is required")
	}
	event := intent.Event
	if !canonicalLiteral(event.Key, 256) || !canonicalLiteral(event.ResourceKind, 128) || !canonicalLiteral(event.ResourceID, 256) || !canonicalLiteral(event.EventType, 128) || len(event.Data) > 1<<20 || !json.Valid(event.Data) {
		return fmt.Errorf("invalid workflow event")
	}
	if _, err := r.appendEvent(ctx, tx, event.ResourceKind, event.ResourceID, event.EventType, event.Data, event.Key); err != nil {
		return err
	}
	if intent.Job.ID == "" {
		return nil
	}
	_, err := r.enqueueTx(ctx, tx, intent.Job)
	return err
}

// CommitWorkflow is the standalone transaction convenience for callers that
// own a pgxpool.Pool or pgx.Conn.
func (r *Repository) CommitWorkflow(ctx context.Context, intent jobs.WorkflowIntent) error {
	b, ok := r.db.(beginner)
	if !ok {
		return fmt.Errorf("postgres jobs database does not support transactions")
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := r.RecordWorkflow(ctx, tx, intent); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) appendEvent(ctx context.Context, db DBTX, kind, id, eventType string, data []byte, key string) (jobs.Event, error) {
	if !canonicalLiteral(kind, 128) || !canonicalLiteral(id, 256) || !canonicalLiteral(eventType, 128) || len(data) > 1<<20 || !json.Valid(data) || len(key) > 256 || (key != "" && !canonicalLiteral(key, 256)) {
		return jobs.Event{}, fmt.Errorf("invalid async event")
	}
	canonicalData, err := canonicalJSON(data, 1<<20)
	if err != nil {
		return jobs.Event{}, fmt.Errorf("invalid async event data: %w", err)
	}
	// A standalone append owns a short transaction so sequence allocation and
	// event insertion cannot be split across implicit autocommit statements.
	if b, ok := db.(beginner); ok {
		if _, alreadyTx := db.(pgx.Tx); !alreadyTx {
			tx, beginErr := b.Begin(ctx)
			if beginErr != nil {
				return jobs.Event{}, beginErr
			}
			event, appendErr := r.appendEvent(ctx, tx, kind, id, eventType, canonicalData, key)
			if appendErr != nil {
				_ = tx.Rollback(ctx)
				return jobs.Event{}, appendErr
			}
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return jobs.Event{}, commitErr
			}
			return event, nil
		}
	}

	// Lock the per-resource sequence row before checking a keyed replay. This
	// serializes keyed and ordinary events alike without advisory locks and
	// guarantees contiguous IDs even when two replays race.
	q := queries(db)
	if err := q.EnsureEventSequence(ctx, jobdb.EnsureEventSequenceParams{ResourceKind: kind, ResourceID: id}); err != nil {
		return jobs.Event{}, err
	}
	if _, err = q.LockEventSequence(ctx, jobdb.LockEventSequenceParams{ResourceKind: kind, ResourceID: id}); err != nil {
		return jobs.Event{}, err
	}
	if key != "" {
		var existing jobs.Event
		row, lookupErr := q.GetEventByKey(ctx, jobdb.GetEventByKeyParams{ResourceKind: kind, ResourceID: id, EventKey: key})
		if lookupErr == nil {
			existing = jobs.Event{ID: row.EventID, ResourceKind: row.ResourceKind, ResourceID: row.ResourceID, EventType: row.EventType, Data: row.Data, CreatedAt: formatPgTimestamp(row.CreatedAt)}
			if existing.EventType != eventType || !jsonEquivalent(existing.Data, canonicalData) {
				return jobs.Event{}, jobs.ErrConflict
			}
			existing.Data = append([]byte(nil), existing.Data...)
			return existing, nil
		}
		if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return jobs.Event{}, lookupErr
		}
	}
	var event jobs.Event
	event.ID, err = q.NextEventID(ctx, jobdb.NextEventIDParams{ResourceKind: kind, ResourceID: id})
	if err != nil {
		return jobs.Event{}, err
	}
	row, err := q.InsertEvent(ctx, jobdb.InsertEventParams{ResourceKind: kind, ResourceID: id, EventID: event.ID, EventType: eventType, EventKey: key, Data: canonicalData})
	if err != nil {
		return jobs.Event{}, err
	}
	event.ID, event.ResourceKind, event.ResourceID, event.EventType = row.EventID, row.ResourceKind, row.ResourceID, row.EventType
	event.Data = append([]byte(nil), row.Data...)
	event.CreatedAt = formatPgTimestamp(row.CreatedAt)
	return event, nil
}

func jsonEquivalent(left, right []byte) bool {
	leftCanonical, leftErr := canonicalJSON(left, 1<<20)
	rightCanonical, rightErr := canonicalJSON(right, 1<<20)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return bytes.Equal(leftCanonical, rightCanonical)
}

// canonicalJSON validates exactly one bounded JSON value, rejects duplicate
// object keys, and emits a stable representation for request identity and
// replay comparisons. json.Number preserves authored integer/decimal spelling
// instead of silently converting large values through float64.
func canonicalJSON(value []byte, maxBytes int) ([]byte, error) {
	if len(value) == 0 || len(value) > maxBytes {
		return nil, fmt.Errorf("JSON payload exceeds %d bytes", maxBytes)
	}
	var validated json.RawMessage
	if err := strictjson.DecodeWithOptions(value, &validated, strictjson.Options{
		MaxBytes: int64(maxBytes), MaxDepth: 100, DuplicateKeys: strictjson.CaseSensitiveKeys, AllowUnknownFields: true,
	}); err != nil {
		return nil, err
	}
	var decoded any
	dec := json.NewDecoder(bytes.NewReader(value))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return nil, err
	}
	if len(canonical) > maxBytes {
		return nil, fmt.Errorf("canonical JSON payload exceeds %d bytes", maxBytes)
	}
	return canonical, nil
}

func mapJob(id, kind, workloadClass, principalID string, groups []string, partitionKey, resourceKind, resourceID string, estimatedMemory int64, payload []byte, status string, attempts int64, leaseOwner string, leaseExpires, created, started, finished pgtype.Timestamptz, generation int64, errorJSON []byte) (jobs.Job, error) {
	job := jobs.Job{ID: id, Kind: kind, WorkloadClass: workloadClass, PrincipalID: principalID, PartitionKey: partitionKey, ResourceKind: resourceKind, ResourceID: resourceID, EstimatedMemoryBytes: estimatedMemory, LeaseOwner: leaseOwner, LeaseGeneration: generation, Status: jobs.Status(status), Attempts: int(attempts), Payload: append([]byte(nil), payload...), ErrorJSON: string(errorJSON)}
	canonicalGroups, err := jobs.CanonicalGroups(groups)
	if err != nil || !equalStrings(groups, canonicalGroups) {
		return jobs.Job{}, fmt.Errorf("invalid persisted async job groups")
	}
	job.GroupIDs = canonicalGroups
	job.LeaseExpiresAt, job.CreatedAt, job.StartedAt, job.FinishedAt = formatPgTimestamp(leaseExpires), formatPgTimestamp(created), formatPgTimestamp(started), formatPgTimestamp(finished)
	return job, nil
}

func fromGetJob(r jobdb.GetJobRow) (jobs.Job, error) {
	return mapJob(r.ID, r.Kind, r.WorkloadClass, r.PrincipalID, r.GroupIds, r.PartitionKey, r.ResourceKind, r.ResourceID, r.EstimatedMemoryBytes, r.Payload, r.Status, r.AttemptCount, r.LeaseOwner, r.LeaseExpiresAt, r.CreatedAt, r.StartedAt, r.FinishedAt, r.LeaseGeneration, r.Error)
}
func fromActiveRefreshJob(r jobdb.GetActiveRefreshJobsRow) (jobs.Job, error) {
	return mapJob(r.ID, r.Kind, r.WorkloadClass, r.PrincipalID, r.GroupIds, r.PartitionKey, r.ResourceKind, r.ResourceID, r.EstimatedMemoryBytes, r.Payload, r.Status, r.AttemptCount, r.LeaseOwner, r.LeaseExpiresAt, r.CreatedAt, r.StartedAt, r.FinishedAt, r.LeaseGeneration, r.Error)
}
func fromCandidateJob(r jobdb.ListCandidatesRow) (jobs.Job, error) {
	return mapJob(r.ID, r.Kind, r.WorkloadClass, r.PrincipalID, r.GroupIds, r.PartitionKey, r.ResourceKind, r.ResourceID, r.EstimatedMemoryBytes, r.Payload, r.Status, r.AttemptCount, r.LeaseOwner, r.LeaseExpiresAt, r.CreatedAt, r.StartedAt, r.FinishedAt, r.LeaseGeneration, r.Error)
}
func fromCandidateResourceJob(r jobdb.ListCandidatesByResourceKindRow) (jobs.Job, error) {
	return mapJob(r.ID, r.Kind, r.WorkloadClass, r.PrincipalID, r.GroupIds, r.PartitionKey, r.ResourceKind, r.ResourceID, r.EstimatedMemoryBytes, r.Payload, r.Status, r.AttemptCount, r.LeaseOwner, r.LeaseExpiresAt, r.CreatedAt, r.StartedAt, r.FinishedAt, r.LeaseGeneration, r.Error)
}
func fromClaimedJob(r jobdb.ClaimByIDRow) (jobs.Job, error) {
	return mapJob(r.ID, r.Kind, r.WorkloadClass, r.PrincipalID, r.GroupIds, r.PartitionKey, r.ResourceKind, r.ResourceID, r.EstimatedMemoryBytes, r.Payload, r.Status, r.AttemptCount, r.LeaseOwner, r.LeaseExpiresAt, r.CreatedAt, r.StartedAt, r.FinishedAt, r.LeaseGeneration, r.Error)
}

func fromDBEvent(r jobdb.ListEventsRow) (jobs.Event, error) {
	return jobs.Event{ID: r.EventID, ResourceKind: r.ResourceKind, ResourceID: r.ResourceID, EventType: r.EventType, Data: append([]byte(nil), r.Data...), CreatedAt: formatPgTimestamp(r.CreatedAt)}, nil
}
func fromInsertedEvent(r jobdb.InsertEventRow) jobs.Event {
	return jobs.Event{ID: r.EventID, ResourceKind: r.ResourceKind, ResourceID: r.ResourceID, EventType: r.EventType, Data: append([]byte(nil), r.Data...), CreatedAt: formatPgTimestamp(r.CreatedAt)}
}

func nullableTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time
	return &t
}

func formatPgTimestamp(value pgtype.Timestamptz) string {
	if !value.Valid {
		return ""
	}
	return formatTimestamp(value.Time)
}

func validFence(id string, fence jobs.Fence) bool {
	return canonicalLiteral(id, 256) && canonicalLiteral(fence.Owner, 256) && fence.Generation > 0
}
func validClass(class string) bool {
	return class == jobpolicy.WorkloadClassBackground || class == jobpolicy.WorkloadClassControl
}
func canonicalLiteral(value string, max int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= max && strings.IndexFunc(value, unicode.IsControl) < 0
}
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func formatTimestamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	// Preserve the database timestamp's full precision.  Cursor consumers use
	// (created_at,id) keyset ordering; second-level formatting can repeat a
	// cursor when a page contains rows created in the same second and cause an
	// unbounded pagination loop.
	return value.UTC().Format(time.RFC3339Nano)
}
func formatOptionalTimestamp(value *time.Time) string {
	if value == nil {
		return ""
	}
	return formatTimestamp(*value)
}
func requireChanged(changed int64, err error) error {
	if err != nil {
		return err
	}
	if changed != 1 {
		return jobs.ErrConflict
	}
	return nil
}
