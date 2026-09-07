package postgres

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
)

var _ access.SemanticRegistryReader = (*Repository)(nil)

// ReadSemanticRegistryTx reads the Access-owned registry in a caller-owned
// publication transaction. It deliberately has no begin/commit/rollback path:
// the project identity ledger owns the transaction boundary.
func ReadSemanticRegistryTx(ctx context.Context, tx pgx.Tx, instanceID string) (access.SemanticRegistryContext, error) {
	if ctx == nil {
		return access.SemanticRegistryContext{}, errors.New("semantic registry context is nil")
	}
	if tx == nil {
		return access.SemanticRegistryContext{}, errors.New("semantic registry publication transaction is nil")
	}
	if err := validateControlInstance(instanceID); err != nil {
		return access.SemanticRegistryContext{}, err
	}
	var control access.AuthorizationControlRevision
	if err := tx.QueryRow(ctx, `
		SELECT instance_id,project_id,revision
		FROM access.control_state WHERE instance_id=$1 FOR SHARE`, instanceID).
		Scan(&control.InstanceID, &control.ProjectID, &control.Revision); err != nil {
		return access.SemanticRegistryContext{}, mapControlNotFound(err)
	}
	if err := control.Validate(); err != nil {
		return access.SemanticRegistryContext{}, err
	}
	// Registry mutations lock this singleton FOR UPDATE. FOR SHARE keeps the
	// exact definition/state digest pair stable until publication commits.
	var locked int
	if err := tx.QueryRow(ctx, `
		SELECT 1 FROM access.semantic_attribute_registry WHERE singleton FOR SHARE`).Scan(&locked); err != nil {
		return access.SemanticRegistryContext{}, err
	}
	bound := &Repository{db: tx}
	registry, err := bound.SemanticAttributeRegistry(ctx)
	if err != nil {
		return access.SemanticRegistryContext{}, err
	}
	value := access.SemanticRegistryContext{Control: control, Registry: registry}
	if err := value.Validate(instanceID, projectgraph.ResourceID(control.ProjectID)); err != nil {
		return access.SemanticRegistryContext{}, err
	}
	return value, nil
}

// ReadSemanticRegistry reads control scope and registry definitions from one
// owned repeatable-read snapshot. The existing registry reader verifies its
// stored digest; no hashing or definition authority is introduced here.
func (r *Repository) ReadSemanticRegistry(ctx context.Context, instanceID string) (access.SemanticRegistryContext, error) {
	if ctx == nil {
		return access.SemanticRegistryContext{}, errors.New("semantic registry context is nil")
	}
	if err := validateControlInstance(instanceID); err != nil {
		return access.SemanticRegistryContext{}, err
	}
	db, err := r.requireDB()
	if err != nil {
		return access.SemanticRegistryContext{}, err
	}
	if _, ok := db.(ownedTransaction); ok {
		return access.SemanticRegistryContext{}, errors.New("semantic registry context rejects caller-owned transactions")
	}
	tx, err := r.beginTx(ctx)
	if err != nil {
		return access.SemanticRegistryContext{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY`); err != nil {
		return access.SemanticRegistryContext{}, err
	}
	bound := &Repository{db: tx, fingerprintKey: r.fingerprintKey}
	control, err := bound.ReadAuthorizationControlRevision(ctx, instanceID)
	if err != nil {
		return access.SemanticRegistryContext{}, err
	}
	registry, err := bound.SemanticAttributeRegistry(ctx)
	if err != nil {
		return access.SemanticRegistryContext{}, err
	}
	value := access.SemanticRegistryContext{Control: control, Registry: registry}
	if err := value.Validate(instanceID, projectgraph.ResourceID(control.ProjectID)); err != nil {
		return access.SemanticRegistryContext{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return access.SemanticRegistryContext{}, err
	}
	return value, nil
}
