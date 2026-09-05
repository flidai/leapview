// Package postgres stores LeapView-owned asynchronous operation history and
// product events. River owns the operational queue and worker state.
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
	"sync"
	"sync/atomic"
	"time"

	jobdb "github.com/flidai/leapview/internal/platform/jobs/postgres/internal/db"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

const (
	MaxAttempts     = 3
	maxEventPage    = 200
	maxPayloadBytes = 1 << 20
)

// ErrStaleRiverClaim distinguishes an executor that lost its River ownership
// from a current worker reporting an ordinary product conflict. River's
// completion API is keyed only by job ID, so callers must not return a stale
// result while a successor still owns the operational row.
var ErrStaleRiverClaim = errors.New("stale River execution claim")

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type MaintenanceDBTX interface{ DBTX }

type Tx = pgx.Tx

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}
type nativePoolProvider interface{ NativePool() *pgxpool.Pool }

// ExecutionArgs is the stable LeapView identity carried by River. River IDs
// never escape as public operation IDs.
type ExecutionArgs struct {
	ProductJobID  string `json:"product_job_id" river:"unique"`
	RequestDigest string `json:"request_digest" river:"unique"`
}

type AgentRunArgs ExecutionArgs

func (AgentRunArgs) Kind() string { return "agent.run" }

type UploadFinalizeArgs ExecutionArgs

func (UploadFinalizeArgs) Kind() string { return "upload.finalize" }

type ReleaseFinalizeArgs ExecutionArgs

func (ReleaseFinalizeArgs) Kind() string { return "release.finalize" }

type DeploymentActivateArgs ExecutionArgs

func (DeploymentActivateArgs) Kind() string { return "deployment.activate" }

type ApprovalActivateArgs ExecutionArgs

func (ApprovalActivateArgs) Kind() string { return "delivery.approval.activate" }

type RefreshPipelineArgs ExecutionArgs

func (RefreshPipelineArgs) Kind() string { return "refresh_pipeline" }

var admittedKinds = map[string]struct{}{
	"agent.run": {}, "upload.finalize": {}, "release.finalize": {},
	"deployment.activate": {}, "delivery.approval.activate": {},
	"refresh_pipeline": {},
}

type Repository struct {
	db     DBTX
	mu     sync.RWMutex
	client *river.Client[pgx.Tx]
}

type Maintenance struct{ db MaintenanceDBTX }

type riverCompletion struct {
	done atomic.Bool
	// riverJobID, owner, generation, and leaseExpiresAt are an ephemeral
	// execution fence for the exact River row locked by the worker. Product
	// history deliberately does not persist River lease columns; Get/GetTx
	// project these values only while this context is in scope.
	riverJobID     int64
	owner          string
	generation     int64
	leaseExpiresAt time.Time
	complete       func(context.Context, pgx.Tx) error
}
type riverCompletionKey struct{}

// ContextWithRiverExecution binds the exact River execution fence to a
// capability-owned context. The owner and timeout are supplied by the job
// module so product handlers can verify the active attempt without persisting
// executor-specific lease columns in product history.
func ContextWithRiverExecution[T river.JobArgs](ctx context.Context, job *river.Job[T], owner string, leaseTimeout time.Duration) context.Context {
	completion := &riverCompletion{}
	if job != nil {
		completion.riverJobID = job.ID
		completion.generation = int64(job.Attempt)
		completion.owner = strings.TrimSpace(owner)
		if completion.owner == "" && len(job.AttemptedBy) > 0 {
			completion.owner = strings.TrimSpace(job.AttemptedBy[len(job.AttemptedBy)-1])
		}
		if leaseTimeout <= 0 {
			leaseTimeout = river.JobTimeoutDefault
		}
		completion.leaseExpiresAt = time.Now().UTC().Add(leaseTimeout)
	}
	completion.complete = func(completeCtx context.Context, tx pgx.Tx) error {
		if job == nil {
			return errors.New("River completion requires a locked job")
		}
		if _, err := river.JobCompleteTx[*riverpgxv5.Driver](completeCtx, tx, job); err != nil {
			return err
		}
		completion.done.Store(true)
		return nil
	}
	return context.WithValue(ctx, riverCompletionKey{}, completion)
}

func completionFromContext(ctx context.Context) *riverCompletion {
	completion, _ := ctx.Value(riverCompletionKey{}).(*riverCompletion)
	return completion
}

func RiverCompletionDone(ctx context.Context) bool {
	completion := completionFromContext(ctx)
	return completion != nil && completion.done.Load()
}

var _ jobs.Repository = (*Repository)(nil)

func queries(db DBTX) *jobdb.Queries { return jobdb.New(db) }

func NewRepository(db DBTX) *Repository              { return &Repository{db: db} }
func New(db DBTX) *Repository                        { return NewRepository(db) }
func NewMaintenance(db MaintenanceDBTX) *Maintenance { return &Maintenance{db: db} }
func (r *Repository) DB() DBTX {
	if r == nil {
		return nil
	}
	return r.db
}
func (r *Repository) Configured() bool { return r != nil && nativeDBConfigured(r.db) }

func nativeDBConfigured(db DBTX) bool {
	if db == nil {
		return false
	}
	v := reflect.ValueOf(db)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !v.IsNil()
	default:
		return true
	}
}

func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return errors.New("schema transaction is required")
	}
	_, err := tx.Exec(ctx, schemaSQL) // sqlc-exception: schema-ddl
	return err
}

// NativePool returns the one admitted pool needed by River's pgx driver.
func (r *Repository) NativePool() (*pgxpool.Pool, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("PostgreSQL jobs repository is not configured")
	}
	if pool, ok := r.db.(*pgxpool.Pool); ok && pool != nil {
		return pool, nil
	}
	if provider, ok := r.db.(nativePoolProvider); ok && provider.NativePool() != nil {
		return provider.NativePool(), nil
	}
	return nil, errors.New("PostgreSQL jobs repository does not expose an admitted pgx pool")
}

func (r *Repository) ConfigureRiver(client *river.Client[pgx.Tx]) error {
	if r == nil || client == nil {
		return errors.New("River client is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client != nil && r.client != client {
		return errors.New("River client is already configured")
	}
	r.client = client
	return nil
}

func (r *Repository) riverClient() (*river.Client[pgx.Tx], error) {
	if r == nil {
		return nil, jobs.ErrStoreRequired
	}
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()
	if client != nil {
		return client, nil
	}
	pool, err := r.NativePool()
	if err != nil {
		return nil, err
	}
	insertClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return nil, fmt.Errorf("configure River insertion: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client == nil {
		r.client = insertClient
	}
	return r.client, nil
}

func validateInput(input jobs.EnqueueInput) ([]string, []byte, string, error) {
	groups, err := jobs.CanonicalActor(input.PrincipalID, input.GroupIDs)
	if err != nil {
		return nil, nil, "", errors.New("invalid async job actor")
	}
	if input.ID == "" || input.Kind == "" || input.PartitionKey == "" || input.ResourceKind == "" || input.ResourceID == "" || input.EstimatedMemoryBytes <= 0 || len(input.Payload) == 0 || len(input.Payload) > maxPayloadBytes {
		return nil, nil, "", errors.New("invalid async job")
	}
	if _, ok := admittedKinds[input.Kind]; !ok {
		return nil, nil, "", errors.Join(jobs.ErrUnknownKind, errors.New(input.Kind))
	}
	if input.WorkloadClass != "control" && input.WorkloadClass != "background" {
		return nil, nil, "", errors.New("invalid async job workload class")
	}
	canonical, err := canonicalJSON(input.Payload)
	if err != nil {
		return nil, nil, "", fmt.Errorf("invalid async job payload: %w", err)
	}
	if len(canonical) == 0 || (canonical[0] != '{' && canonical[0] != '[') {
		return nil, nil, "", errors.New("async job payload must be an object or array")
	}
	h := sha256.New()
	for _, value := range []string{input.ID, input.Kind, input.WorkloadClass, input.PrincipalID, strings.Join(groups, "\x00"), input.PartitionKey, input.ResourceKind, input.ResourceID, fmt.Sprintf("%d", input.EstimatedMemoryBytes), string(canonical)} {
		h.Write([]byte(value))
		h.Write([]byte{0})
	}
	return groups, canonical, "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func riverArgs(kind string, common ExecutionArgs) (river.JobArgs, error) {
	switch kind {
	case "agent.run":
		return AgentRunArgs(common), nil
	case "upload.finalize":
		return UploadFinalizeArgs(common), nil
	case "release.finalize":
		return ReleaseFinalizeArgs(common), nil
	case "deployment.activate":
		return DeploymentActivateArgs(common), nil
	case "delivery.approval.activate":
		return ApprovalActivateArgs(common), nil
	case "refresh_pipeline":
		return RefreshPipelineArgs(common), nil
	default:
		return nil, errors.Join(jobs.ErrUnknownKind, errors.New(kind))
	}
}

func (r *Repository) Enqueue(ctx context.Context, input jobs.EnqueueInput) (jobs.Job, error) {
	begin, ok := r.db.(beginner)
	if r == nil || !ok {
		return jobs.Job{}, errors.New("PostgreSQL jobs transaction authority is required")
	}
	tx, err := begin.Begin(ctx)
	if err != nil {
		return jobs.Job{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	result, err := r.EnqueueTx(ctx, tx, input)
	if err != nil {
		return jobs.Job{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return jobs.Job{}, err
	}
	return result, nil
}

func (r *Repository) EnqueueTx(ctx context.Context, tx Tx, input jobs.EnqueueInput) (jobs.Job, error) {
	if tx == nil {
		return jobs.Job{}, errors.New("enqueue transaction is required")
	}
	groups, canonicalPayload, digest, err := validateInput(input)
	if err != nil {
		return jobs.Job{}, err
	}
	groupsJSON, _ := json.Marshal(groups)
	inserted, err := queries(tx).InsertJobHistory(ctx, jobdb.InsertJobHistoryParams{
		ID: input.ID, Kind: input.Kind, WorkloadClass: input.WorkloadClass,
		PrincipalID: input.PrincipalID, GroupIds: groupsJSON,
		PartitionKey: input.PartitionKey, ResourceKind: input.ResourceKind,
		ResourceID: input.ResourceID, EstimatedMemoryBytes: input.EstimatedMemoryBytes,
		Payload: canonicalPayload, RequestDigest: digest,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return jobs.Job{}, err
	}
	if !inserted {
		current, err := r.get(ctx, tx, input.ID)
		if err != nil {
			return jobs.Job{}, err
		}
		storedDigest, err := queries(tx).GetJobRequestDigest(ctx, input.ID)
		if err != nil {
			return jobs.Job{}, err
		}
		if storedDigest != digest {
			return jobs.Job{}, jobs.ErrConflict
		}
		return current, nil
	}
	client, err := r.riverClient()
	if err != nil {
		return jobs.Job{}, err
	}
	args, err := riverArgs(input.Kind, ExecutionArgs{ProductJobID: input.ID, RequestDigest: digest})
	if err != nil {
		return jobs.Job{}, err
	}
	pgxTx, err := nativeTransaction(tx)
	if err != nil {
		return jobs.Job{}, err
	}
	insert, err := client.InsertTx(ctx, pgxTx, args, &river.InsertOpts{Queue: input.WorkloadClass, MaxAttempts: MaxAttempts, UniqueOpts: river.UniqueOpts{ByArgs: true}})
	if err != nil {
		return jobs.Job{}, err
	}
	if insert == nil || insert.Job == nil {
		return jobs.Job{}, errors.New("River did not return an inserted job")
	}
	riverJobID := insert.Job.ID
	if _, err := queries(tx).UpdateRiverJobID(ctx, jobdb.UpdateRiverJobIDParams{ID: input.ID, RiverJobID: &riverJobID}); err != nil {
		return jobs.Job{}, err
	}
	return r.get(ctx, tx, input.ID)
}

func (r *Repository) Get(ctx context.Context, id string) (jobs.Job, error) {
	return r.get(ctx, r.db, id)
}
func (r *Repository) GetTx(ctx context.Context, tx Tx, id string) (jobs.Job, error) {
	if tx == nil {
		return jobs.Job{}, errors.New("job transaction is required")
	}
	return r.get(ctx, tx, id)
}

func (r *Repository) get(ctx context.Context, db DBTX, id string) (jobs.Job, error) {
	if db == nil || strings.TrimSpace(id) == "" {
		return jobs.Job{}, errors.New("job database and id are required")
	}
	row, err := queries(db).GetJob(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Job{}, jobs.ErrNotFound
	}
	if err != nil {
		return jobs.Job{}, err
	}
	var result jobs.Job
	if err := json.Unmarshal([]byte(row.GroupIds), &result.GroupIDs); err != nil {
		return jobs.Job{}, err
	}
	result.ID = row.ID
	result.Kind = row.Kind
	result.WorkloadClass = row.WorkloadClass
	result.PrincipalID = row.PrincipalID
	result.PartitionKey = row.PartitionKey
	result.ResourceKind = row.ResourceKind
	result.ResourceID = row.ResourceID
	result.EstimatedMemoryBytes = row.EstimatedMemoryBytes
	result.Payload = []byte(row.Payload)
	result.RequestDigest = row.RequestDigest
	result.Status = jobs.Status(row.Status)
	result.Attempts = int(row.AttemptCount)
	result.CreatedAt = row.CreatedAt.UTC().Format(time.RFC3339Nano)
	if row.StartedAt.Valid {
		result.StartedAt = row.StartedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	if row.FinishedAt.Valid {
		result.FinishedAt = row.FinishedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	if row.Error != "null" {
		result.ErrorJSON = row.Error
	}
	// River's claim fence is executor state, not product history. Project it
	// only when this context carries the exact locked River row for the job;
	// ordinary reads therefore never expose a synthetic lease.
	if completion := completionFromContext(ctx); completion != nil && completion.riverJobID > 0 && row.RiverJobID != nil && *row.RiverJobID == completion.riverJobID && result.Status == jobs.StatusRunning && completion.generation > 0 && completion.owner != "" && !completion.leaseExpiresAt.IsZero() {
		result.LeaseOwner = completion.owner
		result.LeaseGeneration = completion.generation
		result.LeaseExpiresAt = completion.leaseExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return result, nil
}

func (r *Repository) RiverJobIDTx(ctx context.Context, tx Tx, id string) (int64, error) {
	if tx == nil {
		return 0, errors.New("job transaction is required")
	}
	riverID, err := queries(tx).GetRiverJobID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, jobs.ErrNotFound
	} else if err != nil {
		return 0, err
	}
	if riverID == nil || *riverID <= 0 {
		return 0, errors.New("product job has no River identity")
	}
	return *riverID, nil
}

// ValidateCurrentClaimTx locks the River row bound to a product job and
// verifies the exact owner/attempt fence. The caller must invoke this on the
// same transaction that admits its product-side attempt, so a rescued worker
// cannot establish a refresh lease after River has handed the job to a newer
// attempt.
func (r *Repository) ValidateCurrentClaimTx(ctx context.Context, tx Tx, id string, fence jobs.Fence) error {
	// Direct adapter callers retain the narrow bootstrap compatibility used by
	// poison-payload handling: an available, never-attempted River row may be
	// terminalized before River starts a worker. A real worker always carries
	// completion context, so rescued or otherwise non-running rows fail closed.
	return r.lockRiverFence(ctx, tx, id, fence, completionFromContext(ctx) == nil)
}

// ValidateCurrentClaim checks the River fence in a short transaction without
// mutating product history. It lets the outer jobs module distinguish a stale
// executor from a current worker whose product operation is invalid.
func (r *Repository) ValidateCurrentClaim(ctx context.Context, id string, fence jobs.Fence) error {
	return r.inTx(ctx, func(tx Tx) error {
		return r.lockRiverFence(ctx, tx, id, fence, false)
	})
}

// ValidateRiverResultClaim proves that an outer River result can still be
// reported for this exact attempt. Terminal or deleted rows are also safe:
// River's state update is conditional on running and will therefore be a
// no-op. Any nonterminal row owned by another attempt is a stale claim.
func (r *Repository) ValidateRiverResultClaim(ctx context.Context, riverJobID int64, fence jobs.Fence) error {
	return r.inTx(ctx, func(tx Tx) error {
		row, err := queries(tx).LockRiverJobFence(ctx, riverJobID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		state := rivertype.JobState(row.State)
		if state == rivertype.JobStateCompleted || state == rivertype.JobStateCancelled || state == rivertype.JobStateDiscarded {
			return nil
		}
		if state == rivertype.JobStateRunning && int64(row.Attempt) == fence.Generation && fence.Owner != "" && len(row.AttemptedBy) > 0 && row.AttemptedBy[len(row.AttemptedBy)-1] == fence.Owner {
			return nil
		}
		return staleRiverConflict()
	})
}

// WaitForRiverClaimFinalization waits until the operational row is terminal or
// deleted. A stale executor must not return while a successor is running or
// merely retryable: River reports every worker result by job ID, so returning
// earlier could finalize a later attempt between the poll and River's result
// report. Cancellation is intentionally observed only after the row reaches a
// terminal state so a successor remains protected; River's rescuer and the
// successor's own result provide eventual terminality.
func (r *Repository) WaitForRiverClaimFinalization(ctx context.Context, riverJobID int64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	waitCtx := context.WithoutCancel(ctx)
	for {
		probeCtx, cancel := context.WithTimeout(waitCtx, time.Second)
		state, found, err := r.riverClaimState(probeCtx, riverJobID)
		cancel()
		if err != nil {
			// Keep the stale worker from reporting an ID-only result while the
			// database is unavailable. Returning while the state is unknown
			// would let River race a recovered connection and finalize a live
			// successor. The bounded probe timeout keeps each attempt finite.
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if !found || state == rivertype.JobStateCompleted || state == rivertype.JobStateCancelled || state == rivertype.JobStateDiscarded {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (r *Repository) riverClaimState(ctx context.Context, riverJobID int64) (rivertype.JobState, bool, error) {
	var state rivertype.JobState
	var found bool
	err := r.inTx(ctx, func(tx Tx) error {
		row, err := queries(tx).LockRiverJobFence(ctx, riverJobID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		state = rivertype.JobState(row.State)
		found = true
		return nil
	})
	return state, found, err
}

// lockRiverFence locks the operational row that is bound to a product job and
// verifies the exact River claim before the caller mutates product history or
// River state. River's completion/cancellation helpers identify a row by ID;
// the lock closes the gap where a reclaimed row could otherwise be acted on by
// a stale worker. allowInitial admits the narrow non-worker bootstrap path
// used by direct adapters: a freshly inserted, still-available River row can
// be terminalized by product-side validation before River claims it. A real
// worker context never receives this exception.
func (r *Repository) lockRiverFence(ctx context.Context, tx Tx, id string, fence jobs.Fence, allowInitial bool) error {
	if tx == nil || strings.TrimSpace(id) == "" || fence.Generation <= 0 {
		return errors.New("River execution fence is invalid")
	}
	if strings.TrimSpace(fence.Owner) != fence.Owner {
		return errors.New("River execution owner is not canonical")
	}
	riverID, err := r.RiverJobIDTx(ctx, tx, id)
	if err != nil {
		return err
	}
	row, err := queries(tx).LockRiverJobFence(ctx, riverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return staleRiverConflict()
	}
	if err != nil {
		return err
	}
	if allowInitial && completionFromContext(ctx) == nil && int(row.Attempt) == 0 && fence.Generation == 1 && rivertype.JobState(row.State) == rivertype.JobStateAvailable {
		return nil
	}
	if rivertype.JobState(row.State) != rivertype.JobStateRunning || int(row.Attempt) != int(fence.Generation) {
		return staleRiverConflict()
	}
	if fence.Owner == "" || len(row.AttemptedBy) == 0 || row.AttemptedBy[len(row.AttemptedBy)-1] != fence.Owner {
		return staleRiverConflict()
	}
	if completion := completionFromContext(ctx); completion != nil && completion.riverJobID > 0 {
		if completion.riverJobID != riverID || completion.generation != fence.Generation || completion.owner != fence.Owner {
			return staleRiverConflict()
		}
	}
	return nil
}

// lockRiverMarkFence is the claim check used while projecting River's claim
// into product history. A freshly inserted River row has attempt zero and is
// still available; retaining this one compatibility case supports callers
// that advance product history in a transaction before River's worker starts.
// Once River has started an attempt, however, only that exact running attempt
// may mark the product row running.
func (r *Repository) lockRiverMarkFence(ctx context.Context, tx Tx, id string, attempt int) error {
	if tx == nil || strings.TrimSpace(id) == "" || attempt < 1 {
		return errors.New("River execution fence is invalid")
	}
	riverID, err := r.RiverJobIDTx(ctx, tx, id)
	if err != nil {
		return err
	}
	row, err := queries(tx).LockRiverJobFence(ctx, riverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return staleRiverConflict()
	}
	if err != nil {
		return err
	}
	if int(row.Attempt) == 0 && attempt == 1 && completionFromContext(ctx) == nil && rivertype.JobState(row.State) == rivertype.JobStateAvailable {
		return nil
	}
	if int(row.Attempt) != attempt || rivertype.JobState(row.State) != rivertype.JobStateRunning {
		return staleRiverConflict()
	}
	completion := completionFromContext(ctx)
	if completion == nil || completion.riverJobID <= 0 || completion.generation != int64(attempt) || completion.owner == "" {
		return staleRiverConflict()
	}
	if completion.riverJobID != riverID || len(row.AttemptedBy) == 0 || row.AttemptedBy[len(row.AttemptedBy)-1] != completion.owner {
		return staleRiverConflict()
	}
	return nil
}

func staleRiverConflict() error {
	return fmt.Errorf("%w: %w", jobs.ErrConflict, ErrStaleRiverClaim)
}

func (r *Repository) MarkRunning(ctx context.Context, id string, attempt int) (jobs.Job, error) {
	if attempt < 1 {
		return jobs.Job{}, errors.New("River attempt must be positive")
	}
	var current jobs.Job
	err := r.inTx(ctx, func(tx Tx) error {
		if err := r.lockRiverMarkFence(ctx, tx, id, attempt); err != nil {
			return err
		}
		changed, err := queries(tx).MarkJobRunning(ctx, jobdb.MarkJobRunningParams{ID: id, Attempt: int32(attempt)})
		if err != nil {
			return err
		}
		current, err = r.get(ctx, tx, id)
		if err != nil {
			return err
		}
		if changed == 0 || current.Status != jobs.StatusRunning || current.Attempts < attempt {
			return jobs.ErrConflict
		}
		return nil
	})
	if err != nil {
		return jobs.Job{}, err
	}
	return current, nil
}

func (r *Repository) setTerminalTx(ctx context.Context, tx Tx, id string, fence jobs.Fence, status jobs.Status, problem []byte) error {
	if status != jobs.StatusSucceeded && status != jobs.StatusFailed && status != jobs.StatusCancelled {
		return errors.New("terminal product job status is required")
	}
	if fence.Generation < 0 {
		return errors.New("terminal product job fence is invalid")
	}
	if status == jobs.StatusFailed && !json.Valid(problem) {
		return errors.New("failure evidence must be valid JSON")
	}
	var changed int64
	var err error
	if status == jobs.StatusFailed {
		changed, err = queries(tx).SetJobTerminalWithError(ctx, jobdb.SetJobTerminalWithErrorParams{
			ID: id, Status: string(status), Problem: problem, FenceGeneration: fence.Generation,
		})
	} else {
		changed, err = queries(tx).SetJobTerminal(ctx, jobdb.SetJobTerminalParams{
			ID: id, Status: string(status), FenceGeneration: fence.Generation,
		})
	}
	if err != nil {
		return err
	}
	if changed == 0 {
		current, e := r.get(ctx, tx, id)
		if e != nil {
			return e
		}
		if current.Status != status || (fence.Generation > 0 && current.Attempts != int(fence.Generation)) {
			return jobs.ErrConflict
		}
	}
	return nil
}

func (r *Repository) CompleteTx(ctx context.Context, tx Tx, id string, fence jobs.Fence) error {
	if err := r.lockRiverFence(ctx, tx, id, fence, false); err != nil {
		return err
	}
	if err := r.setTerminalTx(ctx, tx, id, fence, jobs.StatusSucceeded, nil); err != nil {
		return err
	}
	if completion := completionFromContext(ctx); completion != nil && !completion.done.Load() {
		pgxTx, ok := tx.(pgx.Tx)
		if !ok {
			return errors.New("River completion requires a native pgx transaction")
		}
		if err := completion.complete(ctx, pgxTx); err != nil {
			return err
		}
	}
	return nil
}
func (r *Repository) FailTx(ctx context.Context, tx Tx, id string, fence jobs.Fence, problem []byte) error {
	if err := r.lockRiverFence(ctx, tx, id, fence, true); err != nil {
		return err
	}
	if err := r.setTerminalTx(ctx, tx, id, fence, jobs.StatusFailed, problem); err != nil {
		return err
	}
	client, err := r.riverClient()
	if err != nil {
		return err
	}
	riverID, err := r.RiverJobIDTx(ctx, tx, id)
	if err != nil {
		return err
	}
	pgxTx, nativeErr := nativeTransaction(tx)
	if nativeErr != nil {
		return nativeErr
	}
	_, err = client.JobCancelTx(ctx, pgxTx, riverID)
	if errors.Is(err, rivertype.ErrNotFound) {
		err = nil
	}
	if err == nil {
		if completion := completionFromContext(ctx); completion != nil {
			completion.done.Store(true)
		}
	}
	return err
}
func (r *Repository) Complete(ctx context.Context, id string, f jobs.Fence) error {
	return r.inTx(ctx, func(tx Tx) error { return r.CompleteTx(ctx, tx, id, f) })
}
func (r *Repository) Fail(ctx context.Context, id string, f jobs.Fence, p []byte) error {
	return r.inTx(ctx, func(tx Tx) error { return r.FailTx(ctx, tx, id, f, p) })
}

func (r *Repository) RequeueAfterFailure(ctx context.Context, id string, attempt int, problem []byte) error {
	if !json.Valid(problem) {
		return errors.New("retry evidence must be valid JSON")
	}
	if attempt < 1 {
		return errors.New("River attempt must be positive")
	}
	// Requeue must lock and validate the bound River row in the same
	// transaction as the product update. A database adapter without transaction
	// authority cannot safely perform the transition, so fail closed as a
	// conflict without mutating anything.
	if _, ok := r.db.(beginner); !ok {
		return jobs.ErrConflict
	}
	err := r.inTx(ctx, func(tx Tx) error {
		fence := jobs.Fence{Generation: int64(attempt)}
		if completion := completionFromContext(ctx); completion != nil && completion.riverJobID > 0 {
			fence.Owner = completion.owner
			if completion.generation != int64(attempt) {
				return jobs.ErrConflict
			}
		}
		if fence.Owner != "" {
			if err := r.lockRiverFence(ctx, tx, id, fence, false); err != nil {
				return err
			}
		} else if err := r.lockRiverMarkFence(ctx, tx, id, attempt); err != nil {
			return err
		}
		rows, err := queries(tx).RequeueJobAfterFailure(ctx, jobdb.RequeueJobAfterFailureParams{ID: id, Attempt: int32(attempt), Problem: problem})
		if err != nil {
			return err
		}
		if rows == 0 {
			return jobs.ErrConflict
		}
		return nil
	})
	return err
}

func (r *Repository) Cancel(ctx context.Context, id string) error {
	return r.inTx(ctx, func(tx Tx) error { return r.CancelTx(ctx, tx, id) })
}
func (r *Repository) CancelTx(ctx context.Context, tx Tx, id string) error {
	client, err := r.riverClient()
	if err != nil {
		return err
	}
	riverID, err := r.RiverJobIDTx(ctx, tx, id)
	if err != nil {
		return err
	}
	current, err := r.get(ctx, tx, id)
	if err != nil {
		return err
	}
	if current.Status != jobs.StatusQueued && current.Status != jobs.StatusRunning {
		return jobs.ErrConflict
	}
	pgxTx, nativeErr := nativeTransaction(tx)
	if nativeErr != nil {
		return nativeErr
	}
	if _, err := client.JobCancelTx(ctx, pgxTx, riverID); err != nil && !errors.Is(err, rivertype.ErrNotFound) {
		return err
	}
	if err := r.setTerminalTx(ctx, tx, id, jobs.Fence{}, jobs.StatusCancelled, nil); err != nil {
		return err
	}
	if completion := completionFromContext(ctx); completion != nil {
		completion.done.Store(true)
	}
	return nil
}
func (r *Repository) CancelClaimed(ctx context.Context, id string, f jobs.Fence) error {
	return r.inTx(ctx, func(tx Tx) error { return r.CancelClaimedTx(ctx, tx, id, f) })
}
func (r *Repository) CancelClaimedTx(ctx context.Context, tx Tx, id string, f jobs.Fence) error {
	if tx == nil || strings.TrimSpace(id) == "" || f.Generation <= 0 || strings.TrimSpace(f.Owner) != f.Owner || f.Owner == "" {
		return errors.New("claimed cancellation fence is invalid")
	}
	riverID, err := r.RiverJobIDTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := r.lockRiverFence(ctx, tx, id, f, false); err != nil {
		return err
	}
	if err := r.setTerminalTx(ctx, tx, id, f, jobs.StatusCancelled, nil); err != nil {
		return err
	}
	client, err := r.riverClient()
	if err != nil {
		return err
	}
	pgxTx, nativeErr := nativeTransaction(tx)
	if nativeErr != nil {
		return nativeErr
	}
	if _, err := client.JobCancelTx(ctx, pgxTx, riverID); err != nil && !errors.Is(err, rivertype.ErrNotFound) {
		return err
	}
	if completion := completionFromContext(ctx); completion != nil {
		completion.done.Store(true)
	}
	return nil
}
func (r *Repository) SupersedeTx(ctx context.Context, tx Tx, ids []string) error {
	for _, id := range ids {
		if err := r.CancelTx(ctx, tx, id); err != nil && !errors.Is(err, jobs.ErrConflict) {
			return err
		}
	}
	return nil
}

func (r *Repository) inTx(ctx context.Context, fn func(Tx) error) error {
	b, ok := r.db.(beginner)
	if !ok {
		return errors.New("PostgreSQL jobs transaction authority is required")
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) AppendEvent(ctx context.Context, kind, id, event string, data []byte) (jobs.Event, error) {
	var out jobs.Event
	err := r.inTx(ctx, func(tx Tx) error { var e error; out, e = r.AppendEventTx(ctx, tx, kind, id, event, data); return e })
	return out, err
}
func (r *Repository) AppendEventTx(ctx context.Context, tx Tx, kind, id, event string, data []byte) (jobs.Event, error) {
	canonical, err := canonicalJSON(data)
	if err != nil {
		return jobs.Event{}, err
	}
	keySum := sha256.Sum256(append([]byte(event+"\x00"), canonical...))
	return r.appendEventTx(ctx, tx, jobs.EventInput{Key: "sha256:" + hex.EncodeToString(keySum[:]), ResourceKind: kind, ResourceID: id, EventType: event, Data: canonical})
}
func (r *Repository) appendEventTx(ctx context.Context, tx Tx, input jobs.EventInput) (jobs.Event, error) {
	if input.Key == "" || input.ResourceKind == "" || input.ResourceID == "" || input.EventType == "" || !json.Valid(input.Data) {
		return jobs.Event{}, errors.New("invalid async event")
	}
	canonical, err := canonicalJSON(input.Data)
	if err != nil {
		return jobs.Event{}, err
	}
	if err := queries(tx).EnsureEventSequence(ctx, jobdb.EnsureEventSequenceParams{
		ResourceKind: input.ResourceKind, ResourceID: input.ResourceID,
	}); err != nil {
		return jobs.Event{}, err
	}
	if _, err := queries(tx).LockEventSequence(ctx, jobdb.LockEventSequenceParams{
		ResourceKind: input.ResourceKind, ResourceID: input.ResourceID,
	}); err != nil {
		return jobs.Event{}, err
	}
	existing, err := queries(tx).GetEventByKey(ctx, jobdb.GetEventByKeyParams{
		ResourceKind: input.ResourceKind, ResourceID: input.ResourceID, EventKey: input.Key,
	})
	if err == nil {
		persisted, canonicalErr := canonicalJSON([]byte(existing.Data))
		if canonicalErr != nil {
			return jobs.Event{}, canonicalErr
		}
		if existing.EventType != input.EventType || !bytes.Equal(persisted, canonical) {
			return jobs.Event{}, jobs.ErrConflict
		}
		return jobs.Event{
			ID: existing.EventID, ResourceKind: input.ResourceKind, ResourceID: input.ResourceID,
			EventType: existing.EventType, Data: canonical,
			CreatedAt: existing.CreatedAt.UTC().Format(time.RFC3339Nano),
		}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return jobs.Event{}, err
	}
	next, err := queries(tx).NextEventID(ctx, jobdb.NextEventIDParams{
		ResourceKind: input.ResourceKind, ResourceID: input.ResourceID,
	})
	if err != nil {
		return jobs.Event{}, err
	}
	row, err := queries(tx).InsertEvent(ctx, jobdb.InsertEventParams{
		ResourceKind: input.ResourceKind, ResourceID: input.ResourceID,
		EventID: next, EventType: input.EventType, EventKey: input.Key, Data: canonical,
	})
	if err != nil {
		return jobs.Event{}, jobs.ErrConflict
	}
	return jobs.Event{ID: row.EventID, ResourceKind: input.ResourceKind, ResourceID: input.ResourceID, EventType: input.EventType, Data: canonical, CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339Nano)}, nil
}
func (r *Repository) ListEvents(ctx context.Context, kind, id string, after int64, limit int) ([]jobs.Event, error) {
	if limit < 1 || limit > maxEventPage {
		return nil, errors.New("event page limit is outside bound")
	}
	rows, err := queries(r.db).ListEvents(ctx, jobdb.ListEventsParams{
		ResourceKind: kind, ResourceID: id, AfterID: after, PageLimit: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]jobs.Event, 0, len(rows))
	for _, row := range rows {
		out = append(out, jobs.Event{
			ID: row.EventID, ResourceKind: kind, ResourceID: id, EventType: row.EventType,
			Data: []byte(row.Data), CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return out, nil
}

func (r *Repository) RecordWorkflow(ctx context.Context, tx Tx, intent jobs.WorkflowIntent) error {
	if tx == nil {
		return errors.New("workflow transaction is required")
	}
	if _, err := r.appendEventTx(ctx, tx, intent.Event); err != nil {
		return err
	}
	if intent.Job.ID != "" {
		_, err := r.EnqueueTx(ctx, tx, intent.Job)
		return err
	}
	return nil
}
func (r *Repository) CommitWorkflow(ctx context.Context, intent jobs.WorkflowIntent) error {
	return r.inTx(ctx, func(tx Tx) error { return r.RecordWorkflow(ctx, tx, intent) })
}

// AcquirePartition serializes a partition across nodes and rejects a later
// product job while an earlier live row remains. River still owns the row
// claim; this is only LeapView's product admission/fairness adapter.
func (r *Repository) AcquirePartition(ctx context.Context, job jobs.Job) (func(), bool, error) {
	pool, err := r.NativePool()
	if err != nil {
		return nil, false, err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	release := func() {
		_ = queries(conn).ReleasePartitionAdvisoryLock(context.Background(), job.PartitionKey)
		conn.Release()
	}
	locked, err := queries(conn).TryPartitionAdvisoryLock(ctx, job.PartitionKey)
	if err != nil || !locked {
		conn.Release()
		return nil, false, err
	}
	head, err := queries(conn).PartitionIsHead(ctx, jobdb.PartitionIsHeadParams{
		PartitionKey: job.PartitionKey, ID: job.ID,
	})
	if err != nil {
		release()
		return nil, false, err
	}
	if !head {
		release()
		return nil, false, nil
	}
	return release, true, nil
}

func canonicalGroups(groups []string) []string {
	out := append([]string(nil), groups...)
	sort.Strings(out)
	return out
}

func canonicalJSON(raw []byte) ([]byte, error) {
	var value any
	if err := strictjson.DecodeWithOptions(raw, &value, strictjson.Options{MaxBytes: maxPayloadBytes, AllowUnknownFields: true, DuplicateKeys: strictjson.CaseSensitiveKeys}); err != nil {
		return nil, err
	}
	// Decode a second time with UseNumber so canonicalization never rounds
	// integers through float64 (which would collapse distinct request/event
	// digests such as 9007199254740992 and 9007199254740993).
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func nativeTransaction(tx Tx) (pgx.Tx, error) {
	pgxTx, ok := tx.(pgx.Tx)
	if !ok || pgxTx == nil {
		return nil, errors.New("River requires a native pgx transaction")
	}
	return pgxTx, nil
}
