// Package postgres implements the durable jobs capability on PostgreSQL.
//
// The adapter intentionally accepts pgx's native DBTX shape. It does not
// expose a database/sql compatibility layer; callers that need a transaction
// pass the pgx transaction directly to RecordWorkflow.
package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"

	jobpolicy "github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is implemented by pgx.Conn, pgx.Tx, pgxpool.Pool and pgxpool.Conn.
// Keeping this interface local lets capability tests use a real pgx pool or a
// narrow recording implementation without introducing a backend abstraction.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

type Repository struct{ db DBTX }

var _ jobs.Repository = (*Repository)(nil)

// MaxAttempts is the bounded retry ceiling for jobs created through this
// adapter. EnqueueInput predates retry policy, so one explicit default is
// persisted rather than permitting unbounded lease reclaim.
const MaxAttempts int64 = 3

// MaxRetryDelay bounds persisted backoff so malformed workers cannot hide a
// job indefinitely in the queue.
const MaxRetryDelay = 24 * time.Hour

func NewRepository(db DBTX) *Repository { return &Repository{db: db} }

// New is a concise constructor alias for callers that keep one repository per
// capability package.
func New(db DBTX) *Repository { return NewRepository(db) }

const jobColumns = `id, kind, workload_class, principal_id, group_ids,
 resource_kind, resource_id, estimated_memory_bytes, payload, status,
 attempt_count, lease_owner, lease_expires_at, lease_generation,
 created_at, started_at, finished_at, error`

const jobColumnsQualified = `j.id, j.kind, j.workload_class, j.principal_id, j.group_ids,
 j.resource_kind, j.resource_id, j.estimated_memory_bytes, j.payload, j.status,
 j.attempt_count, j.lease_owner, j.lease_expires_at, j.lease_generation,
 j.created_at, j.started_at, j.finished_at, j.error`

func (r *Repository) Enqueue(ctx context.Context, input jobs.EnqueueInput) (jobs.Job, error) {
	groups, actorErr := jobs.CanonicalActor(input.PrincipalID, input.GroupIDs)
	if !validInput(input, actorErr) {
		return jobs.Job{}, fmt.Errorf("invalid async job")
	}
	digest := jobDigest(input, groups)
	_, err := r.db.Exec(ctx, `
INSERT INTO jobs.job (id, kind, workload_class, principal_id, group_ids,
 resource_kind, resource_id, estimated_memory_bytes, payload, request_digest)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10)
ON CONFLICT (id) DO NOTHING`, input.ID, input.Kind, input.WorkloadClass, input.PrincipalID,
		groups, input.ResourceKind, input.ResourceID, input.EstimatedMemoryBytes, input.Payload, digest)
	if err != nil {
		return jobs.Job{}, err
	}
	var storedDigest string
	err = r.db.QueryRow(ctx, `SELECT request_digest FROM jobs.job WHERE id = $1`, input.ID).Scan(&storedDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Job{}, jobs.ErrNotFound
	}
	if err != nil {
		return jobs.Job{}, err
	}
	if storedDigest != digest {
		return jobs.Job{}, jobs.ErrConflict
	}
	return r.Get(ctx, input.ID)
}

func validInput(input jobs.EnqueueInput, actorErr error) bool {
	return canonicalLiteral(input.ID, 256) && canonicalLiteral(input.Kind, 128) &&
		(input.WorkloadClass == jobpolicy.WorkloadClassBackground || input.WorkloadClass == jobpolicy.WorkloadClassControl) &&
		canonicalLiteral(input.ResourceKind, 128) && canonicalLiteral(input.ResourceID, 256) &&
		input.EstimatedMemoryBytes > 0 && len(input.Payload) <= 1<<20 && json.Valid(input.Payload) && actorErr == nil
}

func jobDigest(input jobs.EnqueueInput, groups []string) string {
	groupJSON, _ := json.Marshal(groups)
	sum := sha256.Sum256([]byte(input.Kind + "\x00" + input.WorkloadClass + "\x00" + input.PrincipalID + "\x00" + string(groupJSON) + "\x00" + input.ResourceKind + "\x00" + input.ResourceID + "\x00" + fmt.Sprint(input.EstimatedMemoryBytes) + "\x00" + string(input.Payload)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (r *Repository) Get(ctx context.Context, id string) (jobs.Job, error) {
	if !canonicalLiteral(id, 256) {
		return jobs.Job{}, fmt.Errorf("invalid async job id")
	}
	row := r.db.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs.job WHERE id = $1`, id)
	job, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Job{}, jobs.ErrNotFound
	}
	return job, err
}

func (r *Repository) Candidates(ctx context.Context, workloadClass string, limit int) ([]jobs.Job, error) {
	if !validClass(workloadClass) || limit < 1 || limit > 200 {
		return nil, fmt.Errorf("workload class and candidate limit are required")
	}
	rows, err := r.db.Query(ctx, `
SELECT `+jobColumns+` FROM jobs.job j
WHERE j.workload_class = $1 AND j.available_at <= clock_timestamp()
  AND (j.status = 'queued' OR (j.status = 'running' AND j.lease_expires_at <= clock_timestamp()))
  AND j.id = (
    SELECT h.id FROM jobs.job h
    WHERE h.principal_id = j.principal_id AND h.workload_class = $1
      AND h.available_at <= clock_timestamp()
      AND (h.status = 'queued' OR (h.status = 'running' AND h.lease_expires_at <= clock_timestamp()))
    ORDER BY h.created_at, h.id LIMIT 1
  )
ORDER BY j.created_at, j.id LIMIT $2`, workloadClass, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]jobs.Job, 0, limit)
	for rows.Next() {
		job, scanErr := scanJob(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, job)
	}
	return result, rows.Err()
}

// ClaimByID uses one locking selection and one UPDATE ... RETURNING. SKIP
// LOCKED makes concurrent workers fail closed instead of waiting behind a
// lease owner that is still processing another claim.
func (r *Repository) ClaimByID(ctx context.Context, id, workloadClass, owner string, lease time.Duration) (jobs.Job, bool, error) {
	if !canonicalLiteral(id, 256) || !validClass(workloadClass) || !canonicalLiteral(owner, 256) || lease <= 0 {
		return jobs.Job{}, false, fmt.Errorf("job id, workload class, worker owner, and positive lease are required")
	}
	row := r.db.QueryRow(ctx, `
WITH candidate AS (
    SELECT id, status, attempt_count, lease_generation FROM jobs.job
    WHERE id = $1 AND workload_class = $2 AND available_at <= clock_timestamp()
      AND (status = 'queued' OR (status = 'running' AND lease_expires_at <= clock_timestamp()))
    ORDER BY created_at, id
    FOR UPDATE SKIP LOCKED
), expired_attempt AS (
    UPDATE jobs.attempt a SET finished_at = clock_timestamp(), outcome = 'expired'
    FROM candidate c WHERE c.status = 'running' AND a.job_id = c.id AND a.attempt_number = c.attempt_count AND a.fencing_generation = c.lease_generation AND a.outcome = 'running'
), exhausted AS (
    UPDATE jobs.job j SET status = 'failed', finished_at = clock_timestamp(), lease_owner = '', lease_expires_at = NULL,
        error = '{"code":"MAX_ATTEMPTS_EXCEEDED"}'::jsonb
    FROM candidate c WHERE j.id = c.id AND j.attempt_count >= j.max_attempts
    RETURNING j.id
), claimed AS (
    UPDATE jobs.job j
    SET status = 'running', started_at = COALESCE(j.started_at, clock_timestamp()),
        lease_owner = $3,
        lease_expires_at = clock_timestamp() + ($4::bigint * interval '1 microsecond'),
        attempt_count = j.attempt_count + 1,
        lease_generation = j.lease_generation + 1
    FROM candidate c WHERE j.id = c.id AND j.attempt_count < j.max_attempts
    RETURNING `+jobColumnsQualified+`
), recorded AS (
    INSERT INTO jobs.attempt (job_id, attempt_number, fencing_generation, owner, lease_expires_at)
    SELECT id, attempt_count, lease_generation, lease_owner, lease_expires_at FROM claimed
)
SELECT `+jobColumns+` FROM claimed`, id, workloadClass, owner, lease.Microseconds())
	job, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Job{}, false, nil
	}
	return job, err == nil, err
}

func (r *Repository) Renew(ctx context.Context, id string, fence jobs.Fence, lease time.Duration) error {
	if !validFence(id, fence) || lease <= 0 {
		return fmt.Errorf("invalid async job fence")
	}
	count, err := r.mutate(ctx, `
WITH changed AS (
    UPDATE jobs.job j
    SET lease_expires_at = clock_timestamp() + ($3::bigint * interval '1 microsecond')
    WHERE j.id = $1 AND j.status = 'running' AND j.lease_owner = $2
      AND j.lease_generation = $4 AND j.lease_expires_at > clock_timestamp()
      AND EXISTS (SELECT 1 FROM jobs.attempt a WHERE a.job_id = j.id AND a.attempt_number = j.attempt_count AND a.fencing_generation = j.lease_generation)
    RETURNING j.id, j.attempt_count, j.lease_generation, j.lease_expires_at
), attempt_changed AS (
    UPDATE jobs.attempt a SET lease_expires_at = c.lease_expires_at
    FROM changed c WHERE a.job_id = c.id AND a.attempt_number = c.attempt_count AND a.fencing_generation = c.lease_generation
    RETURNING a.job_id
)
SELECT count(*) FROM attempt_changed`, id, fence.Owner, lease.Microseconds(), fence.Generation)
	return requireChanged(count, err)
}

func (r *Repository) Complete(ctx context.Context, id string, fence jobs.Fence) error {
	return r.terminal(ctx, id, fence, "succeeded", nil)
}

func (r *Repository) Fail(ctx context.Context, id string, fence jobs.Fence, problem []byte) error {
	if len(problem) > 65536 || !json.Valid(problem) {
		return fmt.Errorf("invalid async job failure JSON")
	}
	return r.terminal(ctx, id, fence, "failed", problem)
}

func (r *Repository) terminal(ctx context.Context, id string, fence jobs.Fence, outcome string, problem []byte) error {
	if !validFence(id, fence) {
		return fmt.Errorf("invalid async job fence")
	}
	errorJSON := []byte(`{}`)
	if problem != nil {
		errorJSON = problem
	}
	count, err := r.mutate(ctx, `
WITH changed AS (
    UPDATE jobs.job j
    SET status = $3, finished_at = clock_timestamp(), lease_owner = '', lease_expires_at = NULL, error = $5::jsonb
    WHERE j.id = $1 AND j.status = 'running' AND j.lease_owner = $2
      AND j.lease_generation = $4 AND j.lease_expires_at > clock_timestamp()
      AND EXISTS (SELECT 1 FROM jobs.attempt a WHERE a.job_id = j.id AND a.attempt_number = j.attempt_count AND a.fencing_generation = j.lease_generation)
    RETURNING j.id, j.attempt_count, j.lease_generation
), attempt_changed AS (
    UPDATE jobs.attempt a SET finished_at = clock_timestamp(), outcome = $3, error = $5::jsonb
    FROM changed c WHERE a.job_id = c.id AND a.attempt_number = c.attempt_count AND a.fencing_generation = c.lease_generation
    RETURNING a.job_id
)
SELECT count(*) FROM attempt_changed`, id, fence.Owner, outcome, fence.Generation, errorJSON)
	return requireChanged(count, err)
}

// Retry requeues a claimed job with an explicitly persisted backoff. It is an
// extension of the public repository contract used by retrying workers; the
// existing Fail method remains terminal for callers that do not opt in.
func (r *Repository) Retry(ctx context.Context, id string, fence jobs.Fence, delay time.Duration, problem []byte) error {
	if !validFence(id, fence) || delay < 0 || delay > MaxRetryDelay || len(problem) > 65536 || !json.Valid(problem) {
		return fmt.Errorf("invalid async job retry")
	}
	count, err := r.mutate(ctx, `
WITH changed AS (
    UPDATE jobs.job j
    SET status = 'queued', available_at = clock_timestamp() + ($4::bigint * interval '1 microsecond'), lease_owner = '', lease_expires_at = NULL, error = $5::jsonb
    WHERE j.id = $1 AND j.status = 'running' AND j.lease_owner = $2 AND j.lease_generation = $3 AND j.lease_expires_at > clock_timestamp() AND j.attempt_count < j.max_attempts
      AND EXISTS (SELECT 1 FROM jobs.attempt a WHERE a.job_id = j.id AND a.attempt_number = j.attempt_count AND a.fencing_generation = j.lease_generation)
    RETURNING j.id, j.attempt_count, j.lease_generation
), attempt_changed AS (
    UPDATE jobs.attempt a SET finished_at = clock_timestamp(), outcome = 'retrying', retry_at = clock_timestamp() + ($4::bigint * interval '1 microsecond'), error = $5::jsonb
    FROM changed c WHERE a.job_id = c.id AND a.attempt_number = c.attempt_count AND a.fencing_generation = c.lease_generation
    RETURNING a.job_id
)
	SELECT count(*) FROM attempt_changed`, id, fence.Owner, fence.Generation, delay.Microseconds(), problem)
	return requireChanged(count, err)
}

func (r *Repository) Cancel(ctx context.Context, id string) error {
	if !canonicalLiteral(id, 256) {
		return fmt.Errorf("invalid async job id")
	}
	result, err := r.db.Exec(ctx, `UPDATE jobs.job SET status = 'cancelled', finished_at = clock_timestamp(), lease_owner = '', lease_expires_at = NULL WHERE id = $1 AND status = 'queued'`, id)
	return requireChanged(result.RowsAffected(), err)
}

func (r *Repository) CancelClaimed(ctx context.Context, id string, fence jobs.Fence) error {
	if !validFence(id, fence) {
		return fmt.Errorf("invalid async job fence")
	}
	count, err := r.mutate(ctx, `
WITH changed AS (
    UPDATE jobs.job j SET status = 'cancelled', finished_at = clock_timestamp(), lease_owner = '', lease_expires_at = NULL
    WHERE j.id = $1 AND j.status = 'running' AND j.lease_owner = $2 AND j.lease_generation = $3 AND j.lease_expires_at > clock_timestamp()
      AND EXISTS (SELECT 1 FROM jobs.attempt a WHERE a.job_id = j.id AND a.attempt_number = j.attempt_count AND a.fencing_generation = j.lease_generation)
    RETURNING j.id, j.attempt_count, j.lease_generation
), attempt_changed AS (
    UPDATE jobs.attempt a SET finished_at = clock_timestamp(), outcome = 'cancelled'
    FROM changed c WHERE a.job_id = c.id AND a.attempt_number = c.attempt_count AND a.fencing_generation = c.lease_generation
    RETURNING a.job_id
)
SELECT count(*) FROM attempt_changed`, id, fence.Owner, fence.Generation)
	return requireChanged(count, err)
}

func (r *Repository) AppendEvent(ctx context.Context, resourceKind, resourceID, eventType string, data []byte) (jobs.Event, error) {
	return r.appendEvent(ctx, r.db, resourceKind, resourceID, eventType, data, "")
}

func (r *Repository) ListEvents(ctx context.Context, resourceKind, resourceID string, after int64, limit int) ([]jobs.Event, error) {
	if !canonicalLiteral(resourceKind, 128) || !canonicalLiteral(resourceID, 256) || after < 0 || limit < 1 || limit > 200 {
		return nil, fmt.Errorf("event limit must be between 1 and 200")
	}
	rows, err := r.db.Query(ctx, `SELECT event_id, resource_kind, resource_id, event_type, data, created_at FROM jobs.event WHERE resource_kind = $1 AND resource_id = $2 AND event_id > $3 ORDER BY event_id LIMIT $4`, resourceKind, resourceID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]jobs.Event, 0, limit)
	for rows.Next() {
		event, scanErr := scanEvent(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

// RecordWorkflow atomically appends the keyed event and optional follow-up
// job using the caller's pgx transaction. The transaction remains owned by
// the capability making the domain transition.
func (r *Repository) RecordWorkflow(ctx context.Context, tx DBTX, intent jobs.WorkflowIntent) error {
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
	groups, actorErr := jobs.CanonicalActor(intent.Job.PrincipalID, intent.Job.GroupIDs)
	if !validInput(intent.Job, actorErr) {
		return fmt.Errorf("invalid async job")
	}
	digest := jobDigest(intent.Job, groups)
	_, err := tx.Exec(ctx, `INSERT INTO jobs.job (id, kind, workload_class, principal_id, group_ids, resource_kind, resource_id, estimated_memory_bytes, payload, request_digest) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10) ON CONFLICT (id) DO NOTHING`, intent.Job.ID, intent.Job.Kind, intent.Job.WorkloadClass, intent.Job.PrincipalID, groups, intent.Job.ResourceKind, intent.Job.ResourceID, intent.Job.EstimatedMemoryBytes, intent.Job.Payload, digest)
	if err != nil {
		return err
	}
	var stored string
	if err := tx.QueryRow(ctx, `SELECT request_digest FROM jobs.job WHERE id = $1`, intent.Job.ID).Scan(&stored); err != nil {
		return err
	}
	if stored != digest {
		return jobs.ErrConflict
	}
	return nil
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

func (r *Repository) mutate(ctx context.Context, sql string, args ...any) (int64, error) {
	var count int64
	err := r.db.QueryRow(ctx, sql, args...).Scan(&count)
	return count, err
}

func (r *Repository) appendEvent(ctx context.Context, db DBTX, kind, id, eventType string, data []byte, key string) (jobs.Event, error) {
	if !canonicalLiteral(kind, 128) || !canonicalLiteral(id, 256) || !canonicalLiteral(eventType, 128) || len(data) > 1<<20 || !json.Valid(data) || len(key) > 256 || (key != "" && !canonicalLiteral(key, 256)) {
		return jobs.Event{}, fmt.Errorf("invalid async event")
	}
	// Workflow keys are serialized per resource in a transaction. This closes
	// the race where two replays both allocate an event-sequence number before
	// one loses the partial unique-key insert.
	if key != "" {
		// pgx.Tx itself exposes Begin for pseudo-nested savepoints. Do not
		// recurse through those when the caller already owns a transaction.
		if b, ok := db.(beginner); ok {
			if _, alreadyTx := db.(pgx.Tx); !alreadyTx {
				tx, beginErr := b.Begin(ctx)
				if beginErr != nil {
					return jobs.Event{}, beginErr
				}
				event, appendErr := r.appendEvent(ctx, tx, kind, id, eventType, data, key)
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
		if _, lockErr := db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || '|' || $2, 0))`, kind, id); lockErr != nil {
			return jobs.Event{}, lockErr
		}
	}
	// Avoid consuming an event-sequence number on the common idempotent
	// workflow replay path. A concurrent replay can still race the allocator;
	// the conflict path below converges on the already persisted row.
	if key != "" {
		var existing jobs.Event
		var existingData []byte
		var existingCreated time.Time
		lookupErr := db.QueryRow(ctx, `SELECT event_id, resource_kind, resource_id, event_type, data, created_at FROM jobs.event WHERE resource_kind = $1 AND resource_id = $2 AND event_key = $3`, kind, id, key).Scan(&existing.ID, &existing.ResourceKind, &existing.ResourceID, &existing.EventType, &existingData, &existingCreated)
		if lookupErr == nil {
			if existing.EventType != eventType || !jsonEquivalent(existingData, data) {
				return jobs.Event{}, jobs.ErrConflict
			}
			existing.Data = append([]byte(nil), existingData...)
			existing.CreatedAt = formatTimestamp(existingCreated)
			return existing, nil
		}
		if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return jobs.Event{}, lookupErr
		}
	}
	query := `
WITH allocated AS (
    INSERT INTO jobs.event_sequence (resource_kind, resource_id, next_event_id) VALUES ($1, $2, 2)
    ON CONFLICT (resource_kind, resource_id) DO UPDATE SET next_event_id = jobs.event_sequence.next_event_id + 1
    RETURNING next_event_id - 1 AS event_id
)
INSERT INTO jobs.event (resource_kind, resource_id, event_id, event_type, event_key, data)
SELECT $1, $2, event_id, $3, $4, $5::jsonb FROM allocated
ON CONFLICT (resource_kind, resource_id, event_key) WHERE event_key <> '' DO NOTHING
RETURNING event_id, resource_kind, resource_id, event_type, data, created_at`
	var event jobs.Event
	var payload []byte
	var created time.Time
	err := db.QueryRow(ctx, query, kind, id, eventType, key, data).Scan(&event.ID, &event.ResourceKind, &event.ResourceID, &event.EventType, &payload, &created)
	if errors.Is(err, pgx.ErrNoRows) && key != "" {
		err = db.QueryRow(ctx, `SELECT event_id, resource_kind, resource_id, event_type, data, created_at FROM jobs.event WHERE resource_kind = $1 AND resource_id = $2 AND event_key = $3`, kind, id, key).Scan(&event.ID, &event.ResourceKind, &event.ResourceID, &event.EventType, &payload, &created)
		if err == nil && (event.EventType != eventType || !jsonEquivalent(payload, data)) {
			return jobs.Event{}, jobs.ErrConflict
		}
	}
	if err != nil {
		return jobs.Event{}, err
	}
	event.Data = append([]byte(nil), payload...)
	event.CreatedAt = formatTimestamp(created)
	return event, nil
}

func jsonEquivalent(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func scanJob(row interface{ Scan(...any) error }) (jobs.Job, error) {
	var job jobs.Job
	var groups []string
	var payload, errorJSON []byte
	var leaseExpires, started, finished *time.Time
	var created time.Time
	var status string
	var attempts int64
	if err := row.Scan(&job.ID, &job.Kind, &job.WorkloadClass, &job.PrincipalID, &groups, &job.ResourceKind, &job.ResourceID, &job.EstimatedMemoryBytes, &payload, &status, &attempts, &job.LeaseOwner, &leaseExpires, &job.LeaseGeneration, &created, &started, &finished, &errorJSON); err != nil {
		return jobs.Job{}, err
	}
	canonicalGroups, err := jobs.CanonicalGroups(groups)
	if err != nil || !equalStrings(groups, canonicalGroups) {
		return jobs.Job{}, fmt.Errorf("invalid persisted async job groups")
	}
	job.GroupIDs = canonicalGroups
	job.Payload = append([]byte(nil), payload...)
	job.Status = jobs.Status(status)
	job.Attempts = int(attempts)
	job.LeaseExpiresAt = formatOptionalTimestamp(leaseExpires)
	job.CreatedAt = formatTimestamp(created)
	job.StartedAt = formatOptionalTimestamp(started)
	job.FinishedAt = formatOptionalTimestamp(finished)
	job.ErrorJSON = string(errorJSON)
	return job, nil
}

func scanEvent(row interface{ Scan(...any) error }) (jobs.Event, error) {
	var event jobs.Event
	var payload []byte
	var created time.Time
	if err := row.Scan(&event.ID, &event.ResourceKind, &event.ResourceID, &event.EventType, &payload, &created); err != nil {
		return jobs.Event{}, err
	}
	event.Data = append([]byte(nil), payload...)
	event.CreatedAt = formatTimestamp(created)
	return event, nil
}

func validFence(id string, fence jobs.Fence) bool {
	return canonicalLiteral(id, 256) && canonicalLiteral(fence.Owner, 256) && fence.Generation >= 0
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
	return value.UTC().Format("2006-01-02 15:04:05")
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
