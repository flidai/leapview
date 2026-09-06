package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/access"
)

var _ access.SemanticAttributeResolutionReader = (*Repository)(nil)

type ownedTransaction interface {
	Commit(context.Context) error
	Rollback(context.Context) error
}

// ResolveSemanticAttributes resolves direct principal/group assignments and
// their registry/control metadata in one owned PostgreSQL snapshot. A
// caller-owned transaction is rejected because its isolation and read-only
// mode cannot be established without changing the caller's transaction.
func (r *Repository) ResolveSemanticAttributes(ctx context.Context, subject access.SubjectRef) (access.SemanticAttributeResolution, error) {
	if ctx == nil {
		return access.SemanticAttributeResolution{}, errors.New("semantic attribute resolution context is nil")
	}
	if err := access.ValidateSemanticAttributeSubject(subject); err != nil {
		return access.SemanticAttributeResolution{}, err
	}
	db, err := r.requireDB()
	if err != nil {
		return access.SemanticAttributeResolution{}, err
	}
	if _, ok := db.(ownedTransaction); ok {
		return access.SemanticAttributeResolution{}, errors.New("semantic attribute resolution rejects caller-owned PostgreSQL transactions")
	}

	tx, err := r.beginTx(ctx)
	if err != nil {
		return access.SemanticAttributeResolution{}, fmt.Errorf("begin semantic attribute resolution transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY`); err != nil {
		return access.SemanticAttributeResolution{}, fmt.Errorf("set semantic attribute resolution transaction: %w", err)
	}

	bound := &Repository{db: tx, fingerprintKey: r.fingerprintKey}
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
	// EffectiveDirectSemanticAttributeAssignments canonicalizes the UUID after
	// validating liveness; use the same canonical subject in the result.
	canonicalSubject := subject
	canonicalSubject.ID, err = uuidID("semantic attribute subject id", subject.ID)
	if err != nil {
		return access.SemanticAttributeResolution{}, err
	}
	result := access.SemanticAttributeResolution{
		Subject:      canonicalSubject,
		Registry:     registry,
		ControlState: control.State,
		Attributes:   attributes,
	}
	if err := tx.Commit(ctx); err != nil {
		return access.SemanticAttributeResolution{}, fmt.Errorf("commit semantic attribute resolution transaction: %w", err)
	}
	return result, nil
}
