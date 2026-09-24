package postgres

// A lifecycle frontier belongs to an immutable recovery point. Recording uses
// the producer's transaction; reconciliation reads the surviving producer
// database, not the ledger copy in the restored database. Neither operation
// activates a host or makes provider backup/object-version claims.

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const lifecycleAdvisoryKey int64 = 0x464149393639 // FAI969, serializes action sequence with frontier capture

type LifecycleBoundary struct {
	CustomerID   string
	DeploymentID string
}

type LifecycleFact struct {
	Boundary       LifecycleBoundary
	OccurrenceID   string
	ResourceID     string
	Store          string
	Action         string
	SourceRevision string
	Completed      bool
}

type LifecycleAction struct {
	LifecycleFact
	Sequence   int64
	OccurredAt time.Time
}

// LifecycleFrontier must be captured in the same source database snapshot as
// the recovery point and bound by the caller to its immutable recovery identity.
// The source database must remain available independently of the restored copy.
type LifecycleFrontier struct {
	Boundary          LifecycleBoundary
	RecoveryIdentity  string
	SourceAuthorityID string
	LastSequence      int64
}

// LifecycleEvidence deliberately excludes resource IDs, customer IDs and
// credentials. The recovery identity is an opaque, caller-supplied reference.
type LifecycleEvidence struct {
	RecoveryIdentity string `json:"recovery_identity"`
	Discovered       int    `json:"discovered"`
	Replayed         int    `json:"replayed"`
	AlreadySatisfied int    `json:"already_satisfied"`
	Rejected         int    `json:"rejected"`
	Reconciled       bool   `json:"reconciled"`
}

func lifecycleText(value string, max int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= max && !strings.ContainsAny(value, "\x00\r\n")
}

func lifecycleDigest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	decoded, err := hex.DecodeString(value[7:])
	return err == nil && len(decoded) == 32 && value == strings.ToLower(value)
}

func (b LifecycleBoundary) validate() error {
	if !lifecycleText(b.CustomerID, 255) || !lifecycleText(b.DeploymentID, 255) {
		return errors.New("lifecycle customer/deployment boundary is invalid")
	}
	return nil
}

func (f LifecycleFact) validate() error {
	if err := f.Boundary.validate(); err != nil {
		return err
	}
	if _, err := uuid.Parse(f.OccurrenceID); err != nil {
		return errors.New("lifecycle occurrence identity is invalid")
	}
	if _, err := uuid.Parse(f.ResourceID); err != nil {
		return errors.New("lifecycle resource identity is invalid")
	}
	if !lifecycleText(f.Store, 128) || (f.SourceRevision != "" && !lifecycleDigest(f.SourceRevision)) {
		return errors.New("lifecycle store/source identity is invalid")
	}
	switch f.Action {
	case "delete", "restrict", "revoke", "disable", "supersede":
	default:
		return errors.New("lifecycle action is unsupported")
	}
	return nil
}

func lifecycleReadCommitted(ctx context.Context, tx pgx.Tx) error {
	var isolation string
	// sqlc-exception: analyzer-incompatible. SHOW is transaction protocol state, not a table query.
	if err := tx.QueryRow(ctx, `SHOW transaction_isolation`).Scan(&isolation); err != nil {
		return err
	}
	if isolation != "read committed" {
		return errors.New("lifecycle sequencing requires a read-committed source transaction")
	}
	return nil
}

// RecordLifecycleAction appends a fact in the caller-owned source transaction.
// The caller must perform the matching mutation in that same transaction.
// A repeated occurrence is accepted only when every fact field matches.
func RecordLifecycleAction(ctx context.Context, tx pgx.Tx, fact LifecycleFact) (LifecycleAction, error) {
	if tx == nil || ctx == nil {
		return LifecycleAction{}, errors.New("lifecycle source transaction/context is required")
	}
	if err := fact.validate(); err != nil {
		return LifecycleAction{}, err
	}
	if err := lifecycleReadCommitted(ctx, tx); err != nil {
		return LifecycleAction{}, err
	}
	// sqlc-exception: analyzer-incompatible. Advisory lock orders source commits and frontier capture.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lifecycleAdvisoryKey); err != nil {
		return LifecycleAction{}, err
	}
	// sqlc-exception: analyzer-incompatible. SECURITY DEFINER append is the only runtime write authority.
	_, err := tx.Exec(ctx, `SELECT access.append_lifecycle_action($1::uuid,$2,$3,$4::uuid,$5,$6,$7,$8)`, fact.OccurrenceID, fact.Boundary.CustomerID,
		fact.Boundary.DeploymentID, fact.ResourceID, fact.Store, fact.Action, fact.SourceRevision, fact.Completed)
	if err != nil {
		return LifecycleAction{}, err
	}
	parsedID, err := pgUUID(fact.OccurrenceID)
	if err != nil {
		return LifecycleAction{}, err
	}
	row, err := accessdb.New(tx).LifecycleActionByOccurrence(ctx, parsedID)
	if err != nil {
		return LifecycleAction{}, err
	}
	got := LifecycleAction{LifecycleFact: LifecycleFact{
		Boundary:     LifecycleBoundary{CustomerID: row.CustomerID, DeploymentID: row.DeploymentID},
		OccurrenceID: principalUUID(row.OccurrenceID), ResourceID: principalUUID(row.ResourceID),
		Store: row.Store, Action: row.Action, SourceRevision: row.SourceRevision, Completed: row.Completed,
	}, Sequence: row.Sequence, OccurredAt: row.OccurredAt.Time.UTC()}
	if got.LifecycleFact != fact {
		return LifecycleAction{}, errors.New("lifecycle occurrence conflicts with existing fact")
	}
	return got, nil
}

// CaptureLifecycleFrontier serializes with RecordLifecycleAction. Callers must
// bind the returned sequence/authority to the selected immutable recovery set.
func CaptureLifecycleFrontier(ctx context.Context, tx pgx.Tx, boundary LifecycleBoundary, recoveryIdentity string) (LifecycleFrontier, error) {
	if tx == nil || ctx == nil {
		return LifecycleFrontier{}, errors.New("lifecycle source transaction/context is required")
	}
	if err := boundary.validate(); err != nil {
		return LifecycleFrontier{}, err
	}
	if !lifecycleDigest(recoveryIdentity) {
		return LifecycleFrontier{}, errors.New("recovery identity is required")
	}
	if err := lifecycleReadCommitted(ctx, tx); err != nil {
		return LifecycleFrontier{}, err
	}
	// sqlc-exception: analyzer-incompatible. The same lock serializes capture with action commits.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lifecycleAdvisoryKey); err != nil {
		return LifecycleFrontier{}, err
	}
	f := LifecycleFrontier{Boundary: boundary, RecoveryIdentity: recoveryIdentity}
	q := accessdb.New(tx)
	authorityID, err := q.LifecycleAuthorityID(ctx)
	if err != nil {
		return LifecycleFrontier{}, err
	}
	f.SourceAuthorityID = principalUUID(authorityID)
	f.LastSequence, err = q.LifecycleHighWater(ctx)
	if err != nil {
		return LifecycleFrontier{}, err
	}
	return f, nil
}

// DisablePrincipalWithLifecycle reuses the existing access mutation in the
// same transaction as its completed lifecycle fact. Other access methods are
// intentionally not intercepted without a validated customer boundary.
func (r *Repository) DisablePrincipalWithLifecycle(ctx context.Context, id string, fact LifecycleFact) (access.Principal, error) {
	if fact.Store != "principal" || fact.Action != "disable" || !fact.Completed || fact.ResourceID != id {
		return access.Principal{}, errors.New("principal disable lifecycle fact does not match mutation")
	}
	if err := fact.validate(); err != nil {
		return access.Principal{}, err
	}
	tx, own, err := r.txOrBegin(ctx)
	if err != nil {
		return access.Principal{}, err
	}
	if own {
		defer func() { _ = tx.Rollback(ctx) }()
	}
	inner := &Repository{db: tx, fingerprintKey: r.fingerprintKey}
	principal, err := inner.DisableProvisionedPrincipal(ctx, id)
	if err != nil {
		return access.Principal{}, err
	}
	if _, err := RecordLifecycleAction(ctx, tx, fact); err != nil {
		return access.Principal{}, err
	}
	if own {
		if err := tx.Commit(ctx); err != nil {
			return access.Principal{}, err
		}
	}
	return principal, nil
}

// ReconcileLifecycleActions is a pre-activation gate. The caller owns the
// restored transaction and must commit it only when Reconciled is true. The
// restored boundary must come from the validated recovery admission context,
// independently of the source frontier. Hold the source transaction (and the
// existing recovery fence) through restored commit. The source handle must
// address the surviving authoritative source database, never the restored
// database. No activation or external-store mutation occurs.
func ReconcileLifecycleActions(ctx context.Context, source pgx.Tx, restored pgx.Tx, restoredBoundary LifecycleBoundary, frontier LifecycleFrontier) (evidence LifecycleEvidence, err error) {
	defer func() {
		if err != nil {
			evidence.Rejected = 1
			evidence.Replayed = 0 // caller must roll back; the savepoint also rolls back on replay failure
			evidence.Reconciled = false
		}
	}()
	if source == nil || restored == nil || ctx == nil || frontier.Boundary.validate() != nil ||
		restoredBoundary.validate() != nil || restoredBoundary != frontier.Boundary ||
		!lifecycleDigest(frontier.RecoveryIdentity) || frontier.LastSequence < 0 {
		return evidence, errors.New("lifecycle reconciliation requires a source, restored transaction and bound recovery frontier")
	}
	evidence.RecoveryIdentity = frontier.RecoveryIdentity
	if _, parseErr := uuid.Parse(frontier.SourceAuthorityID); parseErr != nil {
		return evidence, errors.New("lifecycle source authority identity is invalid")
	}
	if err = lifecycleReadCommitted(ctx, source); err != nil {
		return evidence, err
	}
	// sqlc-exception: analyzer-incompatible. Hold the source ordering lock through replay.
	if _, err = source.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lifecycleAdvisoryKey); err != nil {
		return evidence, err
	}
	sourceQ := accessdb.New(source)
	authorityID, authorityErr := sourceQ.LifecycleAuthorityID(ctx)
	if authorityErr != nil {
		return evidence, fmt.Errorf("lifecycle source authority unavailable: %w", authorityErr)
	}
	if principalUUID(authorityID) != frontier.SourceAuthorityID {
		return evidence, errors.New("lifecycle source authority does not match recovery frontier")
	}
	restoredQ := accessdb.New(restored)
	restoredAuthority, authorityErr := restoredQ.LifecycleAuthorityID(ctx)
	if authorityErr != nil {
		return evidence, fmt.Errorf("restored lifecycle authority unavailable: %w", authorityErr)
	}
	if principalUUID(restoredAuthority) != frontier.SourceAuthorityID {
		return evidence, errors.New("restored lifecycle authority does not match recovery frontier")
	}
	sourceHighWater, err := sourceQ.LifecycleHighWater(ctx)
	if err != nil {
		return evidence, err
	}
	if sourceHighWater < frontier.LastSequence {
		return evidence, errors.New("lifecycle source predates recovery frontier")
	}
	rows, err := sourceQ.PostFrontierLifecycleActions(ctx, accessdb.PostFrontierLifecycleActionsParams{
		DeploymentID: frontier.Boundary.DeploymentID, LastSequence: frontier.LastSequence,
	})
	if err != nil {
		return evidence, err
	}
	if len(rows) > 10000 {
		return evidence, errors.New("lifecycle batch exceeds bounded reconciliation limit")
	}
	evidence.Discovered = len(rows)
	// sqlc-exception: analyzer-incompatible. Savepoint makes partial replay uncommittable on rejection.
	if _, err = restored.Exec(ctx, `SAVEPOINT lifecycle_reconcile`); err != nil {
		return evidence, err
	}
	defer func() {
		if err != nil {
			// sqlc-exception: analyzer-incompatible. Roll back the entire replay unit.
			_, _ = restored.Exec(ctx, `ROLLBACK TO SAVEPOINT lifecycle_reconcile`)
		}
		// sqlc-exception: analyzer-incompatible. Release the replay savepoint on either outcome.
		_, _ = restored.Exec(ctx, `RELEASE SAVEPOINT lifecycle_reconcile`)
	}()
	last := frontier.LastSequence
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		a := LifecycleAction{LifecycleFact: LifecycleFact{
			Boundary:     LifecycleBoundary{CustomerID: row.CustomerID, DeploymentID: row.DeploymentID},
			OccurrenceID: principalUUID(row.OccurrenceID), ResourceID: principalUUID(row.ResourceID),
			Store: row.Store, Action: row.Action, SourceRevision: row.SourceRevision, Completed: row.Completed,
		}, Sequence: row.Sequence, OccurredAt: row.OccurredAt.Time.UTC()}
		if a.Sequence <= last || seen[a.OccurrenceID] || a.Boundary != frontier.Boundary ||
			a.OccurredAt.IsZero() || !a.Completed || a.validate() != nil {
			err = errors.New("lifecycle event ordering, identity or completion is invalid")
			return evidence, err
		}
		last = a.Sequence
		seen[a.OccurrenceID] = true
		if a.Store != "principal" || a.Action != "disable" {
			err = errors.New("lifecycle store/action has no qualified PostgreSQL replay")
			return evidence, err
		}
		principalID, parseErr := pgUUID(a.ResourceID)
		if parseErr != nil {
			return evidence, errors.New("lifecycle authoritative principal identity is invalid")
		}
		principal, lockErr := restoredQ.LockLifecyclePrincipal(ctx, principalID)
		if lockErr != nil {
			return evidence, errors.New("lifecycle authoritative principal cannot be resolved")
		}
		if principal.RevokedAt.Valid {
			return evidence, errors.New("lifecycle principal has an ambiguous stronger transition")
		}
		active, activeErr := restoredQ.PrincipalHasActiveCredential(ctx, principalID)
		if activeErr != nil {
			return evidence, activeErr
		}
		if principal.Status == "disabled" && principal.DisabledAt.Valid && !active {
			evidence.AlreadySatisfied++
			continue
		}
		inner := &Repository{db: restored}
		if _, err = inner.DisableProvisionedPrincipal(ctx, a.ResourceID); err != nil {
			return evidence, err
		}
		evidence.Replayed++
	}
	evidence.Reconciled = true
	return evidence, nil
}
