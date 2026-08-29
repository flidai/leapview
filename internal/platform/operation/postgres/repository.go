// Package postgres implements the operation/idempotency capability on native
// PostgreSQL pgx surfaces. It intentionally does not provide a database/sql
// adapter or hide transaction ownership from callers.
package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is implemented by pgx.Conn, pgx.Tx, pgxpool.Pool and pgxpool.Conn.
// Keeping this native shape lets a caller compose operation state and its
// business mutation on one PostgreSQL transaction.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// Tx is the subset required by caller-owned transaction methods.
type Tx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

var (
	ErrConflict        = errors.New("operation idempotency conflict")
	ErrNotFound        = errors.New("operation idempotency record not found")
	ErrBusy            = errors.New("operation idempotency record is owned by another worker")
	ErrStaleFence      = errors.New("operation owner fencing generation is stale")
	ErrLeaseExpired    = errors.New("operation owner lease expired")
	ErrAlreadyTerminal = errors.New("operation idempotency record is already terminal")
	ErrInvalid         = errors.New("invalid operation idempotency input")
)

const (
	maxLeaseDuration     = 24 * time.Hour
	maxRetentionDuration = 365 * 24 * time.Hour
)

// State is the durable operation state machine.
type State string

const (
	StatePending       State = "pending"
	StateCompleted     State = "completed"
	StateFailed        State = "failed"
	StateIndeterminate State = "indeterminate"
)

type AcquireStatus string

const (
	StatusAcquired      AcquireStatus = "acquired"
	StatusReplay        AcquireStatus = "replay"
	StatusBusy          AcquireStatus = "busy"
	StatusIndeterminate AcquireStatus = "indeterminate"
)

// UnknownOutcome is persisted when external work may have committed but the
// response was lost. Its bytes are stable and replayed exactly.
var UnknownOutcome = json.RawMessage(`{"code":"IDEMPOTENCY_OUTCOME_UNKNOWN","detail":"The original request outcome is indeterminate and requires reconciliation evidence"}`)

// AcquireInput identifies one scoped request. Request is optional when the
// caller already computed RequestDigest; when present, its canonical SHA-256
// digest is checked against RequestDigest.
type AcquireInput struct {
	Scope           string
	OperationType   string
	IdempotencyKey  string
	Request         []byte
	RequestDigest   string
	OwnerID         string
	Lease           time.Duration
	Retention       time.Duration
	AttemptID       string
	AttemptIdentity string
}

// Lease is the owner token. Every terminal transition must supply the exact
// owner and fencing generation returned by Acquire.
type Lease struct {
	Scope             string
	IdempotencyKey    string
	OperationID       string
	OwnerID           string
	FencingGeneration int64
	LeaseExpiresAt    time.Time
	AttemptID         string
	AttemptIdentity   string
}

type Operation struct {
	Scope              string
	OperationType      string
	IdempotencyKey     string
	RequestDigest      string
	OperationID        string
	State              State
	OwnerID            string
	LeaseExpiresAt     time.Time
	FencingGeneration  int64
	Outcome            json.RawMessage
	AttemptID          string
	AttemptIdentity    string
	AttemptEvidence    json.RawMessage
	ResolutionEvidence json.RawMessage
	CreatedAt          time.Time
	UpdatedAt          time.Time
	TerminalAt         time.Time
	ExpiresAt          time.Time
}

type AcquireResult struct {
	Status    AcquireStatus
	Operation Operation
	Lease     Lease
	Replay    bool
}

// BeginAttemptInput creates explicit cross-store attempt identity/evidence.
type BeginAttemptInput struct {
	Lease           Lease
	AttemptID       string
	AttemptIdentity string
}

type Attempt struct {
	AttemptID       string
	AttemptIdentity string
	Lease           Lease
}

// ReconcileAttemptInput resolves an indeterminate operation only when both the
// exact external attempt identity and exact evidence are supplied.
type ReconcileAttemptInput struct {
	Scope           string
	IdempotencyKey  string
	AttemptID       string
	AttemptIdentity string
	State           State // completed or failed
	Outcome         json.RawMessage
	Evidence        json.RawMessage
}

// Repository is safe for concurrent use; state is held in PostgreSQL.
type Repository struct {
	db        DBTX
	lease     time.Duration
	retention time.Duration
	clock     func() time.Time
}

//go:embed schema.sql
var schemaSQL string

func SchemaSQL() string { return schemaSQL }

// ApplySchema applies the capability schema on a caller-owned transaction.
func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := tx.Exec(ctx, schemaSQL)
	return err
}

func New(db DBTX) *Repository {
	return &Repository{db: db, lease: 30 * time.Second, retention: 24 * time.Hour, clock: time.Now}
}

func NewWithConfig(db DBTX, lease, retention time.Duration) *Repository {
	r := New(db)
	if lease > 0 {
		r.lease = lease
	}
	if retention > 0 {
		r.retention = retention
	}
	return r
}

// RequestDigest computes canonical SHA-256 for JSON requests. Whitespace and
// object-key ordering therefore cannot produce a second logical operation.
func RequestDigest(request []byte) (string, error) {
	canonical, err := canonicalJSON(request)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (r *Repository) Acquire(ctx context.Context, in AcquireInput) (AcquireResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil || r.db == nil {
		return AcquireResult{}, ErrInvalid
	}
	b, ok := r.db.(beginner)
	if !ok {
		return AcquireResult{}, errors.New("operation repository requires a pgx transaction-capable DB")
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return AcquireResult{}, err
	}
	result, err := r.AcquireTx(ctx, tx, in)
	if err != nil {
		_ = tx.Rollback(ctx)
		return result, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AcquireResult{}, err
	}
	return result, nil
}

// AcquireTx atomically inserts, acquires, replays, or conflicts on one key.
// The caller owns commit/rollback.
func (r *Repository) AcquireTx(ctx context.Context, tx Tx, in AcquireInput) (AcquireResult, error) {
	if r == nil || tx == nil {
		return AcquireResult{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	digest, err := normalizeInput(&in)
	if err != nil {
		return AcquireResult{}, err
	}
	now := r.now()
	lease := in.Lease
	if lease <= 0 {
		lease = r.lease
	}
	retention := in.Retention
	if retention <= 0 {
		retention = r.retention
	}
	if lease < time.Microsecond || lease > maxLeaseDuration || retention < time.Microsecond || retention > maxRetentionDuration {
		return AcquireResult{}, ErrInvalid
	}
	var inserted bool
	var op Operation
	// ON CONFLICT DO NOTHING keeps competing requests in one serializable row
	// lock path; the following SELECT FOR UPDATE determines the sole owner.
	operationID, err := newUUID()
	if err != nil {
		return AcquireResult{}, err
	}
	var insertedID string
	err = tx.QueryRow(ctx, `
INSERT INTO platform.operation
 (scope_id,operation_type,idempotency_key,request_digest,operation_id,state,owner_id,lease_expires_at,fencing_generation,outcome,attempt_id,attempt_identity,created_at,updated_at,retention_interval,expires_at)
VALUES ($1,$2,$3,$4,$5,'pending',$6,$7,1,'{}'::jsonb,$8,$9,$10,$10,$11::interval,$12)
ON CONFLICT (scope_id,idempotency_key) DO NOTHING
	RETURNING operation_id`, in.Scope, in.OperationType, in.IdempotencyKey, digest, operationID, in.OwnerID, now.Add(lease), nullableUUID(in.AttemptID), nullableText(in.AttemptIdentity), now, fmt.Sprintf("%.9f seconds", retention.Seconds()), now.Add(retention)).Scan(&insertedID)
	if err == nil {
		inserted = true
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return AcquireResult{}, err
	}
	op, err = scanOperation(tx.QueryRow(ctx, `SELECT scope_id,operation_type,idempotency_key,request_digest,operation_id,state,owner_id,lease_expires_at,fencing_generation,outcome,attempt_id,attempt_identity,attempt_evidence,resolution_evidence,created_at,updated_at,terminal_at,expires_at FROM platform.operation WHERE scope_id=$1 AND idempotency_key=$2 FOR UPDATE`, in.Scope, in.IdempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return AcquireResult{}, ErrNotFound
	}
	if err != nil {
		return AcquireResult{}, err
	}
	if op.RequestDigest != digest || op.OperationType != in.OperationType {
		return AcquireResult{}, ErrConflict
	}
	leaseToken := Lease{Scope: op.Scope, IdempotencyKey: op.IdempotencyKey, OperationID: op.OperationID, OwnerID: op.OwnerID, FencingGeneration: op.FencingGeneration, LeaseExpiresAt: op.LeaseExpiresAt}
	leaseToken.AttemptID, leaseToken.AttemptIdentity = op.AttemptID, op.AttemptIdentity
	if inserted {
		return AcquireResult{Status: StatusAcquired, Operation: op, Lease: leaseToken}, nil
	}
	if op.State != StatePending {
		status := StatusReplay
		if op.State == StateIndeterminate {
			status = StatusIndeterminate
		}
		return AcquireResult{Status: status, Operation: op, Replay: true}, nil
	}
	if !op.LeaseExpiresAt.After(now) {
		if op.AttemptID == "" {
			// No external attempt was started. It is safe to hand the operation
			// to a successor, but the fence must advance monotonically.
			var generation int64
			err := tx.QueryRow(ctx, `UPDATE platform.operation SET owner_id=$1, fencing_generation=fencing_generation+1, lease_expires_at=$2, updated_at=$3 WHERE scope_id=$4 AND idempotency_key=$5 AND state='pending' AND lease_expires_at <= $3 AND attempt_id IS NULL RETURNING fencing_generation`, in.OwnerID, now.Add(lease), now, in.Scope, in.IdempotencyKey).Scan(&generation)
			if err == nil {
				op.OwnerID = in.OwnerID
				op.FencingGeneration = generation
				op.LeaseExpiresAt = now.Add(lease)
				op.UpdatedAt = now
				token := Lease{Scope: op.Scope, IdempotencyKey: op.IdempotencyKey, OperationID: op.OperationID, OwnerID: op.OwnerID, FencingGeneration: generation, LeaseExpiresAt: op.LeaseExpiresAt}
				return AcquireResult{Status: StatusAcquired, Operation: op, Lease: token}, nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return AcquireResult{}, err
			}
		}
		// Expiry with an external attempt says nothing about external commit.
		// Fence the old owner and retain an indeterminate terminal record;
		// reconciliation evidence is required before replay can resolve it.
		_, err := tx.Exec(ctx, `UPDATE platform.operation SET state='indeterminate', outcome='{"code":"IDEMPOTENCY_OUTCOME_UNKNOWN","detail":"The original request outcome is indeterminate and requires reconciliation evidence"}'::jsonb, fencing_generation=fencing_generation+1, updated_at=$1, terminal_at=$1, expires_at=$1::timestamptz+retention_interval WHERE scope_id=$2 AND idempotency_key=$3 AND state='pending'`, now, in.Scope, in.IdempotencyKey)
		if err != nil {
			return AcquireResult{}, err
		}
		op.State = StateIndeterminate
		op.FencingGeneration++
		op.Outcome = append(json.RawMessage(nil), UnknownOutcome...)
		op.UpdatedAt = now
		op.TerminalAt = now
		return AcquireResult{Status: StatusIndeterminate, Operation: op, Replay: true}, nil
	}
	if op.OwnerID != in.OwnerID {
		return AcquireResult{Status: StatusBusy, Operation: op}, ErrBusy
	}
	return AcquireResult{Status: StatusAcquired, Operation: op, Lease: leaseToken}, nil
}

// Get returns the durable operation record without acquiring its lease.
func (r *Repository) Get(ctx context.Context, scope, idempotencyKey string) (Operation, error) {
	if r == nil || r.db == nil {
		return Operation{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if scope != strings.TrimSpace(scope) || idempotencyKey != strings.TrimSpace(idempotencyKey) || scope == "" || len(scope) > 255 || idempotencyKey == "" || len(idempotencyKey) > 512 {
		return Operation{}, ErrInvalid
	}
	op, err := scanOperation(r.db.QueryRow(ctx, `SELECT scope_id,operation_type,idempotency_key,request_digest,operation_id,state,owner_id,lease_expires_at,fencing_generation,outcome,attempt_id,attempt_identity,attempt_evidence,resolution_evidence,created_at,updated_at,terminal_at,expires_at FROM platform.operation WHERE scope_id=$1 AND idempotency_key=$2`, scope, idempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	return op, err
}

func normalizeInput(in *AcquireInput) (string, error) {
	if in.OperationType == "" {
		in.OperationType = "idempotency"
	}
	if in.Scope != strings.TrimSpace(in.Scope) || in.OperationType != strings.TrimSpace(in.OperationType) || in.IdempotencyKey != strings.TrimSpace(in.IdempotencyKey) || in.OwnerID != strings.TrimSpace(in.OwnerID) {
		return "", ErrInvalid
	}
	if in.Scope == "" || len(in.Scope) > 255 || in.OperationType == "" || len(in.OperationType) > 255 || in.IdempotencyKey == "" || len(in.IdempotencyKey) > 512 || in.OwnerID == "" || len(in.OwnerID) > 255 {
		return "", ErrInvalid
	}
	if in.AttemptID != strings.TrimSpace(in.AttemptID) || in.AttemptIdentity != strings.TrimSpace(in.AttemptIdentity) || len(in.AttemptIdentity) > 512 {
		return "", ErrInvalid
	}
	if (in.AttemptID == "") != (in.AttemptIdentity == "") {
		return "", ErrInvalid
	}
	if in.AttemptID != "" && !validUUID(in.AttemptID) {
		return "", ErrInvalid
	}
	digest := in.RequestDigest
	if digest != strings.TrimSpace(digest) {
		return "", ErrInvalid
	}
	if len(in.Request) > 0 {
		computed, err := RequestDigest(in.Request)
		if err != nil {
			return "", err
		}
		if digest != "" && digest != computed {
			return "", ErrConflict
		}
		digest = computed
	}
	if len(digest) != 71 || !strings.HasPrefix(digest, "sha256:") || digest != strings.ToLower(digest) {
		return "", ErrInvalid
	}
	for _, c := range digest[7:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", ErrInvalid
		}
	}
	return digest, nil
}

func (r *Repository) now() time.Time {
	if r.clock != nil {
		return r.clock().UTC()
	}
	return time.Now().UTC()
}

func (r *Repository) Complete(ctx context.Context, lease Lease, outcome json.RawMessage) error {
	return r.withTx(ctx, func(tx pgx.Tx) error { return r.CompleteTx(ctx, tx, lease, outcome) })
}

func (r *Repository) CompleteTx(ctx context.Context, tx Tx, lease Lease, outcome json.RawMessage) error {
	if r == nil || tx == nil {
		return ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateLease(lease); err != nil {
		return err
	}
	canonical, err := canonicalObjectJSON(outcome)
	if err != nil {
		return err
	}
	now := r.now()
	command, err := tx.Exec(ctx, `UPDATE platform.operation SET state='completed', outcome=$1::jsonb, updated_at=$2, terminal_at=$2, expires_at=$2::timestamptz+retention_interval WHERE scope_id=$3 AND idempotency_key=$4 AND operation_id=$5 AND owner_id=$6 AND fencing_generation=$7 AND state='pending' AND lease_expires_at > $2`, string(canonical), now, lease.Scope, lease.IdempotencyKey, lease.OperationID, lease.OwnerID, lease.FencingGeneration)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 1 {
		return nil
	}
	return r.transitionError(ctx, tx, lease)
}

func (r *Repository) Fail(ctx context.Context, lease Lease, outcome json.RawMessage) error {
	return r.withTx(ctx, func(tx pgx.Tx) error { return r.FailTx(ctx, tx, lease, outcome) })
}

func (r *Repository) FailTx(ctx context.Context, tx Tx, lease Lease, outcome json.RawMessage) error {
	if r == nil || tx == nil {
		return ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateLease(lease); err != nil {
		return err
	}
	canonical, err := canonicalObjectJSON(outcome)
	if err != nil {
		return err
	}
	now := r.now()
	command, err := tx.Exec(ctx, `UPDATE platform.operation SET state='failed', outcome=$1::jsonb, updated_at=$2, terminal_at=$2, expires_at=$2::timestamptz+retention_interval WHERE scope_id=$3 AND idempotency_key=$4 AND operation_id=$5 AND owner_id=$6 AND fencing_generation=$7 AND state='pending' AND lease_expires_at > $2`, string(canonical), now, lease.Scope, lease.IdempotencyKey, lease.OperationID, lease.OwnerID, lease.FencingGeneration)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 1 {
		return nil
	}
	return r.transitionError(ctx, tx, lease)
}

func (r *Repository) MarkIndeterminate(ctx context.Context, lease Lease, evidence json.RawMessage) error {
	return r.withTx(ctx, func(tx pgx.Tx) error { return r.MarkIndeterminateTx(ctx, tx, lease, evidence) })
}

func (r *Repository) MarkIndeterminateTx(ctx context.Context, tx Tx, lease Lease, evidence json.RawMessage) error {
	if r == nil || tx == nil {
		return ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateLease(lease); err != nil {
		return err
	}
	canonicalEvidence, err := canonicalNonEmptyObjectJSON(evidence)
	if err != nil {
		return err
	}
	now := r.now()
	if lease.AttemptID == "" || lease.AttemptIdentity == "" {
		return ErrInvalid
	}
	command, err := tx.Exec(ctx, `UPDATE platform.operation SET state='indeterminate', outcome='{"code":"IDEMPOTENCY_OUTCOME_UNKNOWN","detail":"The original request outcome is indeterminate and requires reconciliation evidence"}'::jsonb, attempt_evidence=$1::jsonb, fencing_generation=fencing_generation+1, updated_at=$2, terminal_at=$2, expires_at=$2::timestamptz+retention_interval WHERE scope_id=$3 AND idempotency_key=$4 AND operation_id=$5 AND owner_id=$6 AND fencing_generation=$7 AND attempt_id=$8 AND attempt_identity=$9 AND state='pending' AND lease_expires_at > $2`, string(canonicalEvidence), now, lease.Scope, lease.IdempotencyKey, lease.OperationID, lease.OwnerID, lease.FencingGeneration, lease.AttemptID, lease.AttemptIdentity)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 1 {
		return nil
	}
	return r.transitionError(ctx, tx, lease)
}

func (r *Repository) RenewLease(ctx context.Context, lease Lease, duration time.Duration) (Lease, error) {
	var result Lease
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		result, err = r.RenewLeaseTx(ctx, tx, lease, duration)
		return err
	})
	return result, err
}

func (r *Repository) RenewLeaseTx(ctx context.Context, tx Tx, lease Lease, duration time.Duration) (Lease, error) {
	if r == nil || tx == nil {
		return Lease{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateLease(lease); err != nil {
		return Lease{}, err
	}
	if duration <= 0 {
		duration = r.lease
	}
	if duration < time.Microsecond || duration > maxLeaseDuration {
		return Lease{}, ErrInvalid
	}
	now := r.now()
	newExpiry := now.Add(duration)
	command, err := tx.Exec(ctx, `UPDATE platform.operation SET lease_expires_at=$1, updated_at=$2 WHERE scope_id=$3 AND idempotency_key=$4 AND operation_id=$5 AND owner_id=$6 AND fencing_generation=$7 AND state='pending' AND lease_expires_at > $2`, newExpiry, now, lease.Scope, lease.IdempotencyKey, lease.OperationID, lease.OwnerID, lease.FencingGeneration)
	if err != nil {
		return Lease{}, err
	}
	if command.RowsAffected() != 1 {
		return Lease{}, r.transitionError(ctx, tx, lease)
	}
	lease.LeaseExpiresAt = newExpiry
	return lease, nil
}

func (r *Repository) BeginAttempt(ctx context.Context, in BeginAttemptInput) (Attempt, error) {
	var result Attempt
	err := r.withTx(ctx, func(tx pgx.Tx) error { var err error; result, err = r.BeginAttemptTx(ctx, tx, in); return err })
	return result, err
}

func (r *Repository) BeginAttemptTx(ctx context.Context, tx Tx, in BeginAttemptInput) (Attempt, error) {
	if r == nil || tx == nil {
		return Attempt{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateLease(in.Lease); err != nil {
		return Attempt{}, err
	}
	var err error
	attemptID := in.AttemptID
	if attemptID != strings.TrimSpace(attemptID) || in.AttemptIdentity != strings.TrimSpace(in.AttemptIdentity) {
		return Attempt{}, ErrInvalid
	}
	if attemptID == "" {
		attemptID, err = newUUID()
		if err != nil {
			return Attempt{}, err
		}
	} else if !validUUID(attemptID) {
		return Attempt{}, ErrInvalid
	}
	identity := in.AttemptIdentity
	if identity == "" || len(identity) > 512 {
		return Attempt{}, ErrInvalid
	}
	command, err := tx.Exec(ctx, `UPDATE platform.operation SET attempt_id=$1, attempt_identity=$2, updated_at=$3 WHERE scope_id=$4 AND idempotency_key=$5 AND operation_id=$6 AND owner_id=$7 AND fencing_generation=$8 AND state='pending' AND lease_expires_at > $3 AND (attempt_id IS NULL OR (attempt_id=$1 AND attempt_identity=$2))`, attemptID, identity, r.now(), in.Lease.Scope, in.Lease.IdempotencyKey, in.Lease.OperationID, in.Lease.OwnerID, in.Lease.FencingGeneration)
	if err != nil {
		return Attempt{}, err
	}
	if command.RowsAffected() != 1 {
		return Attempt{}, r.transitionError(ctx, tx, in.Lease)
	}
	in.Lease.AttemptID, in.Lease.AttemptIdentity = attemptID, identity
	return Attempt{AttemptID: attemptID, AttemptIdentity: identity, Lease: in.Lease}, nil
}

func (r *Repository) ReconcileAttempt(ctx context.Context, in ReconcileAttemptInput) (Operation, error) {
	var result Operation
	err := r.withTx(ctx, func(tx pgx.Tx) error { var err error; result, err = r.ReconcileAttemptTx(ctx, tx, in); return err })
	return result, err
}

func (r *Repository) ReconcileAttemptTx(ctx context.Context, tx Tx, in ReconcileAttemptInput) (Operation, error) {
	if r == nil || tx == nil {
		return Operation{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if in.Scope != strings.TrimSpace(in.Scope) || in.IdempotencyKey != strings.TrimSpace(in.IdempotencyKey) || in.AttemptID != strings.TrimSpace(in.AttemptID) || in.AttemptIdentity != strings.TrimSpace(in.AttemptIdentity) {
		return Operation{}, ErrInvalid
	}
	if in.Scope == "" || len(in.Scope) > 255 || in.IdempotencyKey == "" || len(in.IdempotencyKey) > 512 || in.AttemptID == "" || !validUUID(in.AttemptID) || in.AttemptIdentity == "" || len(in.AttemptIdentity) > 512 {
		return Operation{}, ErrInvalid
	}
	if in.State != StateCompleted && in.State != StateFailed {
		return Operation{}, ErrInvalid
	}
	canonicalOutcome, err := canonicalObjectJSON(in.Outcome)
	if err != nil {
		return Operation{}, err
	}
	canonicalEvidence, err := canonicalNonEmptyObjectJSON(in.Evidence)
	if err != nil {
		return Operation{}, err
	}
	now := r.now()
	// Lock before deciding whether this is the first reconciliation or an
	// idempotent retry. A different outcome/evidence pair is a conflict.
	op, err := scanOperation(tx.QueryRow(ctx, `SELECT scope_id,operation_type,idempotency_key,request_digest,operation_id,state,owner_id,lease_expires_at,fencing_generation,outcome,attempt_id,attempt_identity,attempt_evidence,resolution_evidence,created_at,updated_at,terminal_at,expires_at FROM platform.operation WHERE scope_id=$1 AND idempotency_key=$2 FOR UPDATE`, in.Scope, in.IdempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, err
	}
	if op.AttemptID != in.AttemptID || op.AttemptIdentity != in.AttemptIdentity {
		return Operation{}, ErrConflict
	}
	if op.State != StateIndeterminate {
		storedOutcome, outcomeErr := canonicalObjectJSON(op.Outcome)
		storedEvidence, evidenceErr := canonicalObjectJSON(op.ResolutionEvidence)
		if outcomeErr != nil || evidenceErr != nil {
			return Operation{}, errors.New("persisted reconciliation result is invalid")
		}
		if op.State == in.State && string(storedOutcome) == string(canonicalOutcome) && string(storedEvidence) == string(canonicalEvidence) {
			return op, nil
		}
		return Operation{}, ErrConflict
	}
	_, err = tx.Exec(ctx, `UPDATE platform.operation SET state=$1, outcome=$2::jsonb, attempt_evidence=COALESCE(attempt_evidence,$3::jsonb), resolution_evidence=$3::jsonb, updated_at=$4, terminal_at=$4, expires_at=$4::timestamptz+retention_interval WHERE scope_id=$5 AND idempotency_key=$6 AND attempt_id=$7 AND attempt_identity=$8 AND state='indeterminate'`, string(in.State), string(canonicalOutcome), string(canonicalEvidence), now, in.Scope, in.IdempotencyKey, in.AttemptID, in.AttemptIdentity)
	if err != nil {
		return Operation{}, err
	}
	op, err = scanOperation(tx.QueryRow(ctx, `SELECT scope_id,operation_type,idempotency_key,request_digest,operation_id,state,owner_id,lease_expires_at,fencing_generation,outcome,attempt_id,attempt_identity,attempt_evidence,resolution_evidence,created_at,updated_at,terminal_at,expires_at FROM platform.operation WHERE scope_id=$1 AND idempotency_key=$2`, in.Scope, in.IdempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, err
	}
	return op, nil
}

func (r *Repository) Prune(ctx context.Context, before time.Time, limit int) (int64, error) {
	var count int64
	err := r.withTx(ctx, func(tx pgx.Tx) error { var err error; count, err = r.PruneTx(ctx, tx, before, limit); return err })
	return count, err
}

func (r *Repository) PruneTx(ctx context.Context, tx Tx, before time.Time, limit int) (int64, error) {
	if r == nil || tx == nil {
		return 0, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 || limit > 1000 {
		return 0, ErrInvalid
	}
	if before.IsZero() {
		before = r.now()
	}
	command, err := tx.Exec(ctx, `WITH doomed AS (SELECT scope_id,idempotency_key FROM platform.operation WHERE state IN ('completed','failed') AND expires_at <= $1 ORDER BY expires_at LIMIT $2) DELETE FROM platform.operation o USING doomed d WHERE o.scope_id=d.scope_id AND o.idempotency_key=d.idempotency_key`, before, limit)
	if err != nil {
		return 0, err
	}
	return command.RowsAffected(), nil
}

func (r *Repository) withTx(ctx context.Context, fn func(pgx.Tx) error) error {
	if r == nil || r.db == nil {
		return ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b, ok := r.db.(beginner)
	if !ok {
		return errors.New("operation repository requires a pgx transaction-capable DB")
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) transitionError(ctx context.Context, tx Tx, lease Lease) error {
	var state State
	var owner string
	var generation int64
	var expiry time.Time
	err := tx.QueryRow(ctx, `SELECT state,owner_id,fencing_generation,lease_expires_at FROM platform.operation WHERE scope_id=$1 AND idempotency_key=$2 AND operation_id=$3`, lease.Scope, lease.IdempotencyKey, lease.OperationID).Scan(&state, &owner, &generation, &expiry)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if generation != lease.FencingGeneration || owner != lease.OwnerID {
		return ErrStaleFence
	}
	if state != StatePending {
		return ErrAlreadyTerminal
	}
	if !expiry.After(r.now()) {
		return ErrLeaseExpired
	}
	return ErrConflict
}

func validateLease(lease Lease) error {
	if lease.Scope != strings.TrimSpace(lease.Scope) || lease.IdempotencyKey != strings.TrimSpace(lease.IdempotencyKey) || lease.OperationID != strings.TrimSpace(lease.OperationID) || lease.OwnerID != strings.TrimSpace(lease.OwnerID) {
		return ErrInvalid
	}
	if lease.Scope == "" || len(lease.Scope) > 255 || lease.IdempotencyKey == "" || len(lease.IdempotencyKey) > 512 || !validUUID(lease.OperationID) || lease.OwnerID == "" || len(lease.OwnerID) > 255 || lease.FencingGeneration <= 0 {
		return ErrInvalid
	}
	if lease.AttemptID != strings.TrimSpace(lease.AttemptID) || lease.AttemptIdentity != strings.TrimSpace(lease.AttemptIdentity) || (lease.AttemptID == "") != (lease.AttemptIdentity == "") || len(lease.AttemptIdentity) > 512 || (lease.AttemptID != "" && !validUUID(lease.AttemptID)) {
		return ErrInvalid
	}
	return nil
}

func canonicalObjectJSON(value []byte) ([]byte, error) {
	canonical, err := canonicalJSONBounded(value, 32768)
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(canonical, &object); err != nil || object == nil {
		return nil, ErrInvalid
	}
	if len(canonical) > 32768 {
		return nil, ErrInvalid
	}
	return canonical, nil
}

func canonicalNonEmptyObjectJSON(value []byte) ([]byte, error) {
	if len(value) == 0 {
		return nil, ErrInvalid
	}
	canonical, err := canonicalObjectJSON(value)
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(canonical, &object); err != nil || len(object) == 0 {
		return nil, ErrInvalid
	}
	return canonical, nil
}

func canonicalJSON(value []byte) ([]byte, error) {
	return canonicalJSONBounded(value, 1<<20)
}

func canonicalJSONBounded(value []byte, maxBytes int64) ([]byte, error) {
	var validated json.RawMessage
	if err := strictjson.DecodeWithOptions(value, &validated, strictjson.Options{
		MaxBytes:           maxBytes,
		MaxDepth:           100,
		DuplicateKeys:      strictjson.CaseSensitiveKeys,
		AllowUnknownFields: true,
	}); err != nil {
		return nil, fmt.Errorf("JSON: %w", err)
	}
	var decoded any
	dec := json.NewDecoder(bytes.NewReader(value))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("JSON: %w", err)
	}
	return json.Marshal(decoded)
}

func nullableUUID(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
func nullableText(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	if value != strings.ToLower(value) {
		return false
	}
	for i, c := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]), hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:16])), nil
}

func scanOperation(row pgx.Row) (Operation, error) {
	var op Operation
	var state string
	var outcome string
	var attemptEvidence *string
	var resolutionEvidence *string
	var attemptID, attemptIdentity *string
	var terminalAt *time.Time
	err := row.Scan(&op.Scope, &op.OperationType, &op.IdempotencyKey, &op.RequestDigest, &op.OperationID, &state, &op.OwnerID, &op.LeaseExpiresAt, &op.FencingGeneration, &outcome, &attemptID, &attemptIdentity, &attemptEvidence, &resolutionEvidence, &op.CreatedAt, &op.UpdatedAt, &terminalAt, &op.ExpiresAt)
	if err != nil {
		return Operation{}, err
	}
	op.State = State(state)
	if terminalAt != nil {
		op.TerminalAt = *terminalAt
	}
	if canonical, err := canonicalJSON([]byte(outcome)); err == nil {
		op.Outcome = canonical
	} else {
		op.Outcome = json.RawMessage(outcome)
	}
	if attemptEvidence != nil {
		if canonical, err := canonicalJSON([]byte(*attemptEvidence)); err == nil {
			op.AttemptEvidence = canonical
		} else {
			op.AttemptEvidence = json.RawMessage(*attemptEvidence)
		}
	}
	if resolutionEvidence != nil {
		if canonical, err := canonicalJSON([]byte(*resolutionEvidence)); err == nil {
			op.ResolutionEvidence = canonical
		} else {
			op.ResolutionEvidence = json.RawMessage(*resolutionEvidence)
		}
	}
	if attemptID != nil {
		op.AttemptID = *attemptID
	}
	if attemptIdentity != nil {
		op.AttemptIdentity = *attemptIdentity
	}
	return op, nil
}
