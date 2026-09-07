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
	"reflect"
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

// MaintenanceDBTX is the native PostgreSQL surface for the separately
// authenticated maintenance pool. It intentionally has the same pgx method
// set as DBTX so bounded pool/transaction implementations can be passed
// without an adapter; the database role remains the enforcement boundary.
// Runtime repositories never retain this value or expose the prune leaf.
type MaintenanceDBTX interface {
	DBTX
}

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// Tx is the complete native transaction contract so mutation methods cannot
// accidentally accept a pool and split one logical unit across connections.
type Tx = pgx.Tx

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

// ExpiredAttemptEvidence is the stable positive evidence recorded when an
// operation lease expires after an external attempt was bound. The external
// commit outcome remains unknown, but the authority can now project a valid
// indeterminate record for reconciliation instead of fabricating completion.
var ExpiredAttemptEvidence = json.RawMessage(`{"code":"IDEMPOTENCY_ATTEMPT_LEASE_EXPIRED","detail":"The operation lease expired after an external attempt was bound"}`)

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

// AppendTerminalInput records a preallocated operation identity as one
// terminal operation. It is used by source capabilities (such as delivery
// approvals) whose immutable evidence IDs are allocated before the operation
// authority is invoked. The operation is still admitted through the same
// guarded pending insert and completed through CompleteTx; callers retain
// transaction ownership and this method never commits or rolls back tx.
type AppendTerminalInput struct {
	OperationID    string
	Scope          string
	OperationType  string
	IdempotencyKey string
	RequestDigest  string
	OwnerID        string
	Outcome        json.RawMessage
}

// AppendTerminalTx inserts (or exactly replays) a preallocated operation and
// atomically settles it to completed. Any immutable identity or outcome
// mismatch returns ErrConflict. The operation ID is intentionally caller
// supplied so append-only evidence can reference it deterministically.
func (r *Repository) AppendTerminalTx(ctx context.Context, tx Tx, in AppendTerminalInput) (Operation, error) {
	if r == nil || tx == nil {
		return Operation{}, ErrInvalid
	}
	operationIDParsed, err := uuid.Parse(strings.TrimSpace(in.OperationID))
	if err != nil || operationIDParsed.String() != in.OperationID {
		return Operation{}, ErrInvalid
	}
	operationID := operationIDParsed.String()
	if in.Scope != strings.TrimSpace(in.Scope) || in.OperationType != strings.TrimSpace(in.OperationType) || in.IdempotencyKey != strings.TrimSpace(in.IdempotencyKey) || in.OwnerID != strings.TrimSpace(in.OwnerID) {
		return Operation{}, ErrInvalid
	}
	if in.Scope == "" || len(in.Scope) > 255 || in.OperationType == "" || len(in.OperationType) > 255 || in.IdempotencyKey == "" || len(in.IdempotencyKey) > 512 || in.OwnerID == "" || len(in.OwnerID) > 255 {
		return Operation{}, ErrInvalid
	}
	if _, err := normalizeInput(&AcquireInput{Scope: in.Scope, OperationType: in.OperationType, IdempotencyKey: in.IdempotencyKey, RequestDigest: in.RequestDigest, OwnerID: in.OwnerID}); err != nil {
		return Operation{}, err
	}
	outcome, err := canonicalObjectJSON(in.Outcome)
	if err != nil {
		return Operation{}, ErrInvalid
	}
	now, err := r.nowTx(ctx, tx)
	if err != nil {
		return Operation{}, err
	}
	lease := r.lease
	if lease <= 0 || lease > maxLeaseDuration {
		lease = 30 * time.Second
	}
	retention := r.retention
	if retention <= 0 || retention > maxRetentionDuration {
		retention = 24 * time.Hour
	}
	_, insertErr := operationdb.New(tx).InsertOperation(ctx, operationdb.InsertOperationParams{
		ScopeID: in.Scope, OperationType: in.OperationType, IdempotencyKey: in.IdempotencyKey,
		RequestDigest: in.RequestDigest, OperationID: uuidParam(operationID), OwnerID: in.OwnerID,
		LeaseExpiresAt: timestampParam(now.Add(lease)), CreatedAt: timestampParam(now),
		RetentionInterval: intervalParam(retention), ExpiresAt: timestampParam(now.Add(retention)),
	})
	if insertErr != nil && !errors.Is(insertErr, pgx.ErrNoRows) {
		return Operation{}, insertErr
	}
	stored, err := operationdb.New(tx).GetOperationForUpdate(ctx, operationdb.GetOperationForUpdateParams{ScopeID: in.Scope, IdempotencyKey: in.IdempotencyKey})
	if errors.Is(err, pgx.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, err
	}
	op, err := operationFromForUpdateRow(stored)
	if err != nil {
		return Operation{}, err
	}
	if op.OperationID != operationID || op.OperationType != in.OperationType || op.RequestDigest != in.RequestDigest || op.OwnerID != in.OwnerID {
		return Operation{}, ErrConflict
	}
	if op.State != StatePending {
		storedOutcome, canonicalErr := canonicalObjectJSON(op.Outcome)
		if op.State == StateCompleted && canonicalErr == nil && bytes.Equal(storedOutcome, outcome) {
			return op, nil
		}
		return Operation{}, ErrConflict
	}
	if err := r.CompleteTx(ctx, tx, Lease{Scope: op.Scope, IdempotencyKey: op.IdempotencyKey, OperationID: op.OperationID, OwnerID: op.OwnerID, FencingGeneration: op.FencingGeneration, LeaseExpiresAt: op.LeaseExpiresAt}, outcome); err != nil {
		return Operation{}, err
	}
	completed, err := operationdb.New(tx).GetOperation(ctx, operationdb.GetOperationParams{ScopeID: in.Scope, IdempotencyKey: in.IdempotencyKey})
	if err != nil {
		return Operation{}, err
	}
	return operationFromRow(completed)
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

// SuccessorAttemptInput admits one append-only execution leaf after an
// indeterminate predecessor. The public operation identity and predecessor
// attempt remain immutable; the leaf carries a fresh executable fence.
type SuccessorAttemptInput struct {
	Predecessor         Lease
	PredecessorID       string
	PredecessorIdentity string
	AttemptID           string
	AttemptIdentity     string
	OwnerID             string
	LeaseExpiresAt      time.Time
}

// SuccessorAttempt is the executable operation leaf. Its lease fields are
// intentionally shaped like Lease so heartbeat and terminal transitions can
// use the same fencing predicates without changing the public operation row.
type SuccessorAttempt struct {
	Scope, IdempotencyKey      string
	OperationID                string
	PredecessorAttemptID       string
	PredecessorAttemptIdentity string
	AttemptID                  string
	AttemptIdentity            string
	OwnerID                    string
	FencingGeneration          int64
	LeaseExpiresAt             time.Time
	State                      State
	AttemptEvidence            json.RawMessage
	ResolutionEvidence         json.RawMessage
	CreatedAt, UpdatedAt       time.Time
	TerminalAt                 time.Time
}

// Repository is safe for concurrent use; state is held in PostgreSQL.
type Repository struct {
	db        DBTX
	lease     time.Duration
	retention time.Duration
	clock     func() time.Time
}

// Maintenance owns destructive retention work. It must be constructed with
// the separately authenticated maintenance DBTX; the database grants deny the
// same SECURITY DEFINER function to the normal runtime role.
type Maintenance struct{ db MaintenanceDBTX }

//go:embed schema.sql
var schemaSQL string

func SchemaSQL() string { return schemaSQL }

// ApplySchema applies the capability schema on a caller-owned transaction.
func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return ErrInvalid
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

// NewMaintenance constructs the bounded operation-retention facade. The
// facade is deliberately separate from Repository so request-serving code has
// no destructive prune method to call accidentally.
func NewMaintenance(db MaintenanceDBTX) *Maintenance { return &Maintenance{db: db} }

// NewMaintenanceRepository is a descriptive alias for callers that name all
// capability adapters as repositories.
func NewMaintenanceRepository(db MaintenanceDBTX) *Maintenance { return NewMaintenance(db) }

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
	if r == nil || !operationDBConfigured(r.db) {
		return AcquireResult{}, ErrInvalid
	}
	b, ok := r.db.(beginner)
	if !ok {
		return AcquireResult{}, errors.New("operation repository requires a pgx transaction-capable DB")
	}
	var result AcquireResult
	err := pgx.BeginFunc(ctx, b, func(tx pgx.Tx) error {
		var err error
		result, err = r.AcquireTx(ctx, tx, in)
		return err
	})
	return result, err
}

// AcquireWithAttempt performs acquisition and binds an external attempt in
// the same PostgreSQL transaction before returning an executable lease. This
// closes the handoff window in which a short lease could expire after
// AcquireTx committed but before the caller persisted its attempt identity.
func (r *Repository) AcquireWithAttempt(ctx context.Context, in AcquireInput, attemptIdentity string) (AcquireResult, error) {
	if r == nil || !operationDBConfigured(r.db) {
		return AcquireResult{}, ErrInvalid
	}
	if attemptIdentity == "" || attemptIdentity != strings.TrimSpace(attemptIdentity) || len(attemptIdentity) > 512 {
		return AcquireResult{}, ErrInvalid
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
			takeoverExpiry := now.Add(lease).UTC().Truncate(time.Microsecond)
			generation, err := operationdb.New(tx).TakeoverOperation(ctx, operationdb.TakeoverOperationParams{
				OwnerID: in.OwnerID, LeaseExpiresAt: timestampParam(takeoverExpiry), UpdatedAt: timestampParam(now),
				ScopeID: in.Scope, IdempotencyKey: in.IdempotencyKey,
			})
			if err == nil {
				op.OwnerID = in.OwnerID
				op.FencingGeneration = generation
				op.LeaseExpiresAt = takeoverExpiry
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
		tag, err := operationdb.New(tx).ExpireOperationAttempt(ctx, operationdb.ExpireOperationAttemptParams{
			AttemptEvidence: ExpiredAttemptEvidence,
			UpdatedAt:       timestampParam(now), ScopeID: in.Scope, IdempotencyKey: in.IdempotencyKey,
			OperationID: uuidParam(op.OperationID), OwnerID: op.OwnerID, FencingGeneration: op.FencingGeneration,
			AttemptID: uuidParam(op.AttemptID), AttemptIdentity: textParam(op.AttemptIdentity),
		})
		if err != nil {
			return AcquireResult{}, err
		}
		if tag.RowsAffected() != 1 {
			return AcquireResult{}, ErrConflict
		}
		persisted, err := operationdb.New(tx).GetOperation(ctx, operationdb.GetOperationParams{ScopeID: in.Scope, IdempotencyKey: in.IdempotencyKey})
		if err != nil {
			return AcquireResult{}, err
		}
		op, err = operationFromRow(persisted)
		if err != nil {
			return AcquireResult{}, err
		}
		return AcquireResult{Status: StatusIndeterminate, Operation: op, Replay: true}, nil
	}
	if op.OwnerID != in.OwnerID {
		return AcquireResult{Status: StatusBusy, Operation: op}, ErrBusy
	}
	return AcquireResult{Status: StatusAcquired, Operation: op, Lease: leaseToken}, nil
}

// Get returns the durable operation record without acquiring its lease.
func (r *Repository) Get(ctx context.Context, scope, idempotencyKey string) (Operation, error) {
	if r == nil || !operationDBConfigured(r.db) {
		return Operation{}, ErrInvalid
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

// GetTxForUpdate returns the durable operation while holding its row lock in
// the caller-owned transaction. Cross-domain recovery uses this read boundary
// to preserve the operation -> target lease -> attempt lock order shared with
// heartbeat renewal; it never commits or rolls back tx.
func (r *Repository) GetTxForUpdate(ctx context.Context, tx Tx, scope, idempotencyKey string) (Operation, error) {
	if r == nil || tx == nil {
		return Operation{}, ErrInvalid
	}
	if scope != strings.TrimSpace(scope) || idempotencyKey != strings.TrimSpace(idempotencyKey) || scope == "" || len(scope) > 255 || idempotencyKey == "" || len(idempotencyKey) > 512 {
		return Operation{}, ErrInvalid
	}
	stored, err := operationdb.New(tx).GetOperationForUpdate(ctx, operationdb.GetOperationForUpdateParams{ScopeID: scope, IdempotencyKey: idempotencyKey})
	if errors.Is(err, pgx.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, err
	}
	return operationFromForUpdateRow(stored)
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
	if err := validateLease(lease); err != nil {
		return err
	}
	if lease.AttemptID != "" {
		if leaf, leafErr := operationdb.New(tx).GetOperationSuccessorAttemptByAttemptForUpdate(ctx, operationdb.GetOperationSuccessorAttemptByAttemptForUpdateParams{OperationID: uuidParam(lease.OperationID), AttemptID: uuidParam(lease.AttemptID)}); leafErr == nil {
			stored, mapErr := successorAttemptFromByAttemptRow(leaf)
			if mapErr != nil {
				return mapErr
			}
			if stored.AttemptIdentity != lease.AttemptIdentity || stored.OwnerID != lease.OwnerID || stored.FencingGeneration != lease.FencingGeneration {
				return ErrStaleFence
			}
			return r.MarkSuccessorIndeterminateTx(ctx, tx, lease, evidence)
		} else if !errors.Is(leafErr, pgx.ErrNoRows) {
			return leafErr
		}
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

// ExpireAttempt settles a bound external attempt after its operation lease
// expired. It owns a short transaction for callers that do not need to
// compose the operation transition with another mutation.
func (r *Repository) ExpireAttempt(ctx context.Context, lease Lease, evidence json.RawMessage) error {
	return r.withTx(ctx, func(tx pgx.Tx) error { return r.ExpireAttemptTx(ctx, tx, lease, evidence) })
}

// ExpireAttemptTx atomically fences a pending operation whose lease expired
// after the exact external attempt was bound. The caller owns tx; this method
// never begins, commits, or rolls it back.
func (r *Repository) ExpireAttemptTx(ctx context.Context, tx Tx, lease Lease, evidence json.RawMessage) error {
	if r == nil || tx == nil {
		return ErrInvalid
	}
	if err := validateLease(lease); err != nil {
		return err
	}
	if lease.AttemptID != "" {
		if leaf, leafErr := operationdb.New(tx).GetOperationSuccessorAttemptByAttemptForUpdate(ctx, operationdb.GetOperationSuccessorAttemptByAttemptForUpdateParams{OperationID: uuidParam(lease.OperationID), AttemptID: uuidParam(lease.AttemptID)}); leafErr == nil {
			stored, mapErr := successorAttemptFromByAttemptRow(leaf)
			if mapErr != nil {
				return mapErr
			}
			if stored.AttemptIdentity != lease.AttemptIdentity || stored.OwnerID != lease.OwnerID || stored.FencingGeneration != lease.FencingGeneration {
				return ErrStaleFence
			}
			return r.ExpireSuccessorAttemptTx(ctx, tx, lease, evidence)
		} else if !errors.Is(leafErr, pgx.ErrNoRows) {
			return leafErr
		}
	}
	if lease.AttemptID == "" || lease.AttemptIdentity == "" {
		return ErrInvalid
	}
	canonicalEvidence, err := canonicalNonEmptyObjectJSON(evidence)
	if err != nil {
		return err
	}
	now, err := r.nowTx(ctx, tx)
	if err != nil {
		return err
	}
	command, err := operationdb.New(tx).ExpireOperationAttempt(ctx, operationdb.ExpireOperationAttemptParams{
		AttemptEvidence: canonicalEvidence,
		UpdatedAt:       timestampParam(now), ScopeID: lease.Scope, IdempotencyKey: lease.IdempotencyKey,
		OperationID: uuidParam(lease.OperationID), OwnerID: lease.OwnerID, FencingGeneration: lease.FencingGeneration,
		AttemptID: uuidParam(lease.AttemptID), AttemptIdentity: textParam(lease.AttemptIdentity),
	})
	if err != nil {
		return err
	}
	if command.RowsAffected() == 1 {
		return nil
	}
	return r.expiredAttemptTransitionError(ctx, tx, lease)
}

// ConfirmExpiredAttemptTx locks and returns the exact indeterminate operation
// produced by direct or expiry fencing. The expected fence must be the
// predecessor lease's generation plus one; callers cannot confirm a later
// takeover or a terminal operation. The caller owns tx and this method does
// not manage its lifecycle.
func (r *Repository) ConfirmExpiredAttemptTx(ctx context.Context, tx Tx, lease Lease, expectedFencingGeneration int64) (Operation, error) {
	if r == nil || tx == nil {
		return Operation{}, ErrInvalid
	}
	if err := validateLease(lease); err != nil {
		return Operation{}, err
	}
	if lease.AttemptID == "" || lease.AttemptIdentity == "" || expectedFencingGeneration <= 0 || lease.FencingGeneration <= 0 || lease.FencingGeneration == 1<<63-1 || expectedFencingGeneration != lease.FencingGeneration+1 {
		return Operation{}, ErrInvalid
	}
	stored, err := operationdb.New(tx).GetExpiredAttemptIndeterminateForUpdate(ctx, operationdb.GetExpiredAttemptIndeterminateForUpdateParams{
		ScopeID: lease.Scope, IdempotencyKey: lease.IdempotencyKey,
		OperationID: uuidParam(lease.OperationID), OwnerID: lease.OwnerID,
		ExpectedFencingGeneration: expectedFencingGeneration,
		AttemptID:                 uuidParam(lease.AttemptID), AttemptIdentity: textParam(lease.AttemptIdentity),
	})
	if err == nil {
		return operationFromExpiredAttemptRow(stored)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Operation{}, err
	}
	return Operation{}, r.confirmExpiredAttemptTransitionError(ctx, tx, lease, expectedFencingGeneration)
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
	if err := validateLease(lease); err != nil {
		return Lease{}, err
	}
	if lease.AttemptID != "" {
		if leaf, leafErr := operationdb.New(tx).GetOperationSuccessorAttemptByAttemptForUpdate(ctx, operationdb.GetOperationSuccessorAttemptByAttemptForUpdateParams{OperationID: uuidParam(lease.OperationID), AttemptID: uuidParam(lease.AttemptID)}); leafErr == nil {
			stored, mapErr := successorAttemptFromByAttemptRow(leaf)
			if mapErr != nil {
				return Lease{}, mapErr
			}
			if stored.AttemptIdentity != lease.AttemptIdentity || stored.OwnerID != lease.OwnerID || stored.FencingGeneration != lease.FencingGeneration {
				return Lease{}, ErrStaleFence
			}
			return r.RenewSuccessorLeaseTx(ctx, tx, lease, duration)
		} else if !errors.Is(leafErr, pgx.ErrNoRows) {
			return Lease{}, leafErr
		}
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
	// PostgreSQL timestamptz and the delivery/DuckLake ledgers are persisted at
	// microsecond precision. Truncate before writing and returning the lease so
	// all four heartbeat ledgers share one exact expiry value.
	newExpiry := now.Add(duration).UTC().Truncate(time.Microsecond)
	command, err := operationdb.New(tx).RenewOperationLease(ctx, operationdb.RenewOperationLeaseParams{
		LeaseExpiresAt: timestampParam(newExpiry), UpdatedAt: timestampParam(now), ScopeID: lease.Scope, IdempotencyKey: lease.IdempotencyKey,
		OperationID: uuidParam(lease.OperationID), OwnerID: lease.OwnerID, FencingGeneration: lease.FencingGeneration,
		AttemptID: uuidParam(lease.AttemptID), AttemptIdentity: textParam(lease.AttemptIdentity),
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

// AdmitSuccessorAttemptTx appends an executable operation leaf while leaving
// the public indeterminate operation row untouched. Replays of the same
// predecessor return the exact stored successor; any identity drift conflicts.
func (r *Repository) AdmitSuccessorAttemptTx(ctx context.Context, tx Tx, in SuccessorAttemptInput) (SuccessorAttempt, error) {
	if r == nil || tx == nil {
		return SuccessorAttempt{}, ErrInvalid
	}
	if err := validateLease(in.Predecessor); err != nil {
		return SuccessorAttempt{}, err
	}
	predecessorID, err := canonicalSuccessorUUID(in.PredecessorID)
	if err != nil || predecessorID != in.PredecessorID {
		return SuccessorAttempt{}, ErrInvalid
	}
	if in.PredecessorIdentity == "" {
		in.PredecessorIdentity = in.Predecessor.AttemptIdentity
	}
	if in.PredecessorIdentity != strings.TrimSpace(in.PredecessorIdentity) || in.PredecessorIdentity == "" || len(in.PredecessorIdentity) > 512 || in.PredecessorIdentity != in.Predecessor.AttemptIdentity {
		return SuccessorAttempt{}, ErrInvalid
	}
	attemptID, err := canonicalSuccessorUUID(in.AttemptID)
	if err != nil || attemptID != in.AttemptID || attemptID == predecessorID {
		return SuccessorAttempt{}, ErrInvalid
	}
	if in.AttemptIdentity == "" || in.AttemptIdentity != strings.TrimSpace(in.AttemptIdentity) || len(in.AttemptIdentity) > 512 || in.OwnerID == "" || in.OwnerID != strings.TrimSpace(in.OwnerID) || len(in.OwnerID) > 255 || in.LeaseExpiresAt.IsZero() || !in.LeaseExpiresAt.Equal(in.LeaseExpiresAt.UTC()) {
		return SuccessorAttempt{}, ErrInvalid
	}
	if in.AttemptIdentity == in.Predecessor.AttemptIdentity {
		return SuccessorAttempt{}, ErrConflict
	}
	if in.Predecessor.AttemptID == "" || in.Predecessor.AttemptIdentity == "" {
		return SuccessorAttempt{}, ErrInvalid
	}
	if in.Predecessor.FencingGeneration == 1<<63-1 {
		return SuccessorAttempt{}, ErrConflict
	}
	now, err := r.nowTx(ctx, tx)
	if err != nil {
		return SuccessorAttempt{}, err
	}
	if !in.LeaseExpiresAt.After(now) || in.LeaseExpiresAt.After(now.Add(maxLeaseDuration)) {
		return SuccessorAttempt{}, ErrInvalid
	}
	public, err := operationdb.New(tx).GetOperationForUpdate(ctx, operationdb.GetOperationForUpdateParams{ScopeID: in.Predecessor.Scope, IdempotencyKey: in.Predecessor.IdempotencyKey})
	if errors.Is(err, pgx.ErrNoRows) {
		return SuccessorAttempt{}, ErrNotFound
	}
	if err != nil {
		return SuccessorAttempt{}, err
	}
	op, err := operationFromForUpdateRow(public)
	if err != nil {
		return SuccessorAttempt{}, err
	}
	if op.OperationID != in.Predecessor.OperationID || op.State != StateIndeterminate {
		return SuccessorAttempt{}, ErrConflict
	}
	if op.AttemptID != predecessorID || op.AttemptIdentity != in.PredecessorIdentity {
		// The predecessor may itself be a prior successor leaf. Lock and verify
		// that immutable chain edge before appending the next leaf.
		row, leafErr := operationdb.New(tx).GetOperationSuccessorAttemptByAttemptForUpdate(ctx, operationdb.GetOperationSuccessorAttemptByAttemptForUpdateParams{OperationID: uuidParam(op.OperationID), AttemptID: uuidParam(predecessorID)})
		if errors.Is(leafErr, pgx.ErrNoRows) {
			return SuccessorAttempt{}, ErrConflict
		}
		if leafErr != nil {
			return SuccessorAttempt{}, leafErr
		}
		leaf, leafErr := successorAttemptFromByAttemptRow(row)
		if leafErr != nil {
			return SuccessorAttempt{}, leafErr
		}
		if leaf.AttemptIdentity != in.PredecessorIdentity || leaf.State != StateIndeterminate || leaf.FencingGeneration != in.Predecessor.FencingGeneration || leaf.OwnerID != in.Predecessor.OwnerID {
			return SuccessorAttempt{}, ErrConflict
		}
	} else if op.AttemptID != predecessorID || op.AttemptIdentity != in.PredecessorIdentity || op.FencingGeneration != in.Predecessor.FencingGeneration || op.OwnerID != in.Predecessor.OwnerID {
		return SuccessorAttempt{}, ErrStaleFence
	}
	if err := validateSuccessorAttemptID(ctx, tx, op, predecessorID, attemptID); err != nil {
		return SuccessorAttempt{}, err
	}
	if in.Predecessor.FencingGeneration >= 1<<63-1 {
		return SuccessorAttempt{}, ErrConflict
	}
	leafFence := in.Predecessor.FencingGeneration + 1
	_, err = operationdb.New(tx).InsertOperationSuccessorAttempt(ctx, operationdb.InsertOperationSuccessorAttemptParams{
		OperationID: uuidParam(op.OperationID), PredecessorAttemptID: uuidParam(predecessorID), PredecessorAttemptIdentity: in.PredecessorIdentity,
		AttemptID: uuidParam(attemptID), AttemptIdentity: in.AttemptIdentity, OwnerID: in.OwnerID, FencingGeneration: leafFence,
		LeaseExpiresAt: timestampParam(in.LeaseExpiresAt.UTC().Truncate(time.Microsecond)), CreatedAt: timestampParam(now),
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		if isUniqueViolation(err) {
			return SuccessorAttempt{}, ErrConflict
		}
		return SuccessorAttempt{}, err
	}
	row, err := operationdb.New(tx).GetOperationSuccessorAttemptForUpdate(ctx, operationdb.GetOperationSuccessorAttemptForUpdateParams{OperationID: uuidParam(op.OperationID), PredecessorAttemptID: uuidParam(predecessorID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return SuccessorAttempt{}, ErrNotFound
	}
	if err != nil {
		return SuccessorAttempt{}, err
	}
	leaf, err := successorAttemptFromRow(row)
	if err != nil {
		return SuccessorAttempt{}, err
	}
	if leaf.AttemptID != attemptID || leaf.AttemptIdentity != in.AttemptIdentity || leaf.OwnerID != in.OwnerID || leaf.FencingGeneration != leafFence || !leaf.LeaseExpiresAt.Equal(in.LeaseExpiresAt.UTC().Truncate(time.Microsecond)) || leaf.PredecessorAttemptIdentity != in.PredecessorIdentity {
		return SuccessorAttempt{}, ErrConflict
	}
	return leaf, nil
}

// CurrentSuccessorAttempt returns the sole pending operation leaf, if any.
func (r *Repository) CurrentSuccessorAttempt(ctx context.Context, operationID string) (SuccessorAttempt, bool, error) {
	if r == nil || !operationDBConfigured(r.db) {
		return SuccessorAttempt{}, false, ErrInvalid
	}
	id, err := canonicalSuccessorUUID(operationID)
	if err != nil || id != operationID {
		return SuccessorAttempt{}, false, ErrInvalid
	}
	row, err := operationdb.New(r.db).GetCurrentOperationSuccessorAttempt(ctx, uuidParam(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return SuccessorAttempt{}, false, nil
	}
	if err != nil {
		return SuccessorAttempt{}, false, err
	}
	leaf, err := successorAttemptFromCurrentRow(row)
	return leaf, err == nil, err
}

// LookupSuccessorAttemptTx locks the public operation row and then the exact
// successor leaf addressed by lease. It is the routing seam for heartbeat and
// terminal transitions: callers never decide based on an out-of-transaction
// "current leaf" read, and terminal leaves remain addressable for exact
// replay. The lock order is public operation -> successor leaf, matching all
// successor admission/reconciliation paths.
func (r *Repository) LookupSuccessorAttemptTx(ctx context.Context, tx Tx, lease Lease) (SuccessorAttempt, bool, error) {
	if r == nil || tx == nil {
		return SuccessorAttempt{}, false, ErrInvalid
	}
	if err := validateLease(lease); err != nil {
		return SuccessorAttempt{}, false, err
	}
	publicRow, err := operationdb.New(tx).GetOperationForUpdate(ctx, operationdb.GetOperationForUpdateParams{ScopeID: lease.Scope, IdempotencyKey: lease.IdempotencyKey})
	if errors.Is(err, pgx.ErrNoRows) {
		return SuccessorAttempt{}, false, ErrNotFound
	}
	if err != nil {
		return SuccessorAttempt{}, false, err
	}
	public, err := operationFromForUpdateRow(publicRow)
	if err != nil {
		return SuccessorAttempt{}, false, err
	}
	if public.OperationID != lease.OperationID {
		return SuccessorAttempt{}, false, ErrConflict
	}
	// The public operation attempt is not a successor. Let callers use the
	// ordinary operation primitives for that immutable root lease.
	if public.AttemptID == lease.AttemptID && public.AttemptIdentity == lease.AttemptIdentity {
		return SuccessorAttempt{}, false, nil
	}
	row, err := operationdb.New(tx).GetOperationSuccessorAttemptByAttemptForUpdate(ctx, operationdb.GetOperationSuccessorAttemptByAttemptForUpdateParams{OperationID: uuidParam(lease.OperationID), AttemptID: uuidParam(lease.AttemptID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return SuccessorAttempt{}, false, nil
	}
	if err != nil {
		return SuccessorAttempt{}, false, err
	}
	leaf, err := successorAttemptFromByAttemptRow(row)
	if err != nil {
		return SuccessorAttempt{}, false, err
	}
	// The exact-leaf query intentionally reads only the append ledger. Restore
	// the public routing identity from the row we already locked so adapters can
	// project a complete lease without a second, unlocked lookup.
	leaf.Scope = public.Scope
	leaf.IdempotencyKey = public.IdempotencyKey
	if leaf.OperationID != lease.OperationID || leaf.AttemptIdentity != lease.AttemptIdentity || leaf.OwnerID != lease.OwnerID || leaf.FencingGeneration != lease.FencingGeneration || !leaf.LeaseExpiresAt.Equal(lease.LeaseExpiresAt.UTC().Truncate(time.Microsecond)) {
		return SuccessorAttempt{}, false, ErrConflict
	}
	return leaf, true, nil
}

func (r *Repository) RenewSuccessorLeaseTx(ctx context.Context, tx Tx, lease Lease, duration time.Duration) (Lease, error) {
	if r == nil || tx == nil {
		return Lease{}, ErrInvalid
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
	expires := now.Add(duration).UTC().Truncate(time.Microsecond)
	command, err := operationdb.New(tx).RenewOperationSuccessorLease(ctx, operationdb.RenewOperationSuccessorLeaseParams{OperationID: uuidParam(lease.OperationID), AttemptID: uuidParam(lease.AttemptID), OwnerID: lease.OwnerID, FencingGeneration: lease.FencingGeneration, LeaseExpiresAt: timestampParam(expires), UpdatedAt: timestampParam(now)})
	if err != nil {
		return Lease{}, err
	}
	if command.RowsAffected() != 1 {
		return Lease{}, ErrConflict
	}
	lease.LeaseExpiresAt = expires
	return lease, nil
}

func (r *Repository) MarkSuccessorIndeterminateTx(ctx context.Context, tx Tx, lease Lease, evidence json.RawMessage) error {
	if r == nil || tx == nil {
		return ErrInvalid
	}
	if err := validateLease(lease); err != nil {
		return err
	}
	canonical, err := canonicalNonEmptyObjectJSON(evidence)
	if err != nil {
		return err
	}
	now, err := r.nowTx(ctx, tx)
	if err != nil {
		return err
	}
	command, err := operationdb.New(tx).MarkOperationSuccessorIndeterminate(ctx, operationdb.MarkOperationSuccessorIndeterminateParams{OperationID: uuidParam(lease.OperationID), AttemptID: uuidParam(lease.AttemptID), OwnerID: lease.OwnerID, FencingGeneration: lease.FencingGeneration, AttemptEvidence: canonical, UpdatedAt: timestampParam(now)})
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (r *Repository) ExpireSuccessorAttemptTx(ctx context.Context, tx Tx, lease Lease, evidence json.RawMessage) error {
	if r == nil || tx == nil {
		return ErrInvalid
	}
	if err := validateLease(lease); err != nil {
		return err
	}
	canonical, err := canonicalNonEmptyObjectJSON(evidence)
	if err != nil {
		return err
	}
	now, err := r.nowTx(ctx, tx)
	if err != nil {
		return err
	}
	command, err := operationdb.New(tx).ExpireOperationSuccessorAttempt(ctx, operationdb.ExpireOperationSuccessorAttemptParams{OperationID: uuidParam(lease.OperationID), AttemptID: uuidParam(lease.AttemptID), OwnerID: lease.OwnerID, FencingGeneration: lease.FencingGeneration, AttemptEvidence: canonical, UpdatedAt: timestampParam(now)})
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (r *Repository) ReconcileSuccessorAttemptTx(ctx context.Context, tx Tx, lease Lease, state State, outcome, evidence json.RawMessage) (Operation, error) {
	if r == nil || tx == nil || (state != StateCompleted && state != StateFailed) {
		return Operation{}, ErrInvalid
	}
	if err := validateLease(lease); err != nil {
		return Operation{}, err
	}
	canonicalOutcome, err := canonicalObjectJSON(outcome)
	if err != nil {
		return Operation{}, err
	}
	canonicalEvidence, err := canonicalNonEmptyObjectJSON(evidence)
	if err != nil {
		return Operation{}, err
	}
	now, err := r.nowTx(ctx, tx)
	if err != nil {
		return Operation{}, err
	}
	// Lock the public idempotency row before any successor leaf. Every
	// operation path follows this order, preventing a concurrent admission or
	// reconciliation from acquiring the inverse leaf→public order.
	public, err := operationdb.New(tx).GetOperationForUpdate(ctx, operationdb.GetOperationForUpdateParams{ScopeID: lease.Scope, IdempotencyKey: lease.IdempotencyKey})
	if errors.Is(err, pgx.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, err
	}
	op, err := operationFromForUpdateRow(public)
	if err != nil {
		return Operation{}, err
	}
	if op.State != StateIndeterminate || op.OperationID != lease.OperationID {
		return Operation{}, ErrConflict
	}
	leafRow, err := operationdb.New(tx).GetOperationSuccessorAttemptByAttemptForUpdate(ctx, operationdb.GetOperationSuccessorAttemptByAttemptForUpdateParams{OperationID: uuidParam(lease.OperationID), AttemptID: uuidParam(lease.AttemptID)})
	if err != nil {
		return Operation{}, err
	}
	leaf, err := successorAttemptFromByAttemptRow(leafRow)
	if err != nil {
		return Operation{}, err
	}
	if leaf.AttemptIdentity != lease.AttemptIdentity || leaf.OwnerID != lease.OwnerID || leaf.FencingGeneration != lease.FencingGeneration || (leaf.State != StatePending && leaf.State != StateIndeterminate) {
		return Operation{}, ErrConflict
	}
	if leaf.State == StatePending && !leaf.LeaseExpiresAt.After(now) {
		return Operation{}, ErrLeaseExpired
	}
	if _, childErr := operationdb.New(tx).GetOperationSuccessorAttemptForUpdate(ctx, operationdb.GetOperationSuccessorAttemptForUpdateParams{OperationID: uuidParam(lease.OperationID), PredecessorAttemptID: uuidParam(leaf.AttemptID)}); childErr == nil {
		return Operation{}, ErrConflict
	} else if !errors.Is(childErr, pgx.ErrNoRows) {
		return Operation{}, childErr
	}
	command, err := operationdb.New(tx).ReconcileOperationSuccessor(ctx, operationdb.ReconcileOperationSuccessorParams{OperationID: uuidParam(lease.OperationID), AttemptID: uuidParam(lease.AttemptID), AttemptIdentity: lease.AttemptIdentity, State: string(state), ResolutionEvidence: canonicalEvidence, UpdatedAt: timestampParam(now)})
	if err != nil {
		return Operation{}, err
	}
	if command.RowsAffected() != 1 {
		return Operation{}, ErrConflict
	}
	// Settle the public idempotency row against its immutable root attempt. The
	// successor evidence is retained on the leaf and in the public resolution
	// document; no predecessor identity is overwritten.
	if err := validateSuccessorChainRoot(ctx, tx, op, leaf); err != nil {
		return Operation{}, err
	}
	command, err = operationdb.New(tx).ReconcileOperationFromSuccessor(ctx, operationdb.ReconcileOperationFromSuccessorParams{
		State:                    string(state),
		Outcome:                  canonicalOutcome,
		Evidence:                 canonicalEvidence,
		UpdatedAt:                timestampParam(now),
		ScopeID:                  op.Scope,
		IdempotencyKey:           op.IdempotencyKey,
		AttemptID:                uuidParam(op.AttemptID),
		AttemptIdentity:          textParam(op.AttemptIdentity),
		SuccessorAttemptID:       uuidParam(leaf.AttemptID),
		SuccessorAttemptIdentity: leaf.AttemptIdentity,
	})
	if err != nil {
		return Operation{}, err
	}
	if command.RowsAffected() != 1 {
		return Operation{}, ErrConflict
	}
	storedAfter, err := operationdb.New(tx).GetOperation(ctx, operationdb.GetOperationParams{ScopeID: op.Scope, IdempotencyKey: op.IdempotencyKey})
	if errors.Is(err, pgx.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, err
	}
	return operationFromRow(storedAfter)
}

func validateSuccessorChainRoot(ctx context.Context, tx Tx, op Operation, leaf SuccessorAttempt) error {
	cursor := leaf.PredecessorAttemptID
	seen := map[string]struct{}{leaf.AttemptID: {}}
	for cursor != op.AttemptID {
		if _, exists := seen[cursor]; exists {
			return ErrConflict
		}
		seen[cursor] = struct{}{}
		row, err := operationdb.New(tx).GetOperationSuccessorAttemptByAttemptForUpdate(ctx, operationdb.GetOperationSuccessorAttemptByAttemptForUpdateParams{OperationID: uuidParam(op.OperationID), AttemptID: uuidParam(cursor)})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		previous, err := successorAttemptFromByAttemptRow(row)
		if err != nil {
			return err
		}
		if previous.State != StateIndeterminate && previous.State != StateCompleted && previous.State != StateFailed {
			return ErrConflict
		}
		cursor = previous.PredecessorAttemptID
	}
	return nil
}

// validateSuccessorAttemptID rejects an attempt UUID already present on the
// public root or any predecessor in the append-only chain.  The public row is
// locked by AdmitSuccessorAttemptTx before this walk; each predecessor leaf
// is then locked in the same public->leaf order used by reconciliation.
func validateSuccessorAttemptID(ctx context.Context, tx Tx, op Operation, predecessorID, attemptID string) error {
	if attemptID == op.AttemptID {
		return ErrConflict
	}
	cursor := predecessorID
	seen := make(map[string]struct{})
	for cursor != op.AttemptID {
		if cursor == attemptID {
			return ErrConflict
		}
		if _, ok := seen[cursor]; ok {
			return ErrConflict
		}
		seen[cursor] = struct{}{}
		row, err := operationdb.New(tx).GetOperationSuccessorAttemptByAttemptForUpdate(ctx, operationdb.GetOperationSuccessorAttemptByAttemptForUpdateParams{
			OperationID: uuidParam(op.OperationID), AttemptID: uuidParam(cursor),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		previous, err := successorAttemptFromByAttemptRow(row)
		if err != nil {
			return err
		}
		cursor = previous.PredecessorAttemptID
	}
	return nil
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
	if op.AttemptID != in.AttemptID {
		if leafRow, leafErr := operationdb.New(tx).GetOperationSuccessorAttemptByAttemptForUpdate(ctx, operationdb.GetOperationSuccessorAttemptByAttemptForUpdateParams{OperationID: uuidParam(op.OperationID), AttemptID: uuidParam(in.AttemptID)}); leafErr == nil {
			leaf, mapErr := successorAttemptFromByAttemptRow(leafRow)
			if mapErr != nil {
				return Operation{}, mapErr
			}
			if leaf.AttemptIdentity != in.AttemptIdentity {
				return Operation{}, ErrConflict
			}
			if op.State != StateIndeterminate {
				// A terminal public row is replayable through a successor only
				// when that exact addressed leaf is the terminal leaf. An older
				// indeterminate predecessor must not be able to replay a newer
				// descendant's result.
				if leaf.State != in.State || (leaf.State != StateCompleted && leaf.State != StateFailed) {
					return Operation{}, ErrConflict
				}
				leafEvidence, leafEvidenceErr := canonicalObjectJSON(leaf.ResolutionEvidence)
				if leafEvidenceErr != nil || string(leafEvidence) != string(canonicalEvidence) {
					return Operation{}, ErrConflict
				}
				if _, childErr := operationdb.New(tx).GetOperationSuccessorAttemptForUpdate(ctx, operationdb.GetOperationSuccessorAttemptForUpdateParams{OperationID: uuidParam(op.OperationID), PredecessorAttemptID: uuidParam(leaf.AttemptID)}); childErr == nil {
					return Operation{}, ErrConflict
				} else if !errors.Is(childErr, pgx.ErrNoRows) {
					return Operation{}, childErr
				}
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
			return r.ReconcileSuccessorAttemptTx(ctx, tx, Lease{Scope: op.Scope, IdempotencyKey: op.IdempotencyKey, OperationID: op.OperationID, OwnerID: leaf.OwnerID, FencingGeneration: leaf.FencingGeneration, LeaseExpiresAt: leaf.LeaseExpiresAt, AttemptID: leaf.AttemptID, AttemptIdentity: leaf.AttemptIdentity}, in.State, in.Outcome, in.Evidence)
		} else if !errors.Is(leafErr, pgx.ErrNoRows) {
			return Operation{}, leafErr
		}
	}
	if op.AttemptID != in.AttemptID || op.AttemptIdentity != in.AttemptIdentity {
		return Operation{}, ErrConflict
	}
	// Once a successor has been admitted, the public root is no longer an
	// executable reconciliation target.  The successor path settles the root
	// through its dedicated ordered update after first terminalizing the leaf;
	// callers addressing the stale root directly must conflict even when the
	// successor is still pending or indeterminate.
	if op.State == StateIndeterminate {
		_, successorErr := operationdb.New(tx).GetOperationSuccessorAttemptForUpdate(ctx, operationdb.GetOperationSuccessorAttemptForUpdateParams{
			OperationID: uuidParam(op.OperationID), PredecessorAttemptID: uuidParam(op.AttemptID),
		})
		if successorErr == nil {
			return Operation{}, ErrConflict
		}
		if !errors.Is(successorErr, pgx.ErrNoRows) {
			return Operation{}, successorErr
		}
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
	command, err := operationdb.New(tx).ReconcileOperation(ctx, operationdb.ReconcileOperationParams{
		State: string(in.State), Outcome: canonicalOutcome, Evidence: canonicalEvidence,
		UpdatedAt: timestampParam(now), ScopeID: in.Scope, IdempotencyKey: in.IdempotencyKey,
		AttemptID: uuidParam(in.AttemptID), AttemptIdentity: textParam(in.AttemptIdentity),
	})
	if err != nil {
		return Operation{}, err
	}
	if command.RowsAffected() != 1 {
		return Operation{}, r.transitionError(ctx, tx, Lease{Scope: in.Scope, IdempotencyKey: in.IdempotencyKey, OperationID: op.OperationID, OwnerID: op.OwnerID, FencingGeneration: op.FencingGeneration, LeaseExpiresAt: op.LeaseExpiresAt, AttemptID: op.AttemptID, AttemptIdentity: op.AttemptIdentity})
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

// Prune removes at most limit expired terminal operations whose database
// expiry is at or before before. Pending and indeterminate records are never
// eligible. Each invocation commits one bounded SECURITY DEFINER batch so a
// retry is idempotent and callers can drain larger backlogs explicitly.
func (m *Maintenance) Prune(ctx context.Context, before time.Time, limit int) (int64, error) {
	var count int64
	err := m.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		count, err = m.PruneTx(ctx, tx, before, limit)
		return err
	})
	return count, err
}

// PruneTx runs one bounded retention batch on a caller-owned transaction. It
// does not commit or roll back, allowing maintenance orchestration to compose
// the prune with its own lease/evidence bookkeeping.
func (m *Maintenance) PruneTx(ctx context.Context, tx Tx, before time.Time, limit int) (int64, error) {
	if m == nil || tx == nil {
		return 0, ErrInvalid
	}
	if limit <= 0 || limit > 1000 {
		return 0, ErrInvalid
	}
	if before.IsZero() {
		now, err := operationdb.New(tx).ClockTimestamp(ctx)
		if err != nil {
			return 0, err
		}
		if !now.Valid {
			return 0, errors.New("postgresql clock timestamp is null")
		}
		before = now.Time
	}
	return operationdb.New(tx).PruneOperations(ctx, operationdb.PruneOperationsParams{PBefore: timestampParam(before), PLimit: int32(limit)})
}

func (m *Maintenance) withTx(ctx context.Context, fn func(pgx.Tx) error) error {
	if m == nil || !operationDBConfigured(m.db) {
		return ErrInvalid
	}
	b, ok := m.db.(beginner)
	if !ok {
		return errors.New("operation maintenance requires a pgx transaction-capable DB")
	}
	return pgx.BeginFunc(ctx, b, fn)
}

func (r *Repository) withTx(ctx context.Context, fn func(pgx.Tx) error) error {
	if r == nil || !operationDBConfigured(r.db) {
		return ErrInvalid
	}
	b, ok := r.db.(beginner)
	if !ok {
		return errors.New("operation repository requires a pgx transaction-capable DB")
	}
	return pgx.BeginFunc(ctx, b, fn)
}

func operationDBConfigured(db any) bool {
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

// expiredAttemptTransitionError preserves the operation authority's precise
// error taxonomy when ExpireAttemptTx's exact attempt predicate matches no
// row. In particular, an attempt mismatch is a conflict rather than a broad
// lease-expired success, while an old owner/fence remains stale.
func (r *Repository) expiredAttemptTransitionError(ctx context.Context, tx Tx, lease Lease) error {
	stored, err := operationdb.New(tx).GetOperationForUpdate(ctx, operationdb.GetOperationForUpdateParams{
		ScopeID: lease.Scope, IdempotencyKey: lease.IdempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	op, err := operationFromForUpdateRow(stored)
	if err != nil {
		return err
	}
	if op.OperationID != lease.OperationID {
		return ErrNotFound
	}
	if op.OwnerID != lease.OwnerID || op.FencingGeneration != lease.FencingGeneration {
		return ErrStaleFence
	}
	if op.State != StatePending {
		return ErrAlreadyTerminal
	}
	if op.AttemptID != lease.AttemptID || op.AttemptIdentity != lease.AttemptIdentity {
		return ErrConflict
	}
	now, err := r.nowTx(ctx, tx)
	if err != nil {
		return err
	}
	if op.LeaseExpiresAt.After(now) {
		return ErrConflict
	}
	return ErrLeaseExpired
}

// confirmExpiredAttemptTransitionError classifies a failed exact confirmation
// without weakening the SELECT predicate. Every mismatch remains observable
// as stale, conflict, not-found, or already-terminal rather than becoming a
// false success for a completed/failed operation.
func (r *Repository) confirmExpiredAttemptTransitionError(ctx context.Context, tx Tx, lease Lease, expectedFencingGeneration int64) error {
	stored, err := operationdb.New(tx).GetOperationForUpdate(ctx, operationdb.GetOperationForUpdateParams{
		ScopeID: lease.Scope, IdempotencyKey: lease.IdempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	op, err := operationFromForUpdateRow(stored)
	if err != nil {
		return err
	}
	if op.OperationID != lease.OperationID {
		return ErrNotFound
	}
	if op.OwnerID != lease.OwnerID || op.FencingGeneration != lease.FencingGeneration && op.FencingGeneration != expectedFencingGeneration {
		return ErrStaleFence
	}
	if op.AttemptID != lease.AttemptID || op.AttemptIdentity != lease.AttemptIdentity {
		return ErrConflict
	}
	if op.State != StateIndeterminate {
		if op.State == StateCompleted || op.State == StateFailed {
			return ErrAlreadyTerminal
		}
		return ErrConflict
	}
	if op.FencingGeneration != expectedFencingGeneration {
		return ErrStaleFence
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

func canonicalSuccessorUUID(value string) (string, error) {
	id, err := uuid.Parse(value)
	if err != nil || id.String() != value || id.Version() != 7 {
		return "", ErrInvalid
	}
	return value, nil
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

func successorAttemptFromRow(row operationdb.GetOperationSuccessorAttemptForUpdateRow) (SuccessorAttempt, error) {
	return successorAttemptValues(row.OperationID, row.PredecessorAttemptID, row.PredecessorAttemptIdentity, row.AttemptID, row.AttemptIdentity, row.OwnerID, row.FencingGeneration, row.LeaseExpiresAt, row.State, row.AttemptEvidence, row.ResolutionEvidence, row.CreatedAt, row.UpdatedAt, row.TerminalAt)
}

func successorAttemptFromByAttemptRow(row operationdb.GetOperationSuccessorAttemptByAttemptForUpdateRow) (SuccessorAttempt, error) {
	return successorAttemptValues(row.OperationID, row.PredecessorAttemptID, row.PredecessorAttemptIdentity, row.AttemptID, row.AttemptIdentity, row.OwnerID, row.FencingGeneration, row.LeaseExpiresAt, row.State, row.AttemptEvidence, row.ResolutionEvidence, row.CreatedAt, row.UpdatedAt, row.TerminalAt)
}

func successorAttemptFromCurrentRow(row operationdb.GetCurrentOperationSuccessorAttemptRow) (SuccessorAttempt, error) {
	leaf, err := successorAttemptValues(row.OperationID, row.PredecessorAttemptID, row.PredecessorAttemptIdentity, row.AttemptID, row.AttemptIdentity, row.OwnerID, row.FencingGeneration, row.LeaseExpiresAt, row.State, row.AttemptEvidence, row.ResolutionEvidence, row.CreatedAt, row.UpdatedAt, row.TerminalAt)
	leaf.Scope, leaf.IdempotencyKey = row.ScopeID, row.IdempotencyKey
	return leaf, err
}

func successorAttemptValues(operationID, predecessorID, predecessorIdentity, attemptID, attemptIdentity, ownerID string, generation int64, expires pgtype.Timestamptz, state string, attemptEvidence, resolutionEvidence []byte, createdAt, updatedAt, terminalAt pgtype.Timestamptz) (SuccessorAttempt, error) {
	if !validUUID(operationID) || !validUUID(predecessorID) || !validUUID(attemptID) || predecessorIdentity == "" || attemptIdentity == "" || ownerID == "" || generation <= 0 || !expires.Valid || expires.Time.IsZero() || !createdAt.Valid || !updatedAt.Valid {
		return SuccessorAttempt{}, ErrInvalid
	}
	if state != string(StatePending) && state != string(StateIndeterminate) && state != string(StateCompleted) && state != string(StateFailed) {
		return SuccessorAttempt{}, ErrInvalid
	}
	leaf := SuccessorAttempt{OperationID: operationID, PredecessorAttemptID: predecessorID, PredecessorAttemptIdentity: predecessorIdentity, AttemptID: attemptID, AttemptIdentity: attemptIdentity, OwnerID: ownerID, FencingGeneration: generation, LeaseExpiresAt: expires.Time, State: State(state), AttemptEvidence: append(json.RawMessage(nil), attemptEvidence...), ResolutionEvidence: append(json.RawMessage(nil), resolutionEvidence...), CreatedAt: createdAt.Time, UpdatedAt: updatedAt.Time}
	if terminalAt.Valid {
		leaf.TerminalAt = terminalAt.Time
	}
	return leaf, nil
}

func operationFromForUpdateRow(row operationdb.GetOperationForUpdateRow) (Operation, error) {
	return operationFromValues(row.ScopeID, row.OperationType, row.IdempotencyKey, row.RequestDigest,
		row.OperationID, row.State, row.OwnerID, row.LeaseExpiresAt, row.FencingGeneration,
		row.Outcome, row.AttemptID, row.AttemptIdentity, row.AttemptEvidence, row.ResolutionEvidence,
		row.CreatedAt, row.UpdatedAt, row.TerminalAt, row.ExpiresAt)
}

func operationFromExpiredAttemptRow(row operationdb.GetExpiredAttemptIndeterminateForUpdateRow) (Operation, error) {
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

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
