package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
)

// TransferOwnedObjects resolves every live product object owned by a
// principal to targetPrincipalID. The operation is one caller-owned
// transaction, so a read-only authority or a stale concurrent mutation rolls
// back all earlier domain changes.
func (r *Repository) TransferOwnedObjects(ctx context.Context, principalID, targetPrincipalID string) (access.OwnershipReport, error) {
	principalID, err := uuidID("principal id", principalID)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	targetPrincipalID, err = uuidID("target principal id", targetPrincipalID)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	if principalID == targetPrincipalID {
		return access.OwnershipReport{}, errors.New("principal and target principal ids must differ")
	}
	tx, ownTx, err := r.txOrBegin(ctx)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	defer func() {
		if ownTx {
			_ = tx.Rollback(ctx)
		}
	}()
	// Ownership transfer and principal offboarding share this authority lock.
	// Lock source then target after the global authority lock so direct and
	// aggregate ownership adapters use one deterministic principal order. This
	// makes source lifecycle changes and the ownership mutation one serialized
	// operation as well as preventing transfer into a deleting target.
	if _, err := r.lockPlatformAdminAuthority(ctx, tx, principalID); err != nil {
		return access.OwnershipReport{}, err
	}
	if _, err := lockPrincipalLifecycle(ctx, tx, principalID); err != nil {
		return access.OwnershipReport{}, err
	}
	if _, err := r.lockPlatformAdminAuthority(ctx, tx, targetPrincipalID); err != nil {
		return access.OwnershipReport{}, err
	}
	if _, err := lockPrincipalLifecycle(ctx, tx, targetPrincipalID); err != nil {
		return access.OwnershipReport{}, err
	}
	transactional := &Repository{db: tx, fingerprintKey: r.fingerprintKey, ownership: r.ownership}
	target, err := transactional.PrincipalByID(ctx, targetPrincipalID)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	if target.AccessDisabled() {
		return access.OwnershipReport{}, fmt.Errorf("target principal is disabled")
	}
	mutator, err := r.transactionalOwnershipMutator(tx)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	report, err := mutator.TransferOwnedObjects(ctx, principalID, targetPrincipalID)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	if err := r.ensureOwnershipResolved(ctx, tx, principalID); err != nil {
		return access.OwnershipReport{}, err
	}
	if ownTx {
		if err := tx.Commit(ctx); err != nil {
			return access.OwnershipReport{}, err
		}
	}
	return report, nil
}

// TombstoneOwnedObjects retires every live product object with an explicit,
// domain-owned tombstone. Dashboard roots become archived and agent
// conversations receive a retained delete marker; child evidence is never
// physically removed. Semantic attributes intentionally remain a conflict
// until their own lifecycle supplies an explicit transfer/tombstone action.
func (r *Repository) TombstoneOwnedObjects(ctx context.Context, principalID string) (access.OwnershipReport, error) {
	principalID, err := uuidID("principal id", principalID)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	tx, ownTx, err := r.txOrBegin(ctx)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	defer func() {
		if ownTx {
			_ = tx.Rollback(ctx)
		}
	}()
	if _, err := r.lockPlatformAdminAuthority(ctx, tx, principalID); err != nil {
		return access.OwnershipReport{}, err
	}
	if _, err := lockPrincipalLifecycle(ctx, tx, principalID); err != nil {
		return access.OwnershipReport{}, err
	}
	mutator, err := r.transactionalOwnershipMutator(tx)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	report, err := mutator.TombstoneOwnedObjects(ctx, principalID)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	if err := r.ensureOwnershipResolved(ctx, tx, principalID); err != nil {
		return access.OwnershipReport{}, err
	}
	if ownTx {
		if err := tx.Commit(ctx); err != nil {
			return access.OwnershipReport{}, err
		}
	}
	return report, nil
}

func (r *Repository) transactionalOwnershipMutator(tx access.OwnershipDBTX) (access.OwnershipMutator, error) {
	if r == nil || r.ownership == nil {
		return nil, access.ErrOffboardingUnavailable
	}
	if transactional, ok := r.ownership.(access.TransactionalOwnershipMutator); ok {
		return transactional.WithOwnershipMutationDB(tx), nil
	}
	if mutator, ok := r.ownership.(access.OwnershipMutator); ok {
		return mutator, nil
	}
	return nil, errors.New("configured ownership inventory does not support mutations")
}

func (r *Repository) ensureOwnershipResolved(ctx context.Context, tx access.OwnershipDBTX, principalID string) error {
	guard := r.ownership
	if transactional, ok := guard.(access.TransactionalOwnershipGuard); ok {
		guard = transactional.WithOwnershipDB(tx)
	}
	if guard == nil {
		return access.ErrOffboardingUnavailable
	}
	report, err := guard.ListOwnedObjects(ctx, principalID)
	if err != nil {
		return fmt.Errorf("verify ownership resolution: %w", err)
	}
	if len(report.Objects) != 0 {
		return &access.OwnershipConflictError{Report: report}
	}
	return nil
}

// ListOwnedObjects preserves the access-owned semantic-attribute adapter's
// report for callers that inspect this repository directly. Cross-domain
// ownership is composed by the configured OwnershipGuard in Ensure...
func (r *Repository) ListOwnedObjects(ctx context.Context, principalID string) (access.OwnershipReport, error) {
	if r == nil {
		return access.OwnershipReport{}, errors.New("access ownership authority is unavailable")
	}
	return NewSemanticAttributeOwnershipAuthority(r.db).ListOwnedObjects(ctx, principalID)
}

// EnsureOffboardingSafe is the fail-closed lifecycle guard used by principal
// and service-principal deletion.  The check runs on the caller's transaction
// when deletion is audited, so an ownership race cannot commit between the
// inspection and the revocation tombstone.
func (r *Repository) EnsureOffboardingSafe(ctx context.Context, principalID string) error {
	if r == nil || r.db == nil {
		return access.ErrOffboardingUnavailable
	}
	principalID, err := r.lockPlatformAdminAuthority(ctx, r.db, principalID)
	if err != nil {
		return err
	}
	if _, err := lockPrincipalLifecycle(ctx, r.db, principalID); err != nil {
		return err
	}
	guard := r.ownership
	if guard != nil {
		if transactional, ok := guard.(access.TransactionalOwnershipGuard); ok {
			guard = transactional.WithOwnershipDB(r.db)
		}
	}
	if guard == nil {
		guard = r
	}
	report, err := guard.ListOwnedObjects(ctx, principalID)
	if err != nil {
		return fmt.Errorf("check principal ownership before offboarding: %w", err)
	}
	if len(report.Objects) != 0 {
		return &access.OwnershipConflictError{Report: report}
	}
	state, err := r.listPlatformAdministrators(ctx, r.db)
	if err != nil {
		return fmt.Errorf("check platform administrator ownership before offboarding: %w", err)
	}
	for _, administrator := range state.Administrators {
		if administrator.Principal.ID == strings.TrimSpace(principalID) && len(state.Administrators) <= 1 {
			return access.ErrPlatformAdminLastAdmin
		}
	}
	return nil
}

// Ensure the explicit ownership authority is present at compile time.
var _ access.OwnershipAuthority = (*Repository)(nil)
