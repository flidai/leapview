package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	refreshdb "github.com/flidai/leapview/internal/refresh/postgres/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	ManualIntentWaiting     = "waiting"
	ManualIntentClaimed     = "claimed"
	ManualIntentAttached    = "attached"
	ManualIntentCancelled   = "cancelled"
	ManualIntentStale       = "stale"
	maxAuditIntentBytes     = 64 * 1024
	MaxWaitingManualIntents = 100
)

var ErrManualIntentQueueFull = errors.New("manual refresh intent queue is full")

// ManualIntentInput is the immutable identity of a user-accepted manual
// refresh request. ReservedRunID is chosen at acceptance and only that ID may
// later be attached to the intent.
type ManualIntentInput struct {
	IntentID, ReservedRunID             string
	IdempotencyKey, RequestDigest       string
	ProjectID, Environment, PipelineID  string
	TargetID, PrincipalID, SourceDigest string
	AuditIntentJSON                     json.RawMessage
}

// ManualIntent is durable acceptance and dispatch state. It remains separate
// from Run until the queued job and run are atomically attached.
type ManualIntent struct {
	ManualIntentInput
	Status                    string
	LeaseOwner                string
	FenceGeneration           int64
	LeaseExpiresAt, ClaimedAt time.Time
	AttachedRunID             string
	CreatedAt, UpdatedAt      time.Time
}

// CreateManualIntent inserts one immutable waiting intent. created is false
// for an exact idempotent replay; a reused key with another payload conflicts.
func (r *Repository) CreateManualIntent(ctx context.Context, in ManualIntentInput) (out ManualIntent, created bool, err error) {
	return r.CreateManualIntentWithAudit(ctx, in, nil)
}

// CreateManualIntentWithAudit records a newly accepted intent and optional
// caller-owned audit handoff in the same transaction.
func (r *Repository) CreateManualIntentWithAudit(ctx context.Context, in ManualIntentInput, audit func(context.Context, Tx, ManualIntent) error) (out ManualIntent, created bool, err error) {
	if err = r.requireDB(); err != nil {
		return ManualIntent{}, false, err
	}
	if _, ok := r.db.(pgx.Tx); ok {
		return r.CreateManualIntentTx(ctx, r.db.(pgx.Tx), in, audit)
	}
	err = r.withTx(ctx, func(tx pgx.Tx) error {
		out, created, err = r.CreateManualIntentTx(ctx, tx, in, audit)
		return err
	})
	return out, created, err
}

// CreateManualIntentTx inserts or replays a manual intent in a caller-owned
// transaction, allowing its audit handoff and acceptance event to share the
// same commit.
func (r *Repository) CreateManualIntentTx(ctx context.Context, tx Tx, in ManualIntentInput, audits ...func(context.Context, Tx, ManualIntent) error) (ManualIntent, bool, error) {
	if tx == nil || len(audits) > 1 {
		return ManualIntent{}, false, ErrInvalid
	}
	var err error
	in, err = normalizeManualIntentInput(in)
	if err != nil {
		return ManualIntent{}, false, err
	}
	q := refreshdb.New(tx)
	if _, err := q.AdvisoryLock(ctx, manualIntentScopeLockKey(in.ProjectID, in.Environment)); err != nil {
		return ManualIntent{}, false, err
	}
	key := refreshdb.GetManualIntentByKeyParams{ProjectID: in.ProjectID, Environment: in.Environment, PrincipalID: in.PrincipalID, IdempotencyKey: in.IdempotencyKey}
	if row, readErr := q.GetManualIntentByKey(ctx, key); readErr == nil {
		existing := manualIntentFromDB(row)
		if !sameManualIntentRequest(existing.ManualIntentInput, in) {
			return ManualIntent{}, false, ErrConflict
		}
		return existing, false, nil
	} else if !errors.Is(readErr, pgx.ErrNoRows) {
		return ManualIntent{}, false, readErr
	}
	waitingCount, err := q.CountWaitingManualIntents(ctx, refreshdb.CountWaitingManualIntentsParams{ProjectID: in.ProjectID, Environment: in.Environment})
	if err != nil {
		return ManualIntent{}, false, err
	}
	if waitingCount >= MaxWaitingManualIntents {
		return ManualIntent{}, false, ErrManualIntentQueueFull
	}
	inserted, err := q.InsertManualIntent(ctx, refreshdb.InsertManualIntentParams{
		IntentID: in.IntentID, ReservedRunID: in.ReservedRunID, ProjectID: in.ProjectID,
		Environment: in.Environment, PipelineID: in.PipelineID, TargetID: in.TargetID,
		PrincipalID: in.PrincipalID, SourceDigest: in.SourceDigest, IdempotencyKey: in.IdempotencyKey,
		RequestDigest: in.RequestDigest, AuditIntent: []byte(in.AuditIntentJSON),
	})
	if err == nil {
		out := manualIntentFromDB(inserted)
		if len(audits) == 1 && audits[0] != nil {
			if err := audits[0](ctx, tx, out); err != nil {
				return ManualIntent{}, false, err
			}
		}
		return out, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ManualIntent{}, false, mapManualIntentUniqueError(err)
	}
	// A conflict target means this key has already been accepted. Read the
	// canonical row and prove the request is an exact replay before returning it.
	replayed, err := q.GetManualIntentByKey(ctx, key)
	if err != nil {
		return ManualIntent{}, false, err
	}
	out := manualIntentFromDB(replayed)
	if !sameManualIntentRequest(out.ManualIntentInput, in) {
		return ManualIntent{}, false, ErrConflict
	}
	return out, false, nil
}

// ClaimNextManualIntent returns and fences the oldest waiting request when no
// root run is active in this project/environment. Claims are serialized at the
// scope shared with root run insertion and expire for crash recovery.
func (r *Repository) ClaimNextManualIntent(ctx context.Context, scope Scope, owner string, lease time.Duration) (ManualIntent, bool, error) {
	if err := r.requireDB(); err != nil {
		return ManualIntent{}, false, err
	}
	var out ManualIntent
	var ok bool
	outerErr := r.withTx(ctx, func(tx pgx.Tx) error {
		var claimErr error
		out, ok, claimErr = r.ClaimNextManualIntentTx(ctx, tx, scope, owner, lease)
		return claimErr
	})
	return out, ok, outerErr
}

// ClaimNextManualIntentTx is ClaimNextManualIntent within a caller-owned
// transaction. A false ok result means an active root or live claim owns the
// project/environment, or the scope has no waiting intent.
func (r *Repository) ClaimNextManualIntentTx(ctx context.Context, tx Tx, scope Scope, owner string, lease time.Duration) (ManualIntent, bool, error) {
	if tx == nil || validateScope(scope.ProjectID, scope.Environment) != nil || canonicalID("owner id", owner, 256) != nil || lease < time.Microsecond || lease > MaxLease {
		return ManualIntent{}, false, ErrInvalid
	}
	lockKey := manualIntentScopeLockKey(scope.ProjectID, scope.Environment)
	q := refreshdb.New(tx)
	if _, err := q.AdvisoryLock(ctx, lockKey); err != nil {
		return ManualIntent{}, false, err
	}
	row, err := q.GetNextManualIntentForUpdate(ctx, refreshdb.GetNextManualIntentForUpdateParams{ProjectID: scope.ProjectID, Environment: scope.Environment})
	if errors.Is(err, pgx.ErrNoRows) {
		return ManualIntent{}, false, nil
	}
	if err != nil {
		return ManualIntent{}, false, err
	}
	item := manualIntentFromDB(row)
	dbNow, err := q.DatabaseClock(ctx)
	if err != nil {
		return ManualIntent{}, false, err
	}
	roots, err := q.GetManualIntentActiveRoots(ctx, refreshdb.GetManualIntentActiveRootsParams{ScopeProjectID: scope.ProjectID, ScopeEnvironment: scope.Environment, ReservedRunID: item.ReservedRunID})
	if err != nil {
		return ManualIntent{}, false, err
	}
	if roots.OtherActiveRoot || (roots.ReservedRunActive && item.Status != ManualIntentClaimed) {
		return ManualIntent{}, false, nil
	}
	if item.Status == ManualIntentClaimed && item.LeaseExpiresAt.After(dbNow.UTC()) {
		return ManualIntent{}, false, nil
	}
	claimed, err := q.ClaimManualIntent(ctx, refreshdb.ClaimManualIntentParams{IntentID: item.IntentID, LeaseOwner: owner, LeaseMicros: lease.Microseconds()})
	if errors.Is(err, pgx.ErrNoRows) {
		return ManualIntent{}, false, nil
	}
	if err != nil {
		return ManualIntent{}, false, err
	}
	return manualIntentFromDB(claimed), true, nil
}

// AttachManualIntentTx binds a live claim to its exact reserved root run. The
// run may already have advanced after enqueue; immutable identity and job
// linkage are checked while the lease fence is current.
func (r *Repository) AttachManualIntentTx(ctx context.Context, tx Tx, intentID, owner string, fence int64, runID string) error {
	if tx == nil || canonicalUUIDv7("intent id", intentID) != nil || canonicalID("owner id", owner, 256) != nil || fence <= 0 || canonicalUUIDv7("run id", runID) != nil {
		return ErrInvalid
	}
	rows, err := refreshdb.New(tx).AttachManualIntent(ctx, refreshdb.AttachManualIntentParams{IntentID: intentID, RunID: runID, LeaseOwner: owner, FenceGeneration: fence})
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrStaleFence
	}
	return nil
}

// ReleaseManualIntentTx returns a still-live claim to the FIFO queue. The
// unchanged fence prevents the former claimant from attaching after release.
func (r *Repository) ReleaseManualIntentTx(ctx context.Context, tx Tx, intentID, owner string, fence int64) error {
	return transitionManualIntentClaimTx(ctx, tx, intentID, owner, fence, ManualIntentWaiting)
}

// MarkManualIntentStaleTx marks a claimed intent stale when its pinned source
// digest no longer matches the serving source. Only the live fence may do so.
func (r *Repository) MarkManualIntentStaleTx(ctx context.Context, tx Tx, intentID, owner string, fence int64) error {
	return transitionManualIntentClaimTx(ctx, tx, intentID, owner, fence, ManualIntentStale)
}

func transitionManualIntentClaimTx(ctx context.Context, tx Tx, intentID, owner string, fence int64, status string) error {
	if tx == nil || canonicalUUIDv7("intent id", intentID) != nil || canonicalID("owner id", owner, 256) != nil || fence <= 0 {
		return ErrInvalid
	}
	rows, err := refreshdb.New(tx).TransitionManualIntentClaim(ctx, refreshdb.TransitionManualIntentClaimParams{IntentID: intentID, LeaseOwner: owner, FenceGeneration: fence, Status: status})
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrStaleFence
	}
	return nil
}

// GetManualIntent reads one intent only from the requested project/environment.
func (r *Repository) GetManualIntent(ctx context.Context, scope Scope, intentID string) (ManualIntent, error) {
	if err := r.requireDB(); err != nil {
		return ManualIntent{}, err
	}
	if err := validateScope(scope.ProjectID, scope.Environment); err != nil {
		return ManualIntent{}, err
	}
	if err := canonicalUUIDv7("intent id", intentID); err != nil {
		return ManualIntent{}, err
	}
	row, err := refreshdb.New(r.db).GetManualIntent(ctx, refreshdb.GetManualIntentParams{ProjectID: scope.ProjectID, Environment: scope.Environment, IntentID: intentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ManualIntent{}, ErrNotFound
	}
	if err != nil {
		return ManualIntent{}, err
	}
	return manualIntentFromDB(row), nil
}

// GetManualIntentByIdempotency resolves an accepted request before current
// pipeline/source validation, allowing retries to return their original
// reservation even after authored configuration has changed.
func (r *Repository) GetManualIntentByIdempotency(ctx context.Context, scope Scope, principalID, idempotencyKey string) (ManualIntent, error) {
	if err := r.requireDB(); err != nil {
		return ManualIntent{}, err
	}
	if err := validateScope(scope.ProjectID, scope.Environment); err != nil {
		return ManualIntent{}, err
	}
	if err := canonicalID("principal id", principalID, 255); err != nil {
		return ManualIntent{}, err
	}
	if err := canonicalID("idempotency key", idempotencyKey, 256); err != nil {
		return ManualIntent{}, err
	}
	row, err := refreshdb.New(r.db).GetManualIntentByKey(ctx, refreshdb.GetManualIntentByKeyParams{ProjectID: scope.ProjectID, Environment: scope.Environment, PrincipalID: principalID, IdempotencyKey: idempotencyKey})
	if errors.Is(err, pgx.ErrNoRows) {
		return ManualIntent{}, ErrNotFound
	}
	if err != nil {
		return ManualIntent{}, err
	}
	return manualIntentFromDB(row), nil
}

// ListManualIntents returns the accepted intents in FIFO order. Empty targetID
// lists the whole scope so callers may apply authorization filters in memory.
func (r *Repository) ListManualIntents(ctx context.Context, scope Scope, targetID string, limit int) ([]ManualIntent, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	if err := validateScope(scope.ProjectID, scope.Environment); err != nil {
		return nil, err
	}
	if targetID != "" {
		if err := canonicalID("target id", targetID, 255); err != nil {
			return nil, err
		}
	}
	if limit < 1 || limit > MaxPageSize {
		return nil, fmt.Errorf("%w: manual intent page size must be between 1 and %d", ErrInvalid, MaxPageSize)
	}
	rows, err := refreshdb.New(r.db).ListManualIntents(ctx, refreshdb.ListManualIntentsParams{ProjectID: scope.ProjectID, Environment: scope.Environment, TargetID: targetID, PageLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]ManualIntent, 0, len(rows))
	for _, row := range rows {
		out = append(out, manualIntentFromDB(row))
	}
	return out, nil
}

// ListRecentStaleManualIntents returns a bounded newest-first view of stale
// requests so current queue pages can explain discarded acceptances without
// scanning an unbounded terminal history.
func (r *Repository) ListRecentStaleManualIntents(ctx context.Context, scope Scope, targetID string, limit int) ([]ManualIntent, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	if err := validateScope(scope.ProjectID, scope.Environment); err != nil {
		return nil, err
	}
	if targetID != "" {
		if err := canonicalID("target id", targetID, 255); err != nil {
			return nil, err
		}
	}
	if limit < 1 || limit > MaxPageSize {
		return nil, fmt.Errorf("%w: stale manual intent page size must be between 1 and %d", ErrInvalid, MaxPageSize)
	}
	rows, err := refreshdb.New(r.db).ListRecentStaleManualIntents(ctx, refreshdb.ListRecentStaleManualIntentsParams{ProjectID: scope.ProjectID, Environment: scope.Environment, TargetID: targetID, PageLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]ManualIntent, 0, len(rows))
	for _, row := range rows {
		out = append(out, manualIntentFromDB(row))
	}
	return out, nil
}

// CancelManualIntent atomically cancels a waiting or claimed request within
// project/environment scope. Replaying cancellation returns the terminal row.
func (r *Repository) CancelManualIntent(ctx context.Context, scope Scope, intentID string) (ManualIntent, error) {
	return r.CancelManualIntentWithAudit(ctx, scope, intentID, nil)
}

// CancelManualIntentWithAudit is the transaction-owning cancellation path
// with an optional caller-owned audit handoff.
func (r *Repository) CancelManualIntentWithAudit(ctx context.Context, scope Scope, intentID string, audit func(context.Context, Tx, ManualIntent) error) (ManualIntent, error) {
	if err := r.requireDB(); err != nil {
		return ManualIntent{}, err
	}
	if err := validateScope(scope.ProjectID, scope.Environment); err != nil {
		return ManualIntent{}, err
	}
	if err := canonicalUUIDv7("intent id", intentID); err != nil {
		return ManualIntent{}, err
	}
	var out ManualIntent
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		var e error
		out, e = r.CancelManualIntentTx(ctx, tx, scope, intentID, audit)
		return e
	})
	return out, err
}

// CancelManualIntentTx cancels a waiting or claimed request in the caller's
// transaction. Cancellation fails if the reserved root run already exists;
// callers can attach that run first and then report the committed result.
func (r *Repository) CancelManualIntentTx(ctx context.Context, tx Tx, scope Scope, intentID string, audits ...func(context.Context, Tx, ManualIntent) error) (ManualIntent, error) {
	if tx == nil || validateScope(scope.ProjectID, scope.Environment) != nil || canonicalUUIDv7("intent id", intentID) != nil || len(audits) > 1 {
		return ManualIntent{}, ErrInvalid
	}
	q := refreshdb.New(tx)
	if _, err := q.AdvisoryLock(ctx, manualIntentScopeLockKey(scope.ProjectID, scope.Environment)); err != nil {
		return ManualIntent{}, err
	}
	row, err := q.GetManualIntentForUpdate(ctx, refreshdb.GetManualIntentForUpdateParams{ProjectID: scope.ProjectID, Environment: scope.Environment, IntentID: intentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ManualIntent{}, ErrNotFound
	}
	if err != nil {
		return ManualIntent{}, err
	}
	current := manualIntentFromDB(row)
	if current.Status == ManualIntentCancelled {
		return current, nil
	}
	if current.Status != ManualIntentWaiting && current.Status != ManualIntentClaimed {
		return ManualIntent{}, ErrConflict
	}
	reservedRunExists, err := q.ManualIntentReservedRunExists(ctx, current.ReservedRunID)
	if err != nil {
		return ManualIntent{}, err
	}
	if reservedRunExists {
		return ManualIntent{}, ErrConflict
	}
	rows, err := q.CancelManualIntent(ctx, refreshdb.CancelManualIntentParams{ProjectID: scope.ProjectID, Environment: scope.Environment, IntentID: intentID})
	if err != nil {
		return ManualIntent{}, err
	}
	if rows != 1 {
		return ManualIntent{}, ErrConflict
	}
	row, err = q.GetManualIntent(ctx, refreshdb.GetManualIntentParams{ProjectID: scope.ProjectID, Environment: scope.Environment, IntentID: intentID})
	if err != nil {
		return ManualIntent{}, err
	}
	out := manualIntentFromDB(row)
	if len(audits) == 1 && audits[0] != nil {
		if err := audits[0](ctx, tx, out); err != nil {
			return ManualIntent{}, err
		}
	}
	return out, nil
}

func normalizeManualIntentInput(in ManualIntentInput) (ManualIntentInput, error) {
	if err := validateScope(in.ProjectID, in.Environment); err != nil {
		return ManualIntentInput{}, err
	}
	for label, value := range map[string]string{
		"pipeline id": in.PipelineID, "target id": in.TargetID, "principal id": in.PrincipalID,
		"idempotency key": in.IdempotencyKey,
	} {
		max := 255
		if label == "idempotency key" {
			max = 256
		}
		if err := canonicalID(label, value, max); err != nil {
			return ManualIntentInput{}, err
		}
	}
	if err := platformdigest.ValidateSHA256Identity(in.SourceDigest); err != nil {
		return ManualIntentInput{}, fmt.Errorf("source digest must be canonical sha256: %w", err)
	}
	if err := platformdigest.ValidateSHA256Identity(in.RequestDigest); err != nil {
		return ManualIntentInput{}, fmt.Errorf("request digest must be canonical sha256: %w", err)
	}
	auditJSON, err := boundedObject(in.AuditIntentJSON, maxAuditIntentBytes)
	if err != nil {
		return ManualIntentInput{}, fmt.Errorf("audit intent: %w", err)
	}
	in.AuditIntentJSON = auditJSON
	if in.IntentID == "" {
		in.IntentID, err = NewUUIDv7()
		if err != nil {
			return ManualIntentInput{}, err
		}
	} else if err := canonicalUUIDv7("intent id", in.IntentID); err != nil {
		return ManualIntentInput{}, err
	}
	if in.ReservedRunID == "" {
		in.ReservedRunID, err = NewUUIDv7()
		if err != nil {
			return ManualIntentInput{}, err
		}
	} else if err := canonicalUUIDv7("reserved run id", in.ReservedRunID); err != nil {
		return ManualIntentInput{}, err
	}
	if in.IntentID == in.ReservedRunID {
		return ManualIntentInput{}, fmt.Errorf("%w: intent and reserved run ids must differ", ErrInvalid)
	}
	return in, nil
}

func canonicalUUIDv7(label, value string) error {
	if err := canonicalID(label, value, 36); err != nil {
		return err
	}
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value || parsed.Version() != 7 {
		return fmt.Errorf("%s must be a canonical UUIDv7", label)
	}
	return nil
}

func sameManualIntentRequest(a, b ManualIntentInput) bool {
	return a.ProjectID == b.ProjectID && a.Environment == b.Environment && a.PrincipalID == b.PrincipalID &&
		a.IdempotencyKey == b.IdempotencyKey && a.RequestDigest == b.RequestDigest
}

func manualIntentScopeLockKey(project, environment string) string {
	return fmt.Sprintf("manual-intent-scope:%d:%s|%d:%s", len(project), project, len(environment), environment)
}

func manualIntentFromDB(row refreshdb.RefreshManualIntent) ManualIntent {
	out := ManualIntent{
		ManualIntentInput: ManualIntentInput{
			IntentID: row.IntentID, ReservedRunID: row.ReservedRunID,
			ProjectID: row.ProjectID, Environment: row.Environment, PipelineID: row.PipelineID,
			TargetID: row.TargetID, PrincipalID: row.PrincipalID, SourceDigest: row.SourceDigest,
			IdempotencyKey: row.IdempotencyKey, RequestDigest: row.RequestDigest,
			AuditIntentJSON: append(json.RawMessage(nil), row.AuditIntent...),
		},
		Status: row.Status, LeaseOwner: row.LeaseOwner, FenceGeneration: row.FenceGeneration,
		CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
	if row.LeaseExpiresAt.Valid {
		out.LeaseExpiresAt = row.LeaseExpiresAt.Time.UTC()
	}
	if row.ClaimedAt.Valid {
		out.ClaimedAt = row.ClaimedAt.Time.UTC()
	}
	if row.AttachedRunID != nil {
		out.AttachedRunID = *row.AttachedRunID
	}
	return out
}

func mapManualIntentUniqueError(err error) error {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) && pgErr.SQLState() == "23505" {
		return ErrConflict
	}
	return err
}
