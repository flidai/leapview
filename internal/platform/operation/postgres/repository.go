// Package postgres implements the operation/idempotency capability on native
// PostgreSQL pgx surfaces. It intentionally does not provide a database/sql
// adapter or hide transaction ownership from callers.
package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	operationdb "github.com/flidai/leapview/internal/platform/operation/postgres/internal/db"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
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
	Scope          string
	OperationType  string
	IdempotencyKey string
	Request        []byte
	RequestDigest  string
	OwnerID        string
	Lease          time.Duration
	Retention      time.Duration
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
	// sqlc-exception:schema-ddl. schema.sql owns the capability DDL, guards,
	// functions, and grants; migration callers retain transaction ownership.
	_, err := tx.Exec(ctx, schemaSQL)
	return err
}

func New(db DBTX) *Repository {
	return &Repository{db: db, lease: 30 * time.Second, retention: 24 * time.Hour}
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

// AcquireWithAttempt performs acquisition and binds an external attempt in
// the same PostgreSQL transaction before returning an executable lease. This
// closes the handoff window in which a short lease could expire after
// AcquireTx committed but before the caller persisted its attempt identity.
func (r *Repository) AcquireWithAttempt(ctx context.Context, in AcquireInput, attemptIdentity string) (AcquireResult, error) {
	if r == nil || r.db == nil {
		return AcquireResult{}, ErrInvalid
	}
	if attemptIdentity == "" || attemptIdentity != strings.TrimSpace(attemptIdentity) || len(attemptIdentity) > 512 {
		return AcquireResult{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var result AcquireResult
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		result, err = r.AcquireTx(ctx, tx, in)
		if err != nil {
			return err
		}
		if result.Status != StatusAcquired || result.Replay || result.Lease.OperationID == "" || result.Lease.AttemptID != "" {
			return nil
		}
		attempt, err := r.BeginAttemptTx(ctx, tx, BeginAttemptInput{
			Lease:           result.Lease,
			AttemptIdentity: attemptIdentity,
		})
		if err != nil {
			return err
		}
		result.Lease = attempt.Lease
		result.Operation.AttemptID = attempt.AttemptID
		result.Operation.AttemptIdentity = attempt.AttemptIdentity
		return nil
	})
	return result, err
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
	now, err := r.nowTx(ctx, tx)
	if err != nil {
		return AcquireResult{}, err
	}
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
	insertedUUID := uuidParam(operationID)
	if !insertedUUID.Valid {
		return AcquireResult{}, ErrInvalid
	}
	_, err = operationdb.New(tx).InsertOperation(ctx, operationdb.InsertOperationParams{
		ScopeID: in.Scope, OperationType: in.OperationType, IdempotencyKey: in.IdempotencyKey,
		RequestDigest: digest, OperationID: insertedUUID, OwnerID: in.OwnerID,
		LeaseExpiresAt: timestampParam(now.Add(lease)), CreatedAt: timestampParam(now),
		RetentionInterval: intervalParam(retention), ExpiresAt: timestampParam(now.Add(retention)),
	})
	if err == nil {
		inserted = true
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return AcquireResult{}, err
	}
	stored, err := operationdb.New(tx).GetOperationForUpdate(ctx, operationdb.GetOperationForUpdateParams{ScopeID: in.Scope, IdempotencyKey: in.IdempotencyKey})
	if err == nil {
		op, err = operationFromForUpdateRow(stored)
	}
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
			generation, err := operationdb.New(tx).TakeoverOperation(ctx, operationdb.TakeoverOperationParams{
				OwnerID: in.OwnerID, LeaseExpiresAt: timestampParam(now.Add(lease)), UpdatedAt: timestampParam(now),
				ScopeID: in.Scope, IdempotencyKey: in.IdempotencyKey,
			})
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
		_, err := operationdb.New(tx).MarkOperationIndeterminate(ctx, operationdb.MarkOperationIndeterminateParams{
			UpdatedAt: timestampParam(now), ScopeID: in.Scope, IdempotencyKey: in.IdempotencyKey,
		})
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
	stored, err := operationdb.New(r.db).GetOperation(ctx, operationdb.GetOperationParams{ScopeID: scope, IdempotencyKey: idempotencyKey})
	var op Operation
	if err == nil {
		op, err = operationFromRow(stored)
	}
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

// nowTx obtains the authoritative database clock for lease and expiry
// decisions. Tests may inject Repository.clock to deterministically advance
// time; production repositories leave it nil and use clock_timestamp() on the
// same PostgreSQL transaction as the state transition.
func (r *Repository) nowTx(ctx context.Context, tx Tx) (time.Time, error) {
	if r != nil && r.clock != nil {
		return r.clock().UTC(), nil
	}
	if tx == nil {
		return time.Time{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now, err := operationdb.New(tx).ClockTimestamp(ctx)
	if err != nil {
		return time.Time{}, err
	}
	if !now.Valid {
		return time.Time{}, errors.New("postgresql clock timestamp is null")
	}
	return now.Time.UTC(), nil
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
	now, err := r.nowTx(ctx, tx)
	if err != nil {
		return err
	}
	command, err := operationdb.New(tx).CompleteOperation(ctx, operationdb.CompleteOperationParams{
		Outcome: canonical, UpdatedAt: timestampParam(now), ScopeID: lease.Scope, IdempotencyKey: lease.IdempotencyKey,
		OperationID: uuidParam(lease.OperationID), OwnerID: lease.OwnerID, FencingGeneration: lease.FencingGeneration,
	})
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
	now, err := r.nowTx(ctx, tx)
	if err != nil {
		return err
	}
	command, err := operationdb.New(tx).FailOperation(ctx, operationdb.FailOperationParams{
		Outcome: canonical, UpdatedAt: timestampParam(now), ScopeID: lease.Scope, IdempotencyKey: lease.IdempotencyKey,
		OperationID: uuidParam(lease.OperationID), OwnerID: lease.OwnerID, FencingGeneration: lease.FencingGeneration,
	})
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
	now, err := r.nowTx(ctx, tx)
	if err != nil {
		return err
	}
	if lease.AttemptID == "" || lease.AttemptIdentity == "" {
		return ErrInvalid
	}
	command, err := operationdb.New(tx).MarkLeaseIndeterminate(ctx, operationdb.MarkLeaseIndeterminateParams{
		AttemptEvidence: canonicalEvidence, UpdatedAt: timestampParam(now), ScopeID: lease.Scope, IdempotencyKey: lease.IdempotencyKey,
		OperationID: uuidParam(lease.OperationID), OwnerID: lease.OwnerID, FencingGeneration: lease.FencingGeneration,
		AttemptID: uuidParam(lease.AttemptID), AttemptIdentity: textParam(lease.AttemptIdentity),
	})
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
	now, err := r.nowTx(ctx, tx)
	if err != nil {
		return Lease{}, err
	}
	newExpiry := now.Add(duration)
	command, err := operationdb.New(tx).RenewOperationLease(ctx, operationdb.RenewOperationLeaseParams{
		LeaseExpiresAt: timestampParam(newExpiry), UpdatedAt: timestampParam(now), ScopeID: lease.Scope, IdempotencyKey: lease.IdempotencyKey,
		OperationID: uuidParam(lease.OperationID), OwnerID: lease.OwnerID, FencingGeneration: lease.FencingGeneration,
	})
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
	now, err := r.nowTx(ctx, tx)
	if err != nil {
		return Attempt{}, err
	}
	command, err := operationdb.New(tx).BindOperationAttempt(ctx, operationdb.BindOperationAttemptParams{
		AttemptID: uuidParam(attemptID), AttemptIdentity: textParam(identity), UpdatedAt: timestampParam(now),
		ScopeID: in.Lease.Scope, IdempotencyKey: in.Lease.IdempotencyKey, OperationID: uuidParam(in.Lease.OperationID),
		OwnerID: in.Lease.OwnerID, FencingGeneration: in.Lease.FencingGeneration,
	})
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
	now, err := r.nowTx(ctx, tx)
	if err != nil {
		return Operation{}, err
	}
	// Lock before deciding whether this is the first reconciliation or an
	// idempotent retry. A different outcome/evidence pair is a conflict.
	stored, err := operationdb.New(tx).GetOperationForUpdate(ctx, operationdb.GetOperationForUpdateParams{ScopeID: in.Scope, IdempotencyKey: in.IdempotencyKey})
	var op Operation
	if err == nil {
		op, err = operationFromForUpdateRow(stored)
	}
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
	_, err = operationdb.New(tx).ReconcileOperation(ctx, operationdb.ReconcileOperationParams{
		State: string(in.State), Outcome: canonicalOutcome, Evidence: canonicalEvidence,
		UpdatedAt: timestampParam(now), ScopeID: in.Scope, IdempotencyKey: in.IdempotencyKey,
		AttemptID: uuidParam(in.AttemptID), AttemptIdentity: textParam(in.AttemptIdentity),
	})
	if err != nil {
		return Operation{}, err
	}
	storedAfter, err := operationdb.New(tx).GetOperation(ctx, operationdb.GetOperationParams{ScopeID: in.Scope, IdempotencyKey: in.IdempotencyKey})
	if err == nil {
		op, err = operationFromRow(storedAfter)
	}
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
		var err error
		before, err = r.nowTx(ctx, tx)
		if err != nil {
			return 0, err
		}
	}
	return operationdb.New(tx).PruneOperations(ctx, operationdb.PruneOperationsParams{PBefore: timestampParam(before), PLimit: int32(limit)})
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
	transition, err := operationdb.New(tx).GetOperationTransitionState(ctx, operationdb.GetOperationTransitionStateParams{ScopeID: lease.Scope, IdempotencyKey: lease.IdempotencyKey, OperationID: uuidParam(lease.OperationID)})
	if err == nil {
		state, owner, generation, expiry = State(transition.State), transition.OwnerID, transition.FencingGeneration, transition.LeaseExpiresAt.Time
	}
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
	now, nowErr := r.nowTx(ctx, tx)
	if nowErr != nil {
		return nowErr
	}
	if !expiry.After(now) {
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
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func operationFromRow(row operationdb.GetOperationRow) (Operation, error) {
	return operationFromValues(row.ScopeID, row.OperationType, row.IdempotencyKey, row.RequestDigest,
		row.OperationID, row.State, row.OwnerID, row.LeaseExpiresAt, row.FencingGeneration,
		row.Outcome, row.AttemptID, row.AttemptIdentity, row.AttemptEvidence, row.ResolutionEvidence,
		row.CreatedAt, row.UpdatedAt, row.TerminalAt, row.ExpiresAt)
}

func operationFromForUpdateRow(row operationdb.GetOperationForUpdateRow) (Operation, error) {
	return operationFromValues(row.ScopeID, row.OperationType, row.IdempotencyKey, row.RequestDigest,
		row.OperationID, row.State, row.OwnerID, row.LeaseExpiresAt, row.FencingGeneration,
		row.Outcome, row.AttemptID, row.AttemptIdentity, row.AttemptEvidence, row.ResolutionEvidence,
		row.CreatedAt, row.UpdatedAt, row.TerminalAt, row.ExpiresAt)
}

func operationFromValues(scope, operationType, key, digest string, operationID pgtype.UUID, state, owner string, leaseExpires pgtype.Timestamptz, generation int64, outcome []byte, attemptID pgtype.UUID, attemptIdentity pgtype.Text, attemptEvidence, resolutionEvidence []byte, createdAt, updatedAt, terminalAt, expiresAt pgtype.Timestamptz) (Operation, error) {
	op := Operation{Scope: scope, OperationType: operationType, IdempotencyKey: key, RequestDigest: digest, State: State(state), OwnerID: owner, FencingGeneration: generation}
	if operationID.Valid {
		op.OperationID = operationID.String()
	}
	if !leaseExpires.Valid || !createdAt.Valid || !updatedAt.Valid || !expiresAt.Valid {
		return Operation{}, errors.New("persisted operation timestamps are invalid")
	}
	op.LeaseExpiresAt, op.CreatedAt, op.UpdatedAt, op.ExpiresAt = leaseExpires.Time, createdAt.Time, updatedAt.Time, expiresAt.Time
	if terminalAt.Valid {
		op.TerminalAt = terminalAt.Time
	}
	if canonical, err := canonicalJSON(outcome); err == nil {
		op.Outcome = canonical
	} else {
		op.Outcome = json.RawMessage(append([]byte(nil), outcome...))
	}
	if attemptID.Valid {
		op.AttemptID = attemptID.String()
	}
	if attemptIdentity.Valid {
		op.AttemptIdentity = attemptIdentity.String
	}
	if len(attemptEvidence) > 0 {
		if canonical, err := canonicalJSON(attemptEvidence); err == nil {
			op.AttemptEvidence = canonical
		} else {
			op.AttemptEvidence = json.RawMessage(append([]byte(nil), attemptEvidence...))
		}
	}
	if len(resolutionEvidence) > 0 {
		if canonical, err := canonicalJSON(resolutionEvidence); err == nil {
			op.ResolutionEvidence = canonical
		} else {
			op.ResolutionEvidence = json.RawMessage(append([]byte(nil), resolutionEvidence...))
		}
	}
	return op, nil
}

func uuidParam(value string) pgtype.UUID {
	var result pgtype.UUID
	if err := result.Scan(value); err != nil {
		return pgtype.UUID{}
	}
	return result
}

func textParam(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func timestampParam(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: !value.IsZero()}
}

func intervalParam(value time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: value.Microseconds(), Valid: true}
}
