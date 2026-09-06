package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/jackc/pgx/v5"
)

var _ access.SemanticAttributeResolutionReader = (*Repository)(nil)

type semanticAttributeOwnedTransaction interface {
	Commit(context.Context) error
	Rollback(context.Context) error
}

type semanticAttributeTransactionBeginner interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

// ResolveSemanticAttributes resolves one principal's direct and active-group
// assignments together with the registry and control snapshots. The method
// deliberately owns the transaction: callers must not be able to supply a
// transaction with weaker isolation or write authority.
func (r *Repository) ResolveSemanticAttributes(ctx context.Context, subject access.SubjectRef) (access.SemanticAttributeResolution, error) {
	if ctx == nil {
		return access.SemanticAttributeResolution{}, errors.New("semantic attribute resolution context is nil")
	}
	if err := access.ValidateSemanticAttributeSubject(subject); err != nil {
		return access.SemanticAttributeResolution{}, err
	}
	canonicalSubjectID, err := uuidID("semantic attribute subject id", subject.ID)
	if err != nil {
		return access.SemanticAttributeResolution{}, err
	}
	subject.ID = canonicalSubjectID
	if subject.Kind != access.SubjectKindPrincipal {
		return access.SemanticAttributeResolution{}, fmt.Errorf("%w: semantic attribute resolution requires a principal subject", access.ErrSemanticAttributeSourceConflict)
	}

	db, err := r.requireDB()
	if err != nil {
		return access.SemanticAttributeResolution{}, err
	}
	// A pgx transaction also implements DBTX. Detect any native transaction
	// shape before beginning so the isolation/read-only contract cannot be
	// silently inherited from the caller's transaction.
	if _, ok := db.(semanticAttributeOwnedTransaction); ok {
		return access.SemanticAttributeResolution{}, errors.New("semantic attribute resolution rejects caller-owned PostgreSQL transactions")
	}

	beginner, ok := db.(semanticAttributeTransactionBeginner)
	if !ok {
		return access.SemanticAttributeResolution{}, errors.New("access PostgreSQL database must support explicit read-only transactions")
	}
	tx, err := beginner.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return access.SemanticAttributeResolution{}, fmt.Errorf("begin semantic attribute resolution transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	bound := &Repository{db: tx, fingerprintKey: r.fingerprintKey}
	if err := requireLiveSemanticAttributePrincipal(ctx, tx, subject.ID); err != nil {
		return access.SemanticAttributeResolution{}, err
	}

	// The generated group query is the same indexed closure used by the
	// effective resolver. Keep the principal first, followed by its active
	// membership rows in query order, and fail closed on any malformed or
	// duplicate authority data.
	groupIDs, err := accessdb.New(tx).ListPrincipalSemanticAttributeGroups(ctx, mustPGUUID(subject.ID))
	if err != nil {
		return access.SemanticAttributeResolution{}, fmt.Errorf("resolve principal semantic attribute groups: %w", err)
	}
	subjects := make([]access.SubjectRef, 0, len(groupIDs)+1)
	subjects = append(subjects, subject)
	seen := map[access.SubjectRef]struct{}{subject: {}}
	for _, groupID := range groupIDs {
		canonicalGroupID, groupErr := uuidID("semantic attribute group id", groupID)
		if groupErr != nil {
			return access.SemanticAttributeResolution{}, fmt.Errorf("%w: active semantic attribute group %q is invalid: %v", access.ErrSemanticAttributeSourceConflict, groupID, groupErr)
		}
		groupSubject := access.SubjectRef{Kind: access.SubjectKindGroup, ID: canonicalGroupID}
		if _, duplicate := seen[groupSubject]; duplicate {
			return access.SemanticAttributeResolution{}, fmt.Errorf("%w: active semantic attribute group %s is repeated", access.ErrSemanticAttributeSourceConflict, canonicalGroupID)
		}
		seen[groupSubject] = struct{}{}
		subjects = append(subjects, groupSubject)
	}

	registry, err := bound.SemanticAttributeRegistry(ctx)
	if err != nil {
		return access.SemanticAttributeResolution{}, err
	}
	control, err := bound.SemanticAttributeControl(ctx)
	if err != nil {
		return access.SemanticAttributeResolution{}, err
	}
	attributes, err := bound.EffectiveDirectSemanticAttributeAssignments(ctx, subject)
	if err != nil {
		return access.SemanticAttributeResolution{}, err
	}
	observedAt := time.Now().UTC()

	result := access.SemanticAttributeResolution{
		Subject:    subject,
		Subjects:   subjects,
		Registry:   registry,
		Control:    control,
		Attributes: attributes,
		ObservedAt: observedAt,
	}
	if err := tx.Commit(ctx); err != nil {
		return access.SemanticAttributeResolution{}, fmt.Errorf("commit semantic attribute resolution transaction: %w", err)
	}
	return result, nil
}

// requireLiveSemanticAttributePrincipal checks liveness inside the owned
// repeatable-read snapshot. GetPrincipal excludes revoked rows; status and
// disabled/blocked timestamps cover the remaining identity lifecycle states.
func requireLiveSemanticAttributePrincipal(ctx context.Context, db DBTX, principalID string) error {
	row, err := accessdb.New(db).GetPrincipal(ctx, mustPGUUID(principalID))
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: semantic attribute principal %s is not live", access.ErrSemanticAttributeSourceConflict, principalID)
	}
	if err != nil {
		return fmt.Errorf("read semantic attribute principal: %w", err)
	}
	if row.Status != "active" || row.DisabledAt.Valid || row.BlockedAt.Valid {
		return fmt.Errorf("%w: semantic attribute principal %s is not live", access.ErrSemanticAttributeSourceConflict, principalID)
	}
	return nil
}
